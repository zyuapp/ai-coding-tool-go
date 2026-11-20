package opencode

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

var policySets = []agent.ServedToolSet{
	{Server: agent.ThreadServerName, Tools: []agent.BoundTool{boundTool("list_threads"), boundTool("start_thread")}},
	{Server: agent.CoordinationServerName, Tools: []agent.BoundTool{boundTool("report_finding")}},
}

// What the rules decide for one call, read as OpenCode reads them: the last rule that matches.
func decideRule(rules []PermissionRule, action string) PermissionEffect {
	var effect PermissionEffect
	for _, rule := range rules {
		pattern := rule.Action
		if pattern == "*" || pattern == action || strings.HasSuffix(pattern, "*") && strings.HasPrefix(action, pattern[:len(pattern)-1]) {
			effect = rule.Effect
		}
	}
	return effect
}

func expectEffect(t *testing.T, rules []PermissionRule, action string, want PermissionEffect, why string) {
	t.Helper()
	if got := decideRule(rules, action); got != want {
		t.Errorf("%s: %s is %q, want %q", why, action, got, want)
	}
}

func mainRules(policy domain.ExecutionPolicy, sets []agent.ServedToolSet, computerUse bool) []PermissionRule {
	return PermissionRules(PolicySources{Policy: policy, Channel: contracts.RunChannelMain}, sets, computerUse)
}

func TestEachModeLetsOpenCodesToolsDoWhatItPromises(t *testing.T) {
	for _, policy := range []domain.ExecutionPolicy{domain.ExecutionPolicyConfirm, domain.ExecutionPolicyAllowEdits, domain.ExecutionPolicyPlan} {
		rules := mainRules(policy, nil, false)
		expectEffect(t, rules, "read", EffectAllow, string(policy)+" reads")
		expectEffect(t, rules, "grep", EffectAllow, string(policy))
		expectEffect(t, rules, "shell", EffectAsk, string(policy)+" asks before a command")
		expectEffect(t, rules, "webfetch", EffectAsk, string(policy))
	}
	expectEffect(t, mainRules(domain.ExecutionPolicyConfirm, nil, false), "edit", EffectAsk, "confirm")
	expectEffect(t, mainRules(domain.ExecutionPolicyAllowEdits, nil, false), "edit", EffectAllow, "allow-edits")
	expectEffect(t, mainRules(domain.ExecutionPolicyAllowEdits, nil, false), "external_directory", EffectAsk, "edits outside the checkout still ask")
	expectEffect(t, mainRules(domain.ExecutionPolicyPlan, nil, false), "edit", EffectDeny, "plan")
	expectEffect(t, mainRules(domain.ExecutionPolicyAutonomous, nil, false), "edit", EffectAllow, "Auto, which OpenCode is never offered, is held to Edits")
	expectEffect(t, mainRules(domain.ExecutionPolicyAutonomous, nil, false), "shell", EffectAsk, "autonomous")
	for _, action := range []string{"shell", "edit", "external_directory", "webfetch"} {
		expectEffect(t, mainRules(domain.ExecutionPolicyBypass, nil, false), action, EffectAllow, "bypass")
	}
	if AgentFor(domain.ExecutionPolicyPlan) != "plan" || AgentFor(domain.ExecutionPolicyConfirm) != "build" {
		t.Error("agents")
	}
}

func TestAppToolsAreGrantedToolByTool(t *testing.T) {
	rules := mainRules(domain.ExecutionPolicyConfirm, policySets, false)
	expectEffect(t, rules, "aicodingtool_list_threads", EffectAllow, "reading threads changes nothing")
	expectEffect(t, rules, "aicodingtool_start_thread", EffectAsk, "starting one does")
	expectEffect(t, rules, "aicodingtool_report_finding", EffectAllow, "coordination")
	expectEffect(t, mainRules(domain.ExecutionPolicyBypass, policySets, false), "aicodingtool_start_thread", EffectAllow, "bypass")
}

func TestComputerUseIsAskedAboutUnlessGranted(t *testing.T) {
	expectEffect(t, mainRules(domain.ExecutionPolicyConfirm, nil, true), "cua-driver_click", EffectAsk, "confirm")
	expectEffect(t, mainRules(domain.ExecutionPolicyBypass, nil, true), "cua-driver_click", EffectAllow, "bypass")
	for _, rule := range mainRules(domain.ExecutionPolicyConfirm, nil, false) {
		if strings.HasPrefix(rule.Action, "cua-driver") {
			t.Error("computer use ruled without it")
		}
	}
}

func TestACoordinatorChangesNothing(t *testing.T) {
	role := domain.CoordinationRoleCoordinator
	rules := PermissionRules(PolicySources{Policy: domain.ExecutionPolicyBypass, Channel: contracts.RunChannelMain, CoordinationRole: &role}, policySets, false)
	for _, action := range []string{"edit", "shell", "subagent"} {
		expectEffect(t, rules, action, EffectDeny, "coordinator")
	}
	expectEffect(t, rules, "read", EffectAllow, "coordinator")
	expectEffect(t, rules, "aicodingtool_start_thread", EffectAllow, "coordinator")
}
