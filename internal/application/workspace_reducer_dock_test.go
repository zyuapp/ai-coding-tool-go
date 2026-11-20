package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestEveryThreadKeepsADockOfItsOwn(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("task-2")), func(s *WorkspaceState) {
			s.LastFolder = new("/repo")
			s.History = []string{"task-1"}
		})
		opened := panelsRun(t, state,
			`{"type":"view.open-dock-panel","panel":"agents"}`,
			`{"type":"browser.open","url":"https://one.example"}`,
			`{"type":"terminal.open"}`,
		)
		page := panelsDock(opened).BrowserTabs[0]
		shell := panelsDock(opened).Terminals[0]
		if !slices.Equal(panelsDock(opened).Panels, []string{"agents"}) || panelsDock(opened).Tab != shell.ID || !panelsDock(opened).Open {
			t.Fatalf("only a panel there is one of is a panel, and the shell is in front: %+v", panelsDock(opened))
		}

		switched, transition := panelsReduce(t, opened, `{"type":"task.select","taskId":"task-2"}`)
		if len(panelsDock(switched).Panels) != 0 || len(panelsDock(switched).BrowserTabs) != 0 || len(panelsDock(switched).Terminals) != 0 {
			t.Fatal("the thread the user lands on opens its own dock, which is empty")
		}
		effects := panelsEffects(t, transition, nil)
		if len(effects) < 2 || !panelsSubset(effects[len(effects)-2], panelsParse(t, `{"type":"browser.show","tabId":null}`)) || !panelsSubset(effects[len(effects)-1], panelsParse(t, `{"type":"focus-window"}`)) {
			t.Fatalf("the panel stops drawing the page, and the page does not keep the keys: %v", effects)
		}
		away := *opened
		away.Focused = false
		if _, transition := panelsReduce(t, &away, `{"type":"task.select","taskId":"task-2"}`); panelsHasEffect(t, transition, `{"type":"focus-window"}`) {
			t.Error("a window the user has left is not pulled back")
		}

		back, _ := panelsReduce(t, switched, `{"type":"task.select","taskId":"task-1"}`)
		if !slices.Equal(panelsDock(back).Panels, []string{"agents"}) || panelsDock(back).BrowserTabs[0].ID != page.ID || panelsDock(back).Terminals[0].ID != shell.ID || panelsDock(back).Tab != shell.ID {
			t.Fatal("the dock a thread was left in comes back as it was")
		}

		closed, _ := panelsReduce(t, back, `{"type":"terminal.close","terminalId":"`+shell.ID+`"}`)
		if panelsDock(closed).Tab != page.ID {
			t.Error("closing a tab hands the dock its neighbour")
		}
		empty, _ := panelsReduce(t, closed, `{"type":"browser.close-tab","tabId":"`+page.ID+`"}`)
		if panelsDock(empty).Tab != "agents" {
			t.Error("the panel this thread opened is what is left")
		}
		hidden, _ := panelsReduce(t, empty, `{"type":"view.set-dock-open","open":false}`)
		if panelsDock(hidden).Open {
			t.Error("the dock hides")
		}
	})
}

func panelsWorkflow(id string, status domain.WorkflowStatus) *domain.Workflow {
	return &domain.Workflow{ID: id, Name: "review-changes", Description: "Review changed files", Status: status, Phases: []*domain.WorkflowPhase{}, Agents: []*domain.WorkflowAgent{}, StartedAt: 1}
}

func TestTheWorkflowAPanelFollowsSurvivesAMoveAndBack(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("task-2")), func(s *WorkspaceState) {
			s.History = []string{"task-1"}
			s.Workflows = map[string][]*domain.Workflow{"task-1": {panelsWorkflow("wf-1", domain.WorkflowStatusRunning)}}
		})
		opened, _ := panelsReduce(t, state, `{"type":"view.open-workflow","workflowId":"wf-1"}`)
		if !slices.Equal(panelsDock(opened).Panels, []string{"workflow"}) || deref(panelsDock(opened).WorkflowID) != "wf-1" || DeriveView(opened).InspectedWorkflow.Name != "review-changes" {
			t.Fatal("the workflow panel opens on the workflow")
		}
		away, _ := panelsReduce(t, opened, `{"type":"task.select","taskId":"task-2"}`)
		if DeriveView(away).InspectedWorkflow != nil {
			t.Error("another thread's dock follows no workflow")
		}
		if !slices.Equal(panelsDock(away, "task-1").Panels, []string{"workflow"}) {
			t.Error("leaving does not close the panel behind you")
		}
		back, _ := panelsReduce(t, away, `{"type":"task.select","taskId":"task-1"}`)
		if DeriveView(back).InspectedWorkflow == nil || DeriveView(back).InspectedWorkflow.ID != "wf-1" {
			t.Error("the panel comes back on the workflow it was following")
		}
		closed, _ := panelsReduce(t, back, `{"type":"view.close-dock-panel","panel":"workflow"}`)
		if panelsDock(closed).WorkflowID != nil {
			t.Error("closing the panel lets the workflow go")
		}
	})
}

