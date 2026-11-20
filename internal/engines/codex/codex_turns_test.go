package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

func review(target domain.ReviewTarget) inputOption {
	return func(in *agentInput) { in.Operation = contracts.RunOperationReview{Target: target} }
}

func TestNativeReviewRunsInlineAndEmitsItsFindingsOnce(t *testing.T) {
	events := &recorder{}
	reports := &reportRecorder{}
	h := newHarness(t, nil, harnessOptions{})
	warm, _ := h.turn(with(), nil)
	reviewing := review(domain.ReviewTargetBaseBranch{Branch: "main"})
	run := h.execute(with(prompt(""), continuing(threadID), func(in *agentInput) {
		in.Model = "gpt-6.1-terra"
		in.Effort = domain.AgentEffortLow
		in.Policy = domain.ExecutionPolicyAllowEdits
		in.Emit = events.emit
		in.ReportSubagent = reports.report
	}, reviewing))
	eventually(t, "review session was never opened", func() bool { return len(h.all()) == 2 })
	client := h.latest()
	sentBy(t, client, "review/start", 1)
	if !warm.isClosed() {
		t.Fatal("review reopens the thread because review/start has no setting overrides")
	}
	assertJSON(t, client.calls("thread/resume"), []any{map[string]any{"threadId": threadID, "cwd": "/tmp/project", "serviceTier": "default", "model": "gpt-6.1-terra", "approvalPolicy": "on-request", "sandbox": "workspace-write", "approvalsReviewer": "user", "config": map[string]any{"model_reasoning_effort": "low"}, "developerInstructions": developerInstructions(reviewing), "excludeTurns": true}})
	assertJSON(t, client.calls("review/start"), `[{"threadId":"thread-1","target":{"type":"baseBranch","branch":"main"},"delivery":"inline"}]`)
	client.notify("item/started", startedItem(commandItem("review-command", "git diff main")))
	client.notify("item/completed", completedItem(commandItem("review-command", "git diff main")))
	result := `{"type":"exitedReviewMode","id":"review-1","review":"No findings."}`
	client.notify("item/completed", completedItem(result))
	client.notify("item/completed", completedItem(result))
	client.notify("item/agentMessage/delta", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "review-message", "delta": "No findings."})
	client.notify("item/completed", completedItem(agentMessageItem("review-message", "No findings.")))
	completeTurn(client, "completed", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	assertJSON(t, events.shapes("continuation"), `[
		{"type":"tool","intent":{"toolId":"review-command","name":"command_execution","input":{"command":"git diff main","cwd":"/tmp/project"}}},
		{"type":"assistant","messageId":"review-1","text":"No findings."}]`)
	if len(client.calls("thread/inject_items")) != 0 || len(reports.all()) != 0 {
		t.Fatal("inline review belongs to the conversation")
	}
	followup := &recorder{}
	h.turn(with(continuing(threadID), emitting(followup)), func(next *fakeClient) {
		next.notify("item/completed", completedItem(agentMessageItem("followup", "I will fix the findings.")))
	})
	found := false
	for _, event := range followup.ofType("assistant") {
		found = found || event["text"] == "I will fix the findings."
	}
	if !found {
		t.Fatal("a follow-up turn streams again")
	}
}

