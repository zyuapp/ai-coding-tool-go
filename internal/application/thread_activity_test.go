package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func threadsWorkflow(status domain.WorkflowStatus) *domain.Workflow {
	return &domain.Workflow{ID: "wf", Name: "wf", Status: status, Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}, StartedAt: 1}
}

func threadsSubagent(status domain.SubagentStatus) *domain.Subagent {
	return &domain.Subagent{ID: "agent", Description: "Review", Status: status}
}

func threadsAutomation(taskID string, edits ...func(*domain.AutomationView)) *domain.AutomationView {
	view := &domain.AutomationView{Automation: domain.Automation{ID: "automation-" + taskID, TaskID: taskID, Prompt: "Poll", Schedule: "0 * * * *", CreatedAt: 1, UpdatedAt: 1}}
	for _, edit := range edits {
		edit(view)
	}
	return view
}

func threadsQueued() map[string][]*QueuedMessage {
	return map[string][]*QueuedMessage{"t": {{HeldMessage: domain.HeldMessage{ID: "message", Text: "next", Prompt: "next", Attachments: []string{}}}}}
}

func TestEachConditionGivesOneStatus(t *testing.T) {
	inWorktree := func(t *domain.Thread) { t.WorktreeID = new("wt") }
	paused := func(window domain.UsageLimitWindow) func(*WorkspaceState) {
		return func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{coreTask("t", inWorktree, func(t *domain.Thread) {
				t.LimitPause = &domain.LimitPause{UsageLimit: domain.UsageLimit{ResetsAt: 10, Window: window}, PausedAt: 1}
			})}
		}
	}
	awaiting := coreActive("t", "run").(ActiveRunRunning)
	awaiting.Status = ActiveRunRunningStatusAwaitingApproval
	for _, item := range []struct {
		condition       string
		edit            func(*WorkspaceState)
		status          ThreadStatus
		working, ranked bool
	}{
		{"nothing going", func(*WorkspaceState) {}, ThreadStatusIdle, false, false},
		{"a run", func(s *WorkspaceState) { s.ActiveRuns = map[string]ActiveRun{"t": coreActive("t", "run")} }, ThreadStatusWorking, true, true},
		{"a run awaiting approval", func(s *WorkspaceState) { s.ActiveRuns = map[string]ActiveRun{"t": awaiting} }, ThreadStatusBlocked, true, true},
		{"a send resolving", func(s *WorkspaceState) {
			s.PendingRuns = map[string]*PendingRun{"p": {ID: "p", RunID: "run", Origin: PendingRunOriginComposer, TaskID: new("t"), Text: "go", Prompt: "go", Attachments: []string{}}}
		}, ThreadStatusWorking, true, true},
		{"a queued message", func(s *WorkspaceState) { s.QueuedMessages = threadsQueued() }, ThreadStatusWorking, true, true},
		{"an empty queue", func(s *WorkspaceState) { s.QueuedMessages = map[string][]*QueuedMessage{"t": {}} }, ThreadStatusIdle, false, false},
		{"a checkout being made", func(s *WorkspaceState) { s.CreatingWorktrees = []string{"t"} }, ThreadStatusWorking, true, true},
		{"its checkout being released", func(s *WorkspaceState) { s.ReleasingWorktrees = []string{"t"} }, ThreadStatusWorking, true, true},
		{"its checkout being deleted", func(s *WorkspaceState) { s.DeletingWorktrees = []string{"/wt"} }, ThreadStatusWorking, true, true},
		{"a running workflow", func(s *WorkspaceState) {
			s.Workflows = map[string][]*domain.Workflow{"t": {threadsWorkflow(domain.WorkflowStatusRunning)}}
		}, ThreadStatusWorking, true, true},
		{"a finished workflow", func(s *WorkspaceState) {
			s.Workflows = map[string][]*domain.Workflow{"t": {threadsWorkflow(domain.WorkflowStatusCompleted)}}
		}, ThreadStatusIdle, false, false},
		{"a working subagent", func(s *WorkspaceState) {
			s.Subagents = map[string][]*domain.Subagent{"t": {threadsSubagent(domain.SubagentStatusWorking)}}
		}, ThreadStatusWorking, true, true},
		{"a stopped subagent", func(s *WorkspaceState) {
			s.Subagents = map[string][]*domain.Subagent{"t": {threadsSubagent(domain.SubagentStatusStopped)}}
		}, ThreadStatusIdle, false, false},
		{"background processes alone", func(s *WorkspaceState) {
			s.BackgroundProcesses = map[string][]*domain.BackgroundProcess{"t": {{ID: "shell", Kind: "shell", Description: "npm run dev"}}}
		}, ThreadStatusIdle, false, true},
		{"a background process stopping", func(s *WorkspaceState) {
			s.BackgroundProcesses = map[string][]*domain.BackgroundProcess{"t": {{ID: "shell", Kind: "shell", Description: "npm run dev", Stopping: new(true)}}}
		}, ThreadStatusIdle, false, false},
		{"a session limit", paused(domain.UsageLimitWindowSession), ThreadStatusPaused, true, true},
		{"a weekly limit", paused(domain.UsageLimitWindowWeekly), ThreadStatusPaused, false, false},
		{"a weekly limit holding a message", func(s *WorkspaceState) { paused(domain.UsageLimitWindowWeekly)(s); s.QueuedMessages = threadsQueued() }, ThreadStatusPaused, false, false},
		{"a live watch", func(s *WorkspaceState) {
			s.Automations = []*domain.AutomationView{threadsAutomation("t", func(a *domain.AutomationView) { a.EndsWhen = new("the PR merges") })}
		}, ThreadStatusIdle, false, true},
		{"a paused watch", func(s *WorkspaceState) {
			s.Automations = []*domain.AutomationView{threadsAutomation("t", func(a *domain.AutomationView) { a.EndsWhen = new("the PR merges"); a.Paused = true })}
		}, ThreadStatusIdle, false, false},
		{"a routine", func(s *WorkspaceState) { s.Automations = []*domain.AutomationView{threadsAutomation("t")} }, ThreadStatusIdle, false, false},
	} {
		state := coreWorkspace(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{coreTask("t", inWorktree)}
			state.Worktrees = []*domain.Worktree{{ID: "wt", ProjectID: "project", Root: "/wt", WorkspaceID: "workspace", BaseCommit: "abc", CreatedAt: 1, LastUsedAt: 1}}
		}, item.edit)
		activity := ThreadActivityThreadActivity(state)
		if got := ThreadActivityThreadStatus(state, "t"); got != item.status || activity.Working.Has("t") != item.working || activity.Ranked.Has("t") != item.ranked {
			t.Errorf("%s: status %s, working %v, ranked %v", item.condition, got, activity.Working.Has("t"), activity.Ranked.Has("t"))
		}
	}
}

