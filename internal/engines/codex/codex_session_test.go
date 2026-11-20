package codex

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

const (
	threadID = "thread-1"
	turnID   = "turn-1"
)

func startedItem(item string) map[string]any {
	return map[string]any{"item": jsontext.Value(item), "threadId": threadID, "turnId": turnID, "startedAtMs": 1}
}

func completedItem(item string) map[string]any {
	return map[string]any{"item": jsontext.Value(item), "threadId": threadID, "turnId": turnID, "completedAtMs": 2}
}

func commandItem(id, command string) string {
	return `{"type":"commandExecution","id":"` + id + `","pluginId":null,"scriptPath":null,"command":"` + command + `","cwd":"/tmp/project","processId":null,"source":"agent","status":"inProgress","commandActions":[],"aggregatedOutput":null,"exitCode":null,"durationMs":null}`
}

func fileChangeItem(id string, paths ...string) string {
	var changes []string
	for _, path := range paths {
		changes = append(changes, `{"path":"`+path+`","kind":{"type":"update","move_path":null},"diff":"--- `+path+`\n+++ `+path+`\n"}`)
	}
	return `{"type":"fileChange","id":"` + id + `","status":"inProgress","changes":[` + strings.Join(changes, ",") + `]}`
}

func mcpCallItem(id, server, tool, args string) string {
	return `{"type":"mcpToolCall","id":"` + id + `","server":"` + server + `","environmentId":null,"tool":"` + tool + `","status":"inProgress","arguments":` + args + `,"appContext":null,"mcpAppUi":null,"pluginId":null,"readOnlyHint":null,"result":null,"error":null,"durationMs":null}`
}

func agentMessageItem(id, text string) string {
	return `{"type":"agentMessage","moderation":null,"id":"` + id + `","text":` + string(encodedOf(text)) + `,"phase":"final_answer","memoryCitation":null,"delivery":null,"questions":null}`
}

func with(options ...inputOption) *agent.ProviderRunInput { return input(options...) }

func policy(value domain.ExecutionPolicy) inputOption {
	return func(in *agentInput) { in.Policy = value }
}

func prompt(value string) inputOption { return func(in *agentInput) { in.Prompt = value } }

func TestARunOpensOneAppServerSignsInStartsAThreadAndTurnsOnIt(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	client, result := h.turn(with(emitting(events)), nil)
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	if client.command.Cwd != "/tmp/project" || !strings.HasSuffix(client.command.Executable, "codex") {
		t.Fatalf("command = %+v", client.command)
	}
	assertJSON(t, client.command.Args[:3], []string{"app-server", "--listen", "stdio://"})
	methods := client.methods()
	index := func(method string) int {
		for at, sent := range methods {
			if sent == method {
				return at
			}
		}
		return -1
	}
	if methods[0] != "initialize" || index("account/read") <= 0 || index("account/read") > index("thread/start") || index("thread/start") > index("turn/start") {
		t.Fatalf("methods = %v", methods)
	}
	assertJSON(t, client.calls("thread/start"), []any{map[string]any{"cwd": "/tmp/project", "model": "gpt-6.2-sol", "serviceTier": "default", "approvalPolicy": "untrusted", "sandbox": "read-only", "approvalsReviewer": "user", "config": map[string]any{"model_reasoning_effort": "high"}, "developerInstructions": developerInstructions()}})
	assertJSON(t, client.calls("turn/start"), `[{"threadId":"thread-1","input":[{"type":"text","text":"inspect the app","text_elements":[]}],"model":"gpt-6.2-sol","effort":"high","serviceTier":"default","approvalPolicy":"untrusted","approvalsReviewer":"user","sandboxPolicy":{"type":"readOnly","networkAccess":false}}]`)
	assertJSON(t, events.shapes(), `[{"type":"continuation","continuation":{"provider":"codex","value":"thread-1"}}]`)

	again, _ := h.turn(with(prompt("and again"), func(in *agentInput) {
		in.Model = "gpt-6.1-terra"
		in.Effort = domain.AgentEffortLow
		in.Policy = domain.ExecutionPolicyAllowEdits
	}, continuing(threadID), emitting(events)), nil)
	if again != client {
		t.Fatal("the second turn rides the warm session")
	}
	if len(client.calls("thread/start")) != 1 || len(client.calls("thread/resume")) != 0 {
		t.Fatal("the thread is not reopened")
	}
	assertJSON(t, client.calls("turn/start")[1], `{"threadId":"thread-1","input":[{"type":"text","text":"and again","text_elements":[]}],"model":"gpt-6.1-terra","effort":"low","serviceTier":"default","approvalPolicy":"on-request","approvalsReviewer":"user","sandboxPolicy":{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":false}}`, "model, effort, and policy are the turn's")
	if len(events.ofType("continuation")) != 1 {
		t.Fatal("the thread's id is reported once")
	}
	h.provider.CloseAll()
	closedEventually(t, client)
}

