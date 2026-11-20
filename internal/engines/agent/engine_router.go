package agent

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// One provider per engine, told apart by the run's engine. A thread's session lives with the engine it runs on.
type EngineRouter struct {
	engines map[domain.AgentEngine]EngineProvider
}

func NewEngineRouter(engines map[domain.AgentEngine]EngineProvider) *EngineRouter {
	return &EngineRouter{engines: engines}
}

// The providers in domain.AgentEngines order.
func (router *EngineRouter) each() []EngineProvider {
	providers := make([]EngineProvider, 0, len(router.engines))
	for _, engine := range domain.AgentEngines {
		if provider := router.engines[engine]; provider != nil {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (router *EngineRouter) Execute(input *ProviderRunInput) (ProviderResult, error) {
	provider := router.engines[input.Engine]
	if provider == nil {
		return ProviderResult{}, &OpenFailure{Message: domain.UnopenableEngine(string(input.Engine))}
	}
	return provider.Execute(input)
}

// A thread's live session sits in its channel's shared pool, where only the engine that opened it
// claims it, so a stop or a label asks each.
func (router *EngineRouter) StopProcess(taskID, processID string) bool {
	for _, provider := range router.each() {
		if provider.StopProcess(taskID, processID) {
			return true
		}
	}
	return false
}

func (router *EngineRouter) LabelThread(taskID, title string) bool {
	for _, provider := range router.each() {
		if provider.LabelThread(taskID, title) {
			return true
		}
	}
	return false
}

// Goes to the engine that made the continuation; a thread with none yet goes to whichever engine holds its session.
func (router *EngineRouter) ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool {
	if continuation == nil {
		for _, provider := range router.each() {
			if provider.ArchiveThread(taskID, nil, archived) {
				return true
			}
		}
		return false
	}
	if !domain.IsAgentEngine(continuation.Provider) {
		return false
	}
	provider := router.engines[domain.AgentEngine(continuation.Provider)]
	return provider != nil && provider.ArchiveThread(taskID, continuation, archived)
}

func (router *EngineRouter) RetireOutOfRole(taskID string, role *domain.CoordinationRole) {
	for _, provider := range router.each() {
		provider.RetireOutOfRole(taskID, role)
	}
}

func (router *EngineRouter) CloseAll() {
	for _, provider := range router.each() {
		provider.CloseAll()
	}
}
