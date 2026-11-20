package opencode

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// A model as the server lists it; only what the picker and a run read.
type Model struct {
	ID         string          `json:"id"`
	ProviderID string          `json:"providerID"`
	Name       *string         `json:"name,omitzero"`
	Enabled    *bool           `json:"enabled,omitzero"`
	Variants   []*ModelVariant `json:"variants,omitzero"`
	Limit      *ModelLimit     `json:"limit,omitzero"`
}

type ModelVariant struct {
	ID string `json:"id"`
}

type ModelLimit struct {
	Context *float64 `json:"context,omitzero"`
}

type ModelProvider struct {
	ID   string  `json:"id"`
	Name *string `json:"name,omitzero"`
}

// What a picker model id names on the server: its provider, the provider's own id for it, and a depth to reason at.
type ModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitzero"`
}

// An app model id is the provider and the provider's id joined, as OpenCode writes it: `openrouter/z-ai/glm-5.3-flash`.
func AppModelID(providerID, id string) domain.AgentModel {
	return providerID + "/" + id
}

// The server's reference for an app model id, at the effort a run asked for when the model reasons
// at more than one depth (variant "" for none); nil when the id names no provider.
func ModelRefOf(model domain.AgentModel, variant string) *ModelRef {
	slash := strings.Index(model, "/")
	if slash <= 0 || slash == len(model)-1 {
		return nil
	}
	return &ModelRef{ProviderID: model[:slash], ID: model[slash+1:], Variant: variant}
}

// The models the server offers, as the picker lists them: each connected provider's models, the
// server's default first. A model reasons at the depths its variants name that the app also names;
// a variant only OpenCode knows is left out.
func Catalog(models []*Model, providers []*ModelProvider, defaultModel *Model) []*domain.ModelSpec {
	names := map[string]string{}
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		if provider.Name != nil {
			names[provider.ID] = *provider.Name
		} else {
			names[provider.ID] = provider.ID
		}
	}
	specs := []*domain.ModelSpec{}
	for _, model := range models {
		if model == nil {
			continue
		}
		id := AppModelID(model.ProviderID, model.ID)
		if (model.Enabled != nil && !*model.Enabled) || !domain.IsModelID(id) {
			continue
		}
		provider, ok := names[model.ProviderID]
		if !ok {
			provider = model.ProviderID
		}
		var offered []domain.AgentEffort
		for _, variant := range model.Variants {
			if variant != nil && domain.IsAgentEffort(variant.ID) {
				offered = append(offered, domain.AgentEffort(variant.ID))
			}
		}
		label := model.ID
		if model.Name != nil {
			label = *model.Name
		}
		window := 0.0
		if model.Limit != nil && model.Limit.Context != nil {
			window = *model.Limit.Context
		}
		specs = append(specs, &domain.ModelSpec{
			ID:               id,
			Label:            label,
			Description:      model.ID,
			Provider:         &provider,
			ContextWindow:    window,
			Efforts:          domain.AgentEngineEfforts(offered...),
			ManualCompaction: new(true),
		})
	}
	if defaultModel == nil {
		return specs
	}
	first := AppModelID(defaultModel.ProviderID, defaultModel.ID)
	for index, spec := range specs {
		if spec.ID != first {
			continue
		}
		if index == 0 {
			break
		}
		ordered := make([]*domain.ModelSpec, 0, len(specs))
		ordered = append(ordered, spec)
		ordered = append(ordered, specs[:index]...)
		return append(ordered, specs[index+1:]...)
	}
	return specs
}
