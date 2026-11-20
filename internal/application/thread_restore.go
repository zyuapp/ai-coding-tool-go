package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

// Threads as the app finds them on launch. None of their work outlived the app, so what was going stopped with it.

// The threads the store kept, the subagents each was following, and those whose run the app quit under.
func RestoredThreads(stored []*domain.StoredThread) (threads []*domain.Thread, subagents map[string][]*domain.Subagent, cutOff []*domain.Thread) {
	subagents = map[string][]*domain.Subagent{}
	cutOff = []*domain.Thread{}
	threads = make([]*domain.Thread, len(stored))
	for index, kept := range stored {
		if len(kept.Subagents) > 0 {
			restored := make([]*domain.Subagent, len(kept.Subagents))
			for at, subagent := range kept.Subagents {
				restored[at] = restoredSubagent(subagent)
			}
			subagents[kept.ID] = restored
		}
		saved := domain.RankedThread(kept)
		thread := restoredThread(saved)
		if thread != saved {
			cutOff = append(cutOff, thread)
		}
		threads[index] = thread
	}
	return threads, subagents, cutOff
}

// A turn cut off with the app reads as one the user stopped: it leaves the same verdict, unread unless
// the thread is on screen, and its coordinator here hears it as stopped, so nothing it hears wakes it
// by itself. A coordinator cut off keeps what news it held quiet, as one the user stopped does.
// Nothing is put on the desktop for it.
func WithCutOffTurnsEnded(state *WorkspaceState, cutOff []*domain.Thread, at float64) *WorkspaceState {
	quit := StopIntentQuit
	settlement := SettleTurn(TurnEnd{Status: TurnStatusCancelled, Stop: &quit})
	next := state
	for _, thread := range cutOff {
		next = WithSettledTurn(next, thread.ID, settlement, nil, at)
	}
	return next
}

// A subagent stored mid-work stopped with the app.
func restoredSubagent(subagent *domain.Subagent) *domain.Subagent {
	if subagent.Status != domain.SubagentStatusWorking {
		return subagent
	}
	stopped := *subagent
	stopped.Stopping = nil
	stopped.Status = domain.SubagentStatusStopped
	return &stopped
}

// A thread saved mid-turn was stopped with the app, a moment after the last thing it did: that is the
// turn's end its coordinator hears, here or on a paired computer, since it comes after anything that
// coordinator has already heard of it.
func restoredThread(thread *domain.Thread) *domain.Thread {
	if thread.ResourceStop != nil && thread.ResourceStop.Cleanup != domain.ResourceStopCleanupComplete {
		next := *thread
		stop := *thread.ResourceStop
		stop.Cleanup = domain.ResourceStopCleanupComplete
		next.ResourceStop = &stop
		thread = &next
	}
	if thread.RunOpen == nil || !*thread.RunOpen {
		return thread
	}
	moved := max(thread.UpdatedAt, domain.ThreadLastOutputAt(thread))
	if thread.Report != nil {
		moved = max(moved, thread.Report.At)
	}
	if thread.LastFindingAt != nil {
		moved = max(moved, *thread.LastFindingAt)
	}
	if thread.RemoteLead != nil && thread.RemoteLead.BackAt != nil {
		moved = max(moved, *thread.RemoteLead.BackAt)
	}
	for _, decision := range thread.Decisions {
		moved = max(moved, decision.RaisedAt)
	}
	next := *thread
	next.RunOpen = nil
	ended := moved + 1
	stopped := domain.ThreadOutcomeStopped
	next.RunEndedAt = &ended
	next.RankedAt = ended
	next.RunEndedAs = &stopped
	return &next
}