func TestInlineReviewHandlesFindingsBeforeReviewStartRespondsEvenWithAnActiveGoal(t *testing.T) {
	events := &recorder{}
	answer := make(chan struct{})
	h := newHarness(t, script{"review/start": func(context.Context, jsontext.Value) (any, error) {
		<-answer
		return jsontext.Value(turnJSON(turnID)), nil
	}}, harnessOptions{})
	run := h.execute(with(review(domain.ReviewTargetUncommittedChanges{}), emitting(events)))
	client := h.opened()
	sentBy(t, client, "review/start", 1)
	client.notify("thread/goal/updated", map[string]any{"threadId": threadID, "turnId": turnID, "goal": map[string]any{"threadId": threadID, "objective": "Fix the app", "status": "active", "tokenBudget": nil, "tokensUsed": 0, "timeUsedSeconds": 0, "createdAt": 1, "updatedAt": 1}})
	client.notify("item/completed", completedItem(`{"type":"exitedReviewMode","id":"early-review","review":"[P1] Fix the race."}`))
	completeTurn(client, "completed", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	close(answer)
	tick()
	assertJSON(t, events.ofType("assistant"), `[{"type":"assistant","messageId":"early-review","text":"[P1] Fix the race."}]`)
}

func TestInlineReviewReportsStartupAndExecutionFailures(t *testing.T) {
	startup := newHarness(t, script{"review/start": refuse(errors.New("Review target is missing"))}, harnessOptions{})
	result, _ := startup.provider.Execute(with(review(domain.ReviewTargetUncommittedChanges{})))
	assertJSON(t, result, agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start the review: Review target is missing"})

	h := newHarness(t, nil, harnessOptions{})
	run := h.execute(with(review(domain.ReviewTargetUncommittedChanges{})))
	client := h.opened()
	sentBy(t, client, "review/start", 1)
	completeTurn(client, "failed", ptr("Reviewer failed"))
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Reviewer failed"})
}

func TestInlineReviewSteersAndInterruptsTheParentTurnIncludingDuringStartup(t *testing.T) {
	for _, cancelBeforeReply := range []bool{false, true} {
		answer := make(chan struct{})
		h := newHarness(t, script{"review/start": func(context.Context, jsontext.Value) (any, error) {
			<-answer
			return jsontext.Value(turnJSON(turnID)), nil
		}}, harnessOptions{})
		steering := agent.NewSteerChannel()
		in := with(review(domain.ReviewTargetUncommittedChanges{}), func(in *agentInput) { in.Steering = steering })
		run := h.execute(in)
		client := h.opened()
		sentBy(t, client, "review/start", 1)
		if cancelBeforeReply {
			in.Abort()
		}
		close(answer)
		if !cancelBeforeReply {
			steering.Push(agent.SteerMessage{MessageID: "review-steer", Prompt: "Focus on concurrency"})
			sentBy(t, client, "turn/steer", 1)
			assertJSON(t, client.calls("turn/steer"), `[{"threadId":"thread-1","expectedTurnId":"turn-1","input":[{"type":"text","text":"Focus on concurrency","text_elements":[]}]}]`)
			in.Abort()
		}
		sentBy(t, client, "turn/interrupt", 1)
		assertJSON(t, client.calls("turn/interrupt"), `[{"threadId":"thread-1","turnId":"turn-1"}]`)
		completeTurn(client, "interrupted", nil)
		assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusCancelled})
		h.provider.CloseAll()
	}
}

const imageGeneration = `{"type":"imageGeneration","id":"image-1","status":"completed","revisedPrompt":"Cat","result":"image bytes","failure":null,"generationId":null}`

func TestImageGenerationReportsProgressAndWaitsForADurableArtifact(t *testing.T) {
	events := &recorder{}
	saved := make(chan string)
	var saves atomic.Int32
	h := newHarness(t, nil, harnessOptions{imageOutput: func(_ context.Context, item protocol.ThreadItem, root string) (string, error) {
		if item.ID != "image-1" || root != "/tmp/project" {
			t.Errorf("saved %s under %s", item.ID, root)
		}
		saves.Add(1)
		return <-saved, nil
	}})
	run := h.execute(with(emitting(events)))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.notify("item/started", startedItem(`{"type":"imageGeneration","id":"image-1","status":"in_progress","revisedPrompt":"Cat","result":"image bytes","failure":null,"generationId":null}`))
	client.notify("item/completed", completedItem(imageGeneration))
	client.notify("item/completed", completedItem(imageGeneration))
	completeTurn(client, "completed", nil)
	tick()
	if run.settled() || saves.Load() != 1 {
		t.Fatalf("settled %v, saves %d", run.settled(), saves.Load())
	}
	assertJSON(t, events.ofType("tool"), `[{"type":"tool","intent":{"toolId":"image-1","name":"image_generation","input":{"description":"Generating image"}}}]`)
	saved <- "[Generated image](/tmp/cat.png)"
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusSucceeded})
	assertJSON(t, events.ofType("assistant"), `[{"type":"assistant","messageId":"image-1","text":"[Generated image](/tmp/cat.png)","artifact":true}]`)
}

