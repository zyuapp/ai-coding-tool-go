package host

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type fakeRuntime struct {
	mu        sync.Mutex
	state     *application.WorkspaceState
	listeners map[int]func()
	next      int
	refused   contracts.WorkspaceCommandResultFalse
	flushed   chan struct{}
}

func (f *fakeRuntime) GetState() *application.WorkspaceState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

func (f *fakeRuntime) Subscribe(listener func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next
	f.next++
	f.listeners[id] = listener
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.listeners, id)
	}
}

func (f *fakeRuntime) Execute(application.WorkspaceInput) application.WorkspaceExecution {
	result := settledFuture[contracts.WorkspaceCommandResult](f.refused, nil)
	return execution(result, result)
}

func (f *fakeRuntime) Flush(ctx context.Context) error {
	select {
	case <-f.flushed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeRuntime) set(change func(state *application.WorkspaceState)) {
	f.mu.Lock()
	next := *f.state
	change(&next)
	f.state = &next
	listeners := make([]func(), 0, len(f.listeners))
	for _, listener := range f.listeners {
		listeners = append(listeners, listener)
	}
	f.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
}

func actionError(message string) func(state *application.WorkspaceState) {
	return func(state *application.WorkspaceState) { state.ActionError = &message }
}

func TestThePublisherNumbersChangesBatchesABurstAndAnswersAtTheRevisionLeft(t *testing.T) {
	fake := &fakeRuntime{state: workspace(), listeners: map[int]func(){}, refused: contracts.WorkspaceCommandResultFalse{Message: "Cannot move a running thread"}, flushed: make(chan struct{})}
	publisher := CreateRuntimePublisher(fake)
	var mu sync.Mutex
	var updates []application.WorkspaceUpdate
	publisher.Subscribe(func(update application.WorkspaceUpdate) {
		mu.Lock()
		updates = append(updates, update)
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(updates)
	}
	patch := func(index int) string {
		mu.Lock()
		defer mu.Unlock()
		raw, _ := jsonx.Marshal(updates[index])
		return string(raw)
	}
	fake.set(actionError("first"))
	fake.set(actionError("second"))
	if count() != 0 {
		t.Fatal("published before the burst ended")
	}
	eventually(t, "the burst's update", func() bool { return count() == 1 })
	time.Sleep(3 * publishDelay)
	if count() != 1 || patch(0) != `{"revision":1,"patches":[{"path":["actionError"],"value":"second"}]}` {
		t.Fatalf("updates = %d, first = %s", count(), patch(0))
	}

	answer := publisher.Request(context.Background(), contracts.TaskCommandTaskMoveWorktree{})
	if raw, _ := jsonx.Marshal(answer); string(raw) != `{"ok":false,"message":"Cannot move a running thread","revision":1}` {
		t.Fatalf("answer = %s", raw)
	}
	if snapshot := publisher.Snapshot(); snapshot.Revision != 1 || snapshot.State != fake.GetState() {
		t.Fatalf("snapshot = %+v", snapshot)
	}

	flushing := make(chan application.WorkspaceResponseResult, 1)
	go func() { flushing <- publisher.Flush(context.Background()) }()
	fake.set(actionError("saved before exit"))
	close(fake.flushed)
	if raw, _ := jsonx.Marshal(<-flushing); string(raw) != `{"ok":true,"revision":2}` {
		t.Fatalf("flush = %s", raw)
	}
	if patch(count()-1) != `{"revision":2,"patches":[{"path":["actionError"],"value":"saved before exit"}]}` {
		t.Fatalf("last = %s", patch(count()-1))
	}
	publisher.Dispose()
	fake.set(actionError("after disposal"))
	time.Sleep(3 * publishDelay)
	if count() != 2 {
		t.Fatal("a disposed publisher told somebody something")
	}
}
