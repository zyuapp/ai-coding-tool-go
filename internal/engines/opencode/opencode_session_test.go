package opencode

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

const testModel = "openrouter/z-ai/glm-5.3-flash"

type fakeHost struct{}

func (fakeHost) Serve(context.Context, []agent.BoundTool) (agent.ServedTools, error) {
	return agent.ServedTools{URL: "http://127.0.0.1:1/mcp", Token: "token-1", Release: func() {}}, nil
}

type noThreads struct{}

func (noThreads) List(context.Context, contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
	return nil, nil
}
func (noThreads) Read(context.Context, string, *float64, *string) (contracts.ThreadTranscript, error) {
	panic("unused")
}
func (noThreads) Wait(context.Context, string, float64) (contracts.ThreadWaitResult, error) {
	panic("unused")
}
func (noThreads) Command(context.Context, contracts.ExternalCommand, *string) (contracts.ThreadCommandResult, error) {
	return contracts.ThreadCommandResult{}, nil
}

type harness struct {
	provider *Provider
	mu       sync.Mutex
	servers  []*fakeServer
}

// A provider whose servers are scripted; prepare readies each before the session sees it.
func newHarness(prepare func(*fakeServer)) *harness {
	h := &harness{}
	h.provider = NewProvider(ProviderOptions{Host: fakeHost{}, LoadingRetry: time.Millisecond, Connect: func(ctx context.Context, options ServerOptions) (Client, error) {
		server := newFakeServer(options)
		if prepare != nil {
			prepare(server)
		}
		h.mu.Lock()
		h.servers = append(h.servers, server)
		h.mu.Unlock()
		return server, nil
	}})
	return h
}

func (h *harness) server() *fakeServer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.servers) == 0 {
		return nil
	}
	return h.servers[len(h.servers)-1]
}

func (h *harness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.servers)
}

type run struct {
	input    *agent.ProviderRunInput
	steering *agent.SteerChannel
	mu       sync.Mutex
	events   []agent.ProviderEvent
	result   chan agent.ProviderResult
}

func newRun(change func(*agent.ProviderRunInput)) *run {
	r := &run{steering: agent.NewSteerChannel(), result: make(chan agent.ProviderResult, 1)}
	signal, abort := context.WithCancel(context.Background())
	r.input = &agent.ProviderRunInput{
		Channel: contracts.RunChannelMain, TaskID: "task-1", Title: "Probe", Prompt: "Reply with PONG.", WorkspaceRoot: "/tmp/project",
		ComputerUse: domain.ComputerUseRunConfigUnavailable{Message: "test"}, Policy: domain.ExecutionPolicyConfirm,
		Engine: domain.AgentEngineOpencode, Model: testModel, Effort: domain.AgentEffortLow,
		Steering: r.steering, Signal: signal, Abort: abort,
		Authorize:   func(context.Context, domain.ToolIntent) agent.ToolDecision { return agent.Allow },
		AskQuestion: func(context.Context, domain.QuestionRequest) (domain.QuestionAnswers, bool) { return nil, false },
		Emit: func(event agent.ProviderEvent) {
			r.mu.Lock()
			r.events = append(r.events, event)
			r.mu.Unlock()
		},
	}
	if change != nil {
		change(r.input)
	}
	return r
}

func (r *run) all() []agent.ProviderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.ProviderEvent(nil), r.events...)
}

func (r *run) execute(provider *Provider) {
	go func() {
		result, err := provider.Execute(r.input)
		if err != nil {
			result = agent.ProviderResult{Status: domain.RunStatusFailed, Message: err.Error()}
		}
		r.result <- result
	}()
}

// The run's result once it has had a moment to settle; false while it has not.
func (r *run) settled() (agent.ProviderResult, bool) {
	time.Sleep(20 * time.Millisecond)
	select {
	case result := <-r.result:
		r.result <- result
		return result, true
	default:
		return agent.ProviderResult{}, false
	}
}

func (r *run) wait(t *testing.T) agent.ProviderResult {
	t.Helper()
	select {
	case result := <-r.result:
		r.result <- result
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("the run never settled")
		return agent.ProviderResult{}
	}
}

