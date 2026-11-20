package domain

import (
	"strings"
	"testing"
)

func settingsJumpIDs(options []*SettingsJumpOption) string {
	ids := []string{}
	for _, option := range options {
		if option.SettingID == nil {
			ids = append(ids, "null")
		} else {
			ids = append(ids, *option.SettingID)
		}
	}
	return strings.Join(ids, "|")
}

func TestRankSettingsJumpsEmptyQueryOffersNone(t *testing.T) {
	if len(RankSettingsJumps("", SettingsJumpRows)) != 0 || len(RankSettingsJumps("   ", SettingsJumpRows)) != 0 {
		t.Fatal("the panel opens on threads alone")
	}
}

func TestSettingsJumpOptionsCoverEverything(t *testing.T) {
	if len(SettingsJumpOptions) != len(SettingsSections)+len(SettingsControls)+len(ShortcutActions) {
		t.Fatal("every page, control, and shortcut is offered")
	}
}

func TestRankSettingsJumpsOrder(t *testing.T) {
	titles := []string{}
	for _, option := range RankSettingsJumps("browser", SettingsJumpRows) {
		titles = append(titles, option.Title)
	}
	if strings.Join(titles, "|") != "Browser|Browser use|Claude in Chrome" {
		t.Fatal(titles)
	}
	for query, want := range map[string]string{
		"font":        "null|appearance.ui-font|appearance.mono-font|appearance.reading-size|appearance.terminal-size",
		"tailscale":   "phone.tailscale|null",
		"text size":   "null|appearance.reading-size|appearance.terminal-size",
		"text purple": "",
		"kubernetes":  "",
	} {
		if got := settingsJumpIDs(RankSettingsJumps(query, SettingsJumpRows)); got != want {
			t.Fatalf("%q = %s", query, got)
		}
	}
	if got := settingsJumpIDs(RankSettingsJumps("cli", SettingsJumpRows)[:2]); got != "null|general.cli" {
		t.Fatal(got)
	}
	trash := RankSettingsJumps("trash", SettingsJumpRows)
	if len(trash) != 1 || trash[0].Section != "archive" {
		t.Fatal("trash")
	}
}

func TestRankSettingsJumpsGroupsAndShortcuts(t *testing.T) {
	if got := RankSettingsJumps("installed engines", SettingsJumpRows); featuresDeref(got[0].SettingID) != "engines.installed" {
		t.Fatal("a group is found by its heading")
	}
	for _, query := range []string{"platform setup", "linux runtime"} {
		got := RankSettingsJumps(query, SettingsJumpRows)
		if len(got) != 1 || got[0].Section != "computer-use" || got[0].SettingID != nil {
			t.Fatalf("%q = %s", query, settingsJumpIDs(got))
		}
	}
	deny := RankSettingsJumps("deny", SettingsJumpRows)[0]
	if deny.Section != "shortcuts" || featuresDeref(deny.SettingID) != "shortcut.run.deny" || featuresDeref(deny.Page) != "Shortcuts" {
		t.Fatal(featuresJSON(t, deny))
	}
	if len(RankSettingsJumps("e", 3)) != 3 {
		t.Fatal("the list is cut to the rows the panel draws")
	}
}
