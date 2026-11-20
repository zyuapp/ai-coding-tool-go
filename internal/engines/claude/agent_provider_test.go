package claude

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

func appended(options protocol.Options) string {
	if options.SystemPrompt.Kind != protocol.SystemPromptPreset || options.SystemPrompt.Append == nil {
		return ""
	}
	return *options.SystemPrompt.Append
}

func modelSession(rows []protocol.ModelInfo, opened **fakeQuery) QueryFactory {
	return func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, true)
		q.models = rows
		*opened = q
		return q, nil
	}
}

func TestModelDiscoveryMatchesShortFullAndWiderContextNames(t *testing.T) {
	var opened *fakeQuery
	models := discoverClaudeModels(context.Background(), modelSession([]protocol.ModelInfo{
		{Value: "opus[1m]", ResolvedModel: ptrTo("claude-opus-5[1m]")},
		{Value: "claude-fable-5[1m]", ResolvedModel: ptrTo("claude-fable-5")},
		{Value: "sonnet", ResolvedModel: ptrTo("claude-sonnet-5")},
	}, &opened))
	assertEqual(t, models, []domain.AgentModel{"fable", "opus", "sonnet"}, "the catalogue order is kept, and Haiku is absent because no row offers it")
	assertEqual(t, opened.isClosed(), true, "the session only lives for the question")
}

func TestAClaudeThatWillNotAnswerItsModelsIsOfferedAllOfThem(t *testing.T) {
	failing := func(context.Context, protocol.Options) (Query, error) { return nil, errors.New("spawn ENOENT") }
	assertEqual(t, discoverClaudeModels(context.Background(), failing), []domain.AgentModel(nil), "unstartable")
	var opened *fakeQuery
	assertEqual(t, discoverClaudeModels(context.Background(), modelSession([]protocol.ModelInfo{{Value: "some-other-model"}}, &opened)), []domain.AgentModel(nil), "a list that matches nothing is treated the same way")
}

func TestCommandDiscoveryInitializesAnIdleWorkspaceSessionAndClosesIt(t *testing.T) {
	var opened *fakeQuery
	commands := []protocol.SlashCommand{{Name: "pdf", Description: "Work with PDFs", ArgumentHint: "<file>"}}
	result, err := discoverClaudeCommands(context.Background(), func(_ context.Context, options protocol.Options) (Query, error) {
		opened = newFake(options, false)
		opened.commands = commands
		return opened, nil
	}, "", "/tmp/project", false)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, result, []*contracts.AvailableCommand{{Name: "pdf", Description: "Work with PDFs", ArgumentHint: "<file>"}}, "commands")
	assertEqual(t, opened.options.SettingSources, []string{"user", "project", "local"}, "settings")
	assertEqual(t, opened.options.AllSkills, true, "skills")
	assertEqual(t, opened.options.Cwd, "/tmp/project", "cwd")
	assertEqual(t, opened.isClosed(), true, "closed")
}

func TestTheAppPluginRidesEverySessionAndItsSkillsGoByShortNames(t *testing.T) {
	var opened *fakeQuery
	commands, err := discoverClaudeCommands(context.Background(), func(_ context.Context, options protocol.Options) (Query, error) {
		opened = newFake(options, false)
		opened.commands = []protocol.SlashCommand{
			{Name: "aicodingtool:review-thread", Description: "(aicodingtool) Review this thread's work.", ArgumentHint: "[focus]", Aliases: []string{"review-thread"}},
			{Name: "pdf", Description: "Work with PDFs", ArgumentHint: "<file>"},
		}
		return opened, nil
	}, "/app/resources/app-plugin", "/tmp/project", false)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, commands, []*contracts.AvailableCommand{
		{Name: "review-thread", Description: "Review this thread's work.", ArgumentHint: "[focus]"},
		{Name: "pdf", Description: "Work with PDFs", ArgumentHint: "<file>"},
	}, "commands")
	assertEqual(t, opened.options.Plugins, []protocol.Plugin{{Path: "/app/resources/app-plugin"}}, "discovery plugins")

	f := scripted(t)
	await(t, execute(NewProvider(nil, f.factory, "/app/resources/app-plugin"), runInput(&recorder{})))
	assertEqual(t, f.options(t).Plugins, []protocol.Plugin{{Path: "/app/resources/app-plugin"}}, "run plugins")

	f = scripted(t)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{})))
	assertEqual(t, f.options(t).Plugins, []protocol.Plugin(nil), "without a plugin root, Claude is offered no plugin at all")
}

