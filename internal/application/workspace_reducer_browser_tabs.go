package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The browser's own state as one input leaves it. Only the functions here make one.
type browserChange = BrowserEdit

func BrowserOf(state *WorkspaceState) BrowserEdit {
	return BrowserEdit{View: BrowserOwn{BrowserOrigins: state.BrowserOrigins, BrowserApproval: state.BrowserApproval}}
}

// ApprovalForgottenIn: a run's ask to browse goes with the thread that asked, once that thread is
// gone for good.
func ApprovalForgottenIn(edit BrowserEdit, removed jsonx.Set[string]) BrowserEdit {
	approval := edit.View.BrowserApproval
	if approval != nil && removed.Has(approval.TaskID) {
		edit.View.BrowserApproval = nil
	}
	return edit
}

// browserTabsCurrent is the browser's own state with this input's changes so far. `own` is the
// browser's own fields as the input has them so far, or nil for none changed (TS: Pages["patch"]).
func browserTabsCurrent(state *WorkspaceState, own *BrowserOwn) BrowserOwn {
	if own != nil {
		return *own
	}
	return BrowserOwn{BrowserOrigins: state.BrowserOrigins, BrowserApproval: state.BrowserApproval}
}

// BrowserTabPatch changes a copy of a page in place (TS: Partial<BrowserTab>).
type BrowserTabPatch func(next *domain.BrowserTab)

func PatchBrowserTab(docks DocksEdit, owner, tabID string, patch BrowserTabPatch) DocksEdit {
	tabs := DocksDock(docks.View, owner).BrowserTabs
	next := make([]*domain.BrowserTab, len(tabs))
	for index, tab := range tabs {
		if tab.ID == tabID {
			patched := *tab
			patch(&patched)
			next[index] = &patched
		} else {
			next[index] = tab
		}
	}
	return EditDock(docks, owner, func(dock *ThreadDock) { dock.BrowserTabs = next })
}

// LoadBrowserPage loads the page, in the tab named or a new one. The origin is remembered when the
// user is the one asking. Only the user's own load takes the panel. `own` as in browserTabsCurrent;
// `tabID` and `taskID` may be nil.
func LoadBrowserPage(state *WorkspaceState, own *BrowserOwn, owner, url string, tabID *string, newTab, byUser bool, taskID *string) SliceTransition {
	origin, hasOrigin := domain.BrowserOrigin(url)
	before := browserTabsCurrent(state, own)
	allowing := byUser && hasOrigin && origin != "" && !slices.Contains(before.BrowserOrigins, origin)
	patched := before
	if allowing {
		patched.BrowserOrigins = append(slices.Clip(before.BrowserOrigins), origin)
	}
	docks := DocksOf(state)
	var target *domain.BrowserTab
	if !newTab {
		target = BrowserTarget(DocksDock(docks.View, owner), tabID)
	}
	var approvalTab, targetID *string
	if before.BrowserApproval != nil {
		approvalTab = before.BrowserApproval.TabID
	}
	if target != nil {
		targetID = &target.ID
	}
	if sameOptionalString(approvalTab, targetID) {
		patched.BrowserApproval = nil
	}
	patch := browserOwnPatch(&patched)
	settledWith := func(changed DocksEdit, effects func(after *WorkspaceState) []WorkspaceEffect) SliceTransition {
		transition := SliceTransition{Patch: patch, ClearsError: true, Edits: []OwnerEdit{changed}}
		transition.Effects = effects(Afterward(state, transition))
		return transition
	}
	if target != nil {
		surfaced := docks
		if byUser {
			surfaced = ShowDockTabIn(docks, owner, target.ID)
		}
		shown := EditDock(PatchBrowserTab(surfaced, owner, target.ID, func(tab *domain.BrowserTab) {
			tab.URL = url
			tab.Loading = true
			tab.Error = nil
		}), owner, func(dock *ThreadDock) { dock.BrowserTabID = new(target.ID) })
		navigating, focusEffects := shown, []WorkspaceEffect{}
		if byUser {
			navigating, focusEffects = FocusDockTabIn(shown, owner, target.ID)
		}
		return settledWith(navigating, func(after *WorkspaceState) []WorkspaceEffect {
			return slices.Concat([]WorkspaceEffect{WorkspaceEffectBrowserNavigate{TabID: target.ID, URL: url, TaskID: nonEmpty(taskID)}}, PersistView(after), focusEffects)
		})
	}
	tab := &domain.BrowserTab{ID: NewID(), URL: url, Title: "", Loading: true, CanGoBack: false, CanGoForward: false}
	surfaced := docks
	if byUser {
		surfaced = ShowDockTabIn(docks, owner, tab.ID)
	}
	tabs := append(slices.Clip(DocksDock(docks.View, owner).BrowserTabs), tab)
	shown := EditDock(surfaced, owner, func(dock *ThreadDock) {
		dock.BrowserTabs = tabs
		dock.BrowserTabID = new(tab.ID)
	})
	opened, focusEffects := shown, []WorkspaceEffect{}
	if byUser {
		opened, focusEffects = FocusDockTabIn(shown, owner, tab.ID)
	}
	return settledWith(opened, func(after *WorkspaceState) []WorkspaceEffect {
		effects := []WorkspaceEffect{WorkspaceEffectBrowserOpen{TabID: tab.ID, URL: new(url), TaskID: nonEmpty(taskID)}}
		// The panel draws one page, so a tab nobody is looking at never claims it.
		if byUser {
			effects = append(effects, WorkspaceEffectBrowserShow{TabID: new(tab.ID)})
		}
		return slices.Concat(effects, PersistView(after), focusEffects)
	})
}

