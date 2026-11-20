package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func desktopInProject(edits ...func(*domain.Thread)) *WorkspaceState {
	return coreWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "project-1", Root: "/repo"}}
		state.Threads = []*domain.Thread{coreTask("task-1", append([]func(*domain.Thread){func(thread *domain.Thread) { thread.ProjectID = new("project-1") }}, edits...)...)}
		state.CurrentID = new("task-1")
	})
}

func desktopCheckout(id string, lastUsedAt float64) *domain.Worktree {
	return &domain.Worktree{ID: id, ProjectID: "project-1", Root: "/worktrees/repo-" + id, WorkspaceID: "ws-" + id, BaseCommit: id, CreatedAt: 1, LastUsedAt: lastUsedAt}
}

func desktopInWorktree(state *WorkspaceState, worktrees ...*domain.Worktree) *WorkspaceState {
	copied := *state
	thread := *state.Threads[0]
	thread.WorktreeID = new("w1")
	copied.Threads = []*domain.Thread{&thread}
	copied.Worktrees = worktrees
	return &copied
}

func TestClosingTheWindowReleasesTerminalsWhileBrowserPagesReopenOnMount(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(state *WorkspaceState) {
			state.CurrentID = new("task-1")
			state.Threads = []*domain.Thread{coreTask("task-1")}
			own := *EmptyDock
			own.Open, own.Tab, own.BrowserTabID = true, "page", new("page")
			own.BrowserTabs = []*domain.BrowserTab{{ID: "page", URL: "https://example.com/", Title: "Page", Loading: true, CanGoBack: true, CanGoForward: true, Error: new("stale")}}
			own.Terminals = []*domain.TerminalSession{{ID: "shell", Cwd: "/repo", Title: "Shell", TaskID: new("task-1"), Status: domain.TerminalSessionStatusRunning}}
			own.TerminalID = new("shell")
			draft := *EmptyDock
			draft.Open, draft.Tab = true, "draft-shell"
			draft.Terminals = []*domain.TerminalSession{{ID: "draft-shell", Cwd: "/repo", Title: "Shell", Status: domain.TerminalSessionStatusRunning}}
			draft.TerminalID = new("draft-shell")
			state.Docks = map[string]*ThreadDock{"task-1": &own, "draft": &draft}
		})
		closed := Reduce(state, WorkspaceEventViewClosed{})
		if closed.State.Focused || len(closed.State.Docks["task-1"].Terminals) != 0 || closed.State.Docks["draft"].Tab != "home" || closed.State.Docks["draft"].TerminalID != nil {
			t.Fatal("closing the window")
		}
		coreEqualJSON(t, DockFor(&closed.State, DockOwner(&closed.State, nil)).BrowserTabs, `[{"id":"page","url":"https://example.com/","title":"Page","loading":false,"canGoBack":false,"canGoForward":false}]`, "pages")
		coreEqualJSON(t, closed.Effects, `[]`, "effects")
		mounted := Reduce(&closed.State, contracts.ViewCommandViewMounted{})
		opens := slices.ContainsFunc(mounted.Effects, func(effect WorkspaceEffect) bool {
			open, ok := effect.(WorkspaceEffectBrowserOpen)
			return ok && open.TabID == "page"
		})
		shows := slices.ContainsFunc(mounted.Effects, func(effect WorkspaceEffect) bool {
			show, ok := effect.(WorkspaceEffectBrowserShow)
			return ok && deref(show.TabID) == "page"
		})
		if !opens || !shows {
			t.Fatalf("mount effects %s", coreJSON(t, mounted.Effects))
		}
	})
}

