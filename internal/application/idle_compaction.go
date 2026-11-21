package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	// Share of the cache window that passes before compacting, which leaves a few minutes to finish in.
	idleCompactionDueShare = 0.9
	// Past this, the cache may already be gone, so compacting would cost as much as the next message.
	idleCompactionLateMs = 60_000
	// Longer than any compaction takes; one still going by then is stopped so its thread is free again.
	idleCompactionLimitMs = 10 * 60_000
	// IdleCompactionMinTokens: a smaller thread costs too little to read again to be worth a summary.
	IdleCompactionMinTokens = 200_000
)

// idleCompactionDue is when a thread is due to be compacted, false when it never is: a large thread
// of an engine whose cache expires, whose session is still its own, idle since a run end not yet
// answered.
func idleCompactionDue(state *WorkspaceState, thread *domain.Thread) (float64, bool) {
	cacheMs := domain.CapabilitiesFor(string(thread.Engine)).IdleCompactionCacheMs
	if cacheMs == nil || thread.RunEndedAt == nil || thread.ContextUsage == nil || thread.ContextUsage.Tokens < IdleCompactionMinTokens {
		return 0, false
	}
	if thread.ArchivedAt != nil || thread.LimitPause != nil || runsIsTrue(thread.InheritedContinuation) {
		return 0, false
	}
	if mark := state.IdleCompactions[thread.ID]; mark != nil && mark.RunEndedAt == *thread.RunEndedAt {
		return 0, false
	}
	if !CompactableSession(state, thread) {
		return 0, false
	}
	return *thread.RunEndedAt + *cacheMs*idleCompactionDueShare, true
}

// idleCompactionDeadline is when the app's own compaction running on a thread is stopped, false for none.
func idleCompactionDeadline(state *WorkspaceState, taskID string) (float64, bool) {
	record := RunRecord(state.ActiveRuns[taskID])
	mark := state.IdleCompactions[taskID]
	if record == nil || !runsIsTrue(record.Idle) || runsIsTrue(record.Cancelling) || mark == nil || mark.StartedAt == nil {
		return 0, false
	}
	return *mark.StartedAt + idleCompactionLimitMs, true
}

// nextIdleCompaction is the next moment a compaction is due or one has run too long, nil for none.
func nextIdleCompaction(state *WorkspaceState) *float64 {
	var next *float64
	consider := func(at float64) {
		if next == nil || at < *next {
			next = &at
		}
	}
	for taskID := range state.ActiveRuns {
		if deadline, ok := idleCompactionDeadline(state, taskID); ok {
			consider(deadline)
		}
	}
	if state.IdleCompaction {
		for _, thread := range state.Threads {
			if due, ok := idleCompactionDue(state, thread); ok {
				consider(due)
			}
		}
	}
	return next
}

// idleCompactionAllowed reports whether a due thread may be compacted now: nobody is working in it,
// and its account is not billed outside a plan or running low on one. A plan not yet read, or a read
// that failed, does not stop it.
func idleCompactionAllowed(state *WorkspaceState, thread *domain.Thread) bool {
	if ThreadBusy(state, thread.ID) || len(QueuedFor(state, thread.ID)) > 0 {
		return false
	}
	if slices.ContainsFunc(state.Workflows[thread.ID], func(workflow *domain.Workflow) bool { return workflow.Status == domain.WorkflowStatusRunning }) ||
		slices.ContainsFunc(state.Subagents[thread.ID], func(subagent *domain.Subagent) bool { return subagent.Status == domain.SubagentStatusWorking }) {
		return false
	}
	report := mobilePlanReport(state.PlanUsage.Reports, thread.Engine)
	switch report.(type) {
	case domain.PlanUsageNotApplicable, *domain.PlanUsageNotApplicable:
		return false
	}
	catalog := CatalogOf(state)
	model, _ := domain.ThreadModel(&catalog, thread)
	tightest := domain.TightestWindow(domain.WindowsFor(report, domain.ModelLabel(&catalog, string(thread.Engine), string(model))))
	return tightest == nil || *tightest.Utilization < domain.NearLimitShare
}

// idleCompactionsAt stops each idle compaction that has run too long, and starts each one due by
// `at`. A thread whose time passed more than a minute ago, or that is in use, is let be until its
// next run ends.
func idleCompactionsAt(state *WorkspaceState, at float64) WorkspaceTransition {
	transition := Settled(state, nil, nil)
	marks := map[string]*IdleCompaction{}
	for _, thread := range state.Threads {
		if mark := state.IdleCompactions[thread.ID]; mark != nil {
			marks[thread.ID] = mark
		}
	}
	apply := func(step Step) {
		applied := Applied(&transition.State, SliceTransition(step))
		applied.Effects = slices.Concat(transition.Effects, applied.Effects)
		transition = applied
	}
	for _, thread := range state.Threads {
		if deadline, ok := idleCompactionDeadline(&transition.State, thread.ID); ok && deadline <= at {
			apply(Cancelling(&transition.State, transition.State.ActiveRuns[thread.ID]))
			continue
		}
		if !state.IdleCompaction {
			continue
		}
		due, ok := idleCompactionDue(state, thread)
		if !ok || due > at {
			continue
		}
		mark := &IdleCompaction{RunEndedAt: *thread.RunEndedAt}
		marks[thread.ID] = mark
		if at-due > idleCompactionLateMs || !idleCompactionAllowed(&transition.State, thread) {
			continue
		}
		step := CompactionRequested(&transition.State, thread.ID, true)
		if len(step.Effects) > 0 {
			mark.StartedAt = new(at)
			apply(step)
		}
	}
	if !maps.EqualFunc(marks, state.IdleCompactions, func(a, b *IdleCompaction) bool { return a == b }) {
		transition.State.IdleCompactions = marks
	}
	return transition
}

// ReconcileIdleCompaction compacts a large thread shortly before its engine's prompt cache expires,
// so coming back to it reads a summary instead of the whole thread at full price. One timer watches
// for the next one due, and for a compaction that has run too long.
func ReconcileIdleCompaction(previous *WorkspaceState, transition WorkspaceTransition, input WorkspaceInput) WorkspaceTransition {
	fired, isFired := input.(WorkspaceEventIdleCompactionDue)
	loaded := false
	switch input.(type) {
	case WorkspaceEventStoreLoaded, WorkspaceEventPreferencesLoaded:
		loaded = true
	}
	after := &transition.State
	if !isFired && !loaded && previous.IdleCompaction == after.IdleCompaction && jsonx.SameSlice(previous.Threads, after.Threads) && jsonx.SameMap(previous.ActiveRuns, after.ActiveRuns) {
		return transition
	}
	if isFired {
		started := idleCompactionsAt(after, fired.At)
		transition = WorkspaceTransition{State: started.State, Effects: slices.Concat(transition.Effects, started.Effects), Result: transition.Result}
	}
	next := nextIdleCompaction(&transition.State)
	switch {
	case isFired:
	case loaded:
		if next == nil {
			return transition
		}
	default:
		before := nextIdleCompaction(previous)
		if before == nil && next == nil || before != nil && next != nil && *before == *next {
			return transition
		}
	}
	transition.Effects = append(slices.Clip(transition.Effects), WorkspaceEffectScheduleIdleCompaction{At: next})
	return transition
}