func TestABypassTurnDisablesApprovalsAndTheSandbox(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(policy(domain.ExecutionPolicyBypass)), nil)
	assertJSON(t, client.calls("thread/start"), []any{map[string]any{"cwd": "/tmp/project", "model": "gpt-6.2-sol", "serviceTier": "default", "approvalPolicy": "never", "sandbox": "danger-full-access", "approvalsReviewer": "user", "config": map[string]any{"model_reasoning_effort": "high"}, "developerInstructions": developerInstructions()}})
	assertJSON(t, client.calls("turn/start")[0], `{"threadId":"thread-1","input":[{"type":"text","text":"inspect the app","text_elements":[]}],"model":"gpt-6.2-sol","effort":"high","serviceTier":"default","approvalPolicy":"never","approvalsReviewer":"user","sandboxPolicy":{"type":"dangerFullAccess"}}`)
}

func TestAMemberInItsOwnCheckoutCannotWriteThroughTmpIntoTheProjectCheckout(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	member := domain.CoordinationRoleMember
	client, _ := h.turn(with(policy(domain.ExecutionPolicyAllowEdits), func(in *agentInput) {
		in.CoordinationRole = &member
		in.WorkspaceRoot = "/home/me/.aicodingtool/worktrees/repo-1"
		in.ProjectRoot = ptr("/tmp/qa/repo")
	}), nil)
	assertJSON(t, decodeAny(client.calls("turn/start")[0]).(map[string]any)["sandboxPolicy"], `{"type":"workspaceWrite","writableRoots":[],"networkAccess":false,"excludeTmpdirEnvVar":false,"excludeSlashTmp":true}`)
}

type goalRecorder struct {
	mu      sync.Mutex
	reports []contracts.GoalReport
}

func (g *goalRecorder) report(report contracts.GoalReport) {
	g.mu.Lock()
	g.reports = append(g.reports, report)
	g.mu.Unlock()
}

func (g *goalRecorder) all() []contracts.GoalReport {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]contracts.GoalReport(nil), g.reports...)
}

func turnNotification(id, status string) map[string]any {
	return map[string]any{"threadId": threadID, "turn": map[string]any{"id": id, "rootTurnId": nil, "items": []any{}, "status": status, "error": nil, "startedAt": 2, "completedAt": nil, "durationMs": nil}}
}

func TestCodexSetsANativeGoalAndKeepsTheRunThroughItsFollowUpTurns(t *testing.T) {
	goals := &goalRecorder{}
	h := newHarness(t, nil, harnessOptions{})
	run := h.execute(with(prompt("/goal All checks pass"), func(in *agentInput) { in.ReportGoal = goals.report }))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	assertJSON(t, client.calls("thread/goal/set"), `[{"threadId":"thread-1","objective":"All checks pass"}]`)
	assertJSON(t, decodeAny(client.calls("turn/start")[0]).(map[string]any)["input"], `[{"type":"text","text":"All checks pass","text_elements":[]}]`)
	completeTurn(client, "completed", nil)
	sentBy(t, client, "thread/backgroundTerminals/list", 1)
	tick()
	if run.settled() {
		t.Fatal("an active goal owns the next native turn")
	}
	client.notify("turn/started", turnNotification("turn-2", "inProgress"))
	client.notify("thread/goal/cleared", map[string]any{"threadId": threadID})
	client.notify("turn/completed", turnNotification("turn-2", "completed"))
	assertJSON(t, run.wait(t), `{"Status":"succeeded","Message":"","Limit":null}`)
	assertJSON(t, goals.all(), `[{"type":"goal.changed","goal":null},{"type":"goal.changed","goal":{"objective":"All checks pass","status":"active"}},{"type":"goal.changed","goal":null}]`)
}

