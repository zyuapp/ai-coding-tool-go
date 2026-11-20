package protocol

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeOptions(t *testing.T) Options {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Executable: executable,
		Env:        map[string]string{"FAKE_CLAUDE": "1", "PROBE": "kept", "NODE_OPTIONS": "--inspect", "CLAUDE_CODE_ENTRYPOINT": "", "CLAUDE_AGENT_SDK_VERSION": ""},
	}
}

func start(t *testing.T, opts Options) *Query {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	q, err := Start(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.Close)
	return q
}

// next reads messages until one of kind arrives.
func next(t *testing.T, q *Query, kind string) Message {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case message, ok := <-q.Messages():
			if !ok {
				t.Fatalf("messages ended waiting for %s: %v", kind, q.Err())
			}
			if message.Kind() == kind {
				return message
			}
		case <-timeout:
			t.Fatalf("no %s message", kind)
		}
	}
}

func field(t *testing.T, raw jsontext.Value, into any) {
	t.Helper()
	if err := unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

func echoed(t *testing.T, q *Query, into any) {
	t.Helper()
	var echo struct {
		Response jsontext.Value `json:"response"`
	}
	field(t, next(t, q, "fake_echo").Raw(), &echo)
	field(t, echo.Response, into)
}

func send(t *testing.T, q *Query, text string) {
	t.Helper()
	if err := q.SendUser(NewUserMessage(TextContent(text))); err != nil {
		t.Fatal(err)
	}
}

func TestArgsFollowTheSDKOrder(t *testing.T) {
	chrome := ExtraArg{Name: "chrome"}
	stdio := "stdio"
	opts := Options{
		Model: "opus", Effort: "high", Betas: []string{"context-1m-2025-08-07"}, Resume: "abc", ForkSession: true,
		DisallowedTools: []string{"AskUserQuestion"}, AllSkills: true, SettingSources: []string{"user", "project"},
		PermissionMode: "default", IncludePartialMessages: true, ExtraArgs: []ExtraArg{chrome, {Name: "flag", Value: ptr("-x")}, {Name: "name", Value: ptr("v")}},
		CanUseTool: func(context.Context, PermissionRequest) (*PermissionResult, error) { return nil, nil },
		McpServers: []NamedMcpServer{{Name: "cua-driver", Config: McpServerConfig{Type: &stdio, Command: "/bin/cua", Args: []string{"mcp"}}}},
		Plugins:    []Plugin{{Path: "/plugins/app"}}, AllowDangerouslySkipPermissions: true, Tools: []string{},
		JSONSchema: jsontext.Value(`{ "type": "object" }`), MaxTurns: 1,
	}
	args, err := opts.Args()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--output-format", "stream-json", "--verbose", "--input-format", "stream-json",
		"--effort=high", "--max-turns=1", "--model=opus", "--betas=context-1m-2025-08-07", `--json-schema={"type":"object"}`,
		"--permission-prompt-tool=stdio", "--resume=abc", "--allowedTools=Skill", "--disallowedTools=AskUserQuestion", "--tools=",
		"--mcp-config", `{"mcpServers":{"cua-driver":{"type":"stdio","command":"/bin/cua","args":["mcp"]}}}`,
		"--setting-sources=user,project", "--permission-mode=default", "--allow-dangerously-skip-permissions",
		"--include-partial-messages", "--plugin-dir=/plugins/app", "--fork-session", "--chrome", "--flag=-x", "--name", "v",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args\n got %q\nwant %q", args, want)
	}
	if _, err := (&Options{Skills: []string{"a(b)"}}).Args(); err == nil || !strings.Contains(err.Error(), "parentheses") {
		t.Fatalf("a skill name with parentheses is refused: %v", err)
	}
	args, _ = (&Options{Skills: []string{"review"}, AllowedTools: []string{"Read"}, SettingSources: []string{}}).Args()
	if !slices.Contains(args, "--allowedTools=Read,Skill(review)") || !slices.Contains(args, "--setting-sources=") {
		t.Fatalf("named skills and empty setting sources: %q", args)
	}
}

