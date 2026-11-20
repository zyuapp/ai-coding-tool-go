package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// awPorted runs body, skipping the test while it reaches a function another group has yet to port.
func awPorted(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if text := fmt.Sprint(recovered); strings.Contains(text, "unported: ") {
				t.Skipf("blocked: %s", text)
			}
			panic(recovered)
		}
	}()
	body()
}

// awTask is a thread as the TS fixtures' task() makes one.
func awTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title = id, id
	thread.Engine = domain.AgentEngine("claude")
	thread.ExecutionPolicy = domain.ExecutionPolicyConfirm
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt = 1
	for _, edit := range edits {
		edit(thread)
	}
	return thread
}

func awInProject(id string) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.ProjectID = &id }
}

func awInWorktree(id string) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.WorktreeID = &id }
}

// awWorkspace is the empty workspace with edits made to it.
func awWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := *EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(&state)
	}
	return &state
}

// awApply runs one slice reducer and joins its answer into the state, as the reducer does.
func awApply(state *WorkspaceState, reducer func(*WorkspaceState, WorkspaceInput) SliceTransition, input WorkspaceInput) WorkspaceTransition {
	return Applied(state, reducer(state, input))
}

func awText(text *string) string {
	if text == nil {
		return "<nil>"
	}
	return *text
}

