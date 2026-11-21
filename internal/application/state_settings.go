package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialSettingsState() SettingsState {
	return SettingsState{
		Sections:                   domain.OpenSidebarSections,
		SubagentGroups:             domain.OpenSubagentGroups,
		Theme:                      domain.DefaultTheme,
		ThemeMode:                  domain.DefaultThemeMode,
		UIFont:                     domain.DefaultUiFont,
		MonoFont:                   domain.DefaultMonoFont,
		ReadingSize:                domain.ReadingSize.Default,
		TerminalSize:               domain.TerminalSize.Default,
		ActivityProject:            nil,
		SidebarOpen:                true,
		SessionPanelOpen:           false,
		CaptureSound:               true,
		CaptureFocus:               true,
		ChromeBrowser:              false,
		ComputerUse:                true,
		BrowserTools:               true,
		MemoryProtection:           true,
		IdleCompaction:             true,
		Notifications:              true,
		SettingsOpen:               false,
		SettingsSection:            nil,
		SettingsFocus:              nil,
		UsageComputer:              nil,
		FavoriteModels:             []*domain.EngineModel{},
		RecentProjects:             []string{},
		Shortcuts:                  domain.ShortcutOverrides{},
		CapturingShortcut:          nil,
		DesktopShortcutUnavailable: nil,
	}
}
