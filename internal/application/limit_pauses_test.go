package application

import (
	"regexp"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const (
	limitAt   = 1_800_000_000_000
	limitHour = 3_600_000
)

var (
	limitSession = domain.UsageLimit{ResetsAt: limitAt + 2*limitHour, Window: "session"}
	limitWeekly  = domain.UsageLimit{ResetsAt: limitAt + 72*limitHour, Window: "weekly"}
)

func limitPaused(state *WorkspaceState, taskID string, limit domain.UsageLimit) *WorkspaceState {
	reached := limitReached(state, taskID, limit)
	return &reached.State
}

func limitReached(state *WorkspaceState, taskID string, limit domain.UsageLimit) WorkspaceTransition {
	runID := RunRecord(state.ActiveRuns[taskID]).RunID
	return Reduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent(taskID, runID, 1), Status: domain.RunStatusFailed, Message: new("You've hit your limit"), Limit: &limit}})
}

func TestLimitPausesSessionPauseAndWake(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		paused := limitReached(runsMidRun("a"), "a", limitSession)
		thread := runsThreadIn(paused.State.Threads, "a")
		if thread.LimitPause == nil || thread.LimitPause.ResetsAt != limitSession.ResetsAt || thread.Outcome != nil || thread.RunEndedAs != nil {
			t.Fatalf("thread = %+v", thread.RunThread)
		}
		if reset, ok := runsEffect[WorkspaceEffectScheduleLimitReset](paused); !ok || reset.At == nil || *reset.At != limitSession.ResetsAt {
			t.Fatal("no timer for the reset")
		}
		early := Reduce(&paused.State, WorkspaceEventLimitsElapsed{At: limitSession.ResetsAt - 1})
		if _, asked := runsEffect[WorkspaceEffectResolveRunWorkspace](early); asked {
			t.Fatal("woke before the reset")
		}
		woke := runsResolveAll(Reduce(&paused.State, WorkspaceEventLimitsElapsed{At: limitSession.ResetsAt}), runsResolution)
		start, ok := runsEffect[WorkspaceEffectStartRun](woke)
		if !ok || !regexp.MustCompile(`usage limit that paused this thread has reset`).MatchString(start.Command.Prompt) {
			t.Fatalf("effects = %+v", woke.Effects)
		}
		messages := runsThreadIn(woke.State.Threads, "a").Messages
		if runsThreadIn(woke.State.Threads, "a").LimitPause != nil || messages[len(messages)-1].Text != "Usage limit reset. Resuming." || !runsIsTrue(RunRecord(woke.State.ActiveRuns["a"]).Warming) {
			t.Fatal("the thread did not resume warming up")
		}
	})
}

func TestLimitPausesOneAtATime(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		state := limitPaused(runsMidRun("a", "b"), "a", limitSession)
		state = limitPaused(state, "b", limitSession)
		now = limitSession.ResetsAt
		first := runsResolveAll(Reduce(state, WorkspaceEventLimitsElapsed{At: limitSession.ResetsAt}), runsResolution)
		if first.State.ActiveRuns["a"] == nil || runsThreadIn(first.State.Threads, "b").LimitPause == nil {
			t.Fatal("the line did not go one at a time")
		}
		runID := RunRecord(first.State.ActiveRuns["a"]).RunID
		warmed := runsResolveAll(Reduce(&first.State, WorkspaceEventRunEvent{Event: contracts.RunEventContextUsage{RunEventBase: runsEvent("a", runID, 1), Tokens: 10, Limit: 100, Model: "claude"}}), runsResolution)
		if RunRecord(warmed.State.ActiveRuns["a"]).Warming != nil || warmed.State.ActiveRuns["b"] == nil || runsThreadIn(warmed.State.Threads, "b").LimitPause != nil {
			t.Fatal("the next in line did not go once the first answered")
		}
	})
}

func TestLimitPausesWritingHoldsTheMessage(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		paused := limitPaused(runsMidRun("a"), "a", limitSession)
		sent := runsQueue(paused, "also fix the tests")
		if held := runsThreadIn(sent.Threads, "a").LimitPause.Held; len(held) != 1 || held[0].Text != "also fix the tests" {
			t.Fatal("the held message is not written down with the pause")
		}
		woke := runsResolveAll(Reduce(sent, WorkspaceEventLimitsElapsed{At: limitSession.ResetsAt}), runsResolution)
		if start, _ := runsEffect[WorkspaceEffectStartRun](woke); start.Command.Prompt != "also fix the tests" || !runsIsTrue(RunRecord(woke.State.ActiveRuns["a"]).UsersOwn) {
			t.Fatalf("start = %+v", start.Command)
		}
	})
}

