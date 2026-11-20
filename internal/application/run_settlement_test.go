package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const settleAt = 1_800_000_000_000

var (
	settleSession = domain.UsageLimit{ResetsAt: settleAt + 3_600_000, Window: "session"}
	settleWeekly  = domain.UsageLimit{ResetsAt: settleAt + 72*3_600_000, Window: "weekly"}
)

func settleAttended(edits ...func(*TurnEndRun)) *TurnEndRun {
	run := &TurnEndRun{Origin: TurnEndRunOriginComposer}
	for _, edit := range edits {
		edit(run)
	}
	return run
}

func settleTick() *TurnEndRun {
	return &TurnEndRun{Origin: TurnEndRunOriginAutomation, Quiet: true, Acknowledged: true}
}

func settleHeard(settlement Settlement) string {
	switch {
	case settlement.Heard == nil:
		return "none"
	case settlement.Heard.Quiet:
		return "quiet"
	}
	return "loud"
}

func settleOutcome(outcome *domain.ThreadOutcome) string {
	if outcome == nil {
		return "null"
	}
	return string(*outcome)
}

func TestRunSettlementAttendedTurn(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusSucceeded, Run: settleAttended()})
	if settlement.Status != TurnStatusSucceeded || *settlement.Surfacing != RunSurfacingAttended || settleOutcome(settlement.Verdict) != "finished" || settleOutcome(settlement.RunEndedAs) != "finished" {
		t.Fatalf("settlement = %+v", settlement)
	}
	if settleHeard(settlement) != "loud" || settlement.Limit != nil || settlement.Withdraws || settlement.Reopens {
		t.Fatalf("settlement = %+v", settlement)
	}
	if !SettleTurn(TurnEnd{Status: TurnStatusFailed, Run: settleAttended()}).Reopens {
		t.Fatal("a failed turn may leave its thread stuck")
	}
}

func TestRunSettlementUsersOwnTurnQuietUnlessFailed(t *testing.T) {
	usersOwn := settleAttended(func(run *TurnEndRun) { run.UsersOwn = new(true) })
	for status, want := range map[TurnStatus]string{TurnStatusSucceeded: "quiet", TurnStatusCancelled: "quiet", TurnStatusFailed: "loud"} {
		if got := settleHeard(SettleTurn(TurnEnd{Status: status, Run: usersOwn})); got != want {
			t.Errorf("%s: heard %s", status, got)
		}
	}
}

func TestRunSettlementStopAll(t *testing.T) {
	stopped := SettleTurn(TurnEnd{Status: TurnStatusCancelled, Run: settleAttended(), Stop: new(StopIntentStopAll)})
	if settleHeard(stopped) != "quiet" || !stopped.AnswersStopAll {
		t.Fatalf("stopped = %+v", stopped)
	}
	for _, item := range []struct {
		end  TurnEnd
		want string
	}{
		{TurnEnd{Status: TurnStatusSucceeded, Run: settleAttended(), Stop: new(StopIntentStopAll)}, "loud"},
		{TurnEnd{Status: TurnStatusFailed, Run: settleAttended(), Stop: new(StopIntentStopAll)}, "loud"},
		{TurnEnd{Status: TurnStatusCancelled, Run: settleAttended(), Stop: new(StopIntentUser)}, "loud"},
		{TurnEnd{Status: TurnStatusFailed, Run: settleAttended(func(run *TurnEndRun) { run.AnswersStop = new(true) })}, "quiet"},
	} {
		if got := settleHeard(SettleTurn(item.end)); got != item.want {
			t.Errorf("%+v: heard %s", item.end, got)
		}
	}
}

func TestRunSettlementSessionLimitCarriesOn(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusFailed, Message: new("You've hit your limit"), Limit: &settleSession, Run: settleAttended()})
	if settlement.Status != TurnStatusFailed || *settlement.Surfacing != RunSurfacingFailed || settlement.Verdict != nil || settlement.RunEndedAs != nil {
		t.Fatalf("settlement = %+v", settlement)
	}
	if settlement.Limit == nil || settlement.Limit.Reached != settleSession || !settlement.Limit.Waits || settlement.Heard != nil || settlement.Reopens {
		t.Fatalf("settlement = %+v", settlement)
	}
}

