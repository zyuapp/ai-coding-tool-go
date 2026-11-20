package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// ResourceStopped is a session stop, which also ends work whose parent turn finished earlier.
func ResourceStopped(state *WorkspaceState, event contracts.ResourceStoppedEvent) Step {
	thread := runThreadOf(state.Threads, event.TaskID)
	if thread == nil || event.Stop.Cleanup != domain.ResourceStopCleanupStopping && (thread.ResourceStop == nil || thread.ResourceStop.At != event.Stop.At) {
		return Step{}
	}
	first := thread.ResourceStop == nil || thread.ResourceStop.At != event.Stop.At
	threads := ResourceStopIn(ThreadsOf(state), thread.ID, new(event.Stop))
	runs := RunsOf(state)
	var effects []WorkspaceEffect
	if first {
		const summary = "Stopped due to high memory usage."
		threads = VerdictIn(threads, thread.ID, domain.ThreadOutcomeStopped, !ThreadOnScreen(state, thread.ID))
		runs = ThreadEventIn(runs, contracts.BackgroundEvent{TaskID: thread.ID, BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{}}})
		runs = ThreadEventIn(runs, contracts.GoalEvent{TaskID: thread.ID})
		for _, child := range state.Subagents[thread.ID] {
			if child.Status == domain.SubagentStatusWorking || child.Status == domain.SubagentStatusIdle {
				runs = ThreadEventIn(runs, contracts.SubagentEventSubagentFinished{TaskID: thread.ID, ID: child.ID, Status: domain.SubagentTerminalStatusStopped, Summary: summary})
			}
		}
		for _, workflow := range state.Workflows[thread.ID] {
			if workflow.Status == domain.WorkflowStatusRunning {
				runs = ThreadEventIn(runs, contracts.WorkflowEventWorkflowFinished{TaskID: thread.ID, ID: workflow.ID, Status: contracts.WorkflowEventWorkflowFinishedStatusStopped, Summary: summary})
			}
		}
		effects = Announced(state, thread, "Stopped due to high memory usage", NoticeReasonFailed)
	}
	return AndThen(state, Step{Edits: []OwnerEdit{threads, runs}, Effects: effects}, func(next *WorkspaceState) Step {
		return Drained(next, thread.ID, QueueIntentHandBack)
	})
}

func ResourceResumed(state *WorkspaceState, taskID string) Step {
	thread := runThreadOf(state.Threads, taskID)
	if thread == nil || thread.ResourceStop == nil || thread.ResourceStop.Cleanup != domain.ResourceStopCleanupComplete || ThreadBusy(state, taskID) {
		return Step{}
	}
	pending := &PendingRun{
		ID:          NewID(),
		RunID:       NewID(),
		TaskID:      new(taskID),
		Origin:      PendingRunOriginComposer,
		Operation:   PendingRunOperationResume{After: PendingRunOperationResumeAfterMemory},
		Text:        "",
		Prompt:      "Continue after the session was stopped for high memory usage. Inspect the current state before repeating any interrupted action. Avoid the allocation or command that caused the excessive memory use.",
		Attachments: []string{},
	}
	threads := ResourceStopIn(ThreadsOf(state), taskID, nil)
	return Step{
		Edits:   []OwnerEdit{threads, PendingIn(RunsOf(state), pending)},
		Effects: []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, thread, ProjectFor(state, thread), WorktreeFor(state, thread), false, nil)},
	}
}
