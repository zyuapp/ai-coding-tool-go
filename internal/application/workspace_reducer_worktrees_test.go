package application

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func awMadeWorktree(id string) contracts.CreatedWorktree {
	return contracts.CreatedWorktree{ID: id, Root: "/worktrees/repo-" + id, WorkspaceID: "worktree-" + id, BaseCommit: "abcdef1234", CreatedAt: 2, LastUsedAt: 2}
}

// A project with `threads` working in the held checkout wt1, the first one on screen.
func awInside(threads ...*domain.Thread) *WorkspaceState {
	return awProjected(func(state *WorkspaceState) {
		state.Worktrees = []*domain.Worktree{awHeldWorktree("wt1")}
		for _, thread := range threads {
			thread.WorktreeID = new("wt1")
		}
		state.Threads = threads
		state.CurrentID = new(threads[0].ID)
	})
}

func awLastMessage(t *testing.T, thread *domain.Thread) *domain.ConversationMessage {
	t.Helper()
	if len(thread.Messages) == 0 {
		t.Fatalf("%s has no messages", thread.ID)
	}
	return thread.Messages[len(thread.Messages)-1]
}

func TestWorktreeSetMakesACheckoutAndMovesTheThreadIn(t *testing.T) {
	awPorted(t, func() {
		state := awProjected(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("task-a", awInProject("project-a"))}
			state.CurrentID, state.ActionError = new("task-a"), new("Earlier failure")
		})
		asked := awWorktrees(state, contracts.TaskCommandTaskSetWorktree{Worktree: true})
		if !reflect.DeepEqual(asked.Effects, []WorkspaceEffect{WorkspaceEffectCreateWorktree{TaskID: "task-a", ProjectRoot: "/repo"}}) {
			t.Fatalf("effects: %#v", asked.Effects)
		}
		if !reflect.DeepEqual(asked.State.CreatingWorktrees, []string{"task-a"}) || asked.State.ActionError != nil {
			t.Fatalf("creating: %v", asked.State.CreatingWorktrees)
		}
		again := awWorktrees(&asked.State, contracts.TaskCommandTaskSetWorktree{Worktree: true})
		if len(again.Effects) != 0 || awText(again.State.ActionError) != WorktreeCreatingError {
			t.Fatalf("a second ask: %s", awText(again.State.ActionError))
		}
		made := awWorktrees(&asked.State, WorktreeEventWorktreeCreated{TaskID: "task-a", Worktree: awMadeWorktree("wt1")})
		if !reflect.DeepEqual(made.State.Worktrees, []*domain.Worktree{awHeldWorktree("wt1")}) || len(made.State.CreatingWorktrees) != 0 {
			t.Fatalf("record: %#v", made.State.Worktrees)
		}
		thread := made.State.Threads[0]
		if awText(thread.WorktreeID) != "wt1" || !strings.Contains(awLastMessage(t, thread).Text, "Moved into a worktree at /worktrees/repo-wt1") {
			t.Fatalf("thread: %#v", thread.WorktreeID)
		}
		failed := awWorktrees(&asked.State, WorktreeEventWorktreeFailed{TaskID: "task-a", Message: "Git is not installed or is not on the PATH."})
		if failed.State.Threads[0].WorktreeID != nil || awText(failed.State.ActionError) != "Git is not installed or is not on the PATH." || len(failed.State.CreatingWorktrees) != 0 {
			t.Fatalf("failed: %#v", failed.State.CreatingWorktrees)
		}
		inWorktree := awInside(awTask("task-a", awInProject("project-a")))
		if again := awWorktrees(inWorktree, contracts.TaskCommandTaskSetWorktree{Worktree: true}); len(again.Effects) != 0 {
			t.Fatalf("a thread in a worktree got a second one")
		}
		projectless := awProjected(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("task-a")}
			state.CurrentID = new("task-a")
		})
		if refused := awWorktrees(projectless, contracts.TaskCommandTaskSetWorktree{Worktree: true}); awText(refused.State.ActionError) != WorktreeProjectError {
			t.Fatalf("projectless: %s", awText(refused.State.ActionError))
		}
	})
}

