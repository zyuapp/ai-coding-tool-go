package host

import (
	"context"
	"errors"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// RuntimeInputHistory is the part of RuntimeHistory inputs wait on.
type RuntimeInputHistory interface {
	Needed(input application.WorkspaceInput) []string
	Hydrate(ctx context.Context, taskID string) error
	Scope(input application.WorkspaceInput) application.InputScope
}

// RuntimeInputHost is what ordering inputs takes from the runtime.
type RuntimeInputHost struct {
	Generation func() int
	Active     func(generation int) bool
	// Screen is what the window shows now, as ScreenOf reads it.
	Screen  func() string
	History RuntimeInputHistory
	Execute func(input application.WorkspaceInput) application.WorkspaceExecution
	// Track holds the runtime's flush until completed has settled.
	Track func(completed func(ctx context.Context) (contracts.WorkspaceCommandResult, error))
}

const (
	closed = "The workspace runtime is closed."
	moved  = "The thread on screen changed before this could run."
)

func refused(message string) application.WorkspaceExecution {
	result := settledFuture[contracts.WorkspaceCommandResult](contracts.WorkspaceCommandResultFalse{Message: message}, nil)
	return execution(result, result)
}

// RuntimeInputs orders inputs. Inputs wait only for the histories they need and for earlier pending
// inputs sharing one of their keys, in arrival order per key. Everything else runs at once, whatever
// another thread is loading.
type RuntimeInputs struct {
	host RuntimeInputHost
	mu   sync.Mutex
	// tails holds the newest pending input per key; the next input with that key starts after it.
	tails   map[string]chan struct{}
	pending map[chan struct{}]struct{}
}

func CreateRuntimeInputs(host RuntimeInputHost) *RuntimeInputs {
	return &RuntimeInputs{host: host, tails: map[string]chan struct{}{}, pending: map[chan struct{}]struct{}{}}
}

func (r *RuntimeInputs) Execute(arrived application.WorkspaceInput) application.WorkspaceExecution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.execute(arrived)
}

// execute runs with r.mu held, so inputs sharing a key reach the runtime in arrival order.
func (r *RuntimeInputs) execute(arrived application.WorkspaceInput) application.WorkspaceExecution {
	host := r.host
	if len(r.tails) == 0 && len(host.History.Needed(arrived)) == 0 {
		return host.Execute(arrived)
	}
	scope := host.History.Scope(arrived)
	input := scope.Input
	needed := host.History.Needed(input)
	keys := map[string]struct{}{}
	for key := range scope.Keys {
		keys[key] = struct{}{}
	}
	for _, taskID := range needed {
		keys[taskID] = struct{}{}
	}
	var before []chan struct{}
	for key := range keys {
		if tail := r.tails[key]; tail != nil {
			before = append(before, tail)
		}
	}
	if len(before) == 0 && len(needed) == 0 {
		return host.Execute(input)
	}
	batch, isBatch := input.(application.WorkspaceEventAgentEvents)
	// A batch spanning threads splits, so events for a thread with nothing to wait for are not held by another's read.
	if isBatch && len(keys) > 1 {
		batchGeneration := host.Generation()
		parts := make([]application.WorkspaceExecution, len(batch.Events))
		for index, event := range batch.Events {
			parts[index] = r.execute(application.AgentEventInput(event))
		}
		// A failure is reported again once the batch is done, so events applied after it cannot hide it.
		completed := fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
			var failure contracts.WorkspaceCommandResult
			for _, part := range parts {
				result, err := part.Completed(ctx)
				if err != nil {
					return nil, err
				}
				if _, refused := result.(contracts.WorkspaceCommandResultFalse); refused && failure == nil {
					failure = result
				}
			}
			if failure == nil {
				return acceptedResult, nil
			}
			if !host.Active(batchGeneration) {
				return failure, nil
			}
			if _, err := host.Execute(contracts.ActionFailedEvent{Message: failure.(contracts.WorkspaceCommandResultFalse).Message}).Completed(ctx); err != nil {
				return nil, err
			}
			return failure, nil
		})
		host.Track(completed.wait)
		return execution(settledFuture[contracts.WorkspaceCommandResult](acceptedResult, nil), completed)
	}
	inputGeneration := host.Generation()
	splitBatch := isBatch && len(needed) > 0
	screen := scope.Screen
	prepared := newFuture[application.WorkspaceExecution]()
	queued := make(chan struct{})
	go func() {
		prepared.settle(r.prepareSafely(before, input, inputGeneration, screen, splitBatch), nil)
		r.mu.Lock()
		delete(r.pending, queued)
		for key := range keys {
			if r.tails[key] == queued {
				delete(r.tails, key)
			}
		}
		r.mu.Unlock()
		close(queued)
	}()
	r.pending[queued] = struct{}{}
	for key := range keys {
		r.tails[key] = queued
	}
	completed := fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
		execution, _ := prepared.wait(ctx)
		if execution.Completed == nil {
			return nil, ctx.Err()
		}
		return execution.Completed(ctx)
	})
	if splitBatch {
		host.Track(completed.wait)
	}
	acceptance := settledFuture[contracts.WorkspaceCommandResult](acceptedResult, nil)
	if !isBatch {
		acceptance = fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
			execution, _ := prepared.wait(ctx)
			if execution.Accepted == nil {
				return nil, ctx.Err()
			}
			return execution.Accepted(ctx)
		})
	}
	return execution(acceptance, completed)
}

