package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func awProjectWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	return awWorkspace(append([]func(*WorkspaceState){func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "project-1", Root: "/work/ai-coding-tool", WorkspaceID: new("workspace-1")}}
	}}, edits...)...)
}

func TestProjectEditNamesAFolderAndNothingElse(t *testing.T) {
	named := awProjects(awProjectWorkspace(), contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("  App  ")})
	if len(named.Effects) != 0 || awText(named.State.Projects[0].Name) != "App" || named.State.Projects[0].Root != "/work/ai-coding-tool" {
		t.Fatalf("named: %#v", named.State.Projects[0])
	}
	cleared := awProjects(&named.State, contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("")})
	if cleared.State.Projects[0].Name != nil {
		t.Fatalf("a blank name gives the folder its own back")
	}
}

func TestProjectEditMovingWaitsForTheFolderToOpen(t *testing.T) {
	moving := awProjects(awProjectWorkspace(), contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("App"), Root: new(" /work/moved ")})
	register, ok := moving.Effects[0].(WorkspaceEffectRegisterProject)
	if !ok || register.Root != "/work/moved" || register.ProjectID != "project-1" {
		t.Fatalf("effects: %#v", moving.Effects)
	}
	if moving.State.Projects[0].Root != "/work/ai-coding-tool" || moving.State.Projects[0].Name != nil {
		t.Fatalf("nothing moves until the folder opens")
	}
	if edit := moving.State.ProjectEdit; edit == nil || !edit.Saving || edit.Error != nil {
		t.Fatalf("edit: %#v", edit)
	} else if name, ok := edit.Name.Get(); !ok || name != "App" {
		t.Fatalf("the name waits with the folder")
	}
	moved := awProjects(&moving.State, ProjectEventProjectRegistered{ProjectID: "project-1", Workspace: domain.WorkspaceRecord{ID: "workspace-2", Kind: "project", Root: "/work/moved"}})
	project := moved.State.Projects[0]
	if project.ID != "project-1" || project.Root != "/work/moved" || awText(project.WorkspaceID) != "workspace-2" || awText(project.Name) != "App" || moved.State.ProjectEdit != nil {
		t.Fatalf("moved: %#v", project)
	}

	failing := awProjects(awProjectWorkspace(), contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("App"), Root: new("/work/gone")})
	failed := awProjects(&failing.State, ProjectEventProjectRegisterFailed{ProjectID: "project-1", Message: "There is no folder at /work/gone."})
	if edit := failed.State.ProjectEdit; edit == nil || edit.Saving || awText(edit.Error) != "There is no folder at /work/gone." {
		t.Fatalf("failed: %#v", edit)
	}
	if failed.State.Projects[0].Name != nil || failed.State.Projects[0].Root != "/work/ai-coding-tool" {
		t.Fatalf("a failed move keeps the folder as it was")
	}
}

func TestProjectEditFollowsTheLastFolder(t *testing.T) {
	awPorted(t, func() {
		state := awProjectWorkspace(func(state *WorkspaceState) { state.LastFolder = new("/work/ai-coding-tool") })
		moved := awProjects(state, ProjectEventProjectRegistered{ProjectID: "project-1", Workspace: domain.WorkspaceRecord{ID: "workspace-2", Kind: "project", Root: "/work/moved"}})
		if awText(moved.State.LastFolder) != "/work/moved" {
			t.Fatalf("lastFolder: %s", awText(moved.State.LastFolder))
		}
	})
}

func TestProjectEditTheSameFolderAsksForNothing(t *testing.T) {
	same := awProjects(awProjectWorkspace(), contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("App"), Root: new("/work/ai-coding-tool/")})
	if len(same.Effects) != 0 || awText(same.State.Projects[0].Name) != "App" || same.State.ProjectEdit != nil {
		t.Fatalf("same: %#v", same.Effects)
	}
}

