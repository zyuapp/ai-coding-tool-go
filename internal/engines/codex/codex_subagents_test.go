package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const rootID = "thread-1"

type reportRecorder struct {
	mu      sync.Mutex
	reports []domain.SubagentReport
}

func (r *reportRecorder) report(report domain.SubagentReport) {
	r.mu.Lock()
	r.reports = append(r.reports, report)
	r.mu.Unlock()
}

func (r *reportRecorder) all() []domain.SubagentReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.SubagentReport(nil), r.reports...)
}

func (r *reportRecorder) shapes(kind string) []map[string]any {
	var shapes []map[string]any
	for _, report := range r.all() {
		shape := decodeAny(encodedOf(report)).(map[string]any)
		if kind == "" || shape["type"] == kind {
			shapes = append(shapes, shape)
		}
	}
	return shapes
}

func decoded[T any](t *testing.T, text string) T {
	t.Helper()
	var value T
	must(t, jsonx.Unmarshal([]byte(text), &value))
	return value
}

func threadItem(t *testing.T, text string) protocol.ThreadItem {
	return decoded[protocol.ThreadItem](t, text)
}

func activityItem(id, childID, path, kind string) string {
	return `{"type":"subAgentActivity","id":"` + id + `","kind":"` + kind + `","agentThreadId":"` + childID + `","agentPath":"` + path + `","model":null,"reasoningEffort":null}`
}

func started(t *testing.T, thread, turn, item string) protocol.ItemStartedNotification {
	return protocol.ItemStartedNotification{ThreadID: thread, TurnID: turn, Item: threadItem(t, item), StartedAtMs: 1}
}

func completed(t *testing.T, thread, turn, item string) protocol.ItemCompletedNotification {
	return protocol.ItemCompletedNotification{ThreadID: thread, TurnID: turn, Item: threadItem(t, item), CompletedAtMs: 2}
}

func turnOf(thread, id string, status protocol.TurnStatus) protocol.TurnNotification {
	return protocol.TurnNotification{ThreadID: thread, Turn: protocol.Turn{ID: id, Status: status}}
}

func spawnedThread(t *testing.T, id, preview, nickname, role, path, extra string) protocol.ThreadStartedNotification {
	return decoded[protocol.ThreadStartedNotification](t, `{"thread":{"id":"`+id+`","preview":"`+preview+`","source":{"subAgent":{"thread_spawn":{"parent_thread_id":"`+rootID+`","depth":1,"agent_path":"`+path+`","agent_nickname":"`+nickname+`","agent_role":"`+role+`"}}},"agentNickname":"`+nickname+`","agentRole":"`+role+`","parentThreadId":"`+rootID+`","status":{"type":"idle"}`+extra+`}}`)
}

func memoryThread(t *testing.T) protocol.ThreadStartedNotification {
	return decoded[protocol.ThreadStartedNotification](t, `{"thread":{"id":"memory-thread","preview":"","source":{"subAgent":"memory_consolidation"},"threadSource":"memory_consolidation","status":{"type":"idle"}}}`)
}

func usageOf(thread, turn string, total, last float64) protocol.ThreadTokenUsageUpdatedNotification {
	breakdown := func(tokens float64) protocol.TokenUsageBreakdown {
		return protocol.TokenUsageBreakdown{TotalTokens: tokens, InputTokens: tokens}
	}
	return protocol.ThreadTokenUsageUpdatedNotification{ThreadID: thread, TurnID: turn, TokenUsage: protocol.ThreadTokenUsage{Total: breakdown(total), Last: breakdown(last), ModelContextWindow: ptr(272_000.0)}}
}

const childCommand = `{"type":"commandExecution","id":"%s","pluginId":null,"scriptPath":null,"command":"rg TODO src","cwd":"/tmp/project","processId":null,"source":"agent","status":"inProgress","commandActions":[],"aggregatedOutput":null,"exitCode":null,"durationMs":null}`

func childCommandItem(id string) string { return strings.Replace(childCommand, "%s", id, 1) }

