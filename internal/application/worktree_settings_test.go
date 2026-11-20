package application

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// awWorktrees runs the worktree slices an input belongs to.
func awWorktrees(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	typ := InputType(input)
	switch {
	case slices.Contains(WorktreeManagementInputs, typ):
		return awApply(state, ReduceWorktreeManagement, input)
	case slices.Contains(WorktreeMenuInputs, typ):
		return awApply(state, ReduceWorktreeMenu, input)
	}
	return awApply(state, ReduceWorktrees, input)
}

func awHeldWorktree(id string) *domain.Worktree {
	return &domain.Worktree{ID: id, ProjectID: "project-a", Root: "/worktrees/repo-" + id, WorkspaceID: "worktree-" + id, BaseCommit: "abcdef1234", CreatedAt: 2, LastUsedAt: 2}
}

func awStatus(changed *float64, ahead *float64) domain.WorktreeStatus {
	status := domain.WorktreeStatus{ChangedFiles: changed}
	if ahead != nil {
		status.Comparison = &domain.WorktreeStatusComparison{Branch: "origin/main", Ahead: *ahead}
	}
	return status
}

func awSettingsFixture() *WorkspaceState {
	present := awHeldWorktree("wt1")
	missing := awHeldWorktree("missing")
	missing.ProjectID = "project-b"
	return awProjected(func(state *WorkspaceState) {
		state.SettingsOpen = true
		state.Projects = append(state.Projects, &domain.Project{ID: "project-b", Root: "/other/repo", Name: new("repo")})
		state.Worktrees = []*domain.Worktree{present, missing}
		state.Threads = []*domain.Thread{
			awTask("linked", awInProject("project-a"), awInWorktree("wt1")),
			awTask("archived", awInProject("project-a"), awInWorktree("wt1"), func(thread *domain.Thread) { thread.ArchivedAt = new(5.0) }),
			awTask("missing-thread", awInProject("project-b"), awInWorktree("missing")),
		}
		state.ManagedWorktrees = []*domain.ManagedWorktree{{ID: present.ID, Root: present.Root, Repository: new("/repo"), Branch: new("feat/worktrees"), Status: awStatus(new(2.0), new(3.0))}}
	})
}

func awPage(state *WorkspaceState) WorktreeSettingsPage {
	return WorktreeSettingsWorktreeSettingsPage(state, WorktreeSettingsViews(state, ThreadActivityThreadActivity(state).Working))
}

func TestWorktreeSettingsFilterByProjectIncludesMissingFolders(t *testing.T) {
	awPorted(t, func() {
		state := awSettingsFixture()
		page := awPage(state)
		if page.Available[0].Title != "feat/worktrees" || page.Missing[0].Title != "missing-thread" || page.Projects[0].Count != 1 || page.Projects[1].Count != 1 {
			t.Fatalf("page: %#v", page)
		}
		if threads := page.Available[0].Threads; len(threads) != 2 || !threads[1].Archived {
			t.Fatalf("threads: %#v", threads)
		}
		filtered := awWorktrees(state, contracts.WorktreeCommandWorktreeFilterProject{Project: new("project-b")}).State
		selected := awPage(&filtered)
		if len(selected.Available) != 0 || len(selected.Missing) != 1 || !selected.MissingOpen {
			t.Fatalf("selected: %#v", selected)
		}
		collapsed := awWorktrees(&filtered, contracts.WorktreeCommandWorktreeSetMissingOpen{Open: false}).State
		if awPage(&collapsed).MissingOpen {
			t.Fatalf("missing stays open")
		}
		closed := WorktreesOf(&filtered)
		closed = ManagementShownIn(closed, false, false)
		if closed.View.WorktreeSettings.Project != nil {
			t.Fatalf("closing settings keeps the filter")
		}
	})
}

func TestWorktreeRefreshKeepsTheListAndAFailureNeverMakesFoldersMissing(t *testing.T) {
	awPorted(t, func() {
		filtered := awWorktrees(awSettingsFixture(), contracts.WorktreeCommandWorktreeFilterProject{Project: new("project-a")}).State
		refreshing := awWorktrees(&filtered, contracts.WorktreeCommandWorktreeRefresh{})
		if len(refreshing.Effects) != 1 || !awPage(&refreshing.State).Loading || len(awPage(&refreshing.State).Available) != 1 {
			t.Fatalf("refresh: %#v", refreshing.Effects)
		}
		if again := awWorktrees(&refreshing.State, contracts.WorktreeCommandWorktreeRefresh{}); len(again.Effects) != 0 {
			t.Fatalf("a second refresh lists again")
		}
		failed := awWorktrees(&refreshing.State, WorktreeEventWorktreesFailed{Message: "Permission denied"}).State
		page := awPage(&failed)
		if page.Loading || awText(page.Project) != "project-a" || len(page.Available) != 1 || len(page.Missing) != 0 || awText(failed.WorktreeManagementError) != "Permission denied" {
			t.Fatalf("failed: %#v", page)
		}
	})
}