// prepareSafely is prepare with a panic turned into the failure a rejected preparation reports.
func (r *RuntimeInputs) prepareSafely(before []chan struct{}, input application.WorkspaceInput, generation int, screen *string, splitBatch bool) (prepared application.WorkspaceExecution) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		if !r.host.Active(generation) {
			prepared = refused(closed)
			return
		}
		result := contracts.WorkspaceCommandResultFalse{Message: errorMessage(panicError(recovered))}
		failed := r.host.Execute(contracts.ActionFailedEvent{Message: result.Message})
		prepared = execution(settledFuture[contracts.WorkspaceCommandResult](result, nil), fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
			if _, err := failed.Completed(ctx); err != nil {
				return nil, err
			}
			return result, nil
		}))
	}()
	return r.prepare(before, input, generation, screen, splitBatch)
}

// prepare waits for the inputs ahead and the histories needed, then runs the input, or turns it
// down when the runtime or the screen moved meanwhile.
func (r *RuntimeInputs) prepare(before []chan struct{}, input application.WorkspaceInput, generation int, screen *string, splitBatch bool) application.WorkspaceExecution {
	host := r.host
	for _, tail := range before {
		<-tail
	}
	// Checked again after every read, since the runtime and the screen can both change during one.
	refusal := func() (application.WorkspaceExecution, bool) {
		if !host.Active(generation) {
			return refused(closed), true
		}
		if screen != nil && host.Screen() != *screen {
			return refused(moved), true
		}
		return application.WorkspaceExecution{}, false
	}
	if early, refusing := refusal(); refusing {
		return early
	}
	if splitBatch {
		return r.splitBatch(input.(application.WorkspaceEventAgentEvents), generation)
	}
	for _, taskID := range host.History.Needed(input) {
		if err := host.History.Hydrate(background, taskID); err != nil {
			if !host.Active(generation) {
				return refused(closed)
			}
			result := contracts.WorkspaceCommandResultFalse{Message: errorMessage(err)}
			failed := host.Execute(contracts.ActionFailedEvent{Message: result.Message})
			completed := fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
				if _, err := failed.Completed(ctx); err != nil {
					return nil, err
				}
				return result, nil
			})
			return execution(settledFuture[contracts.WorkspaceCommandResult](result, nil), completed)
		}
	}
	if late, refusing := refusal(); refusing {
		return late
	}
	return host.Execute(input)
}

// splitBatch runs a batch event by event, each after the history it needs.
func (r *RuntimeInputs) splitBatch(batch application.WorkspaceEventAgentEvents, generation int) application.WorkspaceExecution {
	host := r.host
	var completions []func(ctx context.Context) (contracts.WorkspaceCommandResult, error)
	var failure error
	for _, event := range batch.Events {
		single := application.AgentEventInput(event)
		var err error
		for _, taskID := range host.History.Needed(single) {
			if err = host.History.Hydrate(background, taskID); err != nil {
				break
			}
		}
		if !host.Active(generation) {
			return refused(closed)
		}
		if err == nil {
			completions = append(completions, host.Execute(single).Completed)
			continue
		}
		message := errorMessage(err)
		failure = errors.New(message)
		failed := host.Execute(contracts.ActionFailedEvent{Message: message})
		completions = append(completions, func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
			if _, err := failed.Completed(ctx); err != nil {
				return nil, err
			}
			return contracts.WorkspaceCommandResultFalse{Message: message}, nil
		})
	}
	if failure != nil {
		completions = append(completions, host.Execute(contracts.ActionFailedEvent{Message: failure.Error()}).Completed)
	}
	completed := fromCompletion(func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
		results := make([]contracts.WorkspaceCommandResult, len(completions))
		for index, completion := range completions {
			result, err := completion(ctx)
			if err != nil {
				return nil, err
			}
			results[index] = result
		}
		for _, result := range results {
			if _, refused := result.(contracts.WorkspaceCommandResultFalse); refused {
				return result, nil
			}
		}
		return acceptedResult, nil
	})
	return execution(settledFuture[contracts.WorkspaceCommandResult](acceptedResult, nil), completed)
}

// Settled waits until no input is pending, including ones that arrive meanwhile.
func (r *RuntimeInputs) Settled(ctx context.Context) error {
	for {
		r.mu.Lock()
		pending := make([]chan struct{}, 0, len(r.pending))
		for queued := range r.pending {
			pending = append(pending, queued)
		}
		r.mu.Unlock()
		if len(pending) == 0 {
			return nil
		}
		for _, queued := range pending {
			select {
			case <-queued:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func (r *RuntimeInputs) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.tails)
	clear(r.pending)
}
