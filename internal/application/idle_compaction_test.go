package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const idleEndedAt = 1_000_000.0

var idleDue = idleEndedAt + 54*60_000

// idleWorkspace is one large Claude thread whose last run ended at idleEndedAt, on a plan with room left.
func idleWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	thread := runsTask("a", func(thread *domain.Thread) {
		thread.ProjectID = new(runsProject.ID)
		thread.Model = new(domain.AgentModel("opus"))
		thread.Continuation = &domain.Continuation{Provider: "claude", Value: "session"}
		thread.ContextUsage = &domain.ContextUsage{Tokens: 250_000, Limit: 1_000_000, Model: "claude-opus"}
		thread.RunEndedAt = new(idleEndedAt)
	})
	return runsWorkspace(append([]func(*WorkspaceState){func(state *WorkspaceState) {
		state.Projects = []*domain.Project{runsProject}
		state.Threads = []*domain.Thread{thread}
		state.PlanUsage.Reports.Claude = domain.PlanUsageAvailable{Windows: []*domain.UsageWindow{{ID: "session", Label: "Session", Utilization: new(10.0)}}}
	}}, edits...)...)
}

func idleScheduled(t *testing.T, transition WorkspaceTransition) *float64 {
	t.Helper()
	return coreEffect[WorkspaceEffectScheduleIdleCompaction](t, transition).At
}

func idleStarts(transition WorkspaceTransition) bool {
	for _, effect := range transition.Effects {
		if _, ok := effect.(WorkspaceEffectResolveRunWorkspace); ok {
			return true
		}
	}
	return false
}

func TestALargeIdleClaudeThreadIsCompactedOnceBeforeItsCacheExpires(t *testing.T) {
	loaded := Reduce(idleWorkspace(), WorkspaceEventPreferencesLoaded{Preferences: ViewPreferences(EmptyWorkspaceState(nil))})
	if at := idleScheduled(t, loaded); at == nil || *at != idleDue {
		t.Fatalf("scheduled %v, want %v", at, idleDue)
	}

	fired := runsResolveAll(Reduce(&loaded.State, WorkspaceEventIdleCompactionDue{At: idleDue}), runsResolution)
	record := RunRecord(fired.State.ActiveRuns["a"])
	if record == nil || !runsIsTrue(record.Idle) || record.Operation == nil || *record.Operation != ActiveRunRunningOperationCompact {
		t.Fatalf("no idle compaction began: %s", coreJSON(t, fired.State.ActiveRuns))
	}
	compacted := runsReduce(&fired.State,
		WorkspaceEventRunEvent{Event: contracts.RunEventContextCompacted{RunEventBase: runsEvent("a", record.RunID, 1), Trigger: "manual", PreTokens: 250_000, PostTokens: new(30_000.0)}},
	)
	thread := runThreadOf(compacted.Threads, "a")
	if last := thread.Messages[len(thread.Messages)-1]; last.Text != "Compacted while idle: 250,000 → 30,000 tokens." {
		t.Fatalf("compaction said %q", last.Text)
	}
	ended := Reduce(compacted, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("a", record.RunID, 2), Status: domain.RunStatusSucceeded}})
	if at := runThreadOf(ended.State.Threads, "a").RunEndedAt; at == nil || *at != idleEndedAt {
		t.Fatalf("the compaction moved the thread's run end to %v", at)
	}
	if at := idleScheduled(t, ended); at != nil {
		t.Fatalf("scheduled again at %v", *at)
	}
}

func TestAFailedIdleCompactionIsNotTriedAgainUntilTheThreadRunsAgain(t *testing.T) {
	fired := runsResolveAll(Reduce(idleWorkspace(), WorkspaceEventIdleCompactionDue{At: idleDue}), runsResolution)
	runID := RunRecord(fired.State.ActiveRuns["a"]).RunID
	failed := Reduce(&fired.State, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("a", runID, 1), Status: domain.RunStatusFailed, Message: new("Overloaded")}})
	if at := idleScheduled(t, failed); at != nil {
		t.Fatalf("scheduled again at %v", *at)
	}
	if again := Reduce(&failed.State, WorkspaceEventIdleCompactionDue{At: idleDue + 1}); idleStarts(again) {
		t.Fatal("compacted the same idle stretch twice")
	}
}

