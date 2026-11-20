package protocol

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestSmokeRealClaude drives the installed Claude Code. CLAUDE_SMOKE=1 runs the handshake;
// CLAUDE_SMOKE_TURN=1 also asks Haiku one question through an in-process MCP tool.
func TestSmokeRealClaude(t *testing.T) {
	if os.Getenv("CLAUDE_SMOKE") == "" {
		t.Skip("set CLAUDE_SMOKE=1 to run against the installed claude")
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	called := make(chan string, 1)
	opts := Options{
		Executable: executable, Cwd: t.TempDir(), SettingSources: []string{}, Tools: []string{},
		Env:   map[string]string{"CLAUDE_CODE_ENTRYPOINT": "aicodingtool"},
		Model: "haiku", PermissionMode: PermissionModeDefault, IncludePartialMessages: true,
		SdkMcpServers: []*SdkMcpServer{{Name: "smoke", AlwaysLoad: true, Tools: []SdkMcpTool{{
			Name: "secret_word", Description: "Returns the secret word.",
			InputSchema: jsontext.Value(`{"type":"object","properties":{}}`),
			Handler: func(context.Context, jsontext.Value) (jsontext.Value, error) {
				called <- "called"
				return jsontext.Value(`{"content":[{"type":"text","text":"pomegranate"}]}`), nil
			},
		}}}},
		CanUseTool: func(_ context.Context, request PermissionRequest) (*PermissionResult, error) {
			return Allow(request.Input), nil
		},
	}
	q, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	init, err := q.Initialization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initialize: %d commands, %d models, version %v", len(init.Commands), len(init.Models), deref(init.ClaudeCodeVersion))
	if len(init.Models) == 0 {
		t.Fatal("no models")
	}
	if os.Getenv("CLAUDE_SMOKE_TURN") == "" {
		return
	}
	if err := q.SendUser(NewUserMessage(TextContent("Call the mcp__smoke__secret_word tool and answer with the word it returns, nothing else."))); err != nil {
		t.Fatal(err)
	}
	for message := range q.Messages() {
		t.Logf("%s", message.Kind())
		if result, ok := message.(*ResultMessage); ok {
			t.Logf("result: %s %v", result.Subtype, deref(result.Result))
			break
		}
	}
	select {
	case <-called:
	default:
		t.Error("the in-process tool was not called")
	}
}

func deref[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