func TestAFailureToSaveAnImageStaysVisibleAsAnArtifactFailure(t *testing.T) {
	events := &recorder{}
	h := newHarness(t, nil, harnessOptions{imageOutput: func(context.Context, protocol.ThreadItem, string) (string, error) {
		return "", errors.New("Disk full")
	}})
	h.turn(with(emitting(events)), func(client *fakeClient) { client.notify("item/completed", completedItem(imageGeneration)) })
	assertJSON(t, events.ofType("assistant"), `[{"type":"assistant","messageId":"image-1","text":"The generated image could not be saved or displayed: Disk full","artifact":true}]`)
}

func TestAnImageFinishingAfterCancellationDoesNotEnterALaterRun(t *testing.T) {
	events := &recorder{}
	saved := make(chan string)
	h := newHarness(t, nil, harnessOptions{imageOutput: func(context.Context, protocol.ThreadItem, string) (string, error) { return <-saved, nil }})
	in := with(emitting(events))
	run := h.execute(in)
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.notify("item/completed", completedItem(imageGeneration))
	in.Abort()
	saved <- "[Generated image](/tmp/cat.png)"
	completeTurn(client, "interrupted", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusCancelled})
	if len(events.ofType("assistant")) != 0 {
		t.Fatal("a cancelled run shows no late image")
	}
}

func spendUsage(total, last float64) map[string]any {
	breakdown := func(tokens float64) map[string]any {
		return map[string]any{"totalTokens": tokens, "inputTokens": tokens, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0}
	}
	return map[string]any{"threadId": "thread-9", "turnId": turnID, "tokenUsage": map[string]any{"total": breakdown(total), "last": breakdown(last), "modelContextWindow": 272_000}}
}

func TestAResumedThreadCountsOnlyWhatThisTurnSpent(t *testing.T) {
	spent := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(continuing("thread-9"), emitting(spent)), func(client *fakeClient) {
		client.notify("thread/tokenUsage/updated", spendUsage(480_000, 1_200))
		client.notify("thread/tokenUsage/updated", spendUsage(481_000, 1_000))
	})
	client.notify("thread/tokenUsage/updated", spendUsage(483_000, 2_000))
	h.turn(with(continuing("thread-9"), emitting(spent)), func(client *fakeClient) {
		client.notify("thread/tokenUsage/updated", spendUsage(483_500, 500))
	})
	assertJSON(t, spent.ofType("spend"), `[{"type":"spend","tokens":1200},{"type":"spend","tokens":1000},{"type":"spend","tokens":500}]`, "neither the history it resumed nor what it spent between runs counts")
}

func TestACompactionsRecountIsNoResponseSoAThreadFirstSeenThroughOneSpendsNothing(t *testing.T) {
	spent := &recorder{}
	h := newHarness(t, nil, harnessOptions{})
	h.turn(with(continuing("thread-9"), emitting(spent)), func(client *fakeClient) {
		zero := map[string]any{"inputTokens": 0, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0}
		total := map[string]any{"totalTokens": 900_000, "inputTokens": 880_000, "cachedInputTokens": 0, "cacheWriteInputTokens": 0, "outputTokens": 20_000, "reasoningOutputTokens": 0}
		last := map[string]any{"totalTokens": 13_779}
		for key, value := range zero {
			last[key] = value
		}
		client.notify("thread/tokenUsage/updated", map[string]any{"threadId": "thread-9", "turnId": turnID, "tokenUsage": map[string]any{"total": total, "last": last, "modelContextWindow": 272_000}})
	})
	if len(spent.ofType("spend")) != 0 {
		t.Fatal("a recount spends nothing")
	}
}

func agentQuestion(text string, options string) string {
	return `{"type":"agentMessage","moderation":null,"id":"question","text":"","phase":"commentary","memoryCitation":null,"delivery":"async","questions":[{"title":"` + text + `","options":` + options + `}]}`
}

