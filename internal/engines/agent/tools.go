package agent

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The tool-server shapes of src/main/tools (tool-definition, tool-server, tool-names, mcp-http-host)
// that sessions are built from. The servers themselves are installed with SetToolServers.

type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Content []ToolResultContent `json:"content"`
	IsError bool                `json:"isError,omitzero"`
}

// A tool definition with its bridge supplied, which is all a host needs to serve it.
type BoundTool struct {
	Name        string
	Description string
	// JSON Schema of the input object.
	Input   jsontext.Value
	Handler func(ctx context.Context, args jsontext.Value) (ToolResult, error)
}

// What a served set is reachable as: the address, and the bearer token that names the set.
type ServedTools struct {
	URL   string
	Token string
	// Forgets the token and drops every connection made with it.
	Release func()
}

type ToolHost interface {
	Serve(ctx context.Context, tools []BoundTool) (ServedTools, error)
}

type ToolDefinitionInfo struct {
	Name     string
	ReadOnly bool
}

// Which of a server's tools run without approval.
type ToolGrants string

const (
	GrantsAll      ToolGrants = "all"
	GrantsReadOnly ToolGrants = "read-only"
	GrantsNone     ToolGrants = "none"
)

// The few words an engine needs said its own way.
type BriefDialect struct {
	// The engine's own sessions and helpers, which an app thread ID never names.
	NativeWork string
	// The engine's own way to make a worktree, which the app would never hear about.
	NativeWorktree *string
	// How a coordinator reads on this engine.
	CoordinatorReads string
	// How a member in a checkout of its own commits there, for an engine whose sandbox needs it said.
	OwnCheckoutCommits *string
}

// What a server's brief may read of the run besides what it was offered.
type BriefRun struct {
	CoordinationRole *domain.CoordinationRole
	WorkspaceRoot    string
	ProjectRoot      *string
}

// Whether the run is offered the server at all (tool ""), or that tool of it.
type Offers func(tool string) bool

// One of the app's MCP servers, described once so every engine serves, grants and briefs it alike.
type ToolServer struct {
	Name string
	// Every tool the server can offer, whichever of them a run is given.
	Definitions []ToolDefinitionInfo
	Grants      ToolGrants
	// The tools a run is given before its channel withholds any; false when it is not offered the server.
	Serve func(run *ProviderRunInput) ([]BoundTool, bool)
	// What the run is told, read from what its channel left it. May be nil.
	Brief func(offers Offers, dialect BriefDialect, run BriefRun) []string
}

var toolCatalog struct {
	sync.RWMutex
	servers            []ToolServer
	appServerPrefixes  []string
	readOnlyAppToolSet map[string]bool
}

// Installs every app tool server, in the order a run is offered them and told of them.
func SetToolServers(servers []ToolServer) {
	prefixes := []string{}
	readOnly := map[string]bool{}
	for _, server := range servers {
		switch server.Grants {
		case GrantsAll:
			prefixes = append(prefixes, McpToolName(server.Name, ""))
		case GrantsReadOnly:
			for name := range ReadOnlyToolNames(server.Name, server.Definitions) {
				readOnly[name] = true
			}
		}
	}
	toolCatalog.Lock()
	defer toolCatalog.Unlock()
	toolCatalog.servers = servers
	toolCatalog.appServerPrefixes = prefixes
	toolCatalog.readOnlyAppToolSet = readOnly
}

// The installed tool servers.
func ToolServers() []ToolServer {
	toolCatalog.RLock()
	defer toolCatalog.RUnlock()
	return toolCatalog.servers
}

// How every engine names a tool an MCP server offers, and what the app's grants are written against.
func McpToolName(server, name string) string {
	return "mcp__" + server + "__" + name
}

// The names of a server's read-only tools.
func ReadOnlyToolNames(server string, definitions []ToolDefinitionInfo) map[string]bool {
	names := map[string]bool{}
	for _, definition := range definitions {
		if definition.ReadOnly {
			names[McpToolName(server, definition.Name)] = true
		}
	}
	return names
}

func isAppTool(toolName string) bool {
	toolCatalog.RLock()
	defer toolCatalog.RUnlock()
	if toolCatalog.readOnlyAppToolSet[toolName] {
		return true
	}
	for _, prefix := range toolCatalog.appServerPrefixes {
		if strings.HasPrefix(toolName, prefix) {
			return true
		}
	}
	return false
}
