package domain

import "strings"

// engineNames is every engine by name, so a page about engines answers to each.
var engineNames = func() string {
	names := make([]string, 0, len(AgentEngines))
	for _, engine := range AgentEngines {
		names = append(names, strings.ToLower(EngineLabel(string(engine))))
	}
	return strings.Join(names, " ")
}()

// SettingsPageLabels is the name a page goes by, in the sidebar and in the jump panel alike.
var SettingsPageLabels = map[SettingsSection]string{
	SettingsSectionGeneral:     "General",
	SettingsSectionAppearance:  "Appearance",
	SettingsSectionUsage:       "Usage",
	SettingsSectionEngines:     "Engines",
	SettingsSectionProjects:    "Projects",
	SettingsSectionWorktrees:   "Worktrees",
	SettingsSectionShortcuts:   "Shortcuts",
	SettingsSectionComputerUse: "Computer use",
	SettingsSectionBrowser:     "Browser",
	SettingsSectionPhone:       "Devices",
	SettingsSectionArchive:     "Archived threads",
}

// pageKeywords is what a page answers to besides its own name.
var pageKeywords = map[SettingsSection]string{
	SettingsSectionGeneral:     "cli terminal command notifications chrome dictation voice",
	SettingsSectionAppearance:  "theme colours colors dark light font text size",
	SettingsSectionUsage:       "plan limits quota tokens spend",
	SettingsSectionEngines:     engineNames + " sign in account model provider",
	SettingsSectionProjects:    "folders add remove rename edit path",
	SettingsSectionWorktrees:   "git branch checkout managed disk",
	SettingsSectionShortcuts:   "keyboard keys binding keystroke rebind",
	SettingsSectionComputerUse: "accessibility screen recording permissions automation apps platform setup linux runtime wayland x11",
	SettingsSectionBrowser:     "session cookies sign out sites origins clear data import sign in chrome brave firefox edge arc vivaldi opera",
	SettingsSectionPhone:       "mobile phone computer remote pairing tailscale device qr serve",
	SettingsSectionArchive:     "archived deleted restore threads trash",
}

// SettingsControl is one control or group on a settings page, and the words a search may reach it by.
type SettingsControl struct {
	ID      SettingID       `json:"id"`
	Section SettingsSection `json:"section"`
	// What the row calls itself. The page and the jump panel both draw this.
	Label    string `json:"label"`
	Keywords string `json:"keywords"`
}

