package opencode

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

type PermissionEffect string

const (
	EffectAllow PermissionEffect = "allow"
	EffectDeny  PermissionEffect = "deny"
	EffectAsk   PermissionEffect = "ask"
)

// One OpenCode permission rule. Rules are read in order and the last that matches decides.
type PermissionRule struct {
	Action   string           `json:"action"`
	Resource string           `json:"resource"`
	Effect   PermissionEffect `json:"effect"`
}

// The name OpenCode files the app's own tools under.
const AppServerName = "aicodingtool"
const ComputerUseServerName = "cua-driver"

// How OpenCode names a tool an MCP server offers.
func ToolNameOn(server, tool string) string {
	return server + "_" + tool
}

// Tools that only look, which every mode but a coordinator's lets run unasked, as Claude's default mode does.
var looks = []string{"read", "glob", "grep", "list", "skill", "question", "todowrite", "todoread", "lsp"}

func rule(action string, effect PermissionEffect) PermissionRule {
	return PermissionRule{Action: action, Resource: "*", Effect: effect}
}

func asksButLooks() []PermissionRule {
	rules := []PermissionRule{rule("*", EffectAsk)}
	for _, action := range looks {
		rules = append(rules, rule(action, EffectAllow))
	}
	return rules
}

// What a mode lets OpenCode's own tools do. Auto has no reviewer to stand on here, so it is held to Edits.
func modeRules(policy domain.ExecutionPolicy) []PermissionRule {
	switch policy {
	case domain.ExecutionPolicyBypass:
		return []PermissionRule{rule("*", EffectAllow)}
	case domain.ExecutionPolicyPlan:
		return append(asksButLooks(), rule("edit", EffectDeny))
	case domain.ExecutionPolicyAllowEdits, domain.ExecutionPolicyAutonomous:
		return append(asksButLooks(), rule("edit", EffectAllow), rule("external_directory", EffectAsk))
	default:
		return asksButLooks()
	}
}

type PolicySources struct {
	Policy           domain.ExecutionPolicy
	Channel          contracts.RunChannel
	CoordinationRole *domain.CoordinationRole
}

// The rules a session runs under: the mode's, then the app's tools each granted as Claude grants them,
// then what a coordinator may never do, which changes nothing itself whatever the mode.
func PermissionRules(input PolicySources, sets []agent.ServedToolSet, computerUse bool) []PermissionRule {
	rules := modeRules(input.Policy)
	scope := &agent.ApprovalScope{Policy: input.Policy, Channel: input.Channel}
	for _, set := range sets {
		for _, tool := range set.Tools {
			effect := EffectAsk
			if agent.GrantsTool(agent.ToolReachOf(agent.McpToolName(set.Server, tool.Name)), scope) {
				effect = EffectAllow
			}
			rules = append(rules, rule(ToolNameOn(AppServerName, tool.Name), effect))
		}
	}
	if computerUse {
		effect := EffectAsk
		if agent.GrantsTool(agent.ReachComputerUse, scope) {
			effect = EffectAllow
		}
		rules = append(rules, rule(ComputerUseServerName+"_*", effect))
	}
	if input.CoordinationRole != nil && *input.CoordinationRole == domain.CoordinationRoleCoordinator {
		rules = append(rules, rule("edit", EffectDeny), rule("shell", EffectDeny), rule("subagent", EffectDeny))
	}
	return rules
}

// The agent a mode runs as: OpenCode's own planner for Plan, its builder otherwise.
func AgentFor(policy domain.ExecutionPolicy) string {
	if policy == domain.ExecutionPolicyPlan {
		return "plan"
	}
	return "build"
}