func TestRunSettlementWeeklyLimitWaitsForUser(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusFailed, Limit: &settleWeekly, Run: settleAttended()})
	if settleOutcome(settlement.Verdict) != "failed" || settleOutcome(settlement.RunEndedAs) != "failed" || !settlement.Limit.Waits || settleHeard(settlement) != "loud" || !settlement.Reopens {
		t.Fatalf("settlement = %+v", settlement)
	}
}

func TestRunSettlementStopBeatsLimit(t *testing.T) {
	end := TurnEnd{Status: TurnStatusFailed, Message: new("You've hit your limit"), Limit: &settleSession, Run: settleAttended(), Stop: new(StopIntentUser)}
	settlement := SettleTurn(end)
	if settlement.Status != TurnStatusCancelled || settlement.Message != nil || settleOutcome(settlement.Verdict) != "stopped" || settlement.Limit.Waits || settleHeard(settlement) != "loud" {
		t.Fatalf("settlement = %+v", settlement)
	}
	end.Limit = &settleWeekly
	weekly := SettleTurn(end)
	if weekly.Status != TurnStatusFailed || *weekly.Message != "You've hit your limit" || weekly.Limit.Reached != settleWeekly || weekly.Limit.Waits {
		t.Fatalf("weekly = %+v", weekly)
	}
	event := contracts.RunEventRunStatus{RunEventBase: contracts.RunEventBase{TaskID: "a", RunID: "run-a", Sequence: 1}, Status: domain.RunStatusFailed, Message: new("You've hit your limit"), Limit: &settleSession}
	if stopped := SettledEvent(event, settlement); stopped.Status != domain.RunStatusCancelled || stopped.Message != nil || stopped.Limit != nil || stopped.RunID != "run-a" {
		t.Fatalf("stopped = %+v", stopped)
	}
	if same := SettledEvent(event, weekly); same.Message != event.Message || same.Limit != event.Limit {
		t.Fatal("an event that settles as itself changed")
	}
}

func TestRunSettlementQuietTickEarnsSilence(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusSucceeded, Run: settleTick(), Stop: new(StopIntentStopAll)})
	if settlement.Surfacing != nil || settlement.Verdict != nil || !settlement.Withdraws || !settlement.ClosesIssues || settlement.Heard != nil || settlement.AnswersStopAll {
		t.Fatalf("settlement = %+v", settlement)
	}
	reported := settleTick()
	reported.Notified = true
	if surfacing := SettleTurn(TurnEnd{Status: TurnStatusSucceeded, Run: reported}).Surfacing; *surfacing != RunSurfacingReported {
		t.Fatalf("surfacing = %s", *surfacing)
	}
	if SettleTurn(TurnEnd{Status: TurnStatusFailed, Run: settleTick()}).ClosesIssues {
		t.Fatal("a failed tick closed issues")
	}
}

func TestRunSettlementCompactingIsNoTurn(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusSucceeded, Run: settleAttended(func(run *TurnEndRun) { run.Operation = new(TurnEndRunOperationCompact) })})
	if !settlement.RestoresTestimony || settlement.Verdict != nil || settlement.Heard != nil || settlement.Limit != nil {
		t.Fatalf("settlement = %+v", settlement)
	}
	runsPorted(t, func() {
		before := ThreadMark{UpdatedAt: 1, RunEndedAs: new(domain.ThreadOutcomeFailed), Outcome: new(domain.ThreadOutcomeFailed), RunEndedAt: new(5.0)}
		state := runsWorkspace(runsThreads(runsTask("a", func(thread *domain.Thread) {
			thread.RunEndedAs, thread.RunEndedAt = new(domain.ThreadOutcomeFinished), new(9.0)
			thread.Outcome, thread.OutcomeUnread = new(domain.ThreadOutcomeFinished), new(true)
		})))
		run := runsActive("a", "run-a", func(run *ActiveRunRunning) {
			run.Operation = new(ActiveRunRunningOperationCompact)
			run.Before = before
		})
		restored := WithSettledTurn(state, "a", settlement, run, settleAt).Threads[0]
		if settleOutcome(restored.RunEndedAs) != "failed" || *restored.RunEndedAt != 5 || settleOutcome(restored.Outcome) != "failed" || restored.OutcomeUnread != nil {
			t.Fatalf("restored = %+v", restored.RunThread)
		}
	})
}

