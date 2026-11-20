package application

import (
	"maps"
	"math"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// DockInputs are the input types ReduceDock takes.
var DockInputs = []string{
	"view.close-tab", "view.new-tab", "view.select-dock-index", "view.set-dock-open", "view.set-dock-expanded", "view.open-dock-panel",
	"view.open-workflow", "view.close-dock-panel", "view.select-dock-tab", "view.close-thread-tab", "view.set-overview-group",
}

// reviewSubject reads the review for the checkout it is about now, starting over when that is
// another checkout.
func reviewSubject(state *WorkspaceState, owner string) Step {
	diff := DiffFor(state, owner)
	subject := SubjectWorkspaceID(state, owner)
	if diff.WorkspaceID == nil || sameOptionalString(diff.WorkspaceID, subject) {
		return DiffRead(state, owner, InitialRange(state, owner, diff), nil)
	}
	var diffRange domain.DiffRange = DefaultBranchRange(state, owner)
	if diff.Mode == DiffStateModeUncommitted {
		diffRange = diff.Range
	}
	return DiffRead(state, owner, diffRange, func(next *DiffState) {
		next.Result = nil
		next.Collapsed = []string{}
		next.Viewed = map[string]string{}
	})
}

// closeFront is ⌘W: whatever is in front closes, a dialog before the settings, the settings before
// the dock, and the dock's tab before the window.
func closeFront(state *WorkspaceState) Step {
	owner, dock := FrontDock(state)
	switch {
	case state.WorktreeMove != nil:
		return Step{Edits: []OwnerEdit{MoveDismissedIn(WorktreesOf(state))}}
	case state.ForkName != nil:
		return Step{Edits: []OwnerEdit{ForkNameIn(DialogsOf(state), nil)}}
	case state.ArchivePrompt != nil && *state.ArchivePrompt != "":
		return Step{Edits: []OwnerEdit{ArchivePromptIn(DialogsOf(state), nil)}}
	case state.ProjectEdit != nil:
		return Step{Edits: []OwnerEdit{EditorClosedIn(ProjectsOf(state))}}
	case state.SettingsOpen || state.ComputerUseSetup:
		return SettingsShown(state, false, nil, nil)
	case !dock.Open:
		return Step{Effects: []WorkspaceEffect{WorkspaceEffectCloseWindow{}}}
	// A dock across the whole workspace is the frontmost thing there is, so it gives that up first.
	case dock.Expanded:
		return Step{Edits: []OwnerEdit{EditDock(DocksOf(state), owner, func(dock *ThreadDock) { dock.Expanded = false })}}
	}
	var closed Step
	// Overview does not close, so ⌘W on it hides the dock the way it does on the picker.
	switch kind := DockTabKind(state, owner, dock.Tab); {
	case kind == DockKindPicker || dock.Tab == OverviewPanel:
		closed = Step{Edits: []OwnerEdit{EditDock(DocksOf(state), owner, func(dock *ThreadDock) { dock.Open = false })}}
	case kind == DockKindBrowser:
		holder, ok := OwnerOfBrowserTab(state, dock.Tab)
		if !ok {
			holder = DockOwner(state, nil)
		}
		closed = BrowserTabClosed(state, holder, dock.Tab)
	case kind == DockKindTerminal:
		closed = Step(ReduceTerminal(state, contracts.TerminalCommandTerminalClose{TerminalID: dock.Tab}))
	case kind == DockKindSideChat:
		closing := []*SideChat{}
		for _, chat := range state.SideChats {
			if chat.ID == dock.Tab {
				closing = append(closing, chat)
			}
		}
		closed = SideChatsClosed(state, closing)
	case kind == DockKindThread:
		closed = ReduceDock(state, contracts.ViewCommandViewCloseThreadTab{TaskID: dock.Tab})
	default:
		closed = ReduceDock(state, contracts.ViewCommandViewCloseDockPanel{Panel: dock.Tab})
	}
	// Whatever the closed view was holding is gone with it, so the window takes the keys back.
	closed.Effects = slices.Concat(closed.Effects, TakeKeys)
	return closed
}

// ReduceDock takes an input whose type is one of DockInputs.
func ReduceDock(state *WorkspaceState, input WorkspaceInput) Step {
	switch input := input.(type) {
	case contracts.ViewCommandViewCloseTab:
		return closeFront(state)

	// ⌘W's inverse, answering with whatever the panel is showing rather than one fixed thing.
	case contracts.ViewCommandViewNewTab:
		if state.SettingsOpen {
			return Step{}
		}
		owner, dock := FrontDock(state)
		kind := DockKindPicker
		if dock.Open {
			kind = DockTabKind(state, owner, dock.Tab)
		}
		if kind == DockKindBrowser {
			return NewBrowserTab(state)
		}
		return Step(ReduceTerminal(state, contracts.TerminalCommandTerminalOpen{}))

	case contracts.ViewCommandViewSelectDockIndex:
		tabs := DockTabIds(state, DockOwner(state, nil))
		index := -2
		if input.Index == -1 {
			index = len(tabs) - 1
		} else if input.Index >= 0 && input.Index == math.Trunc(input.Index) && input.Index < float64(len(tabs)) {
			index = int(input.Index)
		}
		if index < 0 || index >= len(tabs) || tabs[index] == "" {
			return Step{}
		}
		return ReduceDock(state, contracts.ViewCommandViewSelectDockTab{Tab: tabs[index]})

	case contracts.ViewCommandViewSetDockOpen:
		owner, dock := FrontDock(state)
		if dock.Open == input.Open {
			return Step{}
		}
		toggled := EditDock(DocksOf(state), owner, func(dock *ThreadDock) { dock.Open = input.Open })
		// A panel shown is one to work in; a panel hidden must not leave a page it was drawing with the keys.
		if !input.Open {
			return Step{Edits: []OwnerEdit{EditDock(toggled, owner, func(dock *ThreadDock) { dock.Expanded = false })}, Effects: slices.Clone(TakeKeys)}
		}
		return focusedUnlessPicker(state, toggled, owner, dock.Tab)

	case contracts.ViewCommandViewSetDockExpanded:
		owner, dock := FrontDock(state)
		if dock.Expanded == input.Expanded {
			return Step{}
		}
		// Taking the whole workspace is also a way of asking for the dock, so expanding shows it.
		open := input.Expanded || dock.Open
		return focusedUnlessPicker(state, EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Expanded = input.Expanded
			dock.Open = open
		}), owner, dock.Tab)

	case contracts.ViewCommandViewOpenDockPanel:
		owner, dock := FrontDock(state)
		panels := dock.Panels
		if !slices.Contains(panels, input.Panel) {
			panels = append(slices.Clip(panels), input.Panel)
		}
		subjects := maps.Clone(dock.Subjects)
		if subjects == nil {
			subjects = map[string]string{}
		}
		delete(subjects, input.Panel)
		if input.TaskID != nil && *input.TaskID != "" && slices.Contains(dock.ThreadTabs, *input.TaskID) {
			subjects[input.Panel] = *input.TaskID
		}
		shown, effects := FocusDockTabIn(EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Open = true
			dock.Panels = panels
			dock.Tab = input.Panel
			dock.Subjects = subjects
		}), owner, input.Panel)
		opened := Step{Edits: []OwnerEdit{shown}, Effects: slices.Concat(browserEffectsIn(DocksDock(shown.View, owner), input.Panel), effects)}
		// However the review is reached, it opens on a list read now rather than one read last time.
		if input.Panel != DiffPanel {
			return opened
		}
		return AndThen(state, opened, func(next *WorkspaceState) Step { return reviewSubject(next, owner) })

	case contracts.ViewCommandViewOpenWorkflow:
		var listed []*domain.Workflow
		if state.CurrentID != nil && *state.CurrentID != "" {
			listed = state.Workflows[*state.CurrentID]
		}
		if !slices.ContainsFunc(listed, func(workflow *domain.Workflow) bool { return workflow.ID == input.WorkflowID }) {
			return Step{}
		}
		workflowID := input.WorkflowID
		following := Step{Edits: []OwnerEdit{EditDock(DocksOf(state), DockOwner(state, nil), func(dock *ThreadDock) { dock.WorkflowID = new(workflowID) })}}
		return AndThen(state, following, func(opened *WorkspaceState) Step {
			return ReduceDock(opened, contracts.ViewCommandViewOpenDockPanel{Panel: WorkflowPanel})
		})

	case contracts.ViewCommandViewCloseDockPanel:
		owner, dock := FrontDock(state)
		if !slices.Contains(dock.Panels, input.Panel) || input.Panel == OverviewPanel {
			return Step{}
		}
		tab := dock.Tab
		if dock.Tab == input.Panel {
			tab = DockTabAfterClosing(state, owner, input.Panel)
		}
		panels := slices.DeleteFunc(slices.Clone(dock.Panels), func(panel string) bool { return panel == input.Panel })
		closed := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Panels = panels
			dock.Tab = tab
			if input.Panel == WorkflowPanel {
				dock.WorkflowID = nil
			}
		})
		return Step{Edits: []OwnerEdit{closed}, Effects: browserEffectsIn(DocksDock(closed.View, owner), tab)}

	case contracts.ViewCommandViewCloseThreadTab:
		owner, dock := FrontDock(state)
		if !slices.Contains(dock.ThreadTabs, input.TaskID) {
			return Step{}
		}
		last := len(dock.ThreadTabs) == 1 && slices.Contains(dock.Panels, OverviewPanel)
		tab := dock.Tab
		if dock.Tab == input.TaskID {
			if last {
				tab = OverviewPanel
			} else {
				tab = DockTabAfterClosing(state, owner, input.TaskID)
			}
		}
		// Closing a coordinator's last thread tab falls back to its Overview; with nothing to fall back to, the dock hides.
		open := dock.Open && tab != DockPicker
		subjects := map[string]string{}
		for panel, id := range dock.Subjects {
			if id != input.TaskID {
				subjects[panel] = id
			}
		}
		threadTabs := slices.DeleteFunc(slices.Clone(dock.ThreadTabs), func(id string) bool { return id == input.TaskID })
		closed := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.ThreadTabs = threadTabs
			dock.Tab = tab
			dock.Open = open
			dock.Subjects = subjects
			if !open {
				dock.Expanded = false
			}
		})
		closing := Step{Edits: []OwnerEdit{closed}, Effects: slices.Clone(TakeKeys)}
		if open {
			closing.Effects = browserEffectsIn(DocksDock(closed.View, owner), tab)
		}
		// A review of the closed tab's checkout goes back to the dock's own.
		if subject, ok := dock.Subjects[DiffPanel]; !ok || subject != input.TaskID || !slices.Contains(dock.Panels, DiffPanel) {
			return closing
		}
		return AndThen(state, closing, func(next *WorkspaceState) Step {
			reread := reviewSubject(next, owner)
			reread.Effects = FollowUps(reread.Effects)
			return reread
		})

	case contracts.ViewCommandViewSetOverviewGroup:
		// The coordinator may run on a paired computer, so its id is not looked up here.
		if !domain.IsOverviewSection(input.Group) {
			return Step{}
		}
		fold := domain.OverviewFold{}
		if folds := state.OverviewFolds[input.TaskID]; folds != nil {
			if held := *overviewFoldSlot(folds, input.Group); held != nil {
				fold = *held
			}
		}
		next := fold
		if input.Open != nil {
			next.Open = new(*input.Open)
		}
		if input.All != nil && *input.All {
			next.All = new(true)
		}
		if sameOptionalBool(next.Open, fold.Open) && sameOptionalBool(next.All, fold.All) {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{OverviewFoldIn(DocksOf(state), input.TaskID, input.Group, next)}}

	case contracts.ViewCommandViewSelectDockTab:
		owner := DockOwner(state, nil)
		kind := DockTabKind(state, owner, input.Tab)
		tab := input.Tab
		shown := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Tab = tab
			dock.Open = true
			if kind == DockKindBrowser {
				dock.BrowserTabID = new(tab)
			}
			if kind == DockKindTerminal {
				dock.TerminalID = new(tab)
			}
		})
		selected, effects := FocusDockTabIn(shown, owner, tab)
		// A side chat or a thread's tab is a thread, so bringing it to the front is landing on it.
		var edits []OwnerEdit
		if kind == DockKindSideChat || kind == DockKindThread {
			edits = append(edits, ReadIn(ThreadsOf(state), tab))
		}
		return Step{Edits: append(edits, selected), Effects: slices.Concat(browserEffectsIn(DocksDock(selected.View, owner), tab), effects)}
	}
	return Step{}
}

// focusedUnlessPicker: a dock just shown or widened hands its tab the keys, unless that tab is the picker.
func focusedUnlessPicker(state *WorkspaceState, docks DocksEdit, owner, tab string) Step {
	if DockTabKind(state, owner, tab) == DockKindPicker {
		return Step{Edits: []OwnerEdit{docks}}
	}
	focused, effects := FocusDockTabIn(docks, owner, tab)
	return Step{Edits: []OwnerEdit{focused}, Effects: effects}
}

func sameOptionalBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
