package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The checkouts as one input leaves them. Only the functions here make one, so another feature changes them through these.
func worktreesChange(view WorktreesState) WorktreesEdit {
	return WorktreesEdit{View: view}
}

func WorktreesOf(state *WorkspaceState) WorktreesEdit {
	return worktreesChange(WorktreesState{
		Worktrees: state.Worktrees, ManagedWorktrees: state.ManagedWorktrees, WorktreeSettings: state.WorktreeSettings,
		WorktreeMenuSearch: state.WorktreeMenuSearch, WorktreeManagementLoading: state.WorktreeManagementLoading,
		WorktreeManagementError: state.WorktreeManagementError, WorktreeManagementNotice: state.WorktreeManagementNotice,
		CreatingWorktrees: state.CreatingWorktrees, ReleasingWorktrees: state.ReleasingWorktrees, DeletingWorktrees: state.DeletingWorktrees,
		MovesAfterRun: state.MovesAfterRun, WorktreeMove: state.WorktreeMove,
	})
}

// The confirmation of a move between checkouts, put away.
func MoveDismissedIn(edit WorktreesEdit) WorktreesEdit {
	next := edit.View
	next.WorktreeMove = nil
	return worktreesChange(next)
}

// Threads gone for good, no longer making or letting go of a checkout, or moving once their run ends.
func CheckoutClaimsForgottenIn(edit WorktreesEdit, removed jsonx.Set[string]) WorktreesEdit {
	next := edit.View
	gone := func(threadID string) bool { _, ok := removed[threadID]; return ok }
	next.CreatingWorktrees = WithoutMatching(next.CreatingWorktrees, gone)
	next.ReleasingWorktrees = WithoutMatching(next.ReleasingWorktrees, gone)
	next.MovesAfterRun = WithoutThreadKeys(next.MovesAfterRun, removed)
	return worktreesChange(next)
}

// Settings shown or put away. Opening them on the worktrees page reads the list; putting them away
// forgets what the page was in the middle of, keeping only a bulk delete that is still going.
func ManagementShownIn(edit WorktreesEdit, open bool, onPage bool) WorktreesEdit {
	next := edit.View
	next.WorktreeManagementLoading = next.WorktreeManagementLoading || (open && onPage)
	if !open {
		next.WorktreeSettings = WorktreeSettingsState{Project: nil, Confirming: nil, MissingOpen: nil, ExpandedThreads: []string{}, ConfirmingSafe: false, Bulk: next.WorktreeSettings.Bulk}
	}
	return worktreesChange(next)
}

// A checkout takes minutes to make, and until it lands the thread is neither where it was nor where
// it is going. Marking it here is what keeps a second ask from making a second, orphaned checkout.
func CreatingWorktree(state *WorkspaceState, taskID string) Step {
	next := WorktreesOf(state).View
	next.CreatingWorktrees = append(slices.Clip(next.CreatingWorktrees), taskID)
	return Step{Edits: []OwnerEdit{worktreesChange(next)}, ClearsError: true}
}

func WithoutCreatingWorktree(state *WorkspaceState, taskID string) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{CreatedIn(WorktreesOf(state), taskID)}})
}

// A thread whose checkout is no longer being made, whether it landed or not.
func CreatedIn(edit WorktreesEdit, taskID string) WorktreesEdit {
	if !slices.Contains(edit.View.CreatingWorktrees, taskID) {
		return edit
	}
	next := edit.View
	next.CreatingWorktrees = slices.DeleteFunc(slices.Clone(next.CreatingWorktrees), func(item string) bool { return item == taskID })
	return worktreesChange(next)
}

// Snapshotting a checkout and taking the directory away is as slow as making one, and the thread is
// still standing in it meanwhile. Marking it here is what says so and what refuses a second ask.
func ReleasingIn(edit WorktreesEdit, taskIDs []string) WorktreesEdit {
	var added []string
	for _, taskID := range taskIDs {
		if !slices.Contains(edit.View.ReleasingWorktrees, taskID) {
			added = append(added, taskID)
		}
	}
	if len(added) == 0 {
		return edit
	}
	next := edit.View
	next.ReleasingWorktrees = slices.Concat(next.ReleasingWorktrees, added)
	return worktreesChange(next)
}

// A move a thread asked to make once its run ends, or none (nil).
func MoveAfterRunIn(edit WorktreesEdit, taskID string, move *domain.MoveAfterRun) WorktreesEdit {
	next := edit.View
	if move != nil {
		next.MovesAfterRun = maps.Clone(next.MovesAfterRun)
		if next.MovesAfterRun == nil {
			next.MovesAfterRun = map[string]*domain.MoveAfterRun{}
		}
		next.MovesAfterRun[taskID] = move
		return worktreesChange(next)
	}
	if next.MovesAfterRun[taskID] == nil {
		return edit
	}
	next.MovesAfterRun = maps.Clone(next.MovesAfterRun)
	delete(next.MovesAfterRun, taskID)
	return worktreesChange(next)
}

