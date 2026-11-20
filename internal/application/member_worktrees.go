package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var noMemberWorktrees = []*MemberWorktree{}

func add(groups map[string][]*domain.Thread, worktreeID string, thread *domain.Thread) {
	groups[worktreeID] = append(groups[worktreeID], thread)
}

// The checkouts a coordinator's threads work in, most recently used first. Reads threads, worktrees,
// environments, managedWorktrees, deletingWorktrees and releasingWorktrees of `state`. `lead` may be nil.
func MemberWorktreesView(state *WorkspaceState, lead *domain.Thread, busy jsonx.Set[string]) []*MemberWorktree {
	if lead == nil {
		return noMemberWorktrees
	}
	members := map[string][]*domain.Thread{}
	claimants := map[string][]*domain.Thread{}
	for _, thread := range state.Threads {
		if thread.WorktreeID == nil || *thread.WorktreeID == "" {
			continue
		}
		add(claimants, *thread.WorktreeID, thread)
		if thread.ParentID != nil && *thread.ParentID == lead.ID && (thread.Role == nil || *thread.Role != domain.ThreadRoleCoordinator) {
			add(members, *thread.WorktreeID, thread)
		}
	}
	if len(members) == 0 {
		return noMemberWorktrees
	}
	managed := map[string]*domain.ManagedWorktree{}
	for _, item := range state.ManagedWorktrees {
		managed[item.Root] = item
	}
	deleting := jsonx.SetOf(state.DeletingWorktrees...)
	releasing := jsonx.SetOf(state.ReleasingWorktrees...)
	var checkouts []*domain.Worktree
	for _, worktree := range state.Worktrees {
		if _, ok := members[worktree.ID]; ok {
			checkouts = append(checkouts, worktree)
		}
	}
	slices.SortStableFunc(checkouts, func(a, b *domain.Worktree) int { return coordCompare(b.LastUsedAt, a.LastUsedAt) })
	views := make([]*MemberWorktree, len(checkouts))
	for index, worktree := range checkouts {
		standing := claimants[worktree.ID]
		var branch *string
		if environment, ok := state.Environments[worktree.WorkspaceID].(contracts.ChangedFilesResultAvailable); ok {
			branch = environment.Branch
		} else if held := managed[worktree.Root]; held != nil {
			branch = held.Branch
		}
		views[index] = &MemberWorktree{
			ID:       worktree.ID,
			Root:     worktree.Root,
			Name:     domain.WorktreeName(worktree),
			Branch:   branch,
			Threads:  members[worktree.ID],
			Busy:     slices.ContainsFunc(standing, func(thread *domain.Thread) bool { return busy.Has(thread.ID) }),
			Deleting: deleting.Has(worktree.Root) || slices.ContainsFunc(standing, func(thread *domain.Thread) bool { return releasing.Has(thread.ID) }),
		}
	}
	return views
}
