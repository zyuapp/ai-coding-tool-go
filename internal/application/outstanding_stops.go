package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// WithStopAll makes a coordinator's Stop all outstanding, with the effects that stop its threads' work on paired computers.
func WithStopAll(state *WorkspaceState, leadID string, cover StopAllCover, at float64) (*WorkspaceState, []WorkspaceEffect) {
	step := StoppedAll(state, leadID, cover, at)
	effects := step.Effects
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	return Afterward(state, SliceTransition(step)), effects
}

// StoppedAll is WithStopAll as the edits it makes: what the coordinator keeps of it, and what the runs here do.
func StoppedAll(state *WorkspaceState, leadID string, cover StopAllCover, at float64) Step {
	effects, stops := remoteStops(cover.Elsewhere, at)
	var kept []OwnerEdit
	if len(stops) > 0 || len(cover.Landing) > 0 {
		kept = append(kept, EditThread(ThreadsOf(state), leadID, func(item *domain.Thread) ThreadPatch {
			return func(next *domain.Thread) {
				if len(stops) > 0 {
					merged := maps.Clone(item.RemoteStops)
					if merged == nil {
						merged = map[string]*domain.RemoteStop{}
					}
					maps.Copy(merged, stops)
					next.RemoteStops = merged
				}
				if len(cover.Landing) > 0 {
					caught := maps.Clone(item.CaughtStarts)
					if caught == nil {
						caught = map[string]*domain.CaughtStart{}
					}
					for _, start := range cover.Landing {
						caught[start.ID] = &domain.CaughtStart{Computer: start.Computer}
					}
					next.CaughtStarts = caught
				}
			}
		}))
	}
	cancelled := make([]string, len(cover.Cancelled))
	for index, run := range cover.Cancelled {
		cancelled[index] = run.RunID
	}
	runs := StopAllIn(RunsOf(state), cancelled, cover.Hushed, state.Threads, at)
	return Step{Edits: append(kept, runs), Effects: effects}
}

// remoteStops is what stops the work of a coordinator's threads on paired computers, one batch per
// computer, and the stops kept by thread id.
func remoteStops(work []*RemoteWork, at float64) (effects []WorkspaceEffect, stops map[string]*domain.RemoteStop) {
	type batch struct {
		computer PairedComputer
		inputs   []WorkspaceInput
	}
	var order []string
	batches := map[string]*batch{}
	stops = map[string]*domain.RemoteStop{}
	for _, item := range work {
		thread, computer := item.Thread, item.Computer
		held := batches[computer.ID]
		if held == nil {
			held = &batch{computer: computer}
			batches[computer.ID] = held
			order = append(order, computer.ID)
		}
		capabilities := contracts.ComputerCapabilities(computer.Capabilities)
		cancel := contracts.RunControlCommandRunCancel{TaskID: new(thread.ID)}
		if item.RunID != nil && *item.RunID != "" && contracts.SupportsComputerCommand(capabilities, cancel) {
			held.inputs = append(held.inputs, cancel)
			stops[thread.ID] = &domain.RemoteStop{Computer: computer.ID, RunID: item.RunID}
		}
		lifted := contracts.RunControlCommandLimitCancel{TaskID: thread.ID}
		if thread.LimitPause != nil && contracts.SupportsComputerCommand(capabilities, lifted) {
			held.inputs = append(held.inputs, lifted)
			if domain.ResumesOnItsOwn(thread.LimitPause.Window) && stops[thread.ID] == nil {
				stops[thread.ID] = &domain.RemoteStop{Computer: computer.ID}
			}
		}
		for _, processID := range item.ProcessIDs {
			held.inputs = append(held.inputs, contracts.RunControlCommandRunStopProcess{TaskID: new(thread.ID), ProcessID: processID})
		}
		if len(item.ProcessIDs) > 0 {
			stop := domain.RemoteStop{Computer: computer.ID}
			if existing := stops[thread.ID]; existing != nil {
				stop = *existing
			}
			stop.Answer = &domain.RemoteStopAnswer{At: at}
			stops[thread.ID] = &stop
		}
	}
	effects = []WorkspaceEffect{}
	for _, id := range order {
		if held := batches[id]; len(held.inputs) > 0 {
			effects = append(effects, WorkspaceEffectComputerForward{ID: held.computer.ID, Inputs: held.inputs})
		}
	}
	return effects, stops
}

// StopIntentOf is who asked this run to end before it ended by itself: Stop all, whose stop it is
// heard as, or the user. Nil when nobody did.
func StopIntentOf(state *WorkspaceState, run ActiveRun) *StopIntent {
	record := RunRecord(run)
	if record == nil {
		return nil
	}
	if slices.Contains(state.QuietStops, record.RunID) {
		return new(StopIntentStopAll)
	}
	if runsIsTrue(record.Cancelling) {
		return new(StopIntentUser)
	}
	return nil
}