func TestWorktreeDeleteWaitsOnRunsAndForgetsMissingFolders(t *testing.T) {
	awPorted(t, func() {
		state := awSettingsFixture()
		root := awHeldWorktree("wt1").Root
		asking := awWorktrees(state, contracts.WorktreeCommandWorktreeConfirmDelete{Root: &root}).State
		if confirmation := awPage(&asking).Confirmation; confirmation == nil || confirmation.Root != root {
			t.Fatalf("confirmation: %#v", confirmation)
		}
		running := asking
		running.ActiveRuns = map[string]ActiveRun{"linked": ActiveRunRunning{TaskID: "linked", RunID: "running", Status: ActiveRunRunningStatusRunning}}
		if confirmation := awPage(&running).Confirmation; !confirmation.Busy {
			t.Fatalf("a run makes the checkout busy")
		}
		if deleting := awWorktrees(&running, contracts.WorktreeCommandWorktreeDelete{Root: &root}); len(deleting.Effects) != 0 {
			t.Fatalf("a running checkout is deleted")
		}
		unasked := running
		unasked.WorktreeSettings = state.WorktreeSettings
		if confirming := awWorktrees(&unasked, contracts.WorktreeCommandWorktreeConfirmDelete{Root: &root}); confirming.State.WorktreeSettings.Confirming != nil {
			t.Fatalf("a busy checkout is confirmed")
		}

		missing := state.Worktrees[1]
		deleting := awWorktrees(state, contracts.WorktreeCommandWorktreeDelete{Root: &missing.Root, MissingOnly: new(true)})
		effect, ok := deleting.Effects[0].(WorkspaceEffectDeleteWorktree)
		if !ok || effect.MissingOnly == nil || effect.Title != "repo-missing" || awText(deleting.State.Threads[2].WorktreeID) != "missing" {
			t.Fatalf("delete: %#v", deleting.Effects)
		}
		deleted := awWorktrees(&deleting.State, WorktreeEventWorktreeDeleted{WorktreeID: missing.ID, Root: missing.Root, MissingOnly: new(true)}).State
		if deleted.Threads[2].WorktreeID != nil || len(deleted.Threads) != 3 || awText(deleted.WorktreeManagementNotice) != "Forgot "+missing.Root+". Thread history kept." {
			t.Fatalf("deleted: %s", awText(deleted.WorktreeManagementNotice))
		}
		if len(awPage(&deleted).Missing) != 0 {
			t.Fatalf("the forgotten folder is still listed")
		}
	})
}

func awBulkFixture() *WorkspaceState {
	ids := []string{"clean", "dirty", "ahead", "unknown", "busy", "loose"}
	statuses := map[string]domain.WorktreeStatus{
		"clean": awStatus(new(0.0), new(0.0)), "dirty": awStatus(new(1.0), new(0.0)), "ahead": awStatus(new(0.0), new(2.0)),
		"unknown": awStatus(new(0.0), nil), "busy": awStatus(new(0.0), new(0.0)), "loose": awStatus(new(0.0), new(0.0)),
	}
	return awProjected(func(state *WorkspaceState) {
		state.SettingsOpen = true
		for _, id := range ids {
			record := awHeldWorktree(id)
			if id != "loose" {
				state.Worktrees = append(state.Worktrees, record)
			}
			state.ManagedWorktrees = append(state.ManagedWorktrees, &domain.ManagedWorktree{ID: id, Root: record.Root, Repository: new("/repo"), Status: statuses[id]})
		}
		state.Threads = []*domain.Thread{awTask("in-clean", awInProject("project-a"), awInWorktree("clean")), awTask("in-busy", awInProject("project-a"), awInWorktree("busy"))}
		state.ActiveRuns = map[string]ActiveRun{"in-busy": ActiveRunRunning{TaskID: "in-busy", RunID: "run", Status: ActiveRunRunningStatusRunning}}
	})
}

func awRoots(ids ...string) []string {
	roots := []string{}
	for _, id := range ids {
		roots = append(roots, awHeldWorktree(id).Root)
	}
	return roots
}

func awIDs[T any](items []T, id func(T) string) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, id(item))
	}
	return out
}

