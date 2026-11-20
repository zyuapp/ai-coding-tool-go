package host

import (
	"context"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// SubscriptionHost is what host modules listen with: TypeScript's RuntimeSubscriptionHost, holding
// the state by pointer and the waiters shared rather than copied.
type SubscriptionHost struct {
	State    func() *application.WorkspaceState
	Dispatch func(ctx context.Context, input application.WorkspaceInput) error
	// Execute starts an input without waiting for it: TypeScript's `void dispatch(input)`.
	Execute              func(input application.WorkspaceInput) application.WorkspaceExecution
	Waiters              *ThreadWaiters
	PrepareThreadRequest PrepareThreadRequest
	// Hydrate is nil where threads have no stored history to load.
	Hydrate func(ctx context.Context, taskID string) error
	Desktop RuntimeDesktop
	// Frame is nil without a window.
	Frame func(flush func()) func()
}

// runReports folds in what runs have reported but the reducer has not yet seen.
type runReports struct {
	mu       sync.Mutex
	flushers []func()
}

func (r *runReports) FlushAll() {
	r.mu.Lock()
	flushers := r.flushers
	r.mu.Unlock()
	for _, flush := range flushers {
		flush()
	}
}

func (r *runReports) HoldReports(flush func()) {
	r.mu.Lock()
	r.flushers = append(r.flushers, flush)
	r.mu.Unlock()
}

// SubscribeWorkspaceRuntime starts every host module's subscription; they outlive every mounted view.
// stop ends them in the order they started, and flush folds in what runs have reported so far, before
// anything reads or acts on the thread a report went to.
func SubscribeWorkspaceRuntime(host SubscriptionHost) (stop func(), flush func()) {
	var stops []func()
	runs := &runReports{}
	for _, module := range HostModules {
		if module.Subscribe == nil {
			continue
		}
		if stop := module.Subscribe(host, runs); stop != nil {
			stops = append(stops, stop)
		}
	}
	return func() {
		for _, stop := range stops {
			stop()
		}
	}, runs.FlushAll
}

// background is the context of work nothing waits on: listeners and fire-and-forget dispatches.
var background = context.Background()

// send is TypeScript's `void host.dispatch(input)`: the input starts now, and nothing waits for it.
func (h SubscriptionHost) send(input application.WorkspaceInput) {
	h.Execute(input)
}

// failed is an action.failed input for err.
func failed(err error) contracts.ActionFailedEvent {
	return contracts.ActionFailedEvent{Message: errorMessage(err)}
}
