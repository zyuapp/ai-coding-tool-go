package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func sendingQueued(state *WorkspaceState, taskID string) []string {
	var texts []string
	for _, message := range state.QueuedMessages[taskID] {
		texts = append(texts, message.Text)
	}
	return texts
}

func TestSendingComposerSendWaitsForWorkspace(t *testing.T) {
	runsPorted(t, func() {
		drafted := runsReduce(runsWorkspace(), runsSetPrompt("Inspect the app"))
		sending := Reduce(drafted, runsSend())
		request, ok := runsEffect[WorkspaceEffectResolveRunWorkspace](sending)
		if !ok || len(sending.Effects) != 1 || request.Picker || len(sending.State.Threads) != 0 {
			t.Fatalf("effects = %+v", sending.Effects)
		}
		started := Reduce(&sending.State, WorkspaceEventRunResolved{PendingID: request.PendingID, Workspace: runsProjectless})
		start, ok := started.Effects[0].(WorkspaceEffectStartRun)
		if !ok || start.Command.Prompt != "Inspect the app" || start.Command.Title != "Inspect the app" || start.Command.WorkspaceID != "projectless" {
			t.Fatalf("effects = %+v", started.Effects)
		}
		if started.State.Threads[0].Messages[0].Text != "Inspect the app" || RunRecord(started.State.ActiveRuns[start.Command.TaskID]).RunID != start.Command.RunID {
			t.Fatal("the run did not start on the new thread")
		}
		if len(started.State.Prompts) != 0 || len(started.State.PendingRuns) != 0 {
			t.Fatal("the draft or the start was left behind")
		}
		if second := Reduce(&sending.State, runsSend()); len(second.Effects) != 0 || len(second.State.PendingRuns) != 1 {
			t.Fatal("a second send went while the first was resolving")
		}
	})
}

func TestSendingUnresolvedReportsWhy(t *testing.T) {
	runsPorted(t, func() {
		drafted := runsReduce(runsWorkspace(func(state *WorkspaceState) {
			state.Projects = []*domain.Project{{ID: "project-1", Root: "/project"}}
			state.DraftProjectID = new("project-1")
		}), runsSetPrompt("Continue"))
		sending := Reduce(drafted, runsSend())
		request, _ := runsEffect[WorkspaceEffectResolveRunWorkspace](sending)
		if !request.Picker || *request.Root != "/project" {
			t.Fatalf("request = %+v", request)
		}
		failed := Reduce(&sending.State, WorkspaceEventRunUnresolved{PendingID: request.PendingID, Message: "Choose the same project folder to continue this task."})
		if failed.State.ActionError == nil || *failed.State.ActionError != "Choose the same project folder to continue this task." || len(failed.State.PendingRuns) != 0 {
			t.Fatal("an unresolved run left something behind")
		}
	})
}

func TestSendingQueuedDuringRun(t *testing.T) {
	runsPorted(t, func() {
		queued := Reduce(runsReduce(runsMidRun("task-a"), runsSetPrompt("Also run the tests")), runsSend())
		if len(queued.Effects) != 0 || !slices.Equal(sendingQueued(&queued.State, "task-a"), []string{"Also run the tests"}) || len(queued.State.Prompts) != 0 || len(queued.State.PendingRuns) != 0 {
			t.Fatalf("queued = %+v", queued.State.QueuedMessages)
		}
	})
}

func TestSendingSteerAndDeliver(t *testing.T) {
	runsPorted(t, func() {
		queued := runsQueue(runsMidRun("task-a"), "Check the tests too")
		message := queued.QueuedMessages["task-a"][0]
		steered := Reduce(queued, contracts.TaskCommandTaskSteerQueued{MessageID: message.ID})
		command, ok := runsEffect[WorkspaceEffectSendRunCommand](steered)
		steer, isSteer := command.Command.(contracts.SteerRunCommand)
		if !ok || !isSteer || steer.TaskID != "task-a" || steer.RunID != "run-task-a" || steer.MessageID != message.ID || steer.Prompt != "Check the tests too" {
			t.Fatalf("effects = %+v", steered.Effects)
		}
		if !runsIsTrue(steered.State.QueuedMessages["task-a"][0].Steering) {
			t.Fatal("not locked while steering")
		}
		if again := Reduce(&steered.State, contracts.TaskCommandTaskSteerQueued{MessageID: message.ID}); len(again.Effects) != 0 {
			t.Fatal("steering twice sent twice")
		}
		if dropped := Reduce(&steered.State, contracts.TaskCommandTaskDropQueued{MessageID: message.ID}); len(dropped.State.QueuedMessages["task-a"]) != 1 {
			t.Fatal("a steered message was dropped")
		}
		delivered := Reduce(&steered.State, WorkspaceEventRunEvent{Event: contracts.RunEventQueuedDelivered{RunEventBase: runsEvent("task-a", "run-task-a", 1), MessageID: message.ID}})
		messages := delivered.State.Threads[0].Messages
		if len(delivered.State.QueuedMessages) != 0 || messages[len(messages)-1].Text != "Check the tests too" {
			t.Fatal("delivery did not thread the message")
		}
	})
}