func TestAThreadTheRunContinuesIsResumedAndASideChatForksItInstead(t *testing.T) {
	settings := func(extra map[string]any) map[string]any {
		base := map[string]any{"cwd": "/tmp/project", "model": "gpt-6.2-sol", "serviceTier": "default", "approvalPolicy": "untrusted", "sandbox": "read-only", "approvalsReviewer": "user", "config": map[string]any{"model_reasoning_effort": "high"}, "developerInstructions": developerInstructions(), "excludeTurns": true}
		for key, value := range extra {
			base[key] = value
		}
		return base
	}
	resumed := newHarness(t, nil, harnessOptions{})
	client, _ := resumed.turn(with(continuing("thread-9")), nil)
	assertJSON(t, client.calls("thread/resume"), []any{settings(map[string]any{"threadId": "thread-9"})})
	if len(client.calls("thread/start")) != 0 {
		t.Fatal("a continued thread is not started anew")
	}

	events := &recorder{}
	forked := newHarness(t, nil, harnessOptions{})
	side := func(in *agentInput) { in.Channel = contracts.RunChannelSide }
	fork, _ := forked.turn(with(side, continuing("thread-9"), func(in *agentInput) { in.ForkContinuation = true }, emitting(events)), nil)
	assertJSON(t, fork.calls("thread/fork"), []any{settings(map[string]any{"threadId": "thread-9", "developerInstructions": developerInstructions() + "\n\n" + agent.SideChatInstructions})})
	assertJSON(t, events.shapes()[0], `{"type":"continuation","continuation":{"provider":"codex","value":"thread-fork"}}`, "the fork's own id is what the side chat keeps")
	assertJSON(t, fork.calls("thread/inject_items"), []any{map[string]any{"threadId": "thread-fork", "items": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": agent.SideChatBoundary}}}}}})
	forked.turn(with(side, continuing("thread-fork"), prompt("okay")), nil)
	if len(fork.calls("thread/inject_items")) != 1 {
		t.Fatal("follow-ups keep the original boundary")
	}

	foreign := newHarness(t, nil, harnessOptions{})
	other, _ := foreign.turn(with(func(in *agentInput) { in.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"} }), nil)
	if len(other.calls("thread/start")) != 1 {
		t.Fatal("another engine's continuation means nothing here")
	}
}

func TestSideChatBoundariesReachFreshAndResumedSessionsWithoutChangingTheRequest(t *testing.T) {
	question := "so you do self review without me putting it in prompt?"
	for _, method := range []string{"thread/start", "thread/resume"} {
		h := newHarness(t, nil, harnessOptions{})
		options := []inputOption{func(in *agentInput) { in.Channel = contracts.RunChannelSide }, prompt(question)}
		if method == "thread/resume" {
			options = append(options, continuing("side-thread"))
		}
		client, _ := h.turn(with(options...), nil)
		settings := decodeAny(client.calls(method)[0]).(map[string]any)
		if settings["developerInstructions"] != developerInstructions()+"\n\n"+agent.SideChatInstructions {
			t.Fatalf("%s instructions = %v", method, settings["developerInstructions"])
		}
		assertJSON(t, decodeAny(client.calls("turn/start")[0]).(map[string]any)["input"], []any{map[string]any{"type": "text", "text": question, "text_elements": []any{}}})
		want := 0
		if method == "thread/start" {
			want = 1
		}
		if len(client.calls("thread/inject_items")) != want {
			t.Fatalf("%s: resuming must not reclassify the side chat's own history", method)
		}
		h.provider.CloseAll()
	}
}

func TestASideChatWaitsForItsHistoryBoundaryAndFailsIfInjectionFails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		release := make(chan error, 1)
		h := newHarness(t, script{"thread/inject_items": func(ctx context.Context, _ jsontext.Value) (any, error) {
			if err := <-release; err != nil {
				return nil, err
			}
			return jsontext.Value(`{}`), nil
		}}, harnessOptions{})
		run := h.execute(with(func(in *agentInput) { in.Channel = contracts.RunChannelSide }, continuing("parent-running"), func(in *agentInput) { in.ForkContinuation = true }, prompt("Why are those tests needed?")))
		client := h.opened()
		sentBy(t, client, "thread/inject_items", 1)
		if len(client.calls("turn/start")) != 0 {
			t.Fatal("the fork cannot answer against unseparated parent history")
		}
		if fail {
			release <- errorText("History boundary unavailable")
			assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start: History boundary unavailable"})
			if len(client.calls("turn/start")) != 0 {
				t.Fatal("no turn starts")
			}
		} else {
			release <- nil
			sentBy(t, client, "turn/start", 1)
			completeTurn(client, "completed", nil)
			assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
		}
		h.provider.CloseAll()
	}
}

type errorText string

func (e errorText) Error() string { return string(e) }

func TestAServerThatDiesWhileResumingFailsTheRunWithoutGivingUpTheContinuation(t *testing.T) {
	events := &recorder{}
	code := 1
	h := newHarness(t, script{"thread/resume": refuse(&protocol.AppServerExited{Exit: protocol.ExitStatus{Code: &code, Stderr: "fatal: rollout store locked"}, Context: "while thread/resume was pending"})}, harnessOptions{})
	result, _ := h.provider.Execute(with(continuing("thread-9"), emitting(events)))
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start: fatal: rollout store locked"})
	if len(events.all()) != 0 {
		t.Fatal("the thread may still be there, so nothing says it is lost")
	}
	closedEventually(t, h.latest())
}

func TestATurnTheServerCompletesBeforeItHasNamedItNeedsNoInterrupt(t *testing.T) {
	name := make(chan struct{})
	h := newHarness(t, script{"turn/start": func(context.Context, jsontext.Value) (any, error) {
		<-name
		return jsontext.Value(`{"turn":{"id":"turn-1"}}`), nil
	}}, harnessOptions{})
	run := h.execute(with())
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	completeTurn(client, "completed", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	close(name)
	tick()
	if len(client.calls("turn/interrupt")) != 0 {
		t.Fatal("a turn already over is not interrupted")
	}
}

func TestAThreadCodexNoLongerHasFailsTheRunAndGivesUpTheContinuation(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, script{"thread/resume": refuse(&protocol.AppServerError{Method: "thread/resume", Code: -32600, Message: "no rollout found for thread id thread-9"})}, harnessOptions{})
	result, _ := h.provider.Execute(with(continuing("thread-9"), emitting(events)))
	if result.Status != domain.RunStatusFailed || !strings.Contains(result.Message, "Codex could not continue this thread") || !strings.Contains(result.Message, "no rollout found for thread id thread-9") || !strings.Contains(result.Message, "Start a new thread") {
		t.Fatalf("result = %+v", result)
	}
	assertJSON(t, events.shapes(), `[{"type":"continuation-lost"}]`)
	client := h.latest()
	if len(client.calls("thread/start")) != 0 {
		t.Fatal("no fresh thread is started in its place")
	}
	closedEventually(t, client)
}

func TestASignedOutCodexFailsTheRunBeforeAnyThreadIsStarted(t *testing.T) {
	h := newHarness(t, script{"account/read": reply(`{"account":null,"requiresOpenaiAuth":true}`)}, harnessOptions{})
	result, _ := h.provider.Execute(with())
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Sign in to Codex to run this thread."})
	if len(h.latest().calls("thread/start")) != 0 {
		t.Fatal("no thread is started")
	}
	closedEventually(t, h.latest())
}

func TestAnAppServerThatCannotStartFailsTheRunInOneLineThatNamesCodex(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{handshake: func(context.Context) error {
		return &protocol.AppServerExited{Exit: protocol.ExitStatus{Stderr: "spawn /app/codex ENOENT\nmore detail"}, Context: "before initialize"}
	}})
	result, _ := h.provider.Execute(with())
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start: spawn /app/codex ENOENT"})

	gone := newHarness(t, nil, harnessOptions{handshake: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }})
	run := gone.execute(with())
	code := 1
	gone.opened().exitWith(protocol.ExitStatus{Code: &code})
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start: exit code 1"})
}

func TestStreamedTextShipsWholeMarkdownBlocksWithTheTailTypedOut(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	full := "## First\n\nParagraph with **bold**.\n\n```ts\nconst x = 1;\n\n```\n"
	h.turn(with(emitting(events)), func(client *fakeClient) {
		client.notify("item/started", startedItem(agentMessageItem("msg-1", "")))
		for _, delta := range []string{"## Fi", "rst\n\nParagraph with **bo", "ld**.\n\n```ts\nconst x = 1;\n\n", "```\n"} {
			client.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "msg-1", "delta": delta})
		}
		client.notify("item/completed", completedItem(agentMessageItem("msg-1", full)))
		client.notify("item/started", startedItem(`{"type":"reasoning","id":"rs-1","summary":[],"content":[]}`))
		client.notify("item/completed", completedItem(`{"type":"reasoning","id":"rs-1","summary":["thinking"],"content":[]}`))
		client.notify("item/completed", completedItem(agentMessageItem("msg-2", "Done.")))
		client.notify("item/completed", completedItem(agentMessageItem("msg-3", "  ")))
	})
	assertJSON(t, events.shapes("continuation"), []any{
		map[string]any{"type": "assistant-tail", "messageId": "msg-1", "text": "## Fi"},
		map[string]any{"type": "assistant", "messageId": "msg-1", "text": "## First\n\n", "append": true},
		map[string]any{"type": "assistant-tail", "messageId": "msg-1", "text": "Paragraph with **bo"},
		map[string]any{"type": "assistant", "messageId": "msg-1", "text": "Paragraph with **bold**.\n\n", "append": true},
		map[string]any{"type": "assistant-tail", "messageId": "msg-1", "text": "```ts\nconst x = 1;\n\n"},
		map[string]any{"type": "assistant", "messageId": "msg-1", "text": "```ts\nconst x = 1;\n\n```\n", "append": true},
		map[string]any{"type": "assistant-tail", "messageId": "msg-1", "text": ""},
		map[string]any{"type": "assistant", "messageId": "msg-2", "text": "Done."},
	})
}

