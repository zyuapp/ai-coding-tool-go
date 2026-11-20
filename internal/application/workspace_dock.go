package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// DockPicker is the dock tab that offers the panels, shown whenever no panel is on top.
const DockPicker = "home"

// DraftDock is the dock the app shows while no thread is current, so a draft has a dock of its own too.
const DraftDock = "draft"

// EmptyDock is every dock nobody opened. Shared; never written.
var EmptyDock = &ThreadDock{
	Open:         false,
	Expanded:     false,
	Panels:       []string{},
	Tab:          DockPicker,
	WorkflowID:   nil,
	BrowserTabs:  []*domain.BrowserTab{},
	BrowserTabID: nil,
	Terminals:    []*domain.TerminalSession{},
	TerminalID:   nil,
	SimulatorID:  nil,
	ThreadTabs:   []string{},
	Subjects:     map[string]string{},
}

// DockPatch changes a copy of a dock in place (TS: Partial<ThreadDock>).
type DockPatch func(next *ThreadDock)

// DockOwner is whose dock a command belongs in: the thread it names, else the one the user is
// looking at. A side chat is a tab within its source thread's dock. `threadID` may be nil.
func DockOwner(state *WorkspaceState, threadID *string) string {
	id := threadID
	if id == nil {
		id = state.CurrentID
	}
	if id == nil || *id == "" {
		return DraftDock
	}
	for _, chat := range state.SideChats {
		if chat.ID == *id {
			return chat.SourceThreadID
		}
	}
	return *id
}

// DockFor reads only state.Docks.
func DockFor(state *WorkspaceState, owner string) *ThreadDock {
	if dock, ok := state.Docks[owner]; ok && dock != nil {
		return dock
	}
	return EmptyDock
}

// DockSubject is the thread tab a dock's panel is about, while that tab is still open. Reads only state.Docks.
func DockSubject(state *WorkspaceState, owner, panel string) *string {
	dock := DockFor(state, owner)
	subject, ok := dock.Subjects[panel]
	if ok && slices.Contains(dock.ThreadTabs, subject) {
		return &subject
	}
	return nil
}

// DiffPanel is the dock tab the review is drawn in, which the picker and the composer both name.
const DiffPanel = "diff"

// AutomationPanel is the dock tab a thread's schedule is edited in.
const AutomationPanel = "automation"

// WorkflowPanel is the dock tab one workflow is followed in.
const WorkflowPanel = "workflow"

// SimulatorPanel is the dock tab the iOS simulator is shown and driven in.
const SimulatorPanel = "simulator"

// OverviewPanel is a coordinator's threads, grouped by what they need. It leads a coordinator's dock
// and does not close.
const OverviewPanel = "overview"

// FrontDock is the dock in front, and whose it is: the pair every view command starts from.
func FrontDock(state *WorkspaceState) (owner string, dock *ThreadDock) {
	owner = DockOwner(state, nil)
	return owner, DockFor(state, owner)
}

// ShownThreadTab is the thread tab the dock in front is showing, while it shows one.
func ShownThreadTab(state *WorkspaceState) *string {
	_, dock := FrontDock(state)
	if dock.Open && slices.Contains(dock.ThreadTabs, dock.Tab) {
		return new(dock.Tab)
	}
	return nil
}

// WithDock is state with one dock changed by `patch` (nil changes nothing but still copies the dock).
// Only state.Docks is read and replaced.
func WithDock(state *WorkspaceState, owner string, patch DockPatch) *WorkspaceState {
	dock := *DockFor(state, owner)
	if patch != nil {
		patch(&dock)
	}
	docks := maps.Clone(state.Docks)
	if docks == nil {
		docks = map[string]*ThreadDock{}
	}
	docks[owner] = &dock
	next := *state
	next.Docks = docks
	return &next
}

// OwnerOfBrowserTab is which dock holds a page, for the events and commands that only name its id.
func OwnerOfBrowserTab(state *WorkspaceState, tabID string) (string, bool) {
	for _, owner := range slices.Sorted(maps.Keys(state.Docks)) {
		if slices.ContainsFunc(state.Docks[owner].BrowserTabs, func(tab *domain.BrowserTab) bool { return tab.ID == tabID }) {
			return owner, true
		}
	}
	return "", false
}

func OwnerOfTerminal(state *WorkspaceState, terminalID string) (string, bool) {
	for _, owner := range slices.Sorted(maps.Keys(state.Docks)) {
		if slices.ContainsFunc(state.Docks[owner].Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == terminalID }) {
			return owner, true
		}
	}
	return "", false
}

// DockSideChats is the forks a dock draws as tabs: the ones taken from the thread that owns it.
func DockSideChats(state *WorkspaceState, owner string) []*SideChat {
	chats := []*SideChat{}
	for _, chat := range state.SideChats {
		if chat.SourceThreadID == owner {
			chats = append(chats, chat)
		}
	}
	return chats
}

// DockKind is what a dock tab is showing (TS: dockTabKind's return).
type DockKind string

const (
	DockKindBrowser  DockKind = "browser"
	DockKindTerminal DockKind = "terminal"
	DockKindSideChat DockKind = "side-chat"
	DockKindThread   DockKind = "thread"
	DockKindPanel    DockKind = "panel"
	DockKindPicker   DockKind = "picker"
)

