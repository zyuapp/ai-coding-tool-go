package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

var StoreInputs = []string{"store.thread-loaded", "store.absent", "store.persisted", "store.failed", "action.failed"}

func ReduceStore(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case WorkspaceEventStoreThreadLoaded:
		thread := findThread(state.Threads, &input.TaskID)
		if thread == nil || thread.HistorySummary == nil {
			return SliceTransition{}
		}
		return SliceTransition{Edits: []OwnerEdit{HistoryLoadedIn(ThreadsOf(state), input.TaskID, input.Messages)}}
	case WorkspaceEventStoreAbsent:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Restored = true }}
	case WorkspaceEventStorePersisted:
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Writable = true
			next.StorageError = nil
		}}
	case WorkspaceEventStoreFailed:
		message := input.Message
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Writable = false
			next.StorageError = &message
			next.Restored = true
		}}
	case contracts.ActionFailedEvent:
		message := input.Message
		return SliceTransition{Error: &SliceError{Message: &message, Page: input.Page}, Refusal: &message}
	}
	return SliceTransition{}
}

// ReduceHiddenThreadsDismissed is the note about threads this build cannot read, put away by the user.
func ReduceHiddenThreadsDismissed(state *WorkspaceState) SliceTransition {
	return SliceTransition{Patch: func(next *WorkspaceState) { next.HiddenThreads = 0 }}
}
