package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Threads themselves: making one, choosing it, and what the list can do to it. Their title, role,
// snooze, archive and agent settings are this feature's own (TS: Step<ThreadCore>).

var ThreadCommandInputs = []string{
	"task.new", "task.select", "task.dismiss", "task.dismiss-all", "task.read-all", "task.result-collected", "task.archive", "task.snooze", "task.unsnooze", "snoozes.elapsed", "task.restore",
	"task.clear-archive", "task.rename", "task.set-role", "title.suggested", "task.fork", "view.fork-name", "view.archive-prompt", "task.move", "task.set-policy", "task.set-model",
	"task.set-effort", "task.set-fast-mode",
}

// The name it suggests is the one a fork gets without asking, numbered among the threads on the computer holding it.
func askForkName(state *WorkspaceState, input WorkspaceInput) Step {
	ask, ok := input.(contracts.ViewCommandViewForkName)
	if !ok {
		return Step{}
	}
	if ask.SourceID == nil {
		return Step{Edits: []OwnerEdit{ForkNameIn(DialogsOf(state), nil)}}
	}
	holder := state
	if computer := ComputerOfThread(state, *ask.SourceID); computer != nil && computer.State != nil {
		holder = computer.State
	}
	source := threadsFind(holder.Threads, *ask.SourceID)
	if source == nil || SideChatIds(holder).Has(source.ID) {
		return Step{}
	}
	taken := make([]string, len(holder.Threads))
	for index, thread := range holder.Threads {
		taken[index] = thread.Title
	}
	title := domain.ForkTitle(source.Title, taken)
	named := &ForkName{TaskID: source.ID, Worktree: ask.Worktree != nil && *ask.Worktree, Title: title}
	return Step{Edits: []OwnerEdit{MenuIn(ForkNameIn(DialogsOf(state), named), nil)}}
}

// Offers the title to the engine's own record of the thread; an engine that keeps none ignores it.
func labelThread(taskID, title string) WorkspaceEffect {
	return WorkspaceEffectSendRunCommand{Command: contracts.LabelThreadCommand{TaskID: taskID, Title: title}}
}

// rosterActivityOf is the parts of a thread activity a coordinator's roster reads.
func rosterActivityOf(activity *ThreadActivity) RosterActivity {
	return RosterActivity{Working: activity.Working, Blocked: activity.Blocked, Ranked: activity.Ranked}
}

// Priority as the sidebar draws it, narrowed to one project when `projectID` names one. A side chat
// has no row of its own, so it is not one of them. A coordinator here stands for its threads on paired
// computers too, read as their computer last said, as the sidebar ranks it.
func priorityThreads(state *WorkspaceState, projectID *string) []*domain.Thread {
	sections, threads := OwnCoordinationSections(state, ThreadActivityThreadLists(state).VisibleThreads, rosterActivityOf(ThreadActivityThreadActivity(state)), ShownComputers(&state.Computers))
	if projectID != nil && *projectID != "" {
		return ProjectActivity(sections, threads, *projectID)[ActivitySectionsKeyPriority]
	}
	return sections[ActivitySectionsKeyPriority]
}

// The rows of Priority Dismiss all files away, by the rule each row's own Dismiss follows, a
// coordinator's read with every thread under it, here or on a paired computer.
func dismissablePriority(state *WorkspaceState, projectID *string) []*domain.Thread {
	here := rosterActivityOf(ThreadActivityThreadActivity(state))
	shown := ShownComputers(&state.Computers)
	rows := []*domain.Thread{}
	for _, row := range priorityThreads(state, projectID) {
		if CoordinationCoordinatorRoster(state, row, here, &shown).Dismissable {
			rows = append(rows, row)
		}
	}
	return rows
}