// DockTabKind is what a dock tab is showing. A page and a shell are tabs in their own right.
func DockTabKind(state *WorkspaceState, owner, tab string) DockKind {
	dock := DockFor(state, owner)
	switch {
	case slices.ContainsFunc(dock.BrowserTabs, func(page *domain.BrowserTab) bool { return page.ID == tab }):
		return DockKindBrowser
	case slices.ContainsFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == tab }):
		return DockKindTerminal
	case slices.ContainsFunc(DockSideChats(state, owner), func(chat *SideChat) bool { return chat.ID == tab }):
		return DockKindSideChat
	case slices.Contains(dock.ThreadTabs, tab):
		return DockKindThread
	case slices.Contains(dock.Panels, tab):
		return DockKindPanel
	}
	return DockKindPicker
}

// DockTabIds is every tab in the dock, in the order the strip draws them. Reads state.Docks and state.SideChats.
func DockTabIds(state *WorkspaceState, owner string) []string {
	dock := DockFor(state, owner)
	tabs := slices.Clone(dock.Panels)
	for _, page := range dock.BrowserTabs {
		tabs = append(tabs, page.ID)
	}
	for _, terminal := range dock.Terminals {
		tabs = append(tabs, terminal.ID)
	}
	for _, chat := range DockSideChats(state, owner) {
		tabs = append(tabs, chat.ID)
	}
	tabs = append(tabs, dock.ThreadTabs...)
	if tabs == nil {
		tabs = []string{}
	}
	return tabs
}

// DockHoldsTab: a dock tab holds the keyboard only while the dock in front is open and still has that tab.
func DockHoldsTab(state *WorkspaceState, tab string) bool {
	owner, dock := FrontDock(state)
	if !dock.Open {
		return false
	}
	return slices.Contains(dock.Panels, tab) ||
		slices.ContainsFunc(dock.BrowserTabs, func(page *domain.BrowserTab) bool { return page.ID == tab }) ||
		slices.ContainsFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == tab }) ||
		slices.ContainsFunc(DockSideChats(state, owner), func(chat *SideChat) bool { return chat.ID == tab }) ||
		slices.Contains(dock.ThreadTabs, tab)
}

// TabHolderOf is which dock holds a thread as a tab of its own: a side chat's source, or the
// coordinator that has the thread open.
func TabHolderOf(state *WorkspaceState, taskID string) (string, bool) {
	for _, chat := range state.SideChats {
		if chat.ID == taskID {
			return chat.SourceThreadID, true
		}
	}
	for _, owner := range slices.Sorted(maps.Keys(state.Docks)) {
		if slices.Contains(state.Docks[owner].ThreadTabs, taskID) {
			return owner, true
		}
	}
	return "", false
}

// KeyboardThreadID is the thread whose tab holds the keyboard, when the tab holding it is a conversation.
func KeyboardThreadID(state *WorkspaceState) *string {
	tab := state.KeyboardTab
	if tab == nil || *tab == "" {
		return nil
	}
	if kind := DockTabKind(state, DockOwner(state, nil), *tab); kind == DockKindSideChat || kind == DockKindThread {
		return tab
	}
	return nil
}

// KeyboardTerminalID is the shell holding the keyboard, when the tab holding it is one.
func KeyboardTerminalID(state *WorkspaceState) *string {
	tab := state.KeyboardTab
	if tab != nil && *tab != "" && DockTabKind(state, DockOwner(state, nil), *tab) == DockKindTerminal {
		return tab
	}
	return nil
}

// DockTabAfterClosing is which tab takes over when `tab` closes: its neighbour on the left, else on
// the right, else the picker. Reads state.Docks and state.SideChats.
func DockTabAfterClosing(state *WorkspaceState, owner, tab string) string {
	tabs := DockTabIds(state, owner)
	index := slices.Index(tabs, tab)
	if index == -1 {
		return DockFor(state, owner).Tab
	}
	remaining := slices.DeleteFunc(slices.Clone(tabs), func(id string) bool { return id == tab })
	if index-1 >= 0 && index-1 < len(remaining) {
		return remaining[index-1]
	}
	if index < len(remaining) {
		return remaining[index]
	}
	return DockPicker
}

func ActiveBrowserTab(dock *ThreadDock) *domain.BrowserTab {
	if dock.BrowserTabID == nil {
		return nil
	}
	for _, tab := range dock.BrowserTabs {
		if tab.ID == *dock.BrowserTabID {
			return tab
		}
	}
	return nil
}

// BrowserTarget is which tab a browser command acts on: the one it names, else the one that dock is
// showing. `tabID` may be nil.
func BrowserTarget(dock *ThreadDock, tabID *string) *domain.BrowserTab {
	if tabID == nil {
		return ActiveBrowserTab(dock)
	}
	for _, tab := range dock.BrowserTabs {
		if tab.ID == *tabID {
			return tab
		}
	}
	return nil
}

func ActiveTerminal(dock *ThreadDock) *domain.TerminalSession {
	if dock.TerminalID == nil {
		return nil
	}
	for _, terminal := range dock.Terminals {
		if terminal.ID == *dock.TerminalID {
			return terminal
		}
	}
	return nil
}

// TerminalTarget is which terminal a read acts on: the one it names, else the one the asking thread
// opened, else the one its dock is showing. Either id may be nil.
func TerminalTarget(dock *ThreadDock, terminalID, threadID *string) *domain.TerminalSession {
	if terminalID != nil {
		for _, terminal := range dock.Terminals {
			if terminal.ID == *terminalID {
				return terminal
			}
		}
		return nil
	}
	if threadID != nil {
		for index := len(dock.Terminals) - 1; index >= 0; index-- {
			if terminal := dock.Terminals[index]; terminal.TaskID != nil && *terminal.TaskID == *threadID {
				return terminal
			}
		}
	}
	return ActiveTerminal(dock)
}
