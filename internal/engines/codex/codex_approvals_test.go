package codex

import (
	"context"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
)

type asked struct {
	mu      sync.Mutex
	intents []domain.ToolIntent
}

func (a *asked) all() []domain.ToolIntent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.ToolIntent(nil), a.intents...)
}

type askingSession struct {
	h      *harness
	client *fakeClient
	asked  *asked
	run    running
}

// A turn going on a fresh session whose user answers each approval with decide.
func asking(t *testing.T, decide func(domain.ToolIntent) agent.ToolDecision, options ...inputOption) *askingSession {
	t.Helper()
	record := &asked{}
	h := newHarness(t, nil, harnessOptions{})
	all := append([]inputOption{func(in *agentInput) {
		in.Authorize = func(_ context.Context, intent domain.ToolIntent) agent.ToolDecision {
			record.mu.Lock()
			record.intents = append(record.intents, intent)
			record.mu.Unlock()
			return decide(intent)
		}
	}}, options...)
	run := h.execute(with(all...))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	return &askingSession{h, client, record, run}
}

func (s *askingSession) end(t *testing.T) agent.ProviderResult {
	completeTurn(s.client, "completed", nil)
	result := s.run.wait(t)
	s.h.provider.CloseAll()
	return result
}

func allow(domain.ToolIntent) agent.ToolDecision { return agent.Allow }
func deny(domain.ToolIntent) agent.ToolDecision  { return agent.Deny }

func commandApproval(itemID, command string, extra map[string]any) map[string]any {
	params := map[string]any{"kind": "command", "threadId": threadID, "turnId": turnID, "itemId": itemID, "startedAtMs": 1, "environmentId": nil, "command": command, "cwd": "/tmp/project"}
	for key, value := range extra {
		params[key] = value
	}
	return params
}

func intentInputs(intents []domain.ToolIntent) []any {
	inputs := []any{}
	for _, intent := range intents {
		inputs = append(inputs, decodeAny(intent.Input))
	}
	return inputs
}

func TestACommandApprovalAsksTheRunsUserAndAcceptsOrDeclinesOnTheirWord(t *testing.T) {
	allowed := asking(t, allow)
	allowed.client.notify("item/started", startedItem(commandItem("cmd-1", "npm test")))
	assertJSON(t, allowed.client.ask(t, "item/commandExecution/requestApproval", commandApproval("cmd-1", "npm test", map[string]any{"reason": "The sandbox blocked it."})), `{"result":{"decision":"accept"}}`)
	assertJSON(t, allowed.asked.all(), `[{"toolId":"cmd-1","name":"command_execution","input":{"command":"npm test","cwd":"/tmp/project","reason":"The sandbox blocked it."}}]`)
	allowed.end(t)

	denied := asking(t, deny)
	assertJSON(t, denied.client.ask(t, "item/commandExecution/requestApproval", commandApproval("cmd-2", "rm -rf build", nil)), `{"result":{"decision":"decline"}}`)
	assertJSON(t, intentInputs(denied.asked.all()), `[{"command":"rm -rf build","cwd":"/tmp/project"}]`)
	denied.end(t)
}

func TestADenialThatSaysWhyIsSteeredIntoTheTurnWhileAPlainOneSaysNothingMore(t *testing.T) {
	reason := "Nobody is watching this scheduled run."
	explained := asking(t, func(domain.ToolIntent) agent.ToolDecision { return agent.DenyWith(reason) })
	assertJSON(t, explained.client.ask(t, "item/commandExecution/requestApproval", commandApproval("cmd-3", "rm -rf build", nil)), `{"result":{"decision":"decline"}}`)
	sentBy(t, explained.client, "turn/steer", 1)
	assertJSON(t, explained.client.calls("turn/steer"), []any{map[string]any{"threadId": threadID, "input": []any{map[string]any{"type": "text", "text": reason, "text_elements": []any{}}}, "expectedTurnId": turnID}})
	explained.end(t)

	plain := asking(t, deny)
	plain.client.ask(t, "item/fileChange/requestApproval", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "patch-2", "startedAtMs": 1})
	tick()
	if len(plain.client.calls("turn/steer")) != 0 {
		t.Fatal("a plain denial steers nothing")
	}
	plain.end(t)
}

