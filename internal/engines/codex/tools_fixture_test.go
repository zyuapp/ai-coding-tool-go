package codex

import (
	"context"
	"encoding/json/jsontext"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

// Installs one app tool server, with a read-only tool and a changing one, served to every main-channel
// run and to side chats without the changing one.
func installFakeToolServers(t *testing.T) func() {
	t.Helper()
	held := agent.ToolServers()
	handler := func(context.Context, jsontext.Value) (agent.ToolResult, error) {
		return agent.ToolResult{Content: []agent.ToolResultContent{{Type: "text", Text: "ok"}}}, nil
	}
	agent.SetToolServers([]agent.ToolServer{{
		Name:        agent.ThreadServerName,
		Definitions: []agent.ToolDefinitionInfo{{Name: "list_threads", ReadOnly: true}, {Name: "start_thread"}},
		Grants:      agent.GrantsReadOnly,
		Serve: func(run *agent.ProviderRunInput) ([]agent.BoundTool, bool) {
			return []agent.BoundTool{{Name: "list_threads", Handler: handler}, {Name: "start_thread", Handler: handler}}, true
		},
		Brief: func(offers agent.Offers, _ agent.BriefDialect, _ agent.BriefRun) []string {
			if offers("") {
				return []string{"Use the thread tools."}
			}
			return nil
		},
	}})
	return func() { agent.SetToolServers(held) }
}
