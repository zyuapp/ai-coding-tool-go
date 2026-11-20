package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func viewThreads(threads ...*domain.Thread) func(*WorkspaceState) {
	return func(state *WorkspaceState) { state.Threads = threads }
}

func viewSelect(id string) WorkspaceInput { return contracts.TaskCommandTaskSelect{TaskID: id} }

func canGo(state *WorkspaceState, step int) bool {
	_, ok := ReachableVisit(state, step)
	return ok
}

func TestVisitingThreadsBuildsATrail(t *testing.T) {
	corePorted(t, func() {
		state := coreRun(coreWorkspace(viewThreads(coreTask("task-a"), coreTask("task-b"), coreTask("task-c"))), viewSelect("task-a"), viewSelect("task-b"), viewSelect("task-c"))
		if !slices.Equal(state.History, []string{"task-a", "task-b", "task-c"}) {
			t.Fatalf("history %v", state.History)
		}
		back := coreRun(state, contracts.ViewCommandViewGoBack{}, contracts.ViewCommandViewGoBack{})
		if deref(back.CurrentID) != "task-a" || !slices.Equal(back.History, state.History) || !canGo(back, 1) || canGo(back, -1) {
			t.Fatalf("back on %s %v", deref(back.CurrentID), back.History)
		}
		forward := coreRun(back, contracts.ViewCommandViewGoForward{})
		if deref(forward.CurrentID) != "task-b" || !canGo(forward, -1) {
			t.Fatal("forward")
		}
		if again := Reduce(back, contracts.ViewCommandViewGoBack{}); !sameState(&again.State, back) {
			t.Fatal("there is nowhere further back to go")
		}
	})
}

func TestVisitingAfterGoingBackDropsTheTrailAhead(t *testing.T) {
	corePorted(t, func() {
		walked := coreRun(coreWorkspace(viewThreads(coreTask("task-a"), coreTask("task-b"), coreTask("task-c"))), viewSelect("task-a"), viewSelect("task-b"), contracts.ViewCommandViewGoBack{}, viewSelect("task-c"))
		if !slices.Equal(walked.History, []string{"task-a", "task-c"}) || deref(walked.CurrentID) != "task-c" || canGo(walked, 1) {
			t.Fatalf("walked %v on %s", walked.History, deref(walked.CurrentID))
		}
	})
}

func TestBackAndForwardStepOverGoneOrArchivedThreads(t *testing.T) {
	corePorted(t, func() {
		visited := coreRun(coreWorkspace(viewThreads(coreTask("task-a"), coreTask("task-b"), coreTask("task-c"))), viewSelect("task-a"), viewSelect("task-b"), viewSelect("task-c"))
		emptied := *visited
		emptied.Threads = slices.DeleteFunc(slices.Clone(visited.Threads), func(thread *domain.Thread) bool { return thread.ID == "task-a" })
		emptied.HistoryIndex = 1
		if canGo(&emptied, -1) {
			t.Fatal("a thread that no longer exists is somewhere to go")
		}
		duplicate := coreWorkspace(func(state *WorkspaceState) {
			archived := func(thread *domain.Thread) { thread.ArchivedAt = new(0.0) }
			state.Threads = []*domain.Thread{coreTask("archived", archived), coreTask("shared", archived), coreTask("shared")}
			state.History = []string{"archived", "shared"}
			state.HistoryIndex = -1
		})
		if got := deref(Reduce(duplicate, contracts.ViewCommandViewGoForward{}).State.CurrentID); got != "shared" {
			t.Fatalf("forward landed on %q", got)
		}
		archived := coreRun(visited, contracts.TaskCommandTaskArchive{TaskID: "task-b"})
		back := coreRun(archived, contracts.ViewCommandViewGoBack{})
		if deref(back.CurrentID) != "task-a" || deref(coreRun(back, contracts.ViewCommandViewGoForward{}).CurrentID) != "task-c" {
			t.Fatal("the archived thread in between is not skipped")
		}
	})
}

func viewSearching(t *testing.T, panel string, target func(owner string) domain.FindTarget) (*WorkspaceState, domain.FindTarget) {
	t.Helper()
	state := coreRun(coreWorkspace(viewThreads(coreTask("task-a")), func(state *WorkspaceState) {
		state.CurrentID = new("task-a")
		state.LastFolder = new("/repo")
	}), contracts.ViewCommandViewOpenDockPanel{Panel: panel})
	searched := target(DockOwner(state, nil))
	return coreRun(state, contracts.ViewCommandViewFindOpen{Target: searched}, contracts.ViewCommandViewFindQuery{Query: "needle"}), searched
}

func reviewTarget(owner string) domain.FindTarget { return domain.FindTargetReview{Owner: owner} }

