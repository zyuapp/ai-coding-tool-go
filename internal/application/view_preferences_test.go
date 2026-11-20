package application

import (
	"testing"
)

func TestFavoriteUnderAReplacedIDRestoresAsItsSuccessor(t *testing.T) {
	storage := coreStorage{ViewPreferencesKey: `{"favoriteModels":["gpt-5.6-sol","gpt-6-sol","gpt-6.1-sol","gpt-6.2-sol","gpt-6-astra","gpt-5.6-luna"]}`}
	coreEqualJSON(t, ReadViewPreferences(storage).FavoriteModels, `[{"engine":"codex","model":"gpt-6.2-sol"},{"engine":"codex","model":"gpt-6.2-astra"},{"engine":"codex","model":"gpt-6-luna"}]`, "favorites")
}

func TestStoredRecentListDropsWhatItCannotReadAndKeepsFive(t *testing.T) {
	storage := coreStorage{ViewPreferencesKey: `{"recentProjects":["a",3,"","a","b","c","d","e","f"]}`}
	coreEqualJSON(t, ReadViewPreferences(storage).RecentProjects, `["a","b","c","d","e"]`, "recent")
}

func TestUnreadablePreferencesReportNone(t *testing.T) {
	for _, raw := range []string{"not json", "null", "[]", `"text"`} {
		storage := coreStorage{ViewPreferencesKey: raw}
		coreEqualJSON(t, ReadViewPreferences(storage), `{}`, raw)
	}
	coreEqualJSON(t, ReadViewPreferences(coreStorage{}), `{}`, "absent")
}

func TestStoredPreferencesKeepOnlyWhatTheAppStillKnows(t *testing.T) {
	storage := coreStorage{ViewPreferencesKey: `{
		"theme":"gruvbox-dark","themeMode":"sepia","uiFont":"no-such-font","readingSize":"large","terminalSize":400,
		"sidebarOpen":false,"activityProject":"","simulatorDevice":"iphone",
		"sections":{"priority":false,"retired":false},"subagentGroups":{"completed":false,"working":"yes"},
		"shortcuts":{"thread.jump":"Mod+P","no.such.action":"Mod+Q","thread.new":null},
		"browserTabs":{"t1":["https://a.example","",3],"t2":[]},"browserOrigins":["https://a.example",""],
		"dismissedUsageWarnings":[],"discardedLeads":["lead"]
	}`}
	coreEqualJSON(t, ReadViewPreferences(storage), `{
		"favoriteModels":[],"theme":"gruvbox-dark","readingSize":17,"sidebarOpen":false,"simulatorDevice":"iphone",
		"sections":{"priority":false,"running":true,"threads":true,"snoozed":true},
		"subagentGroups":{"sidebar":true,"working":true,"idle":true,"failed":true,"stopped":true,"completed":false},
		"shortcuts":{"thread.jump":"Mod+P"},
		"browserTabs":{"t1":["https://a.example"]},"browserOrigins":["https://a.example"],"discardedLeads":["lead"]
	}`, "read")
}
