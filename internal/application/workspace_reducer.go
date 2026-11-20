package application

import (
	"fmt"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"maps"
	"reflect"
	"slices"
)

// Reduce is the single writer for workspace state. Commands come from the UI and anything else
// driving the app; events report what the outside world did back.
func Reduce(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	transition := reduceInput(state, input)
	reduced := &transition.State
	next := SettledArchivePrompt(SettledZen(state, reduced))
	if next != reduced {
		transition.State = *next
	}
	return transition
}

func reduceInput(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	switch input := input.(type) {
	case contracts.ViewCommandViewShortcut:
		return runCommands(state, ShortcutCommands(state, input.Action, input.Surface))
	case contracts.ViewCommandViewEscape:
		return runCommands(state, EscapeCommands(state))
	case contracts.ViewCommandViewForkConfirm:
		return confirmFork(state, input.Title)
	case contracts.ViewCommandViewArchiveConfirm:
		if state.ArchivePrompt == nil {
			return Settled(state, nil, nil)
		}
		cleared := *state
		cleared.ArchivePrompt = nil
		return Reduce(&cleared, contracts.TaskCommandTaskArchive{TaskID: *state.ArchivePrompt})
	case WorkspaceEventAgentEvents:
		transition := Settled(state, nil, nil)
		for _, event := range input.Events {
			next := Reduce(&transition.State, AgentEventInput(event))
			transition = combineTransitions(transition, next)
		}
		return transition
	}
	route := RouteInput(state, input)
	if refuse, ok := route.(InputRouteRefuse); ok {
		return Rejected(state, refuse.Message, nil)
	}
	computer, toComputer := route.(InputRouteComputer)
	if toComputer && (computer.Also == nil || !*computer.Also) {
		return workspaceReducerForwarded(state, computer)
	}
	var carried []WorkspaceEffect
	if toComputer {
		carried = []WorkspaceEffect{forwardEffect(computer)}
	}
	left := leavingComputer(state, input)
	applied := ReconcileSimulators(left, ReconcileAppUpdate(left, ReconcileCoordination(left, ReconcileLimitPauses(left, ReconcileSnoozes(left, Apply(left, input), input), input), input)))
	effects := applied.Effects
	if _, loaded := input.(WorkspaceEventPreferencesLoaded); loaded && applied.State.MemoryProtection != state.MemoryProtection {
		effects = slices.Concat(effects, []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.RunCommandMemoryProtection{Enabled: applied.State.MemoryProtection}}})
	}
	effects = slices.Concat(leftBehind(state, left), carried, effects)
	transition := WorkspaceTransition{State: *SettledUnlisted(left, WithOverviewPanels(PrunedWorkflowPanels(PrunedFind(&applied.State)))), Effects: effects, Result: applied.Result}
	if needsBrowserPermissions(state, &transition, input) {
		transition.Effects = slices.Concat([]WorkspaceEffect{WorkspaceEffectBrowserPermissions{Permissions: BrowserPermissions(&transition.State)}}, transition.Effects)
	}
	if samePointee(transition.State.CurrentID, state.CurrentID) {
		if transition.Effects == nil {
			transition.Effects = []WorkspaceEffect{}
		}
		return transition
	}
	if menu := transition.State.OpenMenu; menu != nil && (*menu == "session:location" || *menu == ThreadStartProjectMenu) {
		transition.State.OpenMenu = nil
	}
	if transition.State.DockFocus != nil && transition.State.DockFocus == state.DockFocus {
		transition.State.DockFocus = nil
	}
	landed := &transition.State
	switch input.(type) {
	case contracts.ViewCommandViewGoBack, contracts.ViewCommandViewGoForward:
	default:
		if landed.CurrentID != nil {
			landed = RecordVisit(landed, *landed.CurrentID)
		}
	}
	var takeKeys []WorkspaceEffect
	if landed.Focused {
		takeKeys = TakeKeys
	}
	return WorkspaceTransition{State: *landed, Effects: slices.Concat(transition.Effects, ShownPageEffects(landed), takeKeys), Result: transition.Result}
}

