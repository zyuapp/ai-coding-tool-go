package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func TestMain(m *testing.M) {
	os.Unsetenv(agent.PrivateCodexHomeEnv)
	engineBinaryPath = func(agent.EngineCommand) (string, bool) { return "/fake/bin/codex", true }
	os.Exit(m.Run())
}

// What the fake server answers a method with. A handler may fail, or block until ctx ends, as the
// real server may.
type answer func(ctx context.Context, params jsontext.Value) (any, error)

type script map[string]answer

func reply(value string) answer {
	return func(context.Context, jsontext.Value) (any, error) { return jsontext.Value(value), nil }
}

func refuse(err error) answer {
	return func(context.Context, jsontext.Value) (any, error) { return nil, err }
}

// Never answers, as a server that hangs.
func hang(ctx context.Context, _ jsontext.Value) (any, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func turnJSON(id string) string {
	return `{"turn":{"id":"` + id + `","items":[],"status":"inProgress","error":null,"startedAt":null,"completedAt":null,"durationMs":null}}`
}

// Real shapes for the handful of responses the session reads, trimmed to what it reads.
func defaultScript() script {
	return script{
		"skills/list":  reply(`{"data":[{"cwd":"/tmp/project","skills":[],"errors":[]}]}`),
		"account/read": reply(`{"account":{"type":"chatgpt","email":"dev@example.com","planType":"pro"},"requiresOpenaiAuth":true}`),
		"thread/start": reply(`{"thread":{"id":"thread-1"},"model":"gpt-6.2-sol"}`),
		"thread/resume": func(_ context.Context, params jsontext.Value) (any, error) {
			var read struct {
				ThreadID string `json:"threadId"`
			}
			_ = jsonx.Unmarshal(params, &read)
			return jsontext.Value(`{"thread":{"id":"` + read.ThreadID + `"},"model":"gpt-6.2-sol"}`), nil
		},
		"thread/fork": reply(`{"thread":{"id":"thread-fork"},"model":"gpt-6.2-sol"}`),
		"thread/goal/set": func(_ context.Context, params jsontext.Value) (any, error) {
			var read struct {
				ThreadID  string `json:"threadId"`
				Objective string `json:"objective"`
			}
			_ = jsonx.Unmarshal(params, &read)
			return map[string]any{"goal": map[string]any{"threadId": read.ThreadID, "objective": read.Objective, "status": "active", "tokenBudget": nil, "tokensUsed": 0, "timeUsedSeconds": 0, "createdAt": 1, "updatedAt": 1}}, nil
		},
		"thread/goal/get":                      reply(`{"goal":null}`),
		"thread/goal/clear":                    reply(`{"cleared":true}`),
		"thread/inject_items":                  reply(`{}`),
		"thread/name/set":                      reply(`{}`),
		"thread/metadata/update":               reply(`{}`),
		"thread/unarchive":                     reply(`{}`),
		"thread/compact/start":                 reply(`{}`),
		"thread/backgroundTerminals/list":      reply(`{"data":[],"nextCursor":null}`),
		"thread/backgroundTerminals/terminate": reply(`{"terminated":true}`),
		"review/start":                         reply(turnJSON("turn-1")),
		"turn/start":                           reply(turnJSON("turn-1")),
		"turn/steer":                           reply(`{"turnId":"turn-1"}`),
		"turn/interrupt":                       reply(`{}`),
	}
}

type sentCall struct {
	method string
	params jsontext.Value
}

type fakeClient struct {
	command   protocol.Command
	script    script
	handshake func(ctx context.Context) error

	mu              sync.Mutex
	sent            []sentCall
	handlers        map[string][]*func(jsontext.Value)
	requestHandlers []*func(*ServerRequest)
	closed          bool
	exit            *protocol.ExitStatus
	exited          chan struct{}
	nextServerID    int
}

func newFakeClient(command protocol.Command, script script, handshake func(ctx context.Context) error) *fakeClient {
	return &fakeClient{command: command, script: script, handshake: handshake, handlers: map[string][]*func(jsontext.Value){}, exited: make(chan struct{})}
}

func (client *fakeClient) record(method string, params any) {
	var raw jsontext.Value
	if params != nil {
		raw, _ = jsonx.Marshal(params)
	}
	client.mu.Lock()
	client.sent = append(client.sent, sentCall{method, raw})
	client.mu.Unlock()
}

func (client *fakeClient) Initialize(ctx context.Context, info protocol.ClientInfo, _ *protocol.InitializeCapabilities) (*protocol.InitializeResponse, error) {
	client.record("initialize", info)
	if client.handshake != nil {
		ctx, cancel := client.bound(ctx)
		defer cancel()
		if err := client.handshake(ctx); err != nil {
			return nil, client.exitedError(err, "initialize")
		}
	}
	return &protocol.InitializeResponse{UserAgent: "fake", CodexHome: "/tmp", PlatformFamily: "unix", PlatformOs: "macos"}, nil
}

// ctx, also ended once the fake has exited.
func (client *fakeClient) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-client.exited:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// What a request learns once the process has gone, as the real client words it.
func (client *fakeClient) exitedError(err error, method string) error {
	if exit := client.Exit(); exit != nil && errors.Is(err, context.Canceled) {
		return &protocol.AppServerExited{Exit: *exit, Context: "while " + method + " was pending"}
	}
	return err
}

func (client *fakeClient) Request(ctx context.Context, method string, params any) (jsontext.Value, error) {
	client.record(method, params)
	handler := client.script[method]
	if handler == nil {
		return nil, fmt.Errorf("%s is not scripted", method)
	}
	var raw jsontext.Value
	if params != nil {
		raw, _ = jsonx.Marshal(params)
	}
	ctx, cancel := client.bound(ctx)
	defer cancel()
	result, err := handler(ctx, raw)
	if err != nil {
		return nil, client.exitedError(err, method)
	}
	if value, ok := result.(jsontext.Value); ok {
		return value, nil
	}
	return jsonx.Marshal(result)
}

func (client *fakeClient) On(method string, handler func(jsontext.Value)) func() {
	client.mu.Lock()
	defer client.mu.Unlock()
	held := &handler
	client.handlers[method] = append(client.handlers[method], held)
	return func() {
		client.mu.Lock()
		defer client.mu.Unlock()
		for index, entry := range client.handlers[method] {
			if entry == held {
				client.handlers[method] = append(client.handlers[method][:index:index], client.handlers[method][index+1:]...)
				return
			}
		}
	}
}

func (client *fakeClient) OnRequest(handler func(*ServerRequest)) func() {
	client.mu.Lock()
	defer client.mu.Unlock()
	held := &handler
	client.requestHandlers = append(client.requestHandlers, held)
	return func() {}
}

func (client *fakeClient) Close() protocol.ExitStatus {
	client.mu.Lock()
	client.closed = true
	client.mu.Unlock()
	code := 0
	client.exitWith(protocol.ExitStatus{Code: &code})
	return *client.Exit()
}

func (client *fakeClient) isClosed() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.closed
}