func TestABypassRunAcceptsAnUnexpectedApprovalWithoutAsking(t *testing.T) {
	session := asking(t, deny, policy(domain.ExecutionPolicyBypass))
	assertJSON(t, session.client.ask(t, "item/commandExecution/requestApproval", commandApproval("cmd-bypass", "npm test", nil)), `{"result":{"decision":"accept"}}`)
	if len(session.asked.all()) != 0 {
		t.Fatal("nobody is asked")
	}
	session.end(t)
}

func TestAFileChangeApprovalIsDescribedByThePatchTheAgentAlreadyStarted(t *testing.T) {
	session := asking(t, func(intent domain.ToolIntent) agent.ToolDecision {
		if intent.WritePath != nil && *intent.WritePath == "/tmp/project/src/app.ts" {
			return agent.Allow
		}
		return agent.Deny
	})
	session.client.notify("item/started", startedItem(fileChangeItem("patch-1", "/tmp/project/src/app.ts")))
	assertJSON(t, session.client.ask(t, "item/fileChange/requestApproval", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "patch-1", "startedAtMs": 1, "reason": "Outside the sandbox."}), `{"result":{"decision":"accept"}}`)
	first := session.asked.all()[0]
	assertJSON(t, first.Input, `{"path":"/tmp/project/src/app.ts","changes":[{"path":"/tmp/project/src/app.ts","kind":"update","diff":"--- /tmp/project/src/app.ts\n+++ /tmp/project/src/app.ts\n"}],"reason":"Outside the sandbox."}`)
	assertJSON(t, session.client.ask(t, "item/fileChange/requestApproval", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "patch-unseen", "startedAtMs": 1}), `{"result":{"decision":"decline"}}`, "a patch the session never saw carries no path")
	session.end(t)
}