func TestAMessageWhoseLastBlockNeverClosedShipsTheRestWhenTheItemCompletes(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	h.turn(with(emitting(events)), func(client *fakeClient) {
		client.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "msg-1", "delta": "Intro.\n\n```ts\nconst delayed ="})
		client.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "msg-1", "delta": " true;"})
		client.notify("item/completed", completedItem(agentMessageItem("msg-1", "Intro.\n\n```ts\nconst delayed = true;")))
	})
	var texts []any
	for _, event := range events.ofType("assistant") {
		texts = append(texts, event["text"])
	}
	assertJSON(t, texts, []string{"Intro.\n\n", "```ts\nconst delayed = true;"})
}

func TestItemsTheAgentStartsBecomeToolIntents(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	h.turn(with(emitting(events)), func(client *fakeClient) {
		client.notify("item/started", startedItem(commandItem("cmd-1", "ls -la")))
		client.notify("item/started", startedItem(fileChangeItem("patch-1", "/tmp/project/src/app.ts", "/tmp/project/README.md")))
		client.notify("item/started", startedItem(mcpCallItem("mcp-1", "aicodingtool-browser", "browser_open", `{"url":"https://example.com"}`)))
		client.notify("item/started", startedItem(`{"type":"webSearch","id":"search-1","query":"vitest forks pool","action":null,"results":null}`))
		client.notify("item/started", startedItem(`{"type":"plan","id":"plan-1","text":"1. look"}`))
		client.notify("item/started", startedItem(`{"type":"imageView","id":"img-1","path":"/tmp/shot.png"}`))
	})
	var intents []any
	for _, event := range events.ofType("tool") {
		intents = append(intents, event["intent"])
	}
	assertJSON(t, intents, `[
		{"toolId":"cmd-1","name":"command_execution","input":{"command":"ls -la","cwd":"/tmp/project"}},
		{"toolId":"patch-1","name":"file_change","input":{"path":"/tmp/project/src/app.ts","changes":[
			{"path":"/tmp/project/src/app.ts","kind":"update","diff":"--- /tmp/project/src/app.ts\n+++ /tmp/project/src/app.ts\n"},
			{"path":"/tmp/project/README.md","kind":"update","diff":"--- /tmp/project/README.md\n+++ /tmp/project/README.md\n"}]},
			"writePath":"/tmp/project/src/app.ts"},
		{"toolId":"mcp-1","name":"browser_open","input":{"url":"https://example.com"}},
		{"toolId":"search-1","name":"web_search","input":{"query":"vitest forks pool"}}]`)
}