func (client *fakeClient) Exited() <-chan struct{} { return client.exited }

func (client *fakeClient) Exit() *protocol.ExitStatus {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.exit
}

// The server side: the process going away.
func (client *fakeClient) exitWith(exit protocol.ExitStatus) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.exit != nil {
		return
	}
	client.exit = &exit
	close(client.exited)
}

// The server side: a notification, delivered to whoever listens.
func (client *fakeClient) notify(method string, params any) {
	raw, err := jsonx.Marshal(params)
	if err != nil {
		panic(err)
	}
	client.mu.Lock()
	handlers := append([]*func(jsontext.Value){}, client.handlers[method]...)
	client.mu.Unlock()
	for _, handler := range handlers {
		(*handler)(raw)
	}
}

type serverReply struct {
	Result jsontext.Value         `json:"result,omitzero"`
	Error  *protocol.JSONRPCError `json:"error,omitzero"`
}

// The server side: a request, answered with whatever the session answers it.
func (client *fakeClient) ask(t *testing.T, method string, params any) serverReply {
	t.Helper()
	select {
	case answer := <-client.send(method, params):
		return answer
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was never answered", method)
		return serverReply{}
	}
}

// The server side: a request whose answer arrives on the channel, if it ever does.
func (client *fakeClient) send(method string, params any) <-chan serverReply {
	raw, err := jsonx.Marshal(params)
	if err != nil {
		panic(err)
	}
	client.mu.Lock()
	id := client.nextServerID
	client.nextServerID++
	handlers := append([]*func(*ServerRequest){}, client.requestHandlers...)
	client.mu.Unlock()
	replies := make(chan serverReply, 1)
	var once sync.Once
	request := &ServerRequest{
		ID:     protocol.NumberID(int64(id)),
		Method: method,
		Params: raw,
		Respond: func(result any) {
			once.Do(func() {
				encoded, err := jsonx.Marshal(result)
				if err != nil {
					panic(err)
				}
				replies <- serverReply{Result: encoded}
			})
		},
		Fail: func(err protocol.JSONRPCError) { once.Do(func() { replies <- serverReply{Error: &err} }) },
	}
	for _, handler := range handlers {
		(*handler)(request)
	}
	return replies
}