func TestCodexCapturesModelAndEffortFromTheChildAndTheFullPromptFromChildInput(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	prompt := "  Inspect carefully.\n\n" + strings.Repeat("Keep every instruction.\n", 10_000)
	input := `{"type":"userMessage","id":"input","clientId":null,"content":[{"type":"text","text":` + string(encodedOf(prompt)) + `,"text_elements":[]}]}`
	tracker.ItemStarted(started(t, "child", "turn", input))
	tracker.ThreadStarted(spawnedThread(t, "child", "Inspect carefully", "reviewer", "reviewer", "/root/reviewer", `,"model":"gpt-6.2-sol","reasoningEffort":"xhigh"`))
	start := reports.shapes("subagent.started")[0]
	if start["prompt"] != prompt || start["model"] != "gpt-6.2-sol" || start["effort"] != "xhigh" || start["description"] != utf16Prefix(jsTrim(prompt), 100_000) {
		t.Fatalf("started = %v", start["description"])
	}
	tracker.ItemCompleted(completed(t, "child", "turn", input))
	tracker.ItemStarted(started(t, "child", "turn-2", strings.Replace(strings.Replace(input, `"id":"input"`, `"id":"followup"`, 1), string(encodedOf(prompt)), `"Follow up"`, 1)))
	if len(reports.shapes("subagent.metadata")) != 0 {
		t.Fatal("follow-ups do not replace the original assignment")
	}
}

func TestCodexEnrichesADiscoveredChildWithCollaborationInputThenThreadSettings(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	tracker.ItemStarted(started(t, rootID, "turn", activityItem("discover", "child", "/root/reviewer", "started")))
	tracker.ItemCompleted(completed(t, rootID, "turn", `{"type":"collabAgentToolCall","id":"spawn","tool":"spawnAgent","status":"completed","senderThreadId":"`+rootID+`","receiverThreadIds":["child"],"prompt":"  Full\nassignment  ","model":"gpt-6.2-sol","reasoningEffort":"high","agentsStates":{}}`))
	tracker.ThreadStarted(spawnedThread(t, "child", "Review", "reviewer", "reviewer", "/root/reviewer", `,"model":"gpt-6.2-astra","reasoningEffort":"xhigh"`))
	details := reports.shapes("subagent.metadata")
	assertJSON(t, details[len(details)-1], `{"type":"subagent.metadata","id":"child","prompt":"  Full\nassignment  ","model":"gpt-6.2-astra","effort":"xhigh"}`)
}

func TestMultiAgentV2DiscoveryExposesChildSettingsWithoutAPlaintextPrompt(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	tracker.ItemStarted(started(t, rootID, "turn", activityItem("discover", "child", "/root/reviewer", "started")))
	tracker.ThreadStarted(spawnedThread(t, "child", "", "Mencius", "reviewer", "/root/reviewer", `,"model":"gpt-6.2-astra","reasoningEffort":"high"`))
	details := reports.shapes("subagent.metadata")
	assertJSON(t, details[len(details)-1], `{"type":"subagent.metadata","id":"child","model":"gpt-6.2-astra","effort":"high"}`)
	for _, report := range reports.shapes("") {
		if _, ok := report["prompt"]; ok {
			t.Fatal("encrypted NEW_TASK bodies are not available on the app-server event path")
		}
	}
}

func TestChildFirstTrafficIsBufferedBothDiscoveryPathsMergeAndRootSelfActivityIsRejected(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	tracker.ThreadStatusChanged(protocol.ThreadStatusChangedNotification{ThreadID: "child-a", Status: protocol.ThreadStatus{Type: "idle"}})
	if len(reports.all()) != 0 {
		t.Fatal("an undiscovered child reports nothing")
	}
	tracker.ItemStarted(started(t, rootID, "root-turn", activityItem("activity-a", "child-a", "/root/reviewer", "interacted")))
	tracker.ThreadStarted(spawnedThread(t, "child-a", "reviewer", "wire", "reviewer", "/root/reviewer", ""))
	tracker.ThreadStarted(spawnedThread(t, "child-b", "Check performance", "perf", "reviewer", "/root/perf", ""))
	tracker.ItemCompleted(completed(t, rootID, "root-turn", activityItem("activity-b", "child-b", "/root/perf", "started")))
	tracker.ItemStarted(started(t, "child-a", "child-turn", activityItem("root-self", rootID, "/root", "interacted")))
	var ids []any
	for _, report := range reports.shapes("subagent.started") {
		ids = append(ids, report["id"])
	}
	assertJSON(t, ids, `["child-a","child-b"]`)
	assertJSON(t, reports.shapes("subagent.status"), `[{"type":"subagent.status","id":"child-a","status":"idle"}]`)
	var last map[string]any
	for _, report := range reports.shapes("subagent.progress") {
		if report["id"] == "child-a" {
			last = report
		}
	}
	if last["agentType"] != "reviewer" {
		t.Fatalf("progress = %v", last)
	}
	for _, report := range reports.shapes("") {
		if report["id"] == rootID {
			t.Fatal("the root never reports as a child")
		}
	}
}