func TestWorktreeCreatedAfterArchiveStaysWithItsThread(t *testing.T) {
	awPorted(t, func() {
		state := awProjected(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("task-a", awInProject("project-a"), func(thread *domain.Thread) { thread.ArchivedAt = new(3.0) })}
		})
		created := awWorktrees(state, WorktreeEventWorktreeCreated{TaskID: "task-a", Worktree: awMadeWorktree("wt1")})
		if len(created.Effects) != 0 || len(created.State.Worktrees) != 1 || awText(created.State.Threads[0].WorktreeID) != "wt1" {
			t.Fatalf("created: %#v", created.Effects)
		}
	})
}

func TestWorktreeMoveConfirmationAsksOnlyWhereItCosts(t *testing.T) {
	awPorted(t, func() {
		local := awProjected(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("task-a", awInProject("project-a"))}
			state.CurrentID = new("task-a")
		})
		asked := awWorktrees(local, contracts.ViewCommandViewMoveWorktree{Worktree: new(true)})
		if len(asked.Effects) != 0 || !reflect.DeepEqual(asked.State.WorktreeMove, &WorktreesStateWorktreeMove{TaskID: "task-a", Worktree: true}) {
			t.Fatalf("asked: %#v", asked.State.WorktreeMove)
		}
		if cancelled := awWorktrees(&asked.State, contracts.ViewCommandViewMoveWorktree{}); cancelled.State.WorktreeMove != nil {
			t.Fatalf("cancel")
		}
		confirmed := awWorktrees(&asked.State, contracts.TaskCommandTaskSetWorktree{Worktree: true})
		if confirmed.State.WorktreeMove != nil || len(confirmed.Effects) != 1 {
			t.Fatalf("the question closes with the move: %#v", confirmed.Effects)
		}

		clean := awInside(awTask("task-a", awInProject("project-a")))
		straight := awWorktrees(clean, contracts.ViewCommandViewMoveWorktree{Worktree: new(false)})
		if straight.State.WorktreeMove != nil || len(straight.Effects) != 1 {
			t.Fatalf("a clean thread walks back: %#v", straight.Effects)
		}
		if _, ok := straight.Effects[0].(WorkspaceEffectReleaseWorktree); !ok {
			t.Fatalf("release: %#v", straight.Effects[0])
		}
		holding := awInside(awTask("task-a", awInProject("project-a")))
		holding.Environments = map[string]contracts.ChangedFilesResult{"worktree-wt1": contracts.ChangedFilesResultAvailable{Files: []string{"src/app.ts"}, Branch: new("main")}}
		returning := awWorktrees(holding, contracts.ViewCommandViewMoveWorktree{Worktree: new(false)})
		if returning.State.WorktreeMove == nil || awText(returning.State.Threads[0].WorktreeID) != "wt1" {
			t.Fatalf("a thread holding changes is asked first")
		}
	})
}