// Every call of one method, in order.
func (client *fakeClient) calls(method string) []jsontext.Value {
	client.mu.Lock()
	defer client.mu.Unlock()
	var found []jsontext.Value
	for _, call := range client.sent {
		if call.method == method {
			found = append(found, call.params)
		}
	}
	return found
}

func (client *fakeClient) methods() []string {
	client.mu.Lock()
	defer client.mu.Unlock()
	methods := make([]string, 0, len(client.sent))
	for _, call := range client.sent {
		methods = append(methods, call.method)
	}
	return methods
}

// Waits until the client has sent a method count times.
func sentBy(t *testing.T, client *fakeClient, method string, count int) {
	t.Helper()
	eventually(t, method+" was never sent", func() bool { return len(client.calls(method)) >= count })
}

func eventually(t *testing.T, failure string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Lets goroutines that are ready run, as a tick of the event loop does.
func tick() { time.Sleep(20 * time.Millisecond) }

func decodeJSON(t *testing.T, value []byte) any {
	t.Helper()
	var decoded any
	if err := jsonx.Unmarshal(value, &decoded); err != nil {
		t.Fatalf("decode %s: %v", value, err)
	}
	return decoded
}

// Asserts got encodes to the same JSON value as want (a JSON text, or a value to encode).
func assertJSON(t *testing.T, got any, want any, message ...any) {
	t.Helper()
	encode := func(value any) []byte {
		switch value := value.(type) {
		case string:
			return []byte(value)
		case jsontext.Value:
			return value
		}
		encoded, err := jsonx.Marshal(value)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return encoded
	}
	gotJSON, wantJSON := encode(got), encode(want)
	if !reflect.DeepEqual(decodeJSON(t, gotJSON), decodeJSON(t, wantJSON)) {
		t.Fatalf("%s\n got: %s\nwant: %s", fmt.Sprint(message...), gotJSON, wantJSON)
	}
}

// Every event a run emitted, as JSON-ready values.
type recorder struct {
	mu     sync.Mutex
	events []agent.ProviderEvent
}

func (r *recorder) emit(event agent.ProviderEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recorder) all() []agent.ProviderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.ProviderEvent(nil), r.events...)
}

// The events a run emitted as their TS shapes, minus those of the given types.
func (r *recorder) shapes(skip ...string) []map[string]any {
	var shapes []map[string]any
	for _, event := range r.all() {
		shape := eventShape(event)
		if !slicesContains(skip, shape["type"].(string)) {
			shapes = append(shapes, shape)
		}
	}
	return shapes
}

func (r *recorder) ofType(kind string) []map[string]any {
	var shapes []map[string]any
	for _, event := range r.all() {
		if shape := eventShape(event); shape["type"] == kind {
			shapes = append(shapes, shape)
		}
	}
	return shapes
}

func slicesContains(values []string, value string) bool {
	for _, held := range values {
		if held == value {
			return true
		}
	}
	return false
}

