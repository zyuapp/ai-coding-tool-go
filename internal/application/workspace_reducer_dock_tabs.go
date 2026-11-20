package application

import (
	"maps"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// FocusedTab: the caret goes to the tab a side chat is drawn in, because its composer is not the
// thread's own. Only state.DockFocus is read and replaced.
func FocusedTab(state *WorkspaceState, owner, tab string) *WorkspaceState {
	next := *state
	next.DockFocus = dockFocusAfter(state.DockFocus, owner, tab)
	return &next
}

// TakeKeys: the window has the keys again, which a page in the panel is otherwise holding.
var TakeKeys = []WorkspaceEffect{WorkspaceEffectFocusWindow{}}

// The docks as one input leaves them. Only the functions here make one.
type docksChange = DocksEdit

func DocksOf(state *WorkspaceState) DocksEdit {
	return DocksEdit{View: DocksState{OverviewFolds: state.OverviewFolds, DockFocus: state.DockFocus, Docks: state.Docks, Diffs: state.Diffs, KeyboardTab: state.KeyboardTab}}
}

// OverviewFoldIn is how a coordinator's Overview folds one of its groups.
func OverviewFoldIn(edit DocksEdit, taskID string, group domain.OverviewSection, fold domain.OverviewFold) DocksEdit {
	folds := domain.OverviewFolds{}
	if held := edit.View.OverviewFolds[taskID]; held != nil {
		folds = *held
	}
	if slot := overviewFoldSlot(&folds, group); slot != nil {
		value := fold
		*slot = &value
	}
	view := edit.View
	view.OverviewFolds = maps.Clone(view.OverviewFolds)
	if view.OverviewFolds == nil {
		view.OverviewFolds = map[string]*domain.OverviewFolds{}
	}
	view.OverviewFolds[taskID] = &folds
	return DocksEdit{View: view}
}

// KeyboardTabIn is the dock tab holding the keyboard, or none.
func KeyboardTabIn(edit DocksEdit, tab *string) DocksEdit {
	edit.View.KeyboardTab = tab
	return edit
}

// DraftReviewDroppedIn: the review the next new thread's draft was left looking at, gone, so a fresh
// draft compares its own project.
func DraftReviewDroppedIn(edit DocksEdit) DocksEdit {
	view := edit.View
	view.Diffs = maps.Clone(view.Diffs)
	if view.Diffs == nil {
		view.Diffs = map[string]*DiffState{}
	}
	delete(view.Diffs, DraftDock)
	return DocksEdit{View: view}
}

// DocksForgottenIn: threads gone for good. Their docks and reviews go, no dock keeps a tab on them,
// and neither the focus nor the keyboard is left on something no dock still draws among `sideChats`.
func DocksForgottenIn(edit DocksEdit, removed jsonx.Set[string], sideChats []*SideChat) DocksEdit {
	view := edit.View
	docks := withoutThreadTabs(WithoutThreadKeys(view.Docks, removed), removed)
	view.Docks = docks
	view.Diffs = WithoutThreadKeys(view.Diffs, removed)
	if view.DockFocus != nil && (removed.Has(view.DockFocus.Owner) || removed.Has(view.DockFocus.Tab)) {
		view.DockFocus = nil
	}
	if view.KeyboardTab == nil || *view.KeyboardTab == "" || !dockKeeps(docks, sideChats, *view.KeyboardTab) {
		view.KeyboardTab = nil
	}
	return DocksEdit{View: view}
}

// withoutThreadTabs: a deleted thread is no dock's tab, and a dock showing it falls back to the picker.
func withoutThreadTabs(docks map[string]*ThreadDock, removed jsonx.Set[string]) map[string]*ThreadDock {
	var cleaned map[string]*ThreadDock
	for owner, dock := range docks {
		if !slices.ContainsFunc(dock.ThreadTabs, removed.Has) {
			continue
		}
		if cleaned == nil {
			cleaned = maps.Clone(docks)
		}
		next := *dock
		next.ThreadTabs = slices.DeleteFunc(slices.Clone(dock.ThreadTabs), removed.Has)
		if removed.Has(dock.Tab) {
			next.Tab = DockPicker
		}
		cleaned[owner] = &next
	}
	if cleaned == nil {
		return docks
	}
	return cleaned
}

// dockKeeps is whether any dock still draws this tab: a panel, a page, a shell, a side chat, or a thread.
func dockKeeps(docks map[string]*ThreadDock, sideChats []*SideChat, tab string) bool {
	if slices.ContainsFunc(sideChats, func(chat *SideChat) bool { return chat.ID == tab }) {
		return true
	}
	for _, dock := range docks {
		if slices.Contains(dock.Panels, tab) ||
			slices.ContainsFunc(dock.BrowserTabs, func(page *domain.BrowserTab) bool { return page.ID == tab }) ||
			slices.ContainsFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == tab }) ||
			slices.Contains(dock.ThreadTabs, tab) {
			return true
		}
	}
	return false
}