func TestEnvironmentAddsTheSDKVariables(t *testing.T) {
	env := (&Options{Env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "aicodingtool"}}).Environment([]string{"PATH=/bin", "NODE_OPTIONS=--x", "DEBUG=1", "claude_code_sdk_reads_session_state=0"})
	want := []string{"PATH=/bin", "claude_code_sdk_reads_session_state=0", "CLAUDE_CODE_ENTRYPOINT=aicodingtool", "CLAUDE_AGENT_SDK_VERSION=" + SDKVersion}
	if !slices.Equal(env, want) {
		t.Fatalf("env\n got %q\nwant %q", env, want)
	}
	env = (&Options{}).Environment(nil)
	if !slices.Contains(env, "CLAUDE_CODE_ENTRYPOINT=sdk-ts") || !slices.Contains(env, "CLAUDE_CODE_SDK_READS_SESSION_STATE=1") {
		t.Fatalf("defaults: %q", env)
	}
}

func TestInitializeCarriesHooksPromptAndManifests(t *testing.T) {
	hook := func(context.Context, HookInput, *string) (any, error) { return struct{}{}, nil }
	opts := Options{
		Hooks: []HookEvent{
			{Event: "PreToolUse", Matchers: []HookMatcher{{Hooks: []HookCallback{hook}}}},
			{Event: "SubagentStop", Matchers: []HookMatcher{{Hooks: []HookCallback{hook, hook}}}},
		},
		SdkMcpServers:       []*SdkMcpServer{{Name: "aicodingtool-threads", AlwaysLoad: true, Tools: []SdkMcpTool{{Name: "list_threads", Description: "List threads."}}}},
		SystemPrompt:        SystemPrompt{Kind: SystemPromptPreset, Append: ptr("be kind")},
		ForwardSubagentText: true,
	}
	request, callbacks := opts.initializeRequest(map[string]McpManifest{"aicodingtool-threads": opts.SdkMcpServers[0].Manifest()})
	data, _ := json.Marshal(request, json.Deterministic(true))
	want := `{"subtype":"initialize","hooks":{"PreToolUse":[{"hookCallbackIds":["hook_0"]}],"SubagentStop":[{"hookCallbackIds":["hook_1","hook_2"]}]},"sdkMcpServers":["aicodingtool-threads"],"sdkMcpServerManifests":{"aicodingtool-threads":{"initializeResult":{"protocolVersion":"2025-11-25","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"aicodingtool-threads","version":"1.0.0"}},"toolsListResult":{"tools":[{"name":"list_threads","description":"List threads.","inputSchema":{"type":"object","properties":{}},"execution":{"taskSupport":"forbidden"},"_meta":{"anthropic/alwaysLoad":true}}]}}},"appendSystemPrompt":"be kind","forwardSubagentText":true}`
	if string(data) != want {
		t.Fatalf("initialize\n got %s\nwant %s", data, want)
	}
	if len(callbacks) != 3 || callbacks["hook_2"] == nil {
		t.Fatalf("callbacks: %v", callbacks)
	}
	request, _ = (&Options{}).initializeRequest(nil)
	data, _ = marshal(request)
	if string(data) != `{"subtype":"initialize","systemPrompt":[""]}` {
		t.Fatalf("no system prompt is an empty one: %s", data)
	}
}

