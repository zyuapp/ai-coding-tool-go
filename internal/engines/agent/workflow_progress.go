package agent

import (
	"encoding/json/jsontext"
	"fmt"
	"math"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The workflow tree the agent SDK sends on task_progress. Every field is read defensively so a shape
// change costs the panel a field, not the run.
type WorkflowProgressSnapshot struct {
	Phases []*domain.WorkflowPhase
	Agents []*domain.WorkflowAgent
}

func wpText(value any) *string {
	if text, ok := value.(string); ok && text != "" {
		return &text
	}
	return nil
}

func wpCount(value any) *float64 {
	if number, ok := value.(float64); ok && !math.IsInf(number, 0) && !math.IsNaN(number) {
		return &number
	}
	return nil
}

func wpFlag(value any) *bool {
	if value == true {
		flag := true
		return &flag
	}
	return nil
}

// A queued agent reports start before it has a start time; only a time means it is really running.
func wpAgentState(value any, startedAt *float64) domain.WorkflowAgentState {
	switch value {
	case "done":
		return domain.WorkflowAgentStateDone
	case "error":
		return domain.WorkflowAgentStateError
	case "progress":
		return domain.WorkflowAgentStateRunning
	}
	if startedAt == nil {
		return domain.WorkflowAgentStateQueued
	}
	return domain.WorkflowAgentStateRunning
}

func wpIsolation(value any) *domain.WorkflowAgentIsolation {
	if value == "worktree" || value == "remote" {
		isolation := domain.WorkflowAgentIsolation(value.(string))
		return &isolation
	}
	return nil
}

func readWorkflowAgent(entry map[string]any) *domain.WorkflowAgent {
	index := wpCount(entry["index"])
	if index == nil {
		return nil
	}
	startedAt := wpCount(entry["startedAt"])
	label := fmt.Sprintf("Agent %s", formatJSNumber(*index+1))
	if text := wpText(entry["label"]); text != nil {
		label = *text
	}
	return &domain.WorkflowAgent{
		Index:             *index,
		Label:             label,
		State:             wpAgentState(entry["state"], startedAt),
		PhaseIndex:        wpCount(entry["phaseIndex"]),
		PhaseTitle:        wpText(entry["phaseTitle"]),
		AgentID:           wpText(entry["agentId"]),
		AgentType:         wpText(entry["agentType"]),
		Model:             wpText(entry["model"]),
		Isolation:         wpIsolation(entry["isolation"]),
		Cached:            wpFlag(entry["cached"]),
		Attempt:           wpCount(entry["attempt"]),
		LastAttemptReason: wpText(entry["lastAttemptReason"]),
		LastToolName:      wpText(entry["lastToolName"]),
		LastToolSummary:   wpText(entry["lastToolSummary"]),
		PromptPreview:     wpText(entry["promptPreview"]),
		ResultPreview:     wpText(entry["resultPreview"]),
		Error:             wpText(entry["error"]),
		QueuedAt:          wpCount(entry["queuedAt"]),
		StartedAt:         startedAt,
		DurationMs:        wpCount(entry["durationMs"]),
		LastProgressAt:    wpCount(entry["lastProgressAt"]),
		Tokens:            wpCount(entry["tokens"]),
		ToolCalls:         wpCount(entry["toolCalls"]),
	}
}

func readWorkflowPhase(entry map[string]any) *domain.WorkflowPhase {
	index, title := wpCount(entry["index"]), wpText(entry["title"])
	if index == nil || title == nil {
		return nil
	}
	return &domain.WorkflowPhase{Index: *index, Title: *title}
}

// Reads one progress payload whole. The workflow republishes its entire tree each time, so the result
// replaces what came before rather than merging into it. False when the payload is not a list.
func ParseWorkflowProgress(value jsontext.Value) (WorkflowProgressSnapshot, bool) {
	var items []any
	if len(value) == 0 || jsonx.Unmarshal(value, &items) != nil || items == nil {
		return WorkflowProgressSnapshot{}, false
	}
	snapshot := WorkflowProgressSnapshot{Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}}
	positions := map[float64]int{}
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch entry["type"] {
		case "workflow_phase":
			if phase := readWorkflowPhase(entry); phase != nil {
				snapshot.Phases = append(snapshot.Phases, phase)
			}
		case "workflow_agent":
			agent := readWorkflowAgent(entry)
			if agent == nil {
				continue
			}
			// A later entry for the same index replaces the earlier one in its first position, as a Map does.
			if position, seen := positions[agent.Index]; seen {
				snapshot.Agents[position] = agent
			} else {
				positions[agent.Index] = len(snapshot.Agents)
				snapshot.Agents = append(snapshot.Agents, agent)
			}
		}
	}
	return snapshot, true
}

// A number as JavaScript's String() writes it, for the integers and plain decimals a label carries.
func formatJSNumber(number float64) string {
	if number == math.Trunc(number) && math.Abs(number) < 1e21 {
		return fmt.Sprintf("%.0f", number)
	}
	encoded, err := jsonx.Marshal(number)
	if err != nil {
		return fmt.Sprint(number)
	}
	return string(encoded)
}