func TestSendingRejectedSteerStaysQueued(t *testing.T) {
	runsPorted(t, func() {
		queued := runsQueue(runsQueue(runsMidRun("task-a"), "Check the tests too"), "Then update the docs")
		message, other := queued.QueuedMessages["task-a"][0], queued.QueuedMessages["task-a"][1]
		steering := runsReduce(queued, contracts.TaskCommandTaskSteerQueued{MessageID: message.ID})
		event := contracts.RunEventQueuedSteerFailed{RunEventBase: runsEvent("task-a", "run-task-a", 1), MessageID: message.ID, Message: "Could not steer. Your message is still queued for the next turn."}
		failed := Reduce(steering, WorkspaceEventRunEvent{Event: event})
		held := failed.State.QueuedMessages["task-a"]
		if *failed.State.ActionError != event.Message || len(held) != 2 || held[0].Steering == nil || *held[0].Steering || held[1] != other || len(failed.Effects) != 0 {
			t.Fatalf("failed = %+v", held)
		}
		finished := Reduce(&failed.State, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 2), Status: domain.RunStatusSucceeded}})
		request, ok := runsEffect[WorkspaceEffectResolveRunWorkspace](finished)
		if !ok || finished.State.PendingRuns[request.PendingID].Prompt != message.Prompt {
			t.Fatal("the message left queued was not the next turn")
		}
		stale := event
		stale.RunID = "old-run"
		if after := Reduce(steering, WorkspaceEventRunEvent{Event: stale}); after.State.ActionError != nil {
			t.Fatal("a stale run showed an error")
		}
		side := *steering
		side.SideChats = []*SideChat{{ID: "task-a", SourceThreadID: "parent"}}
		sideFailed := Reduce(&side, WorkspaceEventRunEvent{Event: event})
		if sideFailed.State.SideChats[0].Error == nil || *sideFailed.State.SideChats[0].Error != event.Message || sideFailed.State.ActionError != nil {
			t.Fatal("a side chat's steer error went to the banner")
		}
	})
}

func TestSendingDrainsQueueOneAtATime(t *testing.T) {
	runsPorted(t, func() {
		queued := runsQueue(runsQueue(runsMidRun("task-a"), "Run the tests"), "Then update the README")
		drain := func(state *WorkspaceState, runID string, waiting int) WorkspaceTransition {
			finished := Reduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", runID, 1), Status: domain.RunStatusSucceeded}})
			request, ok := runsEffect[WorkspaceEffectResolveRunWorkspace](finished)
			if !ok || len(finished.State.QueuedMessages["task-a"]) != waiting {
				t.Fatalf("finished = %+v", finished.Effects)
			}
			return Reduce(&finished.State, WorkspaceEventRunResolved{PendingID: request.PendingID, Workspace: runsResolution})
		}
		first := drain(queued, "run-task-a", 2)
		start, _ := runsEffect[WorkspaceEffectStartRun](first)
		if start.Command.Prompt != "Run the tests" || !slices.Equal(sendingQueued(&first.State, "task-a"), []string{"Then update the README"}) {
			t.Fatalf("first = %q %v", start.Command.Prompt, sendingQueued(&first.State, "task-a"))
		}
		second := drain(&first.State, start.Command.RunID, 1)
		if next, _ := runsEffect[WorkspaceEffectStartRun](second); next.Command.Prompt != "Then update the README" || len(second.State.QueuedMessages) != 0 {
			t.Fatal("the second message did not get its own run")
		}
	})
}

func TestSendingStopHandsQueueBack(t *testing.T) {
	runsPorted(t, func() {
		cancelled := Reduce(runsQueue(runsMidRun("task-a"), "Run the tests"), WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 1), Status: domain.RunStatusCancelled}})
		if _, asked := runsEffect[WorkspaceEffectResolveRunWorkspace](cancelled); asked || len(cancelled.State.QueuedMessages) != 0 || cancelled.State.Prompts["task-a"] != "Run the tests" || cancelled.State.Threads[0].RunEndedAt == nil {
			t.Fatal("a stopped run spoke for the user")
		}
	})
}