func panelTarget(owner string) domain.FindTarget {
	return domain.FindTargetPanel{Owner: owner, Panel: "agents"}
}

func TestReviewCountsItsOwnMatchesAndTheReducerKeepsThePlace(t *testing.T) {
	corePorted(t, func() {
		state, target := viewSearching(t, DiffPanel, reviewTarget)
		if effects := Reduce(state, contracts.ViewCommandViewFindQuery{Query: "needle"}).Effects; len(effects) != 0 {
			t.Fatal("something outside is asked to search a review")
		}
		counted := coreRun(state, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 3}})
		stepped := coreRun(counted, contracts.ViewCommandViewFindStep{Delta: 1}, contracts.ViewCommandViewFindStep{Delta: 1})
		if stepped.Find.Index != 2 || coreRun(stepped, contracts.ViewCommandViewFindStep{Delta: 1}).Find.Index != 0 {
			t.Fatal("stepping")
		}
	})
}

func TestReviewStillReadingMovesTheUserWithTheirMatch(t *testing.T) {
	corePorted(t, func() {
		state, target := viewSearching(t, DiffPanel, reviewTarget)
		counting := coreRun(state, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 2, Counting: new(true)}})
		stepped := coreRun(counting, contracts.ViewCommandViewFindStep{Delta: 1})
		if stepped.Find.Index != 1 {
			t.Fatal("step")
		}
		landed := coreRun(stepped, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 9, Index: new(4.0)}})
		if landed.Find.Index != 4 {
			t.Fatal("the match being read does not keep the user")
		}
		if quiet := Reduce(landed, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 9, Index: new(4.0)}}); !sameState(&quiet.State, landed) {
			t.Fatal("a report that says nothing new costs something")
		}
		if other := Reduce(state, contracts.FindResultsEvent{Target: domain.FindTargetReview{Owner: "somebody-else"}, Results: domain.FindResults{Matches: 7}}); !sameState(&other.State, state) {
			t.Fatal("a report from elsewhere landed")
		}
		closed := coreRun(state, contracts.ViewCommandViewCloseDockPanel{Panel: DiffPanel})
		if closed.Find != nil || closed.FindResults != nil {
			t.Fatal("closing the review keeps its find bar")
		}
	})
}

func TestPanelReportsWhatItDrewAndTheReducerMovesThroughIt(t *testing.T) {
	corePorted(t, func() {
		state, target := viewSearching(t, "agents", panelTarget)
		if effects := Reduce(state, contracts.ViewCommandViewFindQuery{Query: "needle"}).Effects; len(effects) != 0 {
			t.Fatal("something outside is asked to search a panel")
		}
		counted := coreRun(state, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 4}})
		stepped := coreRun(counted, contracts.ViewCommandViewFindStep{Delta: 1})
		if stepped.Find.Index != 1 {
			t.Fatal("step")
		}
		if same := Reduce(stepped, contracts.FindResultsEvent{Target: target, Results: domain.FindResults{Matches: 4}}); !sameState(&same.State, stepped) {
			t.Fatal("redrawing the same count fights the stepping")
		}
		if wrapped := coreRun(stepped, contracts.ViewCommandViewFindStep{Delta: -1}, contracts.ViewCommandViewFindStep{Delta: -1}); wrapped.Find.Index != 3 {
			t.Fatalf("wrapped to %v", wrapped.Find.Index)
		}
		if coreRun(counted, contracts.ViewCommandViewFindQuery{Query: "other"}).FindResults != nil {
			t.Fatal("a new query keeps the count")
		}
		if coreRun(counted, contracts.ViewCommandViewFindOpen{Target: domain.FindTargetThread{TaskID: new("task-a")}}).FindResults != nil {
			t.Fatal("a new target keeps the count")
		}
		coreEqualJSON(t, coreRun(counted, contracts.ViewCommandViewFindOpen{Target: target}).FindResults, `{"matches":4}`, "reopened count")
		closed := coreRun(state, contracts.ViewCommandViewCloseDockPanel{Panel: "agents"})
		if closed.Find != nil || closed.FindResults != nil {
			t.Fatal("closing the panel keeps its find bar")
		}
	})
}

