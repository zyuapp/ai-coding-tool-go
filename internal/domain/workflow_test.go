package domain

import (
	"strings"
	"testing"
)

const workflowStart = 1_700_000_000_000

func workflowOf(agents []*WorkflowAgent, phases []*WorkflowPhase) *Workflow {
	return &Workflow{ID: "wf-1", Name: "review-changes", Description: "Dynamic workflow", Status: WorkflowStatusRunning, Phases: phases, Agents: agents, StartedAt: workflowStart}
}

func workflowNumber(value float64) *float64 { return &value }

func TestWorkflowGroupsByPhase(t *testing.T) {
	model := workflowOf([]*WorkflowAgent{
		{Index: 3, Label: "loose", State: WorkflowAgentStateRunning},
		{Index: 2, Label: "verify:a", State: WorkflowAgentStateRunning, PhaseIndex: workflowNumber(1)},
		{Index: 0, Label: "review:b", State: WorkflowAgentStateDone, PhaseIndex: workflowNumber(0)},
		{Index: 1, Label: "review:a", State: WorkflowAgentStateDone, PhaseIndex: workflowNumber(0)},
	}, []*WorkflowPhase{{Index: 1, Title: "Verify"}, {Index: 0, Title: "Review"}})
	groups := WorkflowGroups(model)
	titles := []string{}
	for _, group := range groups {
		titles = append(titles, group.Key+"="+group.Title)
	}
	if strings.Join(titles, ",") != "phase-0=Review,phase-1=Verify,loose=Agents" {
		t.Fatal(titles)
	}
	if groups[0].Agents[0].Label != "review:b" || groups[0].Agents[1].Label != "review:a" {
		t.Fatal("spawn order within a phase")
	}
	if model.Agents[0].Label != "loose" || model.Agents[2].Label != "review:b" {
		t.Fatal("grouping leaves the workflow snapshot alone")
	}

	undeclared := WorkflowGroups(workflowOf([]*WorkflowAgent{
		{Index: 0, Label: "a", State: WorkflowAgentStateRunning, PhaseIndex: workflowNumber(2), PhaseTitle: shortcutsText("Fix")},
		{Index: 1, Label: "b", State: WorkflowAgentStateRunning, PhaseIndex: workflowNumber(1)},
	}, []*WorkflowPhase{}))
	if len(undeclared) != 2 || undeclared[0].Title != "Phase 2" || undeclared[1].Title != "Fix" {
		t.Fatal(featuresJSON(t, undeclared))
	}
}

func TestWorkflowLanesShareOrigin(t *testing.T) {
	now := float64(workflowStart + 100_000)
	model := workflowOf([]*WorkflowAgent{
		{Index: 0, Label: "first", State: WorkflowAgentStateDone, StartedAt: workflowNumber(workflowStart), DurationMs: workflowNumber(50_000)},
		{Index: 1, Label: "second", State: WorkflowAgentStateRunning, QueuedAt: workflowNumber(workflowStart + 50_000), StartedAt: workflowNumber(workflowStart + 75_000)},
	}, nil)
	span := WorkflowWorkflowSpan(model, now)
	if span != (WorkflowSpan{Start: workflowStart, End: now}) {
		t.Fatal(span)
	}
	featuresExpectJSON(t, WorkflowWorkflowBar(model.Agents[0], span, now), `{"run":{"left":0,"width":50}}`)
	featuresExpectJSON(t, WorkflowWorkflowBar(model.Agents[1], span, now), `{"queue":{"left":50,"width":25},"run":{"left":75,"width":25}}`)
	labels := []string{}
	for _, tick := range WorkflowTicks(span) {
		labels = append(labels, tick.Label)
	}
	if strings.Join(labels, ",") != "0:00,0:30,1:00,1:30" {
		t.Fatal(labels)
	}
}

func TestWorkflowEndedTookUnfinishedAgents(t *testing.T) {
	stopped := workflowOf([]*WorkflowAgent{{Index: 0, Label: "verify:a", State: WorkflowAgentStateRunning}}, nil)
	stopped.Status = WorkflowStatusStopped
	stopped.FinishedAt = workflowNumber(workflowStart + 10)
	if AgentStateIn(stopped, stopped.Agents[0]) != WorkflowAgentStateStopped || WorkflowAgentNote(stopped.Agents[0], WorkflowAgentStateStopped) != "Stopped with the run" {
		t.Fatal("stopped")
	}
	if AgentStateIn(workflowOf(stopped.Agents, nil), stopped.Agents[0]) != WorkflowAgentStateRunning {
		t.Fatal("running")
	}
	if WorkflowLabel(stopped) != "Stopped · 0/1 agents" {
		t.Fatal(WorkflowLabel(stopped))
	}
	stopped.Stopping = new(true)
	if WorkflowStatusLabel(stopped) != "Stopping" {
		t.Fatal("stopping")
	}
}

func TestWorkflowAgentNote(t *testing.T) {
	cached := true
	for _, check := range []struct {
		agent WorkflowAgent
		want  string
	}{
		{WorkflowAgent{State: WorkflowAgentStateRunning, LastToolName: shortcutsText("Grep")}, "Using Grep"},
		{WorkflowAgent{State: WorkflowAgentStateRunning}, "Working"},
		{WorkflowAgent{State: WorkflowAgentStateQueued}, "Queued"},
		{WorkflowAgent{State: WorkflowAgentStateDone, Cached: &cached}, "Replayed from an earlier run"},
		{WorkflowAgent{State: WorkflowAgentStateDone, ResultPreview: shortcutsText("\nconfirmed\nmore")}, "confirmed"},
		{WorkflowAgent{State: WorkflowAgentStateDone}, "Done"},
		{WorkflowAgent{State: WorkflowAgentStateError, Error: shortcutsText("no structured output")}, "no structured output"},
		{WorkflowAgent{State: WorkflowAgentStateError}, "Failed"},
	} {
		if got := WorkflowAgentNote(&check.agent, check.agent.State); got != check.want {
			t.Fatalf("%s, want %s", got, check.want)
		}
	}
	if FormatElapsed(3_723_000) != "1:02:03" || FormatElapsed(59_499) != "0:59" || FormatElapsed(-5) != "0:00" {
		t.Fatal("formatElapsed")
	}
	if _, ok := WorkflowAgentDuration(&WorkflowAgent{State: WorkflowAgentStateQueued}, 10); ok {
		t.Fatal("an agent that has neither queued nor started has no duration")
	}
	if duration, ok := WorkflowAgentDuration(&WorkflowAgent{State: WorkflowAgentStateRunning, QueuedAt: workflowNumber(4)}, 10); !ok || duration != 6 {
		t.Fatal(duration)
	}
}