func TestRunSettlementQuitIsQuietAndHushesNews(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusCancelled, Stop: new(StopIntentQuit)})
	if settleOutcome(settlement.Verdict) != "stopped" || settleOutcome(settlement.RunEndedAs) != "stopped" || settleHeard(settlement) != "quiet" || !settlement.HushesNews {
		t.Fatalf("settlement = %+v", settlement)
	}
	runsPorted(t, func() {
		lead := runsTask("lead", func(thread *domain.Thread) {
			thread.Role = new(domain.ThreadRoleCoordinator)
			thread.Title = "Lead"
			thread.CoordinationNotes = []*domain.CoordinationNote{{ID: "n", ThreadID: "x", Text: "x", At: 1}}
		})
		member := runsTask("one", func(thread *domain.Thread) { thread.ParentID = new("lead") })
		state := runsWorkspace(runsThreads(lead, member))
		ended := WithSettledTurn(WithSettledTurn(state, "lead", settlement, nil, settleAt), "one", settlement, nil, settleAt)
		notes := runsThreadIn(ended.Threads, "lead").CoordinationNotes
		if len(notes) != 2 {
			t.Fatalf("notes = %d", len(notes))
		}
		for _, note := range notes {
			if !runsIsTrue(note.Quiet) {
				t.Fatalf("a loud note survived a quit: %+v", note)
			}
		}
		if settleOutcome(runsThreadIn(ended.Threads, "one").Outcome) != "stopped" {
			t.Fatal("a thread cut off does not read as stopped")
		}
	})
}

func TestRunSettlementStoppedInLineHandsBackQueue(t *testing.T) {
	settlement := SettleTurn(TurnEnd{Status: TurnStatusCancelled, Stop: new(StopIntentUser)})
	if *settlement.Surfacing != RunSurfacingCancelled || settleOutcome(settlement.Verdict) != "stopped" || settleHeard(settlement) != "loud" {
		t.Fatalf("settlement = %+v", settlement)
	}
	runsPorted(t, func() {
		lead := runsTask("lead", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator); thread.Title = "Lead" })
		member := runsTask("one", func(thread *domain.Thread) { thread.ParentID = new("lead") })
		state := runsWorkspace(runsThreads(lead, member), func(state *WorkspaceState) {
			state.QueuedMessages = map[string][]*QueuedMessage{"one": {runQueueOwn("q", "later")}}
		})
		ended := SettledTurn(state, "one", settlement, nil, settleAt)
		notes := runsThreadIn(ended.State.Threads, "lead").CoordinationNotes
		if len(notes) == 0 || notes[len(notes)-1].Text != `"one" was stopped.` || notes[len(notes)-1].Quiet != nil {
			t.Fatalf("notes = %+v", notes)
		}
		if len(ended.State.QueuedMessages["one"]) != 0 || ended.State.Prompts["one"] != "later" {
			t.Fatal("the queue was not handed back")
		}
	})
}

func TestRunSettlementAppliedLimitAndReopen(t *testing.T) {
	runsPorted(t, func() {
		state := runsWorkspace(runsThreads(
			runsTask("a", func(thread *domain.Thread) {
				thread.RunEndedAs, thread.Outcome = new(domain.ThreadOutcomeFailed), new(domain.ThreadOutcomeFinished)
			}),
			runsTask("b", func(thread *domain.Thread) {
				thread.RunEndedAs, thread.ResolvedAt = new(domain.ThreadOutcomeFailed), new(1.0)
			}),
		))
		paused := WithSettledTurn(state, "a", SettleTurn(TurnEnd{Status: TurnStatusFailed, Limit: &settleSession, Run: settleAttended()}), runsActive("a", "run-a"), settleAt)
		thread := runsThreadIn(paused.Threads, "a")
		if thread.Outcome != nil || thread.RunEndedAs != nil || thread.LimitPause == nil || thread.LimitPause.UsageLimit != settleSession || thread.LimitPause.PausedAt != settleAt {
			t.Fatalf("thread = %+v", thread.RunThread)
		}
		reopened := WithSettledTurn(state, "b", SettleTurn(TurnEnd{Status: TurnStatusFailed, Run: settleAttended()}), runsActive("b", "run-b"), settleAt)
		if runsThreadIn(reopened.Threads, "b").ResolvedAt != nil {
			t.Fatal("a stuck thread was not reopened")
		}
	})
}
