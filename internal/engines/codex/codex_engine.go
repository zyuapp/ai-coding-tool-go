package codex

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// Codex as the app knows it: its command, what it answers outside a run, and a provider per channel.
type Engine struct {
	connect CodexConnect
}

// pluginRoot is the app's bundled plugin folder, whose skills Codex reads as an extra root; "" for
// none. A nil connect starts the installed Codex.
func NewEngine(pluginRoot string, connect CodexConnect) *Engine {
	SetAppPluginRoot(pluginRoot)
	if connect == nil {
		connect = ConnectAppServer
	}
	return &Engine{connect: connect}
}

var (
	_ agent.Engine                  = (*Engine)(nil)
	_ agent.EngineServices          = (*Engine)(nil)
	_ agent.SubagentMetadataService = (*Engine)(nil)
	_ agent.SignInService           = (*Engine)(nil)
	_ agent.UpdateService           = (*Engine)(nil)
)

func (engine *Engine) Binary() agent.EngineCommand { return Binary }

func (engine *Engine) Services() agent.EngineServices { return engine }

func (engine *Engine) CreateProvider(_ context.Context, provider agent.ProviderContext) (agent.EngineProvider, error) {
	if provider.PluginRoot != "" {
		SetAppPluginRoot(provider.PluginRoot)
	}
	generated := provider.GeneratedImages
	return NewCodexAgentProvider(CodexProviderOptions{
		Connect: engine.connect,
		Host:    provider.Host,
		Pool:    provider.Pool,
		ImageOutput: func(_ context.Context, item protocol.ThreadItem, root string) (string, error) {
			return CodexImageOutput(item, root, generated)
		},
	}), nil
}

func (engine *Engine) Commands(ctx context.Context, workspace agent.Workspace) ([]*contracts.AvailableCommand, error) {
	skills, err := DiscoverCodexCommands(ctx, workspace.WorkspaceRoot, engine.connect, 0)
	if err != nil {
		return nil, err
	}
	goal := &contracts.AvailableCommand{Name: "goal", Description: "Set a goal — keep working until the condition is met", ArgumentHint: "condition"}
	return append([]*contracts.AvailableCommand{goal}, skills...), nil
}

func (engine *Engine) SuggestTitle(ctx context.Context, text string, images []string, _ *domain.AgentModel) (*string, error) {
	return SuggestCodexTitle(ctx, text, images, nil)
}

func (engine *Engine) PlanUsage(ctx context.Context) (domain.PlanUsage, error) {
	return ReadCodexPlanUsage(ctx, engine.connect, 0), nil
}

func (engine *Engine) Readiness(ctx context.Context) (domain.EngineReadiness, error) {
	return ReadCodexReadiness(ctx, engine.connect), nil
}

func (engine *Engine) Update(ctx context.Context) error {
	return agent.UpgradeEngine(ctx, Binary)
}

func (engine *Engine) SignIn(ctx context.Context, openURL agent.OpenURL) (domain.EngineAccess, error) {
	return SignInToCodex(ctx, openURL, engine.connect)
}

func (engine *Engine) SubagentMetadata(ctx context.Context, id string, _ *string) (domain.SubagentMetadata, error) {
	return ReadCodexSubagentMetadata(ctx, id, engine.connect, 0), nil
}