func messageSent(server *fakeServer) bool {
	return server != nil && len(server.sent("POST", "/prompt"))+len(server.sent("POST", "/command"))+len(server.sent("POST", "/compact")) > 0
}

// Opens a session and lets it put its first message in the inbox.
func started(t *testing.T, change func(*agent.ProviderRunInput)) (*harness, *run) {
	t.Helper()
	h := newHarness(nil)
	r := newRun(change)
	r.execute(h.provider)
	until(t, func() bool { return messageSent(h.server()) }, "the first message")
	t.Cleanup(h.provider.CloseAll)
	return h, r
}

func expectResult(t *testing.T, got agent.ProviderResult, ok bool, want agent.ProviderResult) {
	t.Helper()
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("result %+v (settled %v), want %+v", got, ok, want)
	}
}

func continuation(value string) func(*agent.ProviderRunInput) {
	return func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: "opencode", Value: value}
	}
}

func TestSessionStartsOnTheRunsSettingsThenStreams(t *testing.T) {
	h, r := started(t, func(input *agent.ProviderRunInput) { input.Threads = noThreads{} })
	server := h.server()
	var create call
	for _, c := range server.allCalls() {
		if c.method == "POST" && c.path == "/api/session" {
			create = c
		}
	}
	body := bodyOf(t, create)
	if _, ok := body["permissions"]; !ok {
		t.Fatal("no permissions")
	}
	delete(body, "permissions")
	sameJSON(t, body, `{"location":{"directory":"/tmp/project"},"model":{"providerID":"openrouter","id":"z-ai/glm-5.3-flash","variant":"low"},"agent":"build","metadata":{"aicodingtool":"task-1"}}`)
	config := server.options.Config.(sessionConfig)
	app := config.MCP["aicodingtool"]
	if app.URL != "http://127.0.0.1:1/mcp" || app.Headers["Authorization"] != "Bearer token-1" || app.Codemode {
		t.Fatalf("app server %+v", app)
	}
	if len(config.Instructions) != 1 || server.options.Cwd != "/tmp/project" {
		t.Fatalf("config %+v", config)
	}
	if got := r.all()[0]; !reflect.DeepEqual(got, agent.EventContinuation{Continuation: domain.Continuation{Provider: "opencode", Value: "ses_new"}}) {
		t.Fatalf("first event %+v", got)
	}
	sameJSON(t, bodyOf(t, server.sent("POST", "/prompt")[0]), `{"text":"Reply with PONG.","delivery":"queue"}`)

	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.text.delta", map[string]any{"sessionID": "ses_new", "assistantMessageID": "a1", "ordinal": 0, "delta": "PO"})
	server.emit("session.text.delta", map[string]any{"sessionID": "ses_new", "assistantMessageID": "a1", "ordinal": 0, "delta": "NG"})
	server.emit("session.text.ended", map[string]any{"sessionID": "ses_new", "assistantMessageID": "a1", "ordinal": 0, "text": "PONG"})
	server.emit("session.step.ended", map[string]any{"sessionID": "ses_new", "tokens": map[string]any{"input": 900, "output": 10, "reasoning": 5, "cache": map[string]any{"read": 80, "write": 5}}})
	server.emit("session.text.delta", map[string]any{"sessionID": "ses_other", "assistantMessageID": "x", "ordinal": 0, "delta": "not ours"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	shown := ""
	var usage, spend []agent.ProviderEvent
	for _, event := range r.all() {
		switch event := event.(type) {
		case agent.EventAssistant:
			if event.MessageID != "a1:0" {
				t.Fatalf("message id %q", event.MessageID)
			}
			shown += event.Text
		case agent.EventUsage:
			usage = append(usage, event)
		case agent.EventSpend:
			spend = append(spend, event)
		}
	}
	if shown != "PONG" {
		t.Fatalf("shown %q", shown)
	}
	if !reflect.DeepEqual(usage, []agent.ProviderEvent{agent.EventUsage{Tokens: 1000, Limit: 1000, Model: testModel}}) || !reflect.DeepEqual(spend, []agent.ProviderEvent{agent.EventSpend{Tokens: 1000}}) {
		t.Fatalf("usage %+v spend %+v", usage, spend)
	}
}

func TestSessionSendsNoEffortTheModelOffersNoVariantFor(t *testing.T) {
	h, _ := started(t, func(input *agent.ProviderRunInput) { input.Effort = domain.AgentEffortMedium })
	for _, c := range h.server().allCalls() {
		if c.method == "POST" && c.path == "/api/session" {
			sameJSON(t, bodyOf(t, c)["model"], `{"providerID":"openrouter","id":"z-ai/glm-5.3-flash"}`)
			return
		}
	}
	t.Fatal("no session created")
}

func hasEvent(events []agent.ProviderEvent, want agent.ProviderEvent) bool {
	for _, event := range events {
		if reflect.DeepEqual(event, want) {
			return true
		}
	}
	return false
}

func TestSessionAnswersASteeredMessageBeforeItSettles(t *testing.T) {
	h, r := started(t, nil)
	server := h.server()
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	r.steering.Push(agent.SteerMessage{MessageID: "steer-1", Prompt: "And say BANANA."})
	until(t, func() bool { return len(server.sent("POST", "/prompt")) == 2 }, "the steered message")
	sameJSON(t, bodyOf(t, server.sent("POST", "/prompt")[1]), `{"text":"And say BANANA.","delivery":"steer"}`)
	until(t, func() bool { return hasEvent(r.all(), agent.EventSteered{MessageID: "steer-1"}) }, "the steered event")
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	if _, ok := r.settled(); ok {
		t.Fatal("the steered message is still waiting")
	}
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_2"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestSessionDoesNotWaitOnAMessageTakenBeforeTheSendAnswers(t *testing.T) {
	h := newHarness(func(server *fakeServer) {
		server.after = func(server *fakeServer, method, path string) {
			if strings.HasSuffix(path, "/prompt") {
				server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
				server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
			}
		}
	})
	defer h.provider.CloseAll()
	r := newRun(nil)
	r.execute(h.provider)
	if got := r.wait(t); got.Status != domain.RunStatusSucceeded {
		t.Fatalf("result %+v", got)
	}
}

func TestSessionShowsToolCallsAndPutsApprovalsToTheRun(t *testing.T) {
	var mu sync.Mutex
	var asked []domain.ToolIntent
	answer := agent.Allow
	h, r := started(t, func(input *agent.ProviderRunInput) {
		input.Authorize = func(_ context.Context, intent domain.ToolIntent) agent.ToolDecision {
			mu.Lock()
			defer mu.Unlock()
			asked = append(asked, intent)
			return answer
		}
	})
	server := h.server()
	server.emit("session.tool.input.started", map[string]any{"sessionID": "ses_new", "id": "call-1", "name": "write"})
	server.emit("session.tool.called", map[string]any{"sessionID": "ses_new", "id": "call-1", "input": map[string]any{"path": "notes.md", "content": "hi"}})
	writePath := "/tmp/project/notes.md"
	intent := domain.ToolIntent{ToolID: "call-1", Name: "write", Input: encode(map[string]any{"path": "notes.md", "content": "hi"}), WritePath: &writePath}
	if !hasEvent(r.all(), agent.EventTool{Intent: intent}) {
		t.Fatalf("tool event missing: %+v", r.all())
	}
	server.emit("permission.asked", map[string]any{"id": "per_1", "sessionID": "ses_new", "action": "edit", "resources": []string{"notes.md"}, "source": map[string]any{"type": "tool", "id": "call-1"}})
	until(t, func() bool { return len(server.sent("POST", "/reply")) == 1 }, "the reply")
	mu.Lock()
	if !reflect.DeepEqual(asked, []domain.ToolIntent{intent}) {
		t.Fatalf("asked %+v", asked)
	}
	answer = agent.Deny
	mu.Unlock()
	reply := server.sent("POST", "/reply")[0]
	if reply.path != "/api/session/ses_new/permission/per_1/reply" {
		t.Fatal(reply.path)
	}
	sameJSON(t, bodyOf(t, reply), `{"decision":"once"}`)
	server.emit("session.tool.input.started", map[string]any{"sessionID": "ses_new", "id": "call-2", "name": "aicodingtool_start_thread"})
	server.emit("session.tool.called", map[string]any{"sessionID": "ses_new", "id": "call-2", "input": map[string]any{"prompt": "go"}})
	server.emit("permission.asked", map[string]any{"id": "per_2", "sessionID": "ses_new", "action": "aicodingtool_start_thread", "resources": []string{"*"}, "source": map[string]any{"type": "tool", "id": "call-2"}})
	until(t, func() bool { return len(server.sent("POST", "/reply")) == 2 }, "the second reply")
	sameJSON(t, bodyOf(t, server.sent("POST", "/reply")[1]), `{"decision":"reject","message":"The user denied this action."}`)
	mu.Lock()
	defer mu.Unlock()
	if asked[1].Name != "start_thread" {
		t.Fatalf("the app's own tools are named as the app names them: %q", asked[1].Name)
	}
}

func TestSessionRejectsAWordedDenialWithItsReason(t *testing.T) {
	h, _ := started(t, func(input *agent.ProviderRunInput) {
		input.Authorize = func(context.Context, domain.ToolIntent) agent.ToolDecision {
			return agent.DenyWith("Nobody is watching this scheduled run.")
		}
	})
	server := h.server()
	server.emit("permission.asked", map[string]any{"id": "per_r", "sessionID": "ses_new", "action": "shell", "resources": []string{"rm -rf build"}})
	until(t, func() bool { return len(server.sent("POST", "/reply")) == 1 }, "the reply")
	sameJSON(t, bodyOf(t, server.sent("POST", "/reply")[0]), `{"decision":"reject","message":"Nobody is watching this scheduled run."}`)
}

func TestSessionAnswersForSubagentsAndLeavesStrangersAlone(t *testing.T) {
	h, _ := started(t, nil)
	server := h.server()
	server.emit("session.created", map[string]any{"sessionID": "ses_child", "parentID": "ses_new"})
	server.emit("permission.asked", map[string]any{"id": "per_c", "sessionID": "ses_child", "action": "shell", "resources": []string{"ls"}})
	server.emit("permission.asked", map[string]any{"id": "per_x", "sessionID": "ses_stranger", "action": "shell", "resources": []string{"ls"}})
	until(t, func() bool { return len(server.sent("POST", "/reply")) == 1 }, "the child's reply")
	time.Sleep(10 * time.Millisecond)
	replies := server.sent("POST", "/reply")
	if len(replies) != 1 || replies[0].path != "/api/session/ses_child/permission/per_c/reply" {
		t.Fatalf("replies %+v", replies)
	}
}

func TestSessionAsksFormsAsQuestionsAndAnswersInEachFieldsType(t *testing.T) {
	formJSON := map[string]any{
		"id": "frm_1", "sessionID": "ses_new", "title": "Questions",
		"fields": []any{
			map[string]any{"key": "color", "type": "string", "title": "Color", "description": "Which color?", "options": []any{map[string]string{"value": "r", "label": "Red"}, map[string]string{"value": "b", "label": "Blue"}}},
			map[string]any{"key": "sure", "type": "boolean", "title": "Sure?"},
			map[string]any{"key": "count", "type": "integer", "title": "How many?"},
			map[string]any{"key": "langs", "type": "multiselect", "title": "Languages", "options": []any{map[string]string{"value": "ts", "label": "TypeScript"}, map[string]string{"value": "go", "label": "Go"}}},
		},
	}
	var form FormInfo
	if err := decodeInto(encode(formJSON), &form); err != nil {
		t.Fatal(err)
	}
	request := QuestionsOf(&form)
	sameJSON(t, request.Questions[0], `{"id":"color","header":"Color","question":"Which color?","options":[{"label":"Red","description":""},{"label":"Blue","description":""}]}`)
	if labels := []string{request.Questions[1].Options[0].Label, request.Questions[1].Options[1].Label}; !reflect.DeepEqual(labels, []string{"Yes", "No"}) {
		t.Fatal(labels)
	}
	sameJSON(t, FormAnswer(&form, domain.QuestionAnswers{"color": "Blue", "sure": "Yes", "count": "3", "langs": "TypeScript, Go"}), `{"color":"b","sure":true,"count":3,"langs":["ts","go"]}`)
	if QuestionsOf(&FormInfo{ID: "frm_1", SessionID: "ses_new", Title: "Questions", Fields: []*FormField{{Key: "link", Type: "external"}}}) != nil {
		t.Fatal("a link nobody can answer in chat is not asked")
	}

	var mu sync.Mutex
	answers := domain.QuestionAnswers{"color": "Red", "sure": "No", "count": "1", "langs": "Go"}
	h, _ := started(t, func(input *agent.ProviderRunInput) {
		input.AskQuestion = func(context.Context, domain.QuestionRequest) (domain.QuestionAnswers, bool) {
			mu.Lock()
			defer mu.Unlock()
			return answers, answers != nil
		}
	})
	server := h.server()
	server.emit("form.created", map[string]any{"form": formJSON})
	until(t, func() bool { return len(server.sent("POST", "/form/frm_1/reply")) == 1 }, "the answer")
	sameJSON(t, bodyOf(t, server.sent("POST", "/form/frm_1/reply")[0]), `{"answer":{"color":"r","sure":false,"count":1,"langs":["go"]}}`)
	mu.Lock()
	answers = nil
	mu.Unlock()
	second := map[string]any{}
	for key, value := range formJSON {
		second[key] = value
	}
	second["id"] = "frm_2"
	server.emit("form.created", map[string]any{"form": second})
	until(t, func() bool { return len(server.sent("DELETE", "/form/frm_2")) == 1 }, "the withdrawal")
}

func TestSessionCancelledRunInterruptsAndTheInterruptionEndsIt(t *testing.T) {
	h, r := started(t, nil)
	server := h.server()
	r.input.Abort()
	until(t, func() bool { return len(server.sent("POST", "/interrupt")) == 1 }, "the interrupt")
	server.emit("session.execution.interrupted", map[string]any{"sessionID": "ses_new"})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusCancelled})
}

