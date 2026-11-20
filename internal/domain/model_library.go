package domain

import "strings"

// EveryEngineReady is every engine taken as ready. Shared: never change it.
var EveryEngineReady = ByEngine(func(AgentEngine) *EngineReadiness {
	return &EngineReadiness{Access: EngineAccessReady}
})

// libraryHeading is TypeScript's Heading: a LibraryGroup before what the machine allows is applied.
type libraryHeading struct {
	Key    string
	Engine AgentEngine
	Label  string
	First  bool
	Models []*ModelSpec
}

// headings memoizes headingsOf by catalogue pointer.
var headings weakMemo[ModelCatalog, []*libraryHeading]

func headingsOf(catalog *ModelCatalog) []*libraryHeading {
	if built, ok := headings.get(catalog); ok {
		return built
	}
	built := []*libraryHeading{}
	for _, engine := range AgentEngines {
		providers := []string{}
		byProvider := map[string][]*ModelSpec{}
		for _, spec := range ModelsFor(catalog, string(engine)) {
			provider := ""
			if spec.Provider != nil {
				provider = *spec.Provider
			}
			if _, seen := byProvider[provider]; !seen {
				providers = append(providers, provider)
			}
			byProvider[provider] = append(byProvider[provider], spec)
		}
		if len(providers) == 0 {
			providers = append(providers, "")
			byProvider[""] = []*ModelSpec{}
		}
		for index, provider := range providers {
			heading := &libraryHeading{Key: string(engine), Engine: engine, Label: EngineLabel(string(engine)), First: index == 0, Models: byProvider[provider]}
			if provider != "" {
				heading.Key = string(engine) + ":" + provider
				heading.Label = provider
			}
			built = append(built, heading)
		}
	}
	headings.set(catalog, built)
	return built
}

func hintOf(heading *libraryHeading, readiness *EngineReadiness) LibraryHint {
	if !heading.First {
		return nil
	}
	if readiness.Access == EngineAccessSignedOut {
		return LibraryHintSignIn{}
	}
	if notice := AgentEngineEngineNotice(heading.Engine, readiness); notice != nil {
		return LibraryHintNotice{Notice: *notice}
	}
	return nil
}

// ModelLibraryModelLibrary is the library a model picker draws. access holds every engine.
func ModelLibraryModelLibrary(catalog *ModelCatalog, access map[AgentEngine]*EngineReadiness, options LibraryOptions) *ModelLibrary {
	filter := "all"
	if options.Filter != nil {
		filter = *options.Filter
	}
	search := ""
	if options.Query != nil {
		search = jsLowerTrim(*options.Query)
	}
	pinned := func(group *LibraryGroup, spec *ModelSpec) bool {
		return IsFavorite(options.Favorites, group.Engine, spec.ID)
	}
	available := []*LibraryGroup{}
	for _, heading := range headingsOf(catalog) {
		if options.Locked && string(heading.Engine) != options.Engine {
			continue
		}
		readiness := access[heading.Engine]
		models := []*ModelSpec{}
		for _, spec := range heading.Models {
			if readiness.Models == nil || containsModel(readiness.Models, spec.ID) {
				models = append(models, spec)
			}
		}
		available = append(available, &LibraryGroup{Key: heading.Key, Engine: heading.Engine, Label: heading.Label, First: heading.First, Models: models, Selectable: readiness.Access == EngineAccessReady, Hint: hintOf(heading, readiness)})
	}
	shown := []*LibraryGroup{}
	for _, group := range available {
		if !(search != "" || filter == "all" || filter == "favorites" || filter == group.Key) {
			continue
		}
		models := []*ModelSpec{}
		for _, spec := range group.Models {
			keep := filter != "favorites" || pinned(group, spec)
			if search != "" {
				keep = strings.Contains(jsLower(group.Label+" "+spec.Label+" "+spec.ID+" "+spec.Description), search)
			}
			if keep {
				models = append(models, spec)
			}
		}
		if len(models) == 0 && (search != "" || filter == "favorites") {
			continue
		}
		next := *group
		next.Models = models
		shown = append(shown, &next)
	}
	heading := "All models"
	if filter == "favorites" {
		heading = "Favorites"
	} else if filter != "all" {
		heading = filter
		for _, group := range available {
			if group.Key == filter {
				heading = group.Label
				break
			}
		}
	}
	if search != "" {
		heading = "Search results"
	}
	library := &ModelLibrary{Available: available, Shown: shown, Heading: heading}
	for _, group := range shown {
		library.Count += float64(len(group.Models))
	}
	for _, group := range available {
		library.Total += float64(len(group.Models))
		for _, spec := range group.Models {
			if pinned(group, spec) {
				library.FavoriteCount++
			}
		}
	}
	return library
}

func containsModel(models []AgentModel, model AgentModel) bool {
	for _, id := range models {
		if id == model {
			return true
		}
	}
	return false
}

// jsLower stands for toLocaleLowerCase in an en-US locale.
func jsLower(text string) string { return strings.ToLower(text) }

// jsLowerTrim is String.prototype.trim followed by toLocaleLowerCase.
func jsLowerTrim(text string) string { return jsLower(jsTrim(text)) }

// LibraryOpeningOptions is what libraryOpening takes. An empty Model is none.
type LibraryOpeningOptions struct {
	Engine    AgentEngine
	Locked    bool
	Model     AgentModel
	Favorites []*EngineModel
}

// LibraryOpening is where the library opens.
func LibraryOpening(catalog *ModelCatalog, options LibraryOpeningOptions) LibraryFilter {
	if options.Locked {
		var own []*libraryHeading
		for _, heading := range headingsOf(catalog) {
			if heading.Engine == options.Engine {
				own = append(own, heading)
			}
		}
		for _, heading := range own {
			for _, spec := range heading.Models {
				if options.Model != "" && spec.ID == options.Model {
					return heading.Key
				}
			}
		}
		if len(own) > 0 {
			return own[0].Key
		}
		return "all"
	}
	if len(options.Favorites) > 0 {
		return "favorites"
	}
	return "all"
}
