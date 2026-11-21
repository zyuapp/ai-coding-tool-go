package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// corePreferences is the empty workspace's preferences with edit made to them.
func corePreferences(edit func(*contracts.ViewPreferences)) contracts.ViewPreferences {
	preferences := ViewPreferences(EmptyWorkspaceState(nil))
	edit(&preferences)
	return preferences
}

func TestPanelAndSidebarChoicesPersistAndSurviveTheStoreLoading(t *testing.T) {
	corePorted(t, func() {
		restored := coreRun(EmptyWorkspaceState(nil), WorkspaceEventPreferencesLoaded{Preferences: corePreferences(func(preferences *contracts.ViewPreferences) {
			preferences.SessionPanelOpen = true
			preferences.SidebarOpen = false
		})})
		if !restored.SessionPanelOpen || restored.SidebarOpen {
			t.Fatal("preferences not restored")
		}
		closed := Reduce(restored, contracts.ViewCommandViewSetSessionPanelOpen{Open: false})
		coreEqualJSON(t, closed.Effects, `[{"type":"persist-preferences","preferences":{"theme":"aicodingtool-dark","themeMode":"dark","uiFont":"system","monoFont":"system","readingSize":15,"terminalSize":12,"sessionPanelOpen":false,"captureSound":true,"captureFocus":true,"chromeBrowser":false,"computerUse":true,"memoryProtection":true,"idleCompaction":true,"browserTools":true,"notifications":true,"sidebarOpen":false,"sections":{"priority":true,"running":true,"threads":true,"snoozed":true},"subagentGroups":{"sidebar":true,"working":true,"idle":true,"failed":true,"stopped":true,"completed":true},"favoriteModels":[],"shortcuts":{},"browserTabs":{},"browserOrigins":[]}}]`, "persist")
		if closed.State.SessionPanelOpen {
			t.Fatal("panel still open")
		}
		if effects := Reduce(&closed.State, contracts.ViewCommandViewSetSessionPanelOpen{Open: false}).Effects; len(effects) != 0 {
			t.Fatal("an unchanged choice writes something")
		}
		if effects := Reduce(&closed.State, contracts.ViewCommandViewSetSidebarOpen{Open: false}).Effects; len(effects) != 0 {
			t.Fatal("an unchanged sidebar writes something")
		}
		if !Reduce(&closed.State, contracts.ViewCommandViewSetSidebarOpen{Open: true}).State.SidebarOpen {
			t.Fatal("sidebar did not open")
		}
		loaded := Reduce(restored, WorkspaceEventStoreLoaded{Data: domain.ThreadStoreData{Tasks: []*domain.StoredThread{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{}}})
		if !loaded.State.SessionPanelOpen || loaded.State.SidebarOpen {
			t.Fatal("the store loading reset the choices")
		}
	})
}

func TestPaletteAndGroundMoveOnTheirOwnAxes(t *testing.T) {
	corePorted(t, func() {
		state := EmptyWorkspaceState(nil)
		if state.Theme != "aicodingtool-dark" || state.ThemeMode != domain.ThemeModeDark {
			t.Fatal("default theme")
		}
		palette := Reduce(state, contracts.ViewCommandViewSetThemeFamily{Family: "Gruvbox", SystemDark: true})
		if palette.State.Theme != "gruvbox-dark" || palette.State.ThemeMode != domain.ThemeModeDark {
			t.Fatalf("palette %s %s", palette.State.Theme, palette.State.ThemeMode)
		}
		for _, family := range []string{"Gruvbox", "A Palette We Dropped"} {
			if effects := Reduce(&palette.State, contracts.ViewCommandViewSetThemeFamily{Family: family, SystemDark: true}).Effects; len(effects) != 0 {
				t.Fatalf("%s wrote something", family)
			}
		}
		light := Reduce(&palette.State, contracts.ViewCommandViewSetThemeMode{Mode: domain.ThemeModeLight, SystemDark: true})
		if light.State.Theme != "gruvbox-light" || light.State.ThemeMode != domain.ThemeModeLight {
			t.Fatalf("light %s %s", light.State.Theme, light.State.ThemeMode)
		}
		if coreEffect[WorkspaceEffectPersistPreferences](t, light).Preferences.ThemeMode != domain.ThemeModeLight {
			t.Fatal("light not persisted")
		}
	})
}

func TestAutoFollowsTheSystem(t *testing.T) {
	corePorted(t, func() {
		auto := Reduce(EmptyWorkspaceState(nil), contracts.ViewCommandViewSetThemeMode{Mode: domain.ThemeModeAuto, SystemDark: false})
		if auto.State.ThemeMode != domain.ThemeModeAuto || auto.State.Theme != "aicodingtool-light" {
			t.Fatalf("auto %s %s", auto.State.ThemeMode, auto.State.Theme)
		}
		darkened := Reduce(&auto.State, contracts.ViewCommandViewSystemScheme{Dark: true})
		if darkened.State.Theme != "aicodingtool-dark" || len(darkened.Effects) != 0 {
			t.Fatal("the system's choice is written down or ignored")
		}
		if effects := Reduce(&darkened.State, contracts.ViewCommandViewSystemScheme{Dark: true}).Effects; len(effects) != 0 {
			t.Fatal("unchanged scheme wrote something")
		}
		fixed := Reduce(&darkened.State, contracts.ViewCommandViewSetThemeMode{Mode: domain.ThemeModeLight, SystemDark: true})
		if fixed.State.Theme != "aicodingtool-light" || Reduce(&fixed.State, contracts.ViewCommandViewSystemScheme{Dark: true}).State.Theme != "aicodingtool-light" {
			t.Fatal("a ground of its own does not outrank the system's")
		}
	})
}

func TestNamingAThemeNamesItsGround(t *testing.T) {
	corePorted(t, func() {
		named := Reduce(EmptyWorkspaceState(nil), contracts.ViewCommandViewSetTheme{Theme: "tokyo-night-day"})
		if named.State.Theme != "tokyo-night-day" || named.State.ThemeMode != domain.ThemeModeLight {
			t.Fatalf("named %s %s", named.State.Theme, named.State.ThemeMode)
		}
		if effects := Reduce(&named.State, contracts.ViewCommandViewSetTheme{Theme: "a-theme-we-dropped"}).Effects; len(effects) != 0 {
			t.Fatal("a dropped theme wrote something")
		}
	})
}

func TestSizeIsPxWithinItsRange(t *testing.T) {
	corePorted(t, func() {
		bigger := Reduce(EmptyWorkspaceState(nil), contracts.ViewCommandViewSetReadingSize{Size: 19})
		if bigger.State.ReadingSize != 19 || coreEffect[WorkspaceEffectPersistPreferences](t, bigger).Preferences.ReadingSize != 19 {
			t.Fatal("size not set")
		}
		for _, input := range []WorkspaceInput{contracts.ViewCommandViewSetReadingSize{Size: 19}, contracts.ViewCommandViewSetReadingSize{Size: 400}, contracts.ViewCommandViewSetTerminalSize{Size: 2}} {
			if next := Reduce(&bigger.State, input); len(next.Effects) != 0 || next.State.ReadingSize != 19 {
				t.Fatalf("%T changed something", input)
			}
		}
	})
}

func TestSidebarFoldedListsOutliveTheWindow(t *testing.T) {
	corePorted(t, func() {
		folded := Reduce(EmptyWorkspaceState(nil), contracts.ViewCommandViewSetSectionOpen{Section: domain.SidebarSectionPriority, Open: false})
		if folded.State.Sections[domain.SidebarSectionPriority] || !folded.State.Sections[domain.SidebarSectionRunning] {
			t.Fatal("folding one list")
		}
		if coreEffect[WorkspaceEffectPersistPreferences](t, folded).Preferences.Sections[domain.SidebarSectionPriority] {
			t.Fatal("fold not persisted")
		}
		if effects := Reduce(&folded.State, contracts.ViewCommandViewSetSectionOpen{Section: domain.SidebarSectionPriority, Open: false}).Effects; len(effects) != 0 {
			t.Fatal("an unchanged list writes something")
		}
		unknown := Reduce(&folded.State, contracts.ViewCommandViewSetSectionOpen{Section: "nowhere", Open: false})
		coreEqualJSON(t, unknown.State.Sections, folded.State.Sections, "sections")
		if len(unknown.Effects) != 0 {
			t.Fatal("an unknown list writes something")
		}
	})
}

func TestFoldedSubagentGroupIsWrittenDown(t *testing.T) {
	corePorted(t, func() {
		folded := Reduce(EmptyWorkspaceState(nil), contracts.ViewCommandViewSetSubagentGroup{Group: domain.SubagentGroupCompleted, Open: false})
		if folded.State.SubagentGroups[domain.SubagentGroupCompleted] || !folded.State.SubagentGroups[domain.SubagentGroupWorking] {
			t.Fatal("folding one group")
		}
		if coreEffect[WorkspaceEffectPersistPreferences](t, folded).Preferences.SubagentGroups[domain.SubagentGroupCompleted] {
			t.Fatal("fold not persisted")
		}
		if effects := Reduce(&folded.State, contracts.ViewCommandViewSetSubagentGroup{Group: domain.SubagentGroupCompleted, Open: false}).Effects; len(effects) != 0 {
			t.Fatal("an unchanged group writes something")
		}
		unknown := Reduce(&folded.State, contracts.ViewCommandViewSetSubagentGroup{Group: "nowhere", Open: false})
		coreEqualJSON(t, unknown.State.SubagentGroups, folded.State.SubagentGroups, "groups")
		if len(unknown.Effects) != 0 {
			t.Fatal("an unknown group writes something")
		}
	})
}

func TestModelFavoritesPersistRestoreAndIgnoreDuplicates(t *testing.T) {
	corePorted(t, func() {
		pin := func(engine domain.AgentEngine, favorite bool) WorkspaceInput {
			return contracts.ViewCommandViewSetModelFavorite{Engine: engine, Model: "opus", Favorite: favorite}
		}
		pinned := Reduce(EmptyWorkspaceState(nil), pin(domain.AgentEngineClaude, true))
		coreEqualJSON(t, pinned.State.FavoriteModels, `[{"engine":"claude","model":"opus"}]`, "pinned")
		saved := coreEffect[WorkspaceEffectPersistPreferences](t, pinned).Preferences
		restored := Reduce(EmptyWorkspaceState(nil), WorkspaceEventPreferencesLoaded{Preferences: saved}).State
		coreEqualJSON(t, restored.FavoriteModels, `[{"engine":"claude","model":"opus"}]`, "restored")
		if effects := Reduce(&restored, pin(domain.AgentEngineClaude, true)).Effects; len(effects) != 0 {
			t.Fatal("a duplicate pin writes something")
		}
		removed := Reduce(&restored, pin(domain.AgentEngineClaude, false))
		coreEqualJSON(t, coreEffect[WorkspaceEffectPersistPreferences](t, removed).Preferences.FavoriteModels, `[]`, "removed")

		claude := Reduce(EmptyWorkspaceState(nil), pin(domain.AgentEngineClaude, true)).State
		both := Reduce(&claude, pin(domain.AgentEngineOpencode, true)).State
		coreEqualJSON(t, both.FavoriteModels, `[{"engine":"claude","model":"opus"},{"engine":"opencode","model":"opus"}]`, "both")
		unpinned := Reduce(&both, pin(domain.AgentEngineClaude, false)).State
		coreEqualJSON(t, unpinned.FavoriteModels, `[{"engine":"opencode","model":"opus"}]`, "unpinned")
		if galaxy := Reduce(&claude, pin("galaxy", true)); !sameState(&galaxy.State, &claude) {
			t.Fatal("an unknown engine changed the state")
		}
	})
}

func TestAgentSettingsReloadIsTransient(t *testing.T) {
	corePorted(t, func() {
		initial := EmptyWorkspaceState(nil)
		asked := Reduce(initial, contracts.EngineCommandEngineReloadSettings{})
		coreEqualJSON(t, asked.Effects, `[{"type":"engine.reload-settings"}]`, "asked")
		if asked.State.AgentSettingsReload != WorkspaceStateAgentSettingsReloadReloading {
			t.Fatal("not reloading")
		}
		if effects := Reduce(&asked.State, contracts.EngineCommandEngineReloadSettings{}).Effects; len(effects) != 0 {
			t.Fatal("asked twice")
		}
		status := func(status contracts.AgentSettingsReloadEventStatus) WorkspaceInput {
			return WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{contracts.AgentSettingsReloadEvent{Status: status}}}
		}
		pending := Reduce(&asked.State, status("pending"))
		if pending.State.AgentSettingsReload != WorkspaceStateAgentSettingsReloadPending || len(Reduce(&pending.State, contracts.EngineCommandEngineReloadSettings{}).Effects) != 0 {
			t.Fatal("pending")
		}
		done := Reduce(&pending.State, status("reloaded"))
		if done.State.AgentSettingsReload != WorkspaceStateAgentSettingsReloadReloaded || !sameSlice(done.State.Threads, initial.Threads) || len(done.Effects) != 0 {
			t.Fatal("reloaded")
		}
		failed := Reduce(&asked.State, contracts.AgentSettingsReloadEvent{Status: "failed", Message: new("Worker unavailable")})
		if deref(failed.State.ActionError) != "Worker unavailable" {
			t.Fatal("failure not shown")
		}
		coreEqualJSON(t, Reduce(&failed.State, contracts.EngineCommandEngineReloadSettings{}).Effects, `[{"type":"engine.reload-settings"}]`, "retry")
	})
}

