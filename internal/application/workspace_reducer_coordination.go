package application

import (
	"slices"
	"strconv"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Coordinators and their threads: who works under whom, what they report, and what waits on the user.
// Coordination owns no state of its own: what it keeps lives on threads.

var CoordinationInputs = []string{
	"task.set-coordinator", "task.set-lead-away", "task.resolve", "task.reopen", "coordination.set-thread-model", "coordination.set-muted",
	"coordination.reported", "coordination.decision-raised", "memory.set-purpose", "memory.add", "memory.edit", "memory.forget", "coordination.remembered",
	"decision.answer", "coordination.next-step", "coordination.stop-all",
}

const stepGone = "That step is no longer offered."

const joinRefused = "Only a coordinator can take threads under it, and a coordinator cannot work under another."

func edited(state *WorkspaceState, id string, patch func(thread *domain.Thread) ThreadPatch) ThreadsEdit {
	return EditThread(ThreadsOf(state), id, patch)
}

// coordRefusal is a SliceTransition turning its input down with `message`.
func coordRefusal(message string) SliceTransition {
	return SliceTransition{Refusal: &message}
}

// The coordination slice. `input` is one of the CoordinationInputs.
func ReduceCoordination(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.TaskCommandTaskSetCoordinator:
		thread := coordThread(state.Threads, input.TaskID)
		if thread == nil {
			return SliceTransition{}
		}
		// Resolved only means anything under the coordinator that saw the work settle, so a move leaves
		// it behind, and a thread moved out leaves a coordinator on another computer as well as one here.
		if input.CoordinatorID == nil {
			if thread.ParentID == nil && thread.RemoteLead == nil {
				return SliceTransition{}
			}
			at := Now()
			return SliceTransition{Edits: []OwnerEdit{edited(state, thread.ID, func(*domain.Thread) ThreadPatch {
				return func(next *domain.Thread) {
					next.ParentID, next.RemoteLead, next.ResolvedAt, next.UpdatedAt = nil, nil, nil, at
				}
			})}, ClosesMenu: true}
		}
		lead := coordThread(state.Threads, *input.CoordinatorID)
		if lead == nil || !domain.CanJoinCoordinator(thread, lead) {
			return coordRefusal(joinRefused)
		}
		if thread.ParentID != nil && *thread.ParentID == lead.ID && thread.RemoteLead == nil {
			return SliceTransition{}
		}
		at := Now()
		return SliceTransition{Edits: []OwnerEdit{edited(state, thread.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) {
				next.ResolvedAt, next.RemoteLead, next.ParentID, next.UpdatedAt = nil, nil, new(lead.ID), at
			}
		})}, ClosesMenu: true}

	// A thread keeps the coordinator elsewhere it works under while that one is away, as a thread here
	// keeps its own, and what it settled under it, so it is back as it was once that coordinator is.
	case contracts.TaskCommandTaskSetLeadAway:
		thread := coordThread(state.Threads, input.TaskID)
		if thread == nil || thread.ParentID != nil || thread.RemoteLead == nil || coordTrue(thread.RemoteLead.Away) == input.Away {
			return SliceTransition{}
		}
		lead := *thread.RemoteLead
		lead.Away = nil
		at := Now()
		if input.Away {
			lead.Away = new(true)
		} else {
			lead.BackAt = new(at)
		}
		return SliceTransition{Edits: []OwnerEdit{edited(state, input.TaskID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.RemoteLead, next.UpdatedAt = &lead, at }
		})}}

	// Resolving files away what the thread's last run and its findings had to say, so a thread with
	// nothing left to do never holds its coordinator's row up in the list.
	case contracts.TaskCommandTaskResolve:
		thread := coordThread(state.Threads, input.TaskID)
		if thread == nil || !domain.WorksUnderCoordinator(state.Threads, thread) || thread.ResolvedAt != nil {
			return SliceTransition{}
		}
		activity := ThreadActivityThreadActivity(state)
		if !CanResolve(CoordinatedThreadStatusCoordinatedThreadStatus(thread, activity.Working, activity.Blocked).Status) {
			return coordRefusal("Finish or answer what this thread is waiting on first.")
		}
		at := Now()
		return SliceTransition{Edits: []OwnerEdit{DismissedIn(edited(state, thread.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.ResolvedAt, next.UpdatedAt = new(at), at }
		}), jsonx.SetOf(thread.ID))}}

	case contracts.TaskCommandTaskReopen:
		thread := coordThread(state.Threads, input.TaskID)
		if thread == nil || !domain.WorksUnderCoordinator(state.Threads, thread) || thread.ResolvedAt == nil {
			return SliceTransition{}
		}
		at := Now()
		return SliceTransition{Edits: []OwnerEdit{edited(state, thread.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.ResolvedAt, next.UpdatedAt = nil, at }
		})}}

	case contracts.TaskCommandCoordinationSetThreadModel:
		lead := coordThread(state.Threads, input.TaskID)
		if lead == nil || !domain.IsCoordinator(lead) {
			return coordRefusal("Only a coordinator chooses what its threads run on.")
		}
		catalog := CatalogOf(state)
		var engine domain.AgentEngine
		if input.Engine != nil {
			engine = *input.Engine
		} else {
			engine, _ = domain.ModelEngine(&catalog, input.Model, lead.Engine)
		}
		if engine == "" || !domain.EngineHasModel(&catalog, engine, input.Model) {
			return coordRefusal(domain.UnknownModel(input.Model, coordValueOr(input.Engine, "")))
		}
		// An effort the model lacks, or none, keeps the depth the threads run at as near as that model allows.
		var effort domain.AgentEffort
		if input.Effort != nil && *input.Effort != "" && domain.ModelHasEffort(&catalog, engine, input.Model, *input.Effort, "") {
			effort = *input.Effort
		} else {
			held := domain.DefaultEffortFor(string(engine))
			if choice := domain.ThreadModelOf(&catalog, lead); choice != nil && choice.Effort != nil {
				held = *choice.Effort
			}
			effort = domain.EffortForModel(&catalog, engine, input.Model, held, "")
		}
		threadModel := &domain.ThreadModelChoice{Engine: &engine, Model: input.Model}
		if domain.ModelTakesEffort(&catalog, engine, input.Model) {
			threadModel.Effort = &effort
		}
		if current := lead.ThreadModel; current != nil && current.Engine != nil && *current.Engine == engine && current.Model == threadModel.Model &&
			(current.Effort == nil) == (threadModel.Effort == nil) && (current.Effort == nil || *current.Effort == *threadModel.Effort) {
			return SliceTransition{}
		}
		at := Now()
		return SliceTransition{Edits: []OwnerEdit{edited(state, lead.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.ThreadModel, next.UpdatedAt = threadModel, at }
		})}}

	// Muting is the user's word on a coordinator's notices; it marks nothing in the sidebar.
	case contracts.TaskCommandCoordinationSetMuted:
		lead := coordThread(state.Threads, input.TaskID)
		if lead == nil || !domain.IsCoordinator(lead) || coordTrue(lead.Muted) == input.Muted {
			return SliceTransition{}
		}
		at := Now()
		return SliceTransition{Edits: []OwnerEdit{edited(state, lead.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) {
				next.Muted, next.UpdatedAt = nil, at
				if input.Muted {
					next.Muted = new(true)
				}
			}
		})}}

	// The user's own edits, which the coordinator's session has yet to read.
	case contracts.TaskCommandMemorySetPurpose:
		return remembered(state, input.TaskID, domain.MemoryChangePurpose{Purpose: input.Purpose}, false)
	case contracts.TaskCommandMemoryAdd:
		return remembered(state, input.TaskID, domain.MemoryChangeRemember{Text: input.Text}, false)
	case contracts.TaskCommandMemoryEdit:
		return remembered(state, input.TaskID, domain.MemoryChangeEdit{NoteID: input.NoteID, Text: input.Text}, false)
	case contracts.TaskCommandMemoryForget:
		return remembered(state, input.TaskID, domain.MemoryChangeForget{NoteID: input.NoteID}, false)

	case contracts.TaskCommandDecisionAnswer:
		return decisionAnswered(state, input)

	case contracts.TaskCommandCoordinationNextStep:
		return nextStepTaken(state, input)

	case contracts.TaskCommandCoordinationStopAll:
		return stopAll(state, input.TaskID)

	// The coordinator's own change. Its tool call answers a refusal, so the user's banner never shows one.
	case WorkspaceEventCoordinationRemembered:
		return remembered(state, input.TaskID, input.Change, true)

	// Progress is not news: only a thread that is blocked, done or failed leaves its coordinator a
	// note, which it hears with the rest when the thread's turn ends. One that cannot go on is the
	// user's to hear about at once. A thread under a coordinator on another computer only records
	// where it stands: that computer reads it here, and hears and announces it there.
	case WorkspaceEventCoordinationReported:
		thread := coordThread(state.Threads, input.TaskID)
		var lead *domain.Thread
		if thread != nil {
			lead = domain.CoordinatorOf(state.Threads, thread)
		}
		if thread == nil || lead == nil && domain.RemoteLeadOf(thread) == nil {
			return declinedWith(NoCoordinator)
		}
		at := Now()
		report := domain.CoordinationReport{State: input.State, Summary: input.Summary, At: at}
		reported := edited(state, thread.ID, func(item *domain.Thread) ThreadPatch {
			withReport := *item
			withReport.Report = &report
			open := domain.ReopenedIfStuck(&withReport)
			return func(next *domain.Thread) { next.Report, next.ResolvedAt = open.Report, open.ResolvedAt }
		})
		if lead == nil {
			return SliceTransition{Edits: []OwnerEdit{reported}, Result: recordedAnswer()}
		}
		threads, effects := FactsHeardIn(state, reported, lead, thread, []MemberFact{MemberFactReported{Report: report, RunID: activeRunID(state, thread.ID)}}, at)
		return SliceTransition{Edits: []OwnerEdit{threads}, Effects: effects, Result: recordedAnswer()}

	// The user is told at once; the coordinator hears it with the rest when the thread's turn ends. A
	// thread under a coordinator on another computer only records it, for that computer to raise. A
	// thread with as many waiting as it may raises no more, and its tool call is told why.
	case WorkspaceEventCoordinationDecisionRaised:
		thread := coordThread(state.Threads, input.TaskID)
		var lead *domain.Thread
		if thread != nil {
			lead = thread
			if !domain.IsCoordinator(thread) {
				lead = domain.CoordinatorOf(state.Threads, thread)
			}
		}
		if thread == nil || lead == nil && domain.RemoteLeadOf(thread) == nil {
			return declinedWith(OutsideCoordination)
		}
		if domain.DecisionsFull(thread) {
			return declinedWith(DecisionsFull)
		}
		at := Now()
		decision := &domain.Decision{Question: input.Request.Question, Options: input.Request.Options, RaisedAt: at}
		if input.DecisionID != nil {
			decision.ID = *input.DecisionID
		} else {
			decision.ID = NewID()
		}
		if input.Request.Context != nil && *input.Request.Context != "" {
			decision.Context = input.Request.Context
		}
		raised := edited(state, thread.ID, func(item *domain.Thread) ThreadPatch {
			decisions := domain.WithDecision(item, decision).Decisions
			return func(next *domain.Thread) { next.Decisions = decisions }
		})
		if lead == nil {
			return SliceTransition{Edits: []OwnerEdit{raised}, Result: recordedAnswer()}
		}
		if lead == thread {
			after := Afterward(state, SliceTransition{Edits: []OwnerEdit{raised}})
			return SliceTransition{Edits: []OwnerEdit{raised}, Effects: Announced(after, lead, DecisionHeadline(decision), NoticeReasonDecision), Result: recordedAnswer()}
		}
		threads, effects := FactsHeardIn(state, raised, lead, thread, []MemberFact{MemberFactRaised{Decision: *decision, RunID: activeRunID(state, thread.ID)}}, at)
		return SliceTransition{Edits: []OwnerEdit{threads}, Effects: effects, Result: recordedAnswer()}
	}
	return SliceTransition{}
}