func TestAChildThreadsGrowthIsCountedAndHousekeepingIsNot(t *testing.T) {
	spent := &recorder{}
	tracker := NewCodexSubagents(func(domain.SubagentReport) {}, nil, nil, NewCodexSpend(spent.emit))
	tracker.SetRootThreadID(rootID)
	tracker.ItemStarted(started(t, rootID, "root-turn", activityItem("discover", "child-a", "/root/reviewer", "started")))
	tracker.TokenUsageUpdated(usageOf("child-a", "child-turn", 5_000, 300))
	tracker.TokenUsageUpdated(usageOf("child-a", "child-turn", 5_450, 450))
	tracker.TokenUsageUpdated(usageOf("child-a", "child-turn", 5_450, 450))
	tracker.ThreadStarted(memoryThread(t))
	tracker.TokenUsageUpdated(usageOf("memory-thread", "memory-turn", 900, 900))
	assertJSON(t, spent.shapes(), `[{"type":"spend","tokens":300},{"type":"spend","tokens":450}]`)
}

func TestInternalHousekeepingThreadsNeitherAppearNorKeepTheSessionBusy(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	tracker.TurnStarted(turnOf("memory-thread", "memory-turn", protocol.TurnInProgress))
	if !tracker.Busy() {
		t.Fatal("an unknown thread's turn is work until it is known")
	}
	tracker.ThreadStarted(memoryThread(t))
	tracker.TurnStarted(turnOf("memory-thread", "memory-turn-2", protocol.TurnInProgress))
	if tracker.Busy() || len(reports.all()) != 0 {
		t.Fatal("housekeeping is invisible")
	}
}

func TestChildMessagesToolsUsageResumeAndTerminalErrorsBecomeReports(t *testing.T) {
	reports := &reportRecorder{}
	tracker := NewCodexSubagents(reports.report, nil, nil, nil)
	tracker.SetRootThreadID(rootID)
	tracker.ItemStarted(started(t, rootID, "root-turn", activityItem("discover", "child-a", "/root/reviewer", "started")))
	tracker.TurnStarted(turnOf("child-a", "child-turn", protocol.TurnInProgress))
	tracker.ItemStarted(started(t, "child-a", "child-turn", childCommandItem("command-1")))
	tracker.ItemCompleted(completed(t, "child-a", "child-turn", agentMessageItem("message-1", "Found the issue.")))
	tracker.TokenUsageUpdated(usageOf("child-a", "child-turn", 120, 7))
	tracker.TurnCompleted(turnOf("child-a", "child-turn", protocol.TurnCompleted))
	if _, ok := tracker.PendingItem("child-a", "command-1"); ok {
		t.Fatal("a finished turn's items are dropped")
	}
	tracker.ThreadStatusChanged(protocol.ThreadStatusChangedNotification{ThreadID: "child-a", Status: protocol.ThreadStatus{Type: "idle"}})
	tracker.TurnStarted(turnOf("child-a", "child-turn-2", protocol.TurnInProgress))
	tracker.TokenUsageUpdated(usageOf("child-a", "child-turn-2", 120, 0))
	tracker.Error(protocol.ErrorNotification{ThreadID: "child-a", TurnID: "child-turn", Error: protocol.TurnError{Message: "stale failure"}})
	assertJSON(t, tracker.LiveTurns(), `[{"threadId":"child-a","turnId":"child-turn-2"}]`)
	if turn, _ := tracker.Stop("child-a"); turn == nil || *turn != (CodexChildTurn{"child-a", "child-turn-2"}) {
		t.Fatalf("stop = %v", turn)
	}
	if turn, held := tracker.Stop("stranger"); turn != nil || held {
		t.Fatal("an id no child has is left for a terminal")
	}
	tracker.Error(protocol.ErrorNotification{ThreadID: "child-a", TurnID: "child-turn-2", WillRetry: true, Error: protocol.TurnError{Message: "retry"}})
	tracker.Error(protocol.ErrorNotification{ThreadID: "child-a", TurnID: "child-turn-2", Error: protocol.TurnError{Message: "failed"}})
	var activity []any
	for _, report := range reports.shapes("subagent.activity") {
		activity = append(activity, []any{report["kind"], report["title"], report["text"]})
	}
	assertJSON(t, activity, []any{[]any{"tool", "command_execution", "{\n  \"command\": \"rg TODO src\",\n  \"cwd\": \"/tmp/project\"\n}"}, []any{"text", nil, "Found the issue."}})
	progress := reports.shapes("subagent.progress")
	last := progress[len(progress)-1]
	if last["totalTokens"] != 120.0 {
		t.Fatal("the cumulative total is used instead of the last-turn count")
	}
	if _, ok := last["lastToolName"]; ok {
		t.Fatal("a resumed turn does not revive the previous turn's tool")
	}
	var statuses []any
	for _, report := range reports.shapes("subagent.status") {
		statuses = append(statuses, report["status"])
	}
	assertJSON(t, statuses, `["idle","working"]`)
	all := reports.shapes("")
	assertJSON(t, all[len(all)-1], `{"type":"subagent.finished","id":"child-a","status":"failed","summary":"failed"}`)
	assertJSON(t, tracker.LiveTurns(), `[]`)
	if turn, held := tracker.Stop("child-a"); turn != nil || !held {
		t.Fatal("a child with no turn holds its stop")
	}
	tracker.TurnStarted(turnOf("child-a", "child-turn-3", protocol.TurnInProgress))
	if tracker.TakeHeldStop("child-a") != nil {
		t.Fatal("a child that was not working holds no stop for its next turn")
	}
}