func elicitation(server, tool string, params map[string]any) map[string]any {
	return map[string]any{
		"threadId": threadID, "turnId": turnID, "serverName": server, "environmentId": nil, "mode": "form",
		"_meta":           map[string]any{"codex_approval_kind": "mcp_tool_call", "persist": []any{"session", "always"}, "tool_description": "Append text.", "tool_params": params, "tool_params_display": []any{}},
		"message":         `Allow the ` + server + ` MCP server to run tool "` + tool + `"?`,
		"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func TestAnMcpToolPromptIsTheCallTheAgentHasGoingAgainstThatServer(t *testing.T) {
	allowed := asking(t, allow)
	allowed.client.notify("item/started", startedItem(mcpCallItem("mcp-1", "probe", "probe_write", `{"text":"hello"}`)))
	assertJSON(t, allowed.client.ask(t, "mcpServer/elicitation/request", elicitation("probe", "probe_write", map[string]any{"text": "hello"})), `{"result":{"action":"accept","content":{},"_meta":null}}`)
	assertJSON(t, allowed.asked.all(), `[{"toolId":"mcp-1","name":"probe_write","input":{"text":"hello"}}]`)
	allowed.client.notify("item/completed", completedItem(mcpCallItem("mcp-1", "probe", "probe_write", `{"text":"hello"}`)))
	assertJSON(t, allowed.client.ask(t, "mcpServer/elicitation/request", elicitation("probe", "probe_read", map[string]any{})), `{"result":{"action":"accept","content":{},"_meta":null}}`)
	assertJSON(t, allowed.asked.all()[1], `{"toolId":"probe:1","name":"probe_read","input":{}}`, "a prompt with no call in flight is named by its wording")
	form := map[string]any{"threadId": threadID, "turnId": turnID, "serverName": "probe", "environmentId": nil, "mode": "form", "_meta": nil, "message": "Which region?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}}
	assertJSON(t, allowed.client.ask(t, "mcpServer/elicitation/request", form), `{"result":{"action":"decline","content":null,"_meta":null}}`, "a form nobody here can fill is declined")
	if len(allowed.asked.all()) != 2 {
		t.Fatal("a form asks nobody")
	}
	allowed.end(t)

	denied := asking(t, deny)
	denied.client.notify("item/started", startedItem(mcpCallItem("mcp-2", "probe", "probe_write", `{"text":"no"}`)))
	assertJSON(t, denied.client.ask(t, "mcpServer/elicitation/request", elicitation("probe", "probe_write", map[string]any{"text": "no"})), `{"result":{"action":"decline","content":null,"_meta":null}}`)
	denied.end(t)
}

func TestMcpApprovalCorrelationStaysInsideTheRequestingRootOrChildThread(t *testing.T) {
	session := asking(t, allow)
	session.client.notify("item/started", startedItem(mcpCallItem("mcp-root", "probe", "root_probe", `{"owner":"root"}`)))
	session.client.notify("item/started", startedItem(`{"type":"subAgentActivity","id":"discover-child","kind":"started","agentThreadId":"child-a","agentPath":"/root/reviewer","model":null,"reasoningEffort":null}`))
	session.client.notify("item/started", map[string]any{"threadId": "child-a", "turnId": "child-turn", "startedAtMs": 1, "item": decodeAny([]byte(mcpCallItem("mcp-child", "probe", "child_probe", `{"owner":"child"}`)))})
	child := elicitation("probe", "child_probe", map[string]any{"owner": "child"})
	child["threadId"], child["turnId"] = "child-a", "child-turn"
	session.client.ask(t, "mcpServer/elicitation/request", child)
	session.client.ask(t, "mcpServer/elicitation/request", elicitation("probe", "root_probe", map[string]any{"owner": "root"}))
	assertJSON(t, session.asked.all(), `[{"toolId":"mcp-child","name":"child_probe","input":{"owner":"child"}},{"toolId":"mcp-root","name":"root_probe","input":{"owner":"root"}}]`)
	session.end(t)
}

func TestPermissionsUseApprovalsAndQuestionsReturnTypedAnswers(t *testing.T) {
	allowed := asking(t, allow, func(in *agentInput) {
		in.AskQuestion = func(context.Context, domain.QuestionRequest) (domain.QuestionAnswers, bool) {
			return domain.QuestionAnswers{"q1": "Chicago"}, true
		}
	})
	permissions := map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": nil}
	permission := func(itemID string, reason any) map[string]any {
		return map[string]any{"threadId": threadID, "turnId": turnID, "itemId": itemID, "environmentId": nil, "startedAtMs": 1, "cwd": "/tmp/project", "reason": reason, "permissions": permissions}
	}
	assertJSON(t, allowed.client.ask(t, "item/permissions/requestApproval", permission("perm-1", "npm install")), `{"result":{"permissions":{"network":{"enabled":true}},"scope":"turn"}}`)
	assertJSON(t, allowed.asked.all()[0], map[string]any{"toolId": "perm-1", "name": "permissions", "input": map[string]any{"cwd": "/tmp/project", "reason": "npm install", "permissions": permissions}})
	question := map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "ask-1", "questions": []any{map[string]any{"id": "q1", "header": "Region", "question": "Which region?", "isOther": false, "isSecret": false, "options": nil}}, "isBlocking": true, "autoResolutionMs": nil}
	assertJSON(t, allowed.client.ask(t, "item/tool/requestUserInput", question), `{"result":{"answers":{"q1":{"answers":["Chicago"]}}}}`)
	allowed.end(t)

	denied := asking(t, deny)
	assertJSON(t, denied.client.ask(t, "item/permissions/requestApproval", permission("perm-2", nil)), `{"result":{"permissions":{},"scope":"turn"}}`)
	declined := denied.client.ask(t, "item/tool/requestUserInput", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "ask-2", "questions": []any{}, "isBlocking": true, "autoResolutionMs": nil})
	if declined.Error == nil {
		t.Fatal("a question nobody answers is refused")
	}
	denied.end(t)
}