func TestComingBackToTheWindowReadsGitAndMissingEngines(t *testing.T) {
	corePorted(t, func() {
		project := &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}
		state := coreWorkspace(func(state *WorkspaceState) {
			state.Projects = []*domain.Project{project}
			state.Threads = []*domain.Thread{coreTask("task-a", func(thread *domain.Thread) { thread.ProjectID = &project.ID })}
			state.CurrentID = new("task-a")
		})
		environment := `{"type":"refresh-environment","workspaceId":"workspace-a","taskId":"task-a","followUp":true}`
		away := *state
		away.Focused = false
		coreEqualJSON(t, Reduce(&away, contracts.ViewCommandViewSetFocused{Focused: true}).Effects, "["+environment+"]", "back")
		coreEqualJSON(t, Reduce(state, contracts.ViewCommandViewSetFocused{Focused: false}).Effects, `[]`, "away")

		ready := away
		ready.EngineStatus = &domain.EngineStatus{Claude: &domain.EngineReadiness{Access: domain.EngineAccessReady}}
		coreEqualJSON(t, Reduce(&ready, contracts.ViewCommandViewSetFocused{Focused: true}).Effects, "["+environment+"]", "ready")
		broken := away
		broken.EngineStatus = &domain.EngineStatus{Claude: &domain.EngineReadiness{Access: domain.EngineAccessMissing, Fix: new("curl -fsSL https://claude.ai/install.sh | bash")}}
		asked := Reduce(&broken, contracts.ViewCommandViewSetFocused{Focused: true})
		coreEqualJSON(t, asked.Effects, "["+environment+`,{"type":"engine.read","refresh":true}]`, "asked")
		if !asked.State.EngineChecking {
			t.Fatal("not checking")
		}
		again := asked.State
		again.Focused = false
		coreEqualJSON(t, Reduce(&again, contracts.ViewCommandViewSetFocused{Focused: true}).Effects, "["+environment+"]", "asked twice")
	})
}

func TestJumpPanel(t *testing.T) {
	corePorted(t, func() {
		menu := coreWorkspace(func(state *WorkspaceState) { state.OpenMenu = new("thread:menu") })
		if Reduce(menu, contracts.ViewCommandViewJumpOpen{}).State.OpenMenu != nil {
			t.Fatal("the jump panel leaves the menu open")
		}
		chosen := coreRun(coreWorkspace(viewThreads(coreTask("task-a"), coreTask("task-b"))), contracts.ViewCommandViewJumpOpen{}, contracts.ViewCommandViewJumpChoose{TaskID: "task-b"})
		if deref(chosen.CurrentID) != "task-b" || chosen.Jump != nil || !slices.Equal(chosen.History, []string{"task-b"}) {
			t.Fatal("choosing a row")
		}
		setting := coreRun(coreWorkspace(viewThreads(coreTask("task-a"))), contracts.ViewCommandViewJumpOpen{}, contracts.ViewCommandViewJumpChooseSetting{Section: domain.SettingsSectionAppearance, SettingID: new("appearance.ui-font")})
		if setting.Jump != nil || !setting.SettingsOpen || deref(setting.SettingsSection) != domain.SettingsSectionAppearance {
			t.Fatal("a settings row")
		}
		coreEqualJSON(t, setting.SettingsFocus, `{"id":"appearance.ui-font","visit":1}`, "focus")
		page := coreRun(setting, contracts.ViewCommandViewJumpChooseSetting{Section: domain.SettingsSectionBrowser})
		if deref(page.SettingsSection) != domain.SettingsSectionBrowser || page.SettingsFocus != nil {
			t.Fatal("a page's own row")
		}
		if coreRun(page, contracts.ViewCommandViewSetSettingsOpen{Open: false}).SettingsFocus != nil {
			t.Fatal("closing keeps the focus")
		}
		land := contracts.ViewCommandViewSetSettingsOpen{Open: true, Section: new(domain.SettingsSectionAppearance), SettingID: new("appearance.ui-font")}
		once := coreRun(coreWorkspace(viewThreads(coreTask("task-a")), func(state *WorkspaceState) { state.ComputerUseSetup = true }), land)
		coreEqualJSON(t, once.SettingsFocus, `{"id":"appearance.ui-font","visit":1}`, "once")
		if once.ComputerUseSetup {
			t.Fatal("naming a page keeps computer-use setup")
		}
		coreEqualJSON(t, coreRun(once, land).SettingsFocus, `{"id":"appearance.ui-font","visit":2}`, "twice")

		jumpKey := contracts.ViewCommandViewShortcut{Action: "thread.jump", Surface: domain.ShortcutSurfaceAny}
		opened := coreRun(coreWorkspace(viewThreads(coreTask("task-a")), func(state *WorkspaceState) { state.SettingsOpen = true }), jumpKey)
		if opened.Jump == nil || opened.SettingsOpen {
			t.Fatal("settings do not give way to the jump panel")
		}
		if coreRun(opened, jumpKey).Jump != nil {
			t.Fatal("the jump keystroke does not close the panel")
		}
		walked := coreRun(coreWorkspace(viewThreads(coreTask("task-a", func(thread *domain.Thread) { thread.UpdatedAt = 2 }), coreTask("task-b"))), contracts.ViewCommandViewJumpOpen{}, contracts.ViewCommandViewJumpStep{Delta: 1})
		if walked.Jump.Index != 1 || coreRun(walked, contracts.ViewCommandViewJumpStep{Delta: 1}).Jump.Index != 0 || coreRun(walked, contracts.ViewCommandViewJumpQuery{Query: "task"}).Jump.Index != 0 {
			t.Fatal("walking the rows")
		}
	})
}