func TestSendingWhileQueueStartingWaitsBehind(t *testing.T) {
	runsPorted(t, func() {
		finished := Reduce(runsQueue(runsMidRun("task-a"), "Run the tests"), WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 1), Status: domain.RunStatusSucceeded}})
		sending := runsQueue(&finished.State, "Then the docs")
		if !slices.Equal(sendingQueued(sending, "task-a"), []string{"Run the tests", "Then the docs"}) || len(sending.PendingRuns) != 1 {
			t.Fatalf("queued = %v", sendingQueued(sending, "task-a"))
		}
	})
}

func TestSendingUnstartableQueuedRunHandsBack(t *testing.T) {
	runsPorted(t, func() {
		finished := Reduce(runsQueue(runsQueue(runsMidRun("task-a"), "Run the tests"), "Then the docs"), WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 1), Status: domain.RunStatusSucceeded}})
		request, _ := runsEffect[WorkspaceEffectResolveRunWorkspace](finished)
		unresolved := Reduce(&finished.State, WorkspaceEventRunUnresolved{PendingID: request.PendingID, Message: "Choose the project folder again."})
		if len(unresolved.State.QueuedMessages) != 0 || unresolved.State.Prompts["task-a"] != "Run the tests\n\nThen the docs" {
			t.Fatalf("prompt = %q", unresolved.State.Prompts["task-a"])
		}
	})
}

func TestSendingDropQueued(t *testing.T) {
	runsPorted(t, func() {
		queued := runsQueue(runsQueue(runsMidRun("task-a"), "First"), "Second")
		first := queued.QueuedMessages["task-a"][0]
		dropped := Reduce(queued, contracts.TaskCommandTaskDropQueued{MessageID: first.ID})
		if len(dropped.Effects) != 0 || !slices.Equal(sendingQueued(&dropped.State, "task-a"), []string{"Second"}) {
			t.Fatal("drop removed the wrong message")
		}
		if again := Reduce(&dropped.State, contracts.TaskCommandTaskDropQueued{MessageID: first.ID}); again.Result != nil {
			t.Fatal("a message already gone answered")
		}
		asked := Reduce(&dropped.State, contracts.TaskCommandTaskDropQueued{MessageID: first.ID, IfWaiting: new(true)})
		if refused, ok := asked.Result.(contracts.WorkspaceCommandResultFalse); !ok || refused.Message != QueuedSent {
			t.Fatal("asked, it did not say the message had gone")
		}
	})
}

func TestSendingEditQueued(t *testing.T) {
	runsPorted(t, func() {
		queued := runsReduce(runsQueue(runsQueue(runsMidRun("task-a"), "First"), "Second"), runsSetPrompt("Still typing"))
		first, second := queued.QueuedMessages["task-a"][0], queued.QueuedMessages["task-a"][1]
		edited := Reduce(queued, contracts.TaskCommandTaskEditQueued{MessageID: first.ID})
		if len(edited.Effects) != 0 || len(edited.State.QueuedMessages["task-a"]) != 1 || edited.State.QueuedMessages["task-a"][0] != second || edited.State.Prompts["task-a"] != "First\n\nStill typing" {
			t.Fatalf("prompt = %q", edited.State.Prompts["task-a"])
		}
		steering := runsReduce(queued, contracts.TaskCommandTaskSteerQueued{MessageID: first.ID})
		if after := Reduce(steering, contracts.TaskCommandTaskEditQueued{MessageID: first.ID}); len(after.State.QueuedMessages["task-a"]) != 2 {
			t.Fatal("a steered message was taken back")
		}
		starting := Reduce(queued, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("task-a", "run-task-a", 1), Status: domain.RunStatusSucceeded}})
		if after := Reduce(&starting.State, contracts.TaskCommandTaskEditQueued{MessageID: first.ID}); len(after.State.QueuedMessages["task-a"]) != 2 {
			t.Fatal("a message a starting run is taking was taken back")
		}
		view := QueuedView(&starting.State, "task-a")
		if len(view) != 2 || !runsIsTrue(view[0].Starting) || view[1].Starting != nil {
			t.Fatal("the view does not mark what a starting run takes")
		}
	})
}

func TestSendingNamedMissingTaskChangesNothing(t *testing.T) {
	runsPorted(t, func() {
		state := runsMidRun("task-a")
		sent := Reduce(state, contracts.TaskCommandTaskSend{TaskID: new("gone"), Text: new("hello"), Attachments: []*domain.RunAttachment{}})
		if len(sent.Effects) != 0 || len(sent.State.PendingRuns) != 0 || len(sent.State.QueuedMessages) != 0 {
			t.Fatal("a send to a missing thread did something")
		}
	})
}