func TestInitializeAndMessagesRoundTrip(t *testing.T) {
	opts := fakeOptions(t)
	opts.Model = "opus"
	q := start(t, opts)
	var started struct {
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		Initialize map[string]any    `json:"initialize"`
	}
	field(t, next(t, q, "fake_start").Raw(), &started)
	if !slices.Contains(started.Args, "--model=opus") || started.Args[0] != "--output-format" {
		t.Fatalf("args: %q", started.Args)
	}
	if started.Env["PROBE"] != "kept" || started.Env["NODE_OPTIONS"] != "" || started.Env["CLAUDE_CODE_ENTRYPOINT"] != "sdk-ts" || started.Env["CLAUDE_AGENT_SDK_VERSION"] != SDKVersion || started.Env["CLAUDE_CODE_SDK_READS_SESSION_STATE"] != "1" {
		t.Fatalf("env: %v", started.Env)
	}
	init := next(t, q, "system/init").(*SystemInit)
	if init.SessionID != "session-1" {
		t.Fatalf("init: %+v", init)
	}
	ctx := context.Background()
	commands, err := q.SupportedCommands(ctx)
	if err != nil || len(commands) != 1 || commands[0].Name != "review" {
		t.Fatalf("commands: %v %v", commands, err)
	}
	models, _ := q.SupportedModels(ctx)
	if len(models) != 1 || models[0].Value != "opus" {
		t.Fatalf("models: %v", models)
	}
	if err := q.Interrupt(ctx); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	_, err = q.Usage(ctx)
	var control *ControlError
	if !errors.As(err, &control) || control.Message != "usage is off" || control.Code != "usage_off" {
		t.Fatalf("an error answer fails with its message and code: %v", err)
	}
	if err := q.SetModel(ctx, ptr("sonnet")); err != nil {
		t.Fatal(err)
	}
	var setModel map[string]string
	echoed(t, q, &setModel)
	if setModel["subtype"] != "set_model" || setModel["model"] != "sonnet" {
		t.Fatalf("set_model: %v", setModel)
	}

	send(t, q, "noise")
	var kinds []string
	var assistant *AssistantMessage
	var delta *StreamEvent
	for {
		message := <-q.Messages()
		kinds = append(kinds, message.Kind())
		switch typed := message.(type) {
		case *AssistantMessage:
			assistant = typed
		case *StreamEvent:
			delta = typed
		}
		if message.Kind() == "result" {
			break
		}
	}
	if !slices.Equal(kinds, []string{"assistant", "stream_event", "result"}) {
		t.Fatalf("control frames, keep-alives, host-only state and junk are not messages: %v", kinds)
	}
	if len(*assistant.Message.Content[0].Text) != 200_000 || *assistant.Message.Content[1].Name != "Read" || string(assistant.Message.Content[1].Input) != `{"file_path":"/a"}` {
		t.Fatalf("assistant content: %+v", assistant.Message.Content[1])
	}
	if usage := assistant.Message.Usage; usage.InputTokens != 3 || usage.CacheCreationInputTokens != nil || *usage.CacheReadInputTokens != 5 {
		t.Fatalf("usage: %+v", usage)
	}
	if *delta.Event.Delta.Text != "he" || delta.Event.Type != "content_block_delta" {
		t.Fatalf("delta: %+v", delta.Event)
	}
}

func TestPermissionHookAndMcpRequestsAreAnswered(t *testing.T) {
	opts := fakeOptions(t)
	var asked PermissionRequest
	opts.CanUseTool = func(ctx context.Context, request PermissionRequest) (*PermissionResult, error) {
		asked = request
		if request.ToolName == "Slow" {
			<-ctx.Done()
			return Deny("cancelled"), nil
		}
		return Allow(request.Input), nil
	}
	var hookInput HookInput
	var hookToolUse *string
	noop := func(context.Context, HookInput, *string) (any, error) { return map[string]any{}, nil }
	opts.Hooks = []HookEvent{{Event: "PreToolUse", Matchers: []HookMatcher{{Hooks: []HookCallback{noop}}}}, {Event: "SubagentStop", Matchers: []HookMatcher{{Hooks: []HookCallback{
		func(_ context.Context, input HookInput, toolUseID *string) (any, error) {
			hookInput, hookToolUse = input, toolUseID
			return map[string]bool{"continue": true}, nil
		},
	}}}}}
	opts.SdkMcpServers = []*SdkMcpServer{{Name: "app", Tools: []SdkMcpTool{{Name: "echo", Handler: func(_ context.Context, arguments jsontext.Value) (jsontext.Value, error) {
		return jsontext.Value(`{"content":[{"type":"text","text":` + string(arguments) + `}]}`), nil
	}}}}}
	q := start(t, opts)

	send(t, q, "permission")
	var permission map[string]any
	echoed(t, q, &permission)
	if asked.ToolName != "Bash" || asked.ToolUseID != "toolu_1" || asked.RequestID != "p1" {
		t.Fatalf("asked: %+v", asked)
	}
	response := permission["response"].(map[string]any)
	if permission["subtype"] != "success" || response["behavior"] != "allow" || response["toolUseID"] != "toolu_1" || response["updatedInput"].(map[string]any)["command"] != "ls" {
		t.Fatalf("permission answer: %v", permission)
	}

	send(t, q, "hook")
	var hook map[string]any
	echoed(t, q, &hook)
	if hook["response"].(map[string]any)["continue"] != true || *hookInput.AgentID != "agent-1" || hookInput.Effort.Level != "high" || *hookToolUse != "toolu_2" {
		t.Fatalf("hook: %v %+v", hook, hookInput)
	}
	echoed(t, q, &hook)
	if hook["subtype"] != "error" || hook["error"] != "No hook callback found for ID: hook_9" {
		t.Fatalf("unknown hook: %v", hook)
	}

	send(t, q, "mcp")
	var mcp struct {
		Response struct {
			McpResponse map[string]any `json:"mcp_response"`
		} `json:"response"`
	}
	echoed(t, q, &mcp)
	result := mcp.Response.McpResponse
	if result["id"] != 7.0 || result["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(map[string]any)["say"] != "hi" {
		t.Fatalf("tools/call: %v", result)
	}
	echoed(t, q, &mcp)
	if mcp.Response.McpResponse["id"] != 0.0 || mcp.Response.McpResponse["result"] == nil {
		t.Fatalf("a notification gets the empty result: %v", mcp.Response.McpResponse)
	}
	var missing map[string]any
	echoed(t, q, &missing)
	if missing["error"] != "SDK MCP server not found: nowhere" {
		t.Fatalf("unknown server: %v", missing)
	}

	send(t, q, "unsupported")
	var unsupported map[string]any
	echoed(t, q, &unsupported)
	if unsupported["error"] != "Unsupported control request subtype: bogus" || unsupported["error_code"] != "unsupported_request" {
		t.Fatalf("unsupported: %v", unsupported)
	}
	echoed(t, q, &unsupported)
	if unsupported["response"].(map[string]any)["action"] != "decline" {
		t.Fatalf("elicitation: %v", unsupported)
	}

	send(t, q, "cancel")
	var cancelled map[string]any
	echoed(t, q, &cancelled)
	if cancelled["response"].(map[string]any)["message"] != "cancelled" {
		t.Fatalf("a cancelled request's handler sees its context end, and its answer is still sent: %v", cancelled)
	}
}