func TestSessionFailedExecutionFailsTheRun(t *testing.T) {
	h, r := started(t, nil)
	server := h.server()
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.execution.failed", map[string]any{"sessionID": "ses_new", "error": map[string]any{"message": "Provider rejected the key"}})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Provider rejected the key"})
}

func TestSessionCompactionSettlesWhenItEnds(t *testing.T) {
	h, r := started(t, func(input *agent.ProviderRunInput) {
		input.Prompt = ""
		input.Operation = contracts.RunOperationCompact{PreTokens: 4000}
		continuation("ses_old")(input)
	})
	server := h.server()
	if got := server.sent("POST", "/compact")[0].path; got != "/api/session/ses_old/compact" {
		t.Fatal(got)
	}
	server.emit("session.compaction.started", map[string]any{"sessionID": "ses_old"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_old"})
	if _, ok := r.settled(); ok {
		t.Fatal("an execution ending is not the compaction ending")
	}
	server.emit("session.compaction.ended", map[string]any{"sessionID": "ses_old"})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	var compactions []agent.ProviderEvent
	for _, event := range r.all() {
		switch event.(type) {
		case agent.EventCompactionStatus, agent.EventCompaction:
			compactions = append(compactions, event)
		}
	}
	want := []agent.ProviderEvent{
		agent.EventCompactionStatus{Compacting: true},
		agent.EventCompaction{Trigger: contracts.RunEventContextCompactedTriggerManual, PreTokens: 4000},
		agent.EventCompactionStatus{Compacting: false},
	}
	if !reflect.DeepEqual(compactions, want) {
		t.Fatalf("compactions %+v", compactions)
	}
}

func createdSession(server *fakeServer) bool {
	for _, c := range server.allCalls() {
		if c.method == "POST" && c.path == "/api/session" {
			return true
		}
	}
	return false
}

func TestSessionContinuesForksAndSaysWhenTheServerLostOne(t *testing.T) {
	resumed, _ := started(t, continuation("ses_old"))
	if createdSession(resumed.server()) {
		t.Fatal("a resumed session was created")
	}
	if got := resumed.server().sent("POST", "/prompt")[0].path; got != "/api/session/ses_old/prompt" {
		t.Fatal(got)
	}

	forked, forkedRun := started(t, func(input *agent.ProviderRunInput) {
		continuation("ses_old")(input)
		input.ForkContinuation = true
	})
	if got := forkedRun.all()[0]; !reflect.DeepEqual(got, agent.EventContinuation{Continuation: domain.Continuation{Provider: "opencode", Value: "ses_fork"}}) {
		t.Fatalf("first event %+v", got)
	}
	if got := forked.server().sent("POST", "/prompt")[0].path; got != "/api/session/ses_fork/prompt" {
		t.Fatal(got)
	}

	lost := newHarness(func(server *fakeServer) { server.missing["ses_gone"] = true })
	defer lost.provider.CloseAll()
	r := newRun(continuation("ses_gone"))
	r.execute(lost.provider)
	result := r.wait(t)
	if result.Status != domain.RunStatusFailed || !strings.Contains(result.Message, "could not continue this session") {
		t.Fatalf("result %+v", result)
	}
	if !hasEvent(r.all(), agent.EventContinuationLost{}) {
		t.Fatal("no continuation-lost")
	}
}

func TestSessionSideChatStartsBehindTheBoundary(t *testing.T) {
	h, _ := started(t, func(input *agent.ProviderRunInput) { input.Channel = contracts.RunChannelSide })
	boundary := h.server().sent("POST", "/synthetic")[0]
	if boundary.path != "/api/session/ses_new/synthetic" {
		t.Fatal(boundary.path)
	}
	if bodyOf(t, boundary)["resume"] != false {
		t.Fatal("resumed")
	}
}

func TestSessionWarmIsMovedOnlyOnWhatChanged(t *testing.T) {
	h := newHarness(nil)
	defer h.provider.CloseAll()
	first := newRun(nil)
	first.execute(h.provider)
	until(t, func() bool { return h.server() != nil && len(h.server().sent("POST", "/prompt")) == 1 }, "the first prompt")
	server := h.server()
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	first.wait(t)
	second := newRun(func(input *agent.ProviderRunInput) {
		continuation("ses_new")(input)
		input.Effort = domain.AgentEffortHigh
		input.Policy = domain.ExecutionPolicyBypass
	})
	second.execute(h.provider)
	until(t, func() bool { return len(server.sent("POST", "/prompt")) == 2 }, "the second prompt")
	models := server.sent("POST", "/model")
	sameJSON(t, bodyOf(t, models[len(models)-1]), `{"model":{"providerID":"openrouter","id":"z-ai/glm-5.3-flash","variant":"high"}}`)
	if got := len(server.sent("POST", "/agent")); got != 1 {
		t.Fatalf("the agent was set once and the mode keeps it: %d", got)
	}
	patches := server.sent("PATCH", "/ses_new")
	sameJSON(t, bodyOf(t, patches[len(patches)-1])["permissions"].([]any)[0], `{"action":"*","resource":"*","effect":"allow"}`)
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_2"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	if got := second.wait(t); got.Status != domain.RunStatusSucceeded {
		t.Fatalf("second %+v", got)
	}
	plan := newRun(func(input *agent.ProviderRunInput) {
		continuation("ses_new")(input)
		input.Effort = domain.AgentEffortHigh
		input.Policy = domain.ExecutionPolicyPlan
	})
	plan.execute(h.provider)
	until(t, func() bool { return len(server.sent("POST", "/prompt")) == 3 }, "the third prompt")
	agents := server.sent("POST", "/agent")
	sameJSON(t, bodyOf(t, agents[len(agents)-1]), `{"agent":"plan"}`)
	if h.count() != 1 {
		t.Fatalf("servers %d", h.count())
	}
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_3"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	plan.wait(t)
}

func TestSessionSlashCommandsAndSkills(t *testing.T) {
	command, _ := started(t, func(input *agent.ProviderRunInput) { input.Prompt = "/init add tests" })
	sameJSON(t, bodyOf(t, command.server().sent("POST", "/command")[0]), `{"name":"init","text":"add tests","delivery":"queue"}`)
	skill, _ := started(t, func(input *agent.ProviderRunInput) { input.Prompt = "/review-thread opus high" })
	sameJSON(t, bodyOf(t, skill.server().sent("POST", "/prompt")[0]), `{"text":"/review-thread opus high","skills":[{"id":"review-thread"}],"delivery":"queue"}`)
	path, _ := started(t, func(input *agent.ProviderRunInput) { input.Prompt = "Read /review-thread/SKILL.md" })
	sameJSON(t, bodyOf(t, path.server().sent("POST", "/prompt")[0]), `{"text":"Read /review-thread/SKILL.md","delivery":"queue"}`)
}

func TestSessionClosedWhileItsServerStartsClosesThatServer(t *testing.T) {
	up := make(chan struct{})
	var mu sync.Mutex
	var servers []*fakeServer
	provider := NewProvider(ProviderOptions{Host: fakeHost{}, Connect: func(ctx context.Context, options ServerOptions) (Client, error) {
		<-up
		server := newFakeServer(options)
		mu.Lock()
		servers = append(servers, server)
		mu.Unlock()
		return server, nil
	}})
	r := newRun(nil)
	r.execute(provider)
	time.Sleep(20 * time.Millisecond)
	provider.CloseAll()
	close(up)
	r.wait(t)
	until(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(servers) == 1 && servers[0].isClosed()
	}, "the late server closing")
}

