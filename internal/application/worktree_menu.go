package application

import (
	"cmp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const WorktreeMenu = "session:location"

var activityLabels = map[ThreadStatus]string{ThreadStatusBlocked: "Needs input", ThreadStatusPaused: "Paused", ThreadStatusWorking: "Working"}

func status(state *WorkspaceState, thread *domain.Thread) string {
	if activity := ThreadActivityThreadStatus(state, thread.ID); activity != ThreadStatusIdle {
		return activityLabels[activity]
	}
	if thread.Outcome != nil {
		switch *thread.Outcome {
		case domain.ThreadOutcomeFailed:
			return "Failed"
		case domain.ThreadOutcomeStopped:
			return "Stopped"
		case domain.ThreadOutcomeFinished:
			return "Done"
		}
	}
	return "Idle"
}

// Local has no thread list. Worktree membership is independent of Priority and dismissal. The menu
// is `window`'s, which is the state on screen unless the thread is a paired computer's: its lists
// are that computer's, filled when this window opens the menu and narrowed by what is typed here.
// A nil `window` is `state`'s own; `current` may be nil. Nil when there is no menu to draw.
func WorktreeMenuWorktreeMenuView(state *WorkspaceState, current *domain.Thread, visible []*domain.Thread, window *WorktreeMenuState) *WorktreeMenuView {
	if window == nil {
		window = &WorktreeMenuState{OpenMenu: state.OpenMenu, WorktreeMenuSearch: state.WorktreeMenuSearch}
	}
	if current == nil {
		return nil
	}
	project := ProjectFor(state, current)
	if project == nil {
		return nil
	}
	busy := ThreadActivityThreadActivity(state).Working
	_, currentBusy := busy[current.ID]
	worktree := WorktreeFor(state, current)
	members := []*domain.Thread{}
	if worktree != nil {
		for _, thread := range visible {
			if thread.WorktreeID != nil && *thread.WorktreeID == worktree.ID {
				members = append(members, thread)
			}
		}
	}
	search := window.WorktreeMenuSearch
	open := window.OpenMenu != nil && *window.OpenMenu == WorktreeMenu
	threadQuery := strings.ToLower(projectsTrim(search[WorktreeMenuListThreads]))
	threads := []*WorktreeMenuChoice{}
	if open {
		matching := []*domain.Thread{}
		for _, thread := range members {
			if strings.Contains(strings.ToLower(thread.Title), threadQuery) {
				matching = append(matching, thread)
			}
		}
		isCurrent := func(thread *domain.Thread) int {
			if thread.ID == current.ID {
				return 1
			}
			return 0
		}
		slices.SortStableFunc(matching, func(a, b *domain.Thread) int {
			return cmp.Or(isCurrent(b)-isCurrent(a), cmp.Compare(b.RankedAt, a.RankedAt))
		})
		for _, thread := range matching {
			detail := "Current thread"
			if thread.ID != current.ID {
				detail = status(state, thread)
			}
			threads = append(threads, &WorktreeMenuChoice{ID: thread.ID, Title: thread.Title, Detail: detail, Current: new(thread.ID == current.ID), Command: contracts.TaskCommandTaskSelect{TaskID: thread.ID}})
		}
	}
	managed := map[string]*domain.ManagedWorktree{}
	for _, item := range state.ManagedWorktrees {
		managed[item.Root] = item
	}
	deleting := jsonx.SetOf(state.DeletingWorktrees...)
	releasing := jsonx.SetOf(state.ReleasingWorktrees...)
	releasingCheckouts := jsonx.Set[string]{}
	for _, thread := range state.Threads {
		if _, ok := releasing[thread.ID]; ok && thread.WorktreeID != nil && *thread.WorktreeID != "" {
			releasingCheckouts[*thread.WorktreeID] = struct{}{}
		}
	}
	destinations := []*WorktreeMenuChoice{}
	if open {
		query := strings.ToLower(projectsTrim(search[WorktreeMenuListDestinations]))
		checkouts := []*domain.Worktree{}
		for _, item := range state.Worktrees {
			if item.ProjectID == project.ID && (current.WorktreeID == nil || item.ID != *current.WorktreeID) {
				checkouts = append(checkouts, item)
			}
		}
		slices.SortStableFunc(checkouts, func(a, b *domain.Worktree) int { return cmp.Compare(b.LastUsedAt, a.LastUsedAt) })
		for _, checkout := range checkouts {
			record := managed[checkout.Root]
			if (state.ManagedWorktrees != nil && record == nil) || (record != nil && record.Repository == nil) {
				continue
			}
			name := domain.WorktreeName(checkout)
			branch := "Branch not loaded"
			if record != nil {
				branch = "Detached"
				if record.Branch != nil {
					branch = *record.Branch
				}
			}
			if environment, ok := state.Environments[checkout.WorkspaceID].(contracts.ChangedFilesResultAvailable); ok {
				branch = "Detached"
				if environment.Branch != nil {
					branch = *environment.Branch
				}
			}
			// The folder carries the ID the thread tools report, so either finds the checkout too.
			folder := checkout.Root[strings.LastIndex(checkout.Root, "/")+1:]
			if !strings.Contains(strings.ToLower(name+" "+branch+" "+folder+" "+checkout.ID), query) {
				continue
			}
			_, deletingRoot := deleting[checkout.Root]
			_, releasingCheckout := releasingCheckouts[checkout.ID]
			removing := deletingRoot || releasingCheckout
			if removing {
				branch = "Deleting…"
			}
			destinations = append(destinations, &WorktreeMenuChoice{
				ID: checkout.ID, Title: name, Detail: branch,
				Disabled: new(state.WorktreeManagementLoading || removing || currentBusy),
				Command:  contracts.TaskCommandTaskMoveWorktree{TaskID: &current.ID, Destination: domain.WorktreeDestinationWorktree{ID: checkout.ID}},
			})
		}
	}
	busyCount := 0.0
	if worktree != nil {
		for _, thread := range state.Threads {
			if _, ok := busy[thread.ID]; ok && thread.WorktreeID != nil && *thread.WorktreeID == worktree.ID {
				busyCount++
			}
		}
	}
	view := &WorktreeMenuView{
		ThreadID:     current.ID,
		ProjectID:    project.ID,
		Location:     ThreadLocationLocationOf(state, current),
		Count:        float64(len(members)),
		Threads:      threads,
		Destinations: destinations,
		Search:       search,
		CanMove:      !currentBusy && project.WorkspaceID != nil && *project.WorkspaceID != "",
		BusyCount:    busyCount,
		Loading:      state.WorktreeManagementLoading,
		Error:        state.WorktreeManagementError,
	}
	if worktree != nil {
		view.WorktreeID = &worktree.ID
		view.DeleteRoot = &worktree.Root
		_, deletingRoot := deleting[worktree.Root]
		_, releasingCheckout := releasingCheckouts[worktree.ID]
		view.CanDelete = busyCount == 0 && !deletingRoot && !releasingCheckout
	}
	return view
}
