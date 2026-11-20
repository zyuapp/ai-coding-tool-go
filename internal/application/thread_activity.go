package application

import (
	"cmp"
	"maps"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Where each of one computer's threads stands: working, blocked on the user, paused at a usage limit,
// or idle, and the lists its threads are drawn from. This computer's state and every paired
// computer's state are read through the same functions, each answer derived once per state.

// The activity of the states read last (TS: a WeakMap keyed by state). An entry is found by the
// identity of every field deriveActivity reads, so a state copied with those untouched shares it.
var activities struct {
	sync.Mutex
	entries []activityEntry
}

type activityEntry struct {
	reads    activityReads
	activity *ThreadActivity
}

// activityReads is every field of a state that deriveActivity reads.
type activityReads struct {
	threads             []*domain.Thread
	activeRuns          map[string]ActiveRun
	pendingRuns         map[string]*PendingRun
	creatingWorktrees   []string
	releasingWorktrees  []string
	deletingWorktrees   []string
	worktrees           []*domain.Worktree
	workflows           map[string][]*domain.Workflow
	subagents           map[string][]*domain.Subagent
	queuedMessages      map[string][]*QueuedMessage
	backgroundProcesses map[string][]*domain.BackgroundProcess
	automations         []*domain.AutomationView
}

func activityReadsOf(state *WorkspaceState) activityReads {
	return activityReads{
		threads: state.Threads, activeRuns: state.ActiveRuns, pendingRuns: state.PendingRuns,
		creatingWorktrees: state.CreatingWorktrees, releasingWorktrees: state.ReleasingWorktrees, deletingWorktrees: state.DeletingWorktrees,
		worktrees: state.Worktrees, workflows: state.Workflows, subagents: state.Subagents, queuedMessages: state.QueuedMessages,
		backgroundProcesses: state.BackgroundProcesses, automations: state.Automations,
	}
}

func (reads activityReads) same(other activityReads) bool {
	return jsonx.SameSlice(reads.threads, other.threads) && jsonx.SameMap(reads.activeRuns, other.activeRuns) &&
		jsonx.SameMap(reads.pendingRuns, other.pendingRuns) && jsonx.SameSlice(reads.creatingWorktrees, other.creatingWorktrees) &&
		jsonx.SameSlice(reads.releasingWorktrees, other.releasingWorktrees) && jsonx.SameSlice(reads.deletingWorktrees, other.deletingWorktrees) &&
		jsonx.SameSlice(reads.worktrees, other.worktrees) && jsonx.SameMap(reads.workflows, other.workflows) &&
		jsonx.SameMap(reads.subagents, other.subagents) && jsonx.SameMap(reads.queuedMessages, other.queuedMessages) &&
		jsonx.SameMap(reads.backgroundProcesses, other.backgroundProcesses) && jsonx.SameSlice(reads.automations, other.automations)
}

// How many states' answers the caches hold: this computer's, and each paired computer's beside it.
const threadsCacheSize = 8

// The one answer the sidebar, thread tools, scheduler, and coordinators all read, derived once per
// state. Anything the thread will carry on with unattended is working; a pause shows in place of it.
// The answer is shared: callers must not modify it.
func ThreadActivityThreadActivity(state *WorkspaceState) *ThreadActivity {
	reads := activityReadsOf(state)
	activities.Lock()
	for _, held := range activities.entries {
		if held.reads.same(reads) {
			activities.Unlock()
			return held.activity
		}
	}
	activities.Unlock()
	activity := deriveActivity(state)
	activities.Lock()
	defer activities.Unlock()
	activities.entries = append(activities.entries, activityEntry{reads: reads, activity: activity})
	if len(activities.entries) > threadsCacheSize {
		activities.entries = slices.Delete(activities.entries, 0, 1)
	}
	return activity
}

func deriveActivity(state *WorkspaceState) *ThreadActivity {
	working := jsonx.Set[string]{}
	blocked := jsonx.Set[string]{}
	for taskID, run := range state.ActiveRuns {
		working[taskID] = struct{}{}
		if record := RunRecord(run); record != nil && record.Status == ActiveRunRunningStatusAwaitingApproval {
			blocked[record.TaskID] = struct{}{}
		}
	}
	for taskID := range StartingThreads(state) {
		working[taskID] = struct{}{}
	}
	for _, taskID := range state.CreatingWorktrees {
		working[taskID] = struct{}{}
	}
	// A checkout on its way out is ground about to move, so every thread standing on it waits.
	for taskID := range LeavingThreadIds(state) {
		working[taskID] = struct{}{}
	}
	// A workflow outlives the run that started it. Background shells and monitors only keep a session alive.
	for taskID, workflows := range state.Workflows {
		if runsWorkflow(workflows) {
			working[taskID] = struct{}{}
		}
	}
	for taskID, subagents := range state.Subagents {
		if runsSubagent(subagents) {
			working[taskID] = struct{}{}
		}
	}
	paused := jsonx.Set[string]{}
	for _, thread := range state.Threads {
		if thread.LimitPause == nil {
			continue
		}
		paused[thread.ID] = struct{}{}
		if domain.ResumesOnItsOwn(thread.LimitPause.Window) {
			working[thread.ID] = struct{}{}
		}
	}
	// A paused thread's queue is what it holds for the user's return, not work of its own.
	for taskID, queued := range state.QueuedMessages {
		if len(queued) > 0 && !paused.Has(taskID) {
			working[taskID] = struct{}{}
		}
	}
	monitoring := jsonx.Set[string]{}
	for taskID, processes := range state.BackgroundProcesses {
		if !working.Has(taskID) && slices.ContainsFunc(processes, func(process *domain.BackgroundProcess) bool { return process.Stopping == nil || !*process.Stopping }) {
			monitoring[taskID] = struct{}{}
		}
	}
	ranked := working
	if len(monitoring) > 0 {
		ranked = maps.Clone(working)
		maps.Copy(ranked, monitoring)
	}
	automations := make([]*domain.Automation, len(state.Automations))
	for index, view := range state.Automations {
		automations[index] = &view.Automation
	}
	return &ThreadActivity{Working: working, Blocked: blocked, Paused: paused, Monitoring: monitoring, Ranked: domain.WithWatchedThreads(ranked, automations)}
}

// Whether a thread's session holds work a fresh one would silently end: a native goal, a workflow,
// a background shell or monitor, or a subagent still working.
func SessionHoldsWork(state *WorkspaceState, taskID string) bool {
	return state.Goals[taskID] != nil ||
		runsWorkflow(state.Workflows[taskID]) ||
		len(state.BackgroundProcesses[taskID]) > 0 ||
		runsSubagent(state.Subagents[taskID])
}

// What a thread's session keeps working on past its run, which a stop of the run leaves going: each
// running workflow and working subagent not already on its way to stopping.
func UnstoppedSessionWork(state *WorkspaceState, taskID string) []string {
	ids := []string{}
	for _, workflow := range state.Workflows[taskID] {
		if workflow.Status == domain.WorkflowStatusRunning && (workflow.Stopping == nil || !*workflow.Stopping) {
			ids = append(ids, workflow.ID)
		}
	}
	for _, subagent := range state.Subagents[taskID] {
		if subagent.Status == domain.SubagentStatusWorking && (subagent.Stopping == nil || !*subagent.Stopping) {
			ids = append(ids, subagent.ID)
		}
	}
	return ids
}

func runsWorkflow(workflows []*domain.Workflow) bool {
	return slices.ContainsFunc(workflows, func(workflow *domain.Workflow) bool { return workflow.Status == domain.WorkflowStatusRunning })
}

func runsSubagent(subagents []*domain.Subagent) bool {
	return slices.ContainsFunc(subagents, func(subagent *domain.Subagent) bool { return subagent.Status == domain.SubagentStatusWorking })
}

func IsWorking(state *WorkspaceState, taskID string) bool {
	return ThreadActivityThreadActivity(state).Working.Has(taskID)
}

// Whether a thread is doing work: it has work going, and is not stopped on a question for the user.
func DoingWork(working, blocked jsonx.Set[string], taskID string) bool {
	return working.Has(taskID) && !blocked.Has(taskID)
}

func ThreadActivityThreadStatus(state *WorkspaceState, taskID string) ThreadStatus {
	activity := ThreadActivityThreadActivity(state)
	switch {
	case activity.Blocked.Has(taskID):
		return ThreadStatusBlocked
	case activity.Paused.Has(taskID):
		return ThreadStatusPaused
	case activity.Working.Has(taskID):
		return ThreadStatusWorking
	}
	return ThreadStatusIdle
}

// What a thread (nil for none), or the draft typing under `draftKey`, is waiting on before its run can go; false for nothing.
func WaitingOn(state *WorkspaceState, taskID *string, draftKey string) (ThreadWait, bool) {
	if taskID != nil && *taskID != "" {
		if slices.Contains(state.CreatingWorktrees, *taskID) {
			return ThreadWaitWorktree, true
		}
		if LeavingThreadIds(state).Has(*taskID) {
			return ThreadWaitWorktreeRelease, true
		}
	}
	// TS takes the first pending run in insertion order; Go maps have none, so the lowest id stands in.
	var resolving *PendingRun
	for _, id := range slices.Sorted(maps.Keys(state.PendingRuns)) {
		pending := state.PendingRuns[id]
		if taskID != nil && pending.TaskID != nil && *pending.TaskID == *taskID || pending.DraftKey != nil && *pending.DraftKey == draftKey {
			resolving = pending
			break
		}
	}
	if resolving == nil {
		return "", false
	}
	if resolving.CreatingWorktree != nil && *resolving.CreatingWorktree {
		return ThreadWaitWorktree, true
	}
	return ThreadWaitRun, true
}

// Lists read only threads and side chats, so a state that keeps both keeps its lists too. Holds the
// lists of the threads and side chats read last, by identity (TS: a WeakMap keyed by the threads).
var lists struct {
	sync.Mutex
	entries []threadListsEntry
}

type threadListsEntry struct {
	threads   []*domain.Thread
	sideChats []*SideChat
	lists     *ThreadLists
}

// The answer is shared: callers must not modify it.
func ThreadActivityThreadLists(state *WorkspaceState) *ThreadLists {
	lists.Lock()
	for _, held := range lists.entries {
		if jsonx.SameSlice(held.threads, state.Threads) && jsonx.SameSlice(held.sideChats, state.SideChats) {
			lists.Unlock()
			return held.lists
		}
	}
	lists.Unlock()
	derived := deriveLists(state)
	lists.Lock()
	defer lists.Unlock()
	lists.entries = append(lists.entries, threadListsEntry{threads: state.Threads, sideChats: state.SideChats, lists: derived})
	if len(lists.entries) > threadsCacheSize {
		lists.entries = slices.Delete(lists.entries, 0, 1)
	}
	return derived
}

func deriveLists(state *WorkspaceState) *ThreadLists {
	forked := SideChatIds(state)
	listedThreads := state.Threads
	if len(state.SideChats) > 0 {
		listedThreads = []*domain.Thread{}
		for _, thread := range state.Threads {
			if !forked.Has(thread.ID) {
				listedThreads = append(listedThreads, thread)
			}
		}
	}
	visibleThreads := []*domain.Thread{}
	archivedThreads := []*domain.Thread{}
	worktreeThreadIDs := jsonx.Set[string]{}
	for _, thread := range listedThreads {
		if thread.ArchivedAt == nil {
			visibleThreads = append(visibleThreads, thread)
		} else {
			archivedThreads = append(archivedThreads, thread)
		}
		if thread.WorktreeID != nil && *thread.WorktreeID != "" {
			worktreeThreadIDs[thread.ID] = struct{}{}
		}
	}
	slices.SortStableFunc(archivedThreads, func(left, right *domain.Thread) int { return cmp.Compare(*right.ArchivedAt, *left.ArchivedAt) })
	sideChatAttention, unreadCount := UnreadView(state, visibleThreads)
	return &ThreadLists{
		ListedThreads:     listedThreads,
		VisibleThreads:    visibleThreads,
		ArchivedThreads:   archivedThreads,
		WorktreeThreadIDs: worktreeThreadIDs,
		SideChatAttention: sideChatAttention,
		UnreadCount:       unreadCount,
	}
}

// The threads an input replaced, found by reference, so an input that touched one thread costs a pointer scan.
func ThreadChanges(before, after []*domain.Thread) []*ThreadChange {
	if jsonx.SameSlice(before, after) {
		return []*ThreadChange{}
	}
	if len(before) == len(after) {
		changes := []*ThreadChange{}
		aligned := true
		for index := 0; index < len(after) && aligned; index++ {
			was, is := before[index], after[index]
			if was == is {
				continue
			}
			if was.ID == is.ID {
				changes = append(changes, &ThreadChange{ID: is.ID, Before: was, After: is})
			} else {
				aligned = false
			}
		}
		if aligned {
			return changes
		}
	}
	previous := make(map[string]*domain.Thread, len(before))
	order := make([]string, 0, len(before))
	for _, thread := range before {
		if _, seen := previous[thread.ID]; !seen {
			order = append(order, thread.ID)
		}
		previous[thread.ID] = thread
	}
	changes := []*ThreadChange{}
	for _, thread := range after {
		was, ok := previous[thread.ID]
		delete(previous, thread.ID)
		if was != thread {
			change := &ThreadChange{ID: thread.ID, After: thread}
			if ok {
				change.Before = was
			}
			changes = append(changes, change)
		}
	}
	for _, id := range order {
		if was, ok := previous[id]; ok {
			changes = append(changes, &ThreadChange{ID: id, Before: was})
		}
	}
	return changes
}

// Threads whose activity may differ between two states: every input deriveActivity reads,
// compared by reference first, so a streamed event that changes no run's status finds nothing.
func ActivityChanges(before, after *WorkspaceState, threads []*ThreadChange) jsonx.Set[string] {
	changed := jsonx.Set[string]{}
	changedKeys(before.ActiveRuns, after.ActiveRuns, func(was, is ActiveRun) bool {
		wasRecord, isRecord := RunRecord(was), RunRecord(is)
		if wasRecord == nil || isRecord == nil {
			return wasRecord == nil && isRecord == nil
		}
		return wasRecord.RunID == isRecord.RunID && wasRecord.Status == isRecord.Status
	}, changed)
	if !jsonx.SameMap(before.PendingRuns, after.PendingRuns) {
		ids := jsonx.Set[string]{}
		for id := range before.PendingRuns {
			ids[id] = struct{}{}
		}
		for id := range after.PendingRuns {
			ids[id] = struct{}{}
		}
		for id := range ids {
			was, is := before.PendingRuns[id], after.PendingRuns[id]
			if was == is {
				continue
			}
			if was != nil && was.TaskID != nil && *was.TaskID != "" {
				changed[*was.TaskID] = struct{}{}
			}
			if is != nil && is.TaskID != nil && *is.TaskID != "" {
				changed[*is.TaskID] = struct{}{}
			}
		}
	}
	if !jsonx.SameSlice(before.CreatingWorktrees, after.CreatingWorktrees) {
		symmetricDifference(jsonx.SetOf(before.CreatingWorktrees...), jsonx.SetOf(after.CreatingWorktrees...), changed)
	}
	if !jsonx.SameSlice(before.ReleasingWorktrees, after.ReleasingWorktrees) || !jsonx.SameSlice(before.DeletingWorktrees, after.DeletingWorktrees) || !jsonx.SameSlice(before.Worktrees, after.Worktrees) {
		symmetricDifference(LeavingThreadIds(before), LeavingThreadIds(after), changed)
	}
	changedKeys(before.Workflows, after.Workflows, func(was, is []*domain.Workflow) bool { return runsWorkflow(was) == runsWorkflow(is) }, changed)
	changedKeys(before.Subagents, after.Subagents, func(was, is []*domain.Subagent) bool { return runsSubagent(was) == runsSubagent(is) }, changed)
	changedKeys(before.QueuedMessages, after.QueuedMessages, func(was, is []*QueuedMessage) bool { return (len(was) > 0) == (len(is) > 0) }, changed)
	for _, change := range threads {
		if change.Before == nil || change.After == nil || change.Before.LimitPause != change.After.LimitPause || !threadsSameString(change.Before.WorktreeID, change.After.WorktreeID) {
			changed[change.ID] = struct{}{}
		}
	}
	return changed
}

// Adds to `changed` every key whose value differs by identity between the maps and `same` does not
// excuse. An absent value reaches `same` as the zero value.
func changedKeys[V any](before, after map[string]V, same func(was, is V) bool, changed jsonx.Set[string]) {
	if jsonx.SameMap(before, after) {
		return
	}
	for key, is := range after {
		if was := before[key]; !jsonx.Same(was, is) && !same(was, is) {
			changed[key] = struct{}{}
		}
	}
	var absent V
	for key, was := range before {
		if _, ok := after[key]; !ok && !same(was, absent) {
			changed[key] = struct{}{}
		}
	}
}

func symmetricDifference(before, after, changed jsonx.Set[string]) {
	for id := range before {
		if !after.Has(id) {
			changed[id] = struct{}{}
		}
	}
	for id := range after {
		if !before.Has(id) {
			changed[id] = struct{}{}
		}
	}
}
