package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// threadsForked is the copy a fork made: the one thread after has that before did not.
func threadsForked(t *testing.T, before, after *WorkspaceState) *domain.Thread {
	t.Helper()
	known := map[string]bool{}
	for _, thread := range before.Threads {
		known[thread.ID] = true
	}
	for _, thread := range after.Threads {
		if !known[thread.ID] {
			return thread
		}
	}
	t.Fatal("expected a copy")
	return nil
}

func threadsOn(threads []*domain.Thread, current string) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.Threads = threads
		state.CurrentID = new(current)
	}
}

func TestForkCarriesTheConversationAndSitsUnderItsSource(t *testing.T) {
	corePorted(t, func() {
		source := coreTask("first", func(t *domain.Thread) {
			t.Title = "Fix the login"
			t.ProjectID = new("project-1")
			t.ExecutionPolicy = domain.ExecutionPolicyAutonomous
			t.Model = new("opus")
			t.Effort = new(domain.AgentEffort("high"))
			t.SortIndex = new(0.0)
			t.Messages = []*domain.ConversationMessage{{ID: "m1", Kind: domain.ConversationMessageKindUser, Text: "Have a look", At: 5}}
			t.Continuation = &domain.Continuation{Provider: "claude", Value: "session-1"}
			t.ContinuationStatus = domain.ContinuationStatusAvailable
			t.Outcome = new(domain.ThreadOutcomeFinished)
			t.OutcomeUnread = new(true)
			t.Findings = []*domain.AutomationFinding{{ID: "f1", Headline: "Something", At: 6}}
		})
		other := coreTask("second", thProject("project-1"), thSorted(1))
		before := coreWorkspace(threadsOn([]*domain.Thread{source, other}, "first"))
		state := Reduce(before, contracts.TaskCommandTaskFork{}).State
		fork := threadsForked(t, before, &state)
		if fork.Title != "Fix the login (fork)" || *fork.ProjectID != "project-1" || fork.ExecutionPolicy != domain.ExecutionPolicyAutonomous || *fork.Model != "opus" || *fork.Effort != "high" {
			t.Fatalf("fork: %s", coreJSON(t, fork))
		}
		coreEqualJSON(t, fork.Messages, source.Messages, "messages")
		if fork.Continuation == nil || fork.Continuation.Value != "session-1" || fork.InheritedContinuation == nil || !*fork.InheritedContinuation {
			t.Fatal("session not inherited")
		}
		if fork.Outcome != nil || fork.Findings != nil || fork.WorktreeID != nil {
			t.Fatal("the source's testimony crossed over")
		}
		if *state.CurrentID != fork.ID {
			t.Fatal("the copy was not opened")
		}
		threadsSame(t, threadsIDs(OrderThreads(state.Threads)), []string{"first", fork.ID, "second"}, "order")
	})
}

func TestForkSitsUnderItsSourceBesideACoordinator(t *testing.T) {
	corePorted(t, func() {
		threads := []*domain.Thread{
			coreTask("lead", thProject("p"), thSorted(0), thRole(domain.ThreadRoleCoordinator)),
			coreTask("m1", thProject("p"), thSorted(1), thUnder("lead")),
			coreTask("m2", thProject("p"), thSorted(2), thUnder("lead")),
			coreTask("source", thProject("p"), thSorted(3)),
			coreTask("next", thProject("p"), thSorted(4)),
		}
		rows := func(state *WorkspaceState) []string {
			var top []*domain.Thread
			for _, thread := range state.Threads {
				if thread.ParentID == nil {
					top = append(top, thread)
				}
			}
			return threadsIDs(OrderThreads(top))
		}
		before := coreWorkspace(threadsOn(threads, "source"))
		fromSource := Reduce(before, contracts.TaskCommandTaskFork{}).State
		threadsSame(t, rows(&fromSource), []string{"lead", "source", threadsForked(t, before, &fromSource).ID, "next"}, "from source")
		member := *before
		member.CurrentID = new("m1")
		fromMember := Reduce(&member, contracts.TaskCommandTaskFork{}).State
		threadsSame(t, rows(&fromMember), []string{"lead", threadsForked(t, before, &fromMember).ID, "source", "next"}, "from a member")
	})
}

