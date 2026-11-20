package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// runsPorted runs body, skipping the test while it reaches a function another group has yet to port.
func runsPorted(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if text := fmt.Sprint(recovered); strings.Contains(text, "unported: ") {
				t.Skipf("blocked: %s", text)
			}
			panic(recovered)
		}
	}()
	body()
}

// runsTask is a thread as the TS fixtures' task() makes one.
func runsTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title = id, id
	thread.Engine = domain.AgentEngine("claude")
	thread.ExecutionPolicy = domain.ExecutionPolicyConfirm
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt = 1
	for _, edit := range edits {
		edit(thread)
	}
	if thread.RankedAt == 0 {
		thread.RankedAt = domain.ThreadLastOutputAt(thread)
	}
	return thread
}

// runsActive is a live run as the TS fixtures' activeRun() makes one.
func runsActive(taskID, runID string, edits ...func(*ActiveRunRunning)) ActiveRun {
	run := ActiveRunRunning{
		TaskID:         taskID,
		RunID:          runID,
		Status:         ActiveRunRunningStatusRunning,
		Origin:         ActiveRunRunningOriginComposer,
		ReportedIssues: []string{},
		Before:         ThreadMark{UpdatedAt: 1},
	}
	for _, edit := range edits {
		edit(&run)
	}
	return run
}

// runsWorkspace is the empty workspace with edits made to it.
func runsWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := *EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(&state)
	}
	return &state
}

func runsThreads(threads ...*domain.Thread) func(*WorkspaceState) {
	return func(state *WorkspaceState) { state.Threads = threads }
}

func runsRunning(runs ...ActiveRun) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.ActiveRuns = map[string]ActiveRun{}
		for _, run := range runs {
			state.ActiveRuns[RunRecord(run).TaskID] = run
		}
	}
}

func runsThreadIn(threads []*domain.Thread, id string) *domain.Thread {
	for _, thread := range threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}

// runsPinNow holds the reducer's clock at *at for the rest of the test; the test may move it.
func runsPinNow(t *testing.T, at *float64) {
	t.Helper()
	held := Now
	Now = func() float64 { return *at }
	t.Cleanup(func() { Now = held })
}

// runsReduce runs inputs through Reduce in turn, as the renderer does.
func runsReduce(state *WorkspaceState, inputs ...WorkspaceInput) *WorkspaceState {
	for _, input := range inputs {
		next := Reduce(state, input)
		state = &next.State
	}
	return state
}

// runsEffect is the first effect of type E a transition asks for.
func runsEffect[E WorkspaceEffect](transition WorkspaceTransition) (E, bool) {
	for _, effect := range transition.Effects {
		if found, ok := effect.(E); ok {
			return found, true
		}
	}
	var none E
	return none, false
}

var runsProject = &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}

var runsResolution = domain.WorkspaceRecord{ID: "workspace-a", Kind: domain.WorkspaceKindProject, Root: "/repo"}

var runsProjectless = domain.WorkspaceRecord{ID: "projectless", Kind: domain.WorkspaceKindProjectless, Root: "/tmp"}

// runsResolveAll answers every workspace request the transition asked for, the way the host does.
func runsResolveAll(transition WorkspaceTransition, workspace domain.WorkspaceRecord) WorkspaceTransition {
	current := transition
	for _, effect := range transition.Effects {
		if request, ok := effect.(WorkspaceEffectResolveRunWorkspace); ok {
			current = Reduce(&current.State, WorkspaceEventRunResolved{PendingID: request.PendingID, Workspace: workspace})
		}
	}
	return current
}

// runsMidRun is a task mid-run, the only state a message can be queued or steered in.
func runsMidRun(ids ...string) *WorkspaceState {
	return runsWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{runsProject}
		state.CurrentID = new(ids[0])
		state.ActiveRuns = map[string]ActiveRun{}
		state.RunStatuses = map[string]ThreadRunStatus{}
		for _, id := range ids {
			state.Threads = append(state.Threads, runsTask(id, func(thread *domain.Thread) { thread.ProjectID = new(runsProject.ID) }))
			state.ActiveRuns[id] = runsActive(id, "run-"+id)
			state.RunStatuses[id] = ThreadRunStatusRunning
		}
	})
}

func runsEvent(taskID, runID string, sequence float64) contracts.RunEventBase {
	return contracts.RunEventBase{TaskID: taskID, RunID: runID, Sequence: sequence}
}

func runsSetPrompt(prompt string) WorkspaceInput {
	return contracts.ViewCommandViewSetPrompt{Prompt: prompt}
}

func runsSend() contracts.TaskCommandTaskSend {
	return contracts.TaskCommandTaskSend{Attachments: []*domain.RunAttachment{}}
}

// runsQueue types text into the current thread's composer and sends it.
func runsQueue(state *WorkspaceState, text string) *WorkspaceState {
	return runsReduce(state, runsSetPrompt(text), runsSend())
}