func TestAFailedExitNamesTheErrorResult(t *testing.T) {
	q := start(t, fakeOptions(t))
	next(t, q, "system/init")
	send(t, q, "fail")
	for range q.Messages() {
	}
	if err := q.Err(); err == nil || err.Error() != "Claude Code returned an error result: Prompt is too long" {
		t.Fatalf("err: %v", err)
	}
}

func TestAnExitCarriesItsRedactedStderrTail(t *testing.T) {
	q := start(t, fakeOptions(t))
	next(t, q, "system/init")
	pending := make(chan error, 1)
	send(t, q, "die")
	go func() {
		_, err := q.Request(context.Background(), subtypeOnly{Subtype: "never_answered"})
		pending <- err
	}()
	for range q.Messages() {
	}
	err := q.Err()
	if err == nil || !strings.HasPrefix(err.Error(), "Claude Code process exited with code 3. stderr: ") || !strings.HasSuffix(err.Error(), "fatal: Authorization: Bearer <token>") {
		t.Fatalf("err: %v", err)
	}
	if len(err.Error()) > 2200 {
		t.Fatalf("the tail is bounded: %d", len(err.Error()))
	}
	if requestErr := <-pending; requestErr == nil {
		t.Fatal("a request still waiting fails once the process is gone")
	}
	if _, err := q.Request(context.Background(), subtypeOnly{Subtype: "later"}); err == nil {
		t.Fatal("a request after the end fails")
	}
}

func TestStderrGoesToTheCallerWhenAsked(t *testing.T) {
	opts := fakeOptions(t)
	var seen atomic.Value
	seen.Store("")
	opts.Stderr = func(text string) { seen.Store(seen.Load().(string) + text) }
	q := start(t, opts)
	next(t, q, "system/init")
	send(t, q, "die")
	for range q.Messages() {
	}
	if q.Err() == nil || q.Err().Error() != "Claude Code process exited with code 3" {
		t.Fatalf("err: %v", q.Err())
	}
	if !strings.Contains(seen.Load().(string), "fatal: Authorization: Bearer abcdefghijklmnop0123") {
		t.Fatalf("stderr was not handed over")
	}
}

func TestCloseEndsTheProcessAndTheMessages(t *testing.T) {
	q := start(t, fakeOptions(t))
	next(t, q, "system/init")
	send(t, q, "hang")
	pending := make(chan error, 1)
	go func() {
		_, err := q.Request(context.Background(), subtypeOnly{Subtype: "never_answered"})
		pending <- err
	}()
	time.Sleep(50 * time.Millisecond)
	q.Close()
	if err := <-pending; !errors.Is(err, ErrClosed) {
		t.Fatalf("pending: %v", err)
	}
	for range q.Messages() {
	}
	select {
	case <-q.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("a process that ignores its closed input is terminated")
	}
	if q.Err() != nil {
		t.Fatalf("a closed query ends cleanly: %v", q.Err())
	}
	if err := q.SendUser(NewUserMessage(TextContent("late"))); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after close: %v", err)
	}
}

