package domain

import "slices"

var SettingsSections = []SettingsSection{
	SettingsSectionGeneral, SettingsSectionAppearance, SettingsSectionUsage, SettingsSectionEngines, SettingsSectionProjects,
	SettingsSectionWorktrees, SettingsSectionShortcuts, SettingsSectionComputerUse, SettingsSectionBrowser, SettingsSectionPhone,
	SettingsSectionArchive,
}

func IsSettingsSection(value any) bool {
	switch v := value.(type) {
	case SettingsSection:
		return slices.Contains(SettingsSections, v)
	case string:
		return slices.Contains(SettingsSections, SettingsSection(v))
	}
	return false
}