func TestInstallingTheTerminalCommand(t *testing.T) {
	corePorted(t, func() {
		installing := Reduce(EmptyWorkspaceState(nil), contracts.CliCommandCliInstall{})
		coreEqualJSON(t, installing.Effects, `[{"type":"cli.install"}]`, "install")
		if !installing.State.Cli.Busy {
			t.Fatal("not busy")
		}
		status := domain.CliStatus{State: "installed", Path: "/usr/local/bin/aic"}
		installed := Reduce(&installing.State, CliEventCliReadStatus{Status: status})
		coreEqualJSON(t, installed.State.Cli, `{"status":{"state":"installed","path":"/usr/local/bin/aic"},"busy":false,"error":null}`, "installed")
		uninstalling := Reduce(&installed.State, contracts.CliCommandCliUninstall{})
		refused := Reduce(&uninstalling.State, CliEventCliFailed{Message: "Cancelled."})
		if refused.State.Cli.Busy || deref(refused.State.Cli.Error) != "Cancelled." {
			t.Fatal("a refusal gives the row its controls back")
		}
		coreEqualJSON(t, refused.State.Cli.Status, status, "status kept")
		if Reduce(&refused.State, contracts.CliCommandCliInstall{}).State.Cli.Error != nil {
			t.Fatal("trying again keeps the last error")
		}
	})
}