func TestQueryOptionsFollowRunPolicyAndWorkspaceSettings(t *testing.T) {
	cases := []struct {
		policy domain.ExecutionPolicy
		mode   string
		bypass bool
	}{
		{domain.ExecutionPolicyConfirm, "default", false},
		{domain.ExecutionPolicyPlan, "plan", false},
		{domain.ExecutionPolicyAllowEdits, "acceptEdits", false},
		{domain.ExecutionPolicyAutonomous, "auto", false},
		{domain.ExecutionPolicyBypass, "bypassPermissions", true},
	}
	for _, c := range cases {
		f := scripted(t)
		result := await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.Policy = c.policy })))
		assertEqual(t, result, succeeded(), "result")
		options := f.options(t)
		assertEqual(t, options.PermissionMode, c.mode, string(c.policy))
		assertEqual(t, options.AllowDangerouslySkipPermissions, c.bypass, string(c.policy))
		eventually(t, "closed", f.last().isClosed)
	}

	f := scripted(t)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{}, func(input *agent.ProviderRunInput) {
		input.Projectless = true
		input.Model = "opus"
		input.Effort = "xhigh"
		input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
	})))
	options := f.options(t)
	assertEqual(t, options.SettingSources, []string{"user"}, "settings")
	assertEqual(t, options.Model, "opus", "model")
	assertEqual(t, options.Effort, "xhigh", "effort")
	assertEqual(t, options.Resume, "session-1", "resume")
	assertEqual(t, options.Betas, []string{"context-1m-2025-08-07"}, "betas")
	assertEqual(t, options.AllSkills, true, "skills")
	assertEqual(t, options.ForwardSubagentText, true, "forward")
	assertEqual(t, options.IncludePartialMessages, true, "partial")
	if !regexp.MustCompile(`workspace files as \[label\]\(/absolute/path:line\)`).MatchString(appended(options)) {
		t.Fatal("link instructions missing")
	}
	if strings.Contains(appended(options), agent.SideChatInstructions) {
		t.Fatal("main threads keep their existing task scope")
	}
	assertEqual(t, options.DisallowedTools, []string{"AskUserQuestion", "EnterWorktree", "ExitWorktree"}, "a thread's checkout changes only through the app")
}

func streamStart(id string) string {
	return fmt.Sprintf(`{"type":"stream_event","uuid":"u","parent_tool_use_id":null,"event":{"type":"message_start","message":{"id":%q}}}`, id)
}

func textDelta(text string) string {
	encoded, _ := jsontext.AppendQuote(nil, text)
	return fmt.Sprintf(`{"type":"stream_event","uuid":"u","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}}`, encoded)
}

func finalText(id, text string) string {
	encoded, _ := jsontext.AppendQuote(nil, text)
	return fmt.Sprintf(`{"type":"assistant","uuid":"different-final-wrapper-uuid","parent_tool_use_id":null,"message":{"id":%q,"model":"claude-sonnet","usage":{"input_tokens":1},"content":[{"type":"text","text":%s}]}}`, id, encoded)
}

func TestClaudeStreamsOnlyCompleteMarkdownBlocksAndDoesNotRepeatFinalText(t *testing.T) {
	id := "api-message-streamed"
	full := "## First\n\nParagraph with **bold**.\n\n```ts\nconst x = 1;\n\n```\n"
	f := scripted(t, streamStart(id), textDelta("## Fi"), textDelta("rst\n\nParagraph with **bo"), textDelta("ld**.\n\n```ts\nconst x = 1;\n\n"), textDelta("```\n"), finalText(id, full))
	r := &recorder{}
	assertEqual(t, await(t, execute(NewProvider(nil, f.factory, ""), runInput(r))), succeeded(), "result")
	assertEqual(t, r.all(), []agent.ProviderEvent{
		agent.EventAssistantTail{MessageID: id, Text: "## Fi"},
		agent.EventAssistant{MessageID: id, Text: "## First\n\n", Append: true},
		agent.EventAssistantTail{MessageID: id, Text: "Paragraph with **bo"},
		agent.EventAssistant{MessageID: id, Text: "Paragraph with **bold**.\n\n", Append: true},
		agent.EventAssistantTail{MessageID: id, Text: "```ts\nconst x = 1;\n\n"},
		agent.EventAssistant{MessageID: id, Text: "```ts\nconst x = 1;\n\n```\n", Append: true},
		agent.EventAssistantTail{MessageID: id, Text: ""},
		agent.EventSpend{Tokens: 1},
		agent.EventUsage{Tokens: 1, Limit: 1_000_000, Model: "claude-sonnet"},
	}, "events")
}

