package application

import (
	"reflect"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// ownViewKeys is everything the window paints from its own state whichever computer's thread is on
// screen: the chrome, the settings, and the lists that merge every computer's threads. JSON names of
// OwnWorkspaceView fields.
var ownViewKeys = []string{
	"unreadCount", "sideChatAttention", "threads", "orderedThreads", "projects", "startProjects", "projectSettings", "activityThreads", "threadSlots",
	"runningThreadIds", "monitoringThreadIds", "blockedThreadIds", "stoppableCoordinatorIds", "worktreeThreadIds", "worktreeGroups", "worktreesById", "schedules", "archivedThreads",
	"managedWorktrees", "worktreeSettings", "worktreeManagementError", "worktreeManagementNotice", "worktreeDeleteConfirmation",
	"installedApps", "cli", "planUsage", "computerUsePermissions", "computerUseSetup", "storageError", "hiddenThreads", "restored", "viewingImage", "viewingGallery",
	"projectAdd", "projectEditor", "worktreeMove", "forkName", "archivePrompt", "sections", "subagentGroups", "theme", "themeMode", "uiFont", "monoFont", "readingSize", "terminalSize",
	"activityProject", "sidebarOpen", "sessionPanelOpen", "captureSound", "captureFocus", "chromeBrowser", "computerUse", "browserTools", "memoryProtection", "notifications",
	"favoriteModels", "recentProjects", "shortcuts", "capturingShortcut", "desktopShortcutUnavailable", "composerFocus", "settingsOpen", "settingsSection", "settingsFocus", "usageComputer",
	"openMenu", "jump", "zen", "remote", "remoteChecking", "appUpdate", "visibleToasts", "automationEdits", "canGoBack", "canGoForward", "browserOrigins", "browserImport",
	"computerLinks", "computerUsage", "computerUpkeep", "activeComputer", "computerName", "computerFilter", "computerPairing", "computersFound", "computersSearching", "computersSearchError", "threadHosts", "projectHosts",
	"threadsByCoordinator", "waitingLines", "coordinatorsByComputer", "dictation",
}

var remoteEngineSettings = OwnWorkspaceViewEngineSettingsPage{Section: domain.SettingsSectionPhone, SettingID: new("phone.computers")}

var workspaceViewOverlayReusedSideChats = HeldViews(func(view *SideChatView) string { return view.ID })

var workspaceViewOverlayNoOverviewFolds = OwnWorkspaceViewOverviewFolds{}

var workspaceViewOverlayReusedThreadTabs = HeldViews(func(view *ThreadTabView) string { return view.ID })

// localDraftView is the drafts typed in this window under one key (TS: localDraft's return).
type localDraftView struct {
	Prompt           string
	Annotations      []*domain.Annotation
	AnnotationReveal *AnnotationReveal
	Pastes           []*domain.PastedText
	Images           []*domain.StagedImage
	Files            []*domain.AttachedFile
}

// localDraft: main and side composers both read the drafts typed in this window.
func localDraft(state *WorkspaceState, key string) localDraftView {
	return localDraftView{
		Prompt:           state.Prompts[key],
		Annotations:      AnnotationsFor(state, key),
		AnnotationReveal: AnnotationRevealFor(state, key),
		Pastes:           PastesFor(state, key),
		Images:           ImagesFor(state, key),
		Files:            FilesFor(state, key),
	}
}

var remoteSimulators = func() domain.SimulatorState {
	simulators := domain.NoSimulators
	simulators.Availability = domain.SimulatorAvailabilityUnsupported{Reason: "The simulator only runs on the computer you are using."}
	return simulators
}()

// OverlaidView is the window over a paired computer's thread: that computer's view of it, under this
// window's own chrome and drafts.
func OverlaidView(state *WorkspaceState, own *OwnWorkspaceView, remote *WorkspaceState, derive func(state *WorkspaceState, window WorktreeMenuState) *OwnWorkspaceView) *OwnWorkspaceView {
	// The location menu opens and is searched here, over the other computer's threads and checkouts.
	shown := derive(remote, WorktreeMenuState{OpenMenu: state.OpenMenu, WorktreeMenuSearch: state.WorktreeMenuSearch})
	view := *shown
	target := reflect.ValueOf(&view).Elem()
	source := reflect.ValueOf(own).Elem()
	for _, index := range ownViewFields() {
		target.Field(index).Set(source.Field(index))
	}
	// Drafts stay where they are typed, so the other computer never hears a keystroke.
	draft := localDraft(state, PromptKey(remote))
	view.Prompt, view.Annotations, view.AnnotationReveal = draft.Prompt, draft.Annotations, draft.AnnotationReveal
	view.Pastes, view.Images, view.Files = draft.Pastes, draft.Images, draft.Files
	chats := make([]*SideChatView, len(shown.SideChats))
	for index, chat := range shown.SideChats {
		drafted := *chat
		local := localDraft(state, chat.ID)
		drafted.Prompt, drafted.Annotations, drafted.AnnotationReveal = local.Prompt, local.Annotations, local.AnnotationReveal
		drafted.Pastes, drafted.Images, drafted.Files = local.Pastes, local.Images, local.Files
		chats[index] = &drafted
	}
	view.SideChats = workspaceViewOverlayReusedSideChats(chats)
	tabs := make([]*ThreadTabView, len(shown.ThreadTabs))
	for index, tab := range shown.ThreadTabs {
		drafted := *tab
		local := localDraft(state, tab.ID)
		drafted.Prompt, drafted.Annotations, drafted.AnnotationReveal = local.Prompt, local.Annotations, local.AnnotationReveal
		drafted.Pastes, drafted.Images, drafted.Files = local.Pastes, local.Images, local.Files
		tabs[index] = &drafted
	}
	view.ThreadTabs = workspaceViewOverlayReusedThreadTabs(tabs)
	view.AttachmentSends = state.AttachmentSends
	// Terminal search runs in this window's xterm, using the output it has received.
	switch {
	case findsTerminal(own.Find):
		view.Find = own.Find
	case findsTerminal(shown.Find):
		view.Find = nil
	default:
		view.Find = shown.Find
	}
	view.ReadingPoint = nil
	view.OverviewFolds = workspaceViewOverlayNoOverviewFolds
	if shown.CurrentThread != nil {
		view.ReadingPoint = state.ReadingPoints[shown.CurrentThread.ID]
		// Folding an Overview is this window's own view of the coordinator, wherever the coordinator runs.
		if folds := state.OverviewFolds[shown.CurrentThread.ID]; folds != nil {
			view.OverviewFolds = OwnWorkspaceViewOverviewFolds(*folds)
		}
	}
	// Browser surfaces still belong to the host's native window.
	view.BrowserTabs = []*domain.BrowserTab{}
	view.BrowserApproval = nil
	// So do simulators: this Mac cannot stream or tap a device the other computer runs.
	view.Simulator = remoteSimulators
	view.SimulatorID = nil
	// A delete confirmed here may name the other computer's checkout, which only its list has.
	view.WorktreeDeleteConfirmation = own.WorktreeDeleteConfirmation
	if view.WorktreeDeleteConfirmation == nil {
		for _, item := range shown.ManagedWorktrees {
			if state.WorktreeSettings.Confirming != nil && item.Root == *state.WorktreeSettings.Confirming && !item.Deleting {
				view.WorktreeDeleteConfirmation = item
				break
			}
		}
	}
	// That computer's engines are upgraded from its row on the Computers page, not this one's Engines page.
	view.EngineSettingsPage = remoteEngineSettings
	view.ActionError = shown.ActionError
	if state.ActionError != nil {
		view.ActionError = state.ActionError
	}
	view.ActionErrorPage = shown.ActionErrorPage
	if state.ActionError != nil && *state.ActionError != "" {
		view.ActionErrorPage = (*OwnWorkspaceViewActionErrorPage)(state.ActionErrorPage)
	}
	return &view
}

func findsTerminal(find *FindView) bool {
	if find == nil {
		return false
	}
	_, terminal := find.Target.(domain.FindTargetTerminal)
	return terminal
}

var ownViewIndexes = sync.OnceValue(func() []int {
	keys := map[string]bool{}
	for _, key := range ownViewKeys {
		keys[key] = true
	}
	view := reflect.TypeFor[OwnWorkspaceView]()
	var indexes []int
	for index := range view.NumField() {
		name, _, _ := strings.Cut(view.Field(index).Tag.Get("json"), ",")
		if keys[name] {
			indexes = append(indexes, index)
			delete(keys, name)
		}
	}
	if len(keys) > 0 {
		panic("application: ownViewKeys names fields OwnWorkspaceView lacks")
	}
	return indexes
})

// ownViewFields is the OwnWorkspaceView fields ownViewKeys names, by index.
func ownViewFields() []int { return ownViewIndexes() }