func TestAWorkflowPanelClosesOnceItsRecordIsGone(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("task-2")), func(s *WorkspaceState) {
			s.Workflows = map[string][]*domain.Workflow{"task-1": {panelsWorkflow("wf-1", domain.WorkflowStatusCompleted)}, "task-2": {panelsWorkflow("wf-2", domain.WorkflowStatusCompleted)}}
		})
		opened := panelsRun(t, state, `{"type":"view.open-workflow","workflowId":"wf-1"}`, `{"type":"view.open-dock-panel","panel":"agents"}`)
		if following, _ := panelsReduce(t, opened, `{"type":"view.open-workflow","workflowId":"wf-2"}`); deref(panelsDock(following, "task-1").WorkflowID) != "wf-1" {
			t.Error("a workflow in another thread is not this dock's to open")
		}
		cleared := *opened
		cleared.Workflows = map[string][]*domain.Workflow{"task-2": opened.Workflows["task-2"]}
		pruned, _ := panelsReduce(t, &cleared, `{"type":"view.set-dock-open","open":true}`)
		if !slices.Equal(panelsDock(pruned).Panels, []string{"agents"}) || panelsDock(pruned).WorkflowID != nil {
			t.Errorf("the panel goes with the record it was drawing: %+v", panelsDock(pruned))
		}
	})
}

func TestAViewTheUserOpensTakesTheKeyboardAndARunsPageDoesNot(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(
			panelsTask("task-1", func(thread *domain.Thread) {
				thread.Continuation = &domain.Continuation{Provider: "claude", Value: "main-session"}
			}),
			panelsTask("task-2", func(thread *domain.Thread) { thread.ExecutionPolicy = domain.ExecutionPolicyAutonomous }),
		))
		shell, _ := panelsReduce(t, state, `{"type":"terminal.open","cwd":"/tmp"}`)
		terminalID := panelsDock(shell).Terminals[0].ID
		panelsEqual(t, shell.DockFocus, `{"owner":"task-1","tab":"`+terminalID+`","count":1}`, "a shell takes the keys")
		if focus := DeriveView(shell).DockFocus; focus == nil || focus.Tab != terminalID {
			t.Error("the view draws the dock's focus")
		}
		chat, _ := panelsReduce(t, shell, `{"type":"side-chat.open","chatId":"chat-1"}`)
		panelsEqual(t, chat.DockFocus, `{"owner":"task-1","tab":"chat-1","count":2}`, "a side chat takes the keys")
		page, _ := panelsReduce(t, chat, `{"type":"browser.new-tab"}`)
		if page.DockFocus.Tab != panelsDock(page).BrowserTabs[0].ID {
			t.Error("a blank page is opened to type an address into")
		}
		stepped, _ := panelsReduce(t, page, `{"type":"view.select-dock-index","index":1}`)
		if stepped.DockFocus.Tab != terminalID {
			t.Error("the tab a keystroke names takes the keys with it")
		}
		byRun, _ := panelsReduce(t, stepped, `{"type":"browser.open","taskId":"task-2","url":"https://two.example"}`)
		if byRun.DockFocus != stepped.DockFocus || DeriveView(byRun).DockFocus.Tab != terminalID {
			t.Error("a run's own page never takes the keyboard")
		}
	})
}

