package application

import (
	"maps"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What a coordinator here hears of the threads it started on paired computers, and how they are let go once it is gone.

// RemoteNews with its state by pointer, so whether it moved is read by identity.
type remoteHeard struct {
	state   *WorkspaceState
	effects []WorkspaceEffect
	leads   jsonx.Set[string]
}

// What paired computers' states say of the threads coordinators here started there, read against
// how far each coordinator has heard each thread, so news from while a line was down is caught up
// once it is back rather than lost. Only a computer whose threads, runs or line moved is read, and
// of its threads only those a coordinator elsewhere started. A thread whose work stopped or started
// there may free or hold its coordinator's wake, so that coordinator is looked at again too.
func CoordinationRemoteRemoteNews(previous, next *WorkspaceState) *RemoteNews {
	news := remoteNewsOf(previous, next)
	return &RemoteNews{State: *news.state, Effects: news.effects, Leads: news.leads}
}

func remoteNewsOf(previous, next *WorkspaceState) *remoteHeard {
	news := &remoteHeard{state: next, effects: []WorkspaceEffect{}, leads: jsonx.Set[string]{}}
	if jsonx.SameSlice(previous.Computers.Paired, next.Computers.Paired) {
		return news
	}
	before := map[string]*PairedComputer{}
	for _, computer := range previous.Computers.Paired {
		before[computer.ID] = computer
	}
	for _, computer := range next.Computers.Paired {
		was := before[computer.ID]
		delete(before, computer.ID)
		holder := computer.State
		var prior *WorkspaceState
		if was != nil {
			prior = was.State
		}
		if holder == nil || prior != nil && was.Status == computer.Status && jsonx.SameSlice(prior.Threads, holder.Threads) && jsonx.SameMap(prior.ActiveRuns, holder.ActiveRuns) {
			continue
		}
		memberThreads := RemotelyLedThreads(holder.Threads)
		var earlier []*domain.Thread
		if prior != nil {
			earlier = RemotelyLedThreads(prior.Threads)
		}
		away := LeadAwayThreads(holder.Threads)
		if len(memberThreads) == 0 && len(earlier) == 0 && len(away) == 0 {
			continue
		}
		working := LastWorkingIds(holder)
		workedBefore := jsonx.Set[string]{}
		if prior != nil {
			workedBefore = LastWorkingIds(prior)
		}
		kept := jsonx.Set[string]{}
		for _, thread := range memberThreads {
			kept[thread.ID] = struct{}{}
		}
		// A thread that left, or stopped being one of a coordinator's, may have been what held its coordinator's news.
		for _, thread := range earlier {
			if !kept.Has(thread.ID) && workedBefore.Has(thread.ID) {
				news.leads[thread.RemoteLead.ThreadID] = struct{}{}
			}
		}
		for _, member := range memberThreads {
			lead := coordThread(news.state.Threads, member.RemoteLead.ThreadID)
			if lead == nil || !domain.IsCoordinator(lead) {
				caughtLanding(news, member, computer)
				continue
			}
			if working.Has(member.ID) != workedBefore.Has(member.ID) {
				news.leads[lead.ID] = struct{}{}
			}
			heardState, heardEffects := memberNews(news.state, lead, member, computer, holder, prior)
			if heardState != news.state {
				news.leads[lead.ID] = struct{}{}
			}
			// A thread the user allowed there settles its card as it shows up, even when that computer's answer was lost.
			landed := heardState
			var landedEffects []WorkspaceEffect
			if proposalID := member.RemoteLead.ProposalID; proposalID != nil && *proposalID != "" {
				step := landedStep(heardState, lead.ID, *proposalID, member.ID, member.RunEndedAt, computer)
				landed = Afterward(heardState, SliceTransition(step))
				landedEffects = step.Effects
			}
			news.state = landed
			news.effects = slices.Concat(news.effects, heardEffects, landedEffects)
		}
		for _, thread := range away {
			caughtLanding(news, thread, computer)
		}
	}
	// A computer no longer paired takes its threads' work with it.
	for _, gone := range before {
		if gone.State == nil {
			continue
		}
		working := LastWorkingIds(gone.State)
		for _, thread := range RemotelyLedThreads(gone.State.Threads) {
			if working.Has(thread.ID) {
				news.leads[thread.RemoteLead.ThreadID] = struct{}{}
			}
		}
	}
	kept := keptWork(previous, news)
	if state := WithStopsKept(kept.state); state != kept.state {
		return &remoteHeard{state: state, effects: kept.effects, leads: kept.leads}
	}
	return kept
}

// A start Stop all caught on its way, showing up under a coordinator that is no longer one, or is
// away to it, is stopped all the same. Writes into `news`.
func caughtLanding(news *remoteHeard, thread *domain.Thread, computer *PairedComputer) {
	leadID, proposalID := thread.RemoteLead.ThreadID, thread.RemoteLead.ProposalID
	lead := coordThread(news.state.Threads, leadID)
	if proposalID == nil || *proposalID == "" || !CatchesStart(lead, *proposalID) {
		return
	}
	step := landedStep(news.state, leadID, *proposalID, thread.ID, thread.RunEndedAt, computer)
	news.state = Afterward(news.state, SliceTransition(step))
	news.effects = slices.Concat(news.effects, step.Effects)
}

// Each coordinator here keeps which of its threads each paired computer last said was working, and
// their titles, so a restart here, after which a computer says nothing until its line is back, does
// not take that silence for their work ending. What a computer says anew replaces what it said
// before, and one no longer paired keeps nothing. A coordinator whose kept work changed is looked at again.
func keptWork(previous *WorkspaceState, news *remoteHeard) *remoteHeard {
	before := map[string]*WorkspaceState{}
	for _, computer := range previous.Computers.Paired {
		before[computer.ID] = computer.State
	}
	paired := jsonx.Set[string]{}
	fresh := jsonx.Set[string]{}
	said := map[string]map[string]*domain.HeardWork{}
	for _, computer := range news.state.Computers.Paired {
		paired[computer.ID] = struct{}{}
		holder := computer.State
		if prior, ok := before[computer.ID]; holder == nil || ok && holder == prior {
			continue
		}
		fresh[computer.ID] = struct{}{}
		working := LastWorkingIds(holder)
		for _, thread := range RemotelyLedThreads(holder.Threads) {
			if !working.Has(thread.ID) {
				continue
			}
			leadID := thread.RemoteLead.ThreadID
			if said[leadID] == nil {
				said[leadID] = map[string]*domain.HeardWork{}
			}
			said[leadID][thread.ID] = &domain.HeardWork{Computer: computer.ID, Title: thread.Title}
		}
	}
	state := news.state
	leads := maps.Clone(news.leads)
	for _, lead := range news.state.Threads {
		if lead.RemoteWorking == nil && said[lead.ID] == nil {
			continue
		}
		working := map[string]*domain.HeardWork{}
		for threadID, work := range lead.RemoteWorking {
			if paired.Has(work.Computer) && !fresh.Has(work.Computer) {
				working[threadID] = work
			}
		}
		if domain.IsCoordinator(lead) {
			maps.Copy(working, said[lead.ID])
		}
		held := lead.RemoteWorking
		if len(working) == len(held) && !slices.ContainsFunc(slices.Collect(maps.Keys(working)), func(threadID string) bool {
			was := held[threadID]
			return was == nil || was.Computer != working[threadID].Computer || was.Title != working[threadID].Title
		}) {
			continue
		}
		state = UpdateThread(state, lead.ID, func(thread *domain.Thread) *domain.Thread {
			next := *thread
			next.RemoteWorking = nil
			if len(working) > 0 {
				next.RemoteWorking = working
			}
			return &next
		})
		leads[lead.ID] = struct{}{}
	}
	if state == news.state {
		return news
	}
	return &remoteHeard{state: state, effects: news.effects, leads: leads}
}

// What a coordinator hears of one of its threads on a paired computer since it last heard of it, on
// that computer's clock: that it showed up there, quietly, then each decision it raised, the report
// it made, and its turn ending, as a thread here would have been heard. What the user would have been
// told of a thread here is told of it here, under the coordinator's notices, and what that computer
// carries of the same is dropped: what its scheduled runs found, and its turns ending and waiting on
// the user as a thread here is heard, for a run someone started. What Stop all stopped is heard
// quietly. `prior` is nil when that computer had said nothing before.
func memberNews(state *WorkspaceState, lead, member *domain.Thread, computer *PairedComputer, holder, prior *WorkspaceState) (*WorkspaceState, []WorkspaceEffect) {
	heard, heardBefore := lead.RemoteHeard[member.ID]
	// What it did while its coordinator was away is none of that coordinator's news, as for a thread here.
	back := member.RemoteLead.BackAt
	since := max(coordIf(heardBefore, heard, 0), coordValueOr(back, 0))
	at := Now()
	var facts []MemberFact
	newest := max(coordIf(heardBefore, heard, domain.ThreadCreatedAt(member)), coordValueOr(back, 0))
	runValue := holder.ActiveRuns[member.ID]
	run := RunRecord(runValue)
	var ran *ActiveRunRunning
	if prior != nil {
		ran = RunRecord(prior.ActiveRuns[member.ID])
	}
	var runID *string
	if run != nil {
		runID = &run.RunID
	}
	if !heardBefore && back == nil {
		facts = append(facts, MemberFactArrived{Computer: computer.Name})
	}
	for _, decision := range domain.OpenDecisions(member) {
		if decision.RaisedAt <= since {
			continue
		}
		facts = append(facts, MemberFactRaised{Decision: *decision, RunID: runID})
		newest = max(newest, decision.RaisedAt)
	}
	if report := member.Report; report != nil && report.At > since && report.State != domain.CoordinationStateWorking {
		facts = append(facts, MemberFactReported{Report: *report, RunID: runID})
		newest = max(newest, report.At)
	}
	for _, finding := range member.Findings {
		if finding.At <= since {
			continue
		}
		facts = append(facts, MemberFactFound{Headline: finding.Headline})
		newest = max(newest, finding.At)
	}
	var endedAt *float64
	if member.RunEndedAt != nil && *member.RunEndedAt > since {
		endedAt = member.RunEndedAt
	}
	// A run that computer last said was going and that is gone with no word of its end, as one an older
	// build there lost to quitting, ended its turn all the same. Only a quiet scheduled run that found
	// nothing puts its thread back as if it never ran, and compacting is no turn at all.
	lost := ran != nil && !ran.Quiet && (ran.Operation == nil || *ran.Operation != ActiveRunRunningOperationCompact) && endedAt == nil
	// A thread waiting out a session limit carries on by itself, so its turn has not ended.
	ended := (endedAt != nil || lost) && run == nil && !(member.LimitPause != nil && domain.ResumesOnItsOwn(member.LimitPause.Window))
	stop := OutstandingStopsRemoteTurnStop(lead, member, computer.ID, runValue, ended, at)
	if ended {
		// So is one a start Stop all caught on its way stopped as it landed, unless it finished or failed before that.
		ending, _ := domain.LastRunOutcome(member)
		stopped := !stop.Answered && stop.StopAll && domain.EndsAsStopped(ending)
		status := TurnStatusSucceeded
		switch {
		case stopped || ending == domain.ThreadOutcomeStopped:
			status = TurnStatusCancelled
		case ending == domain.ThreadOutcomeFailed:
			status = TurnStatusFailed
		}
		// Said as a run here would say it: a failure in the words that computer left on the thread, and a
		// finish only for a run someone started, so one this computer never saw go speaks only if it failed.
		// One lost without a word has no finish to tell. A turn of the user's own there is heard with the
		// next wake, as one here is.
		var surfacing *RunSurfacing
		if status == TurnStatusFailed {
			surfacing = new(RunSurfacingFailed)
		} else if status == TurnStatusSucceeded && ran != nil && ran.Origin != ActiveRunRunningOriginAutomation && !lost {
			surfacing = new(RunSurfacingAttended)
		}
		tell := &MemberFactEndedTell{Surfacing: surfacing}
		if count := len(member.Messages); count > 0 {
			if last := member.Messages[count-1]; last.Kind == domain.ConversationMessageKindSystem && last.Tone != nil && *last.Tone == "error" {
				tell.Failure = &last.Text
			}
		}
		var intent *StopIntent
		if stop.StopAll {
			intent = new(StopIntentStopAll)
		}
		usersOwn := ran != nil && coordTrue(ran.UsersOwn)
		facts = append(facts, MemberFactEnded{Status: status, Quiet: EndsQuietly(status, intent, stop.Answered, usersOwn), Tell: tell})
		if endedAt != nil {
			newest = max(newest, *endedAt)
		}
	}
	// Waiting on the user is said as it starts, as it is of a thread here.
	if run != nil && run.Origin == ActiveRunRunningOriginComposer && computer.Status == domain.ComputerStatusConnected {
		if run.Status == ActiveRunRunningStatusAwaitingApproval {
			approval := holder.Approvals[run.RunID]
			if approval != nil && (ran == nil || ran.RunID != run.RunID || ran.Status != ActiveRunRunningStatusAwaitingApproval) {
				facts = append(facts, MemberFactWaiting{On: MemberFactWaitingOn{ToolName: &approval.ToolName}})
			}
		}
		if len(run.Questions) > 0 && !(ran != nil && ran.RunID == run.RunID && len(ran.Questions) > 0) {
			facts = append(facts, MemberFactWaiting{On: MemberFactWaitingOn{Question: &run.Questions[0].Question}})
		}
	}
	told := MemberFactsHeardFacts(state, lead, member, facts, at)
	if len(told.Notes) == 0 && heardBefore && newest == heard && !stop.Spent {
		return state, told.Effects
	}
	// Never forgotten: a thread still under it there, and, while a paired computer has yet to say what
	// it holds since this app started, each it has heard that no computer that has answered holds,
	// which may be that computer's.
	led := jsonx.Set[string]{}
	for _, item := range CoordinationMembers(state, lead.ID) {
		if item.Computer != nil {
			led[item.Thread.ID] = struct{}{}
		}
	}
	if slices.ContainsFunc(state.Computers.Paired, func(item *PairedComputer) bool { return item.State == nil }) {
		held := heldThreadIds(state.Computers.Paired)
		for threadID := range lead.RemoteHeard {
			if !held.Has(threadID) {
				led[threadID] = struct{}{}
			}
		}
	}
	kept := stop.Held(*lead)
	next := domain.WithRemoteHeard(&kept, member.ID, newest, led)
	for _, note := range told.Notes {
		next = domain.WithCoordinationNote(next, note)
	}
	return UpdateThread(state, lead.ID, func(*domain.Thread) *domain.Thread { return next }), told.Effects
}

// coordIf is `value` when `ok`, else `fallback`.
func coordIf[T any](ok bool, value, fallback T) T {
	if ok {
		return value
	}
	return fallback
}

// Every thread the paired computers that have answered hold, held for the last list of them.
var heldIds struct {
	sync.Mutex
	paired []*PairedComputer
	ids    jsonx.Set[string]
}

func heldThreadIds(paired []*PairedComputer) jsonx.Set[string] {
	heldIds.Lock()
	defer heldIds.Unlock()
	if heldIds.ids != nil && jsonx.SameSlice(heldIds.paired, paired) {
		return heldIds.ids
	}
	ids := jsonx.Set[string]{}
	for _, computer := range paired {
		if computer.State == nil {
			continue
		}
		for _, thread := range computer.State.Threads {
			ids[thread.ID] = struct{}{}
		}
	}
	heldIds.paired, heldIds.ids = paired, ids
	return ids
}

// Each thread on a paired computer working under a thread here that is no longer a live coordinator
// is told its coordinator is away, so it stands on its own as this computer's own threads do once
// theirs goes, and is told it is back once it is a coordinator again, as this computer's own are back
// under it. One under a coordinator gone from here is let go for good. It is read off what each
// computer says it holds, so one that was away, or whose state comes in only after its line is back,
// is told once it says so, and what could not be carried is tried again the next time its threads move.
func Released(previous, state *WorkspaceState, threads []*ThreadChange) []WorkspaceEffect {
	changed := slices.ContainsFunc(threads, func(change *ThreadChange) bool {
		return domain.IsCoordinator(change.Before) != domain.IsCoordinator(change.After)
	})
	if !changed && jsonx.SameSlice(previous.Computers.Paired, state.Computers.Paired) {
		return nil
	}
	before := map[string]*PairedComputer{}
	for _, computer := range previous.Computers.Paired {
		before[computer.ID] = computer
	}
	var effects []WorkspaceEffect
	for _, computer := range state.Computers.Paired {
		if computer.Status != domain.ComputerStatusConnected || computer.State == nil {
			continue
		}
		was := before[computer.ID]
		if !changed && was != nil && was.Status == domain.ComputerStatusConnected && was.State != nil && jsonx.SameSlice(was.State.Threads, computer.State.Threads) {
			continue
		}
		held := computer.State.Threads
		var inputs []WorkspaceInput
		for _, thread := range slices.Concat(RemotelyLedThreads(held), LeadAwayThreads(held)) {
			for _, command := range leadChange(state, thread, computer) {
				inputs = append(inputs, command)
			}
		}
		if len(inputs) > 0 {
			effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: inputs, Silent: new(true)})
		}
	}
	return effects
}