func breakdown(total float64) map[string]any {
	return map[string]any{"totalTokens": total, "inputTokens": total - 200, "cachedInputTokens": 11_008, "cacheWriteInputTokens": 0, "outputTokens": 200, "reasoningOutputTokens": 69}
}

func TestEachModelSendsItsEffortAndReportsContextUsageThroughCompaction(t *testing.T) {
	for _, c := range []struct {
		model  string
		effort domain.AgentEffort
		limit  float64
	}{{"gpt-6.1-terra", domain.AgentEffortHigh, 258_400}, {"gpt-6.2-astra", domain.AgentEffortUltra, 872_000}} {
		events := &recorder{}
		h := newHarness(t, nil, harnessOptions{})
		client, _ := h.turn(with(func(in *agentInput) { in.Model = c.model; in.Effort = c.effort }, emitting(events)), func(client *fakeClient) {
			for _, window := range []any{nil, c.limit} {
				client.notify("thread/tokenUsage/updated", map[string]any{"threadId": threadID, "turnId": turnID, "tokenUsage": map[string]any{"total": breakdown(31_379), "last": breakdown(15_707), "modelContextWindow": window}})
			}
			client.notify("item/started", startedItem(`{"type":"contextCompaction","id":"compact-1"}`))
			client.notify("item/completed", completedItem(`{"type":"contextCompaction","id":"compact-1"}`))
		})
		thread := decodeAny(client.calls("thread/start")[0]).(map[string]any)
		request := decodeAny(client.calls("turn/start")[0]).(map[string]any)
		if thread["model"] != c.model || thread["config"].(map[string]any)["model_reasoning_effort"] != string(c.effort) || request["model"] != c.model || request["effort"] != string(c.effort) {
			t.Fatalf("thread %v, turn %v", thread, request)
		}
		assertJSON(t, events.shapes("continuation"), []any{
			map[string]any{"type": "spend", "tokens": 15_707},
			map[string]any{"type": "usage", "tokens": 15_707, "limit": domain.ContextWindowLimit(domain.BuiltInModels, domain.AgentEngineCodex, c.model), "model": c.model},
			map[string]any{"type": "usage", "tokens": 15_707, "limit": c.limit, "model": c.model},
			map[string]any{"type": "compaction-status", "compacting": true},
			map[string]any{"type": "compaction", "trigger": "auto", "preTokens": 15_707},
			map[string]any{"type": "compaction-status", "compacting": false},
		})
		h.provider.CloseAll()
	}
}

func TestManualCompactionUsesTheThreadOperationAndTheSameVisibleProgress(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	run := h.execute(with(prompt(""), continuing(threadID), func(in *agentInput) { in.Operation = contracts.RunOperationCompact{PreTokens: 125_000} }, emitting(events)))
	client := h.opened()
	sentBy(t, client, "thread/compact/start", 1)
	assertJSON(t, client.calls("thread/compact/start"), `[{"threadId":"thread-1"}]`)
	if len(client.calls("turn/start")) != 0 {
		t.Fatal("compaction is not an empty model turn")
	}
	client.notify("item/started", startedItem(`{"type":"contextCompaction","id":"compact-1"}`))
	last := breakdown(13_779)
	for _, key := range []string{"inputTokens", "cachedInputTokens", "outputTokens", "reasoningOutputTokens"} {
		last[key] = 0
	}
	client.notify("thread/tokenUsage/updated", map[string]any{"threadId": threadID, "turnId": turnID, "tokenUsage": map[string]any{"total": breakdown(900_000), "last": last, "modelContextWindow": 258_400}})
	client.notify("item/completed", completedItem(`{"type":"contextCompaction","id":"compact-1"}`))
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	assertJSON(t, events.shapes("continuation"), []any{
		map[string]any{"type": "compaction-status", "compacting": true},
		map[string]any{"type": "usage", "tokens": 13_779, "limit": 258_400, "model": "gpt-6.2-sol"},
		map[string]any{"type": "compaction", "trigger": "manual", "preTokens": 125_000},
		map[string]any{"type": "compaction-status", "compacting": false},
	})
}

func TestCancellingInterruptsTheTurnTheServerNamedAndSettlesOnceItStopped(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	in := with()
	run := h.execute(in)
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	in.Abort()
	sentBy(t, client, "turn/interrupt", 1)
	assertJSON(t, client.calls("turn/interrupt"), `[{"threadId":"thread-1","turnId":"turn-1"}]`)
	completeTurn(client, "interrupted", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusCancelled})
	if client.isClosed() {
		t.Fatal("a turn that stopped cleanly leaves the session warm")
	}
}

func TestACancelBeforeTheServerNamedTheTurnIsSentAsSoonAsItHas(t *testing.T) {
	name := make(chan struct{})
	h := newHarness(t, script{"turn/start": func(context.Context, jsontext.Value) (any, error) {
		<-name
		return jsontext.Value(`{"turn":{"id":"turn-1"}}`), nil
	}}, harnessOptions{})
	in := with()
	run := h.execute(in)
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	in.Abort()
	tick()
	if len(client.calls("turn/interrupt")) != 0 {
		t.Fatal("nothing to interrupt yet")
	}
	close(name)
	sentBy(t, client, "turn/interrupt", 1)
	completeTurn(client, "interrupted", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusCancelled})
}