func TestOnlyAPageHoldsTheKeysItself(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1")), func(s *WorkspaceState) { s.LastFolder = new("/repo") })
		focusWindow := panelsParse(t, `{"type":"focus-window"}`)
		shell, transition := panelsReduce(t, state, `{"type":"terminal.open"}`)
		if !panelsSubset(panelsLastEffect(t, transition), focusWindow) {
			t.Error("a shell is drawn in the window")
		}
		blank, transition := panelsReduce(t, shell, `{"type":"browser.new-tab"}`)
		if !panelsSubset(panelsLastEffect(t, transition), focusWindow) {
			t.Error("a page with no address is answered by the address bar")
		}
		loaded, transition := panelsReduce(t, blank, `{"type":"browser.open","url":"https://one.example"}`)
		if panelsHasEffect(t, transition, `{"type":"focus-window"}`) {
			t.Error("a page the user opens takes the keys itself")
		}
		panel, transition := panelsReduce(t, loaded, `{"type":"view.open-dock-panel","panel":"agents"}`)
		if panel.DockFocus.Tab != "agents" || !panelsSubset(panelsLastEffect(t, transition), focusWindow) {
			t.Error("a panel is a view to read, so it takes the keyboard too")
		}
		hidden, transition := panelsReduce(t, panel, `{"type":"view.set-dock-open","open":false}`)
		panelsEqual(t, transition.Effects, `[{"type":"focus-window"}]`, "a hidden panel must not keep what it was holding")
		shown, _ := panelsReduce(t, hidden, `{"type":"view.set-dock-open","open":true}`)
		if shown.DockFocus.Tab != "agents" {
			t.Error("showing the panel again hands the tab in front the keyboard")
		}
	})
}

func TestANewTabOpensAShellAndChoosingAShellsTabMakesItTheDocksShell(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1")), func(s *WorkspaceState) { s.LastFolder = new("/repo") })
		second := panelsRun(t, state, `{"type":"view.new-tab"}`, `{"type":"view.new-tab"}`)
		terminals := panelsDock(second).Terminals
		if len(terminals) != 2 || deref(panelsDock(second).TerminalID) != terminals[1].ID {
			t.Fatal("each new tab is another shell")
		}
		chosen, _ := panelsReduce(t, second, `{"type":"view.select-dock-tab","tab":"`+terminals[0].ID+`"}`)
		if deref(panelsDock(chosen).TerminalID) != terminals[0].ID {
			t.Error("choosing a shell's tab makes it the dock's shell")
		}
	})
}

func TestClosingADocksLastThreadTabHidesTheDock(t *testing.T) {
	panelsPorted(t, func() {
		state := WithDock(panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("worker"))), "task-1", func(dock *ThreadDock) {
			dock.Open, dock.Expanded, dock.ThreadTabs, dock.Tab = true, true, []string{"worker"}, "worker"
		})
		closed, transition := panelsReduce(t, state, `{"type":"view.close-thread-tab","taskId":"worker"}`)
		if panelsDock(closed).Open || panelsDock(closed).Expanded {
			t.Error("a hidden dock does not come back across the whole workspace")
		}
		if !panelsHasEffect(t, transition, `{"type":"focus-window"}`) {
			t.Error("the window takes the keys back")
		}
	})
}

func TestBringingAThreadsTabToTheFrontReadsIt(t *testing.T) {
	panelsPorted(t, func() {
		state := WithDock(panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("worker", func(thread *domain.Thread) {
			thread.Outcome = new(domain.ThreadOutcomeFinished)
			thread.OutcomeUnread = new(true)
		}))), "task-1", func(dock *ThreadDock) {
			dock.Open, dock.ThreadTabs, dock.Tab = true, []string{"worker"}, "picker"
		})
		shown, _ := panelsReduce(t, state, `{"type":"view.select-dock-tab","tab":"worker"}`)
		if panelsThread(shown, "worker").OutcomeUnread != nil {
			t.Error("bringing a thread's tab to the front reads what it had to say")
		}
	})
}

func TestExpandingTheDockShowsItAndEscapeGivesUpTheWorkspaceFirst(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1")), func(s *WorkspaceState) { s.LastFolder = new("/repo") })
		full, _ := panelsReduce(t, state, `{"type":"view.set-dock-expanded","expanded":true}`)
		if !panelsDock(full).Open || !DeriveView(full).DockExpanded {
			t.Fatal("asking for the whole workspace is a way of asking for the dock")
		}
		panel, _ := panelsReduce(t, full, `{"type":"view.open-dock-panel","panel":"agents"}`)
		restored, _ := panelsReduce(t, panel, `{"type":"view.close-tab"}`)
		if panelsDock(restored).Expanded || !slices.Equal(DeriveView(restored).DockPanels, []string{"agents"}) {
			t.Error("the first Escape puts the dock back in its column and leaves the tab alone")
		}
		closed, _ := panelsReduce(t, restored, `{"type":"view.close-tab"}`)
		if len(DeriveView(closed).DockPanels) != 0 {
			t.Error("the next one closes the tab")
		}
	})
}