// confirmFork forks under the typed name, wherever the thread is held.
func confirmFork(state *WorkspaceState, typed string) WorkspaceTransition {
	ask := state.ForkName
	title := domain.ClampTitle(typed)
	if ask == nil || title == "" {
		return Settled(state, nil, nil)
	}
	taskID := ask.TaskID
	fork := contracts.TaskCommandTaskFork{TaskID: &taskID, Title: &title}
	if ask.Worktree {
		worktree := true
		fork.Worktree = &worktree
	}
	if holder := ComputerOfThread(state, ask.TaskID); holder != nil && !contracts.SupportsComputerCommand(holder.Capabilities, fork) {
		fork.Title = nil
	}
	cleared := *state
	cleared.ForkName = nil
	return Reduce(&cleared, fork)
}

func forwardEffect(route InputRouteComputer) WorkspaceEffect {
	return WorkspaceEffectComputerForward{ID: route.Computer.ID, Inputs: route.Inputs, Draft: route.Draft}
}

// leavingComputer takes the paired computer off screen for a command that moves this window to one of its own threads.
func leavingComputer(state *WorkspaceState, input WorkspaceInput) *WorkspaceState {
	if state.Computers.Active == nil && state.Computers.Dropped == nil || !LeavesComputer(state, input) {
		return state
	}
	next := *state
	next.Computers.Active = nil
	next.Computers.Dropped = nil
	return &next
}

// leftBehind tells a computer whose thread the window has just left that nobody here is looking any more.
func leftBehind(before, after *WorkspaceState) []WorkspaceEffect {
	left := ActiveComputer(before)
	if left == nil || after.Computers.Active != nil && *after.Computers.Active == left.ID {
		return []WorkspaceEffect{}
	}
	return []WorkspaceEffect{WorkspaceEffectComputerForward{ID: left.ID, Inputs: []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: false}}}}
}

// workspaceReducerForwarded is a command on its way to the computer holding its thread.
func workspaceReducerForwarded(state *WorkspaceState, route InputRouteComputer) WorkspaceTransition {
	next := state
	if route.Draft != nil && route.Draft.Attachments != nil {
		key := route.Draft.Attachments.Key
		send := AttachmentSendFor(state.AttachmentSends, &key)
		if send.Busy {
			return Settled(state, nil, nil)
		}
		busy := *send
		busy.Busy = true
		busy.Error = nil
		copied := *next
		copied.AttachmentSends = maps.Clone(next.AttachmentSends)
		if copied.AttachmentSends == nil {
			copied.AttachmentSends = map[string]*AttachmentSendState{}
		}
		copied.AttachmentSends[key] = &busy
		next = &copied
	}
	if route.Select != nil && *route.Select {
		copied := *next
		active := route.Computer.ID
		copied.Computers.Active = &active
		copied.Computers.Dropped = nil
		copied.ActionError = nil
		next = &copied
	}
	return WorkspaceTransition{State: *next, Effects: slices.Concat(leftBehind(state, next), []WorkspaceEffect{forwardEffect(route)}), Result: contracts.WorkspaceCommandResultTrue{}}
}

// AgentEventInput is which channel a report arrived on: a run's own, or the thread's, which outlives every run.
func AgentEventInput(event contracts.AgentEvent) WorkspaceInput {
	switch event := event.(type) {
	case contracts.AgentSettingsReloadEvent:
		return event
	case contracts.RunEvent:
		return WorkspaceEventRunEvent{Event: event}
	case contracts.ThreadEvent:
		return WorkspaceEventThreadEvent{Event: event}
	}
	panic(fmt.Sprintf("application: agent event %T is neither a run's nor a thread's", event))
}

func runCommands(state *WorkspaceState, commands []contracts.AppCommand) WorkspaceTransition {
	transition := Settled(state, nil, nil)
	for _, command := range commands {
		next := Reduce(&transition.State, command)
		transition = combineTransitions(transition, next)
	}
	return transition
}

func combineTransitions(previous, next WorkspaceTransition) WorkspaceTransition {
	combined := next
	combined.Effects = slices.Concat(previous.Effects, next.Effects)
	if combined.Effects == nil {
		combined.Effects = []WorkspaceEffect{}
	}
	if previous.Result != nil {
		if _, failed := previous.Result.(contracts.WorkspaceCommandResultFalse); failed || next.Result == nil {
			combined.Result = previous.Result
		}
	}
	return combined
}

