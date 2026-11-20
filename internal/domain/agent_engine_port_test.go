package domain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// jsonEqual compares got, encoded, with want written as JSON.
func jsonEqual(t *testing.T, got any, want string, what string) {
	t.Helper()
	encoded, err := jsonx.Marshal(got)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	var left, right any
	if err := jsonx.Unmarshal(encoded, &left); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(want), &right); err != nil {
		t.Fatalf("%s: bad want: %v", what, err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("%s:\n got %s\nwant %s", what, encoded, want)
	}
}

func shippedEngines() []AgentEngine {
	shipped := []AgentEngine{}
	for _, engine := range AgentEngines {
		if _, ok := DefaultModelFor(string(engine)); ok {
			shipped = append(shipped, engine)
		}
	}
	return shipped
}

func TestEveryEngineDefaultsToAModelAndEffortItOffers(t *testing.T) {
	for _, engine := range shippedEngines() {
		model, _ := DefaultModelFor(string(engine))
		if !IsAgentEngine(engine) || !EngineHasModel(BuiltInModels, engine, model) || !EngineHasEffort(BuiltInModels, engine, DefaultEffortFor(string(engine))) {
			t.Fatalf("%s defaults", engine)
		}
		for _, spec := range ModelsFor(BuiltInModels, string(engine)) {
			for _, effort := range EffortsFor(BuiltInModels, engine, spec.ID, "") {
				if !IsAgentEffort(effort.ID) {
					t.Fatalf("%s effort %s", spec.ID, effort.ID)
				}
			}
		}
	}
}

func TestModelsBelongToTheirEngine(t *testing.T) {
	c := BuiltInModels
	if EngineHasModel(c, AgentEngineCodex, "opus") || EngineHasModel(c, AgentEngineClaude, "gpt-6.2-sol") {
		t.Fatal("models cross engines")
	}
	if EngineHasEffort(c, AgentEngineClaude, AgentEffortUltra) || !EngineHasEffort(c, AgentEngineCodex, AgentEffortUltra) || !EngineHasEffort(c, AgentEngineCodex, AgentEffortMax) || !EngineHasEffort(c, AgentEngineClaude, AgentEffortMax) {
		t.Fatal("efforts")
	}
	if IsAgentEngine("gpt") || IsAgentEngine("constructor") || CatalogHasModel(c, "claude-opus-4") || CatalogHasModel(c, "constructor") || !CatalogHasModel(c, "opus") {
		t.Fatal("guards")
	}
}

func TestModelIDShape(t *testing.T) {
	for _, id := range []string{"opus", "gpt-6.2-sol", "anthropic/claude-opus-5-5", "openrouter/meta/llama-4:free", strings.Repeat("x", 256)} {
		if !IsModelID(id) {
			t.Fatalf("%q should pass", id)
		}
	}
	for _, id := range []any{nil, 7.0, "", " ", "has space", "tab\tid", "line\nid", "nul\x00id", strings.Repeat("x", 257), []any{"opus"}, "constructor", "toString", "__proto__", "hasOwnProperty", "valueOf"} {
		if IsModelID(id) {
			t.Fatalf("%v should fail", id)
		}
	}
}

func TestForeignModelUsesDefaultContextWindow(t *testing.T) {
	for _, engine := range shippedEngines() {
		var foreign AgentModel
		for _, other := range shippedEngines() {
			for _, spec := range ModelsFor(BuiltInModels, string(other)) {
				if foreign == "" && !EngineHasModel(BuiltInModels, engine, spec.ID) {
					foreign = spec.ID
				}
			}
		}
		model, _ := DefaultModelFor(string(engine))
		if ContextWindowLimit(BuiltInModels, engine, foreign) != ContextWindowLimit(BuiltInModels, engine, model) {
			t.Fatalf("%s foreign window", engine)
		}
	}
}

func TestCapabilitiesAreCatalogueEntries(t *testing.T) {
	every := `["confirm","plan","allow-edits","autonomous","bypass"]`
	jsonEqual(t, CapabilitiesFor("codex"), `{"fastMode":true,"workflows":false,"subagents":true,"subagentMetadata":true,"review":true,"chromeBrowser":false,"policies":`+every+`,"unattendedPolicy":"autonomous"}`, "codex")
	jsonEqual(t, CapabilitiesFor("claude"), `{"fastMode":false,"workflows":true,"subagents":true,"subagentMetadata":true,"review":false,"chromeBrowser":true,"policies":`+every+`,"unattendedPolicy":"autonomous"}`, "claude")
	jsonEqual(t, CapabilitiesFor("opencode"), `{"fastMode":false,"workflows":false,"subagents":false,"subagentMetadata":false,"review":false,"chromeBrowser":false,"policies":["confirm","plan","allow-edits","bypass"],"unattendedPolicy":"bypass"}`, "opencode")
}

