package application

import (
	"maps"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var runRecordNoStarts = []*PendingRun{}

// startsByThread holds StartingThreads for the last pendingRuns map it grouped, by map identity.
var startsByThread struct {
	sync.Mutex
	of      map[string]*PendingRun
	grouped map[string][]*PendingRun
}

// RunOf is the run a thread has going, or nil.
func RunOf(state *WorkspaceState, taskID string) ActiveRun {
	return state.ActiveRuns[taskID]
}

// StartingThreads is each thread with starts still resolving where their run will happen, and those
// starts, ordered by id: Go keeps no insertion order to say which is oldest. The result is shared:
// callers must not change it.
func StartingThreads(state *WorkspaceState) map[string][]*PendingRun {
	startsByThread.Lock()
	defer startsByThread.Unlock()
	if startsByThread.grouped != nil && jsonx.SameMap(startsByThread.of, state.PendingRuns) {
		return startsByThread.grouped
	}
	ids := slices.Sorted(maps.Keys(state.PendingRuns))
	grouped := map[string][]*PendingRun{}
	for _, id := range ids {
		pending := state.PendingRuns[id]
		if pending.TaskID == nil {
			continue
		}
		grouped[*pending.TaskID] = append(grouped[*pending.TaskID], pending)
	}
	startsByThread.of, startsByThread.grouped = state.PendingRuns, grouped
	return grouped
}

// StartsOf is the starts on their way to becoming this thread's run.
func StartsOf(state *WorkspaceState, taskID string) []*PendingRun {
	if starts, ok := StartingThreads(state)[taskID]; ok {
		return starts
	}
	return runRecordNoStarts
}

// ThreadBusy reports whether a thread is working, counting the moment between a send and the checkout it resolves to.
func ThreadBusy(state *WorkspaceState, taskID string) bool {
	if state.ActiveRuns[taskID] != nil {
		return true
	}
	_, starting := StartingThreads(state)[taskID]
	return starting
}

// UsersTurn reports whether the user has a turn of their own going or on its way in this thread.
func UsersTurn(state *WorkspaceState, taskID string) bool {
	if record := RunRecord(state.ActiveRuns[taskID]); record != nil && record.Origin == ActiveRunRunningOriginComposer {
		return true
	}
	return slices.ContainsFunc(StartsOf(state, taskID), func(pending *PendingRun) bool { return pending.Origin == PendingRunOriginComposer })
}

// RunStatus is what a thread's composer shows of its run: running while one is going, else how the last one ended.
func RunStatus(state *WorkspaceState, taskID *string) ThreadRunStatus {
	if taskID == nil || *taskID == "" {
		return ThreadRunStatusIdle
	}
	if state.ActiveRuns[*taskID] != nil {
		return ThreadRunStatusRunning
	}
	if status, ok := state.RunStatuses[*taskID]; ok {
		return status
	}
	return ThreadRunStatusIdle
}

// ThreadToBegin is the thread a resolved start may begin its run on: one still there, not filed away, with no run going.
func ThreadToBegin(state *WorkspaceState, taskID *string) *domain.Thread {
	if taskID == nil || *taskID == "" {
		return nil
	}
	for _, thread := range state.Threads {
		if thread.ID == *taskID {
			if thread.ArchivedAt == nil && thread.ResourceStop == nil && state.ActiveRuns[thread.ID] == nil {
				return thread
			}
			return nil
		}
	}
	return nil
}

// RunsInWorkspace reports whether a run is going in a checkout, so nothing moves the ground under it.
func RunsInWorkspace(state *WorkspaceState, workspaceID *string) bool {
	if workspaceID == nil || *workspaceID == "" {
		return false
	}
	for taskID := range state.ActiveRuns {
		for _, thread := range state.Threads {
			if thread.ID == taskID {
				if id, ok := ThreadWorkspaceID(state, thread); ok && id == *workspaceID {
					return true
				}
				break
			}
		}
	}
	return false
}
