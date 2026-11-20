package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var DiffInputs = []string{
	"view.refresh-environment", "diff.toggle", "diff.refresh", "diff.open-commit", "diff.set-range", "diff.set-collapsed", "diff.set-mode",
	"diff.set-viewed", "diff.set-split", "diff.set-ignore-whitespace", "diff.loaded", "environment.updated",
}

// The paths with `path` folded shut, or opened again; the same slice when that changes nothing to fold.
func foldedPaths(collapsed []string, path string, folded bool) []string {
	if folded {
		if slices.Contains(collapsed, path) {
			return collapsed
		}
		return append(slices.Clip(collapsed), path)
	}
	return slices.DeleteFunc(slices.Clone(collapsed), func(item string) bool { return item == path })
}

// The review slice. `input` is one of the DiffInputs. Its thread edits patch only `lastChangeSnapshot`.
func ReduceDiffs(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.DiffCommandDiffOpenCommit:
		if !domain.IsCommitHash(input.Commit) {
			return SliceTransition{Refusal: new("Invalid commit hash.")}
		}
		var thread *domain.Thread
		if id := input.TaskID; id != nil {
			thread = threadByID(state.Threads, *id)
		} else if state.CurrentID != nil {
			thread = threadByID(state.Threads, *state.CurrentID)
		}
		workspaceID := threadWorkspaceOf(state, thread)
		if workspaceID == nil || *workspaceID == "" {
			return SliceTransition{Refusal: new("Open this thread's project to inspect the commit.")}
		}
		owner := DockOwner(state, nil)
		dock := DockFor(state, owner)
		panels := dock.Panels
		if !slices.Contains(panels, DiffPanel) {
			panels = append(slices.Clip(panels), DiffPanel)
		}
		opened := EditDock(DocksOf(state), owner, func(next *ThreadDock) { next.Panels = panels })
		shown, focus := FocusDockTabIn(ShowDockTabIn(opened, owner, DiffPanel), owner, DiffPanel)
		commit := domain.DiffRangeCommit{Commit: input.Commit}
		return SliceTransition(AndThen(state, Step{Edits: []OwnerEdit{shown}, Effects: focus}, func(next *WorkspaceState) Step {
			read := DiffReadFrom(next, owner, workspaceID, commit, func(diff *DiffState) {
				diff.Mode = DiffStateModeCommit
				clearedReview(diff)
			})
			read.Effects = slices.Concat(BrowserEffectsForTab(Afterward(next, SliceTransition(read)), owner, DiffPanel), read.Effects)
			return read
		}))

	// A thread with no checkout has nothing to read; what other checkouts said is still theirs.
	case contracts.ViewCommandViewRefreshEnvironment:
		return SliceTransition{Effects: RefreshEnvironment(state)}

	case contracts.DiffCommandDiffToggle:
		owner := DockOwner(state, nil)
		dock := DockFor(state, owner)
		subject := DockSubject(state, owner, DiffPanel)
		showing := dock.Open && dock.Tab == DiffPanel && sameOptionalText(subject, input.TaskID)
		if showing {
			return SliceTransition(ReduceDock(state, contracts.ViewCommandViewCloseDockPanel{Panel: DiffPanel}))
		}
		var taskID *string
		if input.TaskID != nil && *input.TaskID != "" {
			taskID = input.TaskID
		}
		return SliceTransition(ReduceDock(state, contracts.ViewCommandViewOpenDockPanel{Panel: DiffPanel, TaskID: taskID}))

	case contracts.DiffCommandDiffRefresh:
		owner := DockOwner(state, nil)
		return SliceTransition(DiffRead(state, owner, DiffFor(state, owner).Range, nil))

	case contracts.DiffCommandDiffSetMode:
		owner := DockOwner(state, nil)
		diff := DiffFor(state, owner)
		if string(diff.Mode) == string(input.Mode) {
			return SliceTransition{ClosesMenu: true}
		}
		var diffRange domain.DiffRange
		if input.Mode == domain.DiffModeUncommitted {
			diffRange = domain.Uncommitted
		} else {
			subject := SubjectWorkspaceID(state, owner)
			// TS compares string | null with string | undefined, so two absent checkouts are not the same one.
			if diff.WorkspaceID != nil && subject != nil && *diff.WorkspaceID == *subject && diff.BranchRange != nil {
				diffRange = *diff.BranchRange
			} else {
				diffRange = DefaultBranchRange(state, owner)
			}
		}
		mode := DiffStateMode(input.Mode)
		read := SliceTransition(DiffRead(state, owner, diffRange, func(next *DiffState) {
			next.Mode = mode
			clearedReview(next)
		}))
		read.ClosesMenu = true
		return read

	case contracts.DiffCommandDiffSetRange:
		owner := DockOwner(state, nil)
		diff := DiffFor(state, owner)
		mode := DiffStateMode(domain.ModeForRange(input.Range))
		if diff.Mode == mode && domain.RangeKey(diff.Range) == domain.RangeKey(input.Range) {
			return SliceTransition{}
		}
		// A different comparison is a different set of files, so nothing carries over but the layout.
		return SliceTransition(DiffRead(state, owner, input.Range, func(next *DiffState) {
			next.Mode = mode
			clearedReview(next)
		}))

	case contracts.DiffCommandDiffSetCollapsed:
		owner := DockOwner(state, nil)
		collapsed := foldedPaths(DiffFor(state, owner).Collapsed, input.Path, input.Collapsed)
		return SliceTransition{Edits: []OwnerEdit{EditDiff(DocksOf(state), owner, func(next *DiffState) { next.Collapsed = collapsed })}}

	case contracts.DiffCommandDiffSetViewed:
		owner := DockOwner(state, nil)
		diff := DiffFor(state, owner)
		available, ok := diff.Result.(contracts.DiffSummaryResultAvailable)
		if !ok {
			return SliceTransition{}
		}
		index := slices.IndexFunc(available.Files, func(item *domain.DiffFileSummary) bool { return item.Path == input.Path })
		if index < 0 {
			return SliceTransition{}
		}
		viewed := maps.Clone(diff.Viewed)
		if viewed == nil {
			viewed = map[string]string{}
		}
		delete(viewed, input.Path)
		if input.Viewed {
			viewed[input.Path] = domain.FileFingerprint(available.Files[index])
		}
		// Ticking a file off folds it away, which is what makes working down the list one click.
		collapsed := foldedPaths(diff.Collapsed, input.Path, input.Viewed)
		return SliceTransition{Edits: []OwnerEdit{EditDiff(DocksOf(state), owner, func(next *DiffState) {
			next.Viewed, next.Collapsed = viewed, collapsed
		})}}

	case contracts.DiffCommandDiffSetSplit:
		return SliceTransition{Edits: []OwnerEdit{EditDiff(DocksOf(state), DockOwner(state, nil), func(next *DiffState) { next.Split = input.Split })}}

	// The same comparison counted a different way, so the list is read again and the review stays put.
	case contracts.DiffCommandDiffSetIgnoreWhitespace:
		owner := DockOwner(state, nil)
		diff := DiffFor(state, owner)
		if diff.IgnoreWhitespace == input.Ignore {
			return SliceTransition{}
		}
		return SliceTransition(DiffRead(state, owner, diff.Range, func(next *DiffState) { next.IgnoreWhitespace = input.Ignore }))

	case DiffEventDiffLoaded:
		diff := DiffFor(state, input.Owner)
		if !DiffMatches(diff, input.WorkspaceID, input.Range) {
			return SliceTransition{}
		}
		available, listed := input.Result.(contracts.DiffSummaryResultAvailable)
		// A list counted the other way was read before the toggle, so it no longer answers anything.
		if listed && available.IgnoreWhitespace != diff.IgnoreWhitespace {
			return SliceTransition{}
		}
		viewed := RetainedViews(diff.Viewed, input.Result, diff.Result)
		var collapsed []string
		if listed {
			collapsed = FoldedOnLoad(diff, available.Files, input.Result)
		}
		transition := SliceTransition{Edits: []OwnerEdit{EditDiff(DocksOf(state), input.Owner, func(next *DiffState) {
			next.Result, next.Loading, next.Viewed = input.Result, false, viewed
			if listed {
				next.Collapsed = collapsed
			}
		})}}
		if failed, ok := input.Result.(contracts.BranchesResultError); ok {
			transition.Result = contracts.WorkspaceCommandResultFalse{Message: failed.Message}
		}
		return transition

	case DiffEventEnvironmentUpdated:
		previous := EnvironmentFor(state, &input.WorkspaceID)
		var read SliceTransition
		if !SameChangedFiles(previous, input.Result) {
			read.Edits = []OwnerEdit{EnvironmentReadIn(GitOf(state), state, input.WorkspaceID, input.Result)}
		}
		if failed, ok := input.Result.(contracts.BranchesResultError); ok {
			read.Result = contracts.WorkspaceCommandResultFalse{Message: failed.Message}
			return read
		}
		// The checkout is worth recording whoever asked; only the thread's own snapshot is the run's.
		if input.RunID != nil && *input.RunID != "" && input.TaskID != nil && *input.TaskID != "" {
			if last, ok := state.LastRunIDs[*input.TaskID]; !ok || last != *input.RunID {
				return read
			}
		}
		available, ok := input.Result.(contracts.ChangedFilesResultAvailable)
		if input.TaskID == nil || *input.TaskID == "" || !ok {
			return read
		}
		files := available.Files
		thread := threadByID(state.Threads, *input.TaskID)
		if thread == nil || SameStrings(thread.LastChangeSnapshot.Files, files) {
			return read
		}
		if workspaceID := threadWorkspaceOf(state, thread); workspaceID != nil && *workspaceID != "" && *workspaceID != input.WorkspaceID {
			return read
		}
		snapshot := EditThread(ThreadsOf(state), *input.TaskID, func(*domain.Thread) ThreadPatch {
			capturedAt := Now()
			updatedAt := Now()
			return func(next *domain.Thread) {
				next.LastChangeSnapshot = domain.ChangeSnapshot{Files: files, CapturedAt: capturedAt}
				next.UpdatedAt = updatedAt
			}
		})
		read.Edits = append(slices.Clip(read.Edits), snapshot)
		return read
	}
	return SliceTransition{}
}