// A coordinator here filed away with threads under it on the paired computers the sidebar shows asks
// each of those computers to file away its own, which only it can, by the rule the row's own Dismiss
// reads: none waiting on an approval, and only those with something to file away. A computer that is
// out of reach, or whose build cannot take it, says so in the errors. `rows` keeps the order they were listed in.
func dismissElsewhere(state *WorkspaceState, rows []string) (effects []WorkspaceEffect, errors []string) {
	type held struct {
		computer *PairedComputer
		inputs   []contracts.AppCommand
	}
	var order []*held
	byComputer := map[string]*held{}
	here := rosterActivityOf(ThreadActivityThreadActivity(state))
	shown := ShownComputers(&state.Computers)
	seen := jsonx.Set[string]{}
	for _, row := range rows {
		if seen.Has(row) {
			continue
		}
		seen[row] = struct{}{}
		lead := threadsFind(state.Threads, row)
		if lead == nil {
			continue
		}
		for _, member := range CoordinationCoordinatorRoster(state, lead, here, &shown).Filed {
			if member.Computer == nil || !domain.WantsAttention(&member.Thread) {
				continue
			}
			entry := byComputer[member.Computer.ID]
			if entry == nil {
				entry = &held{computer: member.Computer}
				byComputer[member.Computer.ID] = entry
				order = append(order, entry)
			}
			taskID := member.Thread.ID
			entry.inputs = append(entry.inputs, contracts.TaskCommandTaskDismiss{TaskID: &taskID})
		}
	}
	effects, errors = []WorkspaceEffect{}, []string{}
	for _, entry := range order {
		computer := entry.computer
		switch {
		case computer.Status != domain.ComputerStatusConnected:
			errors = append(errors, domain.OfflineMessage(&computer.ComputerLink))
		case !slices.ContainsFunc(entry.inputs, func(command contracts.AppCommand) bool {
			return !contracts.SupportsComputerCommand(computer.Capabilities, command)
		}):
			effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: forwardedInputs(entry.inputs)})
		default:
			errors = append(errors, computer.Name+": "+contracts.RemoteUnsupported)
		}
	}
	return effects, errors
}

// forwardedInputs is commands as the inputs a paired computer is forwarded.
func forwardedInputs(commands []contracts.AppCommand) []WorkspaceInput {
	inputs := make([]WorkspaceInput, len(commands))
	for index, command := range commands {
		inputs[index] = command.(WorkspaceInput)
	}
	return inputs
}

// The row Priority moves on to once this one leaves it: the one below, or the one above when it was last.
func priorityNeighbour(state *WorkspaceState, taskID string) (string, bool) {
	// Only a project of this computer's narrows its rows; a filter the sidebar ignores is ignored here too.
	var narrowed *string
	if state.ActivityProject != nil && slices.ContainsFunc(state.Projects, func(project *domain.Project) bool {
		return project.ID == *state.ActivityProject && (project.Unlisted == nil || !*project.Unlisted)
	}) {
		narrowed = state.ActivityProject
	}
	priority := priorityThreads(state, narrowed)
	index := slices.IndexFunc(priority, func(thread *domain.Thread) bool { return thread.ID == taskID })
	switch {
	case index == -1:
		return "", false
	case index+1 < len(priority):
		return priority[index+1].ID, true
	case index > 0:
		return priority[index-1].ID, true
	}
	return "", false
}

// Dismisses the listed computers' Priority, or only one project's rows of it; each host owns its
// changes and reports them back.
func dismissPriority(state *WorkspaceState, localOnly bool, projectID *string) Step {
	own := localOnly || state.Computers.Filter == "all" || state.Computers.Filter == "this"
	// A coordinator in Priority is there for its threads too, so filing it away files them with it.
	var rows []string
	if own {
		for _, thread := range dismissablePriority(state, projectID) {
			rows = append(rows, thread.ID)
		}
	}
	dotted := DismissedWith(state.Threads, jsonx.SetOf(rows...), ThreadActivityThreadActivity(state).Blocked)
	filed := Step{}
	if len(dotted) > 0 {
		filed.Edits = []OwnerEdit{DismissedIn(ThreadsOf(state), dotted)}
	}
	if localOnly {
		return filed
	}
	effects, errors := dismissElsewhere(state, rows)
	for _, computer := range ShownComputers(&state.Computers) {
		if computer.Status != domain.ComputerStatusConnected || computer.State == nil || len(dismissablePriority(computer.State, projectID)) == 0 {
			continue
		}
		scoped := contracts.TaskCommandTaskDismissAll{LocalOnly: new(true)}
		if projectID != nil && *projectID != "" {
			scoped.ProjectID = projectID
		}
		// Older hosts already dismiss only their own threads and do not know the scope field, but nothing narrower holds without the project.
		var command contracts.AppCommand = contracts.TaskCommandTaskDismissAll{}
		if projectID != nil && *projectID != "" || contracts.SupportsComputerCommand(computer.Capabilities, scoped) {
			command = scoped
		}
		if !contracts.SupportsComputerCommand(computer.Capabilities, command) {
			errors = append(errors, computer.Name+": "+contracts.RemoteUnsupported)
			continue
		}
		effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: forwardedInputs([]contracts.AppCommand{command})})
	}
	filed.Effects = effects
	if len(errors) > 0 {
		refusal := strings.Join(errors, "\n")
		filed.Refusal = &refusal
	}
	return filed
}

