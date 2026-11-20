package domain

import "testing"

func TestBuiltInCatalog(t *testing.T) {
	if got := len(BuiltInModels.Models.Claude); got != 4 {
		t.Fatalf("claude models = %d", got)
	}
	if len(BuiltInModels.Models.Opencode) != 0 || len(BuiltInModels.Models.Codex) != 4 {
		t.Fatal("codex/opencode lists")
	}
	if !EngineHasModel(BuiltInModels, AgentEngineCodex, "gpt-6.2-sol") || EngineHasModel(BuiltInModels, AgentEngineClaude, "gpt-6.2-sol") {
		t.Fatal("engine models")
	}
	if got := EffortsFor(BuiltInModels, AgentEngineCodex, "gpt-6.2-sol", CoordinationRoleCoordinator); len(got) != 5 || got[0].ID != AgentEffortMax {
		t.Fatalf("coordinator efforts drop ultra: %v", got)
	}
	if got := AgentEngineOfferedPolicies("opencode"); len(got) != 3 || got[0] != ExecutionPolicyBypass {
		t.Fatalf("opencode policies = %v", got)
	}
	if EveryEngineReady[AgentEngineOpencode].Access != EngineAccessReady {
		t.Fatal("every engine ready")
	}
	if IsModelID("toString") || IsModelID("a b") || !IsModelID("anthropic/claude-x") {
		t.Fatal("model ids")
	}
	if len(NewID()) != 36 {
		t.Fatal("uuid shape")
	}
}
