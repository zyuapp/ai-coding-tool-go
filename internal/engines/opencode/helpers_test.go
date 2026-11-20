package opencode

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

func boundTool(name string) agent.BoundTool {
	return agent.BoundTool{
		Name: name, Description: name, Input: jsontext.Value(`{"type":"object","properties":{"value":{"type":"string"}}}`),
		Handler: func(context.Context, jsontext.Value) (agent.ToolResult, error) { return agent.ToolResult{}, nil },
	}
}

// The app's tool servers as far as these tests reach them: threads, offered to a run with a thread
// bridge and granted by its read-only tools, and coordination, granted whole.
func TestMain(m *testing.M) {
	agent.SetToolServers([]agent.ToolServer{
		{
			Name:        agent.ThreadServerName,
			Definitions: []agent.ToolDefinitionInfo{{Name: "list_threads", ReadOnly: true}, {Name: "start_thread"}},
			Grants:      agent.GrantsReadOnly,
			Serve: func(run *agent.ProviderRunInput) ([]agent.BoundTool, bool) {
				if run.Threads == nil {
					return nil, false
				}
				return []agent.BoundTool{boundTool("list_threads"), boundTool("start_thread")}, true
			},
		},
		{
			Name:        agent.CoordinationServerName,
			Definitions: []agent.ToolDefinitionInfo{{Name: "report_finding"}},
			Grants:      agent.GrantsAll,
		},
	})
	os.Exit(m.Run())
}