func TestWorktreeBulkDeleteRejudgesEachRootAndReportsOneCount(t *testing.T) {
	awPorted(t, func() {
		page := awPage(awBulkFixture())
		if got := awIDs(page.Safe, func(view *WorktreeSettingsView) string { return view.ID }); !reflect.DeepEqual(got, []string{"clean", "loose"}) || page.SafeConfirmation != nil {
			t.Fatalf("safe: %v", got)
		}
		state := awWorktrees(awBulkFixture(), contracts.WorktreeCommandWorktreeConfirmDeleteSafe{Open: true}).State
		if got := awIDs(awPage(&state).SafeConfirmation, func(view *WorktreeSettingsView) string { return view.ID }); !reflect.DeepEqual(got, []string{"clean", "loose"}) {
			t.Fatalf("confirmation: %v", got)
		}
		state.ActionError = new("Earlier failure")
		started := awWorktrees(&state, contracts.WorktreeCommandWorktreeDeleteSafe{Roots: awRoots("clean", "dirty", "busy", "loose", "clean")})
		deleting := awIDs(started.Effects, func(effect WorkspaceEffect) string { return effect.(WorkspaceEffectDeleteWorktree).WorktreeID })
		if !reflect.DeepEqual(deleting, []string{"clean", "loose"}) || !reflect.DeepEqual(started.State.DeletingWorktrees, awRoots("clean", "loose")) {
			t.Fatalf("started: %v", deleting)
		}
		if started.State.WorktreeSettings.ConfirmingSafe || len(awPage(&started.State).Safe) != 0 || started.State.ActionError != nil {
			t.Fatalf("after start: %#v", started.State.WorktreeSettings)
		}
		first := awWorktrees(&started.State, WorktreeEventWorktreeDeleted{WorktreeID: "clean", Root: awRoots("clean")[0]}).State
		if awText(first.WorktreeManagementNotice) != "Deleted 1 worktree." || first.Threads[0].WorktreeID != nil {
			t.Fatalf("first: %s", awText(first.WorktreeManagementNotice))
		}
		failed := awWorktrees(&first, WorktreeEventWorktreesFailed{Root: new(awRoots("loose")[0]), Message: "Locked"}).State
		if failed.WorktreeSettings.Bulk != nil || awText(failed.WorktreeManagementError) != "Locked" || len(failed.DeletingWorktrees) != 0 {
			t.Fatalf("failed: %#v", failed.WorktreeSettings.Bulk)
		}

		nothing := awBulkFixture()
		nothing.ActionError = new("Earlier failure")
		none := awWorktrees(nothing, contracts.WorktreeCommandWorktreeDeleteSafe{Roots: awRoots("dirty")})
		if len(none.Effects) != 0 || none.State.WorktreeSettings.Bulk != nil || awText(none.State.ActionError) != "Earlier failure" {
			t.Fatalf("a bulk that deletes nothing changed something")
		}
	})
}

func TestWorktreeBulkDeleteKeepsRecoveryAndJoinsARunningBulk(t *testing.T) {
	awPorted(t, func() {
		roots := awRoots("clean", "loose")
		first := awWorktrees(awBulkFixture(), contracts.WorktreeCommandWorktreeDeleteSafe{Roots: roots[:1]}).State
		joined := awWorktrees(&first, contracts.WorktreeCommandWorktreeDeleteSafe{Roots: roots[1:]}).State
		if !reflect.DeepEqual(joined.WorktreeSettings.Bulk.Pending, roots) {
			t.Fatalf("join: %v", joined.WorktreeSettings.Bulk.Pending)
		}
		failed := awWorktrees(&joined, WorktreeEventWorktreesFailed{Root: &roots[0], Message: "Locked"}).State
		closed := failed
		closed.WorktreeSettings = ManagementShownIn(WorktreesOf(&failed), false, false).View.WorktreeSettings
		if !reflect.DeepEqual(closed.WorktreeSettings.Bulk.Pending, roots[1:]) {
			t.Fatalf("closing settings dropped the bulk")
		}
		snapshot := contracts.WorktreeSnapshotResult{Commit: new("abcdef1234"), ShortCommit: new("abcdef1"), Ref: new("refs/aicodingtool/loose")}
		done := awWorktrees(&closed, WorktreeEventWorktreeDeleted{WorktreeID: "loose", Root: roots[1], Snapshot: snapshot}).State
		if awText(done.WorktreeManagementNotice) != "Deleted 1 worktree. Recover repo-loose with git show refs/aicodingtool/loose." || awText(done.WorktreeManagementError) != "Locked" || done.WorktreeSettings.Bulk != nil {
			t.Fatalf("done: %s", awText(done.WorktreeManagementNotice))
		}

		both := awWorktrees(awBulkFixture(), contracts.WorktreeCommandWorktreeDeleteSafe{Roots: roots}).State
		for index, id := range []string{"clean", "loose"} {
			both = awWorktrees(&both, WorktreeEventWorktreeDeleted{WorktreeID: id, Root: roots[index]}).State
		}
		if awText(both.WorktreeManagementNotice) != "Deleted 2 worktrees." || both.WorktreeSettings.Bulk != nil {
			t.Fatalf("both: %s", awText(both.WorktreeManagementNotice))
		}
	})
}

func TestWorktreeOpenThreadSelectsItWithoutRestoringIt(t *testing.T) {
	awPorted(t, func() {
		state := awSettingsFixture()
		opened := awWorktrees(state, contracts.WorktreeCommandWorktreeOpenThread{TaskID: "archived"}).State
		thread := threadByID(opened.Threads, "archived")
		if opened.SettingsOpen || awText(opened.CurrentID) != "archived" || thread.ArchivedAt == nil || awText(thread.WorktreeID) != "wt1" {
			t.Fatalf("opened: %v", opened.SettingsOpen)
		}
		if unknown := awWorktrees(state, contracts.WorktreeCommandWorktreeOpenThread{TaskID: "unknown"}); unknown.State.Threads[0] != state.Threads[0] || unknown.State.SettingsOpen != state.SettingsOpen {
			t.Fatalf("an unknown thread changed something")
		}
	})
}