// ChatTabsClosedIn: side chats closing. A dock showing one moves to the tab beside it, among the
// `sideChats` it still holds.
func ChatTabsClosedIn(edit DocksEdit, sideChats []*SideChat, closed jsonx.Set[string]) DocksEdit {
	held := &WorkspaceState{Docks: edit.View.Docks, SideChats: sideChats}
	docks := make(map[string]*ThreadDock, len(edit.View.Docks))
	for owner, dock := range edit.View.Docks {
		if closed.Has(dock.Tab) {
			next := *dock
			next.Tab = DockTabAfterClosing(held, owner, dock.Tab)
			docks[owner] = &next
		} else {
			docks[owner] = dock
		}
	}
	view := edit.View
	view.Docks = docks
	return DocksEdit{View: view}
}

// EditDiff is one dock's review with what the patch says, the rest of it as it was.
func EditDiff(edit DocksEdit, owner string, patch DiffPatch) DocksEdit {
	view := edit.View
	diff := *EmptyDiff
	if held := view.Diffs[owner]; held != nil {
		diff = *held
	}
	if patch != nil {
		patch(&diff)
	}
	view.Diffs = maps.Clone(view.Diffs)
	if view.Diffs == nil {
		view.Diffs = map[string]*DiffState{}
	}
	view.Diffs[owner] = &diff
	return DocksEdit{View: view}
}

func EditDock(edit DocksEdit, owner string, patch DockPatch) DocksEdit {
	view := edit.View
	view.Docks = docksWith(view.Docks, owner, patch)
	return DocksEdit{View: view}
}

func ShowDockTabIn(edit DocksEdit, owner, tab string) DocksEdit {
	return EditDock(edit, owner, func(dock *ThreadDock) {
		dock.Open = true
		dock.Tab = tab
	})
}

// FocusedTabIn is FocusedTab on the docks an input is changing.
func FocusedTabIn(edit DocksEdit, owner, tab string) DocksEdit {
	view := edit.View
	view.DockFocus = dockFocusAfter(view.DockFocus, owner, tab)
	return DocksEdit{View: view}
}

// FocusDockTabIn is FocusDockTab on the docks an input is changing.
func FocusDockTabIn(edit DocksEdit, owner, tab string) (DocksEdit, []WorkspaceEffect) {
	focused := FocusedTabIn(edit, owner, tab)
	if pageHasURL(focused.View.Docks[owner], tab) {
		return focused, []WorkspaceEffect{}
	}
	return focused, slices.Clone(TakeKeys)
}

// ShowDockTab brings a tab to the front of its own dock, so a page or a shell nobody asked to see
// still lands somewhere.
func ShowDockTab(state *WorkspaceState, owner, tab string) *WorkspaceState {
	return WithDock(state, owner, func(dock *ThreadDock) {
		dock.Open = true
		dock.Tab = tab
	})
}

// FocusDockTab hands a dock tab the keyboard. The view watches the count rather than being told to
// focus, and the window takes the keys back on its way, because only a page can hold them itself.
func FocusDockTab(state *WorkspaceState, owner, tab string) WorkspaceTransition {
	focused := FocusedTab(state, owner, tab)
	if pageHasURL(focused.Docks[owner], tab) {
		return Settled(focused, []WorkspaceEffect{}, nil)
	}
	return Settled(focused, slices.Clone(TakeKeys), nil)
}

