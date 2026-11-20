package agent

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type recorder struct {
	mu     sync.Mutex
	events []ProviderEvent
}

func (r *recorder) emit(event ProviderEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) all() []ProviderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProviderEvent(nil), r.events...)
}

// A queue of steered messages that answers false once empty.
type fixedSteering struct {
	mu    sync.Mutex
	queue []SteerMessage
}

func (s *fixedSteering) Next(context.Context) (SteerMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return SteerMessage{}, false
	}
	next := s.queue[0]
	s.queue = s.queue[1:]
	return next, true
}

// A run input like the TS test support's input(): a claude run on the main channel.
func testInput(change func(input *ProviderRunInput)) *ProviderRunInput {
	ctx, cancel := context.WithCancel(context.Background())
	input := &ProviderRunInput{
		Channel:       contracts.RunChannelMain,
		TaskID:        "task",
		Title:         "Task",
		Prompt:        "Hello",
		WorkspaceRoot: "/tmp/workspace",
		ComputerUse:   domain.ComputerUseRunConfigUnavailable{Message: "off"},
		Policy:        domain.ExecutionPolicyConfirm,
		Engine:        domain.AgentEngineClaude,
		Model:         "sonnet",
		Effort:        domain.AgentEffortHigh,
		Steering:      &fixedSteering{},
		Signal:        ctx,
		Abort:         cancel,
		Authorize:     func(context.Context, domain.ToolIntent) ToolDecision { return Deny },
		Emit:          func(ProviderEvent) {},
	}
	if change != nil {
		change(input)
	}
	return input
}

func equal(t *testing.T, got, want any, message ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v\n got: %#v\nwant: %#v", message, got, want)
	}
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		time.Sleep(time.Millisecond)
	}
}

func ptr[T any](value T) *T { return &value }