func TestForkOfAForkIsNumbered(t *testing.T) {
	corePorted(t, func() {
		before := coreWorkspace(threadsOn([]*domain.Thread{coreTask("first", func(t *domain.Thread) { t.Title = "Fix the login" })}, "first"))
		once := Reduce(before, contracts.TaskCommandTaskFork{}).State
		fork := threadsForked(t, before, &once)
		twice := Reduce(&once, contracts.TaskCommandTaskFork{TaskID: new(fork.ID)}).State
		var titles []string
		for _, thread := range twice.Threads {
			titles = append(titles, thread.Title)
		}
		threadsSame(t, titles, []string{"Fix the login", "Fix the login (fork)", "Fix the login (fork 2)"}, "titles")
	})
}

func TestForkIntoWorktreeAsksForACheckout(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("first", thProject("project-1"))}, "first"), func(state *WorkspaceState) {
			state.Projects = []*domain.Project{{ID: "project-1", Root: "/work/api", WorkspaceID: new("workspace-1")}}
			state.OpenMenu = new("workspace:open-in")
		})
		transition := Reduce(state, contracts.TaskCommandTaskFork{Worktree: new(true)})
		fork := threadsForked(t, state, &transition.State)
		created := coreEffect[WorkspaceEffectCreateWorktree](t, transition)
		if created.TaskID != fork.ID || created.ProjectRoot != "/work/api" || transition.State.OpenMenu != nil {
			t.Fatalf("checkout: %s", coreJSON(t, created))
		}
		local := coreWorkspace(threadsOn([]*domain.Thread{coreTask("first")}, "first"))
		unplaced := Reduce(local, contracts.TaskCommandTaskFork{Worktree: new(true)})
		if *unplaced.State.CurrentID != threadsForked(t, local, &unplaced.State).ID || unplaced.State.ActionError == nil || unplaced.Result != nil {
			t.Fatalf("unplaced: error %v, result %s", unplaced.State.ActionError, coreJSON(t, unplaced.Result))
		}
	})
}

func TestForkDialogSuggestsTheDefaultName(t *testing.T) {
	corePorted(t, func() {
		before := coreWorkspace(threadsOn([]*domain.Thread{coreTask("first", func(t *domain.Thread) { t.Title = "Fix the login" })}, "first"))
		asked := Reduce(before, contracts.ViewCommandViewForkName{SourceID: new("first")}).State
		coreEqualJSON(t, asked.ForkName, ForkName{TaskID: "first", Worktree: false, Title: "Fix the login (fork)"}, "suggested")
		if len(asked.Threads) != 1 {
			t.Fatal("forked before the name was confirmed")
		}
		confirmed := Reduce(&asked, contracts.ViewCommandViewForkConfirm{Title: "  Try OAuth  "}).State
		fork := threadsForked(t, before, &confirmed)
		if fork.Title != "Try OAuth" || confirmed.ForkName != nil || *confirmed.CurrentID != fork.ID {
			t.Fatalf("confirmed: %s", coreJSON(t, fork))
		}
		closed := Reduce(&asked, contracts.ViewCommandViewForkName{}).State
		if closed.ForkName != nil || len(closed.Threads) != 1 {
			t.Fatal("closing the dialog forked")
		}
	})
}

func TestForkedThreadsDirectly(t *testing.T) {
	source := coreTask("first", func(t *domain.Thread) { t.Engine = domain.AgentEngineCodex; t.FastMode = nil })
	threads, fork := ForkedThreads([]*domain.Thread{source}, source, "copy", 50, new("  "))
	if len(threads) != 2 || fork.ID != "copy" || fork.Title != "first (fork)" || fork.RankedAt != 50 {
		t.Fatalf("fork: %s", coreJSON(t, fork))
	}
	if domain.CapabilitiesFor(string(domain.AgentEngineCodex)).FastMode && (fork.FastMode == nil || *fork.FastMode) {
		t.Fatal("an engine with fast mode starts the copy with it off")
	}
	chat := SideChatThread(source, "chat", "Chat 1", 7)
	if chat.Title != "Chat 1" || len(chat.Messages) != 0 || chat.ContinuationStatus != domain.ContinuationStatusNone || *chat.CreatedAt != 7 {
		t.Fatalf("side chat: %s", coreJSON(t, chat))
	}
}