func TestClaudeKeepsComplexMarkdownFencesIntactAcrossChunkBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		chunks   []string
		expected []string
	}{
		{"fenced code nested in a list",
			[]string{"Before.\n\n- Example:\n\n    ```ts\n    const first = 1;\n", "\n    const second = 2;\n", "    ```\n\nAfter."},
			[]string{"Before.\n\n- Example:\n\n", "    ```ts\n    const first = 1;\n\n    const second = 2;\n    ```\n\n", "After."}},
		{"four-backtick fence containing a triple-backtick fence",
			[]string{"``", "``md\n```ts\nconst value = 1;\n", "```\n\nStill outer.\n```", "`\n\nDone."},
			[]string{"````md\n```ts\nconst value = 1;\n```\n\nStill outer.\n````\n\n", "Done."}},
		{"tilde fence with CRLF newlines",
			[]string{"~~~ts\r\nconst first = 1;\r", "\n\r\nconst second = 2;\r\n", "~~~\r\n\r\nTail."},
			[]string{"~~~ts\r\nconst first = 1;\r\n\r\nconst second = 2;\r\n~~~\r\n\r\n", "Tail."}},
		{"unterminated final fence",
			[]string{"Intro.\n\n```ts\nconst delayed =", " true;"},
			[]string{"Intro.\n\n", "```ts\nconst delayed = true;"}},
	}
	for _, scenario := range cases {
		id := "complex-" + scenario.name
		raws := []string{streamStart(id)}
		for _, chunk := range scenario.chunks {
			raws = append(raws, textDelta(chunk))
		}
		raws = append(raws, finalText(id, strings.Join(scenario.chunks, "")))
		r := &recorder{}
		await(t, execute(NewProvider(nil, scripted(t, raws...).factory, ""), runInput(r)))
		committed, chunk := "", 0
		var texts []string
		for _, event := range r.all() {
			switch event := event.(type) {
			case agent.EventAssistant:
				committed += event.Text
				texts = append(texts, event.Text)
			case agent.EventAssistantTail:
				chunk++
				assertEqual(t, committed+event.Text, strings.Join(scenario.chunks[:chunk], ""), fmt.Sprintf("%s after chunk %d", scenario.name, chunk))
			}
		}
		assertEqual(t, texts, scenario.expected, scenario.name)
		assertEqual(t, chunk, len(scenario.chunks), scenario.name+" reports a tail for every chunk")
	}
}

func TestClaudeReportsItsNativeGoalAsThreadState(t *testing.T) {
	r := &recorder{}
	f := scripted(t,
		`{"type":"active_goal","uuid":"goal-1","session_id":"session-1","value":{"condition":"All checks pass","iterations":2,"set_at":1,"tokens_at_start":10,"last_reason":"One check still fails"}}`,
		`{"type":"active_goal","uuid":"goal-2","session_id":"session-1","value":null}`)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(r)))
	assertEqual(t, r.goals, []contracts.GoalReport{
		{Goal: nil},
		{Goal: &domain.ActiveGoal{Objective: "All checks pass", Status: domain.ActiveGoalStatusActive, Iterations: ptrTo(2.0), LastReason: ptrTo("One check still fails")}},
		{Goal: nil},
	}, "goals")
}

func TestReportedContextUsageTracksTheWidestWindowTheModelOffers(t *testing.T) {
	assistant := `{"type":"assistant","uuid":"u","parent_tool_use_id":null,"message":{"id":"api-message","model":"claude-model","usage":{"input_tokens":1},"content":[]}}`
	for model, limit := range map[domain.AgentModel]float64{"fable": 1_000_000, "opus": 1_000_000, "sonnet": 1_000_000, "haiku": 200_000} {
		r := &recorder{}
		await(t, execute(NewProvider(nil, scripted(t, assistant).factory, ""), runInput(r, func(input *agent.ProviderRunInput) { input.Model = model })))
		assertEqual(t, eventsOf[agent.EventUsage](r), []agent.EventUsage{{Tokens: 1, Limit: limit, Model: "claude-model"}}, model)
	}
}

func TestASyntheticReplyLeavesTheReportedContextUsageAlone(t *testing.T) {
	r := &recorder{}
	f := scripted(t,
		`{"type":"assistant","uuid":"a","parent_tool_use_id":null,"message":{"id":"api-message","model":"claude-sonnet","usage":{"input_tokens":40000},"content":[]}}`,
		`{"type":"assistant","uuid":"b","parent_tool_use_id":null,"message":{"id":"synthetic-message","model":"<synthetic>","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0},"content":[{"type":"text","text":"6 MCP server(s): 2 connected."}]}}`)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(r)))
	assertEqual(t, eventsOf[agent.EventUsage](r), []agent.EventUsage{{Tokens: 40_000, Limit: 1_000_000, Model: "claude-sonnet"}}, "usage")
	found := false
	for _, event := range eventsOf[agent.EventAssistant](r) {
		found = found || event.Text == "6 MCP server(s): 2 connected."
	}
	if !found {
		t.Fatal("the synthetic reply's text is still shown")
	}
}

func TestSideChatsGetTheirOwnTaskBoundaryWhileKeepingPolicyAndTools(t *testing.T) {
	cases := []func(input *agent.ProviderRunInput){
		func(*agent.ProviderRunInput) {},
		func(input *agent.ProviderRunInput) {
			input.Continuation = &domain.Continuation{Provider: "claude", Value: "main-session"}
			input.ForkContinuation = true
		},
		func(input *agent.ProviderRunInput) {
			input.Continuation = &domain.Continuation{Provider: "claude", Value: "side-session"}
		},
	}
	for index, apply := range cases {
		f := scripted(t)
		input := runInput(&recorder{}, func(input *agent.ProviderRunInput) {
			input.Channel = contracts.RunChannelSide
			input.Policy = domain.ExecutionPolicyAutonomous
		}, apply)
		await(t, execute(NewProvider(nil, f.factory, ""), input))
		options := f.options(t)
		resume := ""
		if input.Continuation != nil {
			resume = input.Continuation.Value
		}
		assertEqual(t, options.Resume, resume, fmt.Sprint("resume ", index))
		assertEqual(t, options.ForkSession, input.ForkContinuation, fmt.Sprint("fork ", index))
		if !strings.Contains(appended(options), agent.SideChatInstructions) {
			t.Fatal("side chat instructions missing")
		}
		assertEqual(t, options.PermissionMode, "auto", "mode")
		assertEqual(t, options.Tools, []string(nil), "a side chat is limited by its policy, not by a tool list")
	}
}