func TestSessionWithAnUnlistedWindowReportsSpendAndNoUsage(t *testing.T) {
	h, r := started(t, func(input *agent.ProviderRunInput) { input.Model = "openrouter/unlisted" })
	server := h.server()
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.step.ended", map[string]any{"sessionID": "ses_new", "tokens": map[string]any{"input": 900, "output": 10, "reasoning": 5, "cache": map[string]any{"read": 80, "write": 5}}})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	if !hasEvent(r.all(), agent.EventSpend{Tokens: 1000}) {
		t.Fatal("no spend")
	}
	for _, event := range r.all() {
		if _, ok := event.(agent.EventUsage); ok {
			t.Fatal("usage against a window of nothing")
		}
	}
}

func TestSessionServerDyingMidTurnFailsTheRun(t *testing.T) {
	h, r := started(t, nil)
	h.server().die("panic: out of memory")
	result, ok := r.settled()
	expectResult(t, result, ok, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "OpenCode stopped: panic: out of memory"})
	again, _ := started(t, nil)
	again.provider.CloseAll()
	until(t, again.server().isClosed, "every server stopping")
}

func TestSessionMovedIntoAnotherCheckout(t *testing.T) {
	same, _ := started(t, continuation("ses_old"))
	if got := len(same.server().sent("POST", "/move")); got != 0 {
		t.Fatalf("moves %d", got)
	}
	worktree, _ := started(t, func(input *agent.ProviderRunInput) {
		continuation("ses_old")(input)
		input.ForkContinuation = true
		input.WorkspaceRoot = "/tmp/project-wt"
	})
	moves := worktree.server().sent("POST", "/move")
	if len(moves) != 1 || moves[0].path != "/api/session/ses_fork/move" {
		t.Fatalf("moves %+v", moves)
	}
	sameJSON(t, bodyOf(t, moves[0]), `{"directory":"/tmp/project-wt"}`)
	if got := worktree.server().sent("POST", "/prompt")[0].path; got != "/api/session/ses_fork/prompt" {
		t.Fatal(got)
	}
}