func WithoutReleasingWorktree(state *WorkspaceState, taskIDs []string) *WorkspaceState {
	going := jsonx.SetOf(taskIDs...)
	leaving := func(taskID string) bool { _, ok := going[taskID]; return ok }
	if !slices.ContainsFunc(state.ReleasingWorktrees, leaving) {
		return state
	}
	next := *state
	next.ReleasingWorktrees = slices.DeleteFunc(slices.Clone(state.ReleasingWorktrees), leaving)
	return &next
}

// The checkout itself is gone, so every thread that claimed it is local again and hears why, and a
// draft pointed at it goes back to the project. The record goes with the directory: nothing is left
// pointing at a folder that is not there. `threads` is the list as the input has it so far.
func WorktreeDropped(state *WorkspaceState, threads ThreadsEdit, worktreeID string, note func() *domain.ConversationMessage) Step {
	var gone *domain.Worktree
	if index := slices.IndexFunc(state.Worktrees, func(worktree *domain.Worktree) bool { return worktree.ID == worktreeID }); index >= 0 {
		gone = state.Worktrees[index]
	}
	claimants := jsonx.Set[string]{}
	for _, thread := range WorktreeClaimants(state, worktreeID) {
		claimants[thread.ID] = struct{}{}
	}
	view := WorktreesOf(state).View
	view.ReleasingWorktrees = slices.DeleteFunc(slices.Clone(view.ReleasingWorktrees), func(taskID string) bool { _, ok := claimants[taskID]; return ok })
	view.Worktrees = slices.DeleteFunc(slices.Clone(view.Worktrees), func(worktree *domain.Worktree) bool { return worktree.ID == worktreeID })
	edits := []OwnerEdit{worktreesChange(view)}
	if gone != nil {
		edits = append(edits, EnvironmentForgottenIn(GitOf(state), gone.WorkspaceID))
	}
	edits = append(edits, DraftCheckoutGoneIn(NavigationOf(state), worktreeID), CheckoutGoneIn(threads, worktreeID, note, Now))
	return Step{Edits: edits}
}

// Hands back a checkout only when every linked thread explicitly leaves it.
func ReleaseWorktrees(state *WorkspaceState, leaving []*domain.Thread) []WorkspaceEffectReleaseWorktree {
	going := jsonx.Set[string]{}
	for _, thread := range leaving {
		going[thread.ID] = struct{}{}
	}
	released := jsonx.Set[string]{}
	effects := []WorkspaceEffectReleaseWorktree{}
	for _, thread := range leaving {
		worktree := WorktreeFor(state, thread)
		if worktree == nil {
			continue
		}
		if _, ok := released[worktree.ID]; ok {
			continue
		}
		if slices.ContainsFunc(WorktreeClaimants(state, worktree.ID), func(claimant *domain.Thread) bool { _, ok := going[claimant.ID]; return !ok }) {
			continue
		}
		released[worktree.ID] = struct{}{}
		effects = append(effects, WorkspaceEffectReleaseWorktree{TaskID: thread.ID, WorktreeID: worktree.ID, Root: worktree.Root, Title: thread.Title})
	}
	return effects
}

// Records a checkout a run just made, and marks the one the run happens in as touched. `created` and
// `worktreeID` may be nil.
func WithUsedWorktree(state *WorkspaceState, created *domain.Worktree, worktreeID *string) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{UsedWorktreeIn(WorktreesOf(state), created, worktreeID)}})
}

// WithUsedWorktree, on the checkouts an input is changing.
func UsedWorktreeIn(edit WorktreesEdit, created *domain.Worktree, worktreeID *string) WorktreesEdit {
	if worktreeID == nil || *worktreeID == "" {
		return edit
	}
	known := edit.View.Worktrees
	if created != nil && !slices.ContainsFunc(known, func(item *domain.Worktree) bool { return item.ID == created.ID }) {
		known = append(slices.Clip(known), created)
	}
	worktrees := make([]*domain.Worktree, len(known))
	for index, item := range known {
		if item.ID != *worktreeID {
			worktrees[index] = item
			continue
		}
		used := *item
		used.LastUsedAt = Now()
		worktrees[index] = &used
	}
	next := edit.View
	next.Worktrees = worktrees
	return worktreesChange(next)
}