func TestAsyncAssistantQuestionsDeliverTypedAnswersIntoTheActiveTurn(t *testing.T) {
	var mu sync.Mutex
	var requests []domain.QuestionRequest
	answers := make(chan domain.QuestionAnswers)
	session := asking(t, deny, policy(domain.ExecutionPolicyBypass), func(in *agentInput) {
		in.AskQuestion = func(_ context.Context, request domain.QuestionRequest) (domain.QuestionAnswers, bool) {
			mu.Lock()
			requests = append(requests, request)
			mu.Unlock()
			return <-answers, true
		}
	})
	session.client.notify("item/completed", completedItem(agentQuestion("Which region?", `["Chicago","London"]`)))
	eventually(t, "the question was never asked", func() bool { mu.Lock(); defer mu.Unlock(); return len(requests) == 1 })
	assertJSON(t, requests, `[{"blocking":false,"questions":[{"id":"0","header":"Question","question":"Which region?","options":[{"label":"Chicago","description":""},{"label":"London","description":""}]}]}]`)
	if len(session.client.calls("turn/steer")) != 0 {
		t.Fatal("nothing is steered before the answer")
	}
	answers <- domain.QuestionAnswers{"0": "Chicago, please"}
	sentBy(t, session.client, "turn/steer", 1)
	assertJSON(t, session.client.calls("turn/steer"), `[{"threadId":"thread-1","expectedTurnId":"turn-1","input":[{"type":"text","text":"Which region?\nChicago, please","text_elements":[]}]}]`)
	if len(session.asked.all()) != 0 {
		t.Fatal("no approval is asked")
	}
	session.end(t)
}

func TestBypassQuestionsStillWaitForInputAndServerWithdrawalCancelsThem(t *testing.T) {
	signals := make(chan context.Context, 1)
	session := asking(t, deny, policy(domain.ExecutionPolicyBypass), func(in *agentInput) {
		in.AskQuestion = func(ctx context.Context, _ domain.QuestionRequest) (domain.QuestionAnswers, bool) {
			signals <- ctx
			<-ctx.Done()
			return nil, false
		}
	})
	session.client.send("item/tool/requestUserInput", map[string]any{"threadId": threadID, "turnId": turnID, "itemId": "ask", "questions": []any{map[string]any{"id": "q", "header": "Region", "question": "Which region?", "isOther": false, "isSecret": false, "options": nil}}, "isBlocking": false, "autoResolutionMs": nil})
	signal := <-signals
	if signal.Err() != nil {
		t.Fatal("the question waits")
	}
	session.client.notify("serverRequest/resolved", map[string]any{"threadId": threadID, "requestId": 0})
	eventually(t, "the question was never withdrawn", func() bool { return signal.Err() != nil })
	session.end(t)
}

func TestAnAsynchronousAnswerCannotSteerATurnThatHasAlreadyEnded(t *testing.T) {
	answers := make(chan domain.QuestionAnswers)
	asked := make(chan struct{}, 1)
	session := asking(t, deny, func(in *agentInput) {
		in.AskQuestion = func(context.Context, domain.QuestionRequest) (domain.QuestionAnswers, bool) {
			asked <- struct{}{}
			return <-answers, true
		}
	})
	session.client.notify("item/completed", completedItem(agentQuestion("Which region?", "null")))
	<-asked
	session.end(t)
	answers <- domain.QuestionAnswers{"0": "Chicago"}
	tick()
	if len(session.client.calls("turn/steer")) != 0 {
		t.Fatal("an ended turn takes no answer")
	}
}

func TestAResumedSessionReceivesTheTitleSavedWhileItsPreviousSessionWasClosed(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	first, _ := h.turn(with(func(in *agentInput) { in.Title = "Original title" }), nil)
	h.provider.CloseAll()
	if h.provider.LabelThread("task-1", "Renamed while closed") {
		t.Fatal("a closed session takes no label")
	}
	resumed, _ := h.turn(with(func(in *agentInput) { in.Title = "Renamed while closed" }, continuing(threadID)), nil)
	if resumed == first || len(resumed.calls("thread/resume")) != 1 {
		t.Fatal("the thread is resumed on a new server")
	}
	sentBy(t, resumed, "thread/name/set", 1)
	assertJSON(t, resumed.calls("thread/name/set"), `[{"threadId":"thread-1","name":"Renamed while closed"}]`)
}

func TestAForkReceivesItsOwnSavedTitle(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(func(in *agentInput) { in.Title = "Review copy"; in.ForkContinuation = true }, continuing(threadID)), nil)
	sentBy(t, client, "thread/name/set", 1)
	assertJSON(t, client.calls("thread/name/set"), `[{"threadId":"thread-fork","name":"Review copy"}]`)
}

