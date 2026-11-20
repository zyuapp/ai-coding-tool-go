package codex

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// The name Codex files the app's own tools under.
const AppServerName = "aicodingtool"

// Where the app server finds the bearer token for the app's tool service.
const ToolTokenEnv = "AICODINGTOOL_MCP_TOKEN"

const ComputerUseServerName = "cua-driver"

// These bundled plugins target Codex's desktop surfaces. User plugins retain their own settings.
var appSurfacePlugins = []string{"browser", "chrome", "computer-use", "unified-computer-use", "codex-app-tools"}

func appSurfacePluginsOff() TomlTable {
	plugins := TomlTable{}
	for _, name := range appSurfacePlugins {
		plugins = append(plugins, TomlEntry{name + "@openai-bundled", TomlTable{{"enabled", false}}})
	}
	return plugins
}

var AppFeatures = []string{"--enable", "fast_mode", "--enable", "goals", "-c", "plugins=" + Toml(appSurfacePluginsOff())}

// Codex asks the user in chat only outside a coordinator. Under one, a coordinator and its threads put
// a question to the user with raise_decision, which the coordinator's Overview and Needs you show, as
// Claude there must. It is disabled there, not just left off, so the user's own config cannot turn it on.
func questionFeatures(coordinated bool) []string {
	if coordinated {
		return []string{"--disable", "default_mode_request_user_input"}
	}
	return []string{"--enable", "default_mode_request_user_input"}
}

// A coordinator hands work only to threads the user can see, so it runs without Codex's own
// subagents, as it does without Claude's.
var coordinatorWithheldFeatures = []string{"--disable", "multi_agent", "--disable", "multi_agent_v2"}

// What of a run its app server's config is read from.
type ConfigSources struct {
	Channel          contracts.RunChannel
	Policy           domain.ExecutionPolicy
	ComputerUse      domain.ComputerUseRunConfig
	CoordinationRole *domain.CoordinationRole
}

func configSources(input *agent.ProviderRunInput) ConfigSources {
	return ConfigSources{Channel: input.Channel, Policy: input.Policy, ComputerUse: input.ComputerUse, CoordinationRole: input.CoordinationRole}
}

func (sources ConfigSources) scope() *agent.ApprovalScope {
	return &agent.ApprovalScope{Policy: sources.Policy, Channel: sources.Channel}
}

// A table that keeps the place a key was first set at, as a JavaScript object does.
type orderedConfig struct{ entries TomlTable }

func (config *orderedConfig) set(key string, value any) {
	for index := range config.entries {
		if config.entries[index].Key == key {
			config.entries[index].Value = value
			return
		}
	}
	config.entries = append(config.entries, TomlEntry{key, value})
}

// The config overrides a Codex app server is spawned with. The app's own tools are served flat
// under one server and granted tool by tool as Claude grants them; the rest prompt. Bundled
// computer use prompts like any other MCP server, except where the run's policy grants it unasked.
func CodexConfig(input ConfigSources, served *agent.ServedTools, sets []agent.ServedToolSet) []string {
	config := orderedConfig{}
	if served != nil {
		config.set("mcp_servers."+AppServerName+".url", served.URL)
		config.set("mcp_servers."+AppServerName+".bearer_token_env_var", ToolTokenEnv)
		unasked := agent.GrantsTool(agent.ReachWorkspace, input.scope())
		config.set("mcp_servers."+AppServerName+".default_tools_approval_mode", approvalMode(unasked))
		// Every tool is spelled out, so an approval the user's own config holds for the same name cannot outlast the policy.
		for _, set := range sets {
			for _, tool := range set.Tools {
				granted := unasked || agent.GrantsTool(agent.ToolReachOf(agent.McpToolName(set.Server, tool.Name)), nil)
				config.set("mcp_servers."+AppServerName+".tools."+tool.Name+".approval_mode", approvalMode(granted))
			}
		}
	}
	if available, ok := input.ComputerUse.(domain.ComputerUseRunConfigAvailable); ok {
		args := available.MCP.Args
		if args == nil {
			args = []string{}
		}
		env := available.MCP.Env
		if env == nil {
			env = map[string]string{}
		}
		config.set("mcp_servers."+ComputerUseServerName+".command", available.MCP.Command)
		config.set("mcp_servers."+ComputerUseServerName+".args", args)
		config.set("mcp_servers."+ComputerUseServerName+".env", env)
		if agent.GrantsTool(agent.ReachComputerUse, input.scope()) {
			config.set("mcp_servers."+ComputerUseServerName+".default_tools_approval_mode", "approve")
		}
	}
	overrides := append([]string(nil), AppFeatures...)
	overrides = append(overrides, questionFeatures(input.CoordinationRole != nil)...)
	if input.CoordinationRole != nil && *input.CoordinationRole == domain.CoordinationRoleCoordinator {
		overrides = append(overrides, coordinatorWithheldFeatures...)
	}
	for _, entry := range config.entries {
		overrides = append(overrides, "-c", entry.Key+"="+Toml(entry.Value))
	}
	return overrides
}

func approvalMode(granted bool) string {
	if granted {
		return "approve"
	}
	return "prompt"
}