func TestClaudeReceivesBundledComputerUseMCP(t *testing.T) {
	f := scripted(t)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{}, func(input *agent.ProviderRunInput) {
		input.ComputerUse = domain.ComputerUseRunConfigAvailable{MCP: domain.ComputerUseMCP{Command: "/app/cua-driver", Args: []string{"mcp", "--embedded"}, Env: map[string]string{"CUA_DRIVER_EMBEDDED": "1"}}}
	})))
	assertEqual(t, f.options(t).McpServers, []protocol.NamedMcpServer{{Name: "cua-driver", Config: protocol.McpServerConfig{
		Type: ptrTo("stdio"), Command: "/app/cua-driver", Args: []string{"mcp", "--embedded"}, Env: map[string]string{"CUA_DRIVER_EMBEDDED": "1"},
	}}}, "servers")
}

func TestAutonomousRunsAllowComputerUseWithoutBypassingOtherApprovals(t *testing.T) {
	var mu sync.Mutex
	var authorized []string
	f, _, end := liveTurn(t, &recorder{}, func(input *agent.ProviderRunInput) {
		input.Policy = domain.ExecutionPolicyAutonomous
		input.ComputerUse = domain.ComputerUseRunConfigAvailable{MCP: domain.ComputerUseMCP{Command: "/app/cua-driver", Args: []string{}, Env: map[string]string{}}}
		input.Authorize = func(_ context.Context, intent domain.ToolIntent) agent.ToolDecision {
			mu.Lock()
			authorized = append(authorized, intent.Name)
			mu.Unlock()
			return agent.Allow
		}
	})
	assertEqual(t, useTool(t, f, "mcp__cua-driver__click", `{}`, "cua-1").Behavior, "allow", "computer use")
	assertEqual(t, authorized, []string(nil), "nobody was asked")
	assertEqual(t, useTool(t, f, "Bash", `{}`, "bash-1").Behavior, "allow", "bash")
	assertEqual(t, authorized, []string{"Bash"}, "bash was asked")
	end()
}

func TestBypassRunsAllowEveryToolWithoutTheGate(t *testing.T) {
	asked := false
	f, _, end := liveTurn(t, &recorder{}, func(input *agent.ProviderRunInput) {
		input.Policy = domain.ExecutionPolicyBypass
		input.Authorize = func(context.Context, domain.ToolIntent) agent.ToolDecision { asked = true; return agent.Deny }
	})
	assertEqual(t, useTool(t, f, "Bash", `{}`, "bash-1").Behavior, "allow", "bash")
	assertEqual(t, asked, false, "the gate was not consulted")
	end()
}

func TestClaudeMessagesTranslateToProviderEventsAndToolIntents(t *testing.T) {
	r := &recorder{}
	f := scripted(t,
		`{"type":"system","subtype":"init","session_id":"session-2"}`,
		`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":190000,"post_tokens":40000}}`,
		`{"type":"system","subtype":"status","status":"compacting"}`,
		`{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"compact failed"}`,
		`{"type":"assistant","uuid":"message-1","message":{"model":"claude-sonnet","usage":{"input_tokens":10,"cache_creation_input_tokens":3,"cache_read_input_tokens":4},"content":[{"type":"text","text":"hello"},{"type":"text","text":"  "},{"type":"tool_use","id":"edit-1","name":"Edit","input":{"file_path":"/tmp/project/a.ts"}}]}}`,
		resultSuccess)
	assertEqual(t, await(t, execute(NewProvider(nil, f.factory, ""), runInput(r))), succeeded(), "result")
	assertEqual(t, r.all(), []agent.ProviderEvent{
		agent.EventContinuation{Continuation: domain.Continuation{Provider: "claude", Value: "session-2"}},
		agent.EventCompaction{Trigger: "auto", PreTokens: 190_000, PostTokens: ptrTo(40_000.0)},
		agent.EventCompactionStatus{Compacting: true},
		agent.EventCompactionStatus{Compacting: false, Error: ptrTo("compact failed")},
		agent.EventSpend{Tokens: 17},
		agent.EventAssistant{MessageID: "message-1", Text: "hello"},
		agent.EventTool{Intent: domain.ToolIntent{ToolID: "edit-1", Name: "Edit", Input: jsontext.Value(`{"file_path":"/tmp/project/a.ts"}`), WritePath: ptrTo("/tmp/project/a.ts")}},
		agent.EventUsage{Tokens: 17, Limit: 1_000_000, Model: "claude-sonnet"},
	}, "events")

	var mu sync.Mutex
	var intents []domain.ToolIntent
	live, _, end := liveTurn(t, &recorder{}, func(input *agent.ProviderRunInput) {
		input.Authorize = func(_ context.Context, intent domain.ToolIntent) agent.ToolDecision {
			mu.Lock()
			intents = append(intents, intent)
			mu.Unlock()
			if intent.Name == "NotebookEdit" {
				return agent.Allow
			}
			return agent.Deny
		}
	})
	assertEqual(t, useTool(t, live, "NotebookEdit", `{"notebook_path":"/tmp/project/a.ipynb"}`, "tool-1").Behavior, "allow", "allowed")
	denied := useTool(t, live, "Bash", `{"command":"pwd"}`, "tool-2")
	assertEqual(t, denied.Behavior, "deny", "denied")
	assertEqual(t, *denied.Message, "The user denied this action.", "denial")
	assertEqual(t, intents, []domain.ToolIntent{
		{ToolID: "tool-1", Name: "NotebookEdit", Input: jsontext.Value(`{"notebook_path":"/tmp/project/a.ipynb"}`), WritePath: ptrTo("/tmp/project/a.ipynb")},
		{ToolID: "tool-2", Name: "Bash", Input: jsontext.Value(`{"command":"pwd"}`)},
	}, "intents")
	end()
}