func TestSteeredMessagesFoldIntoTheTurnAndOneTheServerRefusesStaysWithTheThread(t *testing.T) {
	events := &recorder{}
	var mu sync.Mutex
	refusing := false
	h := newHarness(t, script{"turn/steer": func(context.Context, jsontext.Value) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if refusing {
			return nil, &protocol.AppServerError{Method: "turn/steer", Code: -32600, Message: "no active turn to steer"}
		}
		return jsontext.Value(`{"turnId":"turn-1"}`), nil
	}}, harnessOptions{})
	steering := agent.NewSteerChannel()
	run := h.execute(with(func(in *agentInput) { in.Steering = steering }, emitting(events)))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	steering.Push(agent.SteerMessage{MessageID: "m-1", Prompt: "check the tests too"})
	sentBy(t, client, "turn/steer", 1)
	assertJSON(t, client.calls("turn/steer"), `[{"threadId":"thread-1","input":[{"type":"text","text":"check the tests too","text_elements":[]}],"expectedTurnId":"turn-1"}]`)
	eventually(t, "never steered", func() bool { return len(events.ofType("steered")) == 1 })
	assertJSON(t, events.ofType("steered"), `[{"type":"steered","messageId":"m-1"}]`)

	mu.Lock()
	refusing = true
	mu.Unlock()
	steering.Push(agent.SteerMessage{MessageID: "m-2", Prompt: "too late"})
	eventually(t, "the refusal was never reported", func() bool { return len(events.ofType("steer-failed")) == 1 })
	if len(events.ofType("steered")) != 1 {
		t.Fatal("an undelivered message is the thread's to send next")
	}
	assertJSON(t, events.ofType("steer-failed"), `[{"type":"steer-failed","messageId":"m-2","message":"Could not steer: no active turn to steer. Your message is still queued for the next turn."}]`)

	mu.Lock()
	refusing = false
	mu.Unlock()
	steering.Push(agent.SteerMessage{MessageID: "m-2", Prompt: "too late"})
	eventually(t, "the retry never steered", func() bool { return len(events.ofType("steered")) == 2 })
	completeTurn(client, "completed", nil)
	steering.Close()
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestATurnTheServerFailsEndsTheRunWithItsReasonAndTheSessionStays(t *testing.T) {
	h := newHarness(t, script{"account/rateLimits/read": reply(`{"rateLimits":` + snapshotJSON(`,"primary":{"usedPercent":100,"windowDurationMins":300,"resetsAt":100}`) + `}`)}, harnessOptions{})
	run := h.execute(with())
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.notify("error", map[string]any{"threadId": threadID, "turnId": turnID, "error": map[string]any{"message": "usage limit reached", "codexErrorInfo": "usageLimitExceeded", "additionalDetails": nil}, "willRetry": false})
	completeTurn(client, "failed", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "usage limit reached", Limit: &domain.UsageLimit{ResetsAt: 100_000, Window: domain.UsageLimitWindowSession}})
	again, _ := h.turn(with(continuing(threadID)), nil)
	if again != client {
		t.Fatal("the session stays for the next run")
	}
}

func TestAnAppServerThatDiesMidTurnFailsTheRunAndEndsTheSession(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	run := h.execute(with())
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.exitWith(protocol.ExitStatus{Signal: "SIGKILL"})
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex stopped: signal SIGKILL"})
	eventually(t, "the session was never let go", func() bool { return h.provider.Pool.LiveSession("task-1") == nil })
	next, _ := h.turn(with(continuing(threadID)), nil)
	if next == client {
		t.Fatal("the next run gets a process of its own")
	}
	if decodeAny(next.calls("thread/resume")[0]).(map[string]any)["threadId"] != threadID {
		t.Fatal("the next run resumes the thread")
	}
}

func TestSessionsArePerThreadAndTheColdestIdleOneGoesWhenThePoolIsFull(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	task := func(id string) inputOption { return func(in *agentInput) { in.TaskID = id } }
	for _, id := range []string{"a", "b", "c"} {
		_, result := h.turn(with(task(id)), nil)
		if result.Status != domain.RunStatusSucceeded {
			t.Fatalf("%s: %+v", id, result)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(h.all()) != 3 {
		t.Fatalf("clients = %d", len(h.all()))
	}
	h.turn(with(task("d")), nil)
	clients := h.all()
	if len(clients) != 4 {
		t.Fatal("thread d opens its own session")
	}
	closedEventually(t, clients[0])
	if clients[1].isClosed() {
		t.Fatal("only the coldest session goes")
	}
	h.turn(with(task("b"), continuing(threadID)), nil)
	if len(h.all()) != 4 {
		t.Fatal("thread b's warm session takes its next turn")
	}
	h.turn(with(task("c"), func(in *agentInput) { in.WorkspaceRoot = "/tmp/elsewhere" }), nil)
	if len(h.all()) != 5 {
		t.Fatal("a run built differently gets a session of its own")
	}
	closedEventually(t, h.all()[2])
	h.provider.CloseAll()
	for _, client := range h.all() {
		closedEventually(t, client)
	}
}

func TestAnIdleSessionIsLetGoAfterTheIdlePeriod(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{idle: 5 * time.Millisecond})
	client, _ := h.turn(with(), nil)
	closedEventually(t, client)
	next, _ := h.turn(with(continuing(threadID)), nil)
	if next == client {
		t.Fatal("a new session opens")
	}
}

type backgroundRecorder struct {
	mu      sync.Mutex
	reports []contracts.BackgroundReport
}

func (b *backgroundRecorder) report(report contracts.BackgroundReport) {
	b.mu.Lock()
	b.reports = append(b.reports, report)
	b.mu.Unlock()
}

func (b *backgroundRecorder) last() []*domain.BackgroundProcess {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.reports) == 0 {
		return nil
	}
	return b.reports[len(b.reports)-1].Processes
}

