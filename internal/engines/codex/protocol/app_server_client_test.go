package protocol

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The test binary stands in for the app server when FAKE_APP_SERVER is set, speaking enough of
// the protocol for these tests, with the jsonrpc field omitted as the real server does.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_APP_SERVER") != "" {
		runFakeAppServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeMessage struct {
	ID     *int64         `json:"id"`
	Method *string        `json:"method"`
	Params map[string]any `json:"params"`
	Result jsontext.Value `json:"result"`
	Error  jsontext.Value `json:"error"`
}

func runFakeAppServer() {
	terminated := make(chan os.Signal, 1)
	signal.Notify(terminated, syscall.SIGTERM)
	go func() {
		<-terminated
		os.Exit(0)
	}()
	var mu sync.Mutex
	write := func(text string) {
		mu.Lock()
		os.Stdout.WriteString(text)
		mu.Unlock()
	}
	send := func(message any) {
		line, _ := Marshal(message)
		write(string(line) + "\n")
	}
	awaiting := map[int64]func(jsontext.Value){}
	initialized := false
	nextServerID := int64(0)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var message fakeMessage
		if Unmarshal(scanner.Bytes(), &message) != nil {
			continue
		}
		if message.ID != nil && message.Method == nil {
			reply := message.Result
			if len(reply) == 0 {
				reply = message.Error
			}
			if answer := awaiting[*message.ID]; answer != nil {
				answer(reply)
			}
			continue
		}
		id := message.ID
		switch *message.Method {
		case "initialize":
			name := message.Params["clientInfo"].(map[string]any)["name"]
			send(map[string]any{"id": *id, "result": map[string]any{"userAgent": fmt.Sprintf("fake/%v", name), "codexHome": "/tmp", "platformFamily": "unix", "platformOs": "macos"}})
		case "initialized":
			initialized = true
		case "model/list":
			send(map[string]any{"id": *id, "result": map[string]any{"data": []any{}, "nextCursor": nil, "initialized": initialized}})
		case "thread/start":
			model := message.Params["model"]
			go func() {
				time.Sleep(30 * time.Millisecond)
				send(map[string]any{"id": *id, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}, "model": model}})
			}()
		case "thread/read":
			send(map[string]any{"id": *id, "error": map[string]any{"code": -32600, "message": "no such thread", "data": map[string]any{"threadId": message.Params["threadId"]}}})
		case "turn/start":
			threadID := message.Params["threadId"]
			serverID := nextServerID
			nextServerID++
			send(map[string]any{"method": "turn/started", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "turn-1"}}})
			awaiting[serverID] = func(reply jsontext.Value) {
				send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": threadID, "turn": map[string]any{"id": "turn-1", "status": "completed", "reply": reply}}})
				send(map[string]any{"id": *id, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			}
			send(map[string]any{"method": "item/commandExecution/requestApproval", "id": serverID, "params": map[string]any{"threadId": threadID, "turnId": "turn-1", "itemId": "item-1", "command": "rm -rf build"}})
		case "thread/list":
			body, _ := Marshal(map[string]any{"id": *id, "result": map[string]any{"data": []any{map[string]any{"id": strings.Repeat("x", 200_000)}}, "nextCursor": nil}})
			cut := len(body) / 3
			write(string(body[:cut]))
			time.Sleep(10 * time.Millisecond)
			write(string(body[cut : 2*cut]))
			time.Sleep(10 * time.Millisecond)
			write(string(body[2*cut:]) + "\n" + `{"method":"warning","params":{"message":"framed"}}` + "\n")
		case "account/logout":
			os.Stderr.WriteString("fatal: signed out\n")
			os.Exit(3)
		case "getAuthStatus":
			send(map[string]any{"id": *id, "result": map[string]any{"authMethod": "apikey", "authToken": "fixture-key", "requiresOpenaiAuth": true}})
		case "account/login/start":
			send(map[string]any{"id": *id, "result": map[string]any{"type": "apiKey"}})
			send(map[string]any{"method": "warning", "params": map[string]any{"message": fmt.Sprintf("login %v", message.Params["apiKey"])}})
		}
	}
}

var clientInfo = ClientInfo{Name: "test-client", Version: "0"}

