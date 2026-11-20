package host

import (
	"context"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// publishDelay gathers the commits of one burst, such as a run's streaming text, into one update.
const publishDelay = 16 * time.Millisecond

// Publisher numbers the runtime's states and hands each subscriber the difference between one and the
// next. Commits that land together are published together, at most once per frame, so a run
// reporting on every tool call costs its watchers one update rather than one per report. It stands
// for TypeScript's RuntimePublisher, whose `revision` is a live getter. Subscribers are called one
// update at a time, in revision order, and must not call back into the publisher.
type Publisher struct {
	runtime     PublishedRuntime
	mu          sync.Mutex
	subscribers map[int]RuntimeSubscriber
	nextID      int
	previous    *application.WorkspaceState
	revision    float64
	publishing  bool
	stop        func()
}

// PublishedRuntime is what a publisher reads and drives; *Runtime is one.
type PublishedRuntime interface {
	GetState() *application.WorkspaceState
	Subscribe(listener func()) func()
	Execute(input application.WorkspaceInput) application.WorkspaceExecution
	Flush(ctx context.Context) error
}

func CreateRuntimePublisher(runtime PublishedRuntime) *Publisher {
	p := &Publisher{runtime: runtime, subscribers: map[int]RuntimeSubscriber{}, previous: runtime.GetState()}
	p.stop = runtime.Subscribe(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.publishing {
			return
		}
		p.publishing = true
		time.AfterFunc(publishDelay, p.publishPending)
	})
	return p
}

func (p *Publisher) publishPending() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishLocked()
}

// publishLocked runs with p.mu held.
func (p *Publisher) publishLocked() {
	p.publishing = false
	state := p.runtime.GetState()
	if state == p.previous {
		return
	}
	patches, err := application.Patches(p.previous, state)
	p.previous = state
	update := application.WorkspaceUpdate{Patches: patches}
	if err != nil {
		update = application.WorkspaceUpdate{State: state}
	} else if len(patches) == 0 {
		return
	}
	p.revision++
	update.Revision = p.revision
	for id := range p.nextID {
		if subscriber, ok := p.subscribers[id]; ok {
			subscriber(update)
		}
	}
}

// Revision is the revision the next patch follows.
func (p *Publisher) Revision() float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revision
}

func (p *Publisher) Subscribe(subscriber RuntimeSubscriber) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextID
	p.nextID++
	p.subscribers[id] = subscriber
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.subscribers, id)
	}
}

// Snapshot is the whole state as it stands, at the revision the next patch follows.
func (p *Publisher) Snapshot() application.WorkspaceUpdate {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishLocked()
	return application.WorkspaceUpdate{Revision: p.revision, State: p.runtime.GetState()}
}

// answer is a result with the revision it left the state at.
func (p *Publisher) answer(result contracts.WorkspaceCommandResult, err error) application.WorkspaceResponseResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.publishLocked()
	if err != nil {
		return contracts.ComputerServerMessageResultResultFalse{Message: errorMessage(err), Revision: p.revision}
	}
	switch result := result.(type) {
	case contracts.WorkspaceCommandResultFalse:
		return contracts.ComputerServerMessageResultResultFalse{Message: result.Message, Revision: p.revision}
	case contracts.WorkspaceCommandResultTrue:
		return contracts.ComputerServerMessageResultResultTrue{TaskID: result.TaskID, Answer: result.Answer, Revision: p.revision}
	}
	return contracts.ComputerServerMessageResultResultTrue{Revision: p.revision}
}

// Request runs one input to completion and answers with the revision its effects left the state at.
// A failure is the answer.
func (p *Publisher) Request(ctx context.Context, input application.WorkspaceInput) application.WorkspaceResponseResult {
	return p.answer(p.runtime.Execute(input).Completed(ctx))
}

// Begin starts one input now, in the order Begin is called, and returns what Request would answer
// once its effects have settled.
func (p *Publisher) Begin(input application.WorkspaceInput) func(ctx context.Context) application.WorkspaceResponseResult {
	execution := p.runtime.Execute(input)
	return func(ctx context.Context) application.WorkspaceResponseResult {
		return p.answer(execution.Completed(ctx))
	}
}

// Flush waits for every effect in flight and every write behind it, then publishes what they left.
func (p *Publisher) Flush(ctx context.Context) application.WorkspaceResponseResult {
	return p.answer(acceptedResult, p.runtime.Flush(ctx))
}

func (p *Publisher) Dispose() {
	p.stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.subscribers)
}