// EscapeCommands is what Esc means: the nearest layer claims it first, and only when none is open does it reach a run.
func EscapeCommands(state *WorkspaceState) []contracts.AppCommand {
	switch {
	case state.ProjectAdd != nil:
		return []contracts.AppCommand{contracts.ProjectCommandViewAddProjectKey{Key: contracts.ProjectCommandViewAddProjectKeyKeyEscape}}
	case state.ViewingImage != nil && *state.ViewingImage != "":
		return []contracts.AppCommand{contracts.ImageViewCommandImageClose{}}
	case state.Jump != nil:
		return []contracts.AppCommand{contracts.ViewCommandViewJumpClose{}}
	case state.OpenMenu != nil:
		return []contracts.AppCommand{contracts.ViewCommandViewSetMenu{Menu: nil}}
	case state.SettingsOpen || state.ComputerUseSetup:
		return []contracts.AppCommand{contracts.ViewCommandViewSetSettingsOpen{Open: false}}
	case state.Find != nil:
		return []contracts.AppCommand{contracts.ViewCommandViewFindClose{}}
	}
	if taskID := KeyboardThreadID(state); taskID != nil && *taskID != "" {
		return []contracts.AppCommand{contracts.RunControlCommandRunCancel{TaskID: taskID}}
	}
	return []contracts.AppCommand{contracts.RunControlCommandRunCancel{}}
}

// currentProjectId is the project a new thread starts in: the one the thread on screen is in, else the one being drafted.
func currentProjectId(state *WorkspaceState) *string {
	shown := state
	if computer := SelectedComputer(state); computer != nil && computer.State != nil {
		shown = computer.State
	}
	var project *string
	if shown.CurrentID != nil && *shown.CurrentID != "" {
		if thread := findThread(shown.Threads, shown.CurrentID); thread != nil {
			project = thread.ProjectID
		}
	} else {
		project = shown.DraftProjectID
	}
	if project == nil {
		return nil
	}
	return project
}

