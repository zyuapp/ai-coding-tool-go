package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const UpdateResumePrompt = "The app restarted to install an update, which cut off your last turn. Check whether anything it was doing was left half-done, then continue where you left off."

// ResumableAfterUpdate is the threads whose turn is going when the app quits to update. One waiting
// on the user for an approval or an answer, or that the user asked to stop, stays stopped.
func ResumableAfterUpdate(state *WorkspaceState) []string {
	open := jsonx.Set[string]{}
	for _, thread := range state.Threads {
		if thread.RunOpen != nil && *thread.RunOpen {
			open[thread.ID] = struct{}{}
		}
	}
	resumable := []string{}
	for _, key := range slices.Sorted(maps.Keys(state.ActiveRuns)) {
		run := RunRecord(state.ActiveRuns[key])
		if run != nil && open.Has(run.TaskID) && run.Status != ActiveRunRunningStatusAwaitingApproval && len(run.Questions) == 0 && (run.Cancelling == nil || !*run.Cancelling) {
			resumable = append(resumable, run.TaskID)
		}
	}
	return resumable
}

// ResumedAfterUpdate starts each named thread again on the session it was in, told to carry on.
func ResumedAfterUpdate(transition WorkspaceTransition, taskIDs []string) WorkspaceTransition {
	next := transition
	for _, taskID := range taskIDs {
		resumed := carriedOn(&next.State, taskID)
		resumed.Effects = slices.Concat(next.Effects, resumed.Effects)
		next = resumed
	}
	return next
}

func carriedOn(state *WorkspaceState, taskID string) WorkspaceTransition {
	index := slices.IndexFunc(state.Threads, func(item *domain.Thread) bool { return item.ID == taskID })
	if index < 0 || state.Threads[index].ArchivedAt != nil || ThreadBusy(state, taskID) {
		return Applied(state, SliceTransition{})
	}
	thread := state.Threads[index]
	project := ProjectFor(state, thread)
	pending := &PendingRun{
		ID:          NewID(),
		RunID:       NewID(),
		Origin:      PendingRunOriginComposer,
		Operation:   PendingRunOperationResume{After: PendingRunOperationResumeAfterUpdate},
		TaskID:      new(taskID),
		Text:        "",
		Prompt:      UpdateResumePrompt,
		Attachments: []string{},
	}
	if project != nil {
		pending.ProjectID = new(project.ID)
	}
	return Applied(state, SliceTransition{
		Edits:   []OwnerEdit{PendingIn(RunsOf(state), pending)},
		Effects: []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)},
	})
}