func TestMetadataReadsRetryAfterEarlyPersistenceFailuresAndNeverChangeLifecycle(t *testing.T) {
	reports := &reportRecorder{}
	var reads atomic.Int32
	tracker := NewCodexSubagents(reports.report, nil, func(_ context.Context, id string) (protocol.Thread, error) {
		if reads.Add(1) == 1 {
			return protocol.Thread{}, errors.New("thread not persisted yet")
		}
		return protocol.Thread{ID: id, Model: ptr("gpt-6.2-sol"), ReasoningEffort: ptr("medium")}, nil
	}, nil)
	tracker.SetRootThreadID(rootID)
	tracker.ItemStarted(started(t, rootID, "turn", activityItem("discover", "child", "/root/child", "started")))
	eventually(t, "the first read never failed", func() bool { return reads.Load() == 1 })
	tick()
	if len(reports.shapes("subagent.finished")) != 0 {
		t.Fatal("a failed read changes no lifecycle")
	}
	tracker.ItemCompleted(completed(t, rootID, "turn", activityItem("completed", "child", "/root/child", "completed")))
	eventually(t, "the read was never retried", func() bool {
		all := reports.shapes("")
		return len(all) > 0 && all[len(all)-1]["type"] == "subagent.metadata"
	})
	if reads.Load() != 2 || tracker.Busy() {
		t.Fatalf("reads = %d, busy = %v", reads.Load(), tracker.Busy())
	}
}

type pendingRead struct {
	id      string
	resolve chan protocol.Thread
}

func TestMetadataReadsAreBoundedDoNotOverwriteNewerNotificationsAndStopOnClose(t *testing.T) {
	reports := &reportRecorder{}
	var mu sync.Mutex
	var pending []pendingRead
	tracker := NewCodexSubagents(reports.report, nil, func(_ context.Context, id string) (protocol.Thread, error) {
		read := pendingRead{id, make(chan protocol.Thread)}
		mu.Lock()
		pending = append(pending, read)
		mu.Unlock()
		return <-read.resolve, nil
	}, nil)
	count := func() int { mu.Lock(); defer mu.Unlock(); return len(pending) }
	tracker.SetRootThreadID(rootID)
	for index := range 12 {
		id := string(rune('a' + index))
		tracker.ItemStarted(started(t, rootID, "turn", activityItem("discover-"+id, "child-"+id, "/root/child-"+id, "started")))
	}
	eventually(t, "four reads never started", func() bool { return count() == 4 })
	tick()
	if count() != 4 {
		t.Fatalf("pending = %d", count())
	}
	tracker.ThreadStarted(spawnedThread(t, "child-a", "", "", "", "/root/child-a", `,"model":"gpt-6.2-astra","reasoningEffort":"high"`))
	mu.Lock()
	var first pendingRead
	var others []pendingRead
	for _, read := range pending {
		if read.id == "child-a" {
			first = read
		} else {
			others = append(others, read)
		}
	}
	mu.Unlock()
	if first.id != "child-a" {
		t.Fatal("the first child discovered is among the first reads")
	}
	first.resolve <- protocol.Thread{ID: first.id, Model: ptr("gpt-6.2-sol"), ReasoningEffort: ptr("medium")}
	eventually(t, "the next read never started", func() bool { return count() == 5 })
	var latest map[string]any
	for _, report := range reports.shapes("subagent.metadata") {
		if report["id"] == "child-a" {
			latest = report
		}
	}
	assertJSON(t, latest, `{"type":"subagent.metadata","id":"child-a","model":"gpt-6.2-astra","effort":"high"}`)
	tracker.Close()
	held := len(reports.all())
	mu.Lock()
	rest := append(others, pending[4:]...)
	mu.Unlock()
	for _, read := range rest {
		read.resolve <- protocol.Thread{ID: read.id, Model: ptr("gpt-6.2-sol"), ReasoningEffort: ptr("medium")}
	}
	tick()
	if len(reports.all()) != held || count() != 5 {
		t.Fatal("closing drops queued reads and reports nothing more")
	}
}

