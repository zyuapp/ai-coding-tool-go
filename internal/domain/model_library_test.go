package domain

import (
	"maps"
	"reflect"
	"testing"
)

func librarySpec(id, label, provider string) *ModelSpec {
	return &ModelSpec{ID: id, Label: label, Description: label + " by " + provider, Provider: new(provider), ContextWindow: 1, Efforts: []*EffortSpec{}}
}

var libraryStatus = &EngineStatus{Opencode: &EngineReadiness{Access: EngineAccessReady, Catalog: []*ModelSpec{
	librarySpec("openrouter/qwen-4", "Qwen 4", "OpenRouter"), librarySpec("anthropic/opus", "Opus", "Anthropic"), librarySpec("openrouter/glm-5", "GLM 5", "OpenRouter"),
}}}

func groupKeys(groups []*LibraryGroup) []string {
	keys := []string{}
	for _, group := range groups {
		keys = append(keys, group.Key)
	}
	return keys
}

func modelIDs(models []*ModelSpec) []string {
	ids := []string{}
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestLibraryHeadsModelsByProvider(t *testing.T) {
	catalog := EngineCatalog(libraryStatus)
	library := ModelLibraryModelLibrary(catalog, EveryEngineReady, LibraryOptions{Engine: "claude"})
	type heading struct {
		key, label string
		first      bool
	}
	got := []heading{}
	for _, group := range library.Available {
		got = append(got, heading{group.Key, group.Label, group.First})
		if !group.Selectable || group.Hint != nil {
			t.Fatalf("%s should be selectable without a hint", group.Key)
		}
	}
	want := []heading{{"claude", "Claude", true}, {"codex", "Codex", true}, {"opencode:OpenRouter", "OpenRouter", true}, {"opencode:Anthropic", "Anthropic", false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("headings %v", got)
	}
	if ids := modelIDs(library.Available[2].Models); !reflect.DeepEqual(ids, []string{"openrouter/qwen-4", "openrouter/glm-5"}) {
		t.Fatal(ids)
	}
	if library.Total != 11 || library.Count != 11 {
		t.Fatalf("total %v count %v", library.Total, library.Count)
	}
	locked := ModelLibraryModelLibrary(catalog, EveryEngineReady, LibraryOptions{Engine: "opencode", Locked: true})
	if keys := groupKeys(locked.Available); !reflect.DeepEqual(keys, []string{"opencode:OpenRouter", "opencode:Anthropic"}) {
		t.Fatal(keys)
	}
}

func TestLibraryHints(t *testing.T) {
	catalog := EngineCatalog(libraryStatus)
	access := maps.Clone(EveryEngineReady)
	access[AgentEngineClaude] = &EngineReadiness{Access: EngineAccessSignedOut}
	access[AgentEngineCodex] = &EngineReadiness{Access: EngineAccessMissing, Fix: new("brew install --cask codex")}
	access[AgentEngineOpencode] = &EngineReadiness{Access: EngineAccessReady, Version: new("2.2.5"), Required: new("2.3.0")}
	library := ModelLibraryModelLibrary(catalog, access, LibraryOptions{Engine: "claude"})
	byKey := map[string]*LibraryGroup{}
	for _, group := range library.Available {
		byKey[group.Key] = group
	}
	if _, ok := byKey["claude"].Hint.(LibraryHintSignIn); !ok || byKey["claude"].Selectable {
		t.Fatal("claude signs in")
	}
	jsonEqual(t, byKey["codex"].Hint, `{"kind":"notice","notice":{"blocking":true,"message":"Codex is not installed.","fix":"brew install --cask codex"}}`, "codex hint")
	if _, ok := byKey["opencode:OpenRouter"].Hint.(LibraryHintNotice); !ok || !byKey["opencode:OpenRouter"].Selectable || byKey["opencode:Anthropic"].Hint != nil {
		t.Fatal("a behind engine still runs, and says so once")
	}

	only := maps.Clone(EveryEngineReady)
	only[AgentEngineClaude] = &EngineReadiness{Access: EngineAccessReady, Models: []AgentModel{"opus"}}
	if ids := modelIDs(ModelLibraryModelLibrary(catalog, only, LibraryOptions{Engine: "claude"}).Available[0].Models); !reflect.DeepEqual(ids, []string{"opus"}) {
		t.Fatal(ids)
	}
}

func TestLibraryFilters(t *testing.T) {
	catalog := EngineCatalog(libraryStatus)
	favorites := []*EngineModel{{Engine: AgentEngineOpencode, Model: "anthropic/opus"}, {Engine: AgentEngineClaude, Model: "sonnet"}}
	pinned := ModelLibraryModelLibrary(catalog, EveryEngineReady, LibraryOptions{Engine: "claude", Favorites: favorites, Filter: new("favorites")})
	if pinned.Heading != "Favorites" || pinned.FavoriteCount != 2 {
		t.Fatal(pinned.Heading, pinned.FavoriteCount)
	}
	shown := map[string][]string{}
	for _, group := range pinned.Shown {
		shown[group.Key] = modelIDs(group.Models)
	}
	if !reflect.DeepEqual(groupKeys(pinned.Shown), []string{"claude", "opencode:Anthropic"}) || !reflect.DeepEqual(shown["claude"], []string{"sonnet"}) {
		t.Fatal(shown)
	}
	heading := ModelLibraryModelLibrary(catalog, EveryEngineReady, LibraryOptions{Engine: "claude", Filter: new("opencode:OpenRouter")})
	if heading.Heading != "OpenRouter" || !reflect.DeepEqual(groupKeys(heading.Shown), []string{"opencode:OpenRouter"}) {
		t.Fatal(heading.Heading)
	}
	found := ModelLibraryModelLibrary(catalog, EveryEngineReady, LibraryOptions{Engine: "claude", Filter: new("opencode:OpenRouter"), Query: new(" opus ")})
	if found.Heading != "Search results" || found.Count != 2 || !reflect.DeepEqual(groupKeys(found.Shown), []string{"claude", "opencode:Anthropic"}) || !reflect.DeepEqual(modelIDs(found.Shown[0].Models), []string{"opus"}) {
		t.Fatal(found.Heading, found.Count, groupKeys(found.Shown))
	}
}

func TestLibraryEmptyEngineAndOpening(t *testing.T) {
	access := maps.Clone(EveryEngineReady)
	access[AgentEngineOpencode] = &EngineReadiness{Access: EngineAccessMissing}
	for _, group := range ModelLibraryModelLibrary(BuiltInModels, access, LibraryOptions{Engine: "claude"}).Available {
		if group.Engine == AgentEngineOpencode {
			if _, ok := group.Hint.(LibraryHintNotice); group.Key != "opencode" || group.Label != "OpenCode" || len(group.Models) != 0 || !ok {
				t.Fatal("an engine yet to name its models keeps one empty heading")
			}
		}
	}
	catalog := EngineCatalog(libraryStatus)
	cases := []struct {
		options LibraryOpeningOptions
		want    string
	}{
		{LibraryOpeningOptions{Engine: AgentEngineOpencode, Locked: true, Model: "anthropic/opus"}, "opencode:Anthropic"},
		{LibraryOpeningOptions{Engine: AgentEngineOpencode, Locked: true, Model: "gone/model"}, "opencode:OpenRouter"},
		{LibraryOpeningOptions{Engine: AgentEngineClaude, Model: "opus", Favorites: []*EngineModel{{Engine: AgentEngineClaude, Model: "opus"}}}, "favorites"},
		{LibraryOpeningOptions{Engine: AgentEngineClaude, Model: "opus"}, "all"},
	}
	for _, c := range cases {
		if got := LibraryOpening(catalog, c.options); got != c.want {
			t.Fatalf("%+v: %s", c.options, got)
		}
	}
	if !reflect.DeepEqual(AgentEngineOfferedPolicies("claude"), []ExecutionPolicy{"autonomous", "bypass", "allow-edits", "confirm"}) || len(AgentEngineOfferedPolicies("galaxy")) != 0 {
		t.Fatal("offered policies")
	}
}
