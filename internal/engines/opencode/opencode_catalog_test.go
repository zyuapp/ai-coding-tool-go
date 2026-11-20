package opencode

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func catalogFixture(t *testing.T) ([]*Model, []*ModelProvider) {
	t.Helper()
	var models []*Model
	var providers []*ModelProvider
	if err := jsonx.Unmarshal([]byte(`[
		{"id":"z-ai/glm-5.3-flash","providerID":"openrouter","name":"GLM-5.3-Flash","variants":[{"id":"none"},{"id":"low"},{"id":"max"},{"id":"thinking"},{"id":"high"}],"limit":{"context":1048576}},
		{"id":"gpt-5.6","providerID":"openai","name":"GPT-5.6","variants":[],"limit":{"context":400000}},
		{"id":"glm-5.3","providerID":"opencode-go","name":"GLM-5.3"},
		{"id":"hidden","providerID":"openrouter","enabled":false},
		{"id":"two words","providerID":"openrouter"}
	]`), &models); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(`[{"id":"openrouter","name":"OpenRouter"},{"id":"openai","name":"OpenAI"},{"id":"opencode-go","name":"OpenCode Go"}]`), &providers); err != nil {
		t.Fatal(err)
	}
	return models, providers
}

func specIDs(specs []*domain.ModelSpec) []string {
	ids := []string{}
	for _, spec := range specs {
		ids = append(ids, spec.ID)
	}
	return ids
}

func TestCatalogListsProvidersModelsWithTheDepthsTheAppNames(t *testing.T) {
	models, providers := catalogFixture(t)
	catalog := Catalog(models, providers, nil)
	if got, want := specIDs(catalog), []string{"openrouter/z-ai/glm-5.3-flash", "openai/gpt-5.6", "opencode-go/glm-5.3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids %v, want %v", got, want)
	}
	encoded, _ := jsonx.Marshal(catalog[0])
	want := `{"id":"openrouter/z-ai/glm-5.3-flash","label":"GLM-5.3-Flash","description":"z-ai/glm-5.3-flash","contextWindow":1048576,"efforts":[{"id":"max","label":"Max"},{"id":"high","label":"High"},{"id":"low","label":"Low"}],"manualCompaction":true,"provider":"OpenRouter"}`
	if string(encoded) != want {
		t.Fatalf("spec\n%s\nwant\n%s", encoded, want)
	}
	if len(catalog[1].Efforts) != 0 || catalog[1].Efforts == nil {
		t.Fatalf("a model with no variants reasons at one depth: %v", catalog[1].Efforts)
	}
	if catalog[2].Label != "GLM-5.3" || catalog[2].ContextWindow != 0 {
		t.Fatalf("a window the server does not name is unknown: %+v", catalog[2])
	}
}

func TestCatalogListsTheServersDefaultFirst(t *testing.T) {
	models, providers := catalogFixture(t)
	catalog := Catalog(models, providers, &Model{ID: "glm-5.3", ProviderID: "opencode-go"})
	if catalog[0].ID != "opencode-go/glm-5.3" {
		t.Fatalf("first %s", catalog[0].ID)
	}
	if got := specIDs(catalog)[1:]; !reflect.DeepEqual(got, []string{"openrouter/z-ai/glm-5.3-flash", "openai/gpt-5.6"}) {
		t.Fatalf("rest %v", got)
	}
	engines := domain.EngineCatalog(&domain.EngineStatus{Opencode: &domain.EngineReadiness{Access: domain.EngineAccessReady, Catalog: catalog}})
	if engine, ok := domain.ModelEngine(engines, "openrouter/z-ai/glm-5.3-flash", domain.DefaultEngine); !ok || engine != domain.AgentEngineOpencode {
		t.Fatalf("engine %v %v", engine, ok)
	}
	if got := len(domain.ModelsFor(engines, "opencode")); got != 3 {
		t.Fatalf("opencode models %d", got)
	}
	if got := specIDs(domain.ModelsFor(engines, "claude")); !reflect.DeepEqual(got, []string{"fable", "opus", "sonnet", "haiku"}) {
		t.Fatalf("the other engines keep their own models: %v", got)
	}
}

func TestModelIDSplitsAtItsFirstSlash(t *testing.T) {
	if got := AppModelID("openrouter", "z-ai/glm-5.3-flash"); got != "openrouter/z-ai/glm-5.3-flash" {
		t.Fatal(got)
	}
	if got := ModelRefOf("openrouter/z-ai/glm-5.3-flash", "high"); !reflect.DeepEqual(got, &ModelRef{ProviderID: "openrouter", ID: "z-ai/glm-5.3-flash", Variant: "high"}) {
		t.Fatal(got)
	}
	ref := ModelRefOf("openai/gpt-5.6", "")
	if encoded, _ := jsonx.Marshal(ref); string(encoded) != `{"providerID":"openai","id":"gpt-5.6"}` {
		t.Fatal(string(encoded))
	}
	for _, id := range []string{"opus", "/gpt", "openai/", ""} {
		if ModelRefOf(id, "") != nil {
			t.Fatalf("%q has a ref", id)
		}
	}
}
