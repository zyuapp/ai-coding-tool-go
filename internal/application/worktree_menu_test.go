package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func awMenuFixture() *WorkspaceState {
	first, second := awHeldWorktree("wt1"), awHeldWorktree("wt2")
	first.Name, second.Name = new("Login redesign"), new("Payment fix")
	return awProjected(func(state *WorkspaceState) {
		state.CurrentID = new("review")
		state.Worktrees = []*domain.Worktree{first, second}
		for _, item := range state.Worktrees {
			state.ManagedWorktrees = append(state.ManagedWorktrees, &domain.ManagedWorktree{ID: item.ID, Root: item.Root, Repository: new("/repo"), Branch: new("feature/" + item.ID), Status: awStatus(new(2.0), nil)})
		}
		state.OpenMenu = new(WorktreeMenu)
		titled := func(title string) func(*domain.Thread) { return func(thread *domain.Thread) { thread.Title = title } }
		state.Threads = []*domain.Thread{
			awTask("review", titled("Review login"), awInProject("project-a"), awInWorktree("wt1")),
			awTask("build", titled("Build login"), awInProject("project-a"), awInWorktree("wt1")),
			awTask("local", awInProject("project-a")),
			awTask("payment", titled("Fix invoice rounding"), awInProject("project-a"), awInWorktree("wt2")),
		}
	})
}

func awMenu(state *WorkspaceState) *WorktreeMenuView {
	visible := []*domain.Thread{}
	for _, thread := range state.Threads {
		if thread.ArchivedAt == nil {
			visible = append(visible, thread)
		}
	}
	return WorktreeMenuWorktreeMenuView(state, threadByID(state.Threads, awText(state.CurrentID)), visible, nil)
}

func TestWorktreeMenuMembershipAndSearch(t *testing.T) {
	awPorted(t, func() {
		state := awMenuFixture()
		view := awMenu(state)
		if view.Count != 2 || !reflect.DeepEqual(awIDs(view.Threads, func(choice *WorktreeMenuChoice) string { return choice.ID }), []string{"review", "build"}) {
			t.Fatalf("threads: %#v", view.Threads)
		}
		if view.Threads[0].Detail != "Current thread" || view.Threads[1].Detail != "Idle" {
			t.Fatalf("details: %s / %s", view.Threads[0].Detail, view.Threads[1].Detail)
		}
		filtered := awWorktrees(state, contracts.WorktreeCommandWorktreeMenuSearch{List: "threads", Query: "BUILD"}).State
		if view := awMenu(&filtered); view.Count != 2 || len(view.Threads) != 1 || view.Threads[0].ID != "build" {
			t.Fatalf("search: %#v", view.Threads)
		}
		destinations := awIDs(view.Destinations, func(choice *WorktreeMenuChoice) string { return choice.ID })
		if !reflect.DeepEqual(destinations, []string{"wt2"}) || view.Destinations[0].Detail != "feature/wt2" || *view.Destinations[0].Disabled {
			t.Fatalf("destinations: %v", destinations)
		}
		byFolder := awWorktrees(state, contracts.WorktreeCommandWorktreeMenuSearch{List: "destinations", Query: "repo-wt2"}).State
		if view := awMenu(&byFolder); len(view.Destinations) != 1 {
			t.Fatalf("the folder finds the checkout")
		}
		if !view.CanMove || !view.CanDelete || awText(view.DeleteRoot) != "/worktrees/repo-wt1" {
			t.Fatalf("view: %#v", view)
		}
		closed := *state
		closed.OpenMenu = nil
		if view := awMenu(&closed); len(view.Threads) != 0 || len(view.Destinations) != 0 {
			t.Fatalf("a closed menu lists nothing")
		}
	})
}

func TestWorktreeMenuOpenListsCheckoutsOnlyWhenItMust(t *testing.T) {
	state := awMenuFixture()
	opened := awWorktrees(state, contracts.WorktreeCommandWorktreeMenuOpen{List: "destinations"})
	if len(opened.Effects) != 1 || !opened.State.WorktreeManagementLoading {
		t.Fatalf("opening destinations lists the checkouts: %#v", opened.Effects)
	}
	if threads := awWorktrees(state, contracts.WorktreeCommandWorktreeMenuOpen{List: "threads"}); len(threads.Effects) != 0 {
		t.Fatalf("the thread list asks for nothing")
	}
	if loading := awWorktrees(&opened.State, contracts.WorktreeCommandWorktreeMenuOpen{List: "destinations"}); len(loading.Effects) != 0 {
		t.Fatalf("a list already loading is not asked for again")
	}
}

func TestWorktreeChoicesMostRecentFirstWithoutTheOneInUse(t *testing.T) {
	state := awMenuFixture()
	state.Worktrees[1].LastUsedAt = 9
	unavailable := awHeldWorktree("unavailable")
	state.Worktrees = append(state.Worktrees, unavailable)
	state.ManagedWorktrees = append(state.ManagedWorktrees, &domain.ManagedWorktree{ID: "unavailable", Root: unavailable.Root})
	state.Environments = map[string]contracts.ChangedFilesResult{"worktree-wt2": contracts.ChangedFilesResultAvailable{Files: []string{}, Branch: new("live")}}
	choices := WorktreeChoices(state, "project-a", nil)
	if len(choices) != 2 || choices[0].ID != "wt2" || awText(choices[0].Branch) != "live" || choices[0].Name != "Payment fix" || awText(choices[1].Branch) != "feature/wt1" {
		t.Fatalf("choices: %#v", choices)
	}
	if except := WorktreeChoices(state, "project-a", new("wt2")); len(except) != 1 || except[0].ID != "wt1" {
		t.Fatalf("except: %#v", except)
	}
}
