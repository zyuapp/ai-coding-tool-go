package application

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

var WorktreeMenuInputs = []string{"worktree.menu-open", "worktree.menu-search"}

// The location menu's lists. `input` is one of the WorktreeMenuInputs.
func ReduceWorktreeMenu(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	var list WorktreeMenuList
	query := ""
	searching := false
	switch input := input.(type) {
	case contracts.WorktreeCommandWorktreeMenuOpen:
		list = WorktreeMenuList(input.List)
	case contracts.WorktreeCommandWorktreeMenuSearch:
		list, query, searching = WorktreeMenuList(input.List), input.Query, true
	default:
		return SliceTransition{}
	}
	search := maps.Clone(state.WorktreeMenuSearch)
	if search == nil {
		search = WorktreeMenuSearch{}
	}
	search[list] = query
	searched := SliceTransition{Patch: func(next *WorkspaceState) { next.WorktreeMenuSearch = search }}
	if searching || list == WorktreeMenuListThreads || state.WorktreeManagementLoading {
		return searched
	}
	// The checkouts offered are the ones beside the thread on screen, which a paired computer lists itself.
	if ActiveComputer(state) != nil {
		return searched
	}
	return SliceTransition{
		Patch: func(next *WorkspaceState) {
			next.WorktreeMenuSearch, next.WorktreeManagementLoading, next.WorktreeManagementError = search, true, nil
		},
		Effects: []WorkspaceEffect{WorkspaceEffectListWorktrees{}},
	}
}
