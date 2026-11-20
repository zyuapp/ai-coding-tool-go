package application

import (
	"encoding/json/jsontext"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func trsState() RunTransitionState {
	return RunTransitionState{
		ThreadsState: ThreadsState{Threads: []*domain.Thread{runsTask("task-a"), runsTask("task-b")}},
		RunFeedsState: RunFeedsState{
			Goals:               map[string]*domain.ActiveGoal{},
			ActiveRuns:          map[string]ActiveRun{"task-a": runsActive("task-a", "run-a")},
			RunStatuses:         map[string]ThreadRunStatus{"task-a": ThreadRunStatusRunning},
			Approvals:           map[string]*ApprovalView{},
			StreamingTails:      map[string]*StreamingTail{},
			BackgroundProcesses: map[string][]*domain.BackgroundProcess{},
			Workflows:           map[string][]*domain.Workflow{},
			Subagents:           map[string][]*domain.Subagent{},
		},
	}
}

func trsAt(sequence float64) contracts.RunEventBase {
	return contracts.RunEventBase{TaskID: "task-a", RunID: "run-a", Sequence: sequence}
}

func trsThreadEvent(state RunTransitionState, event contracts.ThreadEvent) RunTransitionState {
	state.RunFeedsState = feedsWithThreadEvent(state.RunFeedsState, event)
	return state
}

func trsRecord(t *testing.T, state RunTransitionState, taskID string) *ActiveRunRunning {
	t.Helper()
	record := RunRecord(state.ActiveRuns[taskID])
	if record == nil {
		t.Fatalf("no run for %s", taskID)
	}
	return record
}

func trsSubagent(t *testing.T, state RunTransitionState, taskID string, index int) *domain.Subagent {
	t.Helper()
	if len(state.Subagents[taskID]) <= index {
		t.Fatalf("no subagent %d", index)
	}
	return state.Subagents[taskID][index]
}

func trsTexts(thread *domain.Thread) []string {
	texts := make([]string, len(thread.Messages))
	for index, message := range thread.Messages {
		texts[index] = message.Text
	}
	return texts
}

func trsIDs(thread *domain.Thread) []string {
	ids := make([]string, len(thread.Messages))
	for index, message := range thread.Messages {
		ids[index] = message.ID
	}
	return ids
}

func TestThreadRunStateGoalsReplaceAndClear(t *testing.T) {
	goal := &domain.ActiveGoal{Objective: "Ship it", Status: "active", Iterations: new(1.0)}
	active := trsThreadEvent(trsState(), contracts.GoalEvent{TaskID: "task-a", GoalReport: contracts.GoalReport{Goal: goal}})
	if active.Goals["task-a"] != goal {
		t.Fatal("goal not kept")
	}
	cleared := trsThreadEvent(active, contracts.GoalEvent{TaskID: "task-a"})
	if _, ok := cleared.Goals["task-a"]; ok {
		t.Fatal("goal not cleared")
	}
}

func TestThreadRunStateArtifactKeepsAnswerIntact(t *testing.T) {
	runsPorted(t, func() {
		next := applyRunEvent(trsState(), contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "answer", Text: "A cat.\n\n"})
		next = applyRunEvent(next, contracts.RunEventAssistantTail{RunEventBase: trsAt(2), MessageID: "answer", Text: "Pixel art"})
		next = applyRunEvent(next, contracts.RunEventAssistantDelta{RunEventBase: trsAt(3), MessageID: "image", Text: "[Generated image](/tmp/cat.png)", Artifact: new(true)})
		if tail := next.StreamingTails["task-a"]; tail == nil || *tail != (StreamingTail{MessageID: "answer", Text: "Pixel art"}) {
			t.Fatalf("tail = %+v", tail)
		}
		next = applyRunEvent(next, contracts.RunEventAssistantDelta{RunEventBase: trsAt(4), MessageID: "answer", Text: "Pixel art.", Append: new(true)})
		messages := next.Threads[0].Messages
		if !slices.Equal(trsIDs(next.Threads[0]), []string{"image", "answer"}) || !runsIsTrue(messages[0].Artifact) || messages[1].Artifact != nil || messages[1].Text != "A cat.\n\nPixel art." {
			t.Fatalf("messages = %v %v", trsIDs(next.Threads[0]), trsTexts(next.Threads[0]))
		}
	})
}

