package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// dictationView is the main composer's dictation: whether it can, and what the recording on this
// draft is doing.
func dictationView(state *WorkspaceState) OwnWorkspaceViewDictation {
	dictation := state.Dictation
	view := OwnWorkspaceViewDictation{Model: dictation.Model, Message: dictation.Message, Progress: dictation.Progress, Sends: dictation.Sends}
	if here := dictation.Session; here != nil && here.Key == PromptKey(state) {
		view.Phase = new(here.Phase)
		view.HandsFree = here.HandsFree && here.Phase != domain.DictationPhaseTranscribing
	}
	return view
}

// Each chat's view outlives the derive that built it, so a report elsewhere never redraws one.
var workspaceViewReusedSideChats = HeldViews(func(view *SideChatView) string { return view.ID })
var workspaceViewReusedThreadTabs = HeldViews(func(view *ThreadTabView) string { return view.ID })

var noSubagents = []*domain.Subagent{}
var workspaceViewNoQueued = []*QueuedMessageView{}
var noWorkflows = []*domain.Workflow{}

// EngineFeeds is what the thread's engine reports about its run; a feed the engine cannot fill stays
// empty. `currentThread` may be nil.
func EngineFeeds(capabilities domain.EngineCapabilities, state *WorkspaceState, currentThread *domain.Thread) (subagents []*domain.Subagent, workflows []*domain.Workflow) {
	subagents, workflows = noSubagents, noWorkflows
	if capabilities.Subagents && currentThread != nil {
		if held := state.Subagents[currentThread.ID]; held != nil {
			subagents = held
		}
	}
	if capabilities.Workflows && state.CurrentID != nil && *state.CurrentID != "" {
		if held := state.Workflows[*state.CurrentID]; held != nil {
			workflows = held
		}
	}
	return subagents, workflows
}

// engineView writes the engine in front into `view`: what it is called, what it can feed, and
// whether a picker may still move off it. `currentThread` may be nil.
func engineView(view *OwnWorkspaceView, state *WorkspaceState, currentThread *domain.Thread) {
	engine := state.DraftEngine
	if currentThread != nil {
		engine = currentThread.Engine
	}
	capabilities := domain.CapabilitiesFor(string(engine))
	view.Engine = engine
	// The engine's name, for wording that speaks of the agent running this thread.
	view.EngineLabel = domain.EngineLabel(string(engine))
	// A thread exists from its first message on, and keeps the engine that message went to.
	view.EngineLocked = currentThread != nil
	// Which engines a picker may hand a run to, why the others cannot be picked, and how to fix it.
	view.EngineAccess = OwnWorkspaceViewEngineAccess{
		Claude:   EngineReadinessOf(state, domain.AgentEngineClaude),
		Codex:    EngineReadinessOf(state, domain.AgentEngineCodex),
		Opencode: EngineReadinessOf(state, domain.AgentEngineOpencode),
	}
	// What the engine can feed, so a panel it cannot is not offered for this thread.
	view.Capabilities = capabilities
	// The models this view's computer can run, for every picker and label drawn from it.
	view.Models = CatalogOf(state)
	// Why the thread in front can only be read here: it runs on an engine only a newer build on a
	// paired computer knows. Nil for any thread this build can work in.
	view.Unopenable = nil
	if currentThread != nil && !domain.IsAgentEngine(string(currentThread.Engine)) {
		view.Unopenable = new(domain.UnopenableEngine(string(currentThread.Engine)))
	}
	view.Subagents, view.Workflows = EngineFeeds(capabilities, state, currentThread)
}