func TestAQuestionWithNoRunToAskIsDeclinedAndOneTheSessionCannotAnswerIsRefused(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(), nil)
	assertJSON(t, client.ask(t, "item/commandExecution/requestApproval", commandApproval("late", "ls", nil)), `{"result":{"decision":"decline"}}`)
	unsupported := client.ask(t, "item/tool/call", map[string]any{"threadId": threadID, "turnId": turnID, "callId": "dyn-1", "namespace": nil, "tool": "custom", "arguments": map[string]any{}})
	if unsupported.Error == nil || unsupported.Error.Code != -32601 || unsupported.Error.Message != "item/tool/call is not supported." {
		t.Fatalf("reply = %+v", unsupported)
	}
}

func TestAMembersCommitFromItsOwnWorktreeIsPutToItsUser(t *testing.T) {
	member := domain.CoordinationRoleMember
	ownCheckout := func(in *agentInput) {
		in.CoordinationRole = &member
		in.WorkspaceRoot = "/worktrees/repo-1"
		in.ProjectRoot = ptr("/repo")
	}
	commit := map[string]any{"kind": "command", "threadId": threadID, "turnId": turnID, "itemId": "commit-1", "startedAtMs": 1, "environmentId": nil, "command": "git commit -m 'Rename the flag'", "cwd": "/worktrees/repo-1", "reason": "Writing the worktree's git data needs to run outside the sandbox."}
	ask := func(decide func(domain.ToolIntent) agent.ToolDecision, options ...inputOption) (serverReply, []domain.ToolIntent, any) {
		session := asking(t, decide, options...)
		reply := session.client.ask(t, "item/commandExecution/requestApproval", commit)
		session.end(t)
		return reply, session.asked.all(), decodeAny(session.client.calls("turn/start")[0]).(map[string]any)["sandboxPolicy"]
	}
	for _, value := range []domain.ExecutionPolicy{domain.ExecutionPolicyConfirm, domain.ExecutionPolicyPlan, domain.ExecutionPolicyAllowEdits} {
		reply, intents, _ := ask(allow, ownCheckout, policy(value))
		assertJSON(t, reply, `{"result":{"decision":"accept"}}`, value)
		assertJSON(t, intentInputs(intents), []any{map[string]any{"command": commit["command"], "cwd": commit["cwd"], "reason": commit["reason"]}}, value)
		refused, _, _ := ask(deny, ownCheckout, policy(value))
		assertJSON(t, refused, `{"result":{"decision":"decline"}}`, value)
	}
	bypass, _, _ := ask(deny, ownCheckout, policy(domain.ExecutionPolicyBypass))
	assertJSON(t, bypass, `{"result":{"decision":"accept"}}`, "a bypass run needs nobody's word")
	_, _, sandbox := ask(allow, ownCheckout, policy(domain.ExecutionPolicyAllowEdits))
	assertJSON(t, sandbox.(map[string]any)["writableRoots"], `[]`, "the project's git data is never writable")
}

func TestACoordinatorsRequestToRunOutsideItsReadOnlyShellIsRefusedWithoutAsking(t *testing.T) {
	coordinator := domain.CoordinationRoleCoordinator
	for _, value := range []domain.ExecutionPolicy{domain.ExecutionPolicyConfirm, domain.ExecutionPolicyAllowEdits, domain.ExecutionPolicyAutonomous} {
		session := asking(t, allow, func(in *agentInput) { in.CoordinationRole = &coordinator }, policy(value))
		assertJSON(t, session.client.ask(t, "item/commandExecution/requestApproval", commandApproval("cmd-1", "rm -rf build", nil)), `{"result":{"decision":"decline"}}`, value)
		if len(session.asked.all()) != 0 {
			t.Fatalf("%s: nobody is asked", value)
		}
		session.end(t)
	}
}
