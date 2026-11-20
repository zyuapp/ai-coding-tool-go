package application

import (
	"cmp"
	"errors"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The project a thread is in, or nil.
func ProjectFor(state *WorkspaceState, thread *domain.Thread) *domain.Project {
	if thread == nil || thread.ProjectID == nil || *thread.ProjectID == "" {
		return nil
	}
	for _, project := range state.Projects {
		if project.ID == *thread.ProjectID {
			return project
		}
	}
	return nil
}

// The checkout with that id, or nil (also for a nil id).
func WorktreeByID(state *WorkspaceState, worktreeID *string) *domain.Worktree {
	if worktreeID == nil || *worktreeID == "" {
		return nil
	}
	for _, worktree := range state.Worktrees {
		if worktree.ID == *worktreeID {
			return worktree
		}
	}
	return nil
}

// The project and checkout a send names, as they are kept here. The checkout says which project the
// thread belongs to, so a project that disagrees is a contradiction rather than something to
// silently pick a winner for. The error's text is what the user is told.
func NamedPlace(state *WorkspaceState, project, worktreeID *string) (*domain.Project, *domain.Worktree, error) {
	worktree := WorktreeByID(state, worktreeID)
	if worktreeID != nil && *worktreeID != "" && worktree == nil {
		return nil, nil, errors.New(WorktreeMissingError)
	}
	var named *domain.Project
	if project != nil {
		found, err := domain.FindProject(state.Projects, *project)
		if err != nil {
			return nil, nil, err
		}
		named = found
	}
	if worktree != nil && named != nil && named.ID != worktree.ProjectID {
		return nil, nil, errors.New(WorktreeElsewhereError)
	}
	return named, worktree, nil
}

// The checkout a thread works in, when it works in one rather than in its project.
func WorktreeFor(state *WorkspaceState, thread *domain.Thread) *domain.Worktree {
	if thread == nil {
		return nil
	}
	return WorktreeByID(state, thread.WorktreeID)
}

// Every thread still linked to a checkout, including archived threads.
func WorktreeClaimants(state *WorkspaceState, worktreeID string) []*domain.Thread {
	claimants := []*domain.Thread{}
	for _, thread := range state.Threads {
		if thread.WorktreeID != nil && *thread.WorktreeID == worktreeID {
			claimants = append(claimants, thread)
		}
	}
	return claimants
}

// The folder a thread works in: the checkout it shares once it has one, otherwise its project's.
func ThreadWorkspaceRoot(state *WorkspaceState, thread *domain.Thread) (string, bool) {
	if worktree := WorktreeFor(state, thread); worktree != nil {
		return worktree.Root, true
	}
	if project := ProjectFor(state, thread); project != nil {
		return project.Root, true
	}
	return "", false
}

// Where a file a message named is looked for, nearest the thread first: the checkout it works in,
// its project's own checkout, then the project's other checkouts, most recently used first.
func ThreadFileRoots(state *WorkspaceState, thread *domain.Thread) []string {
	project := ProjectFor(state, thread)
	var roots []string
	if worktree := WorktreeFor(state, thread); worktree != nil {
		roots = append(roots, worktree.Root)
	}
	if project != nil {
		roots = append(roots, project.Root)
		var siblings []*domain.Worktree
		for _, worktree := range state.Worktrees {
			if worktree.ProjectID == project.ID {
				siblings = append(siblings, worktree)
			}
		}
		slices.SortStableFunc(siblings, func(left, right *domain.Worktree) int {
			return cmp.Compare(right.LastUsedAt, left.LastUsedAt)
		})
		for _, worktree := range siblings {
			roots = append(roots, worktree.Root)
		}
	}
	unique := []string{}
	for _, root := range roots {
		if root != "" && !slices.Contains(unique, root) {
			unique = append(unique, root)
		}
	}
	return unique
}

// Where a thread's runs happen: the checkout it shares once it has one, otherwise its project's.
func ThreadWorkspaceID(state *WorkspaceState, thread *domain.Thread) (string, bool) {
	if worktree := WorktreeFor(state, thread); worktree != nil {
		return worktree.WorkspaceID, true
	}
	if project := ProjectFor(state, thread); project != nil && project.WorkspaceID != nil {
		return *project.WorkspaceID, true
	}
	return "", false
}

// Threads whose checkout is going: the ones that asked to leave, and every thread in a checkout the
// user is deleting, whichever thread or Settings asked for it.
func LeavingThreadIds(state *WorkspaceState) jsonx.Set[string] {
	leaving := jsonx.SetOf(state.ReleasingWorktrees...)
	if len(state.DeletingWorktrees) == 0 {
		return leaving
	}
	going := jsonx.Set[string]{}
	for _, worktree := range state.Worktrees {
		if slices.Contains(state.DeletingWorktrees, worktree.Root) {
			going[worktree.ID] = struct{}{}
		}
	}
	for _, thread := range state.Threads {
		if thread.WorktreeID != nil && *thread.WorktreeID != "" {
			if _, ok := going[*thread.WorktreeID]; ok {
				leaving[thread.ID] = struct{}{}
			}
		}
	}
	return leaving
}

// Where a thread's runs happen, including the two transitions that temporarily have no checkout.
func ThreadLocationLocationOf(state *WorkspaceState, thread *domain.Thread) ThreadLocation {
	if thread != nil {
		if _, ok := LeavingThreadIds(state)[thread.ID]; ok {
			return contracts.MobileLocationReleasing{}
		}
		if slices.Contains(state.CreatingWorktrees, thread.ID) {
			return contracts.MobileLocationCreating{}
		}
	}
	if worktree := WorktreeFor(state, thread); worktree != nil {
		return ThreadLocationWorktree{Worktree: *worktree, Threads: float64(len(WorktreeClaimants(state, worktree.ID)))}
	}
	return domain.WorktreeDestinationLocal{}
}
