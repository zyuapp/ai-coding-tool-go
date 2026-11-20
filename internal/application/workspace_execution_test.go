package application

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

type executionDriver struct {
	mu    sync.Mutex
	state WorkspaceState
	host  *WorkspaceExecutionHost
}

func newExecutionDriver(initial *WorkspaceState, perform func(ctx context.Context, effect WorkspaceEffect, dispatch func(ctx context.Context, input WorkspaceInput) error) error) *executionDriver {
	driver := &executionDriver{state: *initial}
	if perform == nil {
		perform = func(context.Context, WorkspaceEffect, func(context.Context, WorkspaceInput) error) error { return nil }
	}
	driver.host = &WorkspaceExecutionHost{
		State: func() WorkspaceState {
			driver.mu.Lock()
			defer driver.mu.Unlock()
			return driver.state
		},
		Commit: func(state WorkspaceState, _ WorkspaceInput) {
			driver.mu.Lock()
			defer driver.mu.Unlock()
			driver.state = state
		},
		Perform: perform,
	}
	return driver
}

func (d *executionDriver) execute(input WorkspaceInput) WorkspaceExecution {
	return ExecuteWorkspaceInput(input, d.host)
}

func executionResult(t *testing.T, step func(ctx context.Context) (contracts.WorkspaceCommandResult, error)) contracts.WorkspaceCommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := step(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestEachRejectionIsReturnedEvenWhenTheErrorIsShown(t *testing.T) {
	corePorted(t, func() {
		driver := newExecutionDriver(EmptyWorkspaceState(nil), nil)
		first := driver.execute(contracts.BrowserCommandBrowserReload{})
		second := driver.execute(contracts.BrowserCommandBrowserReload{})
		accepted := executionResult(t, first.Accepted)
		if _, failed := accepted.(contracts.WorkspaceCommandResultFalse); !failed {
			t.Fatal("reload with no page accepted")
		}
		coreEqualJSON(t, executionResult(t, second.Accepted), accepted, "second accepted")
		coreEqualJSON(t, executionResult(t, second.Completed), accepted, "second completed")
		coreEqualJSON(t, executionResult(t, driver.execute(contracts.ViewCommandViewSetPrompt{Prompt: "hello"}).Accepted), `{"ok":true}`, "prompt")
	})
}

func TestEffectFailuresBelongToTheirOwnCommand(t *testing.T) {
	corePorted(t, func() {
		release := make(chan struct{})
		driver := newExecutionDriver(EmptyWorkspaceState(nil), func(_ context.Context, effect WorkspaceEffect, _ func(context.Context, WorkspaceInput) error) error {
			if open, ok := effect.(WorkspaceEffectFileOpen); ok && open.Path == "/failure" {
				<-release
				return errors.New("cannot open")
			}
			return nil
		})
		first := driver.execute(contracts.FileCommandFileOpen{Path: "/failure"})
		second := driver.execute(contracts.FileCommandFileOpen{Path: "/success"})
		coreEqualJSON(t, executionResult(t, first.Accepted), `{"ok":true}`, "accepted")
		close(release)
		coreEqualJSON(t, executionResult(t, first.Completed), `{"ok":false,"message":"cannot open"}`, "first")
		coreEqualJSON(t, executionResult(t, second.Completed), `{"ok":true}`, "second")
		if state := driver.host.State(); deref(state.ActionError) != "cannot open" {
			t.Fatal("the failure is not shown")
		}
	})
}

func TestFailuresReportedThroughEventsStayCorrelated(t *testing.T) {
	corePorted(t, func() {
		driver := newExecutionDriver(EmptyWorkspaceState(nil), func(ctx context.Context, effect WorkspaceEffect, dispatch func(context.Context, WorkspaceInput) error) error {
			if _, ok := effect.(WorkspaceEffectFileOpen); ok {
				return dispatch(ctx, contracts.ActionFailedEvent{Message: "cannot open"})
			}
			return nil
		})
		for range 2 {
			execution := driver.execute(contracts.FileCommandFileOpen{Path: "/file"})
			coreEqualJSON(t, executionResult(t, execution.Accepted), `{"ok":true}`, "accepted")
			coreEqualJSON(t, executionResult(t, execution.Completed), `{"ok":false,"message":"cannot open"}`, "completed")
		}
	})
}

func TestClosedRuntimeRefusesInputs(t *testing.T) {
	driver := newExecutionDriver(EmptyWorkspaceState(nil), nil)
	driver.host.Active = func() bool { return false }
	execution := driver.execute(contracts.ViewCommandViewSetPrompt{Prompt: "hello"})
	coreEqualJSON(t, executionResult(t, execution.Accepted), `{"ok":false,"message":"The workspace runtime is closed."}`, "accepted")
	coreEqualJSON(t, executionResult(t, execution.Completed), `{"ok":false,"message":"The workspace runtime is closed."}`, "completed")
}

func TestFollowUpWorkIsTrackedNotAwaited(t *testing.T) {
	corePorted(t, func() {
		state := desktopInProject()
		state.Projects[0].WorkspaceID = new("workspace-1")
		state.Focused = false
		release := make(chan struct{})
		driver := newExecutionDriver(state, func(ctx context.Context, effect WorkspaceEffect, dispatch func(context.Context, WorkspaceInput) error) error {
			if _, ok := effect.(WorkspaceEffectRefreshEnvironment); ok {
				<-release
				return errors.New("git status failed")
			}
			return nil
		})
		var mu sync.Mutex
		var tracked []func(ctx context.Context) (jsontext.Value, error)
		driver.host.Track = func(work func(ctx context.Context) (jsontext.Value, error)) {
			mu.Lock()
			defer mu.Unlock()
			tracked = append(tracked, work)
		}
		coreEqualJSON(t, executionResult(t, driver.execute(contracts.ViewCommandViewSetFocused{Focused: true}).Completed), `{"ok":true}`, "completed before the follow-up")
		mu.Lock()
		held := slices.Clone(tracked)
		mu.Unlock()
		if len(held) != 1 {
			t.Fatalf("%d tracked", len(held))
		}
		close(release)
		if _, err := held[0](context.Background()); err != nil {
			t.Fatal(err)
		}
		if state := driver.host.State(); deref(state.ActionError) != "git status failed" {
			t.Fatal("the follow-up failure did not reach state")
		}
	})
}