// activeRunID is the id of the run going on `taskID`, or nil.
func activeRunID(state *WorkspaceState, taskID string) *string {
	if run := RunRecord(state.ActiveRuns[taskID]); run != nil {
		return &run.RunID
	}
	return nil
}

// A thread's own request carried out.
func recordedAnswer() contracts.WorkspaceCommandResult {
	return contracts.WorkspaceCommandResultTrue{Answer: contracts.CoordinationAnswerTrue{}}
}

// A thread's own request turned down: nothing changes, the user is shown nothing, and the reason answers the thread.
func declinedWith(reason string) SliceTransition {
	return SliceTransition{Result: contracts.WorkspaceCommandResultTrue{Answer: contracts.CoordinationAnswerFalse{Reason: reason}}}
}

// coordGrouped is `value` with its thousands grouped, as toLocaleString writes it in English.
func coordGrouped(value int) string {
	digits := strconv.Itoa(value)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

// The answer is recorded, then said to the thread that asked, steered into a run already going.
func decisionAnswered(state *WorkspaceState, input contracts.TaskCommandDecisionAnswer) SliceTransition {
	thread := coordThread(state.Threads, input.TaskID)
	var decision *domain.Decision
	if thread != nil {
		for _, item := range domain.OpenDecisions(thread) {
			if item.ID == input.DecisionID {
				decision = item
				break
			}
		}
	}
	answer := coordinationTrim(input.Answer)
	if thread == nil || decision == nil || answer == "" {
		return SliceTransition{}
	}
	if coordUTF16Len(answer) > domain.MaxAnswer {
		return coordRefusal("An answer can be at most " + coordGrouped(domain.MaxAnswer) + " characters.")
	}
	answered := func(next *WorkspaceState) ThreadsEdit {
		return edited(next, thread.ID, func(item *domain.Thread) ThreadPatch {
			decisions := domain.WithAnswer(item, decision.ID, answer, Now()).Decisions
			return func(next *domain.Thread) { next.Decisions = decisions }
		})
	}
	if thread.ArchivedAt != nil {
		return SliceTransition{Edits: []OwnerEdit{answered(state)}}
	}
	// A decision only closes once its answer is on its way, so one that could not be sent can be
	// answered again. The message says what it answers, queued behind a run or starting one, so
	// the decision opens again if it never goes.
	text := "The user decided \"" + decision.Question + "\": " + answer
	send := contracts.TaskCommandTaskSend{TaskID: &thread.ID, Text: &text, Steer: new(true)}
	sent := ReduceSending(state, send, CoordinationSend{}, SendSaid{Answers: &decision.ID})
	if Failed(sent) {
		return SliceTransition(sent)
	}
	return SliceTransition(AndThen(state, sent, func(next *WorkspaceState) Step {
		return Step{Edits: []OwnerEdit{answered(next)}}
	}))
}

// A next step is the user's own word to the thread, sent as any message of theirs is, but one its
// coordinator offered, so the turn it starts is news for that coordinator. One the thread's card no
// longer offers, as once it has started working, is refused and nothing is sent.
func nextStepTaken(state *WorkspaceState, input contracts.TaskCommandCoordinationNextStep) SliceTransition {
	thread := coordThread(state.Threads, input.TaskID)
	var lead *domain.Thread
	if thread != nil {
		lead = domain.CoordinatorOf(state.Threads, thread)
	}
	// Only a thread its coordinator's views draw has a card to take a step from, so an archived one
	// has none. One under a coordinator on another computer is drawn there and decided here.
	var drawn bool
	if lead != nil {
		drawn = slices.ContainsFunc(CoordinationMembers(state, lead.ID), func(member *CoordinationMember) bool {
			return member.Computer == nil && member.Thread.ID == thread.ID
		})
	} else {
		drawn = thread != nil && domain.RemoteLeadOf(thread) != nil && thread.ArchivedAt == nil
	}
	if thread == nil || !drawn {
		return coordRefusal(stepGone)
	}
	activity := ThreadActivityThreadActivity(state)
	status := CoordinatedThreadStatusCoordinatedThreadStatus(thread, activity.Working, activity.Blocked).Status
	if !slices.Contains(NextSteps(thread, status, CheckoutAnswer(state, thread)), input.Step) {
		return coordRefusal(stepGone)
	}
	text := domain.NextStepOf(input.Step).Text
	return SliceTransition(ReduceSending(state, contracts.TaskCommandTaskSend{TaskID: &thread.ID, Text: &text}, CoordinationSend{}, SendSaid{Step: new(true)}))
}

// Stops a coordinator and every thread under it: each run is cancelled, each workflow and subagent
// that would outlive it is stopped, each wait on a usage limit ends, and the coordinator's wake and
// each start of a new thread under it are dropped, handing back what the user queued. A start already
// carried to a paired computer cannot be called back, so it is stopped there as it lands. The runs it
// cancels end quietly, so nothing it stopped wakes the coordinator to carry on; scheduled ticks are
// the user's to stop one by one. A thread under it on another computer is stopped there, by a
// computer whose build can.
func stopAll(state *WorkspaceState, leadID string) SliceTransition {
	lead := coordThread(state.Threads, leadID)
	if lead == nil || !domain.IsCoordinator(lead) {
		return SliceTransition{}
	}
	work := CoordinationCoordinatorWork(state, lead)
	var cancelled []*ActiveRunRunning
	for _, thread := range work.Threads {
		if run := RunRecord(state.ActiveRuns[thread.ID]); run != nil {
			cancelled = append(cancelled, run)
		}
	}
	// What it stops on paired computers, and each start it catches on its way to one, the coordinator
	// keeps, so a start is stopped whenever it lands and both are heard quietly, after a restart here too.
	// Stopping a workflow or subagent can make the agent take a turn of its own to answer it, which ends quietly too.
	cover := StopAllCover{Cancelled: []*StopAllCoverCancelled{}, Hushed: []string{}, Elsewhere: work.Elsewhere, Landing: []*StopAllCoverLanding{}}
	for _, run := range cancelled {
		cover.Cancelled = append(cover.Cancelled, &StopAllCoverCancelled{RunID: run.RunID})
	}
	for _, item := range work.SessionWork {
		cover.Hushed = append(cover.Hushed, item.TaskID)
	}
	for _, proposal := range work.Landing {
		cover.Landing = append(cover.Landing, &StopAllCoverLanding{ID: proposal.ID, Computer: proposal.Place.Computer.ID})
	}
	outstanding := StoppedAll(state, lead.ID, cover, Now())
	cancels := make([]WorkspaceEffect, len(cancelled))
	for index, run := range cancelled {
		cancels[index] = WorkspaceEffectSendRunCommand{Command: contracts.CancelRunCommand{TaskID: run.TaskID, RunID: run.RunID}}
	}
	var then []func(*WorkspaceState) Step
	for _, item := range work.SessionWork {
		for _, processID := range item.ProcessIDs {
			then = append(then, func(next *WorkspaceState) Step { return ProcessStopped(next, item.TaskID, processID) })
		}
	}
	for _, thread := range work.Threads {
		then = append(then, func(next *WorkspaceState) Step { return PauseCancelled(next, thread.ID) })
	}
	for _, start := range work.Starts {
		then = append(then, func(next *WorkspaceState) Step { return Unsent(next, start) })
		if start.Coordination != nil && start.Coordination.ProposalID != nil && *start.Coordination.ProposalID != "" {
			proposalID := *start.Coordination.ProposalID
			then = append(then, func(next *WorkspaceState) Step { return ProposalFailure(next, lead.ID, proposalID, nil) })
		}
		// What the user queued behind it goes back to its composer, as any stop hands it back.
		if start.TaskID != nil && *start.TaskID != "" {
			taskID := *start.TaskID
			then = append(then, func(next *WorkspaceState) Step {
				if next.ActiveRuns[taskID] != nil {
					return Step{}
				}
				return Drained(next, taskID, QueueIntentHandBack)
			})
		}
	}
	outstanding.Effects = slices.Concat(cancels, outstanding.Effects)
	return SliceTransition(InTurn(state, outstanding, then))
}

// Only a coordinator keeps a memory; a change that alters nothing writes nothing. The coordinator's
// own change, which its session hears as it makes it, is answered rather than refused to the user.
func remembered(state *WorkspaceState, taskID string, change domain.MemoryChange, heard bool) SliceTransition {
	lead := coordThread(state.Threads, taskID)
	if lead == nil || !domain.IsCoordinator(lead) {
		if heard {
			return declinedWith(NoMemory)
		}
		return SliceTransition{}
	}
	at := Now()
	changed, noteID, err := domain.WithMemoryChange(lead, change, at, heard)
	if err != nil {
		if heard {
			return declinedWith(err.Error())
		}
		return coordRefusal(err.Error())
	}
	made := SliceTransition{}
	if changed != lead {
		made.Edits = []OwnerEdit{edited(state, lead.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.Memory, next.UpdatedAt = changed.Memory, at }
		})}
	}
	if heard {
		answer := contracts.CoordinationAnswerTrue{}
		if noteID != "" {
			answer.NoteID = &noteID
		}
		made.Result = contracts.WorkspaceCommandResultTrue{Answer: answer}
	}
	return made
}

