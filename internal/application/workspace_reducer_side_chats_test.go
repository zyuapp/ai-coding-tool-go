package application

import (
	"math"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func sideChatSource(edits ...func(*domain.Thread)) *domain.Thread {
	return coreTask("main-task", append([]func(*domain.Thread){func(t *domain.Thread) {
		t.Continuation = &domain.Continuation{Provider: "claude", Value: "main-session"}
		t.ContinuationStatus = domain.ContinuationStatusAvailable
	}}, edits...)...)
}

func TestSideChatSnapshotsTheSourceSettings(t *testing.T) {
	corePorted(t, func() {
		source := sideChatSource(func(t *domain.Thread) {
			t.ExecutionPolicy, t.Model, t.Effort = domain.ExecutionPolicyConfirm, new("opus"), new(domain.AgentEffort("high"))
		})
		opened := coreRun(coreWorkspace(threadsOn([]*domain.Thread{source}, "main-task")), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"})
		chat := threadsFind(opened.Threads, "chat-1")
		if chat == nil || chat.Title != "Chat 1" || chat.ExecutionPolicy != domain.ExecutionPolicyConfirm || *chat.Model != "opus" || *chat.Effort != "high" || len(chat.Messages) != 0 || chat.Continuation != nil {
			t.Fatalf("side chat: %s", coreJSON(t, chat))
		}
		if len(opened.SideChats) != 1 || opened.SideChats[0].SourceThreadID != "main-task" || opened.SideChatSequence != 1 {
			t.Fatalf("side chats: %s", coreJSON(t, opened.SideChats))
		}
		if dock := DockFor(opened, "main-task"); !dock.Open || dock.Tab != "chat-1" {
			t.Fatalf("dock: %s", coreJSON(t, dock))
		}
		retuned := coreRun(opened,
			contracts.TaskCommandTaskSetPolicy{TaskID: new("chat-1"), Policy: domain.ExecutionPolicyAutonomous},
			contracts.TaskCommandTaskSetModel{TaskID: new("chat-1"), Engine: domain.AgentEngineClaude, Model: new("sonnet")},
			contracts.TaskCommandTaskSetEffort{TaskID: new("chat-1"), Engine: domain.AgentEngineClaude, Effort: "low"},
			contracts.TaskCommandTaskSetPolicy{TaskID: new("main-task"), Policy: domain.ExecutionPolicyAllowEdits},
		)
		chat, main := threadsFind(retuned.Threads, "chat-1"), threadsFind(retuned.Threads, "main-task")
		if chat.ExecutionPolicy != domain.ExecutionPolicyAutonomous || *chat.Model != "sonnet" || *chat.Effort != "low" || main.ExecutionPolicy != domain.ExecutionPolicyAllowEdits {
			t.Fatalf("retuned: %s / %s", coreJSON(t, chat), coreJSON(t, main))
		}
		if reopened := coreRun(retuned, contracts.SideChatCommandSideChatOpen{ChatID: "chat-2"}); threadsFind(reopened.Threads, "chat-2").Title != "Chat 2" {
			t.Fatal("the second chat's name")
		}
		if none := coreRun(coreWorkspace(), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"}); len(none.SideChats) != 0 {
			t.Fatal("opened with no source")
		}
	})
}

func TestReadingPointKeptAndRefused(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("task-a")}, "task-a"))
		placed := Reduce(state, contracts.ViewCommandViewReadingPoint{TaskID: "task-a", Point: &contracts.ReadingPointValue{Anchor: "m3", Depth: 72}}).State
		coreEqualJSON(t, placed.ReadingPoints["task-a"], `{"anchor":"m3","depth":72}`, "placed")
		again := Reduce(&placed, contracts.ViewCommandViewReadingPoint{TaskID: "task-a", Point: &contracts.ReadingPointValue{Anchor: "m3", Depth: 72}}).State
		if again.ReadingPoints["task-a"] != placed.ReadingPoints["task-a"] {
			t.Fatal("an unchanged place was written")
		}
		cleared := Reduce(&placed, contracts.ViewCommandViewReadingPoint{TaskID: "task-a"}).State
		if point, ok := cleared.ReadingPoints["task-a"]; !ok || point != nil {
			t.Fatal("a cleared place is kept as null")
		}
		for _, point := range []*contracts.ReadingPointValue{{Anchor: "", Depth: 0}, {Anchor: "m1", Depth: math.NaN()}, {Anchor: "m1", Depth: math.Inf(1)}} {
			if refused := ReduceComposer(state, contracts.ViewCommandViewReadingPoint{TaskID: "task-a", Point: point}); refused.Patch != nil {
				t.Fatalf("%+v is not a place", point)
			}
		}
		if refused := ReduceComposer(state, contracts.ViewCommandViewReadingPoint{TaskID: "no-such-thread", Point: &contracts.ReadingPointValue{Anchor: "m1"}}); refused.Patch != nil {
			t.Fatal("a place for no thread")
		}
	})
}

