package agent

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestWorkflowProgressBecomesPhasesAndOneRecordPerAgent(t *testing.T) {
	const start = 1_700_000_000_000
	progress, ok := ParseWorkflowProgress([]byte(`[
		{"type":"workflow_phase","index":0,"title":"Review","kind":"fan-out"},
		{"type":"workflow_log","message":"local branch is not pushed"},
		{"type":"workflow_agent","index":0,"label":"review:bugs","phaseIndex":0,"phaseTitle":"Review","state":"start","queuedAt":1700000000000},
		{"type":"workflow_agent","index":0,"label":"review:bugs","phaseIndex":0,"state":"done","queuedAt":1700000000000,"startedAt":1700000001000,"durationMs":70000,"tokens":41200,"toolCalls":12,"resultPreview":"3 findings"},
		{"type":"workflow_agent","index":1,"label":"review:perf","state":"progress","startedAt":1700000002000,"lastToolName":"Grep","model":"sonnet","isolation":"worktree","attempt":2,"cached":false}
	]`))
	equal(t, ok, true)
	equal(t, progress.Phases, []*domain.WorkflowPhase{{Index: 0, Title: "Review"}})
	equal(t, len(progress.Agents), 2)
	equal(t, progress.Agents[0], &domain.WorkflowAgent{
		Index: 0, Label: "review:bugs", State: domain.WorkflowAgentStateDone, PhaseIndex: ptr(0.0), ResultPreview: ptr("3 findings"),
		QueuedAt: ptr(float64(start)), StartedAt: ptr(float64(start + 1_000)), DurationMs: ptr(70_000.0), Tokens: ptr(41_200.0), ToolCalls: ptr(12.0),
	})
	equal(t, progress.Agents[1].State, domain.WorkflowAgentStateRunning)
	equal(t, *progress.Agents[1].Isolation, domain.WorkflowAgentIsolationWorktree)
	equal(t, *progress.Agents[1].Attempt, 2.0)
	equal(t, progress.Agents[1].Cached, (*bool)(nil), "only a cached agent carries the mark")
}

func TestWorkflowAgentStartingWithoutATimeIsQueued(t *testing.T) {
	progress, _ := ParseWorkflowProgress([]byte(`[{"type":"workflow_agent","index":3,"label":"verify:auth.ts","state":"start","queuedAt":1700000000000}]`))
	equal(t, progress.Agents[0].State, domain.WorkflowAgentStateQueued)
}

func TestWorkflowPayloadOfAnotherShapeCostsAFieldNotTheRun(t *testing.T) {
	_, ok := ParseWorkflowProgress(nil)
	equal(t, ok, false)
	_, ok = ParseWorkflowProgress([]byte(`"progress"`))
	equal(t, ok, false)
	empty, _ := ParseWorkflowProgress([]byte(`[]`))
	equal(t, empty, WorkflowProgressSnapshot{Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}})
	junk, _ := ParseWorkflowProgress([]byte(`[null, 7, {"type":"workflow_agent"}, {"type":"workflow_phase","index":0}]`))
	equal(t, junk, WorkflowProgressSnapshot{Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}})
	labelled, _ := ParseWorkflowProgress([]byte(`[{"type":"workflow_agent","index":0,"label":12,"startedAt":"soon"}]`))
	equal(t, labelled.Agents, []*domain.WorkflowAgent{{Index: 0, Label: "Agent 1", State: domain.WorkflowAgentStateQueued}})
}
