package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"maps"
	"slices"
)

var SettingsInputs = []string{
	"view.set-theme", "view.set-theme-family", "view.set-theme-mode", "view.system-scheme", "view.set-ui-font", "view.set-mono-font",
	"view.set-reading-size", "view.set-terminal-size", "view.set-activity-project", "view.set-sidebar-open",
	"view.focus-composer", "view.set-shortcut", "view.reset-shortcuts", "view.capture-shortcut", "shortcut.captured", "shortcut.unavailable",
	"view.inspect-subagent", "subagent.activity.loaded", "view.set-capture-options", "view.set-chrome-browser", "view.set-computer-use",
	"view.set-memory-protection", "view.set-browser-tools", "view.set-notifications", "view.set-session-panel-open", "view.set-settings-open", "view.set-usage-computer", "view.set-subagent-group",
	"view.set-section-open",
}

// ReduceModelFavorite answers "view.set-model-favorite".
func ReduceModelFavorite(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	favorite := input.(contracts.ViewCommandViewSetModelFavorite)
	if !domain.IsAgentEngine(favorite.Engine) || !domain.IsModelID(string(favorite.Model)) || domain.IsFavorite(state.FavoriteModels, favorite.Engine, favorite.Model) == favorite.Favorite {
		return SliceTransition{}
	}
	favoriteModels := []*domain.EngineModel{}
	for _, pinned := range state.FavoriteModels {
		if pinned.Engine != favorite.Engine || pinned.Model != favorite.Model {
			favoriteModels = append(favoriteModels, pinned)
		}
	}
	if favorite.Favorite {
		favoriteModels = append(favoriteModels, &domain.EngineModel{Engine: favorite.Engine, Model: favorite.Model})
	}
	return SliceTransition{Patch: func(next *WorkspaceState) { next.FavoriteModels = favoriteModels }, Persists: true}
}

// ReduceRememberProject answers "view.remember-project".
func ReduceRememberProject(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	project := input.(contracts.ViewCommandViewRememberProject).ProjectID
	if len(state.RecentProjects) > 0 && state.RecentProjects[0] == project {
		return SliceTransition{}
	}
	recent := domain.RememberProject(state.RecentProjects, project)
	return SliceTransition{Patch: func(next *WorkspaceState) { next.RecentProjects = recent }, Persists: true}
}

// settingsChange is the TypeScript class behind SettingsEdit: settings as one input leaves them.
type settingsChange = SettingsEdit

func SettingsOf(state *WorkspaceState) SettingsEdit {
	return SettingsEdit{View: OwnFields[SettingsState](state)}
}

// CaptureStoppedIn is settings no longer waiting for a keystroke.
func CaptureStoppedIn(edit SettingsEdit) SettingsEdit {
	edit.View.CapturingShortcut = nil
	return edit
}

// sheetIn is settings shown on a page and the control on it something named, or put away.
// An absent usageComputer leaves the one shown as it is.
func sheetIn(edit SettingsEdit, open bool, section *domain.SettingsSection, settingID *string, usageComputer jsonx.Opt[string]) SettingsEdit {
	view := edit.View
	view.SettingsOpen = open
	view.SettingsSection = nil
	if open {
		view.SettingsSection = section
	}
	var focus *domain.SettingMark
	if open && section != nil && settingID != nil && *settingID != "" {
		visit := 1.0
		if view.SettingsFocus != nil {
			visit = view.SettingsFocus.Visit + 1
		}
		focus = &domain.SettingMark{ID: *settingID, Visit: visit}
	}
	view.SettingsFocus = focus
	if !usageComputer.IsAbsent() {
		if computer, ok := usageComputer.Get(); ok {
			view.UsageComputer = &computer
		} else {
			view.UsageComputer = nil
		}
	}
	if !open {
		view.CapturingShortcut = nil
	}
	return SettingsEdit{View: view}
}