// The one place a coordinator is woken. After every input, each coordinator the input could have
// touched, through itself, its threads or who works under it, hears its news if it is due. A
// coordinator the input stopped keeps what it holds quiet, so only news that arrives later wakes it.
func ReconcileCoordination(previous *WorkspaceState, transition WorkspaceTransition, input WorkspaceInput) WorkspaceTransition {
	base := &transition.State
	// Quieted first, since a stop may only have asked for runs to end and changed nothing else yet.
	quiet := base
	if stopped, ok := stoppedBy(previous, input); ok {
		quiet = WithNewsQuieted(base, stopped)
	}
	// What paired computers say of the threads coordinators here started there is news like any thread's.
	remote := remoteNewsOf(previous, quiet)
	threads := ThreadChanges(previous.Threads, remote.state.Threads)
	state := WithoutLapsedStops(remote.state, threads)
	touched := ActivityChanges(previous, state, threads)
	if touched == nil {
		touched = jsonx.Set[string]{}
	}
	for _, change := range threads {
		before, after := change.Before, change.After
		if before == nil || after == nil || !coordSamePointee(before.ParentID, after.ParentID) || !coordSamePointee(before.Role, after.Role) ||
			!coordSamePointee(before.ArchivedAt, after.ArchivedAt) || !jsonx.SameSlice(before.CoordinationNotes, after.CoordinationNotes) {
			touched[change.ID] = struct{}{}
		}
	}
	for id := range remote.leads {
		touched[id] = struct{}{}
	}
	said := slices.Concat(remote.effects, Released(previous, state, threads), roleChanges(previous, state, threads))
	if len(touched) == 0 {
		if state == base && len(said) == 0 {
			return transition
		}
		return WorkspaceTransition{State: *state, Effects: slices.Concat(transition.Effects, said), Result: transition.Result}
	}
	effects := slices.Concat(transition.Effects, said)
	for _, leadID := range coordinatorsOf(previous, state, touched) {
		var delivered []WorkspaceEffect
		state, delivered = deliverCoordinationNotes(state, leadID)
		effects = append(effects, delivered...)
	}
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	return WorkspaceTransition{State: *state, Effects: effects, Result: transition.Result}
}