// What a thread on a paired computer is to be told of the coordinator it names, if that is one here
// whose standing it has not been told. A computer from before a coordinator could be away lets the
// thread go for good instead.
func leadChange(state *WorkspaceState, thread *domain.Thread, computer *PairedComputer) []contracts.AppCommand {
	leadID, away := thread.RemoteLead.ThreadID, coordTrue(thread.RemoteLead.Away)
	lead := coordThread(state.Threads, leadID)
	release := contracts.TaskCommandTaskSetCoordinator{TaskID: thread.ID, CoordinatorID: nil}
	if lead == nil {
		if slices.Contains(state.Computers.DiscardedLeads, leadID) && contracts.SupportsComputerCommand(computer.Capabilities, release) {
			return []contracts.AppCommand{release}
		}
		return nil
	}
	if domain.IsCoordinator(lead) != away {
		return nil
	}
	told := contracts.TaskCommandTaskSetLeadAway{TaskID: thread.ID, Away: !away}
	if contracts.SupportsComputerCommand(computer.Capabilities, told) {
		return []contracts.AppCommand{told}
	}
	if !away && contracts.SupportsComputerCommand(computer.Capabilities, release) {
		return []contracts.AppCommand{release}
	}
	return nil
}

// Threads leaving this computer for good, cleared from the archive or outliving their time in it,
// that may still lead threads on paired computers. Once gone they can no longer say those threads
// are let go, so that is remembered for them, between launches too. Reads only id, role and
// remoteHeard of each thread.
func LeadsDiscarded(state *WorkspaceState, threads []*domain.Thread) Step {
	held := jsonx.SetOf(state.Computers.DiscardedLeads...)
	var leads []string
	for _, thread := range threads {
		if domain.MayLeadElsewhere(thread) && !held.Has(thread.ID) && !slices.Contains(leads, thread.ID) {
			leads = append(leads, thread.ID)
		}
	}
	if len(leads) == 0 {
		return Step{}
	}
	return Step{Edits: []OwnerEdit{LeadsDiscardedIn(ComputersOf(state), leads)}, Persists: true}
}

// LeadsDiscarded, on the whole state.
func DiscardedLeads(state *WorkspaceState, threads []*domain.Thread) WorkspaceTransition {
	return Applied(state, SliceTransition(LeadsDiscarded(state, threads)))
}