func fakeCommand(t *testing.T) Command {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Command{Executable: executable, Env: append(os.Environ(), "FAKE_APP_SERVER=1")}
}

func connect(t *testing.T) *Client {
	t.Helper()
	client := Connect(fakeCommand(t))
	t.Cleanup(func() { client.Close() })
	return client
}

func initialized(t *testing.T) *Client {
	t.Helper()
	client := connect(t)
	if _, err := client.Initialize(testContext(t), clientInfo, nil); err != nil {
		t.Fatal(err)
	}
	return client
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestHandshakeInitializesThenTellsTheServer(t *testing.T) {
	client := connect(t)
	server, err := client.Initialize(testContext(t), clientInfo, nil)
	if err != nil || server.UserAgent != "fake/test-client" {
		t.Fatalf("%+v %v", server, err)
	}
	models, err := Call[map[string]any](testContext(t), client, "model/list", Empty{})
	if err != nil || models["initialized"] != true {
		t.Fatalf("initialized before anything else: %v %v", models, err)
	}
}

func TestResponsesPairWithTheirRequestInAnyOrder(t *testing.T) {
	client := initialized(t)
	ctx := testContext(t)
	var order []string
	var mu sync.Mutex
	var wait sync.WaitGroup
	wait.Add(2)
	var started ThreadSessionResponse
	go func() {
		defer wait.Done()
		model := "gpt-6.2-sol"
		started, _ = Call[ThreadSessionResponse](ctx, client, "thread/start", ThreadStartParams{Model: &model})
		mu.Lock()
		order = append(order, "thread/start")
		mu.Unlock()
	}()
	time.Sleep(5 * time.Millisecond)
	go func() {
		defer wait.Done()
		_, _ = client.Request(ctx, "model/list", Empty{})
		mu.Lock()
		order = append(order, "model/list")
		mu.Unlock()
	}()
	wait.Wait()
	if started.Thread.ID != "thread-1" || started.Model != "gpt-6.2-sol" {
		t.Fatalf("%+v", started)
	}
	if strings.Join(order, ",") != "model/list,thread/start" {
		t.Fatalf("order: %v", order)
	}
}

func TestAnErrorResponseCarriesCodeMessageAndData(t *testing.T) {
	client := initialized(t)
	_, err := client.Request(testContext(t), "thread/read", ThreadReadParams{ThreadID: "missing"})
	var rpc *AppServerError
	if !errors.As(err, &rpc) || rpc.Code != -32600 || rpc.Error() != "thread/read: no such thread" || string(rpc.Data) != `{"threadId":"missing"}` {
		t.Fatalf("%v", err)
	}
}

func TestNotificationsAndServerRequests(t *testing.T) {
	client := initialized(t)
	ctx := testContext(t)
	var started []string
	stop := OnNotification(client, NotifyTurnStarted, func(params TurnNotification) { started = append(started, params.Turn.ID) })
	var requests []*IncomingRequest
	client.OnRequest(func(request *IncomingRequest) {
		requests = append(requests, request)
		if request.Method == RequestCommandApproval {
			request.Respond(ApprovalResponse{Decision: DecisionDecline})
		}
	})
	completed := make(chan jsontext.Value, 2)
	client.On(NotifyTurnCompleted, func(params jsontext.Value) { completed <- params })

	turn, err := Call[TurnResponse](ctx, client, "turn/start", TurnStartParams{ThreadID: "thread-1", Input: []UserInput{Text("go")}})
	if err != nil || turn.Turn.ID != "turn-1" {
		t.Fatalf("%+v %v", turn, err)
	}
	if strings.Join(started, ",") != "turn-1" || len(requests) != 1 || requests[0].Method != RequestCommandApproval {
		t.Fatalf("started %v requests %v", started, requests)
	}
	var params CommandExecutionRequestApprovalParams
	if requests[0].DecodeParams(&params); *params.Command != "rm -rf build" {
		t.Fatalf("%+v", params)
	}
	var done struct {
		Turn struct {
			Reply ApprovalResponse `json:"reply"`
		} `json:"turn"`
	}
	_ = Unmarshal(<-completed, &done)
	if done.Turn.Reply.Decision != "decline" {
		t.Fatalf("reply: %+v", done)
	}
	stop()
	if _, err := client.Request(ctx, "turn/start", TurnStartParams{ThreadID: "thread-1", Input: []UserInput{}}); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 {
		t.Fatalf("an unsubscribed handler hears nothing: %v", started)
	}
}

func TestAServerRequestNobodyHandlesIsRefused(t *testing.T) {
	client := initialized(t)
	completed := make(chan jsontext.Value, 1)
	client.On(NotifyTurnCompleted, func(params jsontext.Value) { completed <- params })
	if _, err := client.Request(testContext(t), "turn/start", TurnStartParams{ThreadID: "thread-1", Input: []UserInput{}}); err != nil {
		t.Fatal(err)
	}
	var done struct {
		Turn struct {
			Reply JSONRPCError `json:"reply"`
		} `json:"turn"`
	}
	_ = Unmarshal(<-completed, &done)
	if done.Turn.Reply.Code != -32601 || !strings.Contains(done.Turn.Reply.Message, "item/commandExecution/requestApproval") {
		t.Fatalf("%+v", done)
	}
}

func TestAMessageSplitAcrossWritesIsFramedByNewline(t *testing.T) {
	client := initialized(t)
	warned := make(chan string, 1)
	OnNotification(client, NotifyWarning, func(params WarningNotification) { warned <- params.Message })
	threads, err := Call[struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}](testContext(t), client, "thread/list", Empty{})
	if err != nil || len(threads.Data) != 1 || len(threads.Data[0].ID) != 200_000 {
		t.Fatalf("%v", err)
	}
	if message := <-warned; message != "framed" {
		t.Fatal(message)
	}
}