func TestThreadRunStateInterruptedAnswerContinuesAsNewPart(t *testing.T) {
	runsPorted(t, func() {
		next := applyRunEvent(trsState(), contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "answer", Text: "First.\n\n"})
		steered := *next.Threads[0]
		steered.Messages = append(slices.Clone(steered.Messages), &domain.ConversationMessage{ID: "steer", Kind: domain.ConversationMessageKindUser, Text: "ELI5", At: 1})
		next.Threads = []*domain.Thread{&steered, next.Threads[1]}
		next = applyRunEvent(next, contracts.RunEventAssistantTail{RunEventBase: trsAt(2), MessageID: "answer", Text: "Sec"})
		if next.StreamingTails["task-a"].MessageID != "answer:1" {
			t.Fatalf("tail id = %s", next.StreamingTails["task-a"].MessageID)
		}
		next = applyRunEvent(next, contracts.RunEventAssistantDelta{RunEventBase: trsAt(3), MessageID: "answer", Text: "Second.", Append: new(true)})
		next = applyRunEvent(next, contracts.RunEventAssistantDelta{RunEventBase: trsAt(4), MessageID: "answer", Text: " Third.", Append: new(true)})
		if !slices.Equal(trsIDs(next.Threads[0]), []string{"answer", "steer", "answer:1"}) || !slices.Equal(trsTexts(next.Threads[0]), []string{"First.\n\n", "ELI5", "Second. Third."}) {
			t.Fatalf("messages = %v %v", trsIDs(next.Threads[0]), trsTexts(next.Threads[0]))
		}
	})
}

func TestThreadRunStateIgnoresOtherTasksAndStaleSequences(t *testing.T) {
	runsPorted(t, func() {
		initial := trsState()
		other := applyRunEvent(initial, contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "task-b", RunID: "run-b", Sequence: 1}, MessageID: "message-b", Text: "wrong"})
		if !jsonx.SameSlice(other.Threads, initial.Threads) || !sameFeeds(other.RunFeedsState, initial.RunFeedsState) {
			t.Fatal("another task's event changed state")
		}
		updated := applyRunEvent(initial, contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-a", Text: "hello"})
		if updated.Threads[0].Messages[0].Text != "hello" || trsRecord(t, updated, "task-a").Sequence != 1 {
			t.Fatal("event not applied")
		}
		late := applyRunEvent(updated, contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-a", Text: "late"})
		if !jsonx.SameSlice(late.Threads, updated.Threads) || !sameFeeds(late.RunFeedsState, updated.RunFeedsState) {
			t.Fatal("stale event changed state")
		}
	})
}

func TestThreadRunStateSubagentMetadataSurvives(t *testing.T) {
	model, effort, high, prompt := "gpt-6.2-astra", "xhigh", "high", "  Exact\nprompt  "
	next := trsThreadEvent(trsState(), contracts.SubagentEventSubagentMetadata{TaskID: "task-a", ID: "child", Prompt: &prompt, Model: &model})
	next = trsThreadEvent(next, contracts.SubagentEventSubagentStarted{TaskID: "task-a", ID: "child", Description: "Inspect", Effort: &high})
	next = trsThreadEvent(next, contracts.SubagentEventSubagentProgress{TaskID: "task-a", ID: "child", Description: "Inspect", TotalTokens: 123})
	next = trsThreadEvent(next, contracts.SubagentEventSubagentFinished{TaskID: "task-a", ID: "child", Status: "completed", Summary: "Done"})
	next = trsThreadEvent(next, contracts.SubagentEventSubagentMetadata{TaskID: "task-a", ID: "child", Effort: &effort})
	child := trsSubagent(t, next, "task-a", 0)
	if *child.Model != model || *child.Effort != effort || *child.Prompt != prompt || child.Status != "completed" || *child.TotalTokens != 123 {
		t.Fatalf("child = %+v", child)
	}
	next = trsThreadEvent(next, contracts.SubagentEventSubagentStarted{TaskID: "task-a", ID: "child", Description: "Inspect again", Prompt: new("Follow-up message")})
	if *trsSubagent(t, next, "task-a", 0).Prompt != prompt {
		t.Fatal("resuming replaced the original delegated prompt")
	}
	next = trsThreadEvent(next, contracts.SubagentEventSubagentMetadata{TaskID: "task-a", ID: "child", Prompt: new("Another follow-up")})
	if *trsSubagent(t, next, "task-a", 0).Prompt != prompt {
		t.Fatal("metadata replaced the original delegated prompt")
	}
}