func TestACleanEndClosesMessagesWithoutError(t *testing.T) {
	q := start(t, fakeOptions(t))
	next(t, q, "system/init")
	q.EndInput()
	for range q.Messages() {
	}
	if q.Err() != nil {
		t.Fatalf("err: %v", q.Err())
	}
}

func TestAMissingBinaryIsNamed(t *testing.T) {
	_, err := Start(context.Background(), Options{Executable: "/nonexistent/claude"})
	if err == nil || err.Error() != "Claude Code native binary not found at /nonexistent/claude. Please ensure Claude Code is installed via native installer or specify a valid path with options.pathToClaudeCodeExecutable." {
		t.Fatalf("err: %v", err)
	}
}

func TestMcpServerAnswersLikeTheSDKServer(t *testing.T) {
	server := &SdkMcpServer{Name: "app", Version: "2.0.0", Tools: []SdkMcpTool{{Name: "fail", Handler: func(context.Context, jsontext.Value) (jsontext.Value, error) {
		return nil, errors.New("boom")
	}}}}
	ctx := context.Background()
	cases := map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`: `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"app","version":"2.0.0"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999"}}`:       `{"jsonrpc":"2.0","id":2,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"app","version":"2.0.0"}}}`,
		`{"jsonrpc":"2.0","id":"a","method":"ping"}`:                                               `{"jsonrpc":"2.0","id":"a","result":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`:                                       `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope"}}`:                  `{"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"MCP error -32602: Tool nope not found"}],"isError":true}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"fail","arguments":{}}}`:   `{"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"boom"}],"isError":true}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`:                                   `{"jsonrpc":"2.0","id":0,"result":{}}`,
	}
	for request, want := range cases {
		if got := string(server.Respond(ctx, jsontext.Value(request))); got != want {
			t.Errorf("%s\n got %s\nwant %s", request, got, want)
		}
	}
}

func TestStderrTailCarriesNoCredentials(t *testing.T) {
	tail := stderrTail("auth failed: Authorization: Bearer abcdefghijklmnop key=sk-ant-api03-secretsecret1 header: Basic QWxhZGRpbjpvcGVuIHNlc2FtZQ==\n")
	if strings.Contains(tail, "abcdefghijklmnop") || strings.Contains(tail, "secretsecret") || strings.Contains(tail, "QWxhZGRp") {
		t.Fatalf("tail: %s", tail)
	}
	if tail != "auth failed: Authorization: Bearer <token> key=<token> header: Basic <token>" {
		t.Fatalf("tail: %s", tail)
	}
	if got := stderrTail("jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U done"); got != "jwt <jwt> done" {
		t.Fatalf("jwt: %s", got)
	}
}

func TestDecodeMessageKeepsUnknownKinds(t *testing.T) {
	message, err := DecodeMessage(jsontext.Value(`{"type":"system","subtype":"hook_progress","x":1}`))
	if err != nil || message.Kind() != "system/hook_progress" {
		t.Fatalf("%v %v", message, err)
	}
	user, err := DecodeMessage(jsontext.Value(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]},"parent_tool_use_id":null}`))
	if err != nil || !user.(*UserMessage).Message.Content.IsBlocks || *user.(*UserMessage).Message.Content.Blocks[0].ToolUseID != "t" {
		t.Fatalf("%v %v", user, err)
	}
	data, _ := marshal(NewUserMessage(BlockContent(Base64ImageBlock("image/png", "AAA"), TextBlock("name it"))))
	want := `{"type":"user","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}},{"type":"text","text":"name it"}]},"parent_tool_use_id":null,"session_id":""}`
	if string(data) != want {
		t.Fatalf("user message\n got %s\nwant %s", data, want)
	}
	steer := NewUserMessage(TextContent("go"))
	steer.Priority = ptr(PriorityNow)
	data, _ = marshal(steer)
	if string(data) != `{"type":"user","message":{"role":"user","content":"go"},"parent_tool_use_id":null,"priority":"now","session_id":""}` {
		t.Fatalf("steer: %s", data)
	}
}