// ClosedWindowDocks: the window is gone. Its shells went with it, and its pages wait to be drawn again.
func ClosedWindowDocks(edit DocksEdit) DocksEdit {
	docks := make(map[string]*ThreadDock, len(edit.View.Docks))
	for owner, dock := range edit.View.Docks {
		next := *dock
		if slices.ContainsFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == dock.Tab }) {
			next.Tab = DockPicker
		}
		next.Terminals = []*domain.TerminalSession{}
		next.TerminalID = nil
		next.BrowserTabs = make([]*domain.BrowserTab, len(dock.BrowserTabs))
		for index, page := range dock.BrowserTabs {
			waiting := *page
			waiting.Error = nil
			waiting.Loading = false
			waiting.CanGoBack = false
			waiting.CanGoForward = false
			next.BrowserTabs[index] = &waiting
		}
		docks[owner] = &next
	}
	view := edit.View
	view.Docks = docks
	view.KeyboardTab = nil
	return DocksEdit{View: view}
}

func PersistView(state *WorkspaceState) []WorkspaceEffect {
	return []WorkspaceEffect{WorkspaceEffectPersistPreferences{Preferences: ViewPreferences(state)}}
}

// HandOverDraftDock: the dock a draft was composed in belongs to the thread that send creates,
// pages, shells and all.
func HandOverDraftDock(state *WorkspaceState, taskID string) *WorkspaceState {
	moved := state
	if draft, ok := state.Docks[DraftDock]; ok {
		docks := maps.Clone(state.Docks)
		delete(docks, DraftDock)
		docks[taskID] = draft
		next := *state
		next.Docks = docks
		moved = &next
	}
	if draftDiff, ok := state.Diffs[DraftDock]; ok && draftDiff != nil {
		diffs := maps.Clone(state.Diffs)
		delete(diffs, DraftDock)
		diffs[taskID] = draftDiff
		next := *moved
		next.Diffs = diffs
		moved = &next
	}
	return moved
}

// DocksDisposed: a thread that is gone for good takes its dock with it. Its pages close and its shells stop.
func DocksDisposed(state *WorkspaceState, owners []string) Step {
	docks := maps.Clone(state.Docks)
	diffs := maps.Clone(state.Diffs)
	effects := []WorkspaceEffect{}
	emptied := false
	for _, owner := range owners {
		if diffs[owner] != nil {
			delete(diffs, owner)
			emptied = true
		}
		dock := docks[owner]
		if dock == nil {
			continue
		}
		for _, tab := range dock.BrowserTabs {
			effects = append(effects, WorkspaceEffectBrowserClose{TabID: tab.ID})
		}
		for _, terminal := range dock.Terminals {
			effects = append(effects, WorkspaceEffectTerminalClose{TerminalID: terminal.ID})
		}
		delete(docks, owner)
		emptied = true
	}
	if !emptied {
		return Step{}
	}
	view := DocksOf(state).View
	view.Docks = docks
	view.Diffs = diffs
	return Step{Edits: []OwnerEdit{DocksEdit{View: view}}, Effects: effects}
}

// validWorkflowPanels is the last docks map found to follow only workflows that exist, with the
// workflows map it was checked against, by identity.
var validWorkflowPanels struct {
	sync.Mutex
	docks     map[string]*ThreadDock
	workflows map[string][]*domain.Workflow
}

// PrunedWorkflowPanels: a dock follows a workflow only while its record is there, so a run that
// clears one closes its panel.
func PrunedWorkflowPanels(state *WorkspaceState) *WorkspaceState {
	validWorkflowPanels.Lock()
	known := validWorkflowPanels.docks != nil && jsonx.Same(validWorkflowPanels.docks, state.Docks) && jsonx.Same(validWorkflowPanels.workflows, state.Workflows)
	validWorkflowPanels.Unlock()
	if known {
		return state
	}
	next := state
	for _, owner := range slices.Sorted(maps.Keys(state.Docks)) {
		dock := state.Docks[owner]
		if dock.WorkflowID == nil || *dock.WorkflowID == "" || WorkflowByID(state, dock.WorkflowID) != nil {
			continue
		}
		panels := slices.DeleteFunc(slices.Clone(dock.Panels), func(panel string) bool { return panel == WorkflowPanel })
		tab := dock.Tab
		if dock.Tab == WorkflowPanel {
			tab = DockTabAfterClosing(next, owner, WorkflowPanel)
		}
		next = WithDock(next, owner, func(dock *ThreadDock) {
			dock.WorkflowID = nil
			dock.Panels = panels
			dock.Tab = tab
		})
	}
	if next == state && state.Docks != nil {
		validWorkflowPanels.Lock()
		validWorkflowPanels.docks, validWorkflowPanels.workflows = state.Docks, state.Workflows
		validWorkflowPanels.Unlock()
	}
	return next
}

