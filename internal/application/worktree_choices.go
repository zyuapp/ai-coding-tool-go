package application

import (
	"cmp"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The project's checkouts a draft can start in, most recently used first, without the one it is in.
// `except` may be nil.
func WorktreeChoices(state *WorkspaceState, projectID string, except *string) []*contracts.MobileWorktreeChoice {
	var managed map[string]*domain.ManagedWorktree
	if state.ManagedWorktrees != nil {
		managed = make(map[string]*domain.ManagedWorktree, len(state.ManagedWorktrees))
		for _, item := range state.ManagedWorktrees {
			managed[item.Root] = item
		}
	}
	candidates := []*domain.Worktree{}
	for _, worktree := range state.Worktrees {
		if worktree.ProjectID != projectID || (except != nil && worktree.ID == *except) || slices.Contains(state.DeletingWorktrees, worktree.Root) {
			continue
		}
		if managed != nil {
			if record := managed[worktree.Root]; record == nil || record.Repository == nil || *record.Repository == "" {
				continue
			}
		}
		candidates = append(candidates, worktree)
	}
	slices.SortStableFunc(candidates, func(left, right *domain.Worktree) int { return cmp.Compare(right.LastUsedAt, left.LastUsedAt) })
	choices := make([]*contracts.MobileWorktreeChoice, len(candidates))
	for index, worktree := range candidates {
		var branch *string
		if environment, ok := state.Environments[worktree.WorkspaceID].(contracts.ChangedFilesResultAvailable); ok {
			branch = environment.Branch
		} else if record := managed[worktree.Root]; record != nil {
			branch = record.Branch
		}
		choices[index] = &contracts.MobileWorktreeChoice{ID: worktree.ID, Name: domain.WorktreeName(worktree), Branch: branch}
	}
	return choices
}
