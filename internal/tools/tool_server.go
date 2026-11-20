package tools

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// BriefDialect is the few words an engine needs said its own way.
type BriefDialect struct {
	// The engine's own sessions and helpers, which an app thread ID never names.
	NativeWork string
	// The engine's own way to make a worktree, which the app would never hear about.
	NativeWorktree *string
	// How a coordinator reads on this engine, said so it is told only of tools it really has.
	CoordinatorReads string
	// How a member in a checkout of its own commits there, for an engine whose sandbox keeps it from
	// doing so as it does everything else.
	OwnCheckoutCommits *string
}

// BriefRun is what a server's brief may read of the run besides what it was offered.
type BriefRun struct {
	CoordinationRole *domain.CoordinationRole
	WorkspaceRoot    string
	ProjectRoot      *string
}

// Offers is whether the run is offered the server at all (tool ""), or that tool of it.
type Offers func(tool string) bool

// Grants is which of a server's tools run without approval: every tool filed under it, only its
// read-only ones, or none.
type Grants string

const (
	GrantsAll      Grants = "all"
	GrantsReadOnly Grants = "read-only"
	GrantsNone     Grants = "none"
)

// DefinitionInfo is what a grant reads of a definition.
type DefinitionInfo struct {
	Name     string
	ReadOnly bool
}

func infos[Bridge any](definitions ...[]Definition[Bridge]) []DefinitionInfo {
	var out []DefinitionInfo
	for _, list := range definitions {
		for _, definition := range list {
			out = append(out, DefinitionInfo{Name: definition.Name, ReadOnly: definition.ReadOnly})
		}
	}
	return out
}

// Run is everything of a run that any server binds to (the TypeScript ToolServerRun). A nil bridge
// means the run is not offered what it serves.
type Run struct {
	ComputerUse domain.ComputerUseRunConfig
	// Says computer use needs setting up: the provider event "computer-use.setup-required".
	EmitSetupRequired func()
	Automations       AutomationBridge
	// Only alongside Automations.
	Findings         FindingBridge
	Threads          ThreadBridge
	Operation        contracts.RunOperation
	CoordinationRole *domain.CoordinationRole
	Coordination     CoordinationBridge
	Browser          BrowserBridge
	Simulator        SimulatorBridge
	Terminal         TerminalBridge
}

func (run *Run) role() domain.CoordinationRole {
	if run.CoordinationRole == nil {
		return ""
	}
	return *run.CoordinationRole
}

func (run *Run) reviewing() bool {
	_, review := run.Operation.(contracts.RunOperationReview)
	return review
}

// Server is one of the app's MCP servers, described once so every engine serves, grants and briefs it alike.
type Server struct {
	Name string
	// Every tool the server can offer, whichever of them a run is given.
	Definitions []DefinitionInfo
	Grants      Grants
	// The tools a run is given, before its channel withholds any; false when it is not offered the server.
	Serve func(run *Run) ([]BoundTool, bool)
	// What the run is told, read from what its channel left it. Nil for a server that says nothing.
	Brief func(offers Offers, dialect BriefDialect, run BriefRun) []string
}