func TestWorktreeSetLocalHandsTheLastClaimBack(t *testing.T) {
	awPorted(t, func() {
		worktree := awHeldWorktree("wt1")
		state := awInside(awTask("task-a", awInProject("project-a")), awTask("task-b", awInProject("project-a")))
		leaving := awWorktrees(state, contracts.TaskCommandTaskSetWorktree{Worktree: false})
		if len(leaving.Effects) != 0 || leaving.State.Threads[0].WorktreeID == nil && awText(leaving.State.Threads[1].WorktreeID) != "wt1" {
			t.Fatalf("leaving: %#v", leaving.Effects)
		}
		if leaving.State.Threads[0].WorktreeID != nil || !strings.Contains(awLastMessage(t, leaving.State.Threads[0]).Text, "other threads are still working in it") {
			t.Fatalf("only this thread walks out")
		}
		last := awWorktrees(&leaving.State, contracts.TaskCommandTaskSetWorktree{TaskID: new("task-b"), Worktree: false})
		want := []WorkspaceEffect{WorkspaceEffectReleaseWorktree{TaskID: "task-b", WorktreeID: "wt1", Root: worktree.Root, Title: "task-b"}}
		if !reflect.DeepEqual(last.Effects, want) || !reflect.DeepEqual(last.State.ReleasingWorktrees, []string{"task-b"}) {
			t.Fatalf("last: %#v", last.Effects)
		}
		again := awWorktrees(&last.State, contracts.TaskCommandTaskSetWorktree{TaskID: new("task-b"), Worktree: false})
		if len(again.Effects) != 0 || awText(again.State.ActionError) != WorktreeReleasingError {
			t.Fatalf("a second ask: %s", awText(again.State.ActionError))
		}
		deleting := awWorktrees(&last.State, contracts.WorktreeCommandWorktreeDelete{Root: &worktree.Root})
		if len(deleting.Effects) != 0 || awText(deleting.State.WorktreeManagementError) != WorktreeReleasingError {
			t.Fatalf("deleting while releasing")
		}
		failed := awWorktrees(&last.State, WorktreeEventWorktreeReleaseFailed{TaskID: "task-b", Message: "Git said no"})
		if len(failed.State.ReleasingWorktrees) != 0 || awText(failed.State.ActionError) != "Git said no" || awText(failed.State.Threads[1].WorktreeID) != "wt1" {
			t.Fatalf("release failed")
		}
		released := awWorktrees(&last.State, WorktreeEventWorktreeReleased{TaskID: "task-b", Snapshot: contracts.WorktreeSnapshotResult{Commit: new("1234567890"), ShortCommit: new("1234567"), Ref: new("refs/aicodingtool/wt1")}})
		thread := released.State.Threads[1]
		note := awLastMessage(t, thread)
		if thread.WorktreeID != nil || len(released.State.Worktrees) != 0 || len(released.State.ReleasingWorktrees) != 0 {
			t.Fatalf("released: %#v", released.State.Worktrees)
		}
		if !strings.Contains(note.Text, "committed as 1234567, and the worktree was removed") || !strings.Contains(awText(note.Detail), "git show refs/aicodingtool/wt1") {
			t.Fatalf("note: %s / %s", note.Text, awText(note.Detail))
		}
	})
}

func TestWorktreeDeletedFilesAwayEveryLinkedThread(t *testing.T) {
	awPorted(t, func() {
		worktree := awHeldWorktree("wt1")
		state := awInside(awTask("task-a", awInProject("project-a")), awTask("task-b", awInProject("project-a")))
		deleting := awWorktrees(state, contracts.WorktreeCommandWorktreeDelete{Root: &worktree.Root})
		effect, ok := deleting.Effects[0].(WorkspaceEffectDeleteWorktree)
		if !ok || effect.WorktreeID != "wt1" || effect.Title != "repo-wt1" || !reflect.DeepEqual(deleting.State.DeletingWorktrees, []string{worktree.Root}) {
			t.Fatalf("delete: %#v", deleting.Effects)
		}
		if twice := awWorktrees(&deleting.State, contracts.WorktreeCommandWorktreeDelete{Root: &worktree.Root}); len(twice.Effects) != 0 {
			t.Fatalf("a checkout already going was deleted twice")
		}
		deleted := awWorktrees(&deleting.State, WorktreeEventWorktreeDeleted{WorktreeID: "wt1", Root: worktree.Root, Snapshot: contracts.WorktreeSnapshotResult{Commit: new("abcdef1234")}})
		if len(deleted.State.Worktrees) != 0 || len(deleted.State.DeletingWorktrees) != 0 {
			t.Fatalf("deleted: %#v", deleted.State.Worktrees)
		}
		if awText(deleted.State.WorktreeManagementNotice) != "Deleted "+worktree.Root+". Recover loose work with git show abcdef1234." {
			t.Fatalf("notice: %s", awText(deleted.State.WorktreeManagementNotice))
		}
		for _, thread := range deleted.State.Threads {
			if thread.WorktreeID != nil || !strings.Contains(awLastMessage(t, thread).Text, "Loose work was committed as abcdef1 first.") {
				t.Fatalf("%s: %v", thread.ID, thread.WorktreeID)
			}
		}
		running := *state
		running.ActiveRuns = map[string]ActiveRun{"task-b": ActiveRunRunning{TaskID: "task-b", RunID: "r", Status: ActiveRunRunningStatusRunning}}
		if refused := awWorktrees(&running, contracts.WorktreeCommandWorktreeDelete{Root: &worktree.Root}); len(refused.Effects) != 0 || awText(refused.State.ActionError) != WorktreeRunningError {
			t.Fatalf("a running thread's checkout was deleted")
		}
		if missing := awWorktrees(state, contracts.WorktreeCommandWorktreeDelete{Root: new("/nowhere")}); awText(missing.State.WorktreeManagementError) != WorktreeMissingError {
			t.Fatalf("missing")
		}
	})
}