func TestManualCompactionBelongsToTheModel(t *testing.T) {
	c := BuiltInModels
	if !ModelSupportsManualCompaction(c, AgentEngineCodex, "gpt-6.2-sol") || !ModelSupportsManualCompaction(c, AgentEngineClaude, "opus") || ModelSupportsManualCompaction(c, AgentEngineClaude, "gpt-6.2-sol") {
		t.Fatal("manual compaction")
	}
}

var (
	high  = &EffortSpec{ID: AgentEffortHigh, Label: "High"}
	low   = &EffortSpec{ID: AgentEffortLow, Label: "Low"}
	named = []*ModelSpec{
		{ID: "openai/gpt-9", Label: "GPT-9", Description: "Named by the engine", ContextWindow: 400_000, Efforts: []*EffortSpec{high, low}, ManualCompaction: new(true)},
		{ID: "local/tiny", Label: "Tiny", Description: "No effort", ContextWindow: 8_000, Efforts: []*EffortSpec{}},
	}
)

func TestStatusWithoutCatalogueKeepsBuiltIn(t *testing.T) {
	if EngineCatalog(nil) != BuiltInModels {
		t.Fatal("nil status")
	}
	status := &EngineStatus{Claude: &EngineReadiness{Access: EngineAccessReady, Models: []AgentModel{"opus"}}, Codex: &EngineReadiness{Access: EngineAccessMissing}}
	if EngineCatalog(status) != BuiltInModels {
		t.Fatal("no catalogue named")
	}
}

func TestEngineNamingItsModelsReplacesItsOwnList(t *testing.T) {
	status := &EngineStatus{Codex: &EngineReadiness{Access: EngineAccessReady, Catalog: named}}
	own := EngineCatalog(status)
	ids := []string{}
	for _, spec := range ModelsFor(own, "codex") {
		ids = append(ids, spec.ID)
	}
	if !reflect.DeepEqual(ids, []string{"openai/gpt-9", "local/tiny"}) || !reflect.DeepEqual(ModelsFor(own, "claude"), ModelsFor(BuiltInModels, "claude")) {
		t.Fatalf("lists %v", ids)
	}
	if engine, _ := ModelEngine(own, "openai/gpt-9", ""); engine != AgentEngineCodex {
		t.Fatal("model engine")
	}
	if _, ok := ModelEngine(own, "gpt-6.2-sol", ""); ok {
		t.Fatal("replaced model still found")
	}
	if !EngineHasModel(own, AgentEngineCodex, "openai/gpt-9") || EngineHasModel(own, AgentEngineClaude, "openai/gpt-9") {
		t.Fatal("has model")
	}
	if ModelLabel(own, "codex", "openai/gpt-9") != "GPT-9" || ModelLabel(own, "codex", "gpt-6.2-sol") != "gpt-6.2-sol" {
		t.Fatal("labels")
	}
	if ContextWindowLimit(own, AgentEngineCodex, "openai/gpt-9") != 400_000 || !ModelSupportsManualCompaction(own, AgentEngineCodex, "openai/gpt-9") || ModelSupportsManualCompaction(own, AgentEngineCodex, "local/tiny") || ModelTakesEffort(own, AgentEngineCodex, "local/tiny") {
		t.Fatal("specs")
	}
	if EffortForModel(own, AgentEngineCodex, "openai/gpt-9", AgentEffortMax, "") != AgentEffortHigh || EffortForModel(own, AgentEngineCodex, "openai/gpt-9", AgentEffortMedium, "") != AgentEffortLow || EngineHasEffort(own, AgentEngineCodex, AgentEffortUltra) {
		t.Fatal("efforts")
	}
	if EngineCatalog(status) != own {
		t.Fatal("a status's catalogue is built once")
	}
	copied := *status
	if EngineCatalog(&copied) == own {
		t.Fatal("another status gets its own catalogue")
	}
}