// checkedOverviews is the last docks map already matched to these threads' coordinators, so a
// reduction that changes neither skips the pass.
var checkedOverviews struct {
	sync.Mutex
	docks   map[string]*ThreadDock
	threads []*domain.Thread
}

// WithOverviewPanels: a coordinator's dock leads with its Overview. The dock is never opened or
// switched for it; a thread that stops coordinating loses the tab.
func WithOverviewPanels(state *WorkspaceState) *WorkspaceState {
	checkedOverviews.Lock()
	known := checkedOverviews.docks != nil && jsonx.Same(checkedOverviews.docks, state.Docks) && jsonx.Same(checkedOverviews.threads, state.Threads)
	checkedOverviews.Unlock()
	if known {
		return state
	}
	leads := jsonx.Set[string]{}
	var order []string
	for _, thread := range state.Threads {
		if domain.IsCoordinator(thread) && !leads.Has(thread.ID) {
			leads[thread.ID] = struct{}{}
			order = append(order, thread.ID)
		}
	}
	next := state
	for _, owner := range order {
		dock := DockFor(next, owner)
		if slices.Contains(dock.Panels, OverviewPanel) {
			continue
		}
		tab := dock.Tab
		if !dock.Open && dock.Tab == DockPicker {
			tab = OverviewPanel
		}
		panels := append([]string{OverviewPanel}, dock.Panels...)
		next = WithDock(next, owner, func(dock *ThreadDock) {
			dock.Panels = panels
			dock.Tab = tab
		})
	}
	for _, owner := range slices.Sorted(maps.Keys(next.Docks)) {
		dock := next.Docks[owner]
		if leads.Has(owner) || !slices.Contains(dock.Panels, OverviewPanel) {
			continue
		}
		tab := dock.Tab
		if dock.Tab == OverviewPanel {
			tab = DockTabAfterClosing(next, owner, OverviewPanel)
		}
		panels := slices.DeleteFunc(slices.Clone(dock.Panels), func(panel string) bool { return panel == OverviewPanel })
		next = WithDock(next, owner, func(dock *ThreadDock) {
			dock.Panels = panels
			dock.Tab = tab
		})
	}
	if next.Docks != nil {
		checkedOverviews.Lock()
		checkedOverviews.docks, checkedOverviews.threads = next.Docks, next.Threads
		checkedOverviews.Unlock()
	}
	return next
}

func dockFocusAfter(focus *DocksStateDockFocus, owner, tab string) *DocksStateDockFocus {
	count := 0.0
	if focus != nil {
		count = focus.Count
	}
	return &DocksStateDockFocus{Owner: owner, Tab: tab, Count: count + 1}
}

// pageHasURL is whether a dock holds a page with this id that has an address.
func pageHasURL(dock *ThreadDock, tab string) bool {
	if dock == nil {
		return false
	}
	for _, page := range dock.BrowserTabs {
		if page.ID == tab {
			return page.URL != ""
		}
	}
	return false
}

// docksWith is WithDock on a docks map.
func docksWith(docks map[string]*ThreadDock, owner string, patch DockPatch) map[string]*ThreadDock {
	dock := *EmptyDock
	if held := docks[owner]; held != nil {
		dock = *held
	}
	if patch != nil {
		patch(&dock)
	}
	next := maps.Clone(docks)
	if next == nil {
		next = map[string]*ThreadDock{}
	}
	next[owner] = &dock
	return next
}

// overviewFoldSlot is the field of folds that holds one group's fold, or nil for no group.
func overviewFoldSlot(folds *domain.OverviewFolds, group domain.OverviewSection) **domain.OverviewFold {
	switch group {
	case domain.OverviewSectionWorking:
		return &folds.Working
	case domain.OverviewSectionDone:
		return &folds.Done
	case domain.OverviewSectionNeeds:
		return &folds.Needs
	case domain.OverviewSectionReview:
		return &folds.Review
	case domain.OverviewSectionResolved:
		return &folds.Resolved
	case domain.OverviewSectionMemory:
		return &folds.Memory
	case domain.OverviewSectionUsage:
		return &folds.Usage
	}
	return nil
}