// Marks the listed computers' threads read; each host owns its changes and reports them back.
func readAll(state *WorkspaceState, localOnly bool) Step {
	own := localOnly || state.Computers.Filter == "all" || state.Computers.Filter == "this"
	read := Step{}
	if own {
		read.Edits = []OwnerEdit{AllReadIn(ThreadsOf(state))}
	}
	if localOnly {
		return read
	}
	var command contracts.AppCommand = contracts.TaskCommandTaskReadAll{LocalOnly: new(true)}
	effects := []WorkspaceEffect{}
	errors := []string{}
	for _, computer := range ShownComputers(&state.Computers) {
		if computer.Status != domain.ComputerStatusConnected || computer.State == nil || !slices.ContainsFunc(computer.State.Threads, domain.HasUnreadAttention) {
			continue
		}
		if contracts.SupportsComputerCommand(computer.Capabilities, command) {
			effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: forwardedInputs([]contracts.AppCommand{command})})
		} else {
			errors = append(errors, computer.Name+": "+contracts.RemoteUnsupported)
		}
	}
	read.Effects = effects
	if len(errors) > 0 {
		refusal := strings.Join(errors, "\n")
		read.Refusal = &refusal
	}
	return read
}

// Archiving a running thread cancels its run; its checkout stays until the user removes it. Its
// pages give up their views but keep their records, so restoring and showing one reopens it. The
// page still on screen, through a side chat of the thread, keeps its view, and every page stays while
// one of its side chats is still running, since that run browses them.
func archiveThread(state *WorkspaceState, taskID string) Step {
	active := state.ActiveRuns[taskID]
	dock := DockFor(state, taskID)
	filing := Step{Edits: []OwnerEdit{
		EditThread(ThreadsOf(state), taskID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) {
				at := Now()
				next.ArchivedAt = &at
			}
		}),
		OffThreadIn(NavigationOf(state), taskID),
	}}
	shown := ""
	hasShown := false
	if owner, _ := FrontDock(Afterward(state, SliceTransition(filing))); owner == taskID {
		shown, hasShown = dock.Tab, true
	}
	browsing := slices.ContainsFunc(state.SideChats, func(chat *SideChat) bool { return chat.SourceThreadID == taskID && state.ActiveRuns[chat.ID] != nil })
	closed := jsonx.Set[string]{}
	var closing []string
	if !browsing {
		for _, tab := range dock.BrowserTabs {
			if hasShown && tab.ID == shown || closed.Has(tab.ID) {
				continue
			}
			closed[tab.ID] = struct{}{}
			closing = append(closing, tab.ID)
		}
	}
	thread := threadsFind(state.Threads, taskID)
	return InTurn(state, filing, []func(*WorkspaceState) Step{
		// Filing a thread away ends its work, including a wait to resume after its usage limit, which ends as filed away.
		func(archived *WorkspaceState) Step { return PauseCancelled(archived, taskID) },
		func(archived *WorkspaceState) Step {
			step := Step{Effects: RetireAutomations(state, []string{taskID})}
			if len(closed) > 0 {
				tabs := make([]*domain.BrowserTab, len(dock.BrowserTabs))
				for index, tab := range dock.BrowserTabs {
					if closed.Has(tab.ID) && tab.Loading {
						stopped := *tab
						stopped.Loading = false
						tab = &stopped
					}
					tabs[index] = tab
				}
				step.Edits = []OwnerEdit{EditDock(DocksOf(archived), taskID, func(next *ThreadDock) { next.BrowserTabs = tabs })}
			}
			return step
		},
		func(parked *WorkspaceState) Step {
			if active == nil {
				return Step{}
			}
			return Cancelling(parked, active)
		},
		func(*WorkspaceState) Step {
			effects := slices.Clone(FileThread(state, thread, true))
			for _, tabID := range closing {
				effects = append(effects, WorkspaceEffectBrowserClose{TabID: tabID})
			}
			return Step{Effects: effects}
		},
	})
}