func TestThreadRunStateCollectsSubagentActivity(t *testing.T) {
	runsPorted(t, func() {
		started := trsThreadEvent(trsState(), contracts.SubagentEventSubagentStarted{TaskID: "task-a", ID: "agent-1", Description: "Inspect the renderer", AgentType: new("Explore")})
		active := trsThreadEvent(started, contracts.SubagentEventSubagentActivity{TaskID: "task-a", ID: "agent-1", ActivityID: "tool-1", Kind: "tool", Title: new("Read"), Text: "src/renderer/App.tsx"})
		finished := trsThreadEvent(active, contracts.SubagentEventSubagentFinished{TaskID: "task-a", ID: "agent-1", Status: "completed", Summary: "Renderer inspected"})
		if child := trsSubagent(t, finished, "task-a", 0); child.Status != "completed" || *child.Activity[0].Title != "Read" {
			t.Fatalf("child = %+v", child)
		}
		terminal := applyRunEvent(active, contracts.RunEventRunStatus{RunEventBase: trsAt(1), Status: domain.RunStatusSucceeded})
		if trsSubagent(t, terminal, "task-a", 0).Status != "completed" {
			t.Fatal("a working subagent outlived its run")
		}
	})
}

func TestThreadRunStateApprovalsExpireOnTerminalState(t *testing.T) {
	runsPorted(t, func() {
		requested := applyRunEvent(trsState(), contracts.RunEventApprovalRequested{RunEventBase: trsAt(1), ApprovalID: "approval-a", Title: "Write needs approval", Description: "Review the write.", Intent: domain.ToolIntent{ToolID: "tool-a", Name: "Write", Input: jsontext.Value(`{"file_path":"src/App.tsx"}`)}})
		if requested.Approvals["run-a"].ApprovalID != "approval-a" || string(requested.Approvals["run-a"].Input["file_path"]) != `"src/App.tsx"` {
			t.Fatalf("approval = %+v", requested.Approvals["run-a"])
		}
		awaiting := applyRunEvent(requested, contracts.RunEventRunStatus{RunEventBase: trsAt(2), Status: domain.RunStatusAwaitingApproval})
		if trsRecord(t, awaiting, "task-a").Status != "awaiting-approval" {
			t.Fatal("not awaiting approval")
		}
		finished := applyRunEvent(awaiting, contracts.RunEventRunStatus{RunEventBase: trsAt(3), Status: domain.RunStatusCancelled})
		if finished.ActiveRuns["task-a"] != nil || len(finished.Approvals) != 0 || finished.RunStatuses["task-a"] != ThreadRunStatusStopped {
			t.Fatal("terminal state kept the run or its approval")
		}
	})
}

func TestThreadRunStateApprovalInputThatIsNoObject(t *testing.T) {
	if got := approvalInput(jsontext.Value(`["a"]`)); string(got["value"]) != `["a"]` {
		t.Fatalf("array input = %v", got)
	}
	if got := approvalInput(jsontext.Value(`null`)); string(got["value"]) != `null` {
		t.Fatalf("null input = %v", got)
	}
}

func TestThreadRunStateContextUsage(t *testing.T) {
	runsPorted(t, func() {
		updated := applyRunEvent(trsState(), contracts.RunEventContextUsage{RunEventBase: trsAt(1), Tokens: 42_000, Limit: 200_000, Model: "claude-sonnet"})
		if usage := updated.Threads[0].ContextUsage; usage == nil || *usage != (domain.ContextUsage{Tokens: 42_000, Limit: 200_000, Model: "claude-sonnet"}) {
			t.Fatalf("usage = %+v", usage)
		}

		initial := trsState()
		with := *initial.Threads[0]
		with.ContextUsage = &domain.ContextUsage{Tokens: 42_000, Limit: 200_000, Model: "claude-sonnet"}
		initial.Threads = []*domain.Thread{&with, initial.Threads[1]}
		initial.ActiveRuns = map[string]ActiveRun{"task-a": runsActive("task-a", "run-a", func(run *ActiveRunRunning) { run.Warming = new(true) })}
		same := applyRunEvent(initial, contracts.RunEventContextUsage{RunEventBase: trsAt(1), Tokens: 42_000, Limit: 200_000, Model: "claude-sonnet"})
		if !jsonx.SameSlice(same.Threads, initial.Threads) {
			t.Fatal("an unchanged report rewrote the thread list")
		}
		if record := trsRecord(t, same, "task-a"); record.Warming != nil || record.Sequence != 1 {
			t.Fatalf("run = %+v", record)
		}
	})
}

