package application

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestRunsApprovalLeadsUntilAnswered(t *testing.T) {
	runsPorted(t, func() {
		state := runsMidRun("task-a")
		requested := Reduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventApprovalRequested{RunEventBase: runsEvent("task-a", "run-task-a", 1), ApprovalID: "approval-a", Title: "Write", Description: "Review the write.", Intent: domain.ToolIntent{ToolID: "tool", Name: "Write", Input: jsontext.Value(`{}`)}}})
		awaiting := Reduce(&requested.State, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 2), Status: domain.RunStatusAwaitingApproval}})
		if RunRecord(awaiting.State.ActiveRuns["task-a"]).Status != "awaiting-approval" {
			t.Fatal("not awaiting approval")
		}
		for _, wrong := range []contracts.RunControlCommandRunDecide{
			{TaskID: "task-a", RunID: "run-old", ApprovalID: "approval-a", Allow: true},
			{TaskID: "task-a", RunID: "run-task-a", ApprovalID: "approval-old", Allow: true},
			{TaskID: "task-b", RunID: "run-task-a", ApprovalID: "approval-a", Allow: true},
		} {
			if decided := Reduce(&awaiting.State, wrong); len(decided.Effects) != 0 {
				t.Fatalf("decision %+v reached a run it was not for", wrong)
			}
		}
		decided := Reduce(&awaiting.State, contracts.RunControlCommandRunDecide{TaskID: "task-a", RunID: "run-task-a", ApprovalID: "approval-a", Allow: true})
		command, ok := runsEffect[WorkspaceEffectSendRunCommand](decided)
		if decision, isDecision := command.Command.(contracts.ApprovalDecisionCommand); !ok || !isDecision || !decision.Allow || decision.ApprovalID != "approval-a" {
			t.Fatalf("effects = %+v", decided.Effects)
		}
		if RunRecord(decided.State.ActiveRuns["task-a"]).Status != "running" || decided.State.Approvals["run-task-a"] != nil {
			t.Fatal("an answered approval still blocks the run")
		}
	})
}

func TestRunsStreamingTailStaysOutOfTheThread(t *testing.T) {
	runsPorted(t, func() {
		state := runsMidRun("task-a")
		tailed := Reduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventAssistantTail{RunEventBase: runsEvent("task-a", "run-task-a", 1), MessageID: "m", Text: "Half"}})
		if tail := tailed.State.StreamingTails["task-a"]; tail == nil || tail.Text != "Half" || len(tailed.State.Threads[0].Messages) != 0 {
			t.Fatal("a tail was stored in the thread")
		}
		emptied := Reduce(&tailed.State, WorkspaceEventRunEvent{Event: contracts.RunEventAssistantTail{RunEventBase: runsEvent("task-a", "run-task-a", 2), MessageID: "m"}})
		if _, ok := emptied.State.StreamingTails["task-a"]; ok {
			t.Fatal("an emptied tail was kept")
		}
	})
}

func TestRunsStopProcessOnce(t *testing.T) {
	runsPorted(t, func() {
		state := runsMidRun("task-a")
		state.BackgroundProcesses = map[string][]*domain.BackgroundProcess{"task-a": {{ID: "bash-1", Kind: "shell", Description: "npm run dev"}}}
		stopping := Reduce(state, contracts.RunControlCommandRunStopProcess{TaskID: new("task-a"), ProcessID: "bash-1"})
		command, ok := runsEffect[WorkspaceEffectSendRunCommand](stopping)
		if stop, isStop := command.Command.(contracts.StopProcessCommand); !ok || !isStop || stop.ProcessID != "bash-1" || !runsIsTrue(stopping.State.BackgroundProcesses["task-a"][0].Stopping) {
			t.Fatalf("effects = %+v", stopping.Effects)
		}
		if again := Reduce(&stopping.State, contracts.RunControlCommandRunStopProcess{TaskID: new("task-a"), ProcessID: "bash-1"}); len(again.Effects) != 0 {
			t.Fatal("a stop already asked for was asked again")
		}
	})
}

func TestRunsCancelAsksTheRunOnce(t *testing.T) {
	runsPorted(t, func() {
		cancelling := Reduce(runsMidRun("task-a"), contracts.RunControlCommandRunCancel{TaskID: new("task-a")})
		command, ok := runsEffect[WorkspaceEffectSendRunCommand](cancelling)
		if cancel, isCancel := command.Command.(contracts.CancelRunCommand); !ok || !isCancel || cancel.RunID != "run-task-a" || !runsIsTrue(RunRecord(cancelling.State.ActiveRuns["task-a"]).Cancelling) {
			t.Fatalf("effects = %+v", cancelling.Effects)
		}
	})
}

func TestRunsThreadEventForAMissingThreadChangesNothing(t *testing.T) {
	runsPorted(t, func() {
		state := runsMidRun("task-a")
		event := contracts.WorkflowEventWorkflowStarted{TaskID: "gone", ID: "wf", Name: "wf", Description: "d"}
		if after := Reduce(state, WorkspaceEventThreadEvent{Event: event}); len(after.State.Workflows) != 0 {
			t.Fatal("a missing thread took a workflow")
		}
		landed := Reduce(state, WorkspaceEventThreadEvent{Event: contracts.WorkflowEventWorkflowStarted{TaskID: "task-a", ID: "wf", Name: "wf", Description: "d"}})
		if len(landed.State.Workflows["task-a"]) != 1 {
			t.Fatal("a workflow frame did not land on its thread")
		}
	})
}

func TestRunsResolvedStartRefusedOnStoppedThread(t *testing.T) {
	runsPorted(t, func() {
		stopped := runsTask("a", func(thread *domain.Thread) {
			thread.ProjectID = new(runsProject.ID)
			thread.ResourceStop = &domain.ResourceStop{At: 100, Bytes: 4 << 30, Cleanup: domain.ResourceStopCleanupComplete}
		})
		for _, operation := range []PendingRunOperation{nil, PendingRunOperationCompact{}, PendingRunOperationResume{After: "update"}} {
			pending := &PendingRun{ID: "pending", RunID: "pending-run", Origin: PendingRunOriginComposer, TaskID: new("a"), ProjectID: new(runsProject.ID), Text: "work", Prompt: "work", Attachments: []string{}, Operation: operation}
			state := runsWorkspace(func(state *WorkspaceState) {
				state.Projects = []*domain.Project{runsProject}
				state.Threads = []*domain.Thread{stopped}
				state.PendingRuns = map[string]*PendingRun{"pending": pending}
			})
			result := Reduce(state, WorkspaceEventRunResolved{PendingID: "pending", Workspace: runsResolution, Worktree: &contracts.CreatedWorktree{ID: "created-checkout", Root: "/tmp/created-checkout", WorkspaceID: "created-workspace", BaseCommit: "base", CreatedAt: 1, LastUsedAt: 1}})
			if _, started := runsEffect[WorkspaceEffectStartRun](result); started || result.State.ActiveRuns["a"] != nil || result.State.PendingRuns["pending"] != nil {
				t.Fatalf("%T: a stopped thread started a run", operation)
			}
			if runsThreadIn(result.State.Threads, "a").ResourceStop == nil || len(result.State.Worktrees) != 1 {
				t.Fatalf("%T: the stop or the made checkout was lost", operation)
			}
		}
	})
}