func TestMonitoringOnlyWithoutWork(t *testing.T) {
	watch := map[string][]*domain.BackgroundProcess{"t": {{ID: "watch", Kind: "monitor", Description: "CI"}}}
	idle := coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{coreTask("t")}; s.BackgroundProcesses = watch })
	if !ThreadActivityThreadActivity(idle).Monitoring.Has("t") {
		t.Fatal("not monitoring")
	}
	running := coreWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{coreTask("t")}
		s.BackgroundProcesses = watch
		s.ActiveRuns = map[string]ActiveRun{"t": coreActive("t", "run")}
	})
	if ThreadActivityThreadActivity(running).Monitoring.Has("t") {
		t.Fatal("a run going shows as monitoring")
	}
}

func TestActivityAndListsAreDerivedOnce(t *testing.T) {
	state := coreWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{coreTask("t")}
		s.ActiveRuns = map[string]ActiveRun{"t": coreActive("t", "run")}
	})
	if ThreadActivityThreadActivity(state) != ThreadActivityThreadActivity(state) {
		t.Fatal("activity derived twice")
	}
	typing := *state
	typing.Prompts = map[string]string{"t": "typing"}
	if ThreadActivityThreadLists(&typing) != ThreadActivityThreadLists(state) {
		t.Fatal("lists derived again for a state keeping threads and side chats")
	}
	forked := *state
	forked.SideChats = []*SideChat{{ID: "t", SourceThreadID: "s"}}
	if ThreadActivityThreadLists(&forked) == ThreadActivityThreadLists(state) {
		t.Fatal("lists kept across a change of side chats")
	}
}

func TestListsLeaveOutSideChatsAndCountTheirMarks(t *testing.T) {
	unread := func(value domain.ThreadOutcome) func(*domain.Thread) {
		return func(t *domain.Thread) { t.Outcome = new(value); t.OutcomeUnread = new(true) }
	}
	state := coreWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{
			coreTask("main"),
			coreTask("chat", unread(domain.ThreadOutcomeFinished)),
			coreTask("filed", thArchived(5), unread(domain.ThreadOutcomeFailed), func(t *domain.Thread) { t.WorktreeID = new("wt") }),
		}
		s.SideChats = []*SideChat{{ID: "chat", SourceThreadID: "main"}}
	})
	lists := ThreadActivityThreadLists(state)
	threadsSame(t, threadsIDs(lists.VisibleThreads), []string{"main"}, "visible")
	threadsSame(t, threadsIDs(lists.ArchivedThreads), []string{"filed"}, "archived")
	threadsSame(t, lists.WorktreeThreadIDs.Sorted(), []string{"filed"}, "worktree threads")
	threadsSame(t, lists.SideChatAttention.Sorted(), []string{"main"}, "side chat attention")
	if lists.UnreadCount != 1 {
		t.Fatalf("unread %v", lists.UnreadCount)
	}

	led := coreWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{
			coreTask("lead", thRole(domain.ThreadRoleCoordinator), unread(domain.ThreadOutcomeFinished)),
			coreTask("a", thUnder("lead"), unread(domain.ThreadOutcomeFinished)),
			coreTask("b", thUnder("lead"), unread(domain.ThreadOutcomeFailed)),
			coreTask("quiet", thRole(domain.ThreadRoleCoordinator)),
			coreTask("c", thUnder("quiet"), unread(domain.ThreadOutcomeFinished)),
			coreTask("own", unread(domain.ThreadOutcomeFinished)),
		}
	})
	if got := ThreadActivityThreadLists(led).UnreadCount; got != 3 {
		t.Fatalf("a coordinator's row counted %v", got)
	}
}