func TestSessionForkOfAnEmptySessionStartsAfresh(t *testing.T) {
	h := newHarness(func(server *fakeServer) { server.empty["ses_blank"] = true })
	defer h.provider.CloseAll()
	r := newRun(func(input *agent.ProviderRunInput) {
		continuation("ses_blank")(input)
		input.ForkContinuation = true
	})
	r.execute(h.provider)
	until(t, func() bool { return h.server() != nil && len(h.server().sent("POST", "/prompt")) == 1 }, "the prompt")
	if !createdSession(h.server()) {
		t.Fatal("no fresh session")
	}
	if got := r.all()[0]; !reflect.DeepEqual(got, agent.EventContinuation{Continuation: domain.Continuation{Provider: "opencode", Value: "ses_new"}}) {
		t.Fatalf("first event %+v", got)
	}
}

func TestSessionWhoseEventsStopFailsAndIsLetGo(t *testing.T) {
	h, r := started(t, nil)
	server := h.server()
	server.emit("aicodingtool.feed.ended", map[string]any{})
	if got := r.wait(t); !reflect.DeepEqual(got, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "OpenCode stopped sending events"}) {
		t.Fatalf("result %+v", got)
	}
	until(t, server.isClosed, "the server closing")
	next := newRun(nil)
	next.execute(h.provider)
	until(t, func() bool {
		for _, event := range next.all() {
			if _, ok := event.(agent.EventContinuation); ok {
				return true
			}
		}
		return false
	}, "a new session")
	if h.count() != 2 {
		t.Fatalf("servers %d", h.count())
	}
}