// WithRunAnswered marks the Stop all that cancelled this run answered by its end.
func WithRunAnswered(state *WorkspaceState, runID string) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{RunAnsweredIn(RunsOf(state), runID)}})
}

// WithTurnBegun spends what Stop all hushed of a thread as its turn begins, and reports whether the
// turn is the agent's answer to that stop.
func WithTurnBegun(state *WorkspaceState, taskID string, agentsOwn bool, at float64) (next *WorkspaceState, answersStop bool) {
	index := slices.IndexFunc(state.QuietSessions, func(session *QuietSession) bool { return session.TaskID == taskID })
	if index == -1 {
		return state, false
	}
	hushed := state.QuietSessions[index]
	copied := *state
	copied.QuietSessions = []*QuietSession{}
	for _, session := range state.QuietSessions {
		if session != hushed {
			copied.QuietSessions = append(copied.QuietSessions, session)
		}
	}
	return &copied, agentsOwn && AnswersInTime(hushed.At, at)
}

// OutstandingStopsRemoteTurnStop is what Stop all makes of a turn of a coordinator's thread on a
// paired computer, as that computer last said it stands. run is nil when none is going.
func OutstandingStopsRemoteTurnStop(lead *domain.Thread, member *domain.Thread, computer string, run ActiveRun, ended bool, at float64) RemoteTurnStop {
	var stop *domain.RemoteStop
	if sent := lead.RemoteStops[member.ID]; sent != nil && sent.Computer == computer {
		stop = sent
	}
	answered := ended && stop != nil && stop.Answer != nil && stop.Answer.RunID != nil
	var kept *domain.RemoteStop
	if stop != nil {
		kept = keptStop(stop, run, ended, answered, at)
	}
	spent := kept != stop
	landed, caught := "", false
	if ended {
		landed, caught = caughtStartOf(lead, member, computer)
	}
	return RemoteTurnStop{
		StopAll:  caught || stop != nil,
		Answered: answered,
		Spent:    spent,
		Held: func(item domain.Thread) domain.Thread {
			settled := &item
			if caught {
				settled = WithoutCaughtStart(settled, landed)
			}
			if spent {
				settled = withRemoteStop(settled, member.ID, kept)
			}
			return *settled
		},
	}
}

// keptStop is what is left of what Stop all stopped of a thread elsewhere, nil when spent.
func keptStop(stop *domain.RemoteStop, run ActiveRun, ended bool, answered bool, at float64) *domain.RemoteStop {
	answer := stop.Answer
	if record := RunRecord(run); record != nil {
		if stop.RunID != nil && record.RunID == *stop.RunID || answer != nil && answer.RunID != nil && record.RunID == *answer.RunID {
			return stop
		}
		if answer != nil && answer.RunID == nil && runsIsTrue(record.AgentInitiated) && AnswersInTime(answer.At, at) {
			return &domain.RemoteStop{Computer: stop.Computer, Answer: &domain.RemoteStopAnswer{At: answer.At, RunID: new(record.RunID)}}
		}
		return nil
	}
	if !ended {
		return stop
	}
	if answer != nil && !answered && answer.RunID == nil && stop.RunID != nil {
		return &domain.RemoteStop{Computer: stop.Computer, Answer: answer}
	}
	return nil
}

// withRemoteStop is a coordinator with what Stop all stopped of a thread elsewhere moved on, or done with for a nil stop.
func withRemoteStop(lead *domain.Thread, threadID string, stop *domain.RemoteStop) *domain.Thread {
	kept := maps.Clone(lead.RemoteStops)
	if kept == nil {
		kept = map[string]*domain.RemoteStop{}
	}
	delete(kept, threadID)
	if stop != nil {
		kept[threadID] = stop
	}
	next := *lead
	next.RemoteStops = nil
	if len(kept) > 0 {
		next.RemoteStops = kept
	}
	return &next
}

// caughtStartOf is the proposal id of the start Stop all caught on its way to computer that this thread is.
func caughtStartOf(lead *domain.Thread, member *domain.Thread, computer string) (string, bool) {
	var proposalID *string
	if member.RemoteLead != nil {
		proposalID = member.RemoteLead.ProposalID
	}
	for _, id := range slices.Sorted(maps.Keys(lead.CaughtStarts)) {
		start := lead.CaughtStarts[id]
		if start.Computer != computer {
			continue
		}
		if start.ThreadID == nil && proposalID != nil && id == *proposalID || start.ThreadID != nil && *start.ThreadID == member.ID {
			return id, true
		}
	}
	return "", false
}

