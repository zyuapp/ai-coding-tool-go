package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Deadlines are durable; expiry and the one timer watching them belong to the reducer.
func ReconcileSnoozes(previous *WorkspaceState, transition WorkspaceTransition, input WorkspaceInput) WorkspaceTransition {
	check := false
	switch input := input.(type) {
	case WorkspaceEventSnoozesElapsed, WorkspaceEventStoreLoaded, WorkspaceEventStoreAbsent:
		check = true
	case contracts.ViewCommandViewSetFocused:
		check = input.Focused
	}
	if !check && jsonx.Same(previous.Threads, transition.State.Threads) {
		return transition
	}
	before, hadBefore := domain.NextSnoozeExpiry(previous.Threads)
	next, hasNext := domain.NextSnoozeExpiry(transition.State.Threads)
	if check && hasNext {
		at := Now()
		if elapsed, ok := input.(WorkspaceEventSnoozesElapsed); ok {
			at = elapsed.At
		}
		if next <= at {
			threads := make([]*domain.Thread, len(transition.State.Threads))
			for index, thread := range transition.State.Threads {
				if thread.SnoozedUntil != nil && *thread.SnoozedUntil <= at {
					thread = domain.WithoutSnooze(thread)
				}
				threads[index] = thread
			}
			transition.State.Threads = threads
			next, hasNext = domain.NextSnoozeExpiry(threads)
		}
	}
	if hasNext == hadBefore && next == before && !(check && hasNext) {
		return transition
	}
	var at *float64
	if hasNext {
		at = &next
	}
	transition.Effects = append(transition.Effects[:len(transition.Effects):len(transition.Effects)], WorkspaceEffectScheduleSnoozeExpiry{At: at})
	return transition
}