func TestASkillInvocationIsNamedAfterTheSkillItRuns(t *testing.T) {
	r := &recorder{}
	f := scripted(t,
		`{"type":"assistant","uuid":"message-1","message":{"model":"claude-sonnet","usage":{"input_tokens":1},"content":[{"type":"tool_use","id":"skill-1","name":"Skill","input":{"skill":"quality-check","args":"--report-only"}},{"type":"tool_use","id":"skill-2","name":"Skill","input":{}}]}}`,
		resultSuccess)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(r)))
	var names []string
	for _, event := range eventsOf[agent.EventTool](r) {
		names = append(names, event.Intent.Name)
	}
	assertEqual(t, names, []string{"Skill: quality-check", "Skill"}, "names")
}

func TestFailuresExceptionsAndAbortsCloseTheQuery(t *testing.T) {
	f := scripted(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["one","two"]}`)
	assertEqual(t, await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{}))), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "one\ntwo"}, "failure")
	eventually(t, "closed", f.last().isClosed)

	var broken *fakeQuery
	exception := func(_ context.Context, options protocol.Options) (Query, error) {
		broken = newFake(options, true)
		broken.failure = errors.New("stream broke")
		return broken, nil
	}
	assertEqual(t, await(t, execute(NewProvider(nil, exception, ""), runInput(&recorder{}))), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "stream broke"}, "exception")
	eventually(t, "closed", broken.isClosed)

	input := runInput(&recorder{})
	input.Abort()
	assertEqual(t, await(t, execute(NewProvider(nil, scripted(t).factory, ""), input)), agent.ProviderResult{Status: domain.RunStatusCancelled}, "aborted")
}

func TestASteeredMessageJoinsTheInputAndOnlyThenCountsAsDelivered(t *testing.T) {
	r := &recorder{}
	var opened *fakeQuery
	factory := func(_ context.Context, options protocol.Options) (Query, error) {
		opened = newFake(options, true)
		opened.endAfterSent = 2
		return opened, nil
	}
	steering := &steerList{messages: []agent.SteerMessage{{MessageID: "message-1", Prompt: "check the tests too"}}}
	assertEqual(t, await(t, execute(NewProvider(nil, factory, ""), runInput(r, func(input *agent.ProviderRunInput) { input.Steering = steering }))), succeeded(), "result")
	assertEqual(t, opened.sentTexts(), []string{"inspect the app", "check the tests too"}, "sent")
	assertEqual(t, eventsOf[agent.EventSteered](r), []agent.EventSteered{{MessageID: "message-1"}}, "steered")
	assertEqual(t, opened.priorities, []string{"", "now"}, "the run's own prompt takes its turn; the steered one asks to join the turn already going")
}

func TestMessagesSteeredIntoOneTurnAreAnsweredTogether(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	steering := &steerList{messages: []agent.SteerMessage{{MessageID: "message-1", Prompt: "say BANANA"}, {MessageID: "message-2", Prompt: "say MANGO"}}}
	done := execute(provider, runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.Steering = steering }))
	eventually(t, "all three inputs", func() bool { q := f.last(); return q != nil && len(q.sentTexts()) == 3 })
	f.last().emit(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["[ede_diagnostic] result_type=user"]}`)
	f.last().emit(t, `{"type":"result","subtype":"success","is_error":false,"result":"BANANA MANGO"}`)
	assertEqual(t, await(t, done), succeeded(), "the turn the steering cut short is not the run's answer")
}

func TestARunEndsOnItsTurnsResultEvenThoughItsInputStaysOpen(t *testing.T) {
	f := &fakes{make: func(options protocol.Options) *fakeQuery {
		q := newFake(options, false)
		q.queue = decodeAll(t, resultSuccess)
		return q
	}}
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	assertEqual(t, await(t, execute(provider, runInput(&recorder{}))), succeeded(), "result")
}

