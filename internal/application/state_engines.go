package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialEnginesState() EnginesState {
	return EnginesState{
		EngineStatus:         nil,
		EngineChecking:       false,
		EngineUpdating:       nil,
		EngineUpdateQueue:    []domain.AgentEngine{},
		EngineUpdatesOffered: false,
		AgentSettingsReload:  EnginesStateAgentSettingsReloadIdle,
	}
}

func EngineReadinessOf(state *WorkspaceState, engine domain.AgentEngine) domain.EngineReadiness {
	if readiness := state.EngineStatus.Get(engine); readiness != nil {
		return *readiness
	}
	return domain.EngineReadiness{Access: domain.EngineAccessReady}
}

func CatalogOf(state *WorkspaceState) domain.ModelCatalog {
	return *domain.EngineCatalog(state.EngineStatus)
}

func EngineOffersModel(state *WorkspaceState, engine domain.AgentEngine, model domain.AgentModel) bool {
	return domain.EngineHasModel(domain.EngineCatalog(state.EngineStatus), engine, model) || !domain.EngineModelsKnown(state.EngineStatus, engine)
}