// WithBlankTab is a page waiting for an address. It is a dock tab of its own from the moment it exists.
func WithBlankTab(docks DocksEdit, owner string) (DocksEdit, *domain.BrowserTab) {
	tab := &domain.BrowserTab{ID: NewID(), URL: "", Title: "", Loading: false, CanGoBack: false, CanGoForward: false}
	tabs := append(slices.Clip(DocksDock(docks.View, owner).BrowserTabs), tab)
	opened := EditDock(ShowDockTabIn(docks, owner, tab.ID), owner, func(dock *ThreadDock) {
		dock.BrowserTabs = tabs
		dock.BrowserTabID = new(tab.ID)
	})
	return opened, tab
}

// AskToBrowse puts the navigation to the user, in the tab it would load in. `tabID` may be nil.
func AskToBrowse(state *WorkspaceState, owner, url, taskID string, tabID *string, newTab bool) SliceTransition {
	if state.BrowserApproval != nil {
		return SliceTransition{Refusal: new("A site is already waiting for approval. Wait for that decision before opening another site.")}
	}
	approvalID := NewID()
	docks := DocksOf(state)
	var target *domain.BrowserTab
	if !newTab {
		target = BrowserTarget(DocksDock(docks.View, owner), tabID)
	}
	if target != nil {
		approval := &domain.BrowserApproval{ApprovalID: approvalID, URL: url, TaskID: taskID, TabID: new(target.ID)}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.BrowserApproval = approval }, Edits: []OwnerEdit{ShowDockTabIn(docks, owner, target.ID)}}
	}
	opened, tab := WithBlankTab(docks, owner)
	approval := &domain.BrowserApproval{ApprovalID: approvalID, URL: url, TaskID: taskID, TabID: new(tab.ID)}
	return SliceTransition{
		Patch:   func(next *WorkspaceState) { next.BrowserApproval = approval },
		Edits:   []OwnerEdit{opened},
		Effects: []WorkspaceEffect{WorkspaceEffectBrowserOpen{TabID: tab.ID}, WorkspaceEffectBrowserShow{TabID: new(tab.ID)}},
	}
}

// CloseBrowserTab: closing a page hands the dock its neighbour, and the panel whichever page that
// turns out to be. `own` as in browserTabsCurrent; `tabID` may be nil.
func CloseBrowserTab(state *WorkspaceState, own *BrowserOwn, owner string, tabID *string, onlyIfBlank bool) SliceTransition {
	dock := DockFor(state, owner)
	index := -1
	if tabID != nil {
		index = slices.IndexFunc(dock.BrowserTabs, func(tab *domain.BrowserTab) bool { return tab.ID == *tabID })
	}
	if index == -1 || (onlyIfBlank && dock.BrowserTabs[index].URL != "") {
		return SliceTransition{Patch: browserOwnPatch(own)}
	}
	tab := dock.Tab
	if dock.Tab == *tabID {
		tab = DockTabAfterClosing(state, owner, *tabID)
	}
	browserTabs := slices.DeleteFunc(slices.Clone(dock.BrowserTabs), func(page *domain.BrowserTab) bool { return page.ID == *tabID })
	var next *domain.BrowserTab
	if dock.BrowserTabID != nil && *dock.BrowserTabID == *tabID {
		if index-1 >= 0 && index-1 < len(browserTabs) {
			next = browserTabs[index-1]
		} else if index < len(browserTabs) {
			next = browserTabs[index]
		}
	} else if dock.BrowserTabID != nil {
		if found := slices.IndexFunc(browserTabs, func(page *domain.BrowserTab) bool { return page.ID == *dock.BrowserTabID }); found >= 0 {
			next = browserTabs[found]
		}
	}
	patched := own
	if approval := browserTabsCurrent(state, own).BrowserApproval; approval != nil && approval.TabID != nil && *approval.TabID == *tabID {
		cleared := browserTabsCurrent(state, own)
		cleared.BrowserApproval = nil
		patched = &cleared
	}
	var nextID *string
	if next != nil {
		nextID = new(next.ID)
	}
	docks := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
		dock.BrowserTabs = browserTabs
		dock.BrowserTabID = nextID
		dock.Tab = tab
	})
	// Only the dock on screen owns the panel, so closing a page in a dock behind it changes nothing there.
	shows := []WorkspaceEffect{}
	if owner == DockOwner(state, nil) {
		if slices.ContainsFunc(browserTabs, func(page *domain.BrowserTab) bool { return page.ID == tab }) {
			shows = browserEffectsIn(DocksDock(docks.View, owner), tab)
		} else {
			shows = []WorkspaceEffect{WorkspaceEffectBrowserShow{TabID: nil}}
		}
	}
	patch := browserOwnPatch(patched)
	return SliceTransition{
		Patch: patch,
		Edits: []OwnerEdit{docks},
		Effects: slices.Concat(
			[]WorkspaceEffect{WorkspaceEffectBrowserClose{TabID: *tabID}},
			shows,
			PersistView(Afterward(state, SliceTransition{Patch: patch, Edits: []OwnerEdit{docks}})),
		),
	}
}

