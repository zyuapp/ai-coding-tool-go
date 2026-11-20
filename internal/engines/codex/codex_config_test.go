package codex

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

func TestConfigValuesAreWrittenAsTomlTheAppServerParses(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{"plain", `"plain"`},
		{"a\"b\\c\nd\te\x01\x7f", `"a\"b\\c\nd\te\u0001\u007f"`},
		{true, "true"},
		{[]string{"x", "y z"}, `["x", "y z"]`},
		{TomlTable{}, "{}"},
		{TomlTable{{"A-B", "1"}, {"C", `"`}}, `{ "A-B" = "1", "C" = "\"" }`},
		{[]any{TomlTable{{"name", "a:b"}, {"enabled", false}}}, `[{ "name" = "a:b", "enabled" = false }]`},
		{map[string]string{"B": "2", "A": "1"}, `{ "A" = "1", "B" = "2" }`},
		{float64(300), "300"},
	}
	for _, c := range cases {
		if got := Toml(c.value); got != c.want {
			t.Errorf("Toml(%#v) = %s, want %s", c.value, got, c.want)
		}
	}
	got := CodexConfig(ConfigSources{Channel: contracts.RunChannelMain, Policy: domain.ExecutionPolicyConfirm, ComputerUse: domain.ComputerUseRunConfigUnavailable{Message: "off"}}, nil, nil)
	want := []string{"--enable", "fast_mode", "--enable", "goals",
		"-c", `plugins={ "browser@openai-bundled" = { "enabled" = false }, "chrome@openai-bundled" = { "enabled" = false }, "computer-use@openai-bundled" = { "enabled" = false }, "unified-computer-use@openai-bundled" = { "enabled" = false }, "codex-app-tools@openai-bundled" = { "enabled" = false } }`,
		"--enable", "default_mode_request_user_input"}
	assertJSON(t, got, want, "only plugins that target Codex's own desktop surfaces are overridden")
}

// The `-c key=value` overrides a spawn carried, by key.
func overrides(args []string) map[string]string {
	found := map[string]string{}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "-c" {
			continue
		}
		key, value, _ := strings.Cut(args[index+1], "=")
		found[key] = value
	}
	return found
}

func TestCoordinatorsAskInChatNeverAndRunWithoutNativeSubagents(t *testing.T) {
	coordinator, member := domain.CoordinationRoleCoordinator, domain.CoordinationRoleMember
	base := ConfigSources{Channel: contracts.RunChannelMain, Policy: domain.ExecutionPolicyConfirm, ComputerUse: domain.ComputerUseRunConfigUnavailable{}}
	withRole := func(role *domain.CoordinationRole) []string {
		sources := base
		sources.CoordinationRole = role
		return CodexConfig(sources, nil, nil)
	}
	tail := func(args []string, count int) []string { return args[len(args)-count:] }
	assertJSON(t, tail(withRole(&coordinator), 6), []string{"--disable", "default_mode_request_user_input", "--disable", "multi_agent", "--disable", "multi_agent_v2"})
	assertJSON(t, tail(withRole(&member), 2), []string{"--disable", "default_mode_request_user_input"})
}

func TestBundledComputerUseIsItsOwnServerAndPreApprovedOnlyWhereThePolicyAllows(t *testing.T) {
	available := domain.ComputerUseRunConfigAvailable{MCP: domain.ComputerUseMCP{Command: "/app/cua-driver", Args: []string{"mcp", "--embedded"}, Env: map[string]string{"CUA_DRIVER_EMBEDDED": "1"}}}
	config := func(channel contracts.RunChannel, policy domain.ExecutionPolicy) map[string]string {
		return overrides(CodexConfig(ConfigSources{Channel: channel, Policy: policy, ComputerUse: available}, nil, nil))
	}
	side := config(contracts.RunChannelSide, domain.ExecutionPolicyAutonomous)
	for key, want := range map[string]string{
		"mcp_servers.cua-driver.command": `"/app/cua-driver"`,
		"mcp_servers.cua-driver.args":    `["mcp", "--embedded"]`,
		"mcp_servers.cua-driver.env":     `{ "CUA_DRIVER_EMBEDDED" = "1" }`,
	} {
		if side[key] != want {
			t.Errorf("%s = %s, want %s", key, side[key], want)
		}
	}
	if _, ok := side["mcp_servers.cua-driver.default_tools_approval_mode"]; ok {
		t.Error("a side chat's computer use still prompts")
	}
	if _, ok := config(contracts.RunChannelMain, domain.ExecutionPolicyConfirm)["mcp_servers.cua-driver.default_tools_approval_mode"]; ok {
		t.Error("a confirm run's computer use prompts")
	}
	if got := config(contracts.RunChannelMain, domain.ExecutionPolicyAutonomous)["mcp_servers.cua-driver.default_tools_approval_mode"]; got != `"approve"` {
		t.Errorf("autonomous main run = %s", got)
	}
	if got := config(contracts.RunChannelSide, domain.ExecutionPolicyBypass)["mcp_servers.cua-driver.default_tools_approval_mode"]; got != `"approve"` {
		t.Errorf("bypass = %s", got)
	}
}

func TestAppToolsAreGrantedUnaskedExactlyWhereClaudeGrantsThem(t *testing.T) {
	restore := installFakeToolServers(t)
	defer restore()
	sets := []agent.ServedToolSet{{Server: agent.ThreadServerName, Tools: []agent.BoundTool{{Name: "list_threads"}, {Name: "start_thread"}}}}
	served := &agent.ServedTools{URL: fakeHostURL, Token: "t"}
	for _, policy := range []domain.ExecutionPolicy{domain.ExecutionPolicyConfirm, domain.ExecutionPolicyPlan, domain.ExecutionPolicyAllowEdits, domain.ExecutionPolicyAutonomous, domain.ExecutionPolicyBypass} {
		found := overrides(CodexConfig(ConfigSources{Channel: contracts.RunChannelMain, Policy: policy, ComputerUse: domain.ComputerUseRunConfigUnavailable{}}, served, sets))
		if found["mcp_servers.aicodingtool.url"] != `"`+fakeHostURL+`"` || found["mcp_servers.aicodingtool.bearer_token_env_var"] != `"AICODINGTOOL_MCP_TOKEN"` {
			t.Fatalf("%s: the app server is not pointed at the tools: %v", policy, found)
		}
		for _, set := range sets {
			for _, tool := range set.Tools {
				granted := agent.GrantsTool(agent.ToolReachOf(agent.McpToolName(set.Server, tool.Name)), &agent.ApprovalScope{Policy: policy, Channel: contracts.RunChannelMain})
				want := `"prompt"`
				if granted {
					want = `"approve"`
				}
				if got := found["mcp_servers.aicodingtool.tools."+tool.Name+".approval_mode"]; got != want {
					t.Errorf("%s · %s = %s, want %s", tool.Name, policy, got, want)
				}
			}
		}
	}
	confirmed := overrides(CodexConfig(ConfigSources{Channel: contracts.RunChannelMain, Policy: domain.ExecutionPolicyConfirm, ComputerUse: domain.ComputerUseRunConfigUnavailable{}}, served, sets))
	if confirmed["mcp_servers.aicodingtool.tools.start_thread.approval_mode"] != `"prompt"` || confirmed["mcp_servers.aicodingtool.tools.list_threads.approval_mode"] != `"approve"` {
		t.Errorf("under confirm a read runs unasked and a change asks: %v", confirmed)
	}
	if confirmed["mcp_servers.aicodingtool.default_tools_approval_mode"] != `"prompt"` {
		t.Error("tools the run is not given still prompt")
	}
}