func TestMetadataReadsStopRetryingUnsupportedSettingsAfterThreeAttempts(t *testing.T) {
	var reads atomic.Int32
	tracker := NewCodexSubagents(func(domain.SubagentReport) {}, nil, func(_ context.Context, id string) (protocol.Thread, error) {
		reads.Add(1)
		return protocol.Thread{ID: id}, nil
	}, nil)
	tracker.SetRootThreadID(rootID)
	for index := range 8 {
		tracker.ItemStarted(started(t, rootID, "turn", activityItem("discover-"+string(rune('a'+index)), "child", "/root/child", "started")))
		tick()
	}
	if reads.Load() != 3 {
		t.Fatalf("reads = %d", reads.Load())
	}
}

func TestV2DiscoveryFetchesChildSettingsThroughTheSession(t *testing.T) {
	reports := &reportRecorder{}
	h := newHarness(t, script{"thread/read": func(_ context.Context, params jsontext.Value) (any, error) {
		id := decodeAny(params).(map[string]any)["threadId"].(string)
		return jsontext.Value(`{"thread":{"id":"` + id + `","model":"gpt-6.2-sol","reasoningEffort":"medium"}}`), nil
	}}, harnessOptions{})
	run := h.execute(with(func(in *agentInput) { in.Model = "gpt-6.2-astra"; in.ReportSubagent = reports.report }))
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.notify("item/started", startedItem(activityItem("discover", "ui-smoke", "/root/ui_smoke", "started")))
	eventually(t, "the settings were never reported", func() bool { return len(reports.shapes("subagent.metadata")) > 0 })
	assertJSON(t, client.calls("thread/read"), `[{"threadId":"ui-smoke","includeTurns":false}]`)
	assertJSON(t, reports.shapes("subagent.metadata")[0], `{"type":"subagent.metadata","id":"ui-smoke","model":"gpt-6.2-sol","effort":"medium"}`)
	client.notify("item/completed", completedItem(activityItem("discover", "ui-smoke", "/root/ui_smoke", "completed")))
	client.notify("item/started", map[string]any{"threadId": "ui-smoke", "turnId": "child-turn", "startedAtMs": 1, "item": jsontext.Value(childCommandItem("command"))})
	tick()
	if len(client.calls("thread/read")) != 1 {
		t.Fatal("known settings are not fetched for each activity item")
	}
	completeTurn(client, "completed", nil)
	run.wait(t)
}