func TestModelTwoEnginesName(t *testing.T) {
	gateway := *named[0]
	gateway.ID, gateway.Label, gateway.ContextWindow, gateway.Efforts = "opus", "Gateway Opus", 64_000, []*EffortSpec{low}
	again := *named[0]
	again.Label = "Again"
	own := EngineCatalog(&EngineStatus{Codex: &EngineReadiness{Access: EngineAccessReady, Catalog: []*ModelSpec{&gateway, named[0], &again}}})
	labels := []string{}
	for _, spec := range ModelsFor(own, "codex") {
		labels = append(labels, spec.Label)
	}
	if !reflect.DeepEqual(labels, []string{"Gateway Opus", "GPT-9"}) {
		t.Fatalf("an id named twice is listed once: %v", labels)
	}
	if !EngineHasModel(own, AgentEngineClaude, "opus") || !EngineHasModel(own, AgentEngineCodex, "opus") {
		t.Fatal("both offer opus")
	}
	if ModelLabel(own, "claude", "opus") != "Opus" || ModelLabel(own, "codex", "opus") != "Gateway Opus" {
		t.Fatal("labels")
	}
	if ContextWindowLimit(own, AgentEngineClaude, "opus") != 1_000_000 || ContextWindowLimit(own, AgentEngineCodex, "opus") != 64_000 {
		t.Fatal("windows")
	}
	if EffortForModel(own, AgentEngineClaude, "opus", AgentEffortMax, "") != AgentEffortMax || EffortForModel(own, AgentEngineCodex, "opus", AgentEffortMax, "") != AgentEffortLow {
		t.Fatal("efforts")
	}
	if engine, _ := ModelEngine(own, "opus", ""); engine != AgentEngineClaude {
		t.Fatal("first engine")
	}
	if engine, _ := ModelEngine(own, "opus", AgentEngineCodex); engine != AgentEngineCodex {
		t.Fatal("preferred engine")
	}
	if got, err := ResolveModel(own, "opus", nil, AgentEngineCodex); err != nil || got != (EngineModel{Engine: AgentEngineCodex, Model: "opus"}) {
		t.Fatal("resolve on codex")
	}
	if _, err := ResolveModel(own, "opus", nil, ""); err == nil || err.Error() != "opus could mean opus on Claude, opus on Codex. Name the engine as well, or pin the one you mean in the model picker." {
		t.Fatalf("ambiguous: %v", err)
	}
	if got, err := ResolveModel(own, "opus", []*EngineModel{{Engine: AgentEngineCodex, Model: "opus"}}, ""); err != nil || got.Engine != AgentEngineCodex {
		t.Fatal("a pin tells engines apart")
	}
	if _, err := ResolveModel(own, "gpt-6.2-sol", nil, AgentEngineCodex); err == nil || err.Error() != "Codex here does not offer the gpt-6.2-sol model. Pick another model." {
		t.Fatalf("unknown: %v", err)
	}
	if got, err := ResolveModel(BuiltInModels, "GPT 6.2 Sol", nil, ""); err != nil || got.Model != "gpt-6.2-sol" {
		t.Fatalf("plain name: %v %v", got, err)
	}
	if got, err := ResolveModel(BuiltInModels, "Sol", nil, ""); err != nil || got.Model != "gpt-6.2-sol" {
		t.Fatalf("by label: %v %v", got, err)
	}
}

func TestUnreadableSpecsAreLeftOut(t *testing.T) {
	bad := func(change func(spec *ModelSpec)) *ModelSpec {
		spec := *named[0]
		change(&spec)
		return &spec
	}
	unreadable := []*ModelSpec{nil, bad(func(s *ModelSpec) { s.ID = "two words" }), bad(func(s *ModelSpec) { s.ID = "constructor" }), bad(func(s *ModelSpec) { s.Efforts = nil }), bad(func(s *ModelSpec) { s.Efforts = []*EffortSpec{nil} }), bad(func(s *ModelSpec) { s.Efforts = []*EffortSpec{{ID: "galaxy"}} })}
	own := EngineCatalog(&EngineStatus{Codex: &EngineReadiness{Access: EngineAccessReady, Catalog: append(unreadable, named[1])}})
	if models := ModelsFor(own, "codex"); len(models) != 1 || models[0].ID != "local/tiny" {
		t.Fatalf("readable: %v", models)
	}
}