func TestLimitPausesWeeklyWaitsForTheUser(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		paused := limitReached(runsMidRun("a"), "a", limitWeekly)
		thread := runsThreadIn(paused.State.Threads, "a")
		if thread.Outcome == nil || *thread.Outcome != "failed" || thread.RunEndedAs == nil || *thread.RunEndedAs != "failed" {
			t.Fatal("a weekly limit lost its verdict")
		}
		if _, scheduled := runsEffect[WorkspaceEffectScheduleLimitReset](paused); scheduled {
			t.Fatal("a weekly limit set a timer")
		}
		if later := Reduce(&paused.State, WorkspaceEventLimitsElapsed{At: limitWeekly.ResetsAt + 1}); runsThreadIn(later.State.Threads, "a").LimitPause == nil {
			t.Fatal("a weekly limit resumed on its own")
		}
		resumed := runsResolveAll(Reduce(&paused.State, contracts.RunControlCommandLimitResume{TaskID: "a"}), runsResolution)
		if resumed.State.ActiveRuns["a"] == nil || runsThreadIn(resumed.State.Threads, "a").LimitPause != nil {
			t.Fatal("the user's resume did not start it")
		}
	})
}

func TestLimitPausesCancelHandsBack(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		state := runsQueue(limitPaused(runsMidRun("a"), "a", limitSession), "later")
		cancelled := Reduce(state, contracts.RunControlCommandLimitCancel{TaskID: "a"})
		thread := runsThreadIn(cancelled.State.Threads, "a")
		if thread.LimitPause != nil || cancelled.State.QueuedMessages["a"] != nil || cancelled.State.Prompts["a"] != "later" {
			t.Fatal("cancelling did not hand the message back")
		}
		if thread.Outcome == nil || *thread.Outcome != "stopped" || *thread.RunEndedAs != "stopped" {
			t.Fatal("its turn did not end as a stopped run's does")
		}
		if reset, ok := runsEffect[WorkspaceEffectScheduleLimitReset](cancelled); !ok || reset.At != nil {
			t.Fatal("the timer was not cleared")
		}
	})
}

func TestLimitPausesStopOutlastsLimit(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		queued := runsReduce(runsMidRun("a"), contracts.TaskCommandTaskSend{TaskID: new("a"), Text: new("later"), Attachments: []*domain.RunAttachment{}})
		stopping := runsReduce(queued, contracts.RunControlCommandRunCancel{TaskID: new("a")})
		ended := limitReached(stopping, "a", limitSession)
		thread := runsThreadIn(ended.State.Threads, "a")
		if thread.LimitPause != nil || *thread.RunEndedAs != "stopped" || ended.State.Prompts["a"] != "later" {
			t.Fatal("a session limit outlasted the user's stop")
		}
		if _, announced := runsEffect[WorkspaceEffectAnnounceThread](ended); announced {
			t.Fatal("a stopped run was announced as a failure")
		}
	})
}

func TestLimitPausesHoldsEveryThreadOfTheEngine(t *testing.T) {
	now := float64(limitAt)
	runsPinNow(t, &now)
	runsPorted(t, func() {
		state := limitPaused(runsMidRun("a", "b"), "a", limitSession)
		state = limitPaused(state, "b", limitWeekly)
		if pause := runsThreadIn(state.Threads, "a").LimitPause; pause.ResetsAt != limitWeekly.ResetsAt || pause.Window != "weekly" {
			t.Fatalf("pause = %+v", pause)
		}
	})
}

func TestLimitPausesHeldByLimit(t *testing.T) {
	paused := func(window domain.UsageLimitWindow) *WorkspaceState {
		return &WorkspaceState{Threads: []*domain.Thread{runsTask("a", func(thread *domain.Thread) {
			thread.LimitPause = &domain.LimitPause{UsageLimit: domain.UsageLimit{ResetsAt: 10, Window: window}}
		})}}
	}
	if !HeldByLimit(paused("session"), "a", 20) || !HeldByLimit(paused("weekly"), "a", 5) || HeldByLimit(paused("weekly"), "a", 20) || HeldByLimit(paused("weekly"), "b", 5) {
		t.Fatal("held by limit")
	}
}