func TestAnIdleThreadIsLeftAloneWhenCompactingWouldNotHelp(t *testing.T) {
	cases := map[string]struct {
		at    float64
		edits []func(*WorkspaceState)
	}{
		"its cache may already be gone": {at: idleDue + 2*60_000},
		"a message waits for it": {at: idleDue, edits: []func(*WorkspaceState){func(state *WorkspaceState) {
			state.QueuedMessages = map[string][]*QueuedMessage{"a": {{HeldMessage: domain.HeldMessage{ID: "m", Text: "next", Prompt: "next"}}}}
		}}},
		"its plan is nearly spent": {at: idleDue, edits: []func(*WorkspaceState){func(state *WorkspaceState) {
			state.PlanUsage.Reports.Claude = domain.PlanUsageAvailable{Windows: []*domain.UsageWindow{{ID: "week", Label: "Week", Utilization: new(85.0)}}}
		}}},
		"it bills no plan": {at: idleDue, edits: []func(*WorkspaceState){func(state *WorkspaceState) {
			state.PlanUsage.Reports.Claude = domain.PlanUsageNotApplicable{}
		}}},
		"the setting is off": {at: idleDue, edits: []func(*WorkspaceState){func(state *WorkspaceState) { state.IdleCompaction = false }}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fired := Reduce(idleWorkspace(c.edits...), WorkspaceEventIdleCompactionDue{At: c.at})
			if idleStarts(fired) {
				t.Fatal("compacted")
			}
			if again := Reduce(&fired.State, WorkspaceEventIdleCompactionDue{At: c.at + 1}); idleStarts(again) {
				t.Fatal("compacted on the next tick")
			}
		})
	}
}

func TestTurningIdleCompactionOffCancelsTheTimer(t *testing.T) {
	loaded := Reduce(idleWorkspace(), WorkspaceEventPreferencesLoaded{Preferences: ViewPreferences(EmptyWorkspaceState(nil))})
	off := Reduce(&loaded.State, contracts.ViewCommandViewSetIdleCompaction{Enabled: false})
	if at := idleScheduled(t, off); at != nil {
		t.Fatalf("still scheduled at %v", *at)
	}
	on := Reduce(&off.State, contracts.ViewCommandViewSetIdleCompaction{Enabled: true})
	if at := idleScheduled(t, on); at == nil || *at != idleDue {
		t.Fatalf("scheduled %v, want %v", at, idleDue)
	}
}

func TestAnIdleCompactionThatRunsTooLongIsStopped(t *testing.T) {
	fired := runsResolveAll(Reduce(idleWorkspace(), WorkspaceEventIdleCompactionDue{At: idleDue}), runsResolution)
	runID := RunRecord(fired.State.ActiveRuns["a"]).RunID
	deadline := idleDue + 10*60_000
	if at := idleScheduled(t, fired); at == nil || *at != deadline {
		t.Fatalf("watching until %v, want %v", at, deadline)
	}
	if early := Reduce(&fired.State, WorkspaceEventIdleCompactionDue{At: deadline - 1}); len(early.Effects) > 1 {
		t.Fatalf("stopped early: %s", coreJSON(t, early.Effects))
	}
	stopped := Reduce(&fired.State, WorkspaceEventIdleCompactionDue{At: deadline})
	cancel, ok := coreEffect[WorkspaceEffectSendRunCommand](t, stopped).Command.(contracts.CancelRunCommand)
	if !ok || cancel.RunID != runID {
		t.Fatalf("no cancel: %s", coreJSON(t, stopped.Effects))
	}
}

func TestAPlanThatCouldNotBeReadDoesNotStopIdleCompaction(t *testing.T) {
	unread := idleWorkspace(func(state *WorkspaceState) { state.PlanUsage.Reports.Claude = nil })
	if !idleStarts(Reduce(unread, WorkspaceEventIdleCompactionDue{At: idleDue})) {
		t.Fatal("not compacted")
	}
}
