package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// BrowserImportInputs are the input types ReduceBrowserImport takes.
var BrowserImportInputs = []string{
	"browser-import.read", "browser-import.sources", "browser-import.choose", "browser-import.sites", "browser-import.toggle",
	"browser-import.filter", "browser-import.run", "browser-import.again", "browser-import.done", "browser-import.failed",
}

// patch writes state.BrowserImport with `change` made to a copy of it.
func patch(state *WorkspaceState, change func(next *BrowserImportState)) OwnPatch {
	next := state.BrowserImport
	change(&next)
	return func(state *WorkspaceState) { state.BrowserImport = next }
}

// ReduceBrowserImport takes an input whose type is one of BrowserImportInputs.
func ReduceBrowserImport(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	current := state.BrowserImport
	picker := current.Picker
	switch input := input.(type) {
	case contracts.BrowserImportCommandBrowserImportRead:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectBrowserImportListSources{}}}

	case BrowserImportEventBrowserImportSources:
		var kept *BrowserImportPicker
		if picker != nil && slices.ContainsFunc(input.Sources, func(source *domain.BrowserImportSource) bool { return source.ID == picker.SourceID }) {
			kept = picker
		}
		sources := jsonx.NullSlice[*domain.BrowserImportSource](input.Sources)
		if sources == nil {
			sources = jsonx.NullSlice[*domain.BrowserImportSource]{}
		}
		return SliceTransition{Patch: patch(state, func(next *BrowserImportState) {
			next.Sources = sources
			next.Picker = kept
		})}

	case contracts.BrowserImportCommandBrowserImportChoose:
		if input.SourceID == nil {
			return SliceTransition{Patch: patch(state, func(next *BrowserImportState) { next.Picker = nil })}
		}
		sourceID := *input.SourceID
		if !slices.ContainsFunc(current.Sources, func(source *domain.BrowserImportSource) bool { return source.ID == sourceID }) {
			return SliceTransition{}
		}
		selected := []string{}
		if current.Last != nil && current.Last.SourceID == sourceID {
			selected = current.Last.Sites
		}
		return SliceTransition{
			Patch: patch(state, func(next *BrowserImportState) {
				next.Picker = &BrowserImportPicker{SourceID: sourceID, Sites: nil, Selected: selected, Filter: ""}
				next.Notice = nil
				next.Error = nil
			}),
			Effects: []WorkspaceEffect{WorkspaceEffectBrowserImportListSites{SourceID: sourceID}},
		}

	case BrowserImportEventBrowserImportSites:
		if picker == nil || picker.SourceID != input.SourceID {
			return SliceTransition{}
		}
		offered := jsonx.Set[string]{}
		for _, site := range input.Sites {
			offered[site.Domain] = struct{}{}
		}
		sites := jsonx.NullSlice[*domain.BrowserImportSite](input.Sites)
		if sites == nil {
			sites = jsonx.NullSlice[*domain.BrowserImportSite]{}
		}
		next := *picker
		next.Sites = sites
		next.Selected = []string{}
		for _, site := range picker.Selected {
			if offered.Has(site) {
				next.Selected = append(next.Selected, site)
			}
		}
		return SliceTransition{Patch: patch(state, func(state *BrowserImportState) { state.Picker = &next })}

	case contracts.BrowserImportCommandBrowserImportToggle:
		if picker == nil || !slices.ContainsFunc(picker.Sites, func(site *domain.BrowserImportSite) bool { return site.Domain == input.Site }) {
			return SliceTransition{}
		}
		on := slices.Contains(picker.Selected, input.Site)
		if !on && len(picker.Selected) >= domain.MaxImportSites {
			return SliceTransition{}
		}
		next := *picker
		if on {
			next.Selected = slices.DeleteFunc(slices.Clone(picker.Selected), func(site string) bool { return site == input.Site })
		} else {
			next.Selected = append(slices.Clip(picker.Selected), input.Site)
		}
		return SliceTransition{Patch: patch(state, func(state *BrowserImportState) { state.Picker = &next })}

	case contracts.BrowserImportCommandBrowserImportFilter:
		if picker == nil {
			return SliceTransition{}
		}
		next := *picker
		next.Filter = input.Text
		return SliceTransition{Patch: patch(state, func(state *BrowserImportState) { state.Picker = &next })}

	case contracts.BrowserImportCommandBrowserImportRun, contracts.BrowserImportCommandBrowserImportAgain:
		var target *domain.BrowserImportMemory
		if _, run := input.(contracts.BrowserImportCommandBrowserImportRun); run {
			if picker != nil {
				target = &domain.BrowserImportMemory{SourceID: picker.SourceID, Sites: picker.Selected}
			}
		} else {
			target = current.Last
		}
		if current.Importing || target == nil || len(target.Sites) == 0 {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch: patch(state, func(next *BrowserImportState) {
				next.Importing = true
				next.Notice = nil
				next.Error = nil
			}),
			Effects: []WorkspaceEffect{WorkspaceEffectBrowserImportImport{SourceID: target.SourceID, Sites: target.Sites}},
		}

	case BrowserImportEventBrowserImportDone:
		sources := []*domain.BrowserImportSource(current.Sources)
		var source *domain.BrowserImportSource
		if index := slices.IndexFunc(sources, func(source *domain.BrowserImportSource) bool { return source.ID == input.SourceID }); index >= 0 {
			source = sources[index]
		}
		from := SourceName(source, sources)
		failed := input.Result.Imported == 0
		done := patch(state, func(next *BrowserImportState) {
			next.Importing = false
			if failed {
				next.Picker = picker
				next.Notice = nil
				next.Error = new("No cookies could be copied from " + from + ".")
				next.Last = current.Last
			} else {
				next.Picker = nil
				next.Notice = new(ImportNotice(input.Result, input.Sites, from))
				next.Error = nil
				next.Last = &domain.BrowserImportMemory{SourceID: input.SourceID, Sites: input.Sites}
			}
		})
		if failed {
			return SliceTransition{Patch: done}
		}
		return SliceTransition{Patch: done, Persists: true}

	case BrowserImportEventBrowserImportFailed:
		return SliceTransition{Patch: patch(state, func(next *BrowserImportState) {
			next.Importing = false
			// A list that never arrived leaves nothing to pick from.
			if picker != nil && picker.Sites == nil {
				next.Picker = nil
			} else {
				next.Picker = picker
			}
			next.Error = new(input.Message)
		})}
	}
	return SliceTransition{}
}