// SettingsShown is settings shown or put away, on the page asked (nil for none) and the control settingID names.
func SettingsShown(state *WorkspaceState, open bool, asked *string, settingID *string) Step {
	var section *domain.SettingsSection
	if asked != nil && *asked != "" && domain.IsSettingsSection(*asked) {
		named := domain.SettingsSection(*asked)
		section = &named
	}
	var usage jsonx.Opt[string]
	if open && section != nil && *section == domain.SettingsSectionUsage {
		if paired := PairedID(&state.Computers, state.Computers.Active); paired != nil {
			usage = jsonx.Some(*paired)
		} else {
			usage = jsonx.Null[string]()
		}
	}
	var effects []WorkspaceEffect
	if open {
		effects = slices.Clone(TakeKeys)
	} else {
		effects = StopCapture(state)
	}
	if open && section != nil && *section == domain.SettingsSectionWorktrees && !state.WorktreeManagementLoading {
		effects = append(effects, WorkspaceEffectListWorktrees{})
	}
	edits := []OwnerEdit{
		sheetIn(SettingsOf(state), open, section, settingID, usage),
		ManagementShownIn(WorktreesOf(state), open, section != nil && *section == domain.SettingsSectionWorktrees),
	}
	if !open || section != nil {
		edits = append(edits, SetupShownIn(ComputerUseOf(state), false))
	}
	if open {
		edits = append(edits, EditDock(DocksOf(state), DockOwner(state, nil), func(dock *ThreadDock) {
			dock.Open = false
			dock.Expanded = false
		}))
	}
	return Step{Edits: edits, Effects: effects}
}

// inspectSubagent loads a child's log and fills settings that older app versions did not capture.
func inspectSubagent(state *WorkspaceState, taskID *string, subagentID string) []WorkspaceEffect {
	if taskID == nil {
		return []WorkspaceEffect{}
	}
	var subagent *domain.Subagent
	for _, candidate := range state.Subagents[*taskID] {
		if candidate.ID == subagentID {
			subagent = candidate
			break
		}
	}
	if subagent == nil || *taskID == "" {
		return []WorkspaceEffect{}
	}
	effects := []WorkspaceEffect{}
	if len(subagent.Activity) == 0 {
		effects = append(effects, WorkspaceEffectLoadSubagentActivity{TaskID: *taskID, SubagentID: subagentID})
	}
	thread := findThread(state.Threads, taskID)
	if thread != nil && thread.Engine != "" && domain.CapabilitiesFor(string(thread.Engine)).SubagentMetadata &&
		(subagent.Model == nil || *subagent.Model == "" || subagent.Effort == nil || *subagent.Effort == "") {
		var sessionID *string
		if thread.Continuation != nil {
			value := thread.Continuation.Value
			sessionID = &value
		}
		effects = append(effects, WorkspaceEffectLoadSubagentMetadata{TaskID: *taskID, SubagentID: subagentID, Engine: thread.Engine, SessionID: sessionID})
	}
	return effects
}

// setPreference keeps one remembered choice, and writes nothing when it already holds. key is
// "activityProject" (value *string) or "sidebarOpen" (value bool).
func setPreference(state *WorkspaceState, key string, value any) SliceTransition {
	switch key {
	case "activityProject":
		project, _ := value.(*string)
		if samePointee(state.ActivityProject, project) {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ActivityProject = project }, Persists: true}
	case "sidebarOpen":
		open, _ := value.(bool)
		if state.SidebarOpen == open {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.SidebarOpen = open }, Persists: true}
	}
	panic("application: setPreference has no key " + key)
}

// savedWith is the preferences as patch leaves them, saved ahead of what else the change asks.
func savedWith(state *WorkspaceState, patch OwnPatch) []WorkspaceEffect {
	return PersistView(Afterward(state, SliceTransition{Patch: patch}))
}