func TestThreadRunStateSpendAddsUpByModel(t *testing.T) {
	runsPorted(t, func() {
		initial := trsState()
		spending := *initial.Threads[0]
		spending.Model = new(domain.AgentModel("opus"))
		spending.Usage = map[string]float64{"sonnet": 900}
		initial.Threads = []*domain.Thread{&spending, initial.Threads[1]}
		next := applyRunEvent(initial, contracts.RunEventContextSpent{RunEventBase: trsAt(1), Tokens: 1_500})
		next = applyRunEvent(next, contracts.RunEventContextSpent{RunEventBase: trsAt(2), Tokens: 250})
		if usage := next.Threads[0].Usage; len(usage) != 2 || usage["sonnet"] != 900 || usage["opus"] != 1_750 {
			t.Fatalf("usage = %v", usage)
		}
		if next.Threads[0].UpdatedAt != spending.UpdatedAt || !jsonx.SameSlice(next.Threads[0].Messages, spending.Messages) || next.Threads[1] != initial.Threads[1] {
			t.Fatal("spend moved the thread")
		}
		unchosen := applyRunEvent(trsState(), contracts.RunEventContextSpent{RunEventBase: trsAt(1), Tokens: 40})
		fallback, _ := domain.DefaultModelFor("claude")
		if unchosen.Threads[0].Usage[string(fallback)] != 40 {
			t.Fatalf("usage = %v", unchosen.Threads[0].Usage)
		}
		stale := applyRunEvent(next, contracts.RunEventContextSpent{RunEventBase: contracts.RunEventBase{TaskID: "task-a", RunID: "run-old", Sequence: 3}, Tokens: 99})
		if !jsonx.SameSlice(stale.Threads, next.Threads) {
			t.Fatal("a superseded run counted")
		}
	})
}

func TestThreadRunStateCompaction(t *testing.T) {
	runsPorted(t, func() {
		initial := trsState()
		full := *initial.Threads[0]
		full.ContextUsage = &domain.ContextUsage{Tokens: 182_000, Limit: 200_000, Model: "claude-sonnet"}
		initial.Threads = []*domain.Thread{&full, initial.Threads[1]}
		compacting := applyRunEvent(initial, contracts.RunEventContextCompactionStatus{RunEventBase: trsAt(1), Compacting: true})
		updated := applyRunEvent(compacting, contracts.RunEventContextCompacted{RunEventBase: trsAt(2), Trigger: "auto", PreTokens: 182_000, PostTokens: new(41_000.0)})
		if trsRecord(t, compacting, "task-a").Status != "compacting" || trsRecord(t, updated, "task-a").Status != "running" {
			t.Fatal("compaction phase not tracked")
		}
		if text := updated.Threads[0].Messages[0].Text; text != "Context auto-compacted: 182,000 → 41,000 tokens." {
			t.Fatalf("text = %q", text)
		}
		if usage := *updated.Threads[0].ContextUsage; usage != (domain.ContextUsage{Tokens: 41_000, Limit: 200_000, Model: "claude-sonnet"}) {
			t.Fatalf("usage = %+v", usage)
		}
		if updated.Threads[0].Messages[0].Tone != nil {
			t.Fatal("a compaction notice is toned as a failure")
		}

		failed := applyRunEvent(trsState(), contracts.RunEventContextCompactionStatus{RunEventBase: trsAt(1), Error: new("Could not compact")})
		manual := applyRunEvent(failed, contracts.RunEventContextCompacted{RunEventBase: trsAt(2), Trigger: "manual", PreTokens: 100_000})
		if trsRecord(t, failed, "task-a").Status != "running" || !slices.Equal(trsTexts(manual.Threads[0]), []string{"Could not compact", "Context manual-compacted at 100,000 tokens."}) {
			t.Fatalf("texts = %v", trsTexts(manual.Threads[0]))
		}
	})
}

func TestThreadRunStateCompactedCoordinatorRereadsMemory(t *testing.T) {
	runsPorted(t, func() {
		initial := trsState()
		lead := *initial.Threads[0]
		lead.Role = new(domain.ThreadRoleCoordinator)
		lead.Memory = &domain.CoordinatorMemory{Purpose: new("Ship the v2 reader"), Notes: []*domain.MemoryNote{}, Revision: 2, Heard: new(2.0)}
		initial.Threads = []*domain.Thread{&lead, initial.Threads[1]}
		compacted := applyRunEvent(initial, contracts.RunEventContextCompacted{RunEventBase: trsAt(1), Trigger: "auto", PreTokens: 182_000, PostTokens: new(41_000.0)})
		if memory := compacted.Threads[0].Memory; memory.Heard != nil || memory.Revision != 2 {
			t.Fatalf("memory = %+v", memory)
		}
	})
}

