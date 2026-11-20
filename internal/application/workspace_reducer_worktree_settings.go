package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func worktreeSettingsPatch(settings WorktreeSettingsState) OwnPatch {
	return func(next *WorkspaceState) { next.WorktreeSettings = settings }
}

// The Settings worktree page's own choices. `input` is a worktree.filter-project,
// worktree.confirm-delete, worktree.confirm-delete-safe, worktree.set-missing-open or
// worktree.set-threads-open command.
func ReduceWorktreeSettings(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	settings := state.WorktreeSettings
	switch input := input.(type) {
	case contracts.WorktreeCommandWorktreeFilterProject:
		settings.Project, settings.Confirming, settings.ConfirmingSafe = input.Project, nil, false
		return SliceTransition{Patch: worktreeSettingsPatch(settings)}
	case contracts.WorktreeCommandWorktreeConfirmDeleteSafe:
		settings.Confirming, settings.ConfirmingSafe = nil, input.Open
		return SliceTransition{Patch: worktreeSettingsPatch(settings)}
	case contracts.WorktreeCommandWorktreeConfirmDelete:
		if input.Root == nil {
			settings.Confirming = nil
			return SliceTransition{Patch: worktreeSettingsPatch(settings)}
		}
		root := *input.Root
		views := WorktreeSettingsViews(state, ThreadActivityThreadActivity(state).Working)
		if index := slices.IndexFunc(views, func(item *WorktreeSettingsView) bool { return item.Root == root }); index >= 0 {
			if worktree := views[index]; worktree.Busy || worktree.Deleting {
				return SliceTransition{}
			}
			settings.Confirming = &root
			return SliceTransition{Patch: worktreeSettingsPatch(settings)}
		}
		settings.Confirming = &root
		// A paired computer's checkout is judged by its list, which the confirmation is drawn from.
		if ActiveComputer(state) != nil {
			return SliceTransition{Patch: worktreeSettingsPatch(settings)}
		}
		index := slices.IndexFunc(state.Worktrees, func(item *domain.Worktree) bool { return item.Root == root })
		if index < 0 || slices.Contains(state.DeletingWorktrees, state.Worktrees[index].Root) {
			return SliceTransition{}
		}
		recorded := state.Worktrees[index]
		busy := ThreadActivityThreadActivity(state).Working
		if slices.ContainsFunc(WorktreeClaimants(state, recorded.ID), func(thread *domain.Thread) bool { _, ok := busy[thread.ID]; return ok }) {
			return SliceTransition{}
		}
		effects := []WorkspaceEffect{}
		if !state.WorktreeManagementLoading {
			effects = []WorkspaceEffect{WorkspaceEffectListWorktrees{}}
		}
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				next.WorktreeSettings, next.WorktreeManagementLoading, next.WorktreeManagementError = settings, true, nil
			},
			Effects: effects,
		}
	case contracts.WorktreeCommandWorktreeSetMissingOpen:
		settings.MissingOpen = new(input.Open)
		return SliceTransition{Patch: worktreeSettingsPatch(settings)}
	case contracts.WorktreeCommandWorktreeSetThreadsOpen:
		expanded := slices.DeleteFunc(slices.Clone(settings.ExpandedThreads), func(root string) bool { return root == input.Root })
		if expanded == nil {
			expanded = []string{}
		}
		if input.Open {
			expanded = append(expanded, input.Root)
		}
		settings.ExpandedThreads = expanded
		return SliceTransition{Patch: worktreeSettingsPatch(settings)}
	}
	return SliceTransition{}
}

// A thread a checkout in Settings lists, opened: Settings closes and the thread is selected.
func OpenWorktreeThread(state *WorkspaceState, input contracts.WorktreeCommandWorktreeOpenThread) Step {
	thread := threadByID(state.Threads, input.TaskID)
	if thread == nil || thread.WorktreeID == nil || *thread.WorktreeID == "" {
		return Step{}
	}
	return AndThen(state, SettingsShown(state, false, nil, nil), func(next *WorkspaceState) Step { return Selected(next, thread.ID) })
}