// coordSamePointee is TypeScript's === on two optional values: both absent, or both present and equal.
func coordSamePointee[T comparable](a, b *T) bool {
	return a == b || a != nil && b != nil && *a == *b
}

// The run commands telling the agent process of each thread whose coordination role an input
// changed, so a session built for what the thread was, holding tools the thread no longer has, is let
// go. A thread's role is read from it and its coordinator, so the threads under a changed one are read too.
func roleChanges(previous, next *WorkspaceState, threads []*ThreadChange) []WorkspaceEffect {
	var read []string
	shaped := jsonx.Set[string]{}
	for _, change := range threads {
		before, after := change.Before, change.After
		if before != nil && after != nil && (!coordSamePointee(before.Role, after.Role) || !coordSamePointee(before.ArchivedAt, after.ArchivedAt) ||
			!coordSamePointee(before.ParentID, after.ParentID) || before.RemoteLead != after.RemoteLead) && !shaped.Has(change.ID) {
			shaped[change.ID] = struct{}{}
			read = append(read, change.ID)
		}
	}
	if len(shaped) == 0 {
		return nil
	}
	for _, thread := range next.Threads {
		if thread.ParentID != nil && shaped.Has(*thread.ParentID) && !slices.Contains(read, thread.ID) {
			read = append(read, thread.ID)
		}
	}
	var effects []WorkspaceEffect
	for _, id := range read {
		before, after := coordThread(previous.Threads, id), coordThread(next.Threads, id)
		if before == nil || after == nil {
			continue
		}
		role := CoordinationRole(next.Threads, after)
		if CoordinationRole(previous.Threads, before) == role {
			continue
		}
		command := contracts.ThreadRoleCommand{TaskID: id}
		if role != "" {
			command.CoordinationRole = &role
		}
		effects = append(effects, WorkspaceEffectSendRunCommand{Command: command})
	}
	return effects
}