func TestBackgroundTerminalsStayWithTheirSessionReportToThePanelAndStopThere(t *testing.T) {
	var mu sync.Mutex
	runningTerminal := true
	h := newHarness(t, script{
		"thread/backgroundTerminals/list": func(context.Context, jsontext.Value) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			if runningTerminal {
				return jsontext.Value(`{"data":[{"itemId":"item-1","processId":"process-1","command":"python3 -m http.server 8000","cwd":"/tmp/project","osPid":101,"cpuPercent":0,"rssKb":1024}],"nextCursor":null}`), nil
			}
			return jsontext.Value(`{"data":[],"nextCursor":null}`), nil
		},
		"thread/backgroundTerminals/terminate": func(_ context.Context, params jsontext.Value) (any, error) {
			if decodeAny(params).(map[string]any)["processId"] != "process-1" {
				t.Errorf("terminated %s", params)
			}
			mu.Lock()
			runningTerminal = false
			mu.Unlock()
			return jsontext.Value(`{"terminated":true}`), nil
		},
	}, harnessOptions{idle: 5 * time.Millisecond})
	reports := &backgroundRecorder{}
	background := func(in *agentInput) { in.ReportBackground = reports.report }
	first, _ := h.turn(with(background), nil)
	assertJSON(t, reports.last(), `[{"id":"process-1","kind":"shell","description":"python3 -m http.server 8000"}]`)
	time.Sleep(30 * time.Millisecond)
	if first.isClosed() {
		t.Fatal("a live terminal keeps its app-server session warm")
	}
	second, _ := h.turn(with(continuing(threadID), background), nil)
	if second != first {
		t.Fatal("a later turn keeps the same terminal and session")
	}
	if !h.provider.StopProcess("task-1", "process-1") {
		t.Fatal("the thread's session takes the stop")
	}
	sentBy(t, first, "thread/backgroundTerminals/terminate", 1)
	eventually(t, "the stopped process was never cleared", func() bool { return len(reports.last()) == 0 })
	closedEventually(t, first)
	if h.provider.StopProcess("task-1", "process-1") {
		t.Fatal("a reclaimed session takes no stop")
	}
}

func TestABackgroundTerminalThatExitsByItselfLeavesThePanel(t *testing.T) {
	var mu sync.Mutex
	runningTerminal := true
	h := newHarness(t, script{"thread/backgroundTerminals/list": func(context.Context, jsontext.Value) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		if runningTerminal {
			return jsontext.Value(`{"data":[{"itemId":"item-1","processId":"process-1","command":"npm run dev","cwd":"/tmp/project","osPid":101,"cpuPercent":0,"rssKb":1024}],"nextCursor":null}`), nil
		}
		return jsontext.Value(`{"data":[],"nextCursor":null}`), nil
	}}, harnessOptions{})
	reports := &backgroundRecorder{}
	client, _ := h.turn(with(func(in *agentInput) { in.ReportBackground = reports.report }), nil)
	if len(reports.last()) != 1 {
		t.Fatal("the terminal is reported")
	}
	mu.Lock()
	runningTerminal = false
	mu.Unlock()
	client.notify("item/completed", completedItem(strings.Replace(strings.Replace(commandItem("item-1", "npm run dev"), `"processId":null`, `"processId":"process-1"`, 1), `"exitCode":null`, `"exitCode":0`, 1)))
	eventually(t, "the exited process was never cleared", func() bool { return len(reports.last()) == 0 })
}

func TestARunThatWasAlreadyCancelledNeverReachesTheServer(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	in := with()
	in.Abort()
	result, _ := h.provider.Execute(in)
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusCancelled})
	if len(h.all()) != 0 {
		t.Fatal("no server is opened")
	}
}

func TestAThreadArchivedElsewhereIsUnarchivedOnceBeforeTheRunGivesUp(t *testing.T) {
	var mu sync.Mutex
	archived := true
	h := newHarness(t, script{
		"thread/resume": func(_ context.Context, params jsontext.Value) (any, error) {
			mu.Lock()
			defer mu.Unlock()
			id := decodeAny(params).(map[string]any)["threadId"].(string)
			if archived {
				return nil, &protocol.AppServerError{Method: "thread/resume", Code: -32600, Message: "no rollout found for thread id " + id}
			}
			return jsontext.Value(`{"thread":{"id":"` + id + `"},"model":"gpt-6.2-sol"}`), nil
		},
		"thread/unarchive": func(context.Context, jsontext.Value) (any, error) {
			mu.Lock()
			archived = false
			mu.Unlock()
			return jsontext.Value(`{}`), nil
		},
	}, harnessOptions{})
	client, result := h.turn(with(continuing(threadID)), nil)
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	assertJSON(t, client.calls("thread/unarchive"), `[{"threadId":"thread-1"}]`)
	if len(client.calls("thread/resume")) != 2 {
		t.Fatal("the resume is retried once the thread is back")
	}
}