func TestSessionFeedLostToADeadServerReportsWhatItDiedOf(t *testing.T) {
	h, r := started(t, nil)
	h.server().emit("aicodingtool.feed.ended", map[string]any{})
	h.server().die("Error: invalid config: mcp.app.url is not a URL")
	if got := r.wait(t); !reflect.DeepEqual(got, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "OpenCode stopped: Error: invalid config: mcp.app.url is not a URL"}) {
		t.Fatalf("result %+v", got)
	}
}

func TestArchivingFilesOnTheThreadsOwnServerOrOneStartedForIt(t *testing.T) {
	warm, r := started(t, nil)
	server := warm.server()
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	r.wait(t)
	opened := warm.count()
	if !warm.provider.ArchiveThread("task-1", &domain.Continuation{Provider: "opencode", Value: "ses_new"}, true) {
		t.Fatal("not filed")
	}
	until(t, server.isClosed, "the warm session going")
	if warm.count() != opened {
		t.Fatal("a server was started to file it")
	}
	patches := server.sent("PATCH", "/ses_new")
	sameJSON(t, bodyOf(t, patches[len(patches)-1]), `{"archived":true}`)

	cold := newHarness(nil)
	defer cold.provider.CloseAll()
	if !cold.provider.ArchiveThread("task-9", &domain.Continuation{Provider: "opencode", Value: "ses_old"}, false) {
		t.Fatal("not filed")
	}
	until(t, func() bool { return cold.count() == 1 && cold.server().isClosed() }, "a server started and stopped for it")
	sameJSON(t, bodyOf(t, cold.server().sent("PATCH", "/ses_old")[0]), `{"archived":false}`)
	if cold.provider.ArchiveThread("task-9", &domain.Continuation{Provider: "codex", Value: "thread-1"}, true) {
		t.Fatal("another engine's thread is not this one's to file")
	}
	if cold.provider.ArchiveThread("task-8", nil, true) {
		t.Fatal("a thread with no session has nothing to file")
	}
}