// The coordinator the user just stopped, filed away, took the role off, or whose run could not start.
func stoppedBy(previous *WorkspaceState, input WorkspaceInput) (string, bool) {
	switch input := input.(type) {
	case contracts.TaskCommandTaskArchive:
		return input.TaskID, true
	case contracts.TaskCommandCoordinationStopAll:
		return input.TaskID, true
	case contracts.TaskCommandTaskSetRole:
		if input.Role != nil && *input.Role == domain.ThreadRoleCoordinator {
			return "", false
		}
		taskID := TargetID(previous, input.TaskID)
		if taskID == nil {
			return "", false
		}
		if lead := coordThread(previous.Threads, *taskID); lead != nil && lead.Role != nil && *lead.Role == domain.ThreadRoleCoordinator {
			return lead.ID, true
		}
	case WorkspaceEventRunEvent:
		if event, ok := input.Event.(contracts.RunEventRunStatus); ok && event.Status == domain.RunStatusCancelled {
			if run := RunRecord(previous.ActiveRuns[event.TaskID]); run != nil && run.RunID == event.RunID {
				return event.TaskID, true
			}
		}
	case WorkspaceEventRunUnresolved:
		if pending := previous.PendingRuns[input.PendingID]; pending != nil && pending.TaskID != nil {
			return *pending.TaskID, true
		}
	case contracts.RunControlCommandLimitCancel:
		if thread := coordThread(previous.Threads, input.TaskID); thread != nil && thread.LimitPause != nil {
			return input.TaskID, true
		}
	}
	return "", false
}

