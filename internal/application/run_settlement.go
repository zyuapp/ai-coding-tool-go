package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// turnEndRunOf is the part of run a turn's end is settled on, or nil for no run.
func turnEndRunOf(run ActiveRun) *TurnEndRun {
	record := RunRecord(run)
	if record == nil {
		return nil
	}
	return &TurnEndRun{
		Quiet:        record.Quiet,
		Origin:       TurnEndRunOrigin(record.Origin),
		Acknowledged: record.Acknowledged,
		Notified:     record.Notified,
		UsersOwn:     record.UsersOwn,
		AnswersStop:  record.AnswersStop,
		Operation:    (*TurnEndRunOperation)(record.Operation),
		Dismissed:    record.Dismissed,
	}
}

// EndsQuietly reports whether a thread's turn ending is heard only with its coordinator's next wake.
// What Stop all stopped is quiet once it has stopped, as is the turn the agent opened itself to
// answer that stop, and a turn cut off with the app. A turn the user's own message started is theirs,
// and they are there to see it end, unless it failed. A nil stop is none.
func EndsQuietly(status TurnStatus, stop *StopIntent, answersStop bool, usersOwn bool) bool {
	ending, _ := domain.RunOutcome(domain.RunStatus(status))
	return answersStop ||
		stop != nil && *stop == StopIntentQuit ||
		stop != nil && *stop == StopIntentStopAll && domain.EndsAsStopped(ending) ||
		usersOwn && status != TurnStatusFailed
}

// SettleTurn settles a turn's end.
func SettleTurn(end TurnEnd) Settlement {
	stopping := end.Stop != nil || end.Run != nil && runsIsTrue(end.Run.AnswersStop)
	stoppedFirst := stopping && end.Status == TurnStatusFailed && end.Limit != nil && end.Limit.Window == "session"
	status := end.Status
	message := end.Message
	if stoppedFirst {
		status, message = TurnStatusCancelled, nil
	}
	var ended *domain.ThreadOutcome
	if outcome, ok := domain.RunOutcome(domain.RunStatus(status)); ok {
		ended = &outcome
	}
	queue := SettlementQueueDeliver
	if status == TurnStatusCancelled {
		queue = SettlementQueueHandBack
	}
	if end.Run != nil && end.Run.Operation != nil && *end.Run.Operation == TurnEndRunOperationCompact {
		return Settlement{Status: status, Queue: queue, Message: message, RunEndedAs: ended, RestoresTestimony: true}
	}
	surfacing := WhyTurnSurfaces(end.Run, domain.RunStatus(status))
	unseen := surfacing == nil
	var reached *domain.UsageLimit
	if end.Status == TurnStatusFailed {
		reached = end.Limit
	}
	carriesOn := reached != nil && !stopping && domain.ResumesOnItsOwn(reached.Window)
	finished := surfacing == nil || *surfacing != RunSurfacingFailed && *surfacing != RunSurfacingCancelled
	settlement := Settlement{
		Status:         status,
		Queue:          queue,
		Message:        message,
		Surfacing:      surfacing,
		Dismisses:      status == TurnStatusSucceeded && end.Run != nil && runsIsTrue(end.Run.Dismissed),
		Withdraws:      unseen,
		ClosesIssues:   finished && end.Run != nil && end.Run.Origin == TurnEndRunOriginAutomation && end.Run.Acknowledged,
		Reopens:        status == TurnStatusFailed && !carriesOn,
		AnswersStopAll: !unseen && end.Stop != nil && *end.Stop == StopIntentStopAll,
		HushesNews:     end.Stop != nil && *end.Stop == StopIntentQuit,
	}
	if !unseen && !carriesOn {
		settlement.Verdict = ended
		var answersStop, usersOwn bool
		if end.Run != nil {
			answersStop, usersOwn = runsIsTrue(end.Run.AnswersStop), runsIsTrue(end.Run.UsersOwn)
		}
		settlement.Heard = &SettlementHeard{Quiet: EndsQuietly(status, end.Stop, answersStop, usersOwn)}
	}
	if !carriesOn {
		settlement.RunEndedAs = ended
	}
	if reached != nil {
		settlement.Limit = &SettlementLimit{Reached: *reached, Waits: !stopping}
	}
	return settlement
}

// SettledEvent is the status event a run's end is applied as: one stopped before its limit drops the limit and the failure with it.
func SettledEvent(event contracts.RunEventRunStatus, settlement Settlement) contracts.RunEventRunStatus {
	if event.Status == domain.RunStatus(settlement.Status) {
		return event
	}
	event.Limit, event.Message = nil, nil
	event.Status = domain.RunStatus(settlement.Status)
	return event
}

// WithSettledTurn applies a settlement to a thread whose turn has already closed, its run record gone
// and its end stamped. run is the run that ended, nil when none did; at is when.
func WithSettledTurn(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) *WorkspaceState {
	return Afterward(state, SliceTransition(settledTurnStep(state, taskID, settlement, run, at)))
}

func settledTurnStep(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) Step {
	if settlement.RestoresTestimony {
		if record := RunRecord(run); record != nil {
			return Step{Edits: []OwnerEdit{TestimonyRestoredIn(ThreadsOf(state), taskID, record.Before)}}
		}
		return Step{}
	}
	return AndThen(state, turnTestimony(state, taskID, settlement, run), func(next *WorkspaceState) Step {
		return turnAftermath(next, taskID, settlement, run, at)
	})
}