// Files one row away, with the threads under it, here and on the paired computers holding them. A
// thread still running is filed away again once its turn finishes.
func dismissThread(state *WorkspaceState, taskID string) Step {
	filing := DismissedIn(ThreadsOf(state), DismissedWith(state.Threads, jsonx.SetOf(taskID), ThreadActivityThreadActivity(state).Blocked))
	running := RunDismissedIn(RunsOf(state), taskID)
	elsewhere, errors := dismissElsewhere(state, []string{taskID})
	if jsonx.SameSlice(filing.View.Threads, state.Threads) && jsonx.SameMap(running.View.ActiveRuns, state.ActiveRuns) && len(elsewhere) == 0 && len(errors) == 0 {
		return Step{}
	}
	// Filing away the thread being read moves on to the row that takes its place, so Priority can be worked down without going back to the list.
	successor, moves := "", false
	if state.CurrentID != nil && *state.CurrentID == taskID {
		successor, moves = priorityNeighbour(state, taskID)
	}
	landing := Step{Edits: []OwnerEdit{filing, running}}
	if moves && successor != "" {
		landing = AndThen(state, landing, func(next *WorkspaceState) Step { return Landed(next, successor) })
	}
	landing.Effects = slices.Concat(landing.Effects, elsewhere)
	if len(errors) > 0 {
		refusal := strings.Join(errors, "\n")
		landing.Refusal = &refusal
	}
	return landing
}

// The window on a fresh draft, in the checkout or project named. A checkout names the project it was
// cut from, so starting in one settles both answers. A new thread is asked for in order to type in it,
// so the caret goes to the empty composer.
func NewThread(state *WorkspaceState, projectIDNamed, worktreeID *string) Step {
	worktree := WorktreeByID(state, worktreeID)
	projectID := projectIDNamed
	var worktreeHeld *string
	if worktree != nil {
		cutFrom, id := worktree.ProjectID, worktree.ID
		projectID, worktreeHeld = &cutFrom, &id
	}
	var folder *string
	if projectID != nil && *projectID != "" {
		for _, project := range state.Projects {
			if project.ID == *projectID {
				root := project.Root
				folder = &root
				break
			}
		}
	}
	return Step{
		Edits:       []OwnerEdit{DraftReviewDroppedIn(DocksOf(state)), FreshDraftIn(NavigationOf(state), projectID, worktreeHeld, folder), FocusComposerIn(ComposerOf(state))},
		Effects:     TakeKeys,
		ClearsError: true,
		ClosesMenu:  true,
	}
}

// A refusal shown where the window shows errors that answers nothing, so the input it came with still succeeds.
func shownNotAnswered(state *WorkspaceState, step Step) Step {
	refusal := step.Refusal
	step.Refusal, step.Result = nil, nil
	if refusal == nil {
		return step
	}
	page := state.ActionErrorPage
	if step.Error != nil && step.Error.Page != nil {
		page = step.Error.Page
	}
	step.Error = &SliceError{Message: refusal, Page: page}
	return step
}