func TestTheChildExitingFailsEveryPendingRequest(t *testing.T) {
	client := initialized(t)
	ctx := testContext(t)
	errs := make(chan error, 2)
	go func() { _, err := client.Request(ctx, "account/logout", nil); errs <- err }()
	go func() { _, err := client.Request(ctx, "thread/start", Empty{}); errs <- err }()
	var logout error
	for range 2 {
		err := <-errs
		var exited *AppServerExited
		if !errors.As(err, &exited) || exited.Exit.Code == nil || *exited.Exit.Code != 3 || !strings.Contains(err.Error(), "exited with code 3") || !strings.Contains(err.Error(), "fatal: signed out") {
			t.Fatalf("%v", err)
		}
		if strings.Contains(err.Error(), "account/logout was pending") {
			logout = err
		}
	}
	if logout == nil {
		t.Fatal("the logout names itself as pending")
	}
	<-client.Exited()
	if code := client.Exit().Code; code == nil || *code != 3 {
		t.Fatalf("exit %v", client.Exit())
	}
	_, err := client.Request(ctx, "model/list", Empty{})
	if err == nil || !strings.Contains(err.Error(), "before model/list") {
		t.Fatalf("%v", err)
	}
}

func TestClosingKillsTheChildAndSettlesItsExit(t *testing.T) {
	client := initialized(t)
	exit := client.Close()
	if exit.Code == nil || *exit.Code != 0 {
		t.Fatalf("%+v", exit)
	}
	if again := client.Close(); again.Code == nil || *again.Code != 0 {
		t.Fatalf("%+v", again)
	}
}

func TestAServerThatCannotStartFailsItsRequests(t *testing.T) {
	client := Connect(Command{Executable: "/nonexistent/codex"})
	_, err := client.Initialize(testContext(t), clientInfo, nil)
	if err == nil || err.Error() != "Codex app server exited with code null before initialize\nspawn /nonexistent/codex ENOENT" {
		t.Fatalf("%v", err)
	}
}

func TestInitializeBorrowsTheSharedAccount(t *testing.T) {
	command := fakeCommand(t)
	shared := fakeCommand(t)
	shared.Env = append(shared.Env, "CODEX_HOME=/fixture/borrow")
	command.SharedAuth = &shared
	client := Connect(command)
	defer client.Close()
	warned := make(chan string, 1)
	OnNotification(client, NotifyWarning, func(params WarningNotification) { warned <- params.Message })
	if _, err := client.Initialize(testContext(t), clientInfo, nil); err != nil {
		t.Fatal(err)
	}
	if message := <-warned; message != "login fixture-key" {
		t.Fatalf("the conversation signs in with the shared key: %s", message)
	}
}

