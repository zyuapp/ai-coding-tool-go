package application

import (
	"math"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The find bar as it is drawn. A thread is counted here, because its messages are in the state; every
// other view counts its own matches and reports them back.

// Nil while the find bar is closed. `currentThread` may be nil.
func WorkspaceFindFindView(state *WorkspaceState, currentThread *domain.Thread) *FindView {
	find := state.Find
	if find == nil {
		return nil
	}
	if target, ok := find.Target.(domain.FindTargetThread); ok {
		// A side chat is a thread like any other, so naming it is all the same search needs.
		var thread *domain.Thread
		var current *string
		if currentThread != nil {
			current = &currentThread.ID
		}
		if threadsSameString(target.TaskID, current) {
			thread = currentThread
		} else if target.TaskID != nil {
			thread = threadsFind(state.Threads, *target.TaskID)
		}
		var messages []*domain.ConversationMessage
		if thread != nil {
			messages = thread.Messages
		}
		hits := domain.MemoizedFindHits(messages, find.Query)
		index := 0.0
		if len(hits) > 0 {
			index = math.Min(find.Index, float64(len(hits)-1))
		}
		view := &FindView{FindState: *find, Matches: float64(len(hits))}
		view.Index = index
		if at := int(index); index == math.Trunc(index) && at >= 0 && at < len(hits) {
			view.Hit = hits[at]
		}
		return view
	}
	reported := state.FindResults
	matches := 0.0
	if reported != nil {
		matches = reported.Matches
	}
	view := &FindView{FindState: *find, Matches: matches}
	if domain.SearchesItself(find.Target) {
		view.Index = 0
		if reported != nil && reported.Index != nil {
			view.Index = *reported.Index
		}
		return view
	}
	// Nothing reported yet is a view still counting, not a view that found nothing.
	if reported != nil {
		view.Counting = reported.Counting != nil && *reported.Counting
	} else {
		view.Counting = threadsTrim(find.Query) != ""
	}
	view.Index = 0
	if matches != 0 {
		view.Index = math.Min(find.Index, matches-1)
	}
	return view
}

// What ⌘F searches: the page when the keystroke came from one and the dock is showing it, else the
// dock view holding the keys — a shell, a side chat's thread, the review, a panel — else the thread
// being read. A keystroke is the only thing that knows about the page, because a page swallows it.
func FindTargetFor(state *WorkspaceState, surface domain.ShortcutSurface) domain.FindTarget {
	if computer := SelectedComputer(state); computer != nil && computer.State != nil {
		remote := *computer.State
		remote.Computers = NoComputers
		return FindTargetFor(&remote, surface)
	}
	owner, dock := FrontDock(state)
	// The page holding the keys is the one the dock is showing, which a run's page never is.
	if surface == domain.ShortcutSurfaceBrowser {
		for _, page := range dock.BrowserTabs {
			if page.ID == dock.Tab {
				return domain.FindTargetBrowser{TabID: page.ID}
			}
		}
	}
	thread := domain.FindTargetThread{TaskID: state.CurrentID}
	if state.KeyboardTab == nil || *state.KeyboardTab == "" {
		return thread
	}
	tab := *state.KeyboardTab
	switch DockTabKind(state, owner, tab) {
	case DockKindBrowser:
		return domain.FindTargetBrowser{TabID: tab}
	case DockKindTerminal:
		return domain.FindTargetTerminal{TerminalID: tab}
	case DockKindSideChat, DockKindThread:
		return domain.FindTargetThread{TaskID: &tab}
	case DockKindPanel:
		if tab == DiffPanel {
			return domain.FindTargetReview{Owner: owner}
		}
		return domain.FindTargetPanel{Owner: owner, Panel: tab}
	}
	return thread
}
