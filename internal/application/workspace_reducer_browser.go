package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// BrowserInputs are the input types ReduceBrowser takes.
var BrowserInputs = []string{
	"browser.open", "browser.new-tab", "browser.decide", "browser.select-tab", "browser.close-tab", "browser.go", "browser.reload", "browser.act",
	"browser.clear-data", "browser.updated",
}

// NewBrowserTab is a blank page in the dock in front, shown and holding the keys.
func NewBrowserTab(state *WorkspaceState) Step {
	owner := DockOwner(state, nil)
	opened, tab := WithBlankTab(DocksOf(state), owner)
	focused, effects := FocusDockTabIn(opened, owner, tab.ID)
	return Step{
		Edits:   []OwnerEdit{focused},
		Effects: slices.Concat([]WorkspaceEffect{WorkspaceEffectBrowserOpen{TabID: tab.ID}, WorkspaceEffectBrowserShow{TabID: new(tab.ID)}}, effects),
	}
}

// ReduceBrowser takes an input whose type is one of BrowserInputs.
func ReduceBrowser(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.BrowserCommandBrowserOpen:
		owner := DockOwner(state, input.TaskID)
		url, ok := domain.BrowserURL(input.URL)
		if !ok || url == "" {
			return SliceTransition{Refusal: new(BrowserURLError)}
		}
		newTab := input.NewTab != nil && *input.NewTab
		byUser := input.TaskID == nil
		if !byUser && !BrowserAllowed(state, *input.TaskID, url) {
			return AskToBrowse(state, owner, url, *input.TaskID, input.TabID, newTab)
		}
		return LoadBrowserPage(state, nil, owner, url, input.TabID, newTab, byUser, input.TaskID)

	case contracts.BrowserCommandBrowserNewTab:
		return SliceTransition(NewBrowserTab(state))

	case contracts.BrowserCommandBrowserDecide:
		approval := state.BrowserApproval
		if approval == nil || approval.ApprovalID != input.ApprovalID {
			return SliceTransition{}
		}
		owner := DockOwner(state, &approval.TaskID)
		// A blank tab only existed to carry the ask, so blocking takes it away again.
		if !input.Allow {
			return CloseBrowserTab(state, &BrowserOwn{BrowserOrigins: state.BrowserOrigins, BrowserApproval: nil}, owner, approval.TabID, true)
		}
		var allowed *BrowserOwn
		if origin, ok := domain.BrowserOrigin(approval.URL); ok && origin != "" {
			allowed = &BrowserOwn{BrowserOrigins: append(slices.Clip(state.BrowserOrigins), origin), BrowserApproval: state.BrowserApproval}
		}
		return LoadBrowserPage(state, allowed, owner, approval.URL, approval.TabID, approval.TabID == nil, false, &approval.TaskID)

	case contracts.BrowserCommandBrowserSelectTab:
		owner, ok := OwnerOfBrowserTab(state, input.TabID)
		if !ok {
			owner = DockOwner(state, input.TaskID)
		}
		if !slices.ContainsFunc(DockFor(state, owner).BrowserTabs, func(item *domain.BrowserTab) bool { return item.ID == input.TabID }) {
			return SliceTransition{}
		}
		tabID := input.TabID
		return SliceTransition{
			Edits:   []OwnerEdit{EditDock(ShowDockTabIn(DocksOf(state), owner, tabID), owner, func(dock *ThreadDock) { dock.BrowserTabID = new(tabID) })},
			Effects: BrowserEffectsForTab(state, owner, tabID),
		}

	case contracts.BrowserCommandBrowserCloseTab:
		owner, ok := OwnerOfBrowserTab(state, input.TabID)
		if !ok {
			owner = DockOwner(state, input.TaskID)
		}
		return CloseBrowserTab(state, nil, owner, new(input.TabID), false)

	case contracts.BrowserCommandBrowserGo:
		return browserTargeted(state, input.TabID, input.TaskID, func(target *domain.BrowserTab) WorkspaceEffect {
			return WorkspaceEffectBrowserHistory{TabID: target.ID, Delta: input.Delta, TaskID: input.TaskID}
		})
	case contracts.BrowserCommandBrowserReload:
		return browserTargeted(state, input.TabID, input.TaskID, func(target *domain.BrowserTab) WorkspaceEffect {
			return WorkspaceEffectBrowserReload{TabID: target.ID, TaskID: input.TaskID}
		})
	case contracts.BrowserCommandBrowserAct:
		owner := browserCommandOwner(state, input.TabID, input.TaskID)
		target := BrowserTarget(DockFor(state, owner), input.TabID)
		if target == nil {
			return SliceTransition{Refusal: new(BrowserTabError)}
		}
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectBrowserAct{TabID: target.ID, Action: input.Action, TaskID: input.TaskID}}}

	case contracts.BrowserCommandBrowserClearData:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.BrowserOrigins = jsonx.Fresh[string](0) }, Effects: []WorkspaceEffect{WorkspaceEffectBrowserClearData{}}, Persists: true}

	case BrowserEvent:
		page := input.Page
		owner, ok := OwnerOfBrowserTab(state, page.TabID)
		if !ok {
			return SliceTransition{}
		}
		index := slices.IndexFunc(DockFor(state, owner).BrowserTabs, func(tab *domain.BrowserTab) bool { return tab.ID == page.TabID })
		if index < 0 {
			return SliceTransition{}
		}
		current := DockFor(state, owner).BrowserTabs[index]
		if request := page.NavigationRequest; request != nil && owner == DockOwner(state, &request.TaskID) {
			return AskToBrowse(state, owner, request.URL, request.TaskID, new(page.TabID), false)
		}
		// Landing on a different page clears the error the page before it left behind.
		clearing := current.Error != nil && page.Error == nil && page.URL != nil && *page.URL != current.URL
		docks := PatchBrowserTab(DocksOf(state), owner, page.TabID, func(tab *domain.BrowserTab) {
			if page.URL != nil {
				tab.URL = *page.URL
			}
			if page.Title != nil {
				tab.Title = *page.Title
			}
			if page.Loading != nil {
				tab.Loading = *page.Loading
			}
			if page.CanGoBack != nil {
				tab.CanGoBack = *page.CanGoBack
			}
			if page.CanGoForward != nil {
				tab.CanGoForward = *page.CanGoForward
			}
			if page.Error != nil {
				tab.Error = page.Error
			}
			if clearing {
				tab.Error = nil
			}
		})
		effects := []WorkspaceEffect{}
		if page.URL != nil {
			effects = PersistView(Afterward(state, SliceTransition{Edits: []OwnerEdit{docks}}))
		}
		return SliceTransition{Edits: []OwnerEdit{docks}, Effects: effects}
	}
	return SliceTransition{}
}

// browserCommandOwner is the dock a page command acts in: the one holding the page it names, else
// the asking thread's.
func browserCommandOwner(state *WorkspaceState, tabID, taskID *string) string {
	if tabID != nil && *tabID != "" {
		if owner, ok := OwnerOfBrowserTab(state, *tabID); ok {
			return owner
		}
	}
	return DockOwner(state, taskID)
}

// browserTargeted moves a page through its history or reloads it, loading until it says otherwise.
func browserTargeted(state *WorkspaceState, tabID, taskID *string, effect func(target *domain.BrowserTab) WorkspaceEffect) SliceTransition {
	owner := browserCommandOwner(state, tabID, taskID)
	target := BrowserTarget(DockFor(state, owner), tabID)
	if target == nil {
		return SliceTransition{Refusal: new(BrowserTabError)}
	}
	return SliceTransition{
		Edits: []OwnerEdit{PatchBrowserTab(DocksOf(state), owner, target.ID, func(tab *domain.BrowserTab) {
			tab.Loading = true
			tab.Error = nil
		})},
		Effects: []WorkspaceEffect{effect(target)},
	}
}