func TestAThreadThatCannotBeUnarchivedReportsTheLoss(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, script{
		"thread/resume":    refuse(&protocol.AppServerError{Method: "thread/resume", Code: -32600, Message: "thread not found"}),
		"thread/unarchive": refuse(&protocol.AppServerError{Method: "thread/unarchive", Code: -32600, Message: "no rollout found"}),
	}, harnessOptions{})
	result, _ := h.provider.Execute(with(continuing(threadID), emitting(events)))
	if result.Status != domain.RunStatusFailed || !strings.Contains(result.Message, "Codex could not continue this thread") {
		t.Fatalf("result = %+v", result)
	}
	if len(events.ofType("continuation-lost")) != 1 || len(h.latest().calls("thread/resume")) != 1 {
		t.Fatal("the loss is reported once")
	}
}

func fast(on bool) inputOption { return func(in *agentInput) { in.FastMode = &on } }

func TestFastModeFollowsEachTurnAndResetsAWarmSessionToStandard(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(fast(true)), nil)
	if decodeAny(client.calls("thread/start")[0]).(map[string]any)["serviceTier"] != "priority" || decodeAny(client.calls("turn/start")[0]).(map[string]any)["serviceTier"] != "priority" {
		t.Fatal("fast mode asks for priority")
	}
	next, _ := h.turn(with(fast(false), continuing(threadID)), nil)
	if next != client || decodeAny(client.calls("turn/start")[1]).(map[string]any)["serviceTier"] != "default" {
		t.Fatal("standard speed is asked for explicitly")
	}
	for _, on := range []bool{false, true} {
		for _, fork := range []bool{false, true} {
			h := newHarness(t, nil, harnessOptions{})
			client, _ := h.turn(with(fast(on), continuing("thread-9"), func(in *agentInput) { in.ForkContinuation = fork }), nil)
			method := "thread/resume"
			if fork {
				method = "thread/fork"
			}
			want := "default"
			if on {
				want = "priority"
			}
			if got := decodeAny(client.calls(method)[0]).(map[string]any)["serviceTier"]; got != want {
				t.Fatalf("fast %v fork %v: %v", on, fork, got)
			}
			h.provider.CloseAll()
		}
	}
}

func TestAnErrorTheServerWillRetryShowsAsARetry(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	run := h.execute(with(emitting(events)))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	for _, failure := range []map[string]any{
		{"message": "server overloaded", "codexErrorInfo": "serverOverloaded", "additionalDetails": nil},
		{"message": "stream disconnected\ndetails", "codexErrorInfo": map[string]any{"responseStreamDisconnected": map[string]any{"httpStatusCode": nil}}, "additionalDetails": nil},
		{"message": "something odd\nmore", "codexErrorInfo": "other", "additionalDetails": nil},
	} {
		client.notify("error", map[string]any{"threadId": threadID, "turnId": turnID, "error": failure, "willRetry": true})
	}
	completeTurn(client, "completed", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	assertJSON(t, events.ofType("retry"), `[{"type":"retry","message":"Codex is overloaded."},{"type":"retry","message":"Cannot reach Codex."},{"type":"retry","message":"something odd"}]`)
}

func TestASideChatIgnoresForeignConversationEventsWhileItsForkIsOpening(t *testing.T) {
	finish := make(chan struct{})
	h := newHarness(t, script{"thread/fork": func(context.Context, jsontext.Value) (any, error) {
		<-finish
		return jsontext.Value(`{"thread":{"id":"side-thread"}}`), nil
	}}, harnessOptions{})
	events := &recorder{}
	run := h.execute(with(func(in *agentInput) { in.Channel = contracts.RunChannelSide }, continuing("main-thread"), func(in *agentInput) { in.ForkContinuation = true }, emitting(events)))
	client := h.opened()
	sentBy(t, client, "thread/fork", 1)
	item := `{"type":"mcpToolCall","id":"main-tool","server":"aicodingtool","environmentId":null,"tool":"read_thread","arguments":{},"status":"inProgress","appContext":null,"mcpAppUi":null,"pluginId":null,"readOnlyHint":true,"result":null,"error":null,"durationMs":null}`
	client.notify("item/started", map[string]any{"threadId": "main-thread", "turnId": "main-turn", "item": jsontext.Value(item), "startedAtMs": 1})
	client.notify("item/completed", map[string]any{"threadId": "main-thread", "turnId": "main-turn", "item": jsontext.Value(strings.Replace(item, "inProgress", "completed", 1)), "completedAtMs": 2})
	client.notify("turn/completed", map[string]any{"threadId": "main-thread", "turn": map[string]any{"id": "main-turn", "rootTurnId": nil, "status": "completed", "items": []any{}, "error": nil, "startedAt": 1, "completedAt": 2, "durationMs": 1000}})
	tick()
	if len(events.all()) != 0 || run.settled() {
		t.Fatal("the parent's events are not the side chat's")
	}
	close(finish)
	sentBy(t, client, "turn/start", 1)
	completeTurn(client, "completed", nil)
	run.wait(t)
}
