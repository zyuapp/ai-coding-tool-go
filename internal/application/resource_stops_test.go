package application

import (
	"regexp"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var resourceStop = domain.ResourceStop{At: 100, Bytes: 4 << 30, Cleanup: domain.ResourceStopCleanupStopping}

func resourceStoppedState() *WorkspaceState {
	initial := runsWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{runsProject}
		state.Threads = []*domain.Thread{
			runsTask("a", func(thread *domain.Thread) {
				thread.ProjectID = new(runsProject.ID)
				thread.Continuation = &domain.Continuation{Provider: "claude", Value: "history"}
				thread.ContinuationStatus = domain.ContinuationStatusAvailable
			}),
			runsTask("b"),
		}
		state.ActiveRuns = map[string]ActiveRun{"a": runsActive("a", "run-a"), "b": runsActive("b", "run-b")}
		state.CurrentID = new("b")
		state.Subagents = map[string][]*domain.Subagent{"a": {{ID: "child", Description: "background agent", Status: "working", SessionScoped: new(true), Activity: []*domain.SubagentActivity{}}}}
		state.BackgroundProcesses = map[string][]*domain.BackgroundProcess{"a": {{ID: "shell", Kind: "shell", Description: "work"}}}
	})
	return runsReduce(initial, WorkspaceEventThreadEvent{Event: contracts.ResourceStoppedEvent{TaskID: "a", Stop: resourceStop}})
}

func TestResourceStopEndsSessionWork(t *testing.T) {
	runsPorted(t, func() {
		state := resourceStoppedState()
		thread := state.Threads[0]
		if state.Subagents["a"][0].Status != "stopped" || len(state.BackgroundProcesses["a"]) != 0 || thread.ResourceStop.Cleanup != "stopping" || !runsIsTrue(thread.OutcomeUnread) || thread.Continuation.Value != "history" || state.ActiveRuns["b"] == nil {
			t.Fatal("a resource stop did not end session work alone")
		}
		state = runsReduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("a", "run-a", 1), Status: domain.RunStatusCancelled}})
		if state.ActiveRuns["a"] != nil || state.ActiveRuns["b"] == nil {
			t.Fatal("the stopped run did not end")
		}
		refused := Reduce(state, contracts.TaskCommandTaskSend{TaskID: new("a"), Text: new("restart")})
		if refused.State.ActionError == nil || !regexp.MustCompile(`Resume this thread`).MatchString(*refused.State.ActionError) {
			t.Fatal("a send to a stopped thread went")
		}
		if resumed := Reduce(state, contracts.RunControlCommandResourceResume{TaskID: "a"}); len(resumed.Effects) != 0 {
			t.Fatal("resumed before cleanup finished")
		}
		stale := Reduce(state, WorkspaceEventRunEvent{Event: contracts.RunEventRunStarted{RunEventBase: runsEvent("a", "late", 1), AgentInitiated: new(true)}})
		if stale.State.ActiveRuns["a"] != nil {
			t.Fatal("an agent's turn opened on a stopped thread")
		}
	})
}

func TestResourceStopResumeWaitsForCleanup(t *testing.T) {
	runsPorted(t, func() {
		state := runsReduce(resourceStoppedState(), WorkspaceEventRunEvent{Event: contracts.RunEventRunStatus{RunEventBase: runsEvent("a", "run-a", 1), Status: domain.RunStatusCancelled}})
		staleCleanup := resourceStop
		staleCleanup.At, staleCleanup.Cleanup = 99, domain.ResourceStopCleanupComplete
		state = runsReduce(state, WorkspaceEventThreadEvent{Event: contracts.ResourceStoppedEvent{TaskID: "a", Stop: staleCleanup}})
		if state.Threads[0].ResourceStop.Cleanup != "stopping" {
			t.Fatal("a stale cleanup was taken")
		}
		complete := resourceStop
		complete.Cleanup = domain.ResourceStopCleanupComplete
		state = runsReduce(state, WorkspaceEventThreadEvent{Event: contracts.ResourceStoppedEvent{TaskID: "a", Stop: complete}})
		resumed := Reduce(state, contracts.RunControlCommandResourceResume{TaskID: "a"})
		if resumed.State.Threads[0].ResourceStop != nil {
			t.Fatal("resume left the stop")
		}
		started := runsResolveAll(resumed, runsResolution)
		start, ok := runsEffect[WorkspaceEffectStartRun](started)
		if !ok || start.Command.Continuation.Value != "history" || !regexp.MustCompile(`Inspect the current state`).MatchString(start.Command.Prompt) {
			t.Fatalf("effects = %+v", started.Effects)
		}
	})
}