func TestASecondTurnKeepsTheWarmSessionAndTakesItsSettingsAsChanges(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	assertEqual(t, runTurn(t, f, provider, runInput(&recorder{}), `{"type":"system","subtype":"init","session_id":"session-1"}`), succeeded(), "first")
	second := runTurn(t, f, provider, runInput(&recorder{}, func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
		input.Model = "sonnet"
		input.Effort = "low"
		input.Policy = domain.ExecutionPolicyAutonomous
		input.Prompt = "and again"
	}))
	assertEqual(t, second, succeeded(), "second")
	assertEqual(t, f.count(), 1, "the warm session answers the second turn, so nothing is spawned or resumed")
	q := f.last()
	assertEqual(t, q.sentTexts(), []string{"inspect the app", "and again"}, "sent")
	q.mu.Lock()
	model, mode, settings := *q.model, q.mode, q.settings
	q.mu.Unlock()
	low := domain.ClaudeEffort("low")
	assertEqual(t, []any{model, settings, mode}, []any{"sonnet", effortSettings{EffortLevel: &low}, "auto"}, "settings")
	provider.CloseAll()
	eventually(t, "closed", q.isClosed)
}

func TestASessionIsOnlyReusedForItsThreadAndConversation(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	continuation := &domain.Continuation{Provider: "claude", Value: "session-1"}
	init := `{"type":"system","subtype":"init","session_id":"session-1"}`
	runTurn(t, f, provider, runInput(&recorder{}), init)
	cases := []struct {
		reason string
		apply  func(input *agent.ProviderRunInput)
	}{
		{"another thread", func(input *agent.ProviderRunInput) { input.TaskID = "task-2"; input.Continuation = continuation }},
		{"a fork of the same session", func(input *agent.ProviderRunInput) { input.Continuation = continuation; input.ForkContinuation = true }},
		{"a different session", func(input *agent.ProviderRunInput) {
			input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-9"}
		}},
		{"a different checkout", func(input *agent.ProviderRunInput) {
			input.Continuation = continuation
			input.WorkspaceRoot = "/tmp/worktree"
		}},
	}
	for _, c := range cases {
		opens := f.count()
		runTurn(t, f, provider, runInput(&recorder{}, c.apply), init)
		assertEqual(t, f.count(), opens+1, c.reason)
	}
}

func TestCancellingATurnInterruptsItAndLeavesTheSessionAlive(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	input := runInput(&recorder{})
	done := execute(provider, input)
	eventually(t, "the turn's input", func() bool { q := f.last(); return q != nil && len(q.sentTexts()) == 1 })
	input.Abort()
	q := f.last()
	eventually(t, "interrupted", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.interrupted })
	q.emit(t, `{"type":"result","subtype":"success","is_error":false,"result":"stopped"}`)
	assertEqual(t, await(t, done), agent.ProviderResult{Status: domain.RunStatusCancelled}, "cancelled")
	assertEqual(t, q.isClosed(), false, "an interrupted turn does not take the session's processes with it")
}

func TestAWorkflowKeepsReportingBetweenTurns(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	r := &recorder{}
	runTurn(t, f, provider, runInput(r),
		`{"type":"system","subtype":"init","session_id":"session-1"}`,
		`{"type":"system","subtype":"task_started","task_type":"local_workflow","task_id":"wf-1","workflow_name":"review-changes","description":"Review changed files"}`)
	q := f.last()
	q.emit(t, `{"type":"system","subtype":"task_progress","task_id":"wf-1","workflow_progress":[{"type":"workflow_phase","index":0,"title":"Review"},{"type":"workflow_agent","index":0,"label":"review:bugs","state":"progress","startedAt":5}],"usage":{"total_tokens":1200,"tool_uses":4}}`)
	q.emit(t, `{"type":"system","subtype":"task_notification","task_id":"wf-1","status":"completed","summary":"Dynamic workflow completed"}`)
	q.settle(t)
	var kinds []string
	for _, report := range r.workflowReports() {
		kinds = append(kinds, fmt.Sprintf("%T", report))
	}
	assertEqual(t, kinds, []string{"contracts.WorkflowReportWorkflowStarted", "contracts.WorkflowReportWorkflowProgress", "contracts.WorkflowReportWorkflowFinished"}, "a workflow with no turn to report under still reports")
	finished := r.workflowReports()[2].(contracts.WorkflowReportWorkflowFinished)
	assertEqual(t, finished.Status, contracts.WorkflowReportWorkflowFinishedStatusCompleted, "status")
	progress := r.workflowReports()[1].(contracts.WorkflowReportWorkflowProgress)
	assertEqual(t, []float64{progress.TotalTokens, progress.TotalToolCalls, float64(len(progress.Phases)), float64(len(progress.Agents))}, []float64{1200, 4, 1, 1}, "progress")

	runTurn(t, f, provider, runInput(r, func(input *agent.ProviderRunInput) {
		input.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
		input.Prompt = "and again"
	}), `{"type":"system","subtype":"task_started","task_type":"local_workflow","task_id":"wf-2","workflow_name":"migrate","description":"Migrate call sites"}`)
	assertEqual(t, f.count(), 1, "one session")
	reports := r.workflowReports()
	assertEqual(t, reports[len(reports)-1], contracts.WorkflowReport(contracts.WorkflowReportWorkflowStarted{ID: "wf-2", Name: "migrate", Description: "Migrate call sites"}), "second workflow")
}

