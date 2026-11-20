package application

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"reflect"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// ExecuteWorkspaceInput accepts synchronously. Completion waits for this input's own effects and the
// replies they dispatch, but not for effects marked FollowUp, which are handed to Track instead.
// Every effect starts at once on its own goroutine; Track receives a wait for one already running.
func ExecuteWorkspaceInput(input WorkspaceInput, host *WorkspaceExecutionHost) WorkspaceExecution {
	if host.Active != nil && !host.Active() {
		var result contracts.WorkspaceCommandResult = contracts.WorkspaceCommandResultFalse{Message: "The workspace runtime is closed."}
		settled := func(context.Context) (contracts.WorkspaceCommandResult, error) { return result, nil }
		return WorkspaceExecution{Accepted: settled, Completed: settled}
	}
	transition := reduceAndCommit(input, host)
	accepted := transition.Result
	if accepted == nil {
		accepted = contracts.WorkspaceCommandResultTrue{}
	}
	var owned []*effectWork
	for _, effect := range transition.Effects {
		work := startEffect(effect, host)
		if !isFollowUp(effect) {
			owned = append(owned, work)
		} else if host.Track != nil {
			host.Track(func(ctx context.Context) (jsontext.Value, error) {
				_, _ = work.wait(ctx)
				return nil, nil
			})
		}
	}
	return WorkspaceExecution{
		Accepted: func(context.Context) (contracts.WorkspaceCommandResult, error) { return accepted, nil },
		Completed: func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
			results := make([]contracts.WorkspaceCommandResult, 0, len(owned))
			for _, work := range owned {
				result, err := work.wait(ctx)
				if err != nil {
					return nil, err
				}
				results = append(results, result)
			}
			return combineResults(accepted, results), nil
		},
	}
}

// executionLocks holds one lock per host, so a read of its state and the commit of what the input
// made of it are one step even when replies arrive from several effects at once.
var executionLocks sync.Map // *WorkspaceExecutionHost -> *sync.Mutex

func reduceAndCommit(input WorkspaceInput, host *WorkspaceExecutionHost) WorkspaceTransition {
	held, _ := executionLocks.LoadOrStore(host, &sync.Mutex{})
	lock := held.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	state := host.State()
	transition := Reduce(&state, input)
	host.Commit(transition.State, input)
	return transition
}

// effectWork is one effect being performed: a promise of its combined result.
type effectWork struct {
	done   chan struct{}
	result contracts.WorkspaceCommandResult
	err    error
}

func (w *effectWork) wait(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
	select {
	case <-w.done:
		return w.result, w.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func startEffect(effect WorkspaceEffect, host *WorkspaceExecutionHost) *effectWork {
	work := &effectWork{done: make(chan struct{})}
	go func() {
		defer close(work.done)
		work.result, work.err = performEffect(context.Background(), effect, host)
	}()
	return work
}

// isFollowUp reads the FollowUp field every WorkspaceEffect member carries.
func isFollowUp(effect WorkspaceEffect) bool {
	field := reflect.ValueOf(effect).FieldByName("FollowUp")
	return field.IsValid() && !field.IsNil() && field.Elem().Bool()
}

func performEffect(ctx context.Context, effect WorkspaceEffect, host *WorkspaceExecutionHost) (contracts.WorkspaceCommandResult, error) {
	var (
		mu      sync.Mutex
		replies []contracts.WorkspaceCommandResult
	)
	dispatch := func(ctx context.Context, reply WorkspaceInput) error {
		if host.Active != nil && !host.Active() {
			return nil
		}
		if host.Prepare != nil {
			if err := host.Prepare(ctx, reply); err != nil {
				return err
			}
		}
		result, err := ExecuteWorkspaceInput(reply, host).Completed(ctx)
		if err != nil {
			return err
		}
		mu.Lock()
		replies = append(replies, result)
		mu.Unlock()
		return nil
	}
	if err := performCaught(ctx, effect, host, dispatch); err != nil {
		if err := dispatch(ctx, contracts.ActionFailedEvent{Message: err.Error()}); err != nil {
			return nil, err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return combineResults(contracts.WorkspaceCommandResultTrue{}, replies), nil
}

// performCaught is host.Perform with a panic taken as its error, as an await takes a throw.
func performCaught(ctx context.Context, effect WorkspaceEffect, host *WorkspaceExecutionHost, dispatch func(ctx context.Context, input WorkspaceInput) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if failure, ok := recovered.(error); ok {
				err = failure
			} else {
				err = fmt.Errorf("%v", recovered)
			}
		}
	}()
	return host.Perform(ctx, effect, dispatch)
}

func combineResults(initial contracts.WorkspaceCommandResult, replies []contracts.WorkspaceCommandResult) contracts.WorkspaceCommandResult {
	if _, failed := initial.(contracts.WorkspaceCommandResultFalse); failed {
		return initial
	}
	result := initial
	for _, reply := range replies {
		switch reply := reply.(type) {
		case contracts.WorkspaceCommandResultFalse:
			return reply
		case contracts.WorkspaceCommandResultTrue:
			if reply.TaskID != nil {
				result = reply
			}
		}
	}
	return result
}