func TestAFailedTitleWriteIsRetriedOnTheNextRunWithoutFailingEitherRun(t *testing.T) {
	var attempts atomic.Int32
	h := newHarness(t, script{"thread/name/set": func(context.Context, jsontext.Value) (any, error) {
		if attempts.Add(1) == 1 {
			return nil, &protocol.AppServerError{Method: "thread/name/set", Code: -32603, Message: "write failed"}
		}
		return jsontext.Value(`{}`), nil
	}}, harnessOptions{})
	titled := func(in *agentInput) { in.Title = "Saved title" }
	first, result := h.turn(with(titled), nil)
	eventually(t, "the title was never written", func() bool { return attempts.Load() == 1 })
	tick()
	second, again := h.turn(with(titled, continuing(threadID)), nil)
	if result.Status != domain.RunStatusSucceeded || again.Status != domain.RunStatusSucceeded || first != second {
		t.Fatal("both runs succeed on one session")
	}
	eventually(t, "the title was never retried", func() bool { return attempts.Load() == 2 })
}

func TestCodexIsToldTheTitleAndWhereTheWorkBelongsOnceTheTurnHasARollout(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{readOrigin: func(context.Context, string) GitOrigin {
		return GitOrigin{OriginURL: ptr("git@github.com:me/app.git"), Branch: ptr("feature"), Sha: ptr("abc123")}
	}})
	client, _ := h.turn(with(), nil)
	sentBy(t, client, "thread/metadata/update", 1)
	assertJSON(t, client.calls("thread/metadata/update"), `[{"threadId":"thread-1","gitInfo":{"originUrl":"git@github.com:me/app.git","branch":"feature","sha":"abc123"}}]`)
	sentBy(t, client, "thread/name/set", 1)
	assertJSON(t, client.calls("thread/name/set"), `[{"threadId":"thread-1","name":"Inspect the app"}]`)
	if !h.provider.LabelThread("task-1", "Inspect the app") {
		t.Fatal("the live session takes the label")
	}
	tick()
	if len(client.calls("thread/name/set")) != 1 {
		t.Fatal("the same title is not sent twice")
	}
	h.provider.LabelThread("task-1", "Review the app")
	again, _ := h.turn(with(func(in *agentInput) { in.Title = "Review the app" }, continuing(threadID)), nil)
	if again != client {
		t.Fatal("the warm session takes the turn")
	}
	sentBy(t, client, "thread/name/set", 2)
	tick()
	assertJSON(t, client.calls("thread/name/set"), `[{"threadId":"thread-1","name":"Inspect the app"},{"threadId":"thread-1","name":"Review the app"}]`)
}

func TestATitleThatLandsBeforeTheFirstTurnWaitsForTheRollout(t *testing.T) {
	letTurnStart := make(chan struct{})
	h := newHarness(t, script{"turn/start": func(context.Context, jsontext.Value) (any, error) {
		<-letTurnStart
		return jsontext.Value(turnJSON(turnID)), nil
	}}, harnessOptions{})
	run := h.execute(with())
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	h.provider.LabelThread("task-1", "Early title")
	tick()
	if len(client.calls("thread/name/set")) != 0 {
		t.Fatal("Codex has no rollout to name until the turn has begun")
	}
	close(letTurnStart)
	sentBy(t, client, "thread/name/set", 1)
	assertJSON(t, client.calls("thread/name/set"), `[{"threadId":"thread-1","name":"Early title"}]`)
	completeTurn(client, "completed", nil)
	run.wait(t)
}