func TestThreadRunStateTerminalRunsFinalizeOnlyWorkingSubagents(t *testing.T) {
	runsPorted(t, func() {
		for status, want := range map[domain.RunStatus]domain.SubagentStatus{"succeeded": "completed", "failed": "failed", "cancelled": "stopped"} {
			initial := trsState()
			initial.Subagents = map[string][]*domain.Subagent{"task-a": {
				{ID: "working", Description: "Working", Status: "working", StartedAt: 1, Activity: []*domain.SubagentActivity{}},
				{ID: "done", Description: "Done", Status: "completed", StartedAt: 1, FinishedAt: new(2.0), Activity: []*domain.SubagentActivity{}},
				{ID: "session", Description: "Session", SessionScoped: new(true), Status: "working", StartedAt: 1, Activity: []*domain.SubagentActivity{}},
			}}
			terminal := applyRunEvent(initial, contracts.RunEventRunStatus{RunEventBase: trsAt(1), Status: status})
			if child := trsSubagent(t, terminal, "task-a", 0); child.Status != want || child.FinishedAt == nil {
				t.Fatalf("%s: working = %+v", status, child)
			}
			if child := trsSubagent(t, terminal, "task-a", 1); child.Status != "completed" || *child.FinishedAt != 2 {
				t.Fatalf("%s: done = %+v", status, child)
			}
			if child := trsSubagent(t, terminal, "task-a", 2); child.Status != "working" || child.FinishedAt != nil {
				t.Fatalf("%s: a session-scoped subagent ended with the run", status)
			}
		}
	})
}

func TestThreadRunStateFailureIsToned(t *testing.T) {
	runsPorted(t, func() {
		failed := applyRunEvent(trsState(), contracts.RunEventRunStatus{RunEventBase: trsAt(1), Status: domain.RunStatusFailed, Message: new("provider failed")})
		messages := failed.Threads[0].Messages
		if last := messages[len(messages)-1]; last.Tone == nil || *last.Tone != "error" {
			t.Fatalf("last = %+v", last)
		}
	})
}

func TestThreadRunStateSubagentProgressFirstAndDuplicateActivity(t *testing.T) {
	progressed := trsThreadEvent(trsState(), contracts.SubagentEventSubagentProgress{TaskID: "task-a", ID: "agent-1", Description: "Inspect", LastToolName: new("Read"), Summary: new("Working"), TotalTokens: 10})
	activity := contracts.SubagentEventSubagentActivity{TaskID: "task-a", ID: "agent-1", ActivityID: "same", Kind: "text", Text: "Only once"}
	once := trsThreadEvent(progressed, activity)
	twice := trsThreadEvent(once, activity)
	child := trsSubagent(t, twice, "task-a", 0)
	if child.Status != "working" || child.StartedAt <= 0 || len(child.Activity) != 1 {
		t.Fatalf("child = %+v", child)
	}
	if !jsonx.SameMap(twice.Subagents, once.Subagents) {
		t.Fatal("a duplicate activity rewrote the subagents")
	}
}

func TestThreadRunStateIdleSubagentResumes(t *testing.T) {
	started := trsThreadEvent(trsState(), contracts.SubagentEventSubagentStarted{TaskID: "task-a", ID: "agent-1", Description: "Inspect", AgentType: new("Explore"), SessionScoped: new(true)})
	progressed := trsThreadEvent(started, contracts.SubagentEventSubagentProgress{TaskID: "task-a", ID: "agent-1", Description: "Inspect", LastToolName: new("Read"), TotalTokens: 50})
	active := trsThreadEvent(progressed, contracts.SubagentEventSubagentActivity{TaskID: "task-a", ID: "agent-1", ActivityID: "note", Kind: "text", Text: "First result"})
	idle := trsThreadEvent(active, contracts.SubagentEventSubagentStatus{TaskID: "task-a", ID: "agent-1", Status: "idle", Summary: new("Ready for more work")})
	resumed := trsThreadEvent(idle, contracts.SubagentEventSubagentStatus{TaskID: "task-a", ID: "agent-1", Status: "working"})
	stale := trsThreadEvent(resumed, contracts.SubagentEventSubagentProgress{TaskID: "task-a", ID: "agent-1", Description: "Inspect again", TotalTokens: 20})
	child := trsSubagent(t, stale, "task-a", 0)
	if child.Status != "working" || child.FinishedAt != nil || child.LastToolName != nil || *child.Summary != "Ready for more work" || *child.TotalTokens != 50 || !runsIsTrue(child.SessionScoped) {
		t.Fatalf("child = %+v", child)
	}
	if len(child.Activity) != 1 || child.Activity[0].Text != "First result" {
		t.Fatalf("activity = %+v", child.Activity)
	}
}

