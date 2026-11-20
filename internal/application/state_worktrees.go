package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialWorktreesState() WorktreesState {
	return WorktreesState{
		Worktrees:                 []*domain.Worktree{},
		ManagedWorktrees:          nil,
		WorktreeSettings:          WorktreeSettingsState{Project: nil, Confirming: nil, MissingOpen: nil, ExpandedThreads: []string{}, ConfirmingSafe: false, Bulk: nil},
		WorktreeMenuSearch:        WorktreeMenuSearch{WorktreeMenuListThreads: "", WorktreeMenuListDestinations: ""},
		WorktreeManagementLoading: false,
		WorktreeManagementError:   nil,
		WorktreeManagementNotice:  nil,
		CreatingWorktrees:         []string{},
		ReleasingWorktrees:        []string{},
		DeletingWorktrees:         []string{},
		MovesAfterRun:             map[string]*domain.MoveAfterRun{},
		WorktreeMove:              nil,
	}
}

func WithoutWorktreeRoot(state *WorkspaceState, root string) []string {
	kept := []string{}
	for _, item := range state.DeletingWorktrees {
		if item != root {
			kept = append(kept, item)
		}
	}
	return kept
}
