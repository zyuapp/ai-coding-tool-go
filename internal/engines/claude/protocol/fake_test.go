package protocol

import (
	"bufio"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// The test binary stands in for Claude Code when FAKE_CLAUDE is set: it speaks enough of the
// stream-json protocol to drive one scripted exchange per user message.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") != "" {
		runFakeClaude()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeLine struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   jsontext.Value `json:"request"`
	Response  jsontext.Value `json:"response"`
	Message   *MessageParam  `json:"message"`
}

func runFakeClaude() {
	out := bufio.NewWriter(os.Stdout)
	emit := func(line string) {
		out.WriteString(line + "\n")
		out.Flush()
	}
	lines := make(chan fakeLine, 16)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Buffer(make([]byte, 1<<20), 1<<24)
		for scanner.Scan() {
			var line fakeLine
			if unmarshal(scanner.Bytes(), &line) == nil {
				lines <- line
			}
		}
		close(lines)
	}()
	// awaitResponse reads until the control response to id, answering requests on the way.
	var answerRequest func(line fakeLine)
	awaitResponse := func(id string) jsontext.Value {
		for line := range lines {
			if line.Type == "control_response" {
				var body struct {
					RequestID string `json:"request_id"`
				}
				_ = unmarshal(line.Response, &body)
				if body.RequestID == id {
					return line.Response
				}
				continue
			}
			answerRequest(line)
		}
		os.Exit(9)
		return nil
	}
	echo := func(response jsontext.Value) {
		emit(fmt.Sprintf(`{"type":"fake_echo","response":%s}`, response))
	}
	answerRequest = func(line fakeLine) {
		if line.Type != "control_request" {
			return
		}
		var head subtypeOnly
		_ = unmarshal(line.Request, &head)
		switch head.Subtype {
		case "initialize":
			emit(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{"commands":[{"name":"review","description":"Review","argumentHint":""}],"agents":[],"output_style":"default","available_output_styles":[],"models":[{"value":"opus","displayName":"Opus","description":"d"}],"account":{"email":"a@b"}}}}`, line.RequestID))
			args, _ := marshal(os.Args[1:])
			env, _ := marshal(map[string]string{
				"CLAUDE_CODE_ENTRYPOINT":              os.Getenv("CLAUDE_CODE_ENTRYPOINT"),
				"CLAUDE_AGENT_SDK_VERSION":            os.Getenv("CLAUDE_AGENT_SDK_VERSION"),
				"CLAUDE_CODE_SDK_READS_SESSION_STATE": os.Getenv("CLAUDE_CODE_SDK_READS_SESSION_STATE"),
				"NODE_OPTIONS":                        os.Getenv("NODE_OPTIONS"),
				"PROBE":                               os.Getenv("PROBE"),
			})
			emit(fmt.Sprintf(`{"type":"fake_start","args":%s,"env":%s,"initialize":%s}`, args, env, line.Request))
			emit(`{"type":"system","subtype":"init","session_id":"session-1","uuid":"u0","apiKeySource":"none","claude_code_version":"9","cwd":"/","tools":[],"mcp_servers":[],"model":"opus","permissionMode":"default","slash_commands":[],"output_style":"default","skills":[],"plugins":[]}`)
		case "interrupt":
			emit(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q,"response":{}}}`, line.RequestID))
		case "get_usage":
			emit(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"error","request_id":%q,"error":"usage is off","error_code":"usage_off"}}`, line.RequestID))
		case "set_model":
			echo(line.Request)
			emit(fmt.Sprintf(`{"type":"control_response","response":{"subtype":"success","request_id":%q}}`, line.RequestID))
		}
	}
	for line := range lines {
		if line.Type != "user" {
			answerRequest(line)
			continue
		}
		switch line.Message.Content.Text {
		case "permission":
			emit(`{"type":"control_request","request_id":"p1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_1"}}`)
			echo(awaitResponse("p1"))
		case "hook":
			emit(`{"type":"control_request","request_id":"h1","request":{"subtype":"hook_callback","callback_id":"hook_1","input":{"hook_event_name":"SubagentStop","session_id":"s","transcript_path":"t","cwd":"/","agent_id":"agent-1","effort":{"level":"high"}},"tool_use_id":"toolu_2"}}`)
			echo(awaitResponse("h1"))
			emit(`{"type":"control_request","request_id":"h2","request":{"subtype":"hook_callback","callback_id":"hook_9","input":{}}}`)
			echo(awaitResponse("h2"))
		case "mcp":
			emit(`{"type":"control_request","request_id":"m1","request":{"subtype":"mcp_message","server_name":"app","message":{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"echo","arguments":{"say":"hi"}}}}}`)
			echo(awaitResponse("m1"))
			emit(`{"type":"control_request","request_id":"m2","request":{"subtype":"mcp_message","server_name":"app","message":{"jsonrpc":"2.0","method":"notifications/initialized"}}}`)
			echo(awaitResponse("m2"))
			emit(`{"type":"control_request","request_id":"m3","request":{"subtype":"mcp_message","server_name":"nowhere","message":{"jsonrpc":"2.0","id":1,"method":"ping"}}}`)
			echo(awaitResponse("m3"))
		case "unsupported":
			emit(`{"type":"control_request","request_id":"x1","request":{"subtype":"bogus"}}`)
			echo(awaitResponse("x1"))
			emit(`{"type":"control_request","request_id":"x2","request":{"subtype":"elicitation","mcp_server_name":"s","message":"m"}}`)
			echo(awaitResponse("x2"))
		case "cancel":
			emit(`{"type":"control_request","request_id":"c1","request":{"subtype":"can_use_tool","tool_name":"Slow","input":{},"tool_use_id":"toolu_3"}}`)
			time.Sleep(50 * time.Millisecond)
			emit(`{"type":"control_cancel_request","request_id":"c1"}`)
			echo(awaitResponse("c1"))
		case "noise":
			emit(`{"type":"keep_alive"}`)
			emit(`not json at all`)
			emit(`{"type":"system","subtype":"session_state_changed","state":"running","sdk_host_only":true,"uuid":"u","session_id":"s"}`)
			emit(`{"type":"control_response","response":{"subtype":"success","request_id":"nobody"}}`)
			emit(fmt.Sprintf(`{"type":"assistant","message":{"id":"msg_1","type":"message","role":"assistant","model":"opus","content":[{"type":"text","text":%q},{"type":"tool_use","id":"toolu_9","name":"Read","input":{"file_path":"/a"}}],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":null,"cache_read_input_tokens":5}},"parent_tool_use_id":null,"uuid":"u1","session_id":"s"}`, strings.Repeat("x", 200_000)))
			emit(`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"he"}},"parent_tool_use_id":null,"uuid":"u2","session_id":"s"}`)
			emit(`{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1,"result":"done","stop_reason":null,"total_cost_usd":0,"usage":{},"modelUsage":{},"permission_denials":[],"uuid":"u3","session_id":"s"}`)
		case "fail":
			os.Stderr.WriteString("auth failed: Authorization: Bearer abcdefghijklmnop0123 key=sk-ant-api03-secretsecret1\n")
			emit(`{"type":"result","subtype":"error_during_execution","duration_ms":1,"duration_api_ms":1,"is_error":true,"num_turns":1,"stop_reason":null,"total_cost_usd":0,"usage":{},"modelUsage":{},"permission_denials":[],"errors":[" Prompt is too long ",""],"uuid":"u","session_id":"s"}`)
			emit(`{"type":"system","subtype":"status","status":null,"uuid":"u","session_id":"s"}`)
			emit(`{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":0,"result":"","stop_reason":null,"total_cost_usd":0,"usage":{},"modelUsage":{},"permission_denials":[],"uuid":"u","session_id":"s"}`)
			os.Exit(1)
		case "die":
			os.Stderr.WriteString(strings.Repeat("y", 10_000) + "\nfatal: Authorization: Bearer abcdefghijklmnop0123\n")
			os.Exit(3)
		case "hang":
			time.Sleep(time.Hour)
		}
	}
}
