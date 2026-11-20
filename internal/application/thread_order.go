package application

import (
	"cmp"
	"math"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Every order a thread list is drawn in lives here: OrderThreads where the user put each row,
// RecentFirst newest `rankedAt` first, and Snoozed's next to wake first. Lists take one of
// these rather than sorting threads themselves, so no row moves while its run speaks.

// Newest `rankedAt` first, in a new slice.
func RecentFirst(threads []*domain.Thread) []*domain.Thread {
	sorted := slices.Clone(threads)
	if sorted == nil {
		sorted = []*domain.Thread{}
	}
	slices.SortStableFunc(sorted, func(left, right *domain.Thread) int { return cmp.Compare(right.RankedAt, left.RankedAt) })
	return sorted
}

// The threads last active where a new thread is about to start: one project's (nil for the chats
// that have none). A thread working under a coordinator is reached through it, so it is left out.
func RecentIn(threads []*domain.Thread, projectID *string, limit int) []*domain.Thread {
	members := domain.CoordinatedThreadIds(threads)
	kept := []*domain.Thread{}
	for _, thread := range threads {
		if _, member := members[thread.ID]; !member && threadsSameString(thread.ProjectID, projectID) && thread.ArchivedAt == nil {
			kept = append(kept, thread)
		}
	}
	recent := RecentFirst(kept)
	return recent[:min(len(recent), max(0, limit))]
}

// Ranks threads by what wants the user rather than by where they live. A thread leads when it is
// blocked on the user, or when it is idle and its last run left a verdict or a run found something;
// a thread still working belongs among the runs however it ended last time. Reviewers stay in Threads
// unless blocked on the user. A snoozed thread waits in Snoozed, the next to wake first. Every thread
// appears once. Running holds its rows where the user put them.
func ThreadOrderActivitySections(threads []*domain.Thread, busy, blocked jsonx.Set[string]) ActivitySections {
	priority := []*domain.Thread{}
	running := []*domain.Thread{}
	settled := []*domain.Thread{}
	snoozed := []*domain.Thread{}
	for _, thread := range threads {
		_, isBlocked := blocked[thread.ID]
		_, isBusy := busy[thread.ID]
		switch {
		case thread.SnoozedUntil != nil:
			snoozed = append(snoozed, thread)
		case isBlocked:
			priority = append(priority, thread)
		case thread.Role != nil && *thread.Role == domain.ThreadRoleReviewer:
			settled = append(settled, thread)
		case isBusy:
			running = append(running, thread)
		case domain.WantsAttention(thread):
			priority = append(priority, thread)
		default:
			settled = append(settled, thread)
		}
	}
	slices.SortStableFunc(snoozed, func(left, right *domain.Thread) int { return cmp.Compare(*left.SnoozedUntil, *right.SnoozedUntil) })
	return ActivitySections{
		ActivitySectionsKeyPriority: RecentFirst(priority),
		ActivitySectionsKeyRunning:  OrderThreads(running),
		ActivitySectionsKeyThreads:  RecentFirst(settled),
		ActivitySectionsKeySnoozed:  snoozed,
	}
}

// Sidebar order. `sortIndex` wins so rows never move on their own; threads stored before
// sortIndex existed fall back to recency until BackfillSortIndex pins them down.
func compareThreads(left, right *domain.Thread) int {
	switch {
	case left.SortIndex != nil && right.SortIndex != nil:
		return cmp.Compare(*left.SortIndex, *right.SortIndex)
	case left.SortIndex != nil:
		return -1
	case right.SortIndex != nil:
		return 1
	}
	return cmp.Compare(right.RankedAt, left.RankedAt)
}

// The threads in sidebar order: the same slice when already in order, else a sorted copy.
func OrderThreads(threads []*domain.Thread) []*domain.Thread {
	for index := 1; index < len(threads); index++ {
		if compareThreads(threads[index-1], threads[index]) > 0 {
			sorted := slices.Clone(threads)
			slices.SortStableFunc(sorted, compareThreads)
			return sorted
		}
	}
	return threads
}

// Freezes the order threads loaded in, so a first launch after upgrading keeps the list the user last saw.
func BackfillSortIndex(threads []*domain.Thread) []*domain.Thread {
	if !slices.ContainsFunc(threads, func(thread *domain.Thread) bool { return thread.SortIndex == nil }) {
		return threads
	}
	positions := map[string]float64{}
	for index, thread := range OrderThreads(threads) {
		positions[thread.ID] = float64(index)
	}
	backfilled := make([]*domain.Thread, len(threads))
	for index, thread := range threads {
		if thread.SortIndex == nil {
			next := *thread
			position := positions[thread.ID]
			next.SortIndex = &position
			thread = &next
		}
		backfilled[index] = thread
	}
	return backfilled
}

func NextSortIndex(threads []*domain.Thread) float64 {
	lowest := 0.0
	for _, thread := range threads {
		if thread.SortIndex != nil {
			lowest = math.Min(lowest, *thread.SortIndex)
		} else {
			lowest = math.Min(lowest, 0)
		}
	}
	return lowest - 1
}

// Why a thread can reorder within its folder but not be carried to another, or false when it can:
// it lives on another computer (`host`, only Name is read), works in a checkout cut from one project,
// or is drawn beneath its coordinator.
func ThreadMoveBlock(thread *domain.Thread, host *ThreadHost, coordinated bool) (string, bool) {
	switch {
	case host != nil:
		return "Stays in its folder on " + host.Name, true
	case thread.WorktreeID != nil && *thread.WorktreeID != "":
		return "Stays in its worktree's folder", true
	case coordinated:
		return "Stays with its coordinator", true
	}
	return "", false
}

// Moves one thread into `target` and renumbers the visible list. `target.Index` counts rows in the
// destination group with the moved thread already taken out, matching what a drop reports. A group is
// one list in the sidebar: a project's threads, or the project-less list. A thread
// ThreadMoveBlock pins reorders only within its own group.
func MoveThread(threads []*domain.Thread, threadID string, target domain.ThreadDropTarget) []*domain.Thread {
	members := domain.CoordinatedThreadIds(threads)
	var moving *domain.Thread
	for _, thread := range threads {
		if thread.ID == threadID && thread.ArchivedAt == nil {
			moving = thread
			break
		}
	}
	projectID := target.ProjectID
	if moving == nil {
		return threads
	}
	if _, member := members[moving.ID]; !threadsSameString(projectID, moving.ProjectID) {
		if _, blocked := ThreadMoveBlock(moving, nil, member); blocked {
			return threads
		}
	}
	// Threads under a coordinator are drawn beneath it, so a drop's index never counts them, nor places one.
	visibleThreads := []*domain.Thread{}
	for _, thread := range threads {
		if _, member := members[thread.ID]; thread.ArchivedAt == nil && !member {
			visibleThreads = append(visibleThreads, thread)
		}
	}
	visible := OrderThreads(visibleThreads)
	if !slices.Contains(visible, moving) {
		return threads
	}
	rest := make([]*domain.Thread, 0, len(visible))
	for _, thread := range visible {
		if thread != moving {
			rest = append(rest, thread)
		}
	}
	var group []*domain.Thread
	for _, thread := range rest {
		if threadsSameString(thread.ProjectID, projectID) {
			group = append(group, thread)
		}
	}
	index := 0
	if at := math.Max(0, target.Index); at == math.Trunc(at) && at < float64(len(group)) {
		index = slices.Index(rest, group[int(at)])
	} else if len(group) > 0 {
		index = slices.Index(rest, group[len(group)-1]) + 1
	}
	detached := *moving
	detached.ProjectID = nil
	if projectID != nil {
		id := *projectID
		detached.ProjectID = &id
	}
	rest = slices.Insert(rest, index, &detached)
	moved := make(map[string]*domain.Thread, len(rest))
	for position, thread := range rest {
		next := *thread
		sortIndex := float64(position)
		next.SortIndex = &sortIndex
		moved[thread.ID] = &next
	}
	result := make([]*domain.Thread, len(threads))
	for at, thread := range threads {
		result[at] = thread
		next, ok := moved[thread.ID]
		if !ok {
			continue
		}
		if !(thread.SortIndex != nil && *next.SortIndex == *thread.SortIndex && threadsSameString(next.ProjectID, thread.ProjectID)) {
			result[at] = next
		}
	}
	return result
}

var activityOrder = []ActivitySectionsKey{ActivitySectionsKeyPriority, ActivitySectionsKeyRunning, ActivitySectionsKeyThreads, ActivitySectionsKeySnoozed}

// The threads a digit reaches: the first nine rows the sidebar draws, top to bottom. A folded
// section draws no rows, so the threads under it have no shortcut slot.
func SlotThreadIds(sections domain.SidebarSections, activityThreads ActivitySections, limit int) []string {
	ids := []string{}
	for _, section := range activityOrder {
		if !sections[domain.SidebarSection(section)] {
			continue
		}
		for _, thread := range activityThreads[section] {
			if len(ids) == limit {
				break
			}
			ids = append(ids, thread.ID)
		}
	}
	return ids
}

// threadsSameString is TypeScript's === on two optional strings.
func threadsSameString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