func TestProjectEditorTakesDownTheMenuAndSavingTheError(t *testing.T) {
	over := awProjectWorkspace(func(state *WorkspaceState) { state.OpenMenu, state.ActionError = new("project:menu"), new("Earlier.") })
	if editing := awProjects(over, contracts.ViewCommandViewEditProject{ProjectID: new("project-1")}); editing.State.OpenMenu != nil {
		t.Fatalf("the editor leaves the menu open")
	} else if !reflect.DeepEqual(editing.State.ProjectEdit, &ProjectEdit{ProjectID: "project-1"}) {
		t.Fatalf("edit: %#v", editing.State.ProjectEdit)
	}
	named := awProjects(over, contracts.ProjectCommandProjectEdit{ProjectID: "project-1", Name: jsonx.Some("App")})
	if named.State.OpenMenu != nil || named.State.ActionError != nil {
		t.Fatalf("naming leaves the menu or error up")
	}
	failed := awProjects(awProjectWorkspace(), ProjectEventProjectRegisterFailed{ProjectID: "project-1", Message: "There is no folder at /work/gone."})
	if awText(failed.State.ActionError) != "There is no folder at /work/gone." {
		t.Fatalf("a failure with no editor is shown in the window")
	}
	if result, ok := failed.Result.(contracts.WorkspaceCommandResultFalse); !ok || result.Message != "There is no folder at /work/gone." {
		t.Fatalf("result: %#v", failed.Result)
	}
}

func TestProjectRemoveRefusesWhileWorkIsThere(t *testing.T) {
	inProject := awTask("t1", awInProject("project-1"))
	running := awProjectWorkspace(func(state *WorkspaceState) {
		state.Threads = []*domain.Thread{inProject}
		state.ActiveRuns = map[string]ActiveRun{"t1": ActiveRunRunning{TaskID: "t1", RunID: "r1"}}
	})
	refused := awProjects(running, contracts.ProjectCommandProjectRemove{ProjectID: "project-1"})
	if awText(refused.State.ActionError) != RunningProjectError || refused.State.ActionErrorPage == nil || *refused.State.ActionErrorPage != domain.SettingsSectionProjects {
		t.Fatalf("running: %s", awText(refused.State.ActionError))
	}
	withWorktree := awProjectWorkspace(func(state *WorkspaceState) {
		state.Worktrees = []*domain.Worktree{{ID: "w1", ProjectID: "project-1", Root: "/w1", WorkspaceID: "ws-w1"}}
	})
	if refused := awProjects(withWorktree, contracts.ProjectCommandProjectRemove{ProjectID: "project-1"}); awText(refused.State.ActionError) != ProjectWorktreesError {
		t.Fatalf("worktrees: %s", awText(refused.State.ActionError))
	}
	awPorted(t, func() {
		editing := awProjects(awProjectWorkspace(), contracts.ViewCommandViewEditProject{ProjectID: new("project-1")})
		removed := awProjects(&editing.State, contracts.ProjectCommandProjectRemove{ProjectID: "project-1"})
		if removed.State.ProjectEdit != nil || len(removed.State.Projects) != 0 {
			t.Fatalf("the editor closes with its folder")
		}
	})
}

func TestOpenedInFindsAMovedProjectByItsFolder(t *testing.T) {
	state := awProjectWorkspace(func(state *WorkspaceState) {
		state.Projects[0].Root = "/work/moved"
	})
	edit, id := OpenedIn(ProjectsOf(state), domain.WorkspaceRecord{ID: "workspace-3", Kind: "project", Root: "/work/moved"}, false)
	if id != "project-1" || len(edit.View.Projects) != 1 || awText(edit.View.Projects[0].WorkspaceID) != "workspace-3" {
		t.Fatalf("opened: %s %#v", id, edit.View.Projects)
	}
	fresh, freshID := OpenedIn(ProjectsOf(state), domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}, true)
	if freshID != domain.LegacyProjectID("/app") || len(fresh.View.Projects) != 2 || fresh.View.Projects[0].ID != freshID || fresh.View.Projects[0].Unlisted == nil {
		t.Fatalf("new: %#v", fresh.View.Projects)
	}
	listed, _ := OpenedIn(fresh, domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}, true)
	if listed.View.Projects[0].Unlisted == nil {
		t.Fatalf("starting in an unlisted folder keeps it unlisted")
	}
	opened, _ := OpenedIn(fresh, domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}, false)
	if opened.View.Projects[0].Unlisted != nil {
		t.Fatalf("opening a folder outright lists it")
	}
	kept, _ := OpenedIn(ProjectsOf(state), domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/work/moved"}, true)
	if kept.View.Projects[0].Unlisted != nil {
		t.Fatalf("starting in a listed folder keeps it listed")
	}
}