// Each touched thread that is a coordinator, and the coordinator each one worked under before or
// after, in first-seen order.
func coordinatorsOf(previous, next *WorkspaceState, touched jsonx.Set[string]) []string {
	var leads []string
	seen := jsonx.Set[string]{}
	add := func(id string) {
		if !seen.Has(id) {
			seen[id] = struct{}{}
			leads = append(leads, id)
		}
	}
	for _, state := range []*WorkspaceState{previous, next} {
		for _, thread := range state.Threads {
			if !touched.Has(thread.ID) {
				continue
			}
			if domain.IsCoordinator(thread) {
				add(thread.ID)
			} else if thread.ParentID != nil && *thread.ParentID != "" {
				add(*thread.ParentID)
			}
		}
	}
	return leads
}

// The news that wakes a coordinator: notes not quiet, and not written during a run of one of its
// threads that is still working, here or on the computer that holds it, as that computer last said.
// A run waiting for the user's approval no longer counts as working.
func wakingNews(state *WorkspaceState, lead *domain.Thread, roster *CoordinatorRoster, unheard jsonx.Set[string]) []*domain.CoordinationNote {
	var notes []*domain.CoordinationNote
	for _, note := range lead.CoordinationNotes {
		if !coordTrue(note.Quiet) {
			notes = append(notes, note)
		}
	}
	if !slices.ContainsFunc(notes, func(note *domain.CoordinationNote) bool { return note.RunID != nil && *note.RunID != "" }) {
		return notes
	}
	members := map[string]*RosterMember{}
	for _, member := range roster.Members {
		if _, held := members[member.Thread.ID]; !held {
			members[member.Thread.ID] = member
		}
	}
	going := func(id, runID string) bool {
		if unheard.Has(id) {
			return true
		}
		member := members[id]
		if member == nil {
			return false
		}
		runs := state.ActiveRuns
		if member.Computer != nil {
			runs = member.Holder.ActiveRuns
		}
		run := RunRecord(runs[id])
		return run != nil && run.RunID == runID && member.Working
	}
	var news []*domain.CoordinationNote
	for _, note := range notes {
		if note.RunID == nil || *note.RunID == "" || !going(note.ThreadID, *note.RunID) {
			news = append(news, note)
		}
	}
	return news
}