func TestThreadRunStateOrderAndContinuation(t *testing.T) {
	runsPorted(t, func() {
		first := applyRunEvent(trsState(), contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-1", Text: "one"})
		second := applyRunEvent(first, contracts.RunEventAssistantDelta{RunEventBase: trsAt(2), MessageID: "message-1", Text: "two"})
		tool := applyRunEvent(second, contracts.RunEventToolIntent{RunEventBase: trsAt(3), Intent: domain.ToolIntent{ToolID: "tool-1", Name: "Read", Input: jsontext.Value(`{"file_path":"src/App.tsx"}`)}})
		continued := applyRunEvent(tool, contracts.RunEventContinuationUpdated{RunEventBase: trsAt(4), Continuation: domain.Continuation{Provider: "claude", Value: "session-1"}})
		thread := continued.Threads[0]
		if thread.Messages[0].Text != "one\ntwo" || thread.Messages[1].Kind != domain.ConversationMessageKindTool || *thread.Messages[1].Detail != "{\n  \"file_path\": \"src/App.tsx\"\n}" {
			t.Fatalf("messages = %v", trsTexts(thread))
		}
		if *thread.Continuation != (domain.Continuation{Provider: "claude", Value: "session-1"}) || thread.ContinuationStatus != domain.ContinuationStatusAvailable {
			t.Fatal("continuation not kept")
		}
		lost := applyRunEvent(continued, contracts.RunEventContinuationLost{RunEventBase: trsAt(5)})
		if lost.Threads[0].Continuation != nil || lost.Threads[0].ContinuationStatus != domain.ContinuationStatusInvalid {
			t.Fatal("a lost continuation was kept")
		}
	})
}

func TestThreadRunStateTextARunEndsOnIsItsAnswer(t *testing.T) {
	runsPorted(t, func() {
		note := applyRunEvent(trsState(), contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-1", Text: "Checking."})
		tool := applyRunEvent(note, contracts.RunEventToolIntent{RunEventBase: trsAt(2), Intent: domain.ToolIntent{ToolID: "tool-1", Name: "Bash", Input: jsontext.Value(`{"command":"ls"}`)}})
		answer := applyRunEvent(tool, contracts.RunEventAssistantDelta{RunEventBase: trsAt(3), MessageID: "message-2", Text: "Done."})
		ended := applyRunEvent(answer, contracts.RunEventRunStatus{RunEventBase: trsAt(4), Status: domain.RunStatusSucceeded})
		var answers []bool
		for _, message := range ended.Threads[0].Messages {
			answers = append(answers, runsIsTrue(message.Answer))
		}
		if !slices.Equal(answers, []bool{false, false, true}) {
			t.Fatalf("answers = %v", answers)
		}
		toolLast := applyRunEvent(tool, contracts.RunEventRunStatus{RunEventBase: trsAt(3), Status: domain.RunStatusCancelled})
		if slices.ContainsFunc(toolLast.Threads[0].Messages, func(message *domain.ConversationMessage) bool { return runsIsTrue(message.Answer) }) {
			t.Fatal("text a tool call followed became the answer")
		}
	})
}

func TestThreadRunStateRetryingUntilAnythingElse(t *testing.T) {
	runsPorted(t, func() {
		retrying := applyRunEvent(trsState(), contracts.RunEventRunRetrying{RunEventBase: trsAt(1), RetryNotice: domain.RetryNotice{Message: "Claude is overloaded.", Attempt: new(2.0), MaxRetries: new(10.0)}})
		if retry := RunRetry(retrying.ActiveRuns["task-a"]); retry == nil || *retry.Attempt != 2 || *retry.MaxRetries != 10 || retry.Message != "Claude is overloaded." {
			t.Fatalf("retry = %+v", retry)
		}
		again := applyRunEvent(retrying, contracts.RunEventRunRetrying{RunEventBase: trsAt(2), RetryNotice: domain.RetryNotice{Message: "Claude is overloaded.", Attempt: new(3.0), MaxRetries: new(10.0)}})
		delivered := applyRunEvent(again, contracts.RunEventQueuedDelivered{RunEventBase: trsAt(3), MessageID: "queued-1"})
		spent := applyRunEvent(delivered, contracts.RunEventContextSpent{RunEventBase: trsAt(4), Tokens: 1_200})
		if retry := RunRetry(spent.ActiveRuns["task-a"]); retry == nil || *retry.Attempt != 3 {
			t.Fatal("a delivery or a subagent's spend ended the retry")
		}
		through := applyRunEvent(spent, contracts.RunEventAssistantDelta{RunEventBase: trsAt(5), MessageID: "message-1", Text: "hello"})
		if _, ok := through.ActiveRuns["task-a"].(ActiveRunRunning); !ok || trsRecord(t, through, "task-a").Status != "running" {
			t.Fatal("still retrying after hearing an answer")
		}
	})
}

func TestThreadRunStateMarkdownBlocksAppend(t *testing.T) {
	runsPorted(t, func() {
		first := applyRunEvent(trsState(), contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-1", Text: "## Title\n\n", Append: new(true)})
		second := applyRunEvent(first, contracts.RunEventAssistantDelta{RunEventBase: trsAt(2), MessageID: "message-1", Text: "Paragraph.", Append: new(true)})
		if text := second.Threads[0].Messages[0].Text; text != "## Title\n\nParagraph." {
			t.Fatalf("text = %q", text)
		}
	})
}

func TestThreadRunStateConcurrentRuns(t *testing.T) {
	runsPorted(t, func() {
		both := trsState()
		both.ActiveRuns = map[string]ActiveRun{"task-a": runsActive("task-a", "run-a"), "task-b": runsActive("task-b", "run-b")}
		both.RunStatuses = map[string]ThreadRunStatus{"task-a": "running", "task-b": "running"}
		atB := func(sequence float64) contracts.RunEventBase {
			return contracts.RunEventBase{TaskID: "task-b", RunID: "run-b", Sequence: sequence}
		}
		first := applyRunEvent(both, contracts.RunEventAssistantDelta{RunEventBase: trsAt(1), MessageID: "message-a", Text: "from a"})
		second := applyRunEvent(first, contracts.RunEventAssistantDelta{RunEventBase: atB(1), MessageID: "message-b", Text: "from b"})
		stopped := applyRunEvent(second, contracts.RunEventRunStatus{RunEventBase: trsAt(2), Status: domain.RunStatusSucceeded})
		if stopped.ActiveRuns["task-a"] != nil || stopped.RunStatuses["task-a"] != "" || trsRecord(t, stopped, "task-b").RunID != "run-b" || stopped.RunStatuses["task-b"] != "running" {
			t.Fatal("one run's end touched the other")
		}
		still := applyRunEvent(stopped, contracts.RunEventAssistantDelta{RunEventBase: atB(2), MessageID: "message-b", Text: "still going"})
		if still.Threads[1].Messages[0].Text != "from b\nstill going" {
			t.Fatalf("text = %q", still.Threads[1].Messages[0].Text)
		}
	})
}

func TestThreadRunStateBackgroundProcesses(t *testing.T) {
	runsPorted(t, func() {
		started := trsThreadEvent(trsState(), contracts.BackgroundEvent{TaskID: "task-a", BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{
			{ID: "bash-1", Kind: "shell", Description: "npm run dev"},
			{ID: "watch-1", Kind: "monitor", Description: "Deploy events"},
		}}})
		stopping := started
		marked := slices.Clone(started.BackgroundProcesses["task-a"])
		asked := *marked[0]
		asked.Stopping = new(true)
		marked[0] = &asked
		stopping.BackgroundProcesses = map[string][]*domain.BackgroundProcess{"task-a": marked}
		replaced := trsThreadEvent(stopping, contracts.BackgroundEvent{TaskID: "task-a", BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{{ID: "bash-1", Kind: "shell", Description: "npm run dev"}}}})
		if kept := replaced.BackgroundProcesses["task-a"]; len(kept) != 1 || !runsIsTrue(kept[0].Stopping) {
			t.Fatalf("processes = %+v", kept)
		}
		ended := applyRunEvent(replaced, contracts.RunEventRunStatus{RunEventBase: trsAt(1), Status: domain.RunStatusSucceeded})
		if !jsonx.SameSlice(ended.BackgroundProcesses["task-a"], replaced.BackgroundProcesses["task-a"]) {
			t.Fatal("the run's end took the session's processes")
		}
		ended.ActiveRuns = map[string]ActiveRun{"task-a": runsActive("task-a", "run-a", func(run *ActiveRunRunning) { run.Sequence = 1 })}
		again := applyRunEvent(ended, contracts.RunEventRunStarted{RunEventBase: trsAt(2)})
		if !jsonx.SameSlice(again.BackgroundProcesses["task-a"], replaced.BackgroundProcesses["task-a"]) {
			t.Fatal("a later run blanked what is still running")
		}
		gone := trsThreadEvent(again, contracts.BackgroundEvent{TaskID: "task-a", BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{}}})
		if _, ok := gone.BackgroundProcesses["task-a"]; ok {
			t.Fatal("an empty set was kept")
		}
	})
}

