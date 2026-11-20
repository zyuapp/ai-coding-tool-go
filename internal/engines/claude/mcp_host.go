package claude

import (
	"context"
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// claudeMcpServer serves the app's tools to Claude as an in-process MCP server.
func claudeMcpServer(name string, tools []agent.BoundTool) *protocol.SdkMcpServer {
	served := make([]protocol.SdkMcpTool, 0, len(tools))
	for _, bound := range tools {
		handler := bound.Handler
		served = append(served, protocol.SdkMcpTool{
			Name:        bound.Name,
			Description: bound.Description,
			InputSchema: bound.Input,
			Handler: func(ctx context.Context, arguments jsontext.Value) (jsontext.Value, error) {
				result, err := handler(ctx, arguments)
				if err != nil {
					return nil, err
				}
				encoded, err := jsonx.Marshal(result)
				return jsontext.Value(encoded), err
			},
		})
	}
	return &protocol.SdkMcpServer{Name: name, Version: "1.0.0", AlwaysLoad: true, Tools: served}
}
