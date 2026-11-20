package agent

import (
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/tools"
)

// The tool-server shapes sessions are built from are internal/tools'. The servers themselves are
// installed with SetToolServers.

type (
	ToolResultContent  = tools.ToolResultContent
	ToolResult         = tools.ToolResult
	BoundTool          = tools.BoundTool
	ServedTools        = tools.ServedTools
	ToolHost           = tools.ToolHost
	ToolDefinitionInfo = tools.DefinitionInfo
	ToolGrants         = tools.Grants
	BriefDialect       = tools.BriefDialect
	BriefRun           = tools.BriefRun
	Offers             = tools.Offers
)

const (
	GrantsAll      = tools.GrantsAll
	GrantsReadOnly = tools.GrantsReadOnly
	GrantsNone     = tools.GrantsNone
)

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

// AppToolServers is every server internal/tools defines, each bound to the run it is served to.
func AppToolServers() []ToolServer {
	servers := tools.Servers()
	served := make([]ToolServer, 0, len(servers))
	for _, server := range servers {
		serve := server.Serve
		served = append(served, ToolServer{
			Name:        server.Name,
			Definitions: server.Definitions,
			Grants:      server.Grants,
			Brief:       server.Brief,
			Serve: func(input *ProviderRunInput) ([]BoundTool, bool) {
				return serve(&tools.Run{
					ComputerUse:       input.ComputerUse,
					EmitSetupRequired: func() { input.Emit(EventComputerUseSetupRequired{}) },
					Automations:       input.Automations,
					Findings:          input.Findings,
					Threads:           input.Threads,
					Operation:         input.Operation,
					CoordinationRole:  input.CoordinationRole,
					Coordination:      input.Coordination,
					Browser:           input.Browser,
					Simulator:         input.Simulator,
					Terminal:          input.Terminal,
				})
			},
		})
	}
	return served
}