// `input` is one of the ThreadCommandInputs.
func ReduceThreadCommands(state *WorkspaceState, input WorkspaceInput) Step {
	switch input := input.(type) {
	case WorkspaceEventSnoozesElapsed:
		return Step{}

	case contracts.TaskCommandTaskSnooze:
		if !domain.IsSnoozeHours(input.Hours) || !slices.ContainsFunc(priorityThreads(state, nil), func(thread *domain.Thread) bool { return thread.ID == input.TaskID }) {
			return Step{}
		}
		snoozedUntil := Now() + input.Hours*60*60*1000
		return Step{Edits: []OwnerEdit{EditThread(ThreadsOf(state), input.TaskID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.SnoozedUntil = &snoozedUntil }
		})}, ClosesMenu: true}

	case contracts.TaskCommandTaskUnsnooze:
		if !slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool { return thread.ID == input.TaskID && thread.SnoozedUntil != nil }) {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{UnsnoozedIn(ThreadsOf(state), input.TaskID)}, ClosesMenu: true}

	case contracts.TaskCommandTaskNew:
		return NewThread(state, input.ProjectID, input.WorktreeID)

	// A side chat is a tab within its source thread, and a thread under a coordinator a tab within the
	// coordinator's, so landing on either lands on the thread holding it and opens its tab there.
	case contracts.TaskCommandTaskSelect:
		return Selected(state, input.TaskID)

	case contracts.TaskCommandTaskDismiss:
		if input.TaskID == nil || *input.TaskID == "" {
			return Step{}
		}
		return dismissThread(state, *input.TaskID)

	case contracts.TaskCommandTaskDismissAll:
		return dismissPriority(state, input.LocalOnly != nil && *input.LocalOnly, input.ProjectID)

	case contracts.TaskCommandTaskReadAll:
		return readAll(state, input.LocalOnly != nil && *input.LocalOnly)

	// The verdict reached the user through the thread that waited on it; what a run found stays theirs to read.
	case WorkspaceEventTaskResultCollected:
		if !slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool {
			return thread.ID == input.TaskID && thread.OutcomeUnread != nil && *thread.OutcomeUnread
		}) {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{VerdictCollectedIn(ThreadsOf(state), input.TaskID)}}

	case contracts.TaskCommandTaskArchive:
		return archiveThread(state, input.TaskID)

	// Restoring leaves the retired automation gone; the user re-arms it themselves.
	case contracts.TaskCommandTaskRestore:
		thread := threadsFind(state.Threads, input.TaskID)
		if thread == nil || thread.ArchivedAt == nil {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{RestoredIn(ThreadsOf(state), input.TaskID)}, Effects: FileThread(state, thread, false)}

	case contracts.TaskCommandTaskClearArchive:
		var archived []*domain.Thread
		var discardedIDs []string
		for _, thread := range state.Threads {
			if thread.ArchivedAt != nil {
				archived = append(archived, thread)
				discardedIDs = append(discardedIDs, thread.ID)
			}
		}
		if len(archived) == 0 {
			return Step{}
		}
		discarded := jsonx.SetOf(discardedIDs...)
		var forks []*SideChat
		for _, chat := range state.SideChats {
			if discarded.Has(chat.SourceThreadID) {
				forks = append(forks, chat)
			}
		}
		// A fork of a thread that is gone has nowhere left to be shown, so it goes with it.
		return InTurn(state, SideChatsClosed(state, forks), []func(*WorkspaceState) Step{
			func(forked *WorkspaceState) Step { return DocksDisposed(forked, discardedIDs) },
			func(disposed *WorkspaceState) Step {
				threads := RemovedIn(ThreadsOf(disposed), discarded)
				return Step{Edits: []OwnerEdit{threads, OnListedThreadIn(NavigationOf(disposed), threads.View.Threads)}}
			},
			func(removed *WorkspaceState) Step { return Pruned(removed, discarded) },
			func(cleared *WorkspaceState) Step { return LeadsDiscarded(cleared, archived) },
		})

	case contracts.TaskCommandTaskRename:
		title := domain.ClampTitle(input.Title)
		if title == "" || threadsFind(state.Threads, input.TaskID) == nil {
			return Step{}
		}
		return Step{
			Edits: []OwnerEdit{EditThread(ThreadsOf(state), input.TaskID, func(*domain.Thread) ThreadPatch {
				return func(next *domain.Thread) {
					next.Title = title
					next.TitleByUser = new(true)
					next.UpdatedAt = Now()
				}
			})},
			Effects: []WorkspaceEffect{labelThread(input.TaskID, title)},
		}

	case contracts.TaskCommandTaskSetRole:
		taskID := TargetID(state, input.TaskID)
		var thread *domain.Thread
		if taskID != nil {
			thread = threadsFind(state.Threads, *taskID)
		}
		if thread == nil {
			if input.TaskID == nil {
				return Step{Edits: []OwnerEdit{DraftRoleIn(NavigationOf(state), input.Role)}}
			}
			return Step{}
		}
		if threadsSameRole(thread.Role, input.Role) {
			return Step{}
		}
		// A thread on another computer never takes a coordinator's role away, nor makes one in a worktree
		// a coordinator, as none here does; the user still can.
		if input.Sender != nil {
			if refusal := CoordinatorAskRefusal(thread, input, slices.Contains(state.CreatingWorktrees, thread.ID)); refusal != nil && *refusal != "" {
				return Step{Refusal: refusal}
			}
		}
		return Step{Edits: []OwnerEdit{RoleSetIn(ThreadsOf(state), thread.ID, input.Role, CatalogOf(state), Now)}}

	// A name the user typed outranks a suggested one, whenever the suggestion lands.
	case WorkspaceEventTitleSuggested:
		thread := threadsFind(state.Threads, input.TaskID)
		title := domain.ClampTitle(input.Title)
		if thread == nil || thread.TitleByUser != nil && *thread.TitleByUser || title == "" || title == thread.Title {
			return Step{}
		}
		return Step{
			Edits: []OwnerEdit{EditThread(ThreadsOf(state), input.TaskID, func(*domain.Thread) ThreadPatch {
				return func(next *domain.Thread) { next.Title = title }
			})},
			Effects: []WorkspaceEffect{labelThread(input.TaskID, title)},
		}

	// The copy is opened the way any thread is opened, and asks for a checkout the way any thread asks.
	case contracts.TaskCommandTaskFork:
		taskID := TargetID(state, input.TaskID)
		var source *domain.Thread
		if taskID != nil {
			source = threadsFind(state.Threads, *taskID)
		}
		if source == nil || SideChatIds(state).Has(source.ID) {
			return Step{}
		}
		forkID := NewID()
		forking := Step{Edits: []OwnerEdit{ForkedIn(ThreadsOf(state), source, forkID, Now(), input.Title), MenuIn(ForkNameIn(DialogsOf(state), nil), nil)}}
		opened := AndThen(state, forking, func(next *WorkspaceState) Step { return Selected(next, forkID) })
		if input.Worktree == nil || !*input.Worktree {
			return opened
		}
		return AndThen(state, opened, func(next *WorkspaceState) Step {
			return shownNotAnswered(next, WorktreeSet(next, contracts.TaskCommandTaskSetWorktree{TaskID: &forkID, Worktree: true}))
		})

	case contracts.ViewCommandViewForkName:
		return askForkName(state, input)

	case contracts.ViewCommandViewArchivePrompt:
		if input.ThreadID == nil {
			return Step{Edits: []OwnerEdit{ArchivePromptIn(DialogsOf(state), nil)}}
		}
		if WorkspaceDialogsArchivePromptView(state, input.ThreadID) == nil {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{MenuIn(ArchivePromptIn(DialogsOf(state), input.ThreadID), nil)}}

	// A drag reveals every folder so it can be dropped into, so the drop leaves the folding alone.
	case contracts.TaskCommandTaskMove:
		threads := MovedInListIn(ThreadsOf(state), input.TaskID, input.Target)
		if jsonx.SameSlice(threads.View.Threads, state.Threads) {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{threads}, ClosesMenu: true}

	case contracts.TaskCommandTaskSetPolicy:
		target := settingsTarget(state, input.TaskID)
		if !slices.Contains(domain.CapabilitiesFor(target.engine).Policies, input.Policy) {
			return Step{}
		}
		policy := input.Policy
		return withSetting(state, target, &AgentDraftChange{Policy: &policy}, func(next *domain.Thread) { next.ExecutionPolicy = policy })

	case contracts.TaskCommandTaskSetModel:
		target := settingsTarget(state, input.TaskID)
		catalog := CatalogOf(state)
		var model domain.AgentModel
		hasModel := false
		if input.Model != nil {
			model, hasModel = *input.Model, true
		} else {
			model, hasModel = domain.DefaultModelIn(&catalog, input.Engine)
		}
		// A thread keeps the engine it started on, so the model must be one that engine offers too.
		if !hasModel || !domain.EngineHasModel(&catalog, input.Engine, model) || target.thread != nil && !domain.EngineHasModel(&catalog, target.thread.Engine, model) {
			return Step{}
		}
		// A draft keeps its effort where the new model takes it, and drops to the nearest one below where it does not.
		engine := input.Engine
		return withSetting(state, target, &AgentDraftChange{Engine: &engine, Model: &model}, func(next *domain.Thread) { next.Model = &model })

	case contracts.TaskCommandTaskSetFastMode:
		target := settingsTarget(state, input.TaskID)
		if target.taskID != nil && *target.taskID != "" && target.thread == nil || !domain.CapabilitiesFor(target.engine).FastMode {
			return Step{}
		}
		fast := input.FastMode
		return withSetting(state, target, &AgentDraftChange{FastMode: &fast}, func(next *domain.Thread) { next.FastMode = &fast })

	case contracts.TaskCommandTaskSetEffort:
		target := settingsTarget(state, input.TaskID)
		// Effort belongs to the model, and to the part the thread plays, so only one the target takes can be set on it.
		if target.thread != nil && !threadTakesEffort(state, target.thread, input.Effort) {
			return Step{}
		}
		catalog := CatalogOf(state)
		draftRole := domain.ThreadRole("")
		if state.DraftRole != nil {
			draftRole = *state.DraftRole
		}
		draftTakes := domain.ModelHasEffort(&catalog, state.DraftEngine, state.DraftModel, input.Effort, domain.EffortRoleOf(nil, draftRole))
		if target.thread == nil && !draftTakes {
			return Step{}
		}
		// The draft keeps its own model, so it takes the effort only where that model offers it.
		effort := input.Effort
		var draft *AgentDraftChange
		if draftTakes {
			draft = &AgentDraftChange{Effort: &effort}
		}
		return withSetting(state, target, draft, func(next *domain.Thread) { next.Effort = &effort })
	}
	return Step{}
}