// Wakes a free coordinator with all its waiting notes at once, once none of its threads is working
// or the news cannot wait. The notes stay until its run actually starts. One waiting out a usage
// limit hears them once its resumed run ends. The state as that leaves it, and its effects.
func deliverCoordinationNotes(state *WorkspaceState, leadID string) (*WorkspaceState, []WorkspaceEffect) {
	lead := coordThread(state.Threads, leadID)
	if lead == nil || !domain.IsCoordinator(lead) || len(lead.CoordinationNotes) == 0 || ThreadBusy(state, leadID) || len(QueuedFor(state, leadID)) > 0 || HeldByLimit(state, leadID, Now()) {
		return state, nil
	}
	notes := lead.CoordinationNotes
	activity := ThreadActivityThreadActivity(state)
	roster := CoordinationCoordinatorRoster(state, lead, RosterActivity{Working: activity.Working, Blocked: activity.Blocked, Ranked: activity.Ranked}, nil)
	unheard := UnheardWork(state, lead)
	unheardIDs := jsonx.Set[string]{}
	for _, item := range unheard {
		unheardIDs[item.ThreadID] = struct{}{}
	}
	news := wakingNews(state, lead, roster, unheardIDs)
	if len(news) == 0 {
		return state, nil
	}
	working := len(unheard)
	for _, member := range roster.Members {
		if member.Working {
			working++
		}
	}
	if working > 0 && !slices.ContainsFunc(news, func(note *domain.CoordinationNote) bool { return coordTrue(note.Urgent) }) {
		return state, nil
	}
	project := ProjectFor(state, lead)
	text, prompt := CoordinationUpdate(notes, working, WaitingLine(roster.Waiting, "the user"))
	ids := make([]string, len(notes))
	for index, note := range notes {
		ids[index] = note.ID
	}
	pending := &PendingRun{
		ID:            NewID(),
		RunID:         NewID(),
		Origin:        PendingRunOriginComposer,
		TaskID:        &leadID,
		Text:          text,
		Prompt:        prompt,
		MessageOrigin: domain.MessageOriginCoordination{},
		Attachments:   []string{},
		Coordination:  &CoordinationSend{Notes: ids},
	}
	if project != nil {
		pending.ProjectID = &project.ID
	}
	return WithPending(state, pending), []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, lead, project, WorktreeFor(state, lead), false, nil)}
}