func TestFileOpensAgainstTheThreadsCheckout(t *testing.T) {
	corePorted(t, func() {
		state := desktopInProject()
		coreEqualJSON(t, Reduce(state, contracts.FileCommandFileOpen{Path: "src/App.tsx", Line: new(42.0)}).Effects, `[{"type":"file.open","roots":["/repo"],"path":"src/App.tsx","line":42}]`, "project")
		inWorktree := desktopInWorktree(state, desktopCheckout("w1", 1), desktopCheckout("w2", 9))
		coreEqualJSON(t, Reduce(inWorktree, contracts.FileCommandFileOpen{Path: "src/App.tsx"}).Effects, `[{"type":"file.open","roots":["/worktrees/repo-w1","/repo","/worktrees/repo-w2"],"path":"src/App.tsx","line":null}]`, "worktree")
		refused := Reduce(EmptyWorkspaceState(nil), contracts.FileCommandFileOpen{Path: "src/App.tsx"})
		if len(refused.Effects) != 0 || deref(refused.State.ActionError) != WorkspaceErrors["fileFolder"] {
			t.Fatal("refusal")
		}
		coreEqualJSON(t, Reduce(EmptyWorkspaceState(nil), contracts.FileCommandFileOpen{Path: "/etc/hosts"}).Effects, `[{"type":"file.open","roots":[],"path":"/etc/hosts","line":null}]`, "absolute")
	})
}

func TestOpeningInAnotherApplicationHandsOverTheCheckout(t *testing.T) {
	corePorted(t, func() {
		state := desktopInProject()
		state.OpenMenu = new("workspace:open-in")
		local := Reduce(state, contracts.ExternalAppCommandAppOpenFolder{AppID: "cursor"})
		coreEqualJSON(t, local.Effects, `[{"type":"app.open-folder","root":"/repo","appId":"cursor"}]`, "local")
		if local.State.OpenMenu != nil {
			t.Fatal("choosing an application leaves the list open")
		}
		inWorktree := desktopInWorktree(state, desktopCheckout("w1", 1))
		coreEqualJSON(t, Reduce(inWorktree, contracts.ExternalAppCommandAppOpenFolder{AppID: "terminal"}).Effects, `[{"type":"app.open-folder","root":"/worktrees/repo-w1","appId":"terminal"}]`, "worktree")
		refused := Reduce(EmptyWorkspaceState(nil), contracts.ExternalAppCommandAppOpenFolder{AppID: "cursor"})
		if len(refused.Effects) != 0 || deref(refused.State.ActionError) != WorkspaceErrors["appFolder"] {
			t.Fatal("refusal")
		}
	})
}

func TestHelpActionsAskTheDesktopWithoutChangingState(t *testing.T) {
	corePorted(t, func() {
		state := EmptyWorkspaceState(nil)
		updates := Reduce(state, contracts.ExternalAppCommandAppCheckForUpdates{})
		licenses := Reduce(state, contracts.ExternalAppCommandAppOpenSourceLicenses{})
		if !sameState(&updates.State, state) || !sameState(&licenses.State, state) {
			t.Fatal("state changed")
		}
		coreEqualJSON(t, updates.Effects, `[{"type":"app.check-for-updates"}]`, "updates")
		coreEqualJSON(t, licenses.Effects, `[{"type":"app.open-source-licenses"}]`, "licenses")
	})
}

func TestInstalledAppsAreKeptWhileTheNextReadRuns(t *testing.T) {
	corePorted(t, func() {
		apps := []*contracts.InstalledApp{{ExternalApp: domain.ExternalApp{ID: "cursor", Label: "Cursor", Kind: "editor"}}}
		asked := Reduce(EmptyWorkspaceState(nil), contracts.ExternalAppCommandAppList{})
		coreEqualJSON(t, asked.Effects, `[{"type":"app.list"}]`, "asked")
		if asked.State.InstalledApps != nil {
			t.Fatal("apps before listing")
		}
		listed := Reduce(&asked.State, DesktopEvent{Apps: apps})
		coreEqualJSON(t, listed.State.InstalledApps, apps, "listed")
		coreEqualJSON(t, Reduce(&listed.State, contracts.ExternalAppCommandAppList{}).State.InstalledApps, apps, "kept")
	})
}