// A provider event as the TS ProviderEvent object writes it.
func eventShape(event agent.ProviderEvent) map[string]any {
	switch event := event.(type) {
	case agent.EventAssistant:
		shape := map[string]any{"type": "assistant", "messageId": event.MessageID, "text": event.Text}
		if event.Append {
			shape["append"] = true
		}
		if event.Artifact {
			shape["artifact"] = true
		}
		return shape
	case agent.EventAssistantTail:
		return map[string]any{"type": "assistant-tail", "messageId": event.MessageID, "text": event.Text}
	case agent.EventUsage:
		return map[string]any{"type": "usage", "tokens": event.Tokens, "limit": event.Limit, "model": event.Model}
	case agent.EventSpend:
		return map[string]any{"type": "spend", "tokens": event.Tokens}
	case agent.EventCompactionStatus:
		shape := map[string]any{"type": "compaction-status", "compacting": event.Compacting}
		if event.Error != nil {
			shape["error"] = *event.Error
		}
		return shape
	case agent.EventRetry:
		return map[string]any{"type": "retry", "message": event.Message}
	case agent.EventCompaction:
		return map[string]any{"type": "compaction", "trigger": string(event.Trigger), "preTokens": event.PreTokens}
	case agent.EventTool:
		return map[string]any{"type": "tool", "intent": decodeAny(encodedOf(event.Intent))}
	case agent.EventContinuation:
		return map[string]any{"type": "continuation", "continuation": map[string]any{"provider": event.Continuation.Provider, "value": event.Continuation.Value}}
	case agent.EventContinuationLost:
		return map[string]any{"type": "continuation-lost"}
	case agent.EventSteered:
		return map[string]any{"type": "steered", "messageId": event.MessageID}
	case agent.EventSteerFailed:
		return map[string]any{"type": "steer-failed", "messageId": event.MessageID, "message": event.Message}
	case agent.EventComputerUseSetupRequired:
		return map[string]any{"type": "computer-use.setup-required"}
	}
	return map[string]any{"type": fmt.Sprintf("%T", event)}
}