// ShortcutCommands is what a bound keystroke means.
func ShortcutCommands(state *WorkspaceState, action string, surface domain.ShortcutSurface) []contracts.AppCommand {
	projectID := currentProjectId(state)
	var newThread contracts.AppCommand = contracts.TaskCommandTaskNew{}
	if projectID != nil && *projectID != "" {
		newThread = contracts.TaskCommandTaskNew{ProjectID: projectID}
	}
	var leaving []contracts.AppCommand
	if state.SettingsOpen || state.ComputerUseSetup {
		leaving = []contracts.AppCommand{contracts.ViewCommandViewSetSettingsOpen{Open: false}}
	}
	var leavingZen []contracts.AppCommand
	if state.Zen {
		leavingZen = []contracts.AppCommand{contracts.ViewCommandViewSetZen{Zen: false}}
	}
	var closingJump []contracts.AppCommand
	if state.Jump != nil {
		closingJump = []contracts.AppCommand{contracts.ViewCommandViewJumpClose{}}
	}
	commands := func(parts ...[]contracts.AppCommand) []contracts.AppCommand {
		joined := slices.Concat(parts...)
		if joined == nil {
			joined = []contracts.AppCommand{}
		}
		return joined
	}
	one := func(command contracts.AppCommand) []contracts.AppCommand { return []contracts.AppCommand{command} }

	if slot, ok := domain.SlotShortcutIndex(action); ok {
		if state.KeyboardTab != nil {
			index := slot
			if slot == 8 {
				index = -1
			}
			return one(contracts.ViewCommandViewSelectDockIndex{Index: float64(index)})
		}
		slots := ThreadSlots(state)
		if slot >= len(slots) || slots[slot] == "" {
			return []contracts.AppCommand{}
		}
		return commands(leaving, closingJump, one(contracts.TaskCommandTaskSelect{TaskID: slots[slot]}))
	}
	switch action {
	case "app.check-for-updates":
		return one(contracts.ExternalAppCommandAppCheckForUpdates{})
	case "app.open-source-licenses":
		return one(contracts.ExternalAppCommandAppOpenSourceLicenses{})
	case "thread.new":
		return commands(leaving, one(newThread))
	case "thread.new-pick-project":
		menu := ThreadStartProjectMenu
		return commands(leaving, one(newThread), one(contracts.ViewCommandViewFocusComposer{}), one(contracts.ViewCommandViewSetMenu{Menu: &menu}))
	case "run.cancel":
		return one(contracts.RunControlCommandRunCancel{})
	case "run.allow", "run.deny":
		taskID := KeyboardThreadID(state)
		if taskID == nil {
			taskID = state.CurrentID
		}
		if taskID == nil || *taskID == "" {
			return []contracts.AppCommand{}
		}
		active := RunRecord(state.ActiveRuns[*taskID])
		if active == nil {
			return []contracts.AppCommand{}
		}
		approval := state.Approvals[active.RunID]
		if approval == nil {
			return []contracts.AppCommand{}
		}
		return one(contracts.RunControlCommandRunDecide{TaskID: approval.TaskID, RunID: approval.RunID, ApprovalID: approval.ApprovalID, Allow: action == "run.allow"})
	case "composer.focus":
		var closing []contracts.AppCommand
		if state.OpenMenu != nil {
			closing = one(contracts.ViewCommandViewSetMenu{Menu: nil})
		}
		return commands(closing, one(contracts.ViewCommandViewFocusComposer{}))
	case "effort.increase", "effort.decrease":
		source := state
		if computer := SelectedComputer(state); computer != nil && computer.State != nil {
			copied := *computer.State
			copied.KeyboardTab = state.KeyboardTab
			source = &copied
		}
		tabbed := KeyboardThreadID(source)
		named := tabbed
		if named == nil {
			named = source.CurrentID
		}
		thread := findThread(source.Threads, named)
		catalog := domain.EngineCatalog(source.EngineStatus)
		engine, model, effort, known := source.DraftEngine, source.DraftModel, source.DraftEffort, true
		if thread != nil {
			engine = thread.Engine
			model, known = domain.ThreadModel(catalog, thread)
			effort = domain.DefaultEffortFor(string(engine))
			if thread.Effort != nil {
				effort = *thread.Effort
			}
		}
		role := domain.EffortRoleOf(thread, deref(source.DraftRole))
		if !known {
			return []contracts.AppCommand{}
		}
		choices := domain.EffortsFor(catalog, engine, model, role)
		current := domain.EffortForModel(catalog, engine, model, effort, role)
		index := slices.IndexFunc(choices, func(choice *domain.EffortSpec) bool { return choice.ID == current })
		step := 1
		if action == "effort.increase" {
			step = -1
		}
		if index < 0 || index+step < 0 || index+step >= len(choices) || choices[index+step] == nil {
			return []contracts.AppCommand{}
		}
		command := contracts.TaskCommandTaskSetEffort{Engine: engine, Effort: choices[index+step].ID}
		if tabbed != nil && *tabbed != "" {
			command.TaskID = tabbed
		}
		return one(command)
	case "thread.jump":
		if state.Jump != nil {
			return one(contracts.ViewCommandViewJumpClose{})
		}
		return commands(leaving, one(contracts.ViewCommandViewJumpOpen{}))
	case "project.start":
		start := true
		return commands(leaving, closingJump, one(contracts.ProjectCommandProjectOpen{Start: &start}))
	case "find.open":
		if state.Find != nil {
			return one(contracts.ViewCommandViewFindOpen{})
		}
		return one(contracts.ViewCommandViewFindOpen{Target: FindTargetFor(state, surface)})
	case "find.next", "find.previous":
		delta := 1.0
		if action == "find.previous" {
			delta = -1
		}
		if state.Find != nil {
			return one(contracts.ViewCommandViewFindStep{Delta: delta})
		}
		return one(contracts.ViewCommandViewFindOpen{Target: FindTargetFor(state, surface)})
	case "nav.back":
		if surface == domain.ShortcutSurfaceBrowser {
			return one(contracts.BrowserCommandBrowserGo{Delta: -1})
		}
		return commands(leaving, one(contracts.ViewCommandViewGoBack{}))
	case "nav.forward":
		if surface == domain.ShortcutSurfaceBrowser {
			return one(contracts.BrowserCommandBrowserGo{Delta: 1})
		}
		return commands(leaving, one(contracts.ViewCommandViewGoForward{}))
	case "page.reload":
		return one(contracts.BrowserCommandBrowserReload{})
	case "tab.new":
		return commands(leavingZen, one(contracts.ViewCommandViewNewTab{}))
	case "terminal.focus":
		shown := state
		if computer := SelectedComputer(state); computer != nil && computer.State != nil {
			shown = computer.State
		}
		if terminal := KeyboardTerminalID(shown); terminal != nil && *terminal != "" {
			return commands(one(contracts.ViewCommandViewSetDockOpen{Open: false}), one(contracts.ViewCommandViewFocusComposer{}))
		}
		terminals := DockFor(shown, DockOwner(shown, nil)).Terminals
		if len(terminals) > 0 {
			return commands(leaving, leavingZen, one(contracts.TerminalCommandTerminalSelect{TerminalID: terminals[len(terminals)-1].ID}))
		}
		return commands(leaving, leavingZen, one(contracts.TerminalCommandTerminalOpen{}))
	case "tab.close":
		return one(contracts.ViewCommandViewCloseTab{})
	case "dock.toggle":
		return commands(leavingZen, one(contracts.ViewCommandViewSetDockOpen{Open: state.Zen || !DockFor(state, DockOwner(state, nil)).Open}))
	case "dock.expand":
		return commands(leavingZen, one(contracts.ViewCommandViewSetDockExpanded{Expanded: !DockFor(state, DockOwner(state, nil)).Expanded}))
	case "sidebar.toggle":
		if state.Zen {
			return commands(leavingZen, one(contracts.ViewCommandViewSetSidebarOpen{Open: true}))
		}
		return one(contracts.ViewCommandViewSetSidebarOpen{Open: !state.SidebarOpen})
	case "zen.toggle":
		if state.Zen {
			return commands(leavingZen)
		}
		if ZenAllowed(state) {
			return commands(leaving, closingJump, one(contracts.ViewCommandViewSetZen{Zen: true}))
		}
		return []contracts.AppCommand{}
	case "settings.toggle":
		return one(contracts.ViewCommandViewSetSettingsOpen{Open: !state.SettingsOpen})
	}
	return []contracts.AppCommand{}
}