// modelView writes the model in front and the effort a run on it gets into `view`.
func modelView(view *OwnWorkspaceView, state *WorkspaceState, currentThread *domain.Thread) {
	engine := state.DraftEngine
	if currentThread != nil {
		engine = currentThread.Engine
	}
	catalog := CatalogOf(state)
	var model *domain.AgentModel
	if currentThread != nil {
		if found, ok := domain.ThreadModel(&catalog, currentThread); ok {
			model = new(found)
		}
	} else {
		model = new(state.DraftModel)
	}
	var draftRole domain.ThreadRole
	if state.DraftRole != nil {
		draftRole = *state.DraftRole
	}
	role := domain.EffortRoleOf(currentThread, draftRole)
	effort := state.DraftEffort
	if currentThread != nil {
		effort = domain.DefaultEffortFor(string(engine))
		if currentThread.Effort != nil {
			effort = *currentThread.Effort
		}
	}
	view.Model = model
	view.Coordinating = role == domain.CoordinationRoleCoordinator
	view.Effort = effort
	if model != nil {
		view.Effort = domain.EffortForModel(&catalog, engine, *model, effort, role)
	}
}

// waitView writes what the thread in front waits on, and what its composer offers, into `view`.
func waitView(view *OwnWorkspaceView, state *WorkspaceState, currentThread *domain.Thread, running bool) {
	var taskID *string
	if currentThread != nil {
		taskID = &currentThread.ID
	}
	waiting, ok := WaitingOn(state, taskID, PromptKey(state))
	view.WaitingOn = nil
	if ok {
		view.WaitingOn = &waiting
	}
	settled := currentThread != nil && !running && !ok
	view.CanCompact = settled && CompactableSession(state, currentThread)
	view.CanReview = settled && ReviewableSession(state, currentThread)
}

// ThreadSlots is the threads ⌘1 through ⌘9 reach, read straight from state so each keystroke follows
// the current visible order.
func ThreadSlots(state *WorkspaceState) []string {
	activity := ThreadActivityThreadActivity(state)
	preferences := SidebarPreferences{Sections: state.Sections, ActivityProject: state.ActivityProject, StartingProposals: state.StartingProposals}
	return SidebarLists(preferences, state.Projects, ThreadActivityThreadLists(state).VisibleThreads, activity.Ranked, activity.Blocked, nil).ThreadSlots
}

var workspaceViewNoOverviewFolds = OwnWorkspaceViewOverviewFolds{}

// engineSettings is the settings page an engine notice opens, and the setting it lands on.
var engineSettings = OwnWorkspaceViewEngineSettingsPage{Section: domain.SettingsSectionEngines, SettingID: nil}

// DeriveView is everything the UI reads, derived in one place so components never reach into raw state.
func DeriveView(state *WorkspaceState) *OwnWorkspaceView {
	own := deriveOwnView(state, WorktreeMenuState{OpenMenu: state.OpenMenu, WorktreeMenuSearch: state.WorktreeMenuSearch}, WorkspaceViewCollections)
	if computer := SelectedComputer(state); computer != nil && computer.State != nil {
		return OverlaidView(state, own, computer.State, func(shown *WorkspaceState, window WorktreeMenuState) *OwnWorkspaceView {
			return deriveOwnView(shown, window, ShownViewCollections)
		})
	}
	ShownViewCollections.Release()
	return own
}

var noWorktreeChoices = []*contracts.MobileWorktreeChoice{}