// threadsSameRole is (role ?? null) === wanted.
func threadsSameRole(role, wanted *domain.ThreadRole) bool {
	if role == nil || wanted == nil {
		return role == nil && wanted == nil
	}
	return *role == *wanted
}

// What a setting command changes: the thread it names, or with none named the draft and the thread on
// screen. `engine` is what that thread runs on, else the draft's.
type threadSettingsTarget struct {
	named  bool
	taskID *string
	thread *domain.Thread
	engine domain.EngineID
}

func settingsTarget(state *WorkspaceState, taskID *string) threadSettingsTarget {
	target := TargetID(state, taskID)
	var thread *domain.Thread
	if target != nil {
		thread = threadsFind(state.Threads, *target)
	}
	engine := domain.EngineID(state.DraftEngine)
	if thread != nil {
		engine = domain.EngineID(thread.Engine)
	}
	return threadSettingsTarget{named: taskID != nil, taskID: target, thread: thread, engine: engine}
}

// Writes a setting to its target; `draft` is what an unnamed one changes on the draft, or nil where
// the draft does not take it. `setting` writes only executionPolicy, model, effort and fastMode.
func withSetting(state *WorkspaceState, target threadSettingsTarget, draft *AgentDraftChange, setting ThreadPatch) Step {
	edits := []OwnerEdit{}
	if !target.named && draft != nil {
		edits = append(edits, DraftChoiceIn(NavigationOf(state), ChangedDraft(state, *draft)))
	}
	if target.taskID != nil && *target.taskID != "" {
		edits = append(edits, EditThread(ThreadsOf(state), *target.taskID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) {
				setting(next)
				next.UpdatedAt = Now()
			}
		}))
	}
	return Step{Edits: edits}
}

func threadTakesEffort(state *WorkspaceState, thread *domain.Thread, effort domain.AgentEffort) bool {
	catalog := CatalogOf(state)
	model, ok := domain.ThreadModel(&catalog, thread)
	return ok && domain.ModelHasEffort(&catalog, thread.Engine, model, effort, domain.EffortRoleOf(thread, ""))
}