func TestTheSessionIsolatesChildTrafficAndCancellationInterruptsChildAndRootTurns(t *testing.T) {
	events := &recorder{}
	reports := &reportRecorder{}
	h := newHarness(t, script{"turn/interrupt": func(_ context.Context, params jsontext.Value) (any, error) {
		if decodeAny(params).(map[string]any)["threadId"] == "child-a" {
			return nil, errors.New("child already moved")
		}
		return jsontext.Value(`{}`), nil
	}}, harnessOptions{})
	in := with(emitting(events), func(in *agentInput) { in.ReportSubagent = reports.report })
	run := h.execute(in)
	client := h.opened()
	sentBy(t, client, "turn/start", 1)
	client.notify("turn/started", turnNotificationFor("child-a", "child-turn"))
	client.notify("item/started", startedItem(activityItem("discover", "child-a", "/root/reviewer", "started")))
	client.notify("item/agentMessage/delta", map[string]any{"threadId": "child-a", "turnId": "child-turn", "itemId": "message-child", "delta": "hidden child text"})
	client.notify("item/started", map[string]any{"threadId": "child-a", "turnId": "child-turn", "startedAtMs": 1, "item": jsontext.Value(childCommandItem("command-child"))})
	client.notify("thread/tokenUsage/updated", usageOf("child-a", "child-turn", 80, 5))
	client.notify("thread/tokenUsage/updated", usageOf(rootID, "turn-1", 900, 9))
	in.Abort()
	sentBy(t, client, "turn/interrupt", 2)
	calls := client.calls("turn/interrupt")
	if string(calls[0]) > string(calls[1]) {
		calls[0], calls[1] = calls[1], calls[0]
	}
	assertJSON(t, []any{decodeAny(calls[0]), decodeAny(calls[1])}, `[{"threadId":"child-a","turnId":"child-turn"},{"threadId":"thread-1","turnId":"turn-1"}]`, "both turns are interrupted")
	for _, event := range events.shapes() {
		if event["type"] == "assistant" || event["type"] == "assistant-tail" || event["type"] == "tool" {
			t.Fatalf("child traffic reached the parent: %v", event)
		}
	}
	assertJSON(t, events.ofType("usage"), `[{"type":"usage","tokens":9,"limit":272000,"model":"gpt-6.2-sol"}]`)
	tool := false
	for _, report := range reports.shapes("subagent.activity") {
		tool = tool || report["kind"] == "tool"
	}
	if !tool {
		t.Fatal("the child's tool is its own activity")
	}
	completeTurn(client, "interrupted", nil)
	assertJSON(t, run.wait(t), agent.ProviderResult{Status: domain.RunStatusCancelled})
}

func turnNotificationFor(thread, id string) map[string]any {
	return map[string]any{"threadId": thread, "turn": map[string]any{"id": id, "rootTurnId": nil, "status": "inProgress", "items": []any{}, "error": nil, "startedAt": 1, "completedAt": nil, "durationMs": nil}}
}

func TestStoppingASubagentInterruptsItsLiveTurnAfterTheParentTurnEndedAndEndsItsCommands(t *testing.T) {
	h := newHarness(t, script{"thread/backgroundTerminals/list": func(_ context.Context, params jsontext.Value) (any, error) {
		if decodeAny(params).(map[string]any)["threadId"] == "child-a" {
			return jsontext.Value(`{"data":[{"itemId":"item-1","processId":"child-process","command":"sleep 120","cwd":"/tmp/project","osPid":101,"cpuPercent":0,"rssKb":1024}],"nextCursor":null}`), nil
		}
		return jsontext.Value(`{"data":[],"nextCursor":null}`), nil
	}}, harnessOptions{})
	client, _ := h.turn(with(), func(client *fakeClient) {
		client.notify("item/started", startedItem(activityItem("discover", "child-a", "/root/reviewer", "started")))
		client.notify("turn/started", turnNotificationFor("child-a", "child-turn"))
	})
	if !h.provider.StopProcess("task-1", "child-a") {
		t.Fatal("the session takes the stop")
	}
	sentBy(t, client, "turn/interrupt", 1)
	assertJSON(t, client.calls("turn/interrupt"), `[{"threadId":"child-a","turnId":"child-turn"}]`)
	sentBy(t, client, "thread/backgroundTerminals/terminate", 1)
	assertJSON(t, client.calls("thread/backgroundTerminals/terminate"), `[{"threadId":"child-a","processId":"child-process"}]`)
}

func TestASubagentStoppedBeforeItsFirstTurnIsInterruptedAsSoonAsThatTurnStarts(t *testing.T) {
	h := newHarness(t, nil, harnessOptions{})
	client, _ := h.turn(with(), func(client *fakeClient) {
		client.notify("item/started", startedItem(activityItem("discover", "child-a", "/root/reviewer", "started")))
	})
	if !h.provider.StopProcess("task-1", "child-a") {
		t.Fatal("the session takes the stop")
	}
	tick()
	if len(client.calls("turn/interrupt")) != 0 || len(client.calls("thread/backgroundTerminals/terminate")) != 0 {
		t.Fatal("nothing is interrupted yet, and a child's stop never reaches the terminals")
	}
	client.notify("turn/started", turnNotificationFor("child-a", "child-turn"))
	sentBy(t, client, "turn/interrupt", 1)
	assertJSON(t, client.calls("turn/interrupt"), `[{"threadId":"child-a","turnId":"child-turn"}]`)
	client.notify("turn/started", turnNotificationFor("child-a", "child-turn-2"))
	tick()
	if len(client.calls("turn/interrupt")) != 1 {
		t.Fatal("a held stop is spent once")
	}
}