func TestWorktreeMovedBetweenCheckoutsAndBackToLocal(t *testing.T) {
	awPorted(t, func() {
		first, second := awHeldWorktree("wt1"), awHeldWorktree("wt2")
		first.Name, second.Name = new("Login redesign"), new("Payment fix")
		state := awProjected(func(state *WorkspaceState) {
			state.Worktrees = []*domain.Worktree{first, second}
			state.Threads = []*domain.Thread{awTask("review", awInProject("project-a"), awInWorktree("wt1")), awTask("payment", awInProject("project-a"), awInWorktree("wt2"))}
			state.CurrentID = new("review")
			state.OpenMenu = new(WorktreeMenu)
		})
		moved := awWorktrees(state, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "wt2"}})
		thread := moved.State.Threads[0]
		if awText(thread.WorktreeID) != "wt2" || len(moved.State.Worktrees) != 2 || moved.State.OpenMenu != nil {
			t.Fatalf("moved: %v", awText(thread.WorktreeID))
		}
		if !strings.Contains(awLastMessage(t, thread).Text, "Moved to Payment fix.") {
			t.Fatalf("note: %s", awLastMessage(t, thread).Text)
		}
		for _, effect := range moved.Effects {
			switch effect.(type) {
			case WorkspaceEffectReleaseWorktree, WorkspaceEffectDeleteWorktree:
				t.Fatalf("a move released a checkout")
			}
		}
		local := awWorktrees(&moved.State, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationLocal{}})
		if local.State.Threads[0].WorktreeID != nil || !strings.Contains(awLastMessage(t, local.State.Threads[0]).Text, "Moved to Local.") {
			t.Fatalf("local")
		}
		other := *state
		other.Worktrees = []*domain.Worktree{first, {ID: "far", ProjectID: "elsewhere", Root: "/far", WorkspaceID: "ws-far"}}
		if refused := awWorktrees(&other, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "far"}}); awText(refused.State.ActionError) != "Choose a worktree in this thread's project." {
			t.Fatalf("another project's checkout: %s", awText(refused.State.ActionError))
		}
		if refused := awWorktrees(state, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "gone"}}); awText(refused.State.ActionError) != WorktreeMissingError {
			t.Fatalf("missing: %s", awText(refused.State.ActionError))
		}
		going := *state
		going.DeletingWorktrees = []string{second.Root}
		if refused := awWorktrees(&going, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "wt2"}}); awText(refused.State.ActionError) != WorktreeReleasingError {
			t.Fatalf("going: %s", awText(refused.State.ActionError))
		}
		running := *state
		running.ActiveRuns = map[string]ActiveRun{"review": ActiveRunRunning{TaskID: "review", RunID: "r", Status: ActiveRunRunningStatusRunning}}
		if refused := awWorktrees(&running, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "wt2"}}); awText(refused.State.ActionError) != WorktreeRunningError {
			t.Fatalf("running: %s", awText(refused.State.ActionError))
		}
		waiting := awWorktrees(&running, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "wt2"}, AfterRun: new(true), Then: new("Keep going")})
		if move := waiting.State.MovesAfterRun["review"]; move == nil || awText(move.Then) != "Keep going" {
			t.Fatalf("after run: %#v", waiting.State.MovesAfterRun)
		}
		stay := awWorktrees(&waiting.State, contracts.TaskCommandTaskMoveWorktree{Destination: domain.WorktreeDestinationWorktree{ID: "wt1"}})
		if stay.State.MovesAfterRun["review"] != nil {
			t.Fatalf("asking to stay takes the waiting move back")
		}
	})
}

