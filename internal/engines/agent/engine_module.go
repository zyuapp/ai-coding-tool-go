package agent

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The command an engine is installed as, the npm package it is published as, and how to install it.
type EngineCommand struct {
	Command     string
	PackageName string
	Install     string
	IsNative    func(realPath string) bool
}

// What every engine's provider is built with.
type ProviderContext struct {
	// Serves the app's tools to every session of the process.
	Host ToolHost
	// The sessions a channel keeps warm, shared by its engines.
	Pool *SessionPool
	// Where images an engine generates are saved.
	GeneratedImages string
	// The app's bundled plugin folder, empty when there is none.
	PluginRoot string
}

// Everything the app knows of one engine. Each engine package (claude, codex, opencode) exports one.
type Engine interface {
	Binary() EngineCommand
	Services() EngineServices
	// One provider per channel; called once per channel at startup.
	CreateProvider(ctx context.Context, context ProviderContext) (EngineProvider, error)
}

// A provider as the router holds it.
type EngineProvider interface {
	AgentProvider
	CloseAll()
}

type Workspace struct {
	WorkspaceRoot string
	Projectless   bool
}

type OpenURL func(ctx context.Context, url string) error

// What the app asks of an engine outside a run. Optional parts are the *Service interfaces below.
type EngineServices interface {
	// The commands the engine offers a composer in the workspace.
	Commands(ctx context.Context, workspace Workspace) ([]*contracts.AvailableCommand, error)
	// Names a thread from its first message and the screenshots sent with it; model is the one the thread runs on.
	SuggestTitle(ctx context.Context, text string, images []string, model *domain.AgentModel) (*string, error)
	// Reads the plan behind this engine's account without starting a thread.
	PlanUsage(ctx context.Context) (domain.PlanUsage, error)
	// Whether this engine can take a run on this machine, and what would fix it when it cannot.
	Readiness(ctx context.Context) (domain.EngineReadiness, error)
}

type SubagentMetadataService interface {
	SubagentMetadata(ctx context.Context, id string, sessionID *string) (domain.SubagentMetadata, error)
}

type SignInService interface {
	SignIn(ctx context.Context, openURL OpenURL) (domain.EngineAccess, error)
}

// Upgrades the installed command the way the user installed it.
type UpdateService interface {
	Update(ctx context.Context) error
}

// Stops anything the engine started to answer outside a run, as the app quits.
type CloseService interface {
	Close(ctx context.Context) error
}