func TestThreadRunStateWorkflows(t *testing.T) {
	runsPorted(t, func() {
		started := trsThreadEvent(trsState(), contracts.WorkflowEventWorkflowStarted{TaskID: "task-a", ID: "wf-1", Name: "review-changes", Description: "Review changed files across dimensions"})
		first := trsThreadEvent(started, contracts.WorkflowEventWorkflowProgress{TaskID: "task-a", ID: "wf-1", Phases: []*domain.WorkflowPhase{{Index: 0, Title: "Review"}}, Agents: []*domain.WorkflowAgent{{Index: 0, Label: "review:bugs", State: "running"}, {Index: 1, Label: "review:perf", State: "queued"}}, TotalTokens: 1_200, TotalToolCalls: 4})
		second := trsThreadEvent(first, contracts.WorkflowEventWorkflowProgress{TaskID: "task-a", ID: "wf-1", Phases: []*domain.WorkflowPhase{{Index: 0, Title: "Review"}}, Agents: []*domain.WorkflowAgent{{Index: 0, Label: "review:bugs", State: "done"}}, TotalTokens: 2_400, TotalToolCalls: 9})
		workflows := second.Workflows["task-a"]
		if len(workflows) != 1 || workflows[0].Name != "review-changes" || len(workflows[0].Agents) != 1 || workflows[0].Agents[0].State != "done" || workflows[0].TotalTokens != 2_400 {
			t.Fatalf("workflow = %+v", workflows[0])
		}
		cancelled := applyRunEvent(second, contracts.RunEventRunStatus{RunEventBase: trsAt(4), Status: domain.RunStatusCancelled})
		if workflow := cancelled.Workflows["task-a"][0]; workflow.Status != "stopped" || workflow.FinishedAt == nil {
			t.Fatalf("cancelled = %+v", workflow)
		}
		answered := applyRunEvent(second, contracts.RunEventRunStatus{RunEventBase: trsAt(4), Status: domain.RunStatusSucceeded})
		if workflow := answered.Workflows["task-a"][0]; workflow.Status != "running" || workflow.FinishedAt != nil {
			t.Fatal("a background workflow ended with the turn that started it")
		}

		finished := trsThreadEvent(second, contracts.WorkflowEventWorkflowFinished{TaskID: "task-a", ID: "wf-1", Status: "completed", Summary: `Dynamic workflow "spec" completed`})
		if workflow := finished.Workflows["task-a"][0]; workflow.Status != "completed" || *workflow.Summary != `Dynamic workflow "spec" completed` || len(workflow.Agents) != 1 {
			t.Fatalf("finished = %+v", workflow)
		}
		finished.ActiveRuns = map[string]ActiveRun{"task-a": runsActive("task-a", "run-b")}
		next := applyRunEvent(finished, contracts.RunEventRunStarted{RunEventBase: contracts.RunEventBase{TaskID: "task-a", RunID: "run-b", Sequence: 1}})
		if _, ok := next.Workflows["task-a"]; ok {
			t.Fatal("an ended workflow was carried into the next run")
		}
		carrying := second
		carrying.ActiveRuns = map[string]ActiveRun{"task-a": runsActive("task-a", "run-b")}
		carried := applyRunEvent(carrying, contracts.RunEventRunStarted{RunEventBase: contracts.RunEventBase{TaskID: "task-a", RunID: "run-b", Sequence: 1}})
		if workflows := carried.Workflows["task-a"]; len(workflows) != 1 || workflows[0].ID != "wf-1" {
			t.Fatal("a running workflow was not carried")
		}
	})
}

func TestThreadRunStateLocaleNumber(t *testing.T) {
	for value, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", 182_000: "182,000", 1234567.891: "1,234,567.891", -1234.5: "-1,234.5", 0.0004: "0"} {
		if got := runsLocaleNumber(value); got != want {
			t.Errorf("runsLocaleNumber(%v) = %q, want %q", value, got, want)
		}
	}
}