func ReduceSettings(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.ViewCommandViewSetTheme:
		chosen := domain.ThemeByID(input.Theme)
		if chosen == nil || state.Theme == chosen.ID && string(state.ThemeMode) == string(chosen.Variant) {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Theme = chosen.ID
			next.ThemeMode = domain.ThemeMode(chosen.Variant)
		}, Persists: true}

	case contracts.ViewCommandViewSetThemeFamily:
		chosen := domain.ThemeFor(input.Family, domain.VariantFor(state.ThemeMode, input.SystemDark))
		if chosen.Family != input.Family || state.Theme == chosen.ID {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Theme = chosen.ID }, Persists: true}

	case contracts.ViewCommandViewSetThemeMode:
		if !domain.IsThemeMode(input.Mode) {
			return SliceTransition{}
		}
		chosen := domain.ThemeFor(domain.ThemeOrDefault(state.Theme).Family, domain.VariantFor(input.Mode, input.SystemDark))
		if state.ThemeMode == input.Mode && state.Theme == chosen.ID {
			return SliceTransition{}
		}
		mode := input.Mode
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.ThemeMode = mode
			next.Theme = chosen.ID
		}, Persists: true}

	case contracts.ViewCommandViewSystemScheme:
		if state.ThemeMode != domain.ThemeModeAuto {
			return SliceTransition{}
		}
		chosen := domain.ThemeFor(domain.ThemeOrDefault(state.Theme).Family, domain.VariantFor(domain.ThemeModeAuto, input.Dark))
		if state.Theme == chosen.ID {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Theme = chosen.ID }}

	case contracts.ViewCommandViewSetUIFont:
		if domain.UiFontByID(input.Font) == nil || state.UIFont == input.Font {
			return SliceTransition{}
		}
		font := input.Font
		return SliceTransition{Patch: func(next *WorkspaceState) { next.UIFont = font }, Persists: true}

	case contracts.ViewCommandViewSetMonoFont:
		if domain.MonoFontByID(input.Font) == nil || state.MonoFont == input.Font {
			return SliceTransition{}
		}
		font := input.Font
		return SliceTransition{Patch: func(next *WorkspaceState) { next.MonoFont = font }, Persists: true}

	case contracts.ViewCommandViewSetReadingSize:
		size, ok := domain.SizeByID(domain.ReadingSize, input.Size)
		if !ok || state.ReadingSize == size {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ReadingSize = size }, Persists: true}

	case contracts.ViewCommandViewSetTerminalSize:
		size, ok := domain.SizeByID(domain.TerminalSize, input.Size)
		if !ok || state.TerminalSize == size {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.TerminalSize = size }, Persists: true}

	case contracts.ViewCommandViewSetActivityProject:
		return setPreference(state, "activityProject", input.ProjectID)

	case contracts.ViewCommandViewSetSidebarOpen:
		return setPreference(state, "sidebarOpen", input.Open)

	case contracts.ViewCommandViewSetUsageComputer:
		usageComputer := PairedID(&state.Computers, input.ComputerID)
		if !samePointee(usageComputer, input.ComputerID) || samePointee(usageComputer, state.UsageComputer) {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.UsageComputer = usageComputer }}

	case contracts.ViewCommandViewSetSectionOpen:
		if open, held := state.Sections[input.Section]; !domain.IsSidebarSection(input.Section) || held && open == input.Open {
			return SliceTransition{}
		}
		sections := maps.Clone(state.Sections)
		if sections == nil {
			sections = domain.SidebarSections{}
		}
		sections[input.Section] = input.Open
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Sections = sections }, Persists: true}

	case contracts.ViewCommandViewSetSubagentGroup:
		if open, held := state.SubagentGroups[input.Group]; !domain.IsSubagentGroup(input.Group) || held && open == input.Open {
			return SliceTransition{}
		}
		groups := maps.Clone(state.SubagentGroups)
		if groups == nil {
			groups = domain.SubagentGroups{}
		}
		groups[input.Group] = input.Open
		return SliceTransition{Patch: func(next *WorkspaceState) { next.SubagentGroups = groups }, Persists: true}

	case contracts.ViewCommandViewFocusComposer:
		effects := TakeKeys
		if input.Recover != nil && *input.Recover && !state.Focused {
			effects = []WorkspaceEffect{}
		}
		return SliceTransition{Edits: []OwnerEdit{FocusComposerIn(ComposerOf(state))}, Effects: effects}

	case contracts.ViewCommandViewSetShortcut:
		if domain.ShortcutsShortcutAction(input.Action) == nil {
			return SliceTransition{}
		}
		if input.Binding != nil {
			if problem, refused := domain.ShortcutProblem(*input.Binding); refused && problem != "" {
				return SliceTransition{Patch: func(next *WorkspaceState) { next.CapturingShortcut = nil }, Refusal: &problem, Effects: StopCapture(state)}
			}
		}
		shortcuts := domain.WithShortcut(state.Shortcuts, input.Action, input.Binding)
		patch := func(next *WorkspaceState) {
			next.Shortcuts = shortcuts
			next.CapturingShortcut = nil
		}
		effects := slices.Concat(savedWith(state, patch), []WorkspaceEffect{WorkspaceEffectApplyShortcuts{Overrides: shortcuts}}, StopCapture(state))
		return SliceTransition{Patch: patch, ClearsError: true, Effects: effects}

	case contracts.ViewCommandViewResetShortcuts:
		shortcuts := domain.ShortcutOverrides{}
		patch := func(next *WorkspaceState) {
			next.Shortcuts = shortcuts
			next.CapturingShortcut = nil
		}
		effects := slices.Concat(savedWith(state, patch), []WorkspaceEffect{WorkspaceEffectApplyShortcuts{Overrides: shortcuts}}, StopCapture(state))
		return SliceTransition{Patch: patch, ClearsError: true, Effects: effects}

	case contracts.ViewCommandViewCaptureShortcut:
		if input.Action != nil && domain.ShortcutsShortcutAction(*input.Action) == nil || samePointee(state.CapturingShortcut, input.Action) {
			return SliceTransition{}
		}
		action := input.Action
		return SliceTransition{
			Patch:       func(next *WorkspaceState) { next.CapturingShortcut = action },
			ClearsError: true,
			Effects:     []WorkspaceEffect{WorkspaceEffectCaptureShortcut{Capturing: action != nil}},
		}

	case contracts.ShortcutEventShortcutCaptured:
		action := state.CapturingShortcut
		if action == nil || *action == "" {
			return SliceTransition{}
		}
		if input.Binding == nil {
			return SliceTransition{Patch: func(next *WorkspaceState) { next.CapturingShortcut = nil }, Effects: StopCapture(state)}
		}
		return ReduceSettings(state, contracts.ViewCommandViewSetShortcut{Action: *action, Binding: input.Binding})

	case contracts.ShortcutEventShortcutUnavailable:
		current := state.DesktopShortcutUnavailable
		if current != nil && current.Binding == input.Refusal.Binding && current.Message == input.Refusal.Message {
			return SliceTransition{}
		}
		refusal := input.Refusal
		return SliceTransition{Patch: func(next *WorkspaceState) { next.DesktopShortcutUnavailable = &refusal }}

	case contracts.ViewCommandViewInspectSubagent:
		return SliceTransition{Effects: inspectSubagent(state, TargetID(state, input.TaskID), input.SubagentID)}

	case WorkspaceEventSubagentActivityLoaded:
		if !slices.ContainsFunc(state.Subagents[input.TaskID], func(subagent *domain.Subagent) bool { return subagent.ID == input.SubagentID }) {
			return SliceTransition{}
		}
		return SliceTransition{Edits: []OwnerEdit{SubagentActivityIn(RunsOf(state), input.TaskID, input.SubagentID, input.Activity)}}

	case contracts.ViewCommandViewSetCaptureOptions:
		if input.Options.Sound == state.CaptureSound && input.Options.Focus == state.CaptureFocus {
			return SliceTransition{}
		}
		options := input.Options
		patch := func(next *WorkspaceState) {
			next.CaptureSound = options.Sound
			next.CaptureFocus = options.Focus
		}
		return SliceTransition{Patch: patch, Effects: slices.Concat(savedWith(state, patch), []WorkspaceEffect{WorkspaceEffectApplyCaptureOptions{Options: options}})}

	case contracts.ViewCommandViewSetMemoryProtection:
		enabled := input.Enabled
		return SliceTransition{
			Patch:    func(next *WorkspaceState) { next.MemoryProtection = enabled },
			Persists: true,
			Effects:  []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.RunCommandMemoryProtection{Enabled: enabled}}},
		}

	case contracts.ViewCommandViewSetChromeBrowser:
		return setSwitch(state.ChromeBrowser, input.Enabled, func(next *WorkspaceState, enabled bool) { next.ChromeBrowser = enabled })
	case contracts.ViewCommandViewSetComputerUse:
		return setSwitch(state.ComputerUse, input.Enabled, func(next *WorkspaceState, enabled bool) { next.ComputerUse = enabled })
	case contracts.ViewCommandViewSetBrowserTools:
		return setSwitch(state.BrowserTools, input.Enabled, func(next *WorkspaceState, enabled bool) { next.BrowserTools = enabled })
	case contracts.ViewCommandViewSetNotifications:
		return setSwitch(state.Notifications, input.Enabled, func(next *WorkspaceState, enabled bool) { next.Notifications = enabled })

	case contracts.ViewCommandViewSetSessionPanelOpen:
		if state.SessionPanelOpen == input.Open {
			return SliceTransition{}
		}
		open := input.Open
		return SliceTransition{Patch: func(next *WorkspaceState) { next.SessionPanelOpen = open }, Persists: true}

	case contracts.ViewCommandViewSetSettingsOpen:
		var asked *string
		if input.Section != nil {
			section := string(*input.Section)
			asked = &section
		}
		return SliceTransition(SettingsShown(state, input.Open, asked, input.SettingID))
	}
	return SliceTransition{}
}

// StopCapture: settings stop waiting for a keystroke the moment they are no longer the thing in front.
func StopCapture(state *WorkspaceState) []WorkspaceEffect {
	if state.CapturingShortcut == nil {
		return []WorkspaceEffect{}
	}
	return []WorkspaceEffect{WorkspaceEffectCaptureShortcut{Capturing: false}}
}

// setSwitch turns one of the switches that change what a run may do, writing nothing when it already holds.
func setSwitch(held, enabled bool, write func(next *WorkspaceState, enabled bool)) SliceTransition {
	if held == enabled {
		return SliceTransition{}
	}
	return SliceTransition{Patch: func(next *WorkspaceState) { write(next, enabled) }, Persists: true}
}
