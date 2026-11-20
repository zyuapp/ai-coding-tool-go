package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type testTurn struct {
	*SessionTurn
	begun bool
}

type testEngine struct {
	mu  sync.Mutex
	log []string
}

func (e *testEngine) Open(base *SessionTurn) *testTurn { return &testTurn{SessionTurn: base} }
func (e *testEngine) Begin(turn *testTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	turn.begun = true
	e.log = append(e.log, "begin")
}
func (e *testEngine) Interrupt(*testTurn) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, "interrupt")
}
func (e *testEngine) entries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

func take(slot *TurnSlot[*testTurn], input *ProviderRunInput, engine *testEngine) <-chan ProviderResult {
	result := make(chan ProviderResult, 1)
	go func() { result <- slot.Take(input, engine) }()
	return result
}

func taken(t *testing.T, slot *TurnSlot[*testTurn], engine *testEngine) {
	t.Helper()
	eventually(t, func() bool { return len(engine.entries()) > 0 })
}

func TestTurnSlotHoldsOneTurnAndAnswersItOnce(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	parts := &testEngine{}
	running := take(slot, testInput(nil), parts)
	taken(t, slot, parts)
	equal(t, slot.Answering(), true)
	turn, _ := slot.Turn()
	equal(t, turn.begun, true)
	slot.Settle(ProviderResult{Status: domain.RunStatusSucceeded})
	equal(t, slot.Answering(), false)
	slot.Settle(ProviderResult{Status: domain.RunStatusFailed, Message: "too late"})
	equal(t, <-running, ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestTurnSlotRunAlreadyOverIsCancelledWithoutStarting(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	parts := &testEngine{}
	input := testInput(nil)
	input.Abort()
	equal(t, slot.Take(input, parts), ProviderResult{Status: domain.RunStatusCancelled})
	equal(t, parts.entries(), []string(nil))
	equal(t, slot.Answering(), false)
}

func TestTurnSlotCancelledRunInterruptsAndIsCancelledWhateverEngineSays(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	parts := &testEngine{}
	input := testInput(nil)
	running := take(slot, input, parts)
	taken(t, slot, parts)
	input.Abort()
	eventually(t, func() bool { return len(parts.entries()) == 2 })
	equal(t, parts.entries(), []string{"begin", "interrupt"})
	equal(t, slot.Answering(), true, "the engine's own result still ends the turn")
	slot.Settle(ProviderResult{Status: domain.RunStatusSucceeded})
	equal(t, <-running, ProviderResult{Status: domain.RunStatusCancelled})
}

func TestTurnSlotUnansweredInterruptGivesSessionUp(t *testing.T) {
	var mu sync.Mutex
	abandoned := 0
	abandon := func() { mu.Lock(); abandoned++; mu.Unlock() }
	count := func() int { mu.Lock(); defer mu.Unlock(); return abandoned }
	slot := NewTurnSlot[*testTurn](0, abandon)
	input := testInput(nil)
	lost := take(slot, input, &testEngine{})
	eventually(t, slot.Answering)
	input.Abort()
	equal(t, <-lost, ProviderResult{Status: domain.RunStatusCancelled})
	eventually(t, func() bool { return count() == 1 })

	answered := NewTurnSlot[*testTurn](20*time.Millisecond, abandon)
	answeredInput := testInput(nil)
	parts := &testEngine{}
	running := take(answered, answeredInput, parts)
	taken(t, answered, parts)
	answeredInput.Abort()
	eventually(t, func() bool { return len(parts.entries()) == 2 })
	answered.Settle(ProviderResult{Status: domain.RunStatusCancelled})
	time.Sleep(40 * time.Millisecond)
	equal(t, count(), 1, "the turn came back before the grace period was out")
	equal(t, <-running, ProviderResult{Status: domain.RunStatusCancelled})
}

func TestTurnSlotSettledTurnLetsGoOfTheRun(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	parts := &testEngine{}
	input := testInput(nil)
	running := take(slot, input, parts)
	taken(t, slot, parts)
	slot.Settle(ProviderResult{Status: domain.RunStatusSucceeded})
	input.Abort()
	time.Sleep(5 * time.Millisecond)
	equal(t, parts.entries(), []string{"begin"}, "nothing is left to interrupt")
	equal(t, <-running, ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestTurnSlotSessionThatCannotOpenFailsTheTurn(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	events := &recorder{}
	parts := &testEngine{}
	running := take(slot, testInput(func(input *ProviderRunInput) { input.Emit = events.emit }), parts)
	taken(t, slot, parts)
	turn, _ := slot.Turn()
	opened := slot.Opened(turn, func() error { return &OpenFailure{Message: "gone", Lost: true} }, func(err error) string { return "could not start: " + err.Error() })
	equal(t, opened, false)
	equal(t, events.all(), []ProviderEvent{EventContinuationLost{}})
	equal(t, <-running, ProviderResult{Status: domain.RunStatusFailed, Message: "could not start: gone"})

	other := &recorder{}
	parts = &testEngine{}
	next := take(slot, testInput(func(input *ProviderRunInput) { input.Emit = other.emit }), parts)
	taken(t, slot, parts)
	turn, _ = slot.Turn()
	equal(t, slot.Opened(turn, func() error { return errors.New("no binary") }, func(error) string { return "could not start" }), false)
	equal(t, other.all(), []ProviderEvent(nil), "only a lost continuation is reported as lost")
	equal(t, <-next, ProviderResult{Status: domain.RunStatusFailed, Message: "could not start"})
}

func TestTurnSlotAnnouncesHandleToFirstTurnOnly(t *testing.T) {
	slot := NewTurnSlot[*testTurn](10*time.Second, func() {})
	continuation := domain.Continuation{Provider: "codex", Value: "thread-1"}
	slot.Announce(continuation)
	first := &recorder{}
	parts := &testEngine{}
	take(slot, testInput(func(input *ProviderRunInput) { input.Emit = first.emit }), parts)
	taken(t, slot, parts)
	turn, _ := slot.Turn()
	equal(t, slot.Opened(turn, func() error { return nil }, func(error) string { return "" }), true)
	slot.Announce(continuation)
	slot.Settle(ProviderResult{Status: domain.RunStatusSucceeded})

	second := &recorder{}
	parts = &testEngine{}
	take(slot, testInput(func(input *ProviderRunInput) { input.Emit = second.emit }), parts)
	taken(t, slot, parts)
	slot.Announce(continuation)
	equal(t, first.all(), []ProviderEvent{EventContinuation{Continuation: continuation}})
	equal(t, second.all(), []ProviderEvent(nil))
}

func steeredInput(events *recorder, messages ...SteerMessage) *ProviderRunInput {
	return testInput(func(input *ProviderRunInput) {
		input.Emit = events.emit
		input.Steering = &fixedSteering{queue: messages}
	})
}

func TestDrainSteeringFeedsWhileGoingAndWordsRefusals(t *testing.T) {
	events := &recorder{}
	input := steeredInput(events, SteerMessage{"m1", "first"}, SteerMessage{"m2", "refused"}, SteerMessage{"m3", "late"})
	var sent []string
	going := true
	DrainSteering(context.Background(), input, func() (string, bool) { return "turn", going }, func(into, prompt string) error {
		sent = append(sent, into+":"+prompt)
		if prompt == "refused" {
			return errors.New("busy\nstack")
		}
		going = false
		return nil
	}, nil)
	equal(t, sent, []string{"turn:first"}, "a turn that ended takes no more")
	equal(t, events.all(), []ProviderEvent{EventSteered{MessageID: "m1"}})

	refused := &recorder{}
	DrainSteering(context.Background(), steeredInput(refused, SteerMessage{"m2", "refused"}), func() (string, bool) { return "turn", true }, func(string, string) error {
		return errors.New("busy\nstack")
	}, nil)
	equal(t, refused.all(), []ProviderEvent{EventSteerFailed{MessageID: "m2", Message: "Could not steer: busy. Your message is still queued for the next turn."}})
}

func TestReportCompactingMeasuresFromBeforeAndSaysOnce(t *testing.T) {
	auto := &recorder{}
	input := testInput(func(input *ProviderRunInput) { input.Emit = auto.emit })
	state := &Compacting{}
	ReportCompacting(input, state, true, 1_200, nil)
	ReportCompacting(input, state, true, 5_000, nil)
	ReportCompacting(input, state, false, 5_000, ptr("Could not compact context: offline"))
	equal(t, *state.CompactionPreTokens, 1_200.0)
	equal(t, auto.all(), []ProviderEvent{EventCompactionStatus{Compacting: true}, EventCompactionStatus{Compacting: false, Error: ptr("Could not compact context: offline")}})

	manual := testInput(func(input *ProviderRunInput) {
		input.Emit = (&recorder{}).emit
		input.Operation = contracts.RunOperationCompact{PreTokens: 9_000}
	})
	asked := &Compacting{}
	ReportCompacting(manual, asked, true, 1_200, nil)
	equal(t, *asked.CompactionPreTokens, 9_000.0, "a manual compaction was told what it starts from")
}