// deriveOwnView: `window` is whose menu the location row draws, this window's when the thread is a
// paired computer's (TS defaults it to state's own); `collectionsOf` defaults to WorkspaceViewCollections.
func deriveOwnView(state *WorkspaceState, window WorktreeMenuState, collectionsOf *ViewCollections) *OwnWorkspaceView {
	currentThread := currentThread(state)
	draftWorktree := WorktreeByID(state, state.DraftWorktreeID)
	var draftProject *domain.Project
	if state.DraftProjectID != nil && *state.DraftProjectID != "" {
		if index := slices.IndexFunc(state.Projects, func(project *domain.Project) bool { return project.ID == *state.DraftProjectID }); index >= 0 {
			draftProject = state.Projects[index]
		}
	}
	currentProject := draftProject
	if currentThread != nil {
		currentProject = ProjectFor(state, currentThread)
	}
	if collectionsOf == nil {
		collectionsOf = WorkspaceViewCollections
	}
	collections := collectionsOf.Of(state)
	busy, blocked, managedWorktrees, lists := collections.Busy, collections.Blocked, collections.ManagedWorktrees, collections.Lists
	var currentRun *ActiveRunRunning
	var currentRetry *domain.RetryNotice
	if state.CurrentID != nil && *state.CurrentID != "" {
		currentRun = RunRecord(state.ActiveRuns[*state.CurrentID])
		currentRetry = RunRetry(state.ActiveRuns[*state.CurrentID])
	}
	var workspaceID *string
	if currentThread != nil {
		if id, ok := ThreadWorkspaceID(state, currentThread); ok {
			workspaceID = new(id)
		}
	} else if draftProject != nil {
		workspaceID = draftProject.WorkspaceID
	}
	var environment contracts.ChangedFilesResult
	if workspaceID != nil && *workspaceID != "" {
		environment = state.Environments[*workspaceID]
	}
	owner := DockOwner(state, nil)
	dock := DockFor(state, owner)
	key := PromptKey(state)

	view := &OwnWorkspaceView{}
	engineView(view, state, currentThread)
	view.SideChatAttention = collections.SideChatAttention
	view.UnreadCount = collections.UnreadCount
	view.Projects = lists.Projects
	view.OrderedThreads = lists.OrderedThreads
	view.ThreadsByCoordinator = lists.ThreadsByCoordinator
	view.WaitingLines = lists.WaitingLines
	view.ActivityThreads = lists.ActivityThreads
	view.ActivityProject = lists.ActivityProject
	view.ThreadSlots = lists.ThreadSlots
	view.StartProjects = collections.StartProjects
	view.ProjectSettings = collections.ProjectSettings
	view.Threads = collections.ListedThreads
	view.ArchivedThreads = collections.ArchivedThreads
	view.CurrentThread = currentThread
	view.Coordination = collections.Coordination
	view.CoordinatorsByComputer = collections.CoordinatorsByComputer
	view.MemberPullRequests = collections.MemberPullRequests
	view.MemberWorktrees = collections.MemberWorktrees
	view.OverviewFolds = workspaceViewNoOverviewFolds
	if state.CurrentID != nil && *state.CurrentID != "" {
		if folds := state.OverviewFolds[*state.CurrentID]; folds != nil {
			view.OverviewFolds = OwnWorkspaceViewOverviewFolds(*folds)
		}
		view.Goal = state.Goals[*state.CurrentID]
	}
	view.CurrentProject = currentProject
	if currentProject != nil {
		view.Folder = currentProject.Root
		// What that folder is called: the name the user gave the project, else the folder's own.
		view.FolderLabel = domain.ProjectName(currentProject)
	}
	view.Policy = OwnWorkspaceViewPolicy(state.DraftPolicy)
	if currentThread != nil {
		view.Policy = OwnWorkspaceViewPolicy(currentThread.ExecutionPolicy)
	}
	modelView(view, state, currentThread)
	engine := state.DraftEngine
	fastMode := state.DraftFastMode
	if currentThread != nil {
		engine = currentThread.Engine
		fastMode = currentThread.FastMode != nil && *currentThread.FastMode
	}
	view.FastMode = domain.CapabilitiesFor(string(engine)).FastMode && fastMode
	view.Prompt = state.Prompts[key]
	view.Dictation = dictationView(state)
	view.Annotations = AnnotationsFor(state, key)
	view.AnnotationReveal = AnnotationRevealFor(state, key)
	view.Pastes = PastesFor(state, key)
	view.Images = ImagesFor(state, key)
	view.Files = FilesFor(state, key)
	view.Status = RunStatus(state, state.CurrentID)
	view.Compacting = currentRun != nil && currentRun.Status == ActiveRunRunningStatusCompacting
	view.Retrying = currentRetry
	// The usage limit the thread is waiting out, and its place in line.
	positions := domain.LinePositions(state.Threads)
	if currentThread != nil && currentThread.LimitPause != nil {
		pause := currentThread.LimitPause
		limit := &OwnWorkspaceViewLimitPause{ResetsAt: pause.ResetsAt, Window: OwnWorkspaceViewLimitPauseWindow(pause.Window), PausedAt: pause.PausedAt, NudgedAt: pause.NudgedAt, Workflow: pause.Workflow, Held: pause.Held}
		if position, ok := positions[currentThread.ID]; ok {
			limit.Position = new(position)
		}
		view.LimitPause = limit
	}
	view.LimitPositions = positions
	view.RunActive = currentRun != nil
	if currentRun != nil && len(currentRun.Questions) > 0 {
		view.Question = currentRun.Questions[0]
	}
	view.QueuedMessages = workspaceViewNoQueued
	if state.CurrentID != nil && *state.CurrentID != "" {
		if queued := QueuedView(state, *state.CurrentID); queued != nil {
			view.QueuedMessages = queued
		}
	}
	view.RunningThreadIDs = collections.EveryBusy
	view.MonitoringThreadIDs = collections.EveryMonitoring
	view.BlockedThreadIDs = collections.EveryBlocked
	view.StoppableCoordinatorIDs = collections.EveryStoppable
	if currentRun != nil && currentRun.Status == ActiveRunRunningStatusAwaitingApproval {
		view.Approval = state.Approvals[currentRun.RunID]
	}
	view.BackgroundProcesses = []*domain.BackgroundProcess{}
	if state.CurrentID != nil && *state.CurrentID != "" {
		if processes := state.BackgroundProcesses[*state.CurrentID]; processes != nil {
			view.BackgroundProcesses = processes
		}
		view.StreamingTail = state.StreamingTails[*state.CurrentID]
		view.ReadingPoint = state.ReadingPoints[*state.CurrentID]
	}
	// The workflow this thread's panel is on, which outlives a move to another thread and back.
	view.InspectedWorkflow = WorkflowByID(state, dock.WorkflowID)
	for _, item := range state.Automations {
		if state.CurrentID != nil && item.TaskID == *state.CurrentID {
			view.Automation = item
			break
		}
	}
	view.AutomationEdits = state.AutomationEdits
	view.Schedules = collections.Schedules
	if currentThread != nil {
		// When a run on this thread last found something, which is what the automation panel reports.
		view.LastFoundAt = currentThread.LastFindingAt
		// What its last silent tick looked at, which is all a schedule that never speaks has to show.
		view.LastChecked = currentThread.LastChecked
	}
	view.WorktreeThreadIDs = collections.WorktreeThreadIDs
	view.WorktreeGroups = collections.WorktreeGroups
	view.WorktreesByID = collections.WorktreesByID
	view.ManagedWorktrees = managedWorktrees
	view.WorktreeSettings = collections.WorktreeSettings
	view.WorktreeManagementError = state.WorktreeManagementError
	view.WorktreeManagementNotice = state.WorktreeManagementNotice
	view.Location = ThreadLocationLocationOf(state, currentThread)
	if menu := WorktreeMenuWorktreeMenuView(state, currentThread, collections.VisibleThreads, &window); menu != nil {
		view.WorktreeMenu = (*OwnWorkspaceViewWorktreeMenu)(menu)
	}
	for _, item := range managedWorktrees {
		if state.WorktreeSettings.Confirming != nil && item.Root == *state.WorktreeSettings.Confirming && !item.Deleting {
			view.WorktreeDeleteConfirmation = item
			break
		}
	}
	waitView(view, state, currentThread, currentRun != nil)
	// The checkout the current thread works in, which is what Git is read from and moved.
	view.WorkspaceID = workspaceID
	view.DraftBranch = state.DraftBranch
	view.DraftWorktree = state.DraftWorktree
	view.DraftWorktreeID = state.DraftWorktreeID
	view.DraftRole = (*OwnWorkspaceViewDraftRole)(state.DraftRole)
	// What the composer calls the checkout a draft starts in, when the user picked one.
	if draftWorktree != nil {
		view.DraftWorktreeName = new(domain.WorktreeName(draftWorktree))
	}
	// The project's existing checkouts a draft can start in instead.
	view.DraftWorktreeChoices = noWorktreeChoices
	if currentThread == nil && currentProject != nil {
		view.DraftWorktreeChoices = WorktreeChoices(state, currentProject.ID, state.DraftWorktreeID)
	}
	view.Environment = environment
	// The pull request that checkout's work belongs to, drawn only while the answer is still its own.
	view.PullRequest = PullRequestFor(state.PullRequest, workspaceID, environment)
	view.AttachmentSends = state.AttachmentSends
	view.InstalledApps = state.InstalledApps
	view.Cli = state.Cli
	view.PlanUsage = state.PlanUsage
	// The limits of the account the thread on screen runs on, which is the paired computer's own for a thread there.
	view.ThreadPlanUsage = state.PlanUsage
	view.ComputerUsePermissions = state.ComputerUsePermissions
	view.StorageError = state.StorageError
	view.HiddenThreads = state.HiddenThreads
	view.ActionError = state.ActionError
	view.ViewingImage = state.ViewingImage
	view.ViewingGallery = state.ViewingGallery
	view.ActionErrorPage = (*OwnWorkspaceViewActionErrorPage)(state.ActionErrorPage)
	view.Restored = state.Restored
	view.ComputerUseSetup = state.ComputerUseSetup
	dialogs := WorkspaceDialogsView(state)
	view.ProjectAdd = dialogs.ProjectAdd
	view.ProjectEditor = dialogs.ProjectEditor
	view.WorktreeMove = dialogs.WorktreeMove
	view.ForkName = dialogs.ForkName
	view.ArchivePrompt = dialogs.ArchivePrompt
	view.Sections = state.Sections
	view.SubagentGroups = state.SubagentGroups
	view.Theme = state.Theme
	view.ThemeMode = state.ThemeMode
	view.UIFont = state.UIFont
	view.MonoFont = state.MonoFont
	view.ReadingSize = state.ReadingSize
	view.TerminalSize = state.TerminalSize
	view.SidebarOpen = state.SidebarOpen
	view.SessionPanelOpen = state.SessionPanelOpen
	view.CaptureSound = state.CaptureSound
	view.CaptureFocus = state.CaptureFocus
	view.ChromeBrowser = state.ChromeBrowser
	view.ComputerUse = state.ComputerUse
	view.MemoryProtection = state.MemoryProtection
	view.BrowserTools = state.BrowserTools
	view.Notifications = state.Notifications
	view.FavoriteModels = state.FavoriteModels
	view.RecentProjects = state.RecentProjects
	view.Shortcuts = domain.ShortcutSettings(state.Shortcuts)
	view.CapturingShortcut = state.CapturingShortcut
	view.DesktopShortcutUnavailable = state.DesktopShortcutUnavailable
	view.ComposerFocus = state.ComposerFocus
	// Only the dock on screen can take the keys, so a request in another thread's dock is not drawn.
	if state.DockFocus != nil && state.DockFocus.Owner == owner {
		view.DockFocus = state.DockFocus
	}
	// Asking for computer use opens settings whether or not the user did.
	view.SettingsOpen = state.SettingsOpen || state.ComputerUseSetup
	if state.ComputerUseSetup {
		view.SettingsSection = new(OwnWorkspaceViewSettingsSectionComputerUse)
	} else {
		view.SettingsSection = (*OwnWorkspaceViewSettingsSection)(state.SettingsSection)
		view.SettingsFocus = state.SettingsFocus
	}
	view.UsageComputer = PairedID(&state.Computers, state.UsageComputer)
	view.EngineChecking = state.EngineChecking
	view.EngineUpdating = state.EngineUpdating
	// Where the composer's engine notice sends the user to fix it.
	view.EngineSettingsPage = engineSettings
	view.AgentSettingsReload = OwnWorkspaceViewAgentSettingsReload(state.AgentSettingsReload)
	view.DockOpen = dock.Open
	view.DockExpanded = dock.Expanded
	view.DockPanels = dock.Panels
	// The thread tabs the review and the automation panel show in place of the dock's own thread.
	view.ReviewSubject = DockSubject(state, owner, DiffPanel)
	view.AutomationSubject = DockSubject(state, owner, AutomationPanel)
	// The review this thread has open, whether or not the panel drawing it is the tab in front.
	view.Diff = *DiffFor(state, owner)
	view.DockTab = dock.Tab
	view.BrowserTabs = dock.BrowserTabs
	view.BrowserApproval = state.BrowserApproval
	view.BrowserOrigins = state.BrowserOrigins
	view.BrowserImport = state.BrowserImport
	// The simulators, and the one this dock's panel shows.
	view.Simulator = state.Simulator
	view.SimulatorID = dock.SimulatorID
	view.Terminals = dock.Terminals
	view.CurrentFolder = CurrentFolder(state)
	view.OpenMenu = state.OpenMenu
	if currentThread != nil && state.ReviewPicker != nil && state.ReviewPicker.TaskID == currentThread.ID && !busy.Has(currentThread.ID) {
		view.ReviewPicker = state.ReviewPicker
	}
	view.Find = WorkspaceFindFindView(state, currentThread)
	view.Jump = WorkspaceJumpJumpView(state, busy)
	view.Zen = state.Zen
	view.ZenAvailable = currentThread != nil && len(currentThread.Messages) > 0
	view.Remote = state.Remote
	view.RemoteChecking = state.RemoteChecking
	view.AppUpdate = state.AppUpdate
	view.VisibleToasts = VisibleToasts(state)
	// The paired computers as the chrome draws them, and the one whose thread is on screen.
	view.ComputerLinks = collections.ComputerLinks
	// Each paired computer's own plan limits, beside this computer's `planUsage`.
	view.ComputerUsage = collections.ComputerUsage
	// Each paired computer's version, and what moves it or its engines to a newer one.
	view.ComputerUpkeep = collections.ComputerUpkeep
	for _, link := range collections.ComputerLinks {
		if state.Computers.Active != nil && link.ID == *state.Computers.Active {
			view.ActiveComputer = link
			break
		}
	}
	view.ComputerName = state.Computers.Name
	view.ComputerFilter = state.Computers.Filter
	view.ComputerPairing = state.Computers.Pairing
	view.ComputersFound = state.Computers.Found
	view.ComputersSearching = state.Computers.Searching
	view.ComputersSearchError = state.Computers.SearchError
	view.ThreadHosts = collections.Remote.ThreadHosts
	view.ProjectHosts = collections.Remote.ProjectHosts
	_, view.CanGoBack = ReachableVisit(state, -1)
	_, view.CanGoForward = ReachableVisit(state, 1)
	chats := []*SideChatView{}
	for _, chat := range DockSideChats(state, owner) {
		chats = append(chats, SideChatViewSideChatView(state, chat)...)
	}
	view.SideChats = workspaceViewReusedSideChats(chats)
	tabs := []*ThreadTabView{}
	for _, taskID := range dock.ThreadTabs {
		tabs = append(tabs, SideChatViewThreadTabView(state, taskID, busy, blocked)...)
	}
	view.ThreadTabs = workspaceViewReusedThreadTabs(tabs)
	return view
}