func TestParamsEncodeAsTheTypeScriptClientWrites(t *testing.T) {
	model, effort, tier := "gpt-6.2-sol", "high", "default"
	reviewer := ApprovalsReviewerUser
	cases := []struct {
		value any
		want  string
	}{
		{TurnStartParams{ThreadID: "t", Input: []UserInput{Text("go"), LocalImageInput{Path: "/a.png"}, SkillInput{Name: "s", Path: "/s"}}, Model: &model, Effort: &effort, ServiceTier: &tier,
			ApprovalPolicy: &AskForApproval{Granular: &GranularApproval{McpElicitations: true}}, ApprovalsReviewer: &reviewer,
			SandboxPolicy: WorkspaceWritePolicy{WritableRoots: []string{}}},
			`{"threadId":"t","input":[{"type":"text","text":"go","text_elements":[]},{"type":"localImage","path":"/a.png"},{"type":"skill","name":"s","path":"/s"}],"approvalPolicy":{"granular":{"sandbox_approval":false,"rules":false,"skill_approval":false,"request_permissions":false,"mcp_elicitations":true}},"approvalsReviewer":"user","sandboxPolicy":{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":false},"model":"gpt-6.2-sol","serviceTier":"default","effort":"high"}`},
		{ThreadStartParams{ApprovalPolicy: ApprovalMode(ApprovalNever), Config: map[string]jsontext.Value{"model_reasoning_effort": jsontext.Value(`"low"`)}},
			`{"approvalPolicy":"never","config":{"model_reasoning_effort":"low"}}`},
		{ReviewStartParams{ThreadID: "t", Target: UncommittedChangesTarget{}, Delivery: strPtr("inline")}, `{"threadId":"t","target":{"type":"uncommittedChanges"},"delivery":"inline"}`},
		{ChatGPTAuthTokensLogin{AccessToken: "a", ChatgptAccountID: "b"}, `{"type":"chatgptAuthTokens","accessToken":"a","chatgptAccountId":"b","chatgptPlanType":null}`},
		{DeclineElicitation(), `{"action":"decline","content":null,"_meta":null}`},
		{ToolRequestUserInputResponse{Answers: map[string]ToolRequestUserInputAnswer{"q": {Answers: []string{"yes"}}}}, `{"answers":{"q":{"answers":["yes"]}}}`},
		{replyMessage{ID: StringID("r1"), Body: resultBody{Result: ApprovalResponse{Decision: "accept"}}}, `{"jsonrpc":"2.0","id":"r1","result":{"decision":"accept"}}`},
	}
	for _, c := range cases {
		data, err := Marshal(c.value)
		if err != nil || string(data) != c.want {
			t.Errorf("%T\n got %s %v\nwant %s", c.value, data, err, c.want)
		}
	}
}

func TestThreadItemsDecodeFlat(t *testing.T) {
	var completed ItemCompletedNotification
	raw := `{"item":{"type":"userMessage","id":"i","clientId":null,"content":[{"type":"text","text":"hi","text_elements":[]},{"type":"localImage","path":"/x.png"}]},"threadId":"t","turnId":"u","completedAtMs":1}`
	if err := Unmarshal([]byte(raw), &completed); err != nil {
		t.Fatal(err)
	}
	inputs := completed.Item.UserInputs()
	if len(inputs) != 2 || *inputs[0].Text != "hi" || *inputs[1].Path != "/x.png" {
		t.Fatalf("%+v", inputs)
	}
	var item ThreadItem
	_ = Unmarshal([]byte(`{"type":"mcpToolCall","id":"m","server":"s","tool":"t","status":"completed","arguments":{"a":1},"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":null,"_meta":null},"error":null,"durationMs":3}`), &item)
	if result := item.McpResult(); result == nil || len(result.Content) != 1 || *item.Tool != "t" {
		t.Fatalf("%+v", item)
	}
	_ = Unmarshal([]byte(`{"type":"imageGeneration","id":"g","status":"completed","revisedPrompt":null,"failure":null,"generationId":null,"result":"QUJD"}`), &item)
	if item.ImageResult() != "QUJD" {
		t.Fatalf("%+v", item)
	}
	var started TurnNotification
	_ = Unmarshal([]byte(`{"threadId":"t","turn":{"id":"u","items":[],"status":"failed","error":{"message":"m","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":502}},"additionalDetails":null}}}`), &started)
	if started.Turn.Error.CodexErrorInfo.Name() != "httpConnectionFailed" {
		t.Fatalf("%+v", started)
	}
}

