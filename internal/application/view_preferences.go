package application

import (
	"bytes"
	"encoding/json/jsontext"
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	ViewPreferencesKey = "aicodingtool.view-preferences.v1"
	DraftPromptsKey    = "aicodingtool.draft-prompts.v1"
)

const maxRemembered = 50

// StoredViewPreferences is Partial<ViewPreferences>, what readViewPreferences found: nil is no preference.
type StoredViewPreferences struct {
	FavoriteModels         []*domain.EngineModel       `json:"favoriteModels,omitzero"`
	Theme                  *string                     `json:"theme,omitzero"`
	ThemeMode              *domain.ThemeMode           `json:"themeMode,omitzero"`
	UIFont                 *string                     `json:"uiFont,omitzero"`
	MonoFont               *string                     `json:"monoFont,omitzero"`
	ReadingSize            *float64                    `json:"readingSize,omitzero"`
	TerminalSize           *float64                    `json:"terminalSize,omitzero"`
	SessionPanelOpen       *bool                       `json:"sessionPanelOpen,omitzero"`
	SidebarOpen            *bool                       `json:"sidebarOpen,omitzero"`
	CaptureSound           *bool                       `json:"captureSound,omitzero"`
	CaptureFocus           *bool                       `json:"captureFocus,omitzero"`
	ActivityProject        *string                     `json:"activityProject,omitzero"`
	SimulatorDevice        *string                     `json:"simulatorDevice,omitzero"`
	Sections               *domain.SidebarSections     `json:"sections,omitzero"`
	SubagentGroups         *domain.SubagentGroups      `json:"subagentGroups,omitzero"`
	ChromeBrowser          *bool                       `json:"chromeBrowser,omitzero"`
	ComputerUse            *bool                       `json:"computerUse,omitzero"`
	BrowserTools           *bool                       `json:"browserTools,omitzero"`
	MemoryProtection       *bool                       `json:"memoryProtection,omitzero"`
	Notifications          *bool                       `json:"notifications,omitzero"`
	Shortcuts              domain.ShortcutOverrides    `json:"shortcuts,omitzero"`
	BrowserTabs            map[string][]string         `json:"browserTabs,omitzero"`
	BrowserOrigins         []string                    `json:"browserOrigins,omitzero"`
	BrowserImport          *domain.BrowserImportMemory `json:"browserImport,omitzero"`
	DismissedToasts        []string                    `json:"dismissedToasts,omitzero"`
	DismissedUsageWarnings []string                    `json:"dismissedUsageWarnings,omitzero"`
	RecentProjects         []string                    `json:"recentProjects,omitzero"`
	DiscardedLeads         []string                    `json:"discardedLeads,omitzero"`
}

// The reading helpers take values decoded into `any` (map[string]any, []any, string, float64, bool, nil);
// ok false is TypeScript's undefined.

func urlList(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	urls := []string{}
	for _, item := range items {
		if url, ok := item.(string); ok && url != "" && prefsUTF16Len(url) <= 8_192 {
			urls = append(urls, url)
		}
	}
	return urls[:min(len(urls), maxRemembered)], true
}

// urlsByThread is the pages a thread's dock reopens. A thread whose entry is unreadable simply reopens none.
func urlsByThread(value jsontext.Value) (map[string][]string, bool) {
	entries, ok := objectEntries(value)
	if !ok {
		return nil, false
	}
	threads := map[string][]string{}
	for _, entry := range entries[:min(len(entries), maxRemembered)] {
		var urls any
		if jsonx.Unmarshal(entry.value, &urls) != nil {
			continue
		}
		if list, ok := urlList(urls); ok && len(list) > 0 {
			threads[entry.key] = list
		}
	}
	return threads, true
}

// subagentGroups drops a group the app no longer draws, and one that is missing takes its default of unfolded.
func subagentGroups(value any) (domain.SubagentGroups, bool) {
	stored, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	groups := maps.Clone(domain.OpenSubagentGroups)
	for _, group := range domain.RunSubagentGroups {
		if open, ok := stored[string(group)].(bool); ok {
			groups[group] = open
		}
	}
	return groups, true
}

// sidebarSections drops a list the app no longer draws, and one that is missing takes its default of unfolded.
func sidebarSections(value any) (domain.SidebarSections, bool) {
	stored, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	sections := maps.Clone(domain.OpenSidebarSections)
	for _, section := range domain.SidebarSidebarSections {
		if open, ok := stored[string(section)].(bool); ok {
			sections[section] = open
		}
	}
	return sections, true
}