func TestASessionClosingUnderAWorkflowReportsItStopped(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	r := &recorder{}
	runTurn(t, f, provider, runInput(r),
		`{"type":"system","subtype":"task_started","task_type":"local_workflow","task_id":"wf-1","workflow_name":"review-changes","description":"Review changed files"}`)
	provider.CloseAll()
	eventually(t, "stopped", func() bool {
		reports := r.workflowReports()
		return len(reports) > 0 && fmt.Sprint(reports[len(reports)-1]) == fmt.Sprint(contracts.WorkflowReportWorkflowFinished{ID: "wf-1", Status: "stopped", Summary: ""})
	})
}

func TestABackgroundProcessIsStoppedThroughTheThreadsSession(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	assertEqual(t, provider.StopProcess("task-1", "bash-1"), false, "no session, nothing to stop")
	runTurn(t, f, provider, runInput(&recorder{}))
	assertEqual(t, provider.StopProcess("task-gone", "bash-1"), false, "another thread")
	assertEqual(t, provider.StopProcess("task-1", "wf-1"), true, "the session outlives the run, so the workflow it holds is still reachable")
	q := f.last()
	eventually(t, "stopped", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.stopped) == 1 && q.stopped[0] == "wf-1" })
}

func running(ids ...string) string {
	tasks := make([]string, 0, len(ids))
	for _, id := range ids {
		tasks = append(tasks, fmt.Sprintf(`{"task_id":%q,"task_type":"local_workflow","description":"Review changed files"}`, id))
	}
	return `{"type":"system","subtype":"background_tasks_changed","tasks":[` + strings.Join(tasks, ",") + `]}`
}

func TestASessionWithWorkRunningOutlivesTheIdleDeadline(t *testing.T) {
	f := live()
	provider := NewProvider(agent.NewSessionPool(nil, 5*time.Millisecond), f.factory, "")
	runTurn(t, f, provider, runInput(&recorder{}), running("wf-1"))
	q := f.last()
	time.Sleep(60 * time.Millisecond)
	assertEqual(t, q.isClosed(), false, "the workflow the turn left running is not on the turn's clock")
	q.emit(t, running())
	eventually(t, "the session is handed back once nothing is running under it", q.isClosed)
}

func TestASessionWithWorkRunningIsPassedOverForEviction(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	runTurn(t, f, provider, runInput(&recorder{}), running("wf-1"))
	for _, taskID := range []string{"task-2", "task-3", "task-4", "task-5"} {
		runTurn(t, f, provider, runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.TaskID = taskID }))
	}
	f.mu.Lock()
	workflow, oldestIdle := f.sessions[0], f.sessions[1]
	f.mu.Unlock()
	eventually(t, "the oldest idle session is given up", oldestIdle.isClosed)
	assertEqual(t, workflow.isClosed(), false, "the least recently used session is still running a workflow")
}

func TestWorkOutstandingWhenARunIsCancelledHoldsTheSessionNoLonger(t *testing.T) {
	f := live()
	provider := NewProvider(agent.NewSessionPool(nil, 5*time.Millisecond), f.factory, "")
	input := runInput(&recorder{})
	done := execute(provider, input)
	eventually(t, "the turn's input", func() bool { q := f.last(); return q != nil && len(q.sentTexts()) == 1 })
	q := f.last()
	q.emit(t, running("wf-1"))
	q.settle(t)
	input.Abort()
	q.emit(t, `{"type":"result","subtype":"success","is_error":false,"result":"stopped"}`)
	assertEqual(t, await(t, done), agent.ProviderResult{Status: domain.RunStatusCancelled}, "cancelled")
	time.Sleep(60 * time.Millisecond)
	assertEqual(t, q.isClosed(), false, "cancelling the turn does not cancel what it left running")
	q.emit(t, running())
	eventually(t, "and the session is not pinned once that work stops", q.isClosed)
}