func TestMoveAfterRunSettledMovesOnceTheRunEnds(t *testing.T) {
	awPorted(t, func() {
		state := awProjected(func(state *WorkspaceState) {
			state.Worktrees = []*domain.Worktree{awHeldWorktree("wt1"), awHeldWorktree("wt2")}
			state.Threads = []*domain.Thread{awTask("review", awInProject("project-a"), awInWorktree("wt1"))}
			state.MovesAfterRun = map[string]*domain.MoveAfterRun{"review": {Destination: domain.WorktreeDestinationWorktree{ID: "wt2"}, Then: new("Keep going")}}
		})
		moved := Afterward(state, SliceTransition(MoveAfterRunSettled(state, "review", domain.RunStatusSucceeded)))
		if awText(moved.Threads[0].WorktreeID) != "wt2" || moved.MovesAfterRun["review"] != nil {
			t.Fatalf("moved: %s", awText(moved.Threads[0].WorktreeID))
		}
		if queued := QueuedFor(moved, "review"); len(queued) != 1 || queued[0].Text != "Keep going" {
			t.Fatalf("queued: %#v", queued)
		}
		stopped := Afterward(state, SliceTransition(MoveAfterRunSettled(state, "review", domain.RunStatusCancelled)))
		if awText(stopped.Threads[0].WorktreeID) != "wt2" || len(QueuedFor(stopped, "review")) != 0 {
			t.Fatalf("a stopped run still moves and goes no further")
		}
		gone := *state
		gone.Worktrees = gone.Worktrees[:1]
		refused := Afterward(&gone, SliceTransition(MoveAfterRunSettled(&gone, "review", domain.RunStatusSucceeded)))
		if awText(refused.Threads[0].WorktreeID) != "wt1" || !strings.Contains(awLastMessage(t, refused.Threads[0]).Text, "Did not move. "+WorktreeMissingError) || len(QueuedFor(refused, "review")) != 0 {
			t.Fatalf("a move that cannot happen says so")
		}
	})
}

func TestWorktreeCheckoutBranch(t *testing.T) {
	awPorted(t, func() {
		state := awProjected(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("task-a", awInProject("project-a"))}
			state.CurrentID = new("task-a")
		})
		checkout := awWorktrees(state, contracts.TaskCommandTaskCheckoutBranch{Branch: "topic", Create: new(true)})
		if !reflect.DeepEqual(checkout.Effects, []WorkspaceEffect{WorkspaceEffectCheckoutBranch{WorkspaceID: "workspace-a", Branch: "topic", Create: new(true)}}) {
			t.Fatalf("checkout: %#v", checkout.Effects)
		}
		draft := awProjected()
		branch := awWorktrees(draft, contracts.TaskCommandTaskCheckoutBranch{Branch: "topic"})
		if branch.State.DraftBranch == nil || branch.State.DraftBranch.Name != "topic" || branch.State.DraftBranch.Create {
			t.Fatalf("a draft records where to start: %#v", branch.State.DraftBranch)
		}
		finished := awWorktrees(state, WorktreeEventCheckoutFinished{Message: new("Uncommitted changes")})
		if awText(finished.State.ActionError) != "Uncommitted changes" {
			t.Fatalf("finished: %s", awText(finished.State.ActionError))
		}
	})
}