func TestARunWaitsForItsThreadsFilingToLand(t *testing.T) {
	h := newHarness(nil)
	defer h.provider.CloseAll()
	h.provider.ArchiveThread("task-1", &domain.Continuation{Provider: "opencode", Value: "ses_old"}, false)
	r := newRun(continuation("ses_old"))
	r.execute(h.provider)
	until(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.servers) == 2 && len(h.servers[1].sent("POST", "/prompt")) == 1
	}, "the run's prompt")
	if !h.servers[0].isClosed() {
		t.Fatal("the filing's server was not done before the run's started")
	}
}

func TestAThreadArchivedBeforeItsSessionWasSavedIsFiledUnderTheSessionItHas(t *testing.T) {
	h, r := started(t, nil)
	server := h.server()
	if !h.provider.ArchiveThread("task-1", nil, true) {
		t.Fatal("not filed")
	}
	until(t, func() bool {
		for _, c := range server.sent("PATCH", "/ses_new") {
			if bodyOf(t, c)["archived"] == true {
				return true
			}
		}
		return false
	}, "the filing")
	if server.isClosed() {
		t.Fatal("the session waits out the turn it is answering")
	}
	server.emit("session.inbox.delivered", map[string]any{"sessionID": "ses_new", "inboxID": "msg_1"})
	server.emit("session.execution.succeeded", map[string]any{"sessionID": "ses_new"})
	r.wait(t)
	until(t, server.isClosed, "the warm session going once its turn answered")
}
