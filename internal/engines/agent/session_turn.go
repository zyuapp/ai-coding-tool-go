package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What every turn carries, whichever engine answers it: the run that asked, and how it is answered.
// Engines embed *SessionTurn in their own turn type.
type SessionTurn struct {
	Input  *ProviderRunInput
	settle func(result ProviderResult)
	// Lets go of what is held for this turn alone. Runs once, as the turn settles.
	release func()
}

func (turn *SessionTurn) Base() *SessionTurn { return turn }

// A turn an engine builds on SessionTurn, by pointer.
type Turn interface {
	comparable
	Base() *SessionTurn
}

// How an engine takes a turn: what it adds to it, how it starts it, and how it cuts it short.
type TurnEngine[T Turn] interface {
	Open(base *SessionTurn) T
	Begin(turn T)
	Interrupt(turn T)
}

// The one turn a session answers at a time. A cancelled run interrupts the engine rather than
// answering for it, so the engine's own result still ends the turn; an interrupt that goes unanswered
// for the grace period gives the session up instead.
type TurnSlot[T Turn] struct {
	mu        sync.Mutex
	current   T
	has       bool
	announced bool
	grace     time.Duration
	abandon   func()
}

func NewTurnSlot[T Turn](grace time.Duration, abandon func()) *TurnSlot[T] {
	return &TurnSlot[T]{grace: grace, abandon: abandon}
}

// A turn is in flight, so the session owes an answer before it can take another.
func (slot *TurnSlot[T]) Answering() bool {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.has
}

// The turn in flight; false between turns.
func (slot *TurnSlot[T]) Turn() (T, bool) {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	return slot.current, slot.has
}

// Takes the turn a run asked for, and answers it with the result the engine settles on. Blocks until then.
func (slot *TurnSlot[T]) Take(input *ProviderRunInput, engine TurnEngine[T]) ProviderResult {
	results := make(chan ProviderResult, 1)
	var (
		mu          sync.Mutex
		graceTimer  *time.Timer
		stopAbort   func() bool
		releaseOnce sync.Once
		released    bool
	)
	base := &SessionTurn{Input: input, settle: func(result ProviderResult) { results <- result }}
	var turn T
	base.release = func() {
		releaseOnce.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			released = true
			if graceTimer != nil {
				graceTimer.Stop()
			}
			if stopAbort != nil {
				stopAbort()
			}
		})
	}
	turn = engine.Open(base)
	// The turn is the session's before anything is awaited, so a session that ends still answers it.
	slot.mu.Lock()
	slot.current, slot.has = turn, true
	slot.mu.Unlock()
	if input.Aborted() {
		slot.Settle(ProviderResult{Status: domain.RunStatusCancelled})
		return <-results
	}
	if input.Signal != nil {
		stop := context.AfterFunc(input.Signal, func() {
			mu.Lock()
			if released {
				mu.Unlock()
				return
			}
			mu.Unlock()
			engine.Interrupt(turn)
			mu.Lock()
			defer mu.Unlock()
			if released {
				return
			}
			graceTimer = time.AfterFunc(slot.grace, func() {
				slot.Settle(ProviderResult{Status: domain.RunStatusCancelled})
				slot.abandon()
			})
		})
		mu.Lock()
		stopAbort = stop
		mu.Unlock()
	}
	engine.Begin(turn)
	return <-results
}

// Waits on the session's opening for the turn. One that cannot open fails the turn, saying so when the
// continuation itself is gone, and leaves the session for the caller to close.
func (slot *TurnSlot[T]) Opened(turn T, opening func() error, failure func(err error) string) bool {
	err := opening()
	if err == nil {
		return true
	}
	slot.mu.Lock()
	current := slot.has && slot.current == turn
	slot.mu.Unlock()
	if current {
		var open *OpenFailure
		if errors.As(err, &open) && open.Lost {
			turn.Base().Input.Emit(EventContinuationLost{})
		}
		slot.Settle(ProviderResult{Status: domain.RunStatusFailed, Message: failure(err)})
	}
	return false
}

// Reports the session's handle to the turn in flight. Once is enough: the handle never changes.
func (slot *TurnSlot[T]) Announce(continuation domain.Continuation) {
	slot.mu.Lock()
	if slot.announced || !slot.has {
		slot.mu.Unlock()
		return
	}
	slot.announced = true
	turn := slot.current
	slot.mu.Unlock()
	turn.Base().Input.Emit(EventContinuation{Continuation: continuation})
}

// Answers the turn in flight, once. A run the user cancelled is cancelled whatever the engine says.
func (slot *TurnSlot[T]) Settle(result ProviderResult) {
	slot.mu.Lock()
	if !slot.has {
		slot.mu.Unlock()
		return
	}
	turn := slot.current
	var zero T
	slot.current, slot.has = zero, false
	slot.mu.Unlock()
	base := turn.Base()
	base.release()
	if base.Input.Aborted() {
		result = ProviderResult{Status: domain.RunStatusCancelled}
	}
	base.settle(result)
}

// Feeds a run's steered messages into its turn for as long as target finds the turn going. One the
// engine refuses stays queued on the thread, which sends it as the next turn once the run settles.
// reason may be nil.
func DrainSteering[C any](ctx context.Context, input *ProviderRunInput, target func() (C, bool), steer func(into C, prompt string) error, reason func(err error) string) {
	if reason == nil {
		reason = ReasonOf
	}
	for {
		next, ok := input.Steering.Next(ctx)
		if !ok {
			return
		}
		into, going := target()
		if !going {
			return
		}
		if err := steer(into, next.Prompt); err != nil {
			if _, going := target(); !going {
				return
			}
			input.Emit(EventSteerFailed{MessageID: next.MessageID, Message: fmt.Sprintf("Could not steer: %s. Your message is still queued for the next turn.", reason(err))})
			continue
		}
		input.Emit(EventSteered{MessageID: next.MessageID})
	}
}

// What a turn keeps of a compaction going under it.
type Compacting struct {
	Compacting          *bool
	CompactionPreTokens *float64
}

// Says whether the turn is compacting, measured from the context before it began: what a manual
// compaction was told, or what the session last read. A repeat says nothing unless it carries an error.
func ReportCompacting(input *ProviderRunInput, state *Compacting, compacting bool, lastTokens float64, err *string) {
	if compacting && state.CompactionPreTokens == nil {
		pre := lastTokens
		if operation, ok := input.Operation.(contracts.RunOperationCompact); ok {
			pre = operation.PreTokens
		}
		state.CompactionPreTokens = &pre
	}
	if state.Compacting != nil && *state.Compacting == compacting && err == nil {
		return
	}
	state.Compacting = &compacting
	event := EventCompactionStatus{Compacting: compacting}
	if err != nil && *err != "" {
		event.Error = err
	}
	input.Emit(event)
}