func BrowserPermissions(state *WorkspaceState) domain.BrowserPermissions {
	autonomous := []string{}
	for _, thread := range state.Threads {
		if thread.ExecutionPolicy == domain.ExecutionPolicyAutonomous {
			autonomous = append(autonomous, thread.ID)
		}
	}
	return domain.BrowserPermissions{Origins: state.BrowserOrigins, AutonomousTaskIDs: autonomous}
}

// needsBrowserPermissions says whether the panel's permissions go out ahead of this input's effects:
// when the origins or a thread's policy moved, the store or the preferences arrived, or a page is
// about to be driven.
func needsBrowserPermissions(before *WorkspaceState, transition *WorkspaceTransition, input WorkspaceInput) bool {
	if !jsonx.Same(transition.State.BrowserOrigins, before.BrowserOrigins) {
		return true
	}
	switch input.(type) {
	case contracts.TaskCommandTaskSetPolicy:
		if !sameState(&transition.State, before) {
			return true
		}
	case WorkspaceEventStoreLoaded, WorkspaceEventPreferencesLoaded:
		return true
	}
	return slices.ContainsFunc(transition.Effects, func(effect WorkspaceEffect) bool {
		switch effect.(type) {
		case WorkspaceEffectBrowserOpen, WorkspaceEffectBrowserNavigate, WorkspaceEffectBrowserAct, WorkspaceEffectBrowserHistory, WorkspaceEffectBrowserReload:
			return true
		}
		return false
	})
}

// sameState is TypeScript's === between two states: every field the same by identity.
func sameState(a, b *WorkspaceState) bool {
	return a == b || sameValue(reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem())
}