func TestPlanLimitsKeepWhatEachProviderSaid(t *testing.T) {
	corePorted(t, func() {
		var claude domain.PlanUsage = domain.PlanUsageAvailable{Subscription: new("max"), Windows: []*domain.UsageWindow{}}
		reading := Reduce(EmptyWorkspaceState(nil), contracts.PlanUsageCommandUsageRead{})
		want := []WorkspaceEffect{}
		for _, engine := range domain.AgentEngines {
			want = append(want, WorkspaceEffectReadPlanUsage{Engine: engine, Read: 1})
		}
		coreEqualJSON(t, reading.Effects, want, "reads")
		coreEqualJSON(t, reading.State.PlanUsage.Reading, domain.AgentEngines, "reading")
		answered := Reduce(&reading.State, PlanUsageEvent{Engine: domain.AgentEngineClaude, Read: 1, Usage: claude})
		rest := slices.DeleteFunc(slices.Clone(domain.AgentEngines), func(engine domain.AgentEngine) bool { return engine == domain.AgentEngineClaude })
		coreEqualJSON(t, answered.State.PlanUsage.Reading, rest, "answered reading")
		coreEqualJSON(t, answered.State.PlanUsage.Reports.Claude, claude, "report")
		again := Reduce(&answered.State, contracts.PlanUsageCommandUsageRead{})
		coreEqualJSON(t, again.State.PlanUsage.Reports.Claude, claude, "kept while reading")
		late := Reduce(&again.State, PlanUsageEvent{Engine: domain.AgentEngineClaude, Read: 1, Usage: domain.PlanUsageNotApplicable{}})
		coreEqualJSON(t, late.State.PlanUsage.Reports.Claude, claude, "late answer dropped")
	})
}