func TestAHiddenDockDoesNotComeBackExpanded(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("task-1"), panelsTask("task-2")), func(s *WorkspaceState) { s.LastFolder = new("/repo") })
		full := panelsRun(t, state, `{"type":"view.set-dock-expanded","expanded":true}`)
		hidden, _ := panelsReduce(t, full, `{"type":"view.set-dock-open","open":false}`)
		if panelsDock(hidden, "task-1").Expanded {
			t.Error("hiding the dock ends the posture it was hidden in")
		}
		shown, _ := panelsReduce(t, hidden, `{"type":"view.set-dock-open","open":true}`)
		if DeriveView(shown).DockExpanded {
			t.Error("a shown dock is not expanded")
		}
		spread := panelsRun(t, shown, `{"type":"view.set-dock-expanded","expanded":true}`, `{"type":"task.select","taskId":"task-2"}`)
		if DeriveView(spread).DockExpanded || !panelsDock(spread, "task-1").Expanded {
			t.Error("each thread keeps its own posture")
		}
	})
}

func TestARestoredPageWaitsForThePanelToShowIt(t *testing.T) {
	panelsPorted(t, func() {
		preferences := ViewPreferences(panelsWorkspace())
		preferences.SessionPanelOpen = false
		preferences.BrowserTabs = map[string][]string{"draft": {"https://example.com/docs", "not a url"}}
		preferences.BrowserOrigins = []string{"https://example.com"}
		restored := Reduce(panelsWorkspace(), WorkspaceEventPreferencesLoaded{Preferences: preferences})
		tabs := panelsDock(&restored.State).BrowserTabs
		if len(tabs) != 1 || tabs[0].URL != "https://example.com/docs" || tabs[0].Loading {
			t.Fatalf("a restored page waits unloaded: %+v", tabs)
		}
		permissions := `{"type":"browser.permissions","permissions":{"origins":["https://example.com"],"autonomousTaskIds":[]}}`
		panelsEqual(t, restored.Effects, `[`+permissions+`]`, "restoring permissions loads no page on its own")
		tabID := tabs[0].ID
		_, shown := panelsReduce(t, &restored.State, `{"type":"view.select-dock-tab","tab":"`+tabID+`"}`)
		panelsEqual(t, shown.Effects, `[`+permissions+`,{"type":"browser.open","tabId":"`+tabID+`","url":"https://example.com/docs"},{"type":"browser.show","tabId":"`+tabID+`"}]`, "showing the tab loads it")
	})
}

func TestASideChatIsTheDockTabItOpens(t *testing.T) {
	panelsPorted(t, func() {
		opened := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("task-1"))),
			`{"type":"view.open-dock-panel","panel":"browser"}`,
			`{"type":"side-chat.open","chatId":"chat-1"}`,
		)
		if panelsDock(opened).Tab != "chat-1" || !panelsDock(opened).Open {
			t.Fatal("a side chat is the dock tab it opens")
		}
		closed, _ := panelsReduce(t, opened, `{"type":"side-chat.close","chatId":"chat-1"}`)
		if panelsDock(closed).Tab != "browser" {
			t.Error("closing it gives the dock back its last panel")
		}
	})
}