// bindings drops a binding the app no longer knows, or one it would refuse to record.
func bindings(value any) (domain.ShortcutOverrides, bool) {
	stored, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	kept := domain.ShortcutOverrides{}
	for action, binding := range stored {
		if domain.ShortcutsShortcutAction(action) == nil {
			continue
		}
		switch binding := binding.(type) {
		case nil:
			kept[action] = nil
		case string:
			if _, refused := domain.ShortcutProblem(binding); !refused {
				kept[action] = &binding
			}
		}
	}
	return domain.ShortcutsShortcutOverrides(kept), true
}

// favoriteModels is the pinned models, each on its engine. One saved as a bare id is read on the engine that offers it.
func favoriteModels(value any) []*domain.EngineModel {
	entries, ok := value.([]any)
	if !ok {
		return []*domain.EngineModel{}
	}
	pinned := []*domain.EngineModel{}
	at := map[string]int{}
	for _, entry := range entries {
		var saved map[string]any
		switch entry := entry.(type) {
		case string:
			saved = map[string]any{"model": entry}
		case map[string]any:
			saved = entry
		}
		model := domain.CurrentModel(saved["model"])
		if !domain.IsModelID(model) {
			continue
		}
		id := domain.AgentModel(model.(string))
		var engine any
		if held, named := saved["engine"]; named {
			engine = held
		} else if found, ok := domain.SavedModelEngine(id); ok {
			engine = string(found)
		}
		if !domain.IsAgentEngine(engine) {
			continue
		}
		name, _ := engine.(string)
		pin := &domain.EngineModel{Engine: domain.AgentEngine(name), Model: id}
		key := name + "\x00" + string(id)
		if index, seen := at[key]; seen {
			pinned[index] = pin
		} else {
			at[key] = len(pinned)
			pinned = append(pinned, pin)
		}
	}
	return pinned
}

// ReadViewPreferences reports anything unreadable as no preference, so the caller's default decides.
func ReadViewPreferences(storage KeyValueStorage) StoredViewPreferences {
	var preferences StoredViewPreferences
	if _, failed := catching(func() { preferences = readStoredPreferences(storage) }); failed {
		return StoredViewPreferences{}
	}
	return preferences
}

// WriteViewPreferences: a refused write costs the preference and nothing else.
func WriteViewPreferences(storage KeyValueStorage, preferences contracts.ViewPreferences) {
	catching(func() {
		encoded, err := jsonx.Marshal(preferences)
		if err != nil {
			return
		}
		storage.SetItem(ViewPreferencesKey, string(encoded))
	})
}

// ViewPreferences is the slice of state that survives a restart.
func ViewPreferences(state *WorkspaceState) contracts.ViewPreferences {
	browserTabs := map[string][]string{}
	threadIDs := make(map[string]bool, len(state.Threads))
	for _, thread := range state.Threads {
		threadIDs[thread.ID] = true
	}
	for owner, dock := range state.Docks {
		if owner != DraftDock && !threadIDs[owner] {
			continue
		}
		urls := []string{}
		for _, tab := range dock.BrowserTabs {
			if tab.URL != "" {
				urls = append(urls, tab.URL)
			}
		}
		if len(urls) > 0 {
			browserTabs[owner] = urls
		}
	}
	memoryProtection := state.MemoryProtection
	preferences := contracts.ViewPreferences{
		Theme:            state.Theme,
		ThemeMode:        state.ThemeMode,
		UIFont:           state.UIFont,
		MonoFont:         state.MonoFont,
		ReadingSize:      state.ReadingSize,
		TerminalSize:     state.TerminalSize,
		SessionPanelOpen: state.SessionPanelOpen,
		CaptureSound:     state.CaptureSound,
		CaptureFocus:     state.CaptureFocus,
		ChromeBrowser:    state.ChromeBrowser,
		ComputerUse:      state.ComputerUse,
		MemoryProtection: &memoryProtection,
		BrowserTools:     state.BrowserTools,
		Notifications:    state.Notifications,
		SidebarOpen:      state.SidebarOpen,
		Sections:         state.Sections,
		SubagentGroups:   state.SubagentGroups,
		FavoriteModels:   state.FavoriteModels,
		Shortcuts:        state.Shortcuts,
		BrowserTabs:      browserTabs,
		BrowserOrigins:   state.BrowserOrigins,
		BrowserImport:    state.BrowserImport.Last,
	}
	if state.ActivityProject != nil && *state.ActivityProject != "" {
		preferences.ActivityProject = state.ActivityProject
	}
	if state.SimulatorDevice != nil && *state.SimulatorDevice != "" {
		preferences.SimulatorDevice = state.SimulatorDevice
	}
	if len(state.DismissedToasts) > 0 {
		preferences.DismissedToasts = state.DismissedToasts
	}
	if len(state.PlanUsage.Dismissed) > 0 {
		preferences.DismissedUsageWarnings = state.PlanUsage.Dismissed
	}
	if len(state.Computers.DiscardedLeads) > 0 {
		preferences.DiscardedLeads = state.Computers.DiscardedLeads
	}
	if len(state.RecentProjects) > 0 {
		preferences.RecentProjects = state.RecentProjects
	}
	return preferences
}

