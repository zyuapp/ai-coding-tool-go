package host

import (
	"context"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// future is a promise: a value or error, settled once, that any number of goroutines wait for.
type future[T any] struct {
	once  sync.Once
	done  chan struct{}
	value T
	err   error
}

func newFuture[T any]() *future[T] { return &future[T]{done: make(chan struct{})} }

func settledFuture[T any](value T, err error) *future[T] {
	f := newFuture[T]()
	f.settle(value, err)
	return f
}

func (f *future[T]) settle(value T, err error) {
	f.once.Do(func() {
		f.value, f.err = value, err
		close(f.done)
	})
}

func (f *future[T]) wait(ctx context.Context) (T, error) {
	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// commandResult is a WorkspaceCommandResult on its way.
type commandResult = future[contracts.WorkspaceCommandResult]

// execution is a WorkspaceExecution answered by futures.
func execution(accepted, completed *commandResult) application.WorkspaceExecution {
	return application.WorkspaceExecution{Accepted: accepted.wait, Completed: completed.wait}
}

// fromCompletion is a future for a completion function, waited for on its own goroutine.
func fromCompletion(completed func(ctx context.Context) (contracts.WorkspaceCommandResult, error)) *commandResult {
	f := newFuture[contracts.WorkspaceCommandResult]()
	go func() { f.settle(completed(background)) }()
	return f
}

var acceptedResult = contracts.WorkspaceCommandResultTrue{}
