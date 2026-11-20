package domain

// NormalizeDraft holds a draft to its engine. An empty draft.Model is none named; an empty role is none.
func NormalizeDraft(catalog *ModelCatalog, draft AgentDraft, role CoordinationRole) AgentDraft {
	named, ok := draft.Model, draft.Model != ""
	if !ok {
		named, ok = DefaultModelIn(catalog, draft.Engine)
	}
	engine, model := draft.Engine, named
	if !ok {
		engine = DefaultEngine
		if model, ok = DefaultModelIn(catalog, DefaultEngine); !ok {
			model = DefaultModel
		}
	}
	return AgentDraft{Engine: engine, Model: model, Effort: EffortForModel(catalog, engine, model, draft.Effort, role), Policy: PolicyFor(engine, draft.Policy), FastMode: draft.FastMode}
}
