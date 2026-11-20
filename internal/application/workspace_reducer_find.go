package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The find bar's target: what it asks of the view holding the text, and when that view is gone.

// What a search asks of whoever holds the text. A thread and a review are counted where they are drawn.
func SearchEffects(find *FindState, findNext, forward bool) []WorkspaceEffect {
	query := threadsTrim(find.Query)
	if query == "" || !domain.SearchesItself(find.Target) {
		return []WorkspaceEffect{}
	}
	switch target := find.Target.(type) {
	case domain.FindTargetBrowser:
		return []WorkspaceEffect{WorkspaceEffectFindInPage{TabID: target.TabID, Query: query, Forward: forward, FindNext: findNext}}
	case domain.FindTargetTerminal:
		return []WorkspaceEffect{WorkspaceEffectFindInTerminal{TerminalID: target.TerminalID, Query: query, Forward: forward}}
	}
	return []WorkspaceEffect{}
}

// A page and a shell keep highlighting what was found until they are told to stop. `find` may be nil.
func StopSearchEffects(find *FindState) []WorkspaceEffect {
	if find == nil || !domain.SearchesItself(find.Target) {
		return []WorkspaceEffect{}
	}
	switch target := find.Target.(type) {
	case domain.FindTargetBrowser:
		return []WorkspaceEffect{WorkspaceEffectStopFindInPage{TabID: target.TabID}}
	case domain.FindTargetTerminal:
		return []WorkspaceEffect{WorkspaceEffectStopFindInTerminal{TerminalID: target.TerminalID}}
	}
	return []WorkspaceEffect{}
}

// Find belongs to the view it is searching, so it goes when that view does.
func findViewGone(state *WorkspaceState, target domain.FindTarget) bool {
	owner, dock := FrontDock(state)
	switch target := target.(type) {
	case domain.FindTargetThread:
		if threadsSameString(target.TaskID, state.CurrentID) {
			return false
		}
		return !slices.ContainsFunc(DockSideChats(state, owner), func(chat *SideChat) bool { return target.TaskID != nil && chat.ID == *target.TaskID })
	case domain.FindTargetBrowser:
		holder, ok := OwnerOfBrowserTab(state, target.TabID)
		return !ok || holder == ""
	case domain.FindTargetTerminal:
		searched := state
		if computer := SelectedComputer(state); computer != nil && computer.State != nil {
			searched = computer.State
		}
		holder, ok := OwnerOfTerminal(searched, target.TerminalID)
		return !ok || holder == ""
	case domain.FindTargetReview:
		return target.Owner != owner || !slices.Contains(dock.Panels, DiffPanel)
	case domain.FindTargetPanel:
		return target.Owner != owner || !slices.Contains(dock.Panels, target.Panel)
	}
	return false
}

// Runs at the end of every reduce, which is also what validates an externally supplied target: a
// review or a panel naming a dock that is not in front is cleared on the reduce that opened it.
func PrunedFind(state *WorkspaceState) *WorkspaceState {
	keyboardTab := state.KeyboardTab
	if keyboardTab != nil && (*keyboardTab == "" || !DockHoldsTab(state, *keyboardTab)) {
		keyboardTab = nil
	}
	gone := state.Find != nil && findViewGone(state, state.Find.Target)
	if !gone && keyboardTab == state.KeyboardTab {
		return state
	}
	next := *state
	next.KeyboardTab = keyboardTab
	if gone {
		next.Find, next.FindResults = nil, nil
	}
	return &next
}
