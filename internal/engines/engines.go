// Package engines is the registry of agent engines: the one place that reaches into each engine's
// package. Ports src/main/engines.mts.
package engines

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/opencode"
)

// Every engine the app runs, built once for the app's lifetime.
type Registry struct {
	Modules map[domain.AgentEngine]agent.Engine
}

// pluginRoot is the app's bundled plugin folder, empty for none.
func New(pluginRoot string) *Registry {
	return &Registry{Modules: map[domain.AgentEngine]agent.Engine{
		domain.AgentEngineClaude:   claude.NewEngine(pluginRoot, nil),
		domain.AgentEngineCodex:    codex.NewEngine(pluginRoot, nil),
		domain.AgentEngineOpencode: opencode.NewEngine(pluginRoot),
	}}
}

// What each engine answers outside a run.
func (registry *Registry) Services() map[domain.AgentEngine]agent.EngineServices {
	services := make(map[domain.AgentEngine]agent.EngineServices, len(registry.Modules))
	for engine, module := range registry.Modules {
		services[engine] = module.Services()
	}
	return services
}

// Stops whatever an engine keeps running to answer outside a run.
func (registry *Registry) CloseServices(ctx context.Context) error {
	return agent.CloseEngineServices(ctx, registry.Services())
}

// The in-process agent worker running these engines; config.Engines is filled in.
func (registry *Registry) Agent(config agent.WorkerConfig) agent.StartAgentProcess {
	config.Engines = registry.Modules
	return agent.InProcessAgent(config)
}

// The EnginePort runtime over these engines.
func (registry *Registry) EnginePort(openURL agent.OpenURL, savedAttachmentPath func(string) (string, bool)) *agent.EnginePort {
	return &agent.EnginePort{Services: registry.Services(), OpenURL: openURL, SavedAttachmentPath: savedAttachmentPath}
}
