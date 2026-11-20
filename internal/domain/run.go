package domain

import (
	"slices"
	"strings"
)

var ExecutionPolicies = []ExecutionPolicy{ExecutionPolicyConfirm, ExecutionPolicyPlan, ExecutionPolicyAllowEdits, ExecutionPolicyAutonomous, ExecutionPolicyBypass}

// IsExecutionPolicy accepts an ExecutionPolicy or a string.
func IsExecutionPolicy(value any) bool {
	switch v := value.(type) {
	case ExecutionPolicy:
		return slices.Contains(ExecutionPolicies, v)
	case string:
		return slices.Contains(ExecutionPolicies, ExecutionPolicy(v))
	}
	return false
}

// PolicyText is how a mode reads wherever it is named.
type PolicyText struct {
	Label       string
	Description string
}

var Policies = map[ExecutionPolicy]PolicyText{
	ExecutionPolicyAutonomous: {Label: "Auto", Description: "Only ask for potentially unsafe actions"},
	ExecutionPolicyBypass:     {Label: "Bypass", Description: "Use tools and change files without asking"},
	ExecutionPolicyAllowEdits: {Label: "Edits", Description: "Apply file edits without asking"},
	ExecutionPolicyConfirm:    {Label: "Confirm", Description: "Ask before using tools or changing files"},
	ExecutionPolicyPlan:       {Label: "Plan", Description: "Plan the work without doing it"},
}

// PolicyChoices is the modes a picker offers, in the order it lists them.
var PolicyChoices = []ExecutionPolicy{ExecutionPolicyAutonomous, ExecutionPolicyBypass, ExecutionPolicyAllowEdits, ExecutionPolicyConfirm}

const DefaultEffort AgentEffort = AgentEffortHigh

var RunSubagentGroups = []SubagentGroup{SubagentGroupSidebar, SubagentGroupWorking, SubagentGroupIdle, SubagentGroupFailed, SubagentGroupStopped, SubagentGroupCompleted}

// OpenSubagentGroups is every group unfolded. Shared: clone before changing.
var OpenSubagentGroups = SubagentGroups{
	SubagentGroupSidebar: true, SubagentGroupWorking: true, SubagentGroupIdle: true,
	SubagentGroupFailed: true, SubagentGroupStopped: true, SubagentGroupCompleted: true,
}

// IsSubagentGroup accepts a SubagentGroup or a string.
func IsSubagentGroup(value any) bool {
	switch v := value.(type) {
	case SubagentGroup:
		return slices.Contains(RunSubagentGroups, v)
	case string:
		return slices.Contains(RunSubagentGroups, SubagentGroup(v))
	}
	return false
}

func SubagentStatusLabel(status SubagentStatus) string {
	switch status {
	case SubagentStatusWorking:
		return "Working"
	case SubagentStatusIdle:
		return "Idle"
	case SubagentStatusCompleted:
		return "Completed"
	case SubagentStatusFailed:
		return "Failed"
	case SubagentStatusStopped:
		return "Stopped"
	}
	return ""
}

func SubagentLine(subagent *Subagent) string {
	if subagent.Status == SubagentStatusWorking {
		if subagent.LastToolName != nil && *subagent.LastToolName != "" {
			return "Using " + *subagent.LastToolName
		}
		return "Working"
	}
	if subagent.Summary != nil {
		first, _, _ := strings.Cut(*subagent.Summary, "\n")
		return first
	}
	return SubagentStatusLabel(subagent.Status)
}

var subagentStatusRank = map[SubagentStatus]int{
	SubagentStatusFailed: 0, SubagentStatusWorking: 1, SubagentStatusIdle: 2, SubagentStatusStopped: 3, SubagentStatusCompleted: 4,
}

// OrderSubagents returns a sorted copy, stable within a status.
func OrderSubagents(subagents []*Subagent) []*Subagent {
	ordered := slices.Clone(subagents)
	slices.SortStableFunc(ordered, func(left, right *Subagent) int {
		return subagentStatusRank[left.Status] - subagentStatusRank[right.Status]
	})
	return ordered
}

func ProcessLabel(process *BackgroundProcess) string {
	if process.Stopping != nil && *process.Stopping {
		return "Stopping"
	}
	if process.Kind == BackgroundProcessKindShell {
		return "Shell"
	}
	return "Monitor"
}