func TestSharedAuthForwardsCredentialsWithoutLosingAccountIdentity(t *testing.T) {
	method := func(value string) *string { return &value }
	login, err := SharedAuthLogin(GetAuthStatusResponse{AuthMethod: method("apikey"), AuthToken: method("synthetic-api-key")})
	if err != nil || login.(APIKeyLogin).APIKey != "synthetic-api-key" {
		t.Fatalf("%v %v", login, err)
	}
	claims, _ := Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "fixture-account", "chatgpt_plan_type": "pro"}})
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
	login, err = SharedAuthLogin(GetAuthStatusResponse{AuthMethod: method("chatgpt"), AuthToken: &token})
	tokens := login.(ChatGPTAuthTokensLogin)
	if err != nil || tokens.ChatgptAccountID != "fixture-account" || *tokens.ChatgptPlanType != "pro" || tokens.AccessToken != token {
		t.Fatalf("%+v %v", login, err)
	}
	if _, err := SharedAuthLogin(GetAuthStatusResponse{AuthMethod: method("chatgpt"), AuthToken: method("invalid-synthetic-token")}); err == nil || strings.Contains(err.Error(), "invalid-synthetic-token") || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("%v", err)
	}
	if _, err := SharedAuthLogin(GetAuthStatusResponse{AuthMethod: method("headers")}); err == nil || !strings.Contains(err.Error(), "cannot share headers authentication") {
		t.Fatalf("%v", err)
	}
	if login, err := SharedAuthLogin(GetAuthStatusResponse{}); login != nil || err != nil {
		t.Fatalf("%v %v", login, err)
	}
}

type fakeAuthClient struct {
	opened, closed, reads *atomic.Int32
	status                chan jsontext.Value
}

func (f fakeAuthClient) Initialize(context.Context, ClientInfo, *InitializeCapabilities) (*InitializeResponse, error) {
	f.opened.Add(1)
	return &InitializeResponse{}, nil
}

func (f fakeAuthClient) Request(context.Context, string, any) (jsontext.Value, error) {
	f.reads.Add(1)
	status := <-f.status
	f.status <- status
	return status, nil
}

func (f fakeAuthClient) Close() ExitStatus {
	f.closed.Add(1)
	return ExitStatus{}
}

func TestConcurrentConversationsShareOneAuthConnectionAndRefresh(t *testing.T) {
	fake := fakeAuthClient{opened: &atomic.Int32{}, closed: &atomic.Int32{}, reads: &atomic.Int32{}, status: make(chan jsontext.Value, 1)}
	connect := func(Command) AuthClient { return fake }
	command := Command{Executable: "fixture-codex", Env: []string{"CODEX_HOME=/synthetic/shared/auth"}}
	ctx := testContext(t)
	one, err := AcquireSharedAuth(ctx, command, connect)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := AcquireSharedAuth(ctx, command, connect)
	results := make(chan LoginAccountParams, 2)
	go func() { login, _ := one.Read(ctx, true); results <- login }()
	time.Sleep(20 * time.Millisecond)
	go func() { login, _ := two.Read(ctx, true); results <- login }()
	time.Sleep(20 * time.Millisecond)
	if fake.opened.Load() != 1 || fake.reads.Load() != 1 {
		t.Fatalf("opened %d reads %d", fake.opened.Load(), fake.reads.Load())
	}
	fake.status <- jsontext.Value(`{"authMethod":"apikey","authToken":"synthetic","requiresOpenaiAuth":true}`)
	for range 2 {
		if login := <-results; login.(APIKeyLogin).APIKey != "synthetic" {
			t.Fatalf("%v", login)
		}
	}
	one.Release()
	if fake.closed.Load() != 0 {
		t.Fatal("closed while still held")
	}
	two.Release()
	two.Release()
	if fake.closed.Load() != 1 {
		t.Fatalf("closed %d", fake.closed.Load())
	}
}