var zenKey = contracts.ViewCommandViewShortcut{Action: "zen.toggle", Surface: domain.ShortcutSurfaceAny}

func talked(id string) *domain.Thread {
	return coreTask(id, func(thread *domain.Thread) {
		thread.Messages = []*domain.ConversationMessage{{ID: id + "-m1", Kind: domain.ConversationMessageKindUser, Text: "Have a look", At: 5}}
	})
}

func TestZenKeystroke(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(viewThreads(talked("task-a")), func(state *WorkspaceState) {
			state.CurrentID = new("task-a")
			state.KeyboardTab = new("terminal-1")
		})
		entered := coreRun(state, zenKey)
		if !entered.Zen || entered.KeyboardTab != nil || entered.ComposerFocus != state.ComposerFocus+1 || entered.SidebarOpen != state.SidebarOpen {
			t.Fatal("entering zen")
		}
		if coreRun(entered, zenKey).Zen {
			t.Fatal("the same keystroke does not leave zen")
		}
		if coreRun(entered, contracts.ViewCommandViewEscape{}).Zen != true {
			t.Fatal("escape leaves zen")
		}
		draft := coreWorkspace(viewThreads(talked("task-a")))
		silent := coreWorkspace(viewThreads(coreTask("task-a")), func(state *WorkspaceState) { state.CurrentID = new("task-a") })
		if coreRun(draft, zenKey).Zen || coreRun(silent, zenKey).Zen || coreRun(silent, contracts.ViewCommandViewSetZen{Zen: true}).Zen {
			t.Fatal("zen on a draft or a silent thread")
		}
	})
}

func TestZenEndsOnAnotherThreadAndBringsBackWhatIsAskedFor(t *testing.T) {
	corePorted(t, func() {
		entered := coreRun(coreWorkspace(viewThreads(talked("task-a"), talked("task-b")), func(state *WorkspaceState) { state.CurrentID = new("task-a") }), zenKey)
		moved := coreRun(entered, viewSelect("task-b"))
		if moved.Zen || coreRun(moved, viewSelect("task-a")).Zen || coreRun(entered, contracts.ViewCommandViewShortcut{Action: "thread.new", Surface: domain.ShortcutSurfaceAny}).Zen {
			t.Fatal("zen kept across threads")
		}
		sidebar := coreRun(entered, contracts.ViewCommandViewShortcut{Action: "sidebar.toggle", Surface: domain.ShortcutSurfaceAny})
		if sidebar.Zen || !sidebar.SidebarOpen {
			t.Fatal("sidebar in zen")
		}
		dock := coreRun(entered, contracts.ViewCommandViewShortcut{Action: "dock.toggle", Surface: domain.ShortcutSurfaceAny})
		if dock.Zen || DockOwner(dock, nil) != "task-a" || !DockFor(dock, "task-a").Open {
			t.Fatal("dock in zen")
		}
		if coreRun(entered, contracts.ViewCommandViewSetDockOpen{Open: true}).Zen {
			t.Fatal("opening the dock keeps zen")
		}
		if !coreRun(entered, contracts.ViewCommandViewSetPrompt{Prompt: "Keep going"}).Zen {
			t.Fatal("typing leaves zen")
		}
		shell := &domain.TerminalSession{ID: "terminal-1", Title: "zsh", Cwd: "/repo", TaskID: new("task-a"), Status: domain.TerminalSessionStatusRunning}
		docked := WithDock(coreWorkspace(viewThreads(talked("task-a")), func(state *WorkspaceState) { state.CurrentID = new("task-a") }), "task-a", func(dock *ThreadDock) {
			dock.Open = true
			dock.Tab = shell.ID
			dock.Terminals = []*domain.TerminalSession{shell}
			dock.TerminalID = &shell.ID
		})
		typing := coreRun(docked, contracts.ViewCommandViewDockKeys{Tab: &shell.ID})
		if deref(typing.KeyboardTab) != shell.ID || coreRun(typing, contracts.ViewCommandViewSetZen{Zen: true}).KeyboardTab != nil {
			t.Fatal("zen does not take the keyboard back from a shell")
		}
	})
}