func TestUsageWarningPutAwayStaysAway(t *testing.T) {
	corePorted(t, func() {
		key := "claude:five_hour:near@1787041140000"
		dismissed := Reduce(EmptyWorkspaceState(nil), contracts.PlanUsageCommandUsageDismissWarning{Warning: key})
		if !slices.Equal(dismissed.State.PlanUsage.Dismissed, []string{key}) {
			t.Fatal("not dismissed")
		}
		saved := coreEffect[WorkspaceEffectPersistPreferences](t, dismissed).Preferences
		if !slices.Equal(saved.DismissedUsageWarnings, []string{key}) {
			t.Fatal("not saved")
		}
		if again := Reduce(&dismissed.State, contracts.PlanUsageCommandUsageDismissWarning{Warning: key}); !sameState(&again.State, &dismissed.State) {
			t.Fatal("dismissing twice changed the state")
		}
		relaunched := Reduce(EmptyWorkspaceState(nil), WorkspaceEventPreferencesLoaded{Preferences: saved})
		if !slices.Equal(relaunched.State.PlanUsage.Dismissed, []string{key}) {
			t.Fatal("not restored")
		}
	})
}

func TestPickedProjectsLeadARecentListOfFive(t *testing.T) {
	corePorted(t, func() {
		state := EmptyWorkspaceState(nil)
		for _, projectID := range []string{"a", "b", "c", "d", "e", "f"} {
			state = coreRun(state, contracts.ViewCommandViewRememberProject{ProjectID: projectID})
		}
		if !slices.Equal(state.RecentProjects, []string{"f", "e", "d", "c", "b"}) {
			t.Fatalf("recent %v", state.RecentProjects)
		}
		moved := Reduce(state, contracts.ViewCommandViewRememberProject{ProjectID: "c"})
		want := []string{"c", "f", "e", "d", "b"}
		if !slices.Equal(moved.State.RecentProjects, want) {
			t.Fatalf("moved %v", moved.State.RecentProjects)
		}
		saved := coreEffect[WorkspaceEffectPersistPreferences](t, moved).Preferences
		if !slices.Equal(saved.RecentProjects, want) || len(Reduce(&moved.State, contracts.ViewCommandViewRememberProject{ProjectID: "c"}).Effects) != 0 {
			t.Fatal("saving")
		}
		storage := coreStorage{}
		WriteViewPreferences(storage, saved)
		read := ReadViewPreferences(storage)
		restored := Reduce(EmptyWorkspaceState(nil), WorkspaceEventPreferencesLoaded{Preferences: corePreferences(func(preferences *contracts.ViewPreferences) { preferences.RecentProjects = read.RecentProjects })})
		if !slices.Equal(restored.State.RecentProjects, want) {
			t.Fatalf("restored %v", restored.State.RecentProjects)
		}
	})
}

func TestRefusedBindingStaysUpOnlyUntilTheNextKeystroke(t *testing.T) {
	corePorted(t, func() {
		refused := coreWorkspace(func(state *WorkspaceState) { state.ActionError = new("That keystroke is taken.") })
		for _, input := range []WorkspaceInput{
			contracts.ViewCommandViewCaptureShortcut{Action: new("thread.jump")},
			contracts.ViewCommandViewSetShortcut{Action: "thread.jump", Binding: new("Mod+P")},
			contracts.ViewCommandViewResetShortcuts{},
		} {
			if Reduce(refused, input).State.ActionError != nil {
				t.Fatalf("%T kept the error", input)
			}
		}
	})
}