func TestClosingATabTakesWhatIsInFrontAndOnlyThenTheWindow(t *testing.T) {
	panelsPorted(t, func() {
		base := panelsWorkspace(panelsOn(panelsTask("task-1")))
		_, bare := panelsReduce(t, base, `{"type":"view.close-tab"}`)
		panelsEqual(t, bare.Effects, `[{"type":"close-window"}]`, "nothing is in front of a bare window")

		settings, opening := panelsReduce(t, base, `{"type":"view.set-settings-open","open":true}`)
		shut, transition := panelsReduce(t, settings, `{"type":"view.close-tab"}`)
		if shut.SettingsOpen {
			t.Error("settings close first")
		}
		panelsEqual(t, transition.Effects, `[]`, "settings closing is not the window closing")
		panelsEqual(t, opening.Effects, `[{"type":"focus-window"}]`, "settings opening takes the keys off whatever was drawing")

		dialogs := []func(*WorkspaceState){
			func(s *WorkspaceState) {
				s.WorktreeMove = &WorktreesStateWorktreeMove{TaskID: "task-1", Worktree: true}
			},
			func(s *WorkspaceState) { s.ForkName = &ForkName{TaskID: "task-1", Worktree: false, Title: "Copy"} },
			func(s *WorkspaceState) { s.ArchivePrompt = new("task-1") },
			func(s *WorkspaceState) { s.ProjectEdit = &ProjectEdit{ProjectID: "p"} },
		}
		for index, dialog := range dialogs {
			open := *base
			dialog(&open)
			open.SettingsOpen = true
			closed, _ := panelsReduce(t, &open, `{"type":"view.close-tab"}`)
			if closed.WorktreeMove != nil || closed.ForkName != nil || closed.ArchivePrompt != nil || closed.ProjectEdit != nil || !closed.SettingsOpen {
				t.Errorf("dialog %d closes before anything behind it", index)
			}
		}
		asked := *base
		asked.ComputerUseSetup = true
		if closed, _ := panelsReduce(t, &asked, `{"type":"view.close-tab"}`); closed.ComputerUseSetup {
			t.Error("the settings computer use opened close the same way")
		}

		browsing := panelsRun(t, base,
			`{"type":"view.open-dock-panel","panel":"agents"}`,
			`{"type":"browser.open","url":"https://one.example"}`,
			`{"type":"browser.open","url":"https://two.example","newTab":true}`,
		)
		first, second := panelsDock(browsing).BrowserTabs[0], panelsDock(browsing).BrowserTabs[1]
		closedPage, transition := panelsReduce(t, browsing, `{"type":"view.close-tab"}`)
		if len(panelsDock(closedPage).BrowserTabs) != 1 || panelsDock(closedPage).BrowserTabs[0].ID != first.ID {
			t.Error("the page in front is what ⌘W takes")
		}
		if !panelsHasEffect(t, transition, `{"type":"browser.close","tabId":"`+second.ID+`"}`) || panelsDock(closedPage).Tab != first.ID {
			t.Error("the page closes and the dock lands on its neighbour")
		}
		closedLast, _ := panelsReduce(t, closedPage, `{"type":"view.close-tab"}`)
		if len(panelsDock(closedLast).BrowserTabs) != 0 || panelsDock(closedLast).Tab != "agents" {
			t.Error("the panel behind the pages is the next thing in front")
		}
		closedAgents, _ := panelsReduce(t, closedLast, `{"type":"view.close-tab"}`)
		if len(panelsDock(closedAgents).Panels) != 0 || panelsDock(closedAgents).Tab != DockPicker {
			t.Error("the panel closes to the picker")
		}
		closedDock, transition := panelsReduce(t, closedAgents, `{"type":"view.close-tab"}`)
		if panelsDock(closedDock).Open {
			t.Error("the picker showing means the dock itself is what is in front")
		}
		panelsEqual(t, transition.Effects, `[{"type":"focus-window"}]`, "the window is left with the keyboard")
		_, last := panelsReduce(t, closedDock, `{"type":"view.close-tab"}`)
		panelsEqual(t, last.Effects, `[{"type":"close-window"}]`, "then the window goes")
	})
}

func TestASideChatInFrontClosesOnCommandW(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsRun(t, panelsWorkspace(panelsOn(panelsTask("task-1"))),
			`{"type":"view.open-dock-panel","panel":"agents"}`,
			`{"type":"side-chat.open","chatId":"chat-1"}`,
		)
		closed, _ := panelsReduce(t, state, `{"type":"view.close-tab"}`)
		if len(closed.SideChats) != 0 || panelsThread(closed, "chat-1") != nil || panelsDock(closed).Tab != "agents" {
			t.Error("a side chat's thread goes with it, and the dock lands on the panel")
		}
	})
}

func TestACoordinatorLeadsItsDockWithTheOverview(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsWorkspace(panelsOn(panelsTask("lead"), panelsTask("other")))
		made, _ := panelsReduce(t, state, `{"type":"task.set-role","taskId":"lead","role":"coordinator"}`)
		if !slices.Equal(panelsDock(made, "lead").Panels, []string{"overview"}) || panelsDock(made, "lead").Tab != "overview" {
			t.Error("what its threads will run on is chosen there before the first one starts")
		}
		if len(panelsDock(made, "other").Panels) != 0 {
			t.Error("a thread that coordinates nothing has none")
		}
		saved := panelsWorkspace(panelsOn(panelsTask("lead", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator) })))
		if loaded, _ := panelsReduce(t, saved, `{"type":"view.set-dock-open","open":true}`); !slices.Equal(panelsDock(loaded, "lead").Panels, []string{"overview"}) {
			t.Error("a coordinator saved before it had threads gets one too")
		}
		if stepped, _ := panelsReduce(t, made, `{"type":"task.set-role","taskId":"lead","role":"implementer"}`); len(panelsDock(stepped, "lead").Panels) != 0 {
			t.Error("a thread whose role is taken off loses it")
		}
	})
}