func encodedOf(value any) []byte {
	data, err := jsonx.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func decodeAny(data []byte) any {
	var decoded any
	if err := jsonx.Unmarshal(data, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

type fakeToolHost struct {
	mu     sync.Mutex
	served []*serving
}

type serving struct {
	token    string
	tools    []agent.BoundTool
	released bool
}

const fakeHostURL = "http://127.0.0.1:1/mcp"

func (host *fakeToolHost) Serve(_ context.Context, tools []agent.BoundTool) (agent.ServedTools, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	held := &serving{token: fmt.Sprintf("token-%d", len(host.served)+1), tools: tools}
	host.served = append(host.served, held)
	return agent.ServedTools{URL: fakeHostURL, Token: held.token, Release: func() {
		host.mu.Lock()
		held.released = true
		host.mu.Unlock()
	}}, nil
}

type harness struct {
	t        *testing.T
	provider *CodexAgentProvider
	host     *fakeToolHost
	mu       sync.Mutex
	clients  []*fakeClient
}

type harnessOptions struct {
	handshake   func(ctx context.Context) error
	idle        time.Duration
	readOrigin  ReadOrigin
	imageOutput ImageOutput
}

// A provider whose app servers are scripted fakes, one per session it opens.
func newHarness(t *testing.T, overrides script, options harnessOptions) *harness {
	h := &harness{t: t, host: &fakeToolHost{}}
	full := defaultScript()
	for method, answer := range overrides {
		full[method] = answer
	}
	readOrigin := options.readOrigin
	if readOrigin == nil {
		readOrigin = noOrigin
	}
	h.provider = NewCodexAgentProvider(CodexProviderOptions{
		Connect: func(command protocol.Command) CodexClient {
			client := newFakeClient(command, full, options.handshake)
			h.mu.Lock()
			h.clients = append(h.clients, client)
			h.mu.Unlock()
			return client
		},
		Host:        h.host,
		IdleTime:    options.idle,
		ReadOrigin:  readOrigin,
		ImageOutput: options.imageOutput,
	})
	t.Cleanup(h.provider.CloseAll)
	return h
}

func (h *harness) all() []*fakeClient {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*fakeClient(nil), h.clients...)
}

func (h *harness) latest() *fakeClient {
	clients := h.all()
	return clients[len(clients)-1]
}

// The client the run just opened, once it exists.
func (h *harness) opened() *fakeClient {
	h.t.Helper()
	eventually(h.t, "no app server was opened", func() bool { return len(h.all()) > 0 })
	return h.latest()
}

type running struct {
	result chan agent.ProviderResult
}

func (r running) wait(t *testing.T) agent.ProviderResult {
	t.Helper()
	select {
	case result := <-r.result:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the run never settled")
		return agent.ProviderResult{}
	}
}

func (r running) settled() bool {
	select {
	case result := <-r.result:
		r.result <- result
		return true
	default:
		return false
	}
}

func (h *harness) execute(input *agent.ProviderRunInput) running {
	results := make(chan agent.ProviderResult, 1)
	go func() {
		result, err := h.provider.Execute(input)
		if err != nil {
			result = agent.ProviderResult{Status: domain.RunStatusFailed, Message: err.Error()}
		}
		results <- result
	}()
	return running{results}
}

// Runs one turn to its completion, delivering the given notifications before the server completes it.
func (h *harness) turn(input *agent.ProviderRunInput, deliver func(client *fakeClient)) (*fakeClient, agent.ProviderResult) {
	h.t.Helper()
	earlier := map[*fakeClient]int{}
	for _, client := range h.all() {
		earlier[client] = len(client.calls("turn/start"))
	}
	run := h.execute(input)
	var client *fakeClient
	eventually(h.t, "turn/start was never sent", func() bool {
		for _, candidate := range h.all() {
			if len(candidate.calls("turn/start")) > earlier[candidate] {
				client = candidate
				return true
			}
		}
		return false
	})
	if deliver != nil {
		deliver(client)
	}
	completeTurn(client, "completed", nil)
	return client, run.wait(h.t)
}

func completeTurn(client *fakeClient, status string, message *string) {
	threadID := "thread-1"
	if calls := client.calls("turn/start"); len(calls) > 0 {
		var started struct {
			ThreadID string `json:"threadId"`
		}
		_ = jsonx.Unmarshal(calls[len(calls)-1], &started)
		threadID = started.ThreadID
	}
	var turnError any
	if message != nil {
		turnError = map[string]any{"message": *message, "codexErrorInfo": nil, "additionalDetails": nil}
	}
	client.notify("turn/completed", map[string]any{
		"threadId": threadID,
		"turn":     map[string]any{"id": "turn-1", "rootTurnId": nil, "items": []any{}, "status": status, "error": turnError, "startedAt": 1, "completedAt": 2, "durationMs": 1000},
	})
}

type agentInput = agent.ProviderRunInput

type inputOption func(input *agent.ProviderRunInput)

// A run's input with the defaults every test starts from.
func input(options ...inputOption) *agent.ProviderRunInput {
	signal, abort := context.WithCancel(context.Background())
	run := &agent.ProviderRunInput{
		Channel:          contracts.RunChannelMain,
		TaskID:           "task-1",
		Title:            "Inspect the app",
		Prompt:           "inspect the app",
		WorkspaceRoot:    "/tmp/project",
		ComputerUse:      domain.ComputerUseRunConfigUnavailable{Message: "test"},
		Policy:           domain.ExecutionPolicyConfirm,
		Engine:           domain.AgentEngineCodex,
		Model:            "gpt-6.2-sol",
		Effort:           domain.AgentEffortHigh,
		Steering:         agent.NewSteerChannel(),
		Signal:           signal,
		Abort:            abort,
		Authorize:        func(context.Context, domain.ToolIntent) agent.ToolDecision { return agent.Allow },
		AskQuestion:      func(context.Context, domain.QuestionRequest) (domain.QuestionAnswers, bool) { return nil, false },
		Emit:             func(agent.ProviderEvent) {},
		ReportWorkflow:   func(contracts.WorkflowReport) {},
		ReportBackground: func(contracts.BackgroundReport) {},
		ReportSubagent:   func(domain.SubagentReport) {},
		ReportGoal:       func(contracts.GoalReport) {},
	}
	for _, option := range options {
		option(run)
	}
	return run
}

func continuing(value string) inputOption {
	return func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: string(domain.AgentEngineCodex), Value: value}
	}
}

func emitting(r *recorder) inputOption {
	return func(input *agent.ProviderRunInput) { input.Emit = r.emit }
}

// The developer instructions a thread is started with for this run.
func developerInstructions(options ...inputOption) string {
	brief := CodexBrief(input(options...))
	text := ""
	for index, instruction := range brief.Instructions {
		if index > 0 {
			text += "\n\n"
		}
		text += instruction
	}
	return text
}

var errScripted = errors.New("scripted failure")