func TestArchivingLetsTheSessionGoBeforeAServerOfItsOwnFilesTheThreadAway(t *testing.T) {
	h := newHarness(t, script{"thread/archive": reply(`{}`)}, harnessOptions{})
	session, _ := h.turn(with(), nil)
	continuation := &domain.Continuation{Provider: string(domain.AgentEngineCodex), Value: threadID}
	if !h.provider.ArchiveThread("task-1", continuation, true) {
		t.Fatal("the thread is filed")
	}
	eventually(t, "no archiving server was opened", func() bool { return len(h.all()) == 2 })
	archiver := h.latest()
	sentBy(t, archiver, "thread/archive", 1)
	if !session.isClosed() {
		t.Fatal("the session holding the thread is gone first")
	}
	assertJSON(t, archiver.calls("thread/archive"), `[{"threadId":"thread-1"}]`)
	closedEventually(t, archiver)
	h.provider.ArchiveThread("task-1", continuation, false)
	eventually(t, "no restoring server was opened", func() bool { return len(h.all()) == 3 })
	sentBy(t, h.latest(), "thread/unarchive", 1)
	assertJSON(t, h.latest().calls("thread/unarchive"), `[{"threadId":"thread-1"}]`)
	if h.provider.ArchiveThread("task-1", &domain.Continuation{Provider: "claude", Value: "session"}, true) {
		t.Fatal("another engine's thread is not Codex's to file")
	}
}

func TestASessionServesTheRunsToolsUnderOneTokenAndPointsTheServerAtThem(t *testing.T) {
	restore := installFakeToolServers(t)
	defer restore()
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(), nil)
	found := overrides(client.command.Args)
	if found["mcp_servers.aicodingtool.url"] != `"`+fakeHostURL+`"` || found["mcp_servers.aicodingtool.bearer_token_env_var"] != `"AICODINGTOOL_MCP_TOKEN"` || found["mcp_servers.aicodingtool.default_tools_approval_mode"] != `"prompt"` {
		t.Fatalf("overrides = %v", found)
	}
	env := map[string]string{}
	for _, entry := range client.command.Env {
		for index := range entry {
			if entry[index] == '=' {
				env[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	if env["AICODINGTOOL_MCP_TOKEN"] != "token-1" || env["PATH"] == "" {
		t.Fatalf("env token = %q, PATH = %q", env["AICODINGTOOL_MCP_TOKEN"], env["PATH"])
	}
	h.host.mu.Lock()
	served := h.host.served
	h.host.mu.Unlock()
	if len(served) != 1 || len(served[0].tools) != 2 {
		t.Fatalf("served = %+v", served)
	}
	if decodeAny(client.calls("thread/start")[0]).(map[string]any)["developerInstructions"] != developerInstructions() {
		t.Fatal("the brief names the served tools")
	}
	h.turn(with(prompt("again"), continuing(threadID)), nil)
	h.host.mu.Lock()
	if len(h.host.served) != 1 || h.host.served[0].released {
		t.Fatal("a warm session keeps its token")
	}
	h.host.mu.Unlock()
	h.provider.CloseAll()
	h.host.mu.Lock()
	defer h.host.mu.Unlock()
	if !h.host.served[0].released {
		t.Fatal("closing the session forgets its token")
	}
}

func TestARunWithNoToolsIsServedNothingAndAPolicySwitchRidesTheWarmSession(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	first, _ := h.turn(with(policy(domain.ExecutionPolicyConfirm)), nil)
	if _, ok := overrides(first.command.Args)["mcp_servers.cua-driver.command"]; ok || len(h.host.served) != 0 {
		t.Fatal("Codex reads its own skills without an app tool server")
	}
	second, _ := h.turn(with(policy(domain.ExecutionPolicyAutonomous), continuing(threadID)), nil)
	if second != first {
		t.Fatal("with computer use off, a policy switch rides the warm session")
	}
	available := func(in *agentInput) {
		in.ComputerUse = domain.ComputerUseRunConfigAvailable{MCP: domain.ComputerUseMCP{Command: "/app/cua-driver", Args: []string{"mcp"}, Env: map[string]string{}}}
	}
	confirmed, _ := h.turn(with(available, policy(domain.ExecutionPolicyConfirm), func(in *agentInput) { in.TaskID = "task-2" }), nil)
	granted, _ := h.turn(with(available, policy(domain.ExecutionPolicyAutonomous), func(in *agentInput) { in.TaskID = "task-2" }, continuing(threadID)), nil)
	if granted == confirmed || overrides(granted.command.Args)["mcp_servers.cua-driver.default_tools_approval_mode"] != `"approve"` {
		t.Fatal("the grant is the process's, so the policy switch opens a new one")
	}
	closedEventually(t, confirmed)
}