// ViewPreferenceState writes the same slice on the way back in into next, a state copy the caller
// owns. A preference the stored set lacks takes the app's own default.
func ViewPreferenceState(next *WorkspaceState, preferences *contracts.ViewPreferences) {
	orEmpty := func(values []string) []string {
		if values == nil {
			return []string{}
		}
		return values
	}
	next.Theme = domain.ThemeOrDefault(preferences.Theme).ID
	next.ThemeMode = domain.ThemeModeOrDefault(preferences.ThemeMode)
	next.UIFont = domain.UiFontOrDefault(preferences.UIFont).ID
	next.MonoFont = domain.MonoFontOrDefault(preferences.MonoFont).ID
	next.ReadingSize = domain.SizeOrDefault(domain.ReadingSize, preferences.ReadingSize)
	next.TerminalSize = domain.SizeOrDefault(domain.TerminalSize, preferences.TerminalSize)
	next.SessionPanelOpen = preferences.SessionPanelOpen
	next.CaptureSound = preferences.CaptureSound
	next.CaptureFocus = preferences.CaptureFocus
	next.ChromeBrowser = preferences.ChromeBrowser
	next.ComputerUse = preferences.ComputerUse
	next.MemoryProtection = preferences.MemoryProtection == nil || *preferences.MemoryProtection
	next.BrowserTools = preferences.BrowserTools
	next.Notifications = preferences.Notifications
	next.SidebarOpen = preferences.SidebarOpen
	next.ActivityProject = preferences.ActivityProject
	next.SimulatorDevice = preferences.SimulatorDevice
	next.Sections = preferences.Sections
	if next.Sections == nil {
		next.Sections = domain.OpenSidebarSections
	}
	next.SubagentGroups = preferences.SubagentGroups
	if next.SubagentGroups == nil {
		next.SubagentGroups = domain.OpenSubagentGroups
	}
	next.FavoriteModels = preferences.FavoriteModels
	if next.FavoriteModels == nil {
		next.FavoriteModels = []*domain.EngineModel{}
	}
	next.Shortcuts = preferences.Shortcuts
	if next.Shortcuts == nil {
		next.Shortcuts = domain.ShortcutOverrides{}
	}
	next.BrowserOrigins = orEmpty(preferences.BrowserOrigins)
	next.DismissedToasts = orEmpty(preferences.DismissedToasts)
	next.RecentProjects = orEmpty(preferences.RecentProjects)
}