func TestEngineNoticesAndStatus(t *testing.T) {
	cases := []struct {
		engine     AgentEngine
		readiness  EngineReadiness
		word, line string
	}{
		{AgentEngineClaude, EngineReadiness{Access: EngineAccessReady}, "Ready", "Installed and ready."},
		{AgentEngineCodex, EngineReadiness{Access: EngineAccessSignedOut}, "Signed out", "Sign in to run threads on Codex."},
		{AgentEngineCodex, EngineReadiness{Access: EngineAccessMissing}, "Not installed", "Codex is not installed."},
		{AgentEngineClaude, EngineReadiness{Access: EngineAccessReady, Version: new("2.1.0"), Latest: new("2.2.0")}, "Update available", "Version 2.1.0. 2.2.0 is available; update it the way you installed it."},
		{AgentEngineClaude, EngineReadiness{Access: EngineAccessOutdated, Version: new("1.0"), Required: new("2.0"), Fix: new("npm i -g x")}, "Too old to run", "Claude 1.0 is too old. Update to 2.0."},
		{AgentEngineOpencode, EngineReadiness{Access: EngineAccessReady, Catalog: []*ModelSpec{}}, "Ready", "OpenCode has no models. Connect a provider first."},
		{AgentEngineOpencode, EngineReadiness{Access: EngineAccessReady, Version: new("2.2.5"), Required: new("2.3.0")}, "Needs update", "OpenCode 2.2.5 is behind 2.3.0, so some models are hidden."},
	}
	for _, c := range cases {
		if word, line := EngineStatusWord(&c.readiness), EngineStatusLine(c.engine, &c.readiness); word != c.word || line != c.line {
			t.Fatalf("%s: %q %q", c.engine, word, line)
		}
	}
	outdated := &EngineReadiness{Access: EngineAccessOutdated, Version: new("1.0"), Required: new("2.0"), Fix: new("npm i -g x")}
	if blocker, _ := EngineBlocker(AgentEngineClaude, outdated); blocker != "Claude 1.0 is too old. Update to 2.0. Update it in Settings." {
		t.Fatal(blocker)
	}
	missing := &EngineReadiness{Access: EngineAccessMissing, Fix: new("brew install codex")}
	if blocker, _ := EngineBlocker(AgentEngineCodex, missing); blocker != "Codex is not installed. Run `brew install codex` to fix it." {
		t.Fatal(blocker)
	}
	if _, blocked := EngineBlocker(AgentEngineClaude, &EngineReadiness{Access: EngineAccessReady, Required: new("3")}); blocked {
		t.Fatal("behind is no blocker")
	}
	jsonEqual(t, AgentEngineEngineUpdate(outdated), `{"target":"2.0","version":"1.0","command":"npm i -g x"}`, "update")
	if AgentEngineEngineUpdate(missing) != nil || !EngineNeedsAttention(&EngineStatus{Codex: missing}) || EngineNeedsAttention(nil) {
		t.Fatal("update / attention")
	}
}

func TestPoliciesAndDrafts(t *testing.T) {
	if PolicyFor(AgentEngineOpencode, ExecutionPolicyAutonomous) != ExecutionPolicyAllowEdits || PolicyFor(AgentEngineClaude, ExecutionPolicyPlan) != ExecutionPolicyPlan {
		t.Fatal("policyFor")
	}
	if AgentEngineClaudeEffort(AgentEffortUltra) != ClaudeEffortHigh || AgentEngineClaudeEffort(AgentEffortLow) != ClaudeEffortLow {
		t.Fatal("claude effort")
	}
	draft := NormalizeDraft(BuiltInModels, AgentDraft{Engine: AgentEngineOpencode, Effort: AgentEffortUltra, Policy: ExecutionPolicyAutonomous}, "")
	if draft != (AgentDraft{Engine: AgentEngineClaude, Model: "opus", Effort: AgentEffortMax, Policy: ExecutionPolicyAutonomous}) {
		t.Fatalf("an engine with no models gives way: %+v", draft)
	}
	draft = NormalizeDraft(BuiltInModels, AgentDraft{Engine: AgentEngineCodex, Model: "gpt-6.2-sol", Effort: AgentEffortUltra, Policy: ExecutionPolicyBypass}, CoordinationRoleCoordinator)
	if draft.Effort != AgentEffortMax || draft.Model != "gpt-6.2-sol" {
		t.Fatalf("a coordinator draft drops ultra: %+v", draft)
	}
}
