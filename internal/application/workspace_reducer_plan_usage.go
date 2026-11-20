package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"slices"
)

var PlanUsageInputs = []string{"usage.read", "usage.reported", "usage.dismiss-warning"}

// askedElsewhere asks each connected computer for its own limits, without `everywhere`.
func askedElsewhere(state *WorkspaceState) []WorkspaceEffect {
	effects := []WorkspaceEffect{}
	for _, computer := range state.Computers.Paired {
		if computer.Status == domain.ComputerStatusConnected && contracts.SupportsComputerCommand(computer.Capabilities, contracts.PlanUsageCommandUsageRead{}) {
			effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: []WorkspaceInput{contracts.PlanUsageCommandUsageRead{}}})
		}
	}
	return effects
}

func ReducePlanUsage(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	usage := state.PlanUsage
	switch input := input.(type) {
	case contracts.PlanUsageCommandUsageRead:
		read := usage.Read + 1
		effects := []WorkspaceEffect{}
		for _, engine := range domain.AgentEngines {
			effects = append(effects, WorkspaceEffectReadPlanUsage{Engine: engine, Read: read})
		}
		if input.Everywhere != nil && *input.Everywhere {
			effects = append(effects, askedElsewhere(state)...)
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.PlanUsage = usage
			next.PlanUsage.Read = read
			next.PlanUsage.Reading = slices.Clone(domain.AgentEngines)
		}, Effects: effects}
	case contracts.PlanUsageCommandUsageDismissWarning:
		if slices.Contains(usage.Dismissed, input.Warning) {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.PlanUsage = usage
			next.PlanUsage.Dismissed = domain.WithUsageWarningDismissed(usage.Dismissed, input.Warning)
		}, Persists: true}
	case PlanUsageEvent:
		if input.Read != usage.Read {
			return SliceTransition{}
		}
		reports := usage.Reports
		switch input.Engine {
		case domain.AgentEngineClaude:
			reports.Claude = input.Usage
		case domain.AgentEngineCodex:
			reports.Codex = input.Usage
		case domain.AgentEngineOpencode:
			reports.Opencode = input.Usage
		}
		reading := []domain.AgentEngine{}
		for _, engine := range usage.Reading {
			if engine != input.Engine {
				reading = append(reading, engine)
			}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.PlanUsage = usage
			next.PlanUsage.Reports = reports
			next.PlanUsage.Reading = reading
		}}
	}
	return SliceTransition{}
}