func TestClosingASideChatTakesItsDraftsAndCancelsItsRun(t *testing.T) {
	corePorted(t, func() {
		opened := coreRun(coreWorkspace(threadsOn([]*domain.Thread{sideChatSource(), coreTask("other")}, "main-task"), func(s *WorkspaceState) {
			s.ReadingPoints = map[string]contracts.ReadingPoint{"chat-1": {Anchor: "m2", Depth: 10}, "main-task": {Anchor: "m4", Depth: 30}}
		}), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"})
		running := *opened
		running.Files = map[string][]*domain.AttachedFile{"chat-1": {{ID: "file-1", Path: "/repo/notes.md", Name: "notes.md"}}}
		running.Prompts = map[string]string{"chat-1": "Ask"}
		running.ActiveRuns = map[string]ActiveRun{"chat-1": coreActive("chat-1", "run-chat")}
		running.Approvals = map[string]*ApprovalView{
			"run-chat":  {ApprovalID: "chat-1-approval", TaskID: "chat-1", RunID: "run-chat", Title: "Run", ToolName: "Bash"},
			"run-other": {ApprovalID: "other-approval", TaskID: "other", RunID: "run-other", Title: "Run", ToolName: "Bash"},
		}
		running.Automations = []*domain.AutomationView{threadsAutomation("chat-1"), threadsAutomation("main-task")}
		closed := Reduce(&running, contracts.SideChatCommandSideChatClose{ChatID: "chat-1"})
		cancel := coreEffect[WorkspaceEffectSendRunCommand](t, closed)
		coreEqualJSON(t, cancel.Command, `{"type":"cancel","taskId":"chat-1","runId":"run-chat"}`, "cancel")
		if deleted := coreEffect[WorkspaceEffectAutomationDelete](t, closed); deleted.TaskID != "chat-1" {
			t.Fatalf("retired %s", deleted.TaskID)
		}
		state := closed.State
		threadsSame(t, threadsIDs(state.Threads), []string{"main-task", "other"}, "the thread went with it")
		_, active := state.ActiveRuns["chat-1"]
		_, prompt := state.Prompts["chat-1"]
		_, files := state.Files["chat-1"]
		_, point := state.ReadingPoints["chat-1"]
		_, approval := state.Approvals["run-chat"]
		_, otherApproval := state.Approvals["run-other"]
		if len(state.SideChats) != 0 || active || prompt || files || point || approval || !otherApproval {
			t.Fatalf("left behind: %s", coreJSON(t, state))
		}
	})
}

func TestSelectingASideChatOpensItsTabOnTheSource(t *testing.T) {
	corePorted(t, func() {
		opened := coreRun(coreWorkspace(threadsOn([]*domain.Thread{sideChatSource(), coreTask("other")}, "main-task")), contracts.SideChatCommandSideChatOpen{ChatID: "chat-1"})
		marked := *opened
		marked.Threads = make([]*domain.Thread, len(opened.Threads))
		for index, thread := range opened.Threads {
			if thread.ID == "chat-1" {
				unread := *thread
				unread.Outcome, unread.OutcomeUnread = new(domain.ThreadOutcomeFinished), new(true)
				thread = &unread
			}
			marked.Threads[index] = thread
		}
		if !ThreadActivityThreadLists(&marked).SideChatAttention.Has("main-task") || ThreadActivityThreadLists(&marked).UnreadCount != 1 {
			t.Fatal("the chat's mark counts under its source")
		}
		elsewhere := coreRun(&marked, contracts.TaskCommandTaskSelect{TaskID: "other"})
		if len(elsewhere.SideChats) != 1 {
			t.Fatal("leaving the thread closed its chat")
		}
		clicked := coreRun(elsewhere, contracts.TaskCommandTaskSelect{TaskID: "chat-1"})
		if *clicked.CurrentID != "main-task" || !DockFor(clicked, "main-task").Open || DockFor(clicked, "main-task").Tab != "chat-1" || threadsFind(clicked.Threads, "chat-1").OutcomeUnread != nil {
			t.Fatalf("clicked: current %v dock %s", *clicked.CurrentID, coreJSON(t, DockFor(clicked, "main-task")))
		}
	})
}

func TestChatFailedSaysWhyInTheChat(t *testing.T) {
	edit := SideChatsEdit{View: SideChatsState{SideChats: []*SideChat{{ID: "chat-1", SourceThreadID: "main"}, {ID: "chat-2", SourceThreadID: "main"}}}}
	failed := ChatFailedIn(edit, "chat-1", "No checkout")
	if *failed.View.SideChats[0].Error != "No checkout" || failed.View.SideChats[1] != edit.View.SideChats[1] || edit.View.SideChats[0].Error != nil {
		t.Fatalf("failed: %s", coreJSON(t, failed.View))
	}
}
