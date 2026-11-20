package opencode

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// OpenCode as the app knows it: its command, what it answers outside a run, and a provider per channel.
type Engine struct {
	services *Services
}

// pluginRoot is the app's bundled plugin folder, whose skills OpenCode lists and loads; "" for none.
func NewEngine(pluginRoot string) *Engine {
	return &Engine{services: NewServices(nil, pluginRoot)}
}

func (e *Engine) Binary() agent.EngineCommand { return Binary }

func (e *Engine) Services() agent.EngineServices { return engineServices{e.services} }

func (e *Engine) CreateProvider(ctx context.Context, provider agent.ProviderContext) (agent.EngineProvider, error) {
	pluginRoot := provider.PluginRoot
	if pluginRoot == "" {
		pluginRoot = e.services.pluginRoot
	}
	return NewProvider(ProviderOptions{Host: provider.Host, Pool: provider.Pool, PluginRoot: pluginRoot}), nil
}

// Also an agent.UpdateService and an agent.CloseService.
type engineServices struct{ services *Services }

func (e engineServices) Commands(ctx context.Context, workspace agent.Workspace) ([]*contracts.AvailableCommand, error) {
	return e.services.Commands(ctx, workspace.WorkspaceRoot)
}

func (e engineServices) SuggestTitle(ctx context.Context, text string, images []string, model *domain.AgentModel) (*string, error) {
	return e.services.Title(ctx, text, model), nil
}

func (e engineServices) PlanUsage(ctx context.Context) (domain.PlanUsage, error) {
	return e.services.PlanUsage(ctx)
}

func (e engineServices) Readiness(ctx context.Context) (domain.EngineReadiness, error) {
	return e.services.Readiness(ctx)
}

func (e engineServices) Update(ctx context.Context) error {
	return agent.UpgradeEngine(ctx, Binary)
}

// Never starts a server.
func (e engineServices) Close(ctx context.Context) error {
	e.services.Close()
	return nil
}

var (
	_ agent.Engine         = (*Engine)(nil)
	_ agent.UpdateService  = engineServices{}
	_ agent.CloseService   = engineServices{}
	_ agent.EngineProvider = (*Provider)(nil)
	_ agent.PooledSession  = (*Session)(nil)
)