func TestSettledUnlistedListsAStartedFolderAndLetsAnEmptyOneGo(t *testing.T) {
	state := awWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "app", Root: "/app", Unlisted: new(true)}}
		state.DraftProjectID, state.LastFolder = new("app"), new("/app")
	})
	if SettledUnlisted(state, state) != state {
		t.Fatalf("an unchanged state is kept")
	}
	started := *state
	started.Threads = []*domain.Thread{awTask("t1", awInProject("app"))}
	if settled := SettledUnlisted(state, &started); settled.Projects[0].Unlisted != nil {
		t.Fatalf("a thread lists its folder")
	}
	left := *state
	left.DraftProjectID = nil
	settled := SettledUnlisted(state, &left)
	if len(settled.Projects) != 0 || settled.LastFolder != nil {
		t.Fatalf("a folder left without a thread goes, and stops being the last: %#v", settled.Projects)
	}
}

func TestProjectOrder(t *testing.T) {
	project := func(id string, index *float64) *domain.Project {
		return &domain.Project{ID: id, Root: "/" + id, SortIndex: index}
	}
	ids := func(projects []*domain.Project) []string {
		out := []string{}
		for _, project := range projects {
			out = append(out, project.ID)
		}
		return out
	}
	if got := ids(OrderProjects([]*domain.Project{project("a", new(1.0)), project("b", new(0.0)), project("first", nil), project("second", nil)})); !reflect.DeepEqual(got, []string{"b", "a", "first", "second"}) {
		t.Fatalf("order: %v", got)
	}
	settled := []*domain.Project{project("a", new(0.0)), project("b", new(1.0))}
	if !jsonx.Same(BackfillProjectSortIndex(settled), settled) {
		t.Fatalf("settled projects are kept")
	}
	mixed := BackfillProjectSortIndex([]*domain.Project{project("a", new(0.0)), project("b", nil), project("c", nil)})
	for index, item := range mixed {
		if item.SortIndex == nil || *item.SortIndex != float64(index) {
			t.Fatalf("backfill: %s", item.ID)
		}
	}
	if NextProjectSortIndex(mixed) != -1 || NextProjectSortIndex(nil) != -1 || NextProjectSortIndex([]*domain.Project{project("a", new(-3.0)), project("b", new(4.0))}) != -4 {
		t.Fatalf("next sort index")
	}
}

func TestProjectSettingsRows(t *testing.T) {
	paired := func(id, name string, remote *WorkspaceState, status domain.ComputerStatus) *PairedComputer {
		return &PairedComputer{ComputerLink: domain.ComputerLink{ID: id, Name: name, Host: id + ".tail.ts.net", Status: status, PairedAt: 1}, State: remote}
	}
	summary := func(state *WorkspaceState) [][3]any {
		out := [][3]any{}
		for _, row := range ProjectSettingsRows(state) {
			var host any
			if row.Host != nil {
				host = row.Host.Name
			}
			out = append(out, [3]any{row.Project.ID, host, row.Threads})
		}
		return out
	}
	state := awWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "zeta", Root: "/work/zeta"}, {ID: "alpha", Root: "/work/alpha", Name: new("Alpha app")}, {ID: "draft", Root: "/work/draft", Unlisted: new(true)}}
		state.Threads = []*domain.Thread{awTask("listed", awInProject("zeta")), awTask("archived", awInProject("zeta"), func(thread *domain.Thread) { thread.ArchivedAt = new(5.0) }), awTask("loose")}
		state.Computers.Paired = []*PairedComputer{
			paired("studio", "Studio", awWorkspace(func(remote *WorkspaceState) {
				remote.Projects = []*domain.Project{{ID: "s-b", Root: "/studio/b"}, {ID: "s-a", Root: "/studio/a"}}
			}), domain.ComputerStatusOffline),
			paired("linux", "linux-box", awWorkspace(func(remote *WorkspaceState) { remote.Projects = []*domain.Project{{ID: "ml", Root: "/linux/ml"}} }), domain.ComputerStatusConnected),
			paired("blank", "Blank", nil, domain.ComputerStatusConnected),
		}
	})
	want := [][3]any{{"alpha", nil, 0.0}, {"zeta", nil, 1.0}, {"ml", "linux-box", 0.0}, {"s-a", "Studio", 0.0}, {"s-b", "Studio", 0.0}}
	if got := summary(state); !reflect.DeepEqual(got, want) {
		t.Fatalf("rows: %v", got)
	}
	if rows := ProjectSettingsRows(state); !rows[3].Host.Offline {
		t.Fatalf("an away computer's rows are marked away")
	}
}
