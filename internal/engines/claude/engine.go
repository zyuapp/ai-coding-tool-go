package claude

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// Engine is everything the app knows of Claude: its command, what it answers outside a run, and the
// provider that runs its turns.
type Engine struct {
	queries    QueryFactory
	pluginRoot string
}

// NewEngine is the Claude engine. pluginRoot is the app's own plugin folder, empty for none; a nil
// queries starts the installed Claude Code.
func NewEngine(pluginRoot string, queries QueryFactory) *Engine {
	if queries == nil {
		queries = StartQuery
	}
	return &Engine{queries: queries, pluginRoot: pluginRoot}
}

var (
	_ agent.Engine                  = (*Engine)(nil)
	_ agent.EngineServices          = (*Engine)(nil)
	_ agent.SubagentMetadataService = (*Engine)(nil)
	_ agent.UpdateService           = (*Engine)(nil)
	_ agent.EngineProvider          = (*Provider)(nil)
)

func (engine *Engine) Binary() agent.EngineCommand { return claudeBinary }

func (engine *Engine) Services() agent.EngineServices { return engine }

func (engine *Engine) CreateProvider(_ context.Context, provider agent.ProviderContext) (agent.EngineProvider, error) {
	root := provider.PluginRoot
	if root == "" {
		root = engine.pluginRoot
	}
	return NewProvider(provider.Pool, engine.queries, root), nil
}

func (engine *Engine) Commands(ctx context.Context, workspace agent.Workspace) ([]*contracts.AvailableCommand, error) {
	return discoverClaudeCommands(ctx, engine.queries, engine.pluginRoot, workspace.WorkspaceRoot, workspace.Projectless)
}

func (engine *Engine) SuggestTitle(ctx context.Context, text string, images []string, _ *domain.AgentModel) (*string, error) {
	return suggestTaskTitle(ctx, engine.queries, text, images), nil
}

func (engine *Engine) PlanUsage(ctx context.Context) (domain.PlanUsage, error) {
	return readPlanUsage(ctx, engine.queries, planUsageTimeout), nil
}

func (engine *Engine) Readiness(ctx context.Context) (domain.EngineReadiness, error) {
	return readClaudeReadiness(ctx, engine.queries), nil
}

func (engine *Engine) SubagentMetadata(_ context.Context, id string, sessionID *string) (domain.SubagentMetadata, error) {
	return readClaudeSubagentMetadata(id, sessionID, claudeConfigDir()), nil
}

func (engine *Engine) Update(ctx context.Context) error {
	return agent.UpgradeEngine(ctx, claudeBinary)
}