func readStoredPreferences(storage KeyValueStorage) StoredViewPreferences {
	raw := storage.GetItem(ViewPreferencesKey)
	if raw == nil {
		return StoredViewPreferences{}
	}
	var fields map[string]jsontext.Value
	if err := jsonx.Unmarshal([]byte(*raw), &fields); err != nil || fields == nil {
		return StoredViewPreferences{}
	}
	value := func(name string) any {
		held, ok := fields[name]
		if !ok {
			return nil
		}
		var decoded any
		if jsonx.Unmarshal(held, &decoded) != nil {
			return nil
		}
		return decoded
	}
	flag := func(name string) *bool {
		if held, ok := value(name).(bool); ok {
			return &held
		}
		return nil
	}
	text := func(name string) *string {
		if held, ok := value(name).(string); ok && held != "" {
			return &held
		}
		return nil
	}
	keys := func(name string, limit int, last bool) ([]string, bool) {
		items, ok := value(name).([]any)
		if !ok {
			return nil, false
		}
		kept := []string{}
		for _, item := range items {
			if key, ok := item.(string); ok && key != "" && prefsUTF16Len(key) <= 256 {
				kept = append(kept, key)
			}
		}
		if last {
			return kept[max(0, len(kept)-limit):], true
		}
		return kept[:min(len(kept), limit)], true
	}

	stored := StoredViewPreferences{FavoriteModels: favoriteModels(value("favoriteModels"))}
	if theme := value("theme"); domain.ThemeByID(theme) != nil {
		id := theme.(string)
		stored.Theme = &id
	}
	if mode := value("themeMode"); domain.IsThemeMode(mode) {
		themeMode := domain.ThemeModeOrDefault(mode)
		stored.ThemeMode = &themeMode
	}
	if font := value("uiFont"); domain.UiFontByID(font) != nil {
		id := font.(string)
		stored.UIFont = &id
	}
	if font := value("monoFont"); domain.MonoFontByID(font) != nil {
		id := font.(string)
		stored.MonoFont = &id
	}
	if size, ok := domain.SizeByID(domain.ReadingSize, value("readingSize")); ok {
		stored.ReadingSize = &size
	}
	if size, ok := domain.SizeByID(domain.TerminalSize, value("terminalSize")); ok {
		stored.TerminalSize = &size
	}
	stored.SessionPanelOpen = flag("sessionPanelOpen")
	stored.CaptureSound = flag("captureSound")
	stored.ChromeBrowser = flag("chromeBrowser")
	stored.ComputerUse = flag("computerUse")
	stored.MemoryProtection = flag("memoryProtection")
	stored.BrowserTools = flag("browserTools")
	stored.Notifications = flag("notifications")
	stored.CaptureFocus = flag("captureFocus")
	stored.SidebarOpen = flag("sidebarOpen")
	stored.ActivityProject = text("activityProject")
	stored.SimulatorDevice = text("simulatorDevice")
	if folds, ok := sidebarSections(value("sections")); ok {
		stored.Sections = &folds
	}
	if groups, ok := subagentGroups(value("subagentGroups")); ok {
		stored.SubagentGroups = &groups
	}
	if shortcuts, ok := bindings(value("shortcuts")); ok {
		stored.Shortcuts = shortcuts
	}
	if tabs, ok := urlsByThread(fields["browserTabs"]); ok {
		stored.BrowserTabs = tabs
	}
	if origins, ok := urlList(value("browserOrigins")); ok {
		stored.BrowserOrigins = origins
	}
	if memory := value("browserImport"); domain.IsBrowserImportMemory(memory) {
		var decoded domain.BrowserImportMemory
		if jsonx.Unmarshal(fields["browserImport"], &decoded) == nil {
			stored.BrowserImport = &decoded
		}
	}
	if dismissed, ok := keys("dismissedToasts", domain.MaxRememberedToasts, true); ok {
		stored.DismissedToasts = dismissed
	}
	if warnings, _ := keys("dismissedUsageWarnings", domain.MaxDismissedWarnings, true); len(warnings) > 0 {
		stored.DismissedUsageWarnings = warnings
	}
	if leads, _ := keys("discardedLeads", MaxDiscardedLeads, true); len(leads) > 0 {
		stored.DiscardedLeads = leads
	}
	if items, ok := value("recentProjects").([]any); ok {
		recent := []string{}
		for _, item := range items {
			if id, ok := item.(string); ok && id != "" && prefsUTF16Len(id) <= 256 && !slices.Contains(recent, id) {
				recent = append(recent, id)
			}
		}
		if recent = recent[:min(len(recent), domain.MaxRecentProjects)]; len(recent) > 0 {
			stored.RecentProjects = recent
		}
	}
	return stored
}

type objectEntry struct {
	key   string
	value jsontext.Value
}

// objectEntries is a JSON object's members in the order written, a later duplicate taking the
// first one's place as JSON.parse does; false when value is not an object.
func objectEntries(value jsontext.Value) ([]objectEntry, bool) {
	if value.Kind() != '{' {
		return nil, false
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(value), jsonx.Options())
	if _, err := decoder.ReadToken(); err != nil {
		return nil, false
	}
	entries := []objectEntry{}
	at := map[string]int{}
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil {
			return nil, false
		}
		key := name.String()
		member, err := decoder.ReadValue()
		if err != nil {
			return nil, false
		}
		entry := objectEntry{key: key, value: slices.Clone(member)}
		if index, seen := at[key]; seen {
			entries[index] = entry
		} else {
			at[key] = len(entries)
			entries = append(entries, entry)
		}
	}
	return entries, true
}

// prefsUTF16Len is a string's JavaScript length.
func prefsUTF16Len(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return units
}
