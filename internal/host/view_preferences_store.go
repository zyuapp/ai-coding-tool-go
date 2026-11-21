package host

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func or[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

// LoadViewPreferences reads the stored preferences over their defaults. Wide windows open the
// session panel, and windows with room for it show the sidebar. A host without a window (nil
// viewportWidth) shows the sidebar and keeps the panel shut.
func LoadViewPreferences(storage application.KeyValueStorage, viewportWidth *float64) contracts.ViewPreferences {
	stored := application.ReadViewPreferences(storage)
	preferences := contracts.ViewPreferences{
		Theme:            or(stored.Theme, domain.DefaultTheme),
		ThemeMode:        or(stored.ThemeMode, domain.DefaultThemeMode),
		UIFont:           or(stored.UIFont, domain.DefaultUiFont),
		MonoFont:         or(stored.MonoFont, domain.DefaultMonoFont),
		ReadingSize:      or(stored.ReadingSize, domain.ReadingSize.Default),
		TerminalSize:     or(stored.TerminalSize, domain.TerminalSize.Default),
		SessionPanelOpen: or(stored.SessionPanelOpen, viewportWidth != nil && *viewportWidth >= 1400),
		CaptureSound:     or(stored.CaptureSound, true),
		CaptureFocus:     or(stored.CaptureFocus, true),
		ChromeBrowser:    or(stored.ChromeBrowser, false),
		ComputerUse:      or(stored.ComputerUse, true),
		MemoryProtection: new(or(stored.MemoryProtection, true)),
		IdleCompaction:   new(or(stored.IdleCompaction, true)),
		BrowserTools:     or(stored.BrowserTools, true),
		Notifications:    or(stored.Notifications, true),
		SidebarOpen:      or(stored.SidebarOpen, viewportWidth == nil || *viewportWidth >= 900),
		Sections:         or(stored.Sections, maps.Clone(domain.OpenSidebarSections)),
		SubagentGroups:   or(stored.SubagentGroups, maps.Clone(domain.OpenSubagentGroups)),
		FavoriteModels:   nonNil(stored.FavoriteModels),
		Shortcuts:        stored.Shortcuts,
		BrowserTabs:      stored.BrowserTabs,
		BrowserOrigins:   nonNil(stored.BrowserOrigins),
		DismissedToasts:  nonNil(stored.DismissedToasts),
		RecentProjects:   nonNil(stored.RecentProjects),
	}
	if stored.ActivityProject != nil && *stored.ActivityProject != "" {
		preferences.ActivityProject = stored.ActivityProject
	}
	if stored.SimulatorDevice != nil && *stored.SimulatorDevice != "" {
		preferences.SimulatorDevice = stored.SimulatorDevice
	}
	if preferences.Shortcuts == nil {
		preferences.Shortcuts = domain.ShortcutOverrides{}
	}
	if preferences.BrowserTabs == nil {
		preferences.BrowserTabs = map[string][]string{}
	}
	if stored.DismissedUsageWarnings != nil {
		preferences.DismissedUsageWarnings = stored.DismissedUsageWarnings
	}
	return preferences
}

func SaveViewPreferences(storage application.KeyValueStorage, preferences contracts.ViewPreferences) {
	application.WriteViewPreferences(storage, preferences)
}