// WithStopsKept lets go of what Stop all stopped on paired computers for threads those computers no longer hold under the coordinator.
func WithStopsKept(state *WorkspaceState) *WorkspaceState {
	next := state
	holderOf := func(id string) *PairedComputer {
		for _, computer := range state.Computers.Paired {
			if computer.ID == id {
				return computer
			}
		}
		return nil
	}
	for _, lead := range state.Threads {
		stops, starts := lead.RemoteStops, lead.CaughtStarts
		if stops == nil && starts == nil {
			continue
		}
		under := func(holder *WorkspaceState, threadID string) bool {
			return slices.ContainsFunc(RemotelyLedThreads(holder.Threads), func(thread *domain.Thread) bool {
				return thread.ID == threadID && thread.RemoteLead.ThreadID == lead.ID
			})
		}
		stopsKept := map[string]*domain.RemoteStop{}
		for threadID, stop := range stops {
			if holder := holderOf(stop.Computer); holder != nil && (holder.State == nil || under(holder.State, threadID)) {
				stopsKept[threadID] = stop
			}
		}
		startsKept := map[string]*domain.CaughtStart{}
		for id, start := range starts {
			holder := holderOf(start.Computer)
			if holder == nil {
				continue
			}
			if holder.State == nil || start.ThreadID == nil ||
				!slices.ContainsFunc(holder.State.Threads, func(thread *domain.Thread) bool { return thread.ID == *start.ThreadID }) ||
				under(holder.State, *start.ThreadID) {
				startsKept[id] = start
			}
		}
		if len(stopsKept) == len(stops) && len(startsKept) == len(starts) {
			continue
		}
		next = UpdateThread(next, lead.ID, func(thread *domain.Thread) *domain.Thread {
			kept := *thread
			kept.RemoteStops, kept.CaughtStarts = nil, nil
			if len(stopsKept) > 0 {
				kept.RemoteStops = stopsKept
			}
			if len(startsKept) > 0 {
				kept.CaughtStarts = startsKept
			}
			return &kept
		})
	}
	return next
}

// WithoutLapsedStops lets a coordinator that is no longer one go of what Stop all stopped under it.
func WithoutLapsedStops(state *WorkspaceState, threads []*ThreadChange) *WorkspaceState {
	next := state
	for _, change := range threads {
		after := change.After
		if after != nil && after.RemoteStops != nil && !domain.IsCoordinator(after) {
			next = UpdateThread(next, after.ID, func(thread *domain.Thread) *domain.Thread {
				kept := *thread
				kept.RemoteStops = nil
				return &kept
			})
		}
	}
	return next
}

// CaughtStartComputer is the paired computer a coordinator's start went to, when Stop all caught it on its way there.
func CaughtStartComputer(lead *domain.Thread, proposalID string) (string, bool) {
	if lead == nil {
		return "", false
	}
	caught := lead.CaughtStarts[proposalID]
	if caught == nil {
		return "", false
	}
	return caught.Computer, true
}

// CatchesStart reports whether Stop all caught a coordinator's start elsewhere on its way, and it has yet to be heard landing.
func CatchesStart(lead *domain.Thread, proposalID string) bool {
	if lead == nil {
		return false
	}
	caught := lead.CaughtStarts[proposalID]
	return caught != nil && caught.ThreadID == nil
}

// WithStartLanded is a caught start heard landing as threadID.
func WithStartLanded(lead *domain.Thread, proposalID string, threadID string) *domain.Thread {
	if !domain.IsCoordinator(lead) {
		return WithoutCaughtStart(lead, proposalID)
	}
	caught := maps.Clone(lead.CaughtStarts)
	if caught == nil {
		caught = map[string]*domain.CaughtStart{}
	}
	landed := domain.CaughtStart{}
	if held := lead.CaughtStarts[proposalID]; held != nil {
		landed = *held
	}
	landed.ThreadID = new(threadID)
	caught[proposalID] = &landed
	next := *lead
	next.CaughtStarts = caught
	return &next
}

// WithoutCaughtStart is a coordinator done with a start elsewhere Stop all caught on its way.
func WithoutCaughtStart(lead *domain.Thread, proposalID string) *domain.Thread {
	if lead.CaughtStarts[proposalID] == nil {
		return lead
	}
	rest := maps.Clone(lead.CaughtStarts)
	delete(rest, proposalID)
	next := *lead
	next.CaughtStarts = nil
	if len(rest) > 0 {
		next.CaughtStarts = rest
	}
	return &next
}