// WithTurnTestimony leaves what the turn leaves on its own thread: its verdict, and for a tick, the
// run withdrawn or its filed-away issues closed.
func WithTurnTestimony(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun) *WorkspaceState {
	return Afterward(state, SliceTransition(turnTestimony(state, taskID, settlement, run)))
}

func turnTestimony(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun) Step {
	if settlement.RestoresTestimony {
		return Step{}
	}
	threads := ThreadsOf(state)
	if settlement.Verdict != nil {
		threads = VerdictIn(threads, taskID, *settlement.Verdict, !ThreadOnScreen(state, taskID))
	}
	if settlement.Dismisses {
		threads = DismissedIn(threads, jsonx.SetOf(taskID))
	}
	if record := RunRecord(run); record != nil && (settlement.Withdraws || settlement.ClosesIssues) {
		threads = WithdrawnIn(threads, taskID, WithdrawnRun{MessagesBefore: record.MessagesBefore, Before: record.Before, ReportedIssues: record.ReportedIssues}, settlement.Withdraws, settlement.ClosesIssues)
	}
	return Step{Edits: []OwnerEdit{threads}}
}

// SettledAfterTestimony takes up the thread's queue once its testimony is already left, as the turn's end says.
func SettledAfterTestimony(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) WorkspaceTransition {
	return Applied(state, SliceTransition(AndThen(state, turnAftermath(state, taskID, settlement, run, at), func(next *WorkspaceState) Step {
		return Drained(next, taskID, QueueIntent(settlement.Queue))
	})))
}

// turnAftermath is what follows from the turn's end beyond its testimony: its usage limit, a
// reopening, Stop all's answer, and its coordinator.
func turnAftermath(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) Step {
	threads := ThreadsOf(state)
	runs := RunsOf(state)
	if limit := settlement.Limit; limit != nil {
		if limit.Waits {
			threads = pausedForLimit(state, threads, taskID, limit.Reached, settlement.RunEndedAs == nil, at)
		} else {
			threads = EngineLimitIn(threads, taskID, limit.Reached)
		}
	}
	if settlement.Reopens {
		threads = ReopenedIn(threads, taskID)
	}
	if record := RunRecord(run); settlement.AnswersStopAll && record != nil {
		runs = RunAnsweredIn(runs, record.RunID)
	}
	thread := runThreadOf(threads.View.Threads, taskID)
	if settlement.Heard != nil && thread != nil {
		threads = turnHeard(Afterward(state, SliceTransition{Edits: []OwnerEdit{threads, runs}}), threads, thread, settlement.Status, at, settlement.Heard.Quiet)
	}
	if settlement.HushesNews && domain.IsCoordinator(thread) {
		threads = NewsQuietedIn(threads, taskID)
	}
	return Step{Edits: []OwnerEdit{threads, runs}}
}

// SettledTurn applies a settlement and takes up the thread's queue as the turn's end says.
func SettledTurn(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) WorkspaceTransition {
	return Applied(state, SliceTransition(TurnSettled(state, taskID, settlement, run, at)))
}

// TurnSettled is SettledTurn as the edits it makes.
func TurnSettled(state *WorkspaceState, taskID string, settlement Settlement, run ActiveRun, at float64) Step {
	return AndThen(state, settledTurnStep(state, taskID, settlement, run, at), func(next *WorkspaceState) Step {
		return Drained(next, taskID, QueueIntent(settlement.Queue))
	})
}

// TurnEnded is a thread's turn ending with status: it reads as stopped or idle, and keeps how and when it ended.
func TurnEnded(state *WorkspaceState, taskID string, status TurnStatus) Step {
	return Step{Edits: []OwnerEdit{RunStatusIn(RunsOf(state), taskID, TurnEndStatus(domain.RunStatus(status))), TurnEndedIn(ThreadsOf(state), taskID, domain.RunStatus(status))}}
}

// pausedForLimit puts a thread whose run hit the limit in line. One that carries on by itself has its verdict and its end taken off.
func pausedForLimit(state *WorkspaceState, threads ThreadsEdit, taskID string, limit domain.UsageLimit, carriesOn bool, at float64) ThreadsEdit {
	if runThreadOf(threads.View.Threads, taskID) == nil {
		return threads
	}
	pause := domain.LimitPause{UsageLimit: limit, PausedAt: at}
	if slices.ContainsFunc(state.Workflows[taskID], func(workflow *domain.Workflow) bool { return workflow.Status == domain.WorkflowStatusStopped }) {
		pause.Workflow = new(true)
	}
	return LimitPausedIn(EngineLimitIn(threads, taskID, limit), taskID, pause, carriesOn)
}

// turnHeard is its coordinator here hearing that a thread's turn ended, as state reads with threads in.
func turnHeard(state *WorkspaceState, threads ThreadsEdit, thread *domain.Thread, status TurnStatus, at float64, quiet bool) ThreadsEdit {
	lead := domain.CoordinatorOf(threads.View.Threads, thread)
	if lead == nil {
		return threads
	}
	heard := MemberFactsHeardFacts(state, lead, thread, []MemberFact{MemberFactEnded{Status: status, Quiet: quiet}}, at)
	if len(heard.Notes) == 0 {
		return threads
	}
	return NotedIn(threads, lead.ID, heard.Notes...)
}