var SettingsControls = []*SettingsControl{
	{ID: SettingIDGeneralCli, Section: SettingsSectionGeneral, Label: "aic", Keywords: "cli terminal command install shell path open folder"},
	{ID: SettingIDGeneralDictation, Section: SettingsSectionGeneral, Label: "Speech model", Keywords: "dictation voice speak talk microphone mic hold hands-free tap parakeet transcribe"},
	{ID: SettingIDGeneralMemoryProtection, Section: SettingsSectionGeneral, Label: "Memory protection", Keywords: "memory leak ram process stop automatic"},
	{ID: SettingIDGeneralNotifications, Section: SettingsSectionGeneral, Label: "Notifications", Keywords: "alert banner notify sound away desktop phone push"},
	{ID: SettingIDGeneralChromeBrowser, Section: SettingsSectionGeneral, Label: "Claude in Chrome", Keywords: "extension browser chrome experimental"},
	{ID: SettingIDGeneralUpdates, Section: SettingsSectionGeneral, Label: "Updates", Keywords: "check version release upgrade"},
	{ID: SettingIDGeneralLicenses, Section: SettingsSectionGeneral, Label: "Open source licenses", Keywords: "legal notices third party copyright"},
	{ID: SettingIDUsagePlanLimits, Section: SettingsSectionUsage, Label: "Plan limits", Keywords: "quota tokens spend session weekly reset"},
	{ID: SettingIDEnginesInstalled, Section: SettingsSectionEngines, Label: "Installed engines", Keywords: engineNames + " cli version update sign in install check"},
	{ID: SettingIDEnginesAgentSettings, Section: SettingsSectionEngines, Label: "Agent settings", Keywords: "reload skills mcp hooks config apply"},
	{ID: SettingIDAppearanceTheme, Section: SettingsSectionAppearance, Label: "Colours", Keywords: "theme colors palette dark light auto mode appearance"},
	{ID: SettingIDAppearanceUIFont, Section: SettingsSectionAppearance, Label: "Interface", Keywords: "font typeface family ui interface sans"},
	{ID: SettingIDAppearanceMonoFont, Section: SettingsSectionAppearance, Label: "Code and terminal", Keywords: "font typeface family monospace code terminal shell diff"},
	{ID: SettingIDAppearanceReadingSize, Section: SettingsSectionAppearance, Label: "Conversation text", Keywords: "text size font bigger smaller reading message"},
	{ID: SettingIDAppearanceTerminalSize, Section: SettingsSectionAppearance, Label: "Terminal text", Keywords: "text size font bigger smaller terminal shell"},
	{ID: SettingIDComputerUseAvailability, Section: SettingsSectionComputerUse, Label: "Computer use", Keywords: "automation control apps see operate switch"},
	{ID: SettingIDComputerUseAccessibility, Section: SettingsSectionComputerUse, Label: "Accessibility", Keywords: "permission macos click type navigate"},
	{ID: SettingIDComputerUseScreenRecording, Section: SettingsSectionComputerUse, Label: "Screen & System Audio Recording", Keywords: "permission macos screenshot capture window"},
	{ID: SettingIDBrowserAvailability, Section: SettingsSectionBrowser, Label: "Browser use", Keywords: "panel pages agent switch web"},
	{ID: SettingIDBrowserSession, Section: SettingsSectionBrowser, Label: "Session", Keywords: "cookies sign out clear data sites origins allowed"},
	{ID: SettingIDBrowserImport, Section: SettingsSectionBrowser, Label: "Import", Keywords: "sign ins logins cookies brave firefox edge arc vivaldi opera safari"},
	{ID: SettingIDPhoneAvailability, Section: SettingsSectionPhone, Label: "Phone access", Keywords: "mobile remote server pair device switch"},
	{ID: SettingIDPhoneTailscale, Section: SettingsSectionPhone, Label: "Tailscale", Keywords: "vpn network tailnet install https"},
	{ID: SettingIDPhonePairing, Section: SettingsSectionPhone, Label: "Pair a phone", Keywords: "pairing code qr connect add mobile"},
	{ID: SettingIDPhonePhones, Section: SettingsSectionPhone, Label: "Phones", Keywords: "paired mobile revoke remove"},
	{ID: SettingIDPhoneComputers, Section: SettingsSectionPhone, Label: "Computers", Keywords: "remote machine pair link host rename forget mac linux"},
}

var settingsCatalogByID = func() map[SettingID]*SettingsControl {
	byID := make(map[SettingID]*SettingsControl, len(SettingsControls))
	for _, control := range SettingsControls {
		byID[control.ID] = control
	}
	return byID
}()

// SettingControl is the entry a row draws itself from. Every SettingID has one.
func SettingControl(id SettingID) *SettingsControl {
	return settingsCatalogByID[id]
}

// ShortcutSettingID is what a shortcut's row is marked by.
func ShortcutSettingID(actionID string) string {
	return "shortcut." + actionID
}

// SettingsJumpOptions is every page, then every control, then every shortcut, which is the order two equal matches are offered in.
var SettingsJumpOptions = func() []*SettingsJumpOption {
	options := make([]*SettingsJumpOption, 0, len(SettingsSections)+len(SettingsControls)+len(ShortcutActions))
	for _, section := range SettingsSections {
		options = append(options, &SettingsJumpOption{
			ID:       "settings:" + string(section),
			Section:  section,
			Title:    SettingsPageLabels[section],
			Keywords: pageKeywords[section],
		})
	}
	for _, control := range SettingsControls {
		settingID := string(control.ID)
		page := SettingsPageLabels[control.Section]
		options = append(options, &SettingsJumpOption{
			ID:        "settings:" + settingID,
			Section:   control.Section,
			SettingID: &settingID,
			Title:     control.Label,
			Page:      &page,
			Keywords:  control.Keywords,
		})
	}
	for _, action := range ShortcutActions {
		settingID := ShortcutSettingID(action.ID)
		page := SettingsPageLabels[SettingsSectionShortcuts]
		options = append(options, &SettingsJumpOption{
			ID:        "settings:" + settingID,
			Section:   SettingsSectionShortcuts,
			SettingID: &settingID,
			Title:     action.Label,
			Page:      &page,
			Keywords:  "shortcut keyboard " + action.Group + " " + action.Description,
		})
	}
	return options
}()