func TestLandingOnACoordinatorReadsItsThreads(t *testing.T) {
	corePorted(t, func() {
		unread := func(t *domain.Thread) { t.Outcome = new(domain.ThreadOutcomeFinished); t.OutcomeUnread = new(true) }
		state := coreWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{coreTask("lead", thRole(domain.ThreadRoleCoordinator)), coreTask("a", thUnder("lead"), unread), coreTask("other", unread)}
		})
		landed := Reduce(state, contracts.TaskCommandTaskSelect{TaskID: "lead"}).State
		if threadsFind(landed.Threads, "a").OutcomeUnread != nil || threadsFind(landed.Threads, "other").OutcomeUnread == nil {
			t.Fatal("reading the row")
		}
		if ThreadActivityThreadLists(&landed).UnreadCount != 1 {
			t.Fatal("unread count")
		}
	})
}

func TestWaitingOnCheckoutBeforeRun(t *testing.T) {
	pending := func(edit func(*PendingRun)) func(*WorkspaceState) {
		return func(s *WorkspaceState) {
			run := &PendingRun{ID: "p", RunID: "run", Origin: PendingRunOriginComposer, TaskID: new("t"), Text: "go", Prompt: "go", Attachments: []string{}}
			edit(run)
			s.PendingRuns = map[string]*PendingRun{"p": run}
		}
	}
	none := func(*PendingRun) {}
	check := func(state *WorkspaceState, taskID *string, draftKey string, want ThreadWait, waits bool) {
		t.Helper()
		if got, ok := WaitingOn(state, taskID, draftKey); got != want || ok != waits {
			t.Fatalf("waiting on %q %v", got, ok)
		}
	}
	check(coreWorkspace(pending(none), func(s *WorkspaceState) { s.CreatingWorktrees = []string{"t"} }), new("t"), "t", ThreadWaitWorktree, true)
	check(coreWorkspace(pending(none), func(s *WorkspaceState) { s.ReleasingWorktrees = []string{"t"} }), new("t"), "t", ThreadWaitWorktreeRelease, true)
	check(coreWorkspace(pending(none)), new("t"), "t", ThreadWaitRun, true)
	check(coreWorkspace(pending(func(run *PendingRun) {
		run.TaskID = nil
		run.DraftKey = new("draft:")
		run.CreatingWorktree = new(true)
	})), nil, "draft:", ThreadWaitWorktree, true)
	check(coreWorkspace(), new("t"), "t", "", false)
}

func TestThreadChangesAndActivityChanges(t *testing.T) {
	a, b := coreTask("a"), coreTask("b")
	before := []*domain.Thread{a, b}
	b2 := *b
	b2.Title = "renamed"
	if changes := ThreadChanges(before, []*domain.Thread{a, &b2}); len(changes) != 1 || changes[0].ID != "b" || changes[0].Before != b || changes[0].After != &b2 {
		t.Fatalf("aligned: %s", coreJSON(t, changes))
	}
	c := coreTask("c")
	changes := ThreadChanges(before, []*domain.Thread{c, a})
	if len(changes) != 2 || changes[0].ID != "c" || changes[0].Before != nil || changes[1].ID != "b" || changes[1].After != nil {
		t.Fatalf("realigned: %s", coreJSON(t, changes))
	}
	state := coreWorkspace(func(s *WorkspaceState) { s.Threads = before })
	running := *state
	running.ActiveRuns = map[string]ActiveRun{"a": coreActive("a", "run")}
	threadsSame(t, ActivityChanges(state, &running, nil).Sorted(), []string{"a"}, "a run starting")
	same := running
	same.ActiveRuns = map[string]ActiveRun{"a": coreActive("a", "run")}
	if changed := ActivityChanges(&running, &same, nil); len(changed) != 0 {
		t.Fatalf("an unchanged run changed %v", changed.Sorted())
	}
}