// BrowserTabClosed is CloseBrowserTab of the user's own accord, which any slice can carry.
func BrowserTabClosed(state *WorkspaceState, owner, tabID string) Step {
	closed := closeBrowserTabOwn(state, nil, owner, &tabID, false)
	step := Step(closed.transition)
	step.Patch = nil
	if closed.own != nil {
		step.Edits = slices.Concat([]OwnerEdit{BrowserEdit{View: *closed.own}}, step.Edits)
	}
	return step
}

// BrowserAllowed is whether a run may load this page without asking.
func BrowserAllowed(state *WorkspaceState, taskID, url string) bool {
	if origin, ok := domain.BrowserOrigin(url); ok && origin != "" && slices.Contains(state.BrowserOrigins, origin) {
		return true
	}
	for _, thread := range state.Threads {
		if thread.ID == taskID {
			return thread.ExecutionPolicy == domain.ExecutionPolicyAutonomous
		}
	}
	return false
}

// BrowserEffectsForTab: bringing a page to the front is what gives a restored one its view, and only
// then. Reads only state.Docks.
func BrowserEffectsForTab(state *WorkspaceState, owner, dockTab string) []WorkspaceEffect {
	return browserEffectsIn(DockFor(state, owner), dockTab)
}

// browserEffectsIn is BrowserEffectsForTab on one dock.
func browserEffectsIn(dock *ThreadDock, dockTab string) []WorkspaceEffect {
	for _, page := range dock.BrowserTabs {
		if page.ID != dockTab {
			continue
		}
		open := WorkspaceEffectBrowserOpen{TabID: page.ID}
		if page.URL != "" {
			open.URL = new(page.URL)
		}
		return []WorkspaceEffect{open, WorkspaceEffectBrowserShow{TabID: new(page.ID)}}
	}
	return []WorkspaceEffect{}
}

// ShownPageEffects is which page the panel draws once the dock on screen changes.
func ShownPageEffects(state *WorkspaceState) []WorkspaceEffect {
	owner, dock := FrontDock(state)
	if effects := BrowserEffectsForTab(state, owner, dock.Tab); len(effects) > 0 {
		return effects
	}
	return []WorkspaceEffect{WorkspaceEffectBrowserShow{TabID: nil}}
}

// DocksDock is DockFor on the docks an input is changing.
func DocksDock(view DocksState, owner string) *ThreadDock {
	if dock := view.Docks[owner]; dock != nil {
		return dock
	}
	return EmptyDock
}

// browserOwnPatch writes the browser's own fields as `own` has them, or nothing for nil.
func browserOwnPatch(own *BrowserOwn) OwnPatch {
	if own == nil {
		return nil
	}
	held := *own
	return func(next *WorkspaceState) {
		next.BrowserOrigins = held.BrowserOrigins
		next.BrowserApproval = held.BrowserApproval
	}
}

// closedBrowserTab is CloseBrowserTab's answer with the browser's own fields it patches kept apart.
type closedBrowserTab struct {
	transition SliceTransition
	own        *BrowserOwn
}

func closeBrowserTabOwn(state *WorkspaceState, own *BrowserOwn, owner string, tabID *string, onlyIfBlank bool) closedBrowserTab {
	transition := CloseBrowserTab(state, own, owner, tabID, onlyIfBlank)
	if transition.Patch == nil {
		return closedBrowserTab{transition: transition}
	}
	patched := browserTabsCurrent(Afterward(state, SliceTransition{Patch: transition.Patch}), nil)
	return closedBrowserTab{transition: transition, own: &patched}
}

func sameOptionalString(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// nonEmpty is value, or nil for a nil or empty one (TS: `...(value ? { value } : {})`).
func nonEmpty(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}