func TestATurnThatFailsGivesItsSessionUp(t *testing.T) {
	f := live()
	provider := NewProvider(agent.NewSessionPool(nil, 5*time.Millisecond), f.factory, "")
	defer provider.CloseAll()
	done := execute(provider, runInput(&recorder{}))
	eventually(t, "the turn's input", func() bool { q := f.last(); return q != nil && len(q.sentTexts()) == 1 })
	q := f.last()
	q.emit(t, running("wf-1"))
	q.emit(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"errors":["broke"]}`)
	assertEqual(t, await(t, done), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "broke"}, "failed")
	eventually(t, "closed", q.isClosed)
	runTurn(t, f, provider, runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.Prompt = "and again" }))
	assertEqual(t, f.count(), 2, "the failed session is not left in the pool to be reused or reaped")
}

func TestQuittingClosesASessionThatStillHasWorkRunning(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	runTurn(t, f, provider, runInput(&recorder{}), running("wf-1"))
	provider.CloseAll()
	eventually(t, "closed", f.last().isClosed)
}

func TestARunTheChromeSettingIsOnForEnablesTheIntegration(t *testing.T) {
	f := scripted(t)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.ChromeBrowser = true })))
	assertEqual(t, f.options(t).ExtraArgs, []protocol.ExtraArg{{Name: "chrome"}}, "extra args")
	if !strings.Contains(appended(f.options(t)), "mcp__claude-in-chrome__") {
		t.Fatal("chrome instructions missing")
	}

	f = scripted(t)
	await(t, execute(NewProvider(nil, f.factory, ""), runInput(&recorder{})))
	assertEqual(t, f.options(t).ExtraArgs, []protocol.ExtraArg(nil), "off")
	if strings.Contains(appended(f.options(t)), "mcp__claude-in-chrome__") {
		t.Fatal("chrome instructions present")
	}
}

func TestTurningChromeOnGivesTheThreadASessionOfItsOwn(t *testing.T) {
	f := live()
	provider := NewProvider(nil, f.factory, "")
	defer provider.CloseAll()
	runTurn(t, f, provider, runInput(&recorder{}))
	runTurn(t, f, provider, runInput(&recorder{}))
	assertEqual(t, f.count(), 1, "the same settings reuse the warm session")
	runTurn(t, f, provider, runInput(&recorder{}, func(input *agent.ProviderRunInput) { input.ChromeBrowser = true }))
	assertEqual(t, f.count(), 2, "a session of its own")
	assertEqual(t, f.options(t).ExtraArgs, []protocol.ExtraArg{{Name: "chrome"}}, "extra args")
}

func TestEveryClaudeProcessNamesThisAppAndKeepsTheEnvironment(t *testing.T) {
	var mu sync.Mutex
	var seen []protocol.Options
	factory := func(_ context.Context, options protocol.Options) (Query, error) {
		mu.Lock()
		seen = append(seen, options)
		mu.Unlock()
		q := newFake(options, true)
		q.models = []protocol.ModelInfo{}
		q.usage = func(context.Context) (jsontext.Value, error) { return jsontext.Value(`{}`), nil }
		return q, nil
	}
	ctx := context.Background()
	_, _ = discoverClaudeCommands(ctx, factory, "", "/tmp/project", false)
	discoverClaudeModels(ctx, factory)
	suggestTaskTitle(ctx, factory, "name this", nil)
	readPlanUsage(ctx, factory, time.Second)
	await(t, execute(NewProvider(nil, factory, ""), runInput(&recorder{})))
	assertEqual(t, len(seen), 5, "every entry point opens a process")
	for _, options := range seen {
		assertEqual(t, options.Env, map[string]string{"CLAUDE_CODE_ENTRYPOINT": "aicodingtool", "CLAUDE_CODE_DISABLE_ARTIFACT": "1"}, "env")
		if options.Command == nil {
			t.Fatal("every process starts through platform.Command")
		}
		environment := options.Environment(options.Command(options.Executable).Env)
		assertEqual(t, envValue(environment, "CLAUDE_CODE_ENTRYPOINT"), "aicodingtool", "named in place of any inherited name")
		assertEqual(t, envValue(environment, "HOME"), os.Getenv("HOME"), "the rest of the environment is kept")
	}
}

func envValue(environment []string, key string) string {
	value := ""
	for _, entry := range environment {
		if name, found, ok := strings.Cut(entry, "="); ok && name == key {
			value = found
		}
	}
	return value
}

func TestTheProcessExitDiagnosticCarriesItsStderrTail(t *testing.T) {
	factory := func(_ context.Context, options protocol.Options) (Query, error) {
		q := newFake(options, true)
		options.Stderr(strings.Repeat("x", 512*1024) + "\nfinal diagnostic")
		q.failure = errors.New("Claude Code process exited with code 7")
		return q, nil
	}
	provider := NewProvider(nil, factory, "")
	defer provider.CloseAll()
	result := await(t, execute(provider, runInput(&recorder{})))
	assertEqual(t, result.Status, domain.RunStatusFailed, "status")
	if !strings.Contains(result.Message, "exited with code 7. stderr:") || !strings.HasSuffix(result.Message, "final diagnostic") {
		t.Fatalf("message %q", result.Message[max(0, len(result.Message)-80):])
	}
	if len(result.Message) >= 8500 {
		t.Fatal("diagnostics stay bounded")
	}
}

func TestAHostedServerKeepsEveryToolUnderItsName(t *testing.T) {
	server := claudeMcpServer(agent.BrowserServerName, []agent.BoundTool{{Name: "browser_read", Input: jsontext.Value(`{"type":"object"}`)}})
	assertEqual(t, server.Name, agent.BrowserServerName, "name")
	assertEqual(t, server.AlwaysLoad, true, "always load")
	assertEqual(t, server.Tools[0].Name, "browser_read", "tool")
}
