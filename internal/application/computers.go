package application

import (
	"errors"
	"reflect"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Threads that live on other computers. Each paired computer's whole workspace state is mirrored
// here as it changes there, so its threads can be listed beside this computer's own and one of
// them shown in the conversation. Nothing is decided about those threads on this side: a command
// aimed at one is carried to the computer that holds it, and what comes back is its new state.

// The paired computers as one input leaves them. Only the functions here make one.
type computersChange = ComputersEdit

func ComputersOf(state *WorkspaceState) ComputersEdit {
	return ComputersEdit{View: ComputersSliceState{Computers: state.Computers}}
}

// Threads gone from here that may still lead threads on paired computers, remembered as let go, the newest kept.
func LeadsDiscardedIn(edit ComputersEdit, ids []string) ComputersEdit {
	computers := edit.View.Computers
	leads := slices.Concat(computers.DiscardedLeads, ids)
	if len(leads) > MaxDiscardedLeads {
		leads = leads[len(leads)-MaxDiscardedLeads:]
	}
	computers.DiscardedLeads = leads
	return ComputersEdit{View: ComputersSliceState{Computers: computers}}
}

// A paired computer put on screen, which lets go of one waiting for its line.
func ShownIn(edit ComputersEdit, id string) ComputersEdit {
	computers := edit.View.Computers
	computers.Active, computers.Dropped = &id, nil
	return ComputersEdit{View: ComputersSliceState{Computers: computers}}
}

// The terminal ids of a docks map, by the map's identity. Output leaves these records unchanged.
var terminalIds struct {
	sync.Mutex
	entries []terminalIDEntry
}

type terminalIDEntry struct {
	docks map[string]*ThreadDock
	ids   jsonx.Set[string]
}

// How many docks maps keep their terminal ids: this computer's and each paired one's, with room to spare.
const computersHeldTerminalIDs = 16

// Keystrokes must not scan every thread's dock.
func holdsTerminal(state *WorkspaceState, id string) bool {
	terminalIds.Lock()
	defer terminalIds.Unlock()
	for _, entry := range terminalIds.entries {
		if jsonx.SameMap(entry.docks, state.Docks) {
			return entry.ids.Has(id)
		}
	}
	ids := jsonx.Set[string]{}
	for _, dock := range state.Docks {
		for _, terminal := range dock.Terminals {
			ids[terminal.ID] = struct{}{}
		}
	}
	entries := append(terminalIds.entries, terminalIDEntry{docks: state.Docks, ids: ids})
	if len(entries) > computersHeldTerminalIDs {
		entries = slices.Delete(entries, 0, len(entries)-computersHeldTerminalIDs)
	}
	terminalIds.entries = entries
	return ids.Has(id)
}

// The paired computer holding what `holds` finds, or nil when this computer holds it or nobody does.
func holderOf(state *WorkspaceState, holds func(held *WorkspaceState) bool) *PairedComputer {
	if holds(state) {
		return nil
	}
	for _, computer := range state.Computers.Paired {
		if computer.State != nil && holds(computer.State) {
			return computer
		}
	}
	return nil
}

// The paired computer holding a thread, or nil for one of this computer's own or one nobody holds.
// An empty id names nothing.
func ComputerOfThread(state *WorkspaceState, taskID string) *PairedComputer {
	if taskID == "" {
		return nil
	}
	return holderOf(state, func(held *WorkspaceState) bool { return threadNamed(held.Threads, taskID) != nil })
}

func ComputerOfProject(state *WorkspaceState, projectID string) *PairedComputer {
	if projectID == "" {
		return nil
	}
	return holderOf(state, func(held *WorkspaceState) bool {
		return slices.ContainsFunc(held.Projects, func(project *domain.Project) bool { return project.ID == projectID })
	})
}

// The paired computer whose project or checkout a workspace id names, or nil for one of this computer's own.
func ComputerOfWorkspace(state *WorkspaceState, workspaceID string) *PairedComputer {
	return holderOf(state, func(held *WorkspaceState) bool {
		return slices.ContainsFunc(held.Projects, func(project *domain.Project) bool {
			return project.WorkspaceID != nil && *project.WorkspaceID == workspaceID
		}) || slices.ContainsFunc(held.Worktrees, func(worktree *domain.Worktree) bool { return worktree.WorkspaceID == workspaceID })
	})
}

func computerOfWorktree(state *WorkspaceState, worktreeID string) *PairedComputer {
	if worktreeID == "" {
		return nil
	}
	return holderOf(state, func(held *WorkspaceState) bool {
		return slices.ContainsFunc(held.Worktrees, func(worktree *domain.Worktree) bool { return worktree.ID == worktreeID })
	})
}

// The paired computer whose checkout is at a root, or nil for one of this computer's own or one nobody holds.
func computerOfWorktreeRoot(state *WorkspaceState, root string) *PairedComputer {
	return holderOf(state, func(held *WorkspaceState) bool {
		return slices.ContainsFunc(held.Worktrees, func(worktree *domain.Worktree) bool { return worktree.Root == root }) ||
			slices.ContainsFunc(held.ManagedWorktrees, func(worktree *domain.ManagedWorktree) bool { return worktree.Root == root })
	})
}

var local InputRoute = domain.WorktreeDestinationLocal{}

// Whether a command routed here means the window is leaving the paired computer it was showing. A
// folder opened to start in leaves only when it answers the open dialog's own ask.
func LeavesComputer(state *WorkspaceState, input WorkspaceInput) bool {
	if added, ok := input.(ProjectAddEventProjectAdded); ok {
		return added.Start != nil && *added.Start && (added.Request == nil || (state.ProjectAdd != nil && state.ProjectAdd.Request == *added.Request))
	}
	typ := InputType(input)
	return contracts.IsAppCommandType(typ) && contracts.CommandLeaves(typ)
}

const (
	PanelElsewhere             = "The browser panel opens only for threads on this computer."
	AttachmentsElsewhere       = "Files and folders attached by local path cannot be sent to another computer."
	QueuedAttachmentsElsewhere = "A message with images or files cannot be edited from another computer."
	FilesElsewhere             = "That folder is on another computer, so it cannot be opened here."
)

func computersRefused(message string) InputRoute {
	return InputRouteRefuse{Message: message}
}

// What a forwarded route carries besides its inputs.
type computersForwardOptions struct {
	Select bool
	Also   bool
	Draft  *SentDraft
}

func computersForwarded(computer *PairedComputer, inputs []WorkspaceInput, options computersForwardOptions) InputRoute {
	for _, input := range inputs {
		if command, ok := input.(contracts.AppCommand); ok && !contracts.SupportsComputerCommand(computer.Capabilities, command) {
			return InputRouteRefuse{Reason: new("unsupported"), Message: contracts.RemoteUnsupported}
		}
	}
	route := InputRouteComputer{Computer: *computer, Inputs: inputs, Draft: options.Draft}
	if options.Select {
		route.Select = new(true)
	}
	if options.Also {
		route.Also = new(true)
	}
	return route
}

// A command bound for the computer holding what it names, which only a line that is up can carry.
func toward(computer *PairedComputer, route func(computer *PairedComputer) InputRoute) InputRoute {
	if computer == nil {
		return local
	}
	if computer.Status != domain.ComputerStatusConnected {
		return computersRefused(domain.OfflineMessage(&computer.ComputerLink))
	}
	return route(computer)
}

// Puts a computer on screen: it is told first whether anyone here is looking, which is what its unread marks go by.
func selecting(state *WorkspaceState, computer *PairedComputer, inputs []WorkspaceInput) InputRoute {
	return computersForwarded(computer, slices.Concat([]WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: state.Focused}}, inputs), computersForwardOptions{Select: true})
}

// A composer send carries the draft this computer holds for the thread: the text and what rides
// with it are put into the other computer's own composer for that thread, whole, so a send it
// refused leaves nothing behind for the next, and its send reads them from there. The draft stays
// here until that computer has taken it. A send with text of its own goes as it is.
// `command` is a contracts.TaskCommandTaskSend or contracts.TaskCommandAttachmentsSend.
func forwardedSend(state *WorkspaceState, computer *PairedComputer, command contracts.AppCommand) InputRoute {
	send, isSend := command.(contracts.TaskCommandTaskSend)
	attach, _ := command.(contracts.TaskCommandAttachmentsSend)
	if isSend && len(send.Attachments) > 0 {
		return computersRefused(AttachmentsElsewhere)
	}
	remote := computer.State
	if remote == nil {
		return computersRefused("That computer has not answered yet.")
	}
	named := attach.TaskID
	if isSend {
		named = send.TaskID
	}
	// A thread on an engine only that computer's newer build knows is shown here read-only, and takes nothing typed here.
	var aimed *string
	switch {
	case named != nil:
		aimed = named
	case !isSend || send.Text == nil:
		aimed = new(PromptKey(remote))
	}
	if aimed != nil {
		if thread := threadNamed(remote.Threads, *aimed); thread != nil && !domain.IsAgentEngine(string(thread.Engine)) {
			return computersRefused(domain.UnopenableEngine(string(thread.Engine)))
		}
	}
	if isSend {
		send = SendFor(computer, send)
		send.Attachments = nil
		if send.Text != nil {
			return computersForwarded(computer, []WorkspaceInput{send}, computersForwardOptions{})
		}
	}
	// The thread the send is for: the one named, else the one that computer has open, else its draft.
	key := PromptKey(remote)
	if named != nil {
		key = *named
	}
	if len(FilesFor(state, key)) > 0 {
		return computersRefused(AttachmentsElsewhere)
	}
	prompt := state.Prompts[key]
	annotations := AnnotationsFor(state, key)
	pastes := PastesFor(state, key)
	// A new thread's draft goes to whichever draft that computer's own composer reads.
	var target *string
	if key != NewThreadDraft {
		target = &key
	}
	var outgoing WorkspaceInput
	if isSend {
		send.TaskID = target
		outgoing = send
	} else {
		attachments := make([]*domain.OutgoingAttachment, len(attach.Attachments))
		for index, attachment := range attach.Attachments {
			sent := *attachment
			sent.Path = nil
			attachments[index] = &sent
		}
		outgoing = contracts.TaskCommandAttachmentsSend{TaskID: target, Steer: attach.Steer, Attachments: attachments, Text: attach.Text}
	}
	inputs := []WorkspaceInput{
		contracts.ViewCommandViewSetPrompt{TaskID: target, Prompt: prompt},
		contracts.AnnotationCommandAnnotationRecall{TaskID: target, Annotations: annotations},
		contracts.PasteCommandPasteRecall{TaskID: target, Pastes: pastes},
		outgoing,
	}
	draft := &SentDraft{Key: key, Prompt: prompt, Pastes: pastes, Annotations: annotations}
	if !isSend {
		strip := MainComposer
		if attach.TaskID != nil {
			strip = *attach.TaskID
		}
		ids := make([]string, len(attach.Attachments))
		for index, attachment := range attach.Attachments {
			ids[index] = attachment.ID
		}
		draft.Attachments = &SentDraftAttachments{Key: strip, IDs: ids}
	}
	return computersForwarded(computer, inputs, computersForwardOptions{Draft: draft})
}

// How a command is routed by where it is placed.
type computersRoute func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute

// A command as the computer holding its thread can take it: a list of suggestions to start or turn
// down goes to a build that takes them only one at a time as one command each, which is what Start
// all and Dismiss all sent before they named the list, and a model goes without its engine to a
// build that reads the engine from the model.
func asHolderTakes(holder *PairedComputer, input contracts.AppCommand) []WorkspaceInput {
	switch command := input.(type) {
	case contracts.TaskCommandCoordinationSetThreadModel:
		if command.Engine != nil && !contracts.SupportsComputerCommand(holder.Capabilities, command) {
			command.Engine = nil
			return []WorkspaceInput{command}
		}
	case contracts.TaskCommandProposalStart:
		if command.ProposalIDs != nil && !contracts.SupportsComputerCommand(holder.Capabilities, command) {
			ids := command.ProposalIDs
			command.ProposalIDs = nil
			inputs := make([]WorkspaceInput, len(ids))
			for index, id := range ids {
				one := command
				one.ProposalID = &id
				inputs[index] = one
			}
			return inputs
		}
	case contracts.TaskCommandProposalDismiss:
		if command.ProposalIDs != nil && !contracts.SupportsComputerCommand(holder.Capabilities, command) {
			ids := command.ProposalIDs
			command.ProposalIDs = nil
			inputs := make([]WorkspaceInput, len(ids))
			for index, id := range ids {
				one := command
				one.ProposalID = &id
				inputs[index] = one
			}
			return inputs
		}
	}
	return []WorkspaceInput{input}
}

// A string field of a command by its Go name: present when the command declares it and it is set.
// An empty id names nothing, as validation never lets one through.
func computersCommandField(input contracts.AppCommand, name string) (string, bool) {
	value := reflect.ValueOf(input)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return "", false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return "", false
	}
	field := value.FieldByName(name)
	switch {
	case !field.IsValid():
		return "", false
	case field.Kind() == reflect.String:
		return field.String(), field.String() != ""
	case field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.String:
		if field.IsNil() {
			return "", false
		}
		return field.Elem().String(), field.Elem().String() != ""
	}
	return "", false
}

// The computer holding the thread a command names, else the one showing the thread on screen.
func threadHolder(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) *PairedComputer {
	taskID, named := computersCommandField(input, "TaskID")
	if !named {
		return active
	}
	return ComputerOfThread(state, taskID)
}

// Where a send goes: the computer holding the thread it names, else the one holding the project or
// checkout a new thread is named into, whatever is on screen. Only the composer's own send, which
// carries no text, follows the computer showing a thread; one with text of its own starts here.
func sendHolder(state *WorkspaceState, input *contracts.TaskCommandTaskSend, active *PairedComputer) *PairedComputer {
	if input.TaskID != nil {
		return ComputerOfThread(state, *input.TaskID)
	}
	if computer, placed := ComputerOfPlace(state, input.Project, input.WorktreeID); placed {
		return computer
	}
	if input.Text == nil {
		return active
	}
	return nil
}

// The computer holding the checkout or project a new thread is named into by id: nil for this
// computer, and placed false when no id names one.
func ComputerOfPlace(state *WorkspaceState, project, worktreeID *string) (computer *PairedComputer, placed bool) {
	if worktreeID != nil {
		if slices.ContainsFunc(state.Worktrees, func(worktree *domain.Worktree) bool { return worktree.ID == *worktreeID }) {
			return nil, true
		}
		if holder := computerOfWorktree(state, *worktreeID); holder != nil {
			return holder, true
		}
	}
	if project != nil {
		if slices.ContainsFunc(state.Projects, func(item *domain.Project) bool { return item.ID == *project }) {
			return nil, true
		}
		if holder := ComputerOfProject(state, *project); holder != nil {
			return holder, true
		}
	}
	return nil, false
}

// The project on a paired computer a new thread starts in: the one named, else the one at the asking
// thread's project's path, else the one with its folder's name. The error's text is shown as it is.
func ProjectOnComputer(computer *PairedComputer, named *string, own *domain.Project) (*domain.Project, error) {
	var projects []*domain.Project
	if computer.State != nil {
		projects = computer.State.Projects
	}
	if named != nil {
		found, err := domain.FindProject(projects, *named)
		if err != nil {
			return nil, errors.New("On " + computer.Name + ": " + err.Error())
		}
		return found, nil
	}
	if own == nil {
		return nil, errors.New("Name the project on " + computer.Name + " to start the thread in.")
	}
	matches := domain.MatchingProjects(projects, own.Root)
	if len(matches) == 0 {
		matches = domain.MatchingProjects(projects, domain.FolderName(own.Root))
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	count := "No"
	if len(matches) > 0 {
		count = "More than one"
	}
	return nil, errors.New(count + " project on " + computer.Name + " matches " + domain.ProjectName(own) + ". Name the project to start the thread in.")
}

// The fields a paired computer must say it takes before it is handed a thread to run under a
// coordinator here. A build that names none predates them, so it is asked by name, never assumed.
var ledSend = []string{"command:task.send:lead", "command:task.send:memory"}

// Why a paired computer cannot take a thread under a coordinator here right now, or nil when it can.
func LedStartRefusal(computer *PairedComputer) *string {
	if computer == nil {
		return new("That computer is no longer paired.")
	}
	if computer.Status != domain.ComputerStatusConnected {
		return new(domain.OfflineMessage(&computer.ComputerLink))
	}
	if computer.Capabilities == nil || slices.ContainsFunc(ledSend, func(name string) bool { return !slices.Contains(computer.Capabilities, name) }) {
		return new("Update AI Coding Tool on " + computer.Name + " to run threads there.")
	}
	return nil
}

// A new thread's name is only a name: a paired computer whose build cannot take one names the
// thread itself, as its own starts are named, rather than refusing the start over it. One that
// cannot take the engine a model runs on reads it from the model, as it always has.
func SendFor(computer *PairedComputer, send contracts.TaskCommandTaskSend) contracts.TaskCommandTaskSend {
	title, engine := send.Title, send.Engine
	send.Title, send.Engine = nil, nil
	if title != nil && contracts.SupportsComputerCommand(computer.Capabilities, contracts.TaskCommandTaskSend{Title: title}) {
		send.Title = title
	}
	if engine != nil && contracts.SupportsComputerCommand(computer.Capabilities, contracts.TaskCommandTaskSend{Engine: engine}) {
		send.Engine = engine
	}
	return send
}

// The paired computer holding a thread's queue, with the thread a command without a `taskId` means
// there. A nil computer is none.
func QueueHolder(state *WorkspaceState, taskID *string) (computer *PairedComputer, heldID *string) {
	if taskID == nil {
		computer = SelectedComputer(state)
	} else {
		computer = ComputerOfThread(state, *taskID)
	}
	if computer == nil {
		return nil, nil
	}
	if taskID != nil {
		return computer, taskID
	}
	if computer.State != nil {
		return computer, computer.State.CurrentID
	}
	return computer, nil
}

var placed = map[contracts.CommandPlacement]computersRoute{
	contracts.CommandPlacementLocal: func(*WorkspaceState, contracts.AppCommand, *PairedComputer) InputRoute { return local },
	contracts.CommandPlacementThread: func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		return toward(threadHolder(state, input, active), func(holder *PairedComputer) InputRoute {
			return computersForwarded(holder, asHolderTakes(holder, input), computersForwardOptions{})
		})
	},
	contracts.CommandPlacementPanel: func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		return toward(threadHolder(state, input, active), func(*PairedComputer) InputRoute { return computersRefused(PanelElsewhere) })
	},
	contracts.CommandPlacementProject: func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		projectID, _ := computersCommandField(input, "ProjectID")
		return toward(ComputerOfProject(state, projectID), func(holder *PairedComputer) InputRoute {
			return computersForwarded(holder, []WorkspaceInput{input}, computersForwardOptions{})
		})
	},
	// A delayed resize or keystroke follows its shell, even after another thread is selected.
	contracts.CommandPlacementTerminal: func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		terminalID, _ := computersCommandField(input, "TerminalID")
		if holdsTerminal(state, terminalID) {
			return local
		}
		for _, computer := range state.Computers.Paired {
			if computer.State != nil && holdsTerminal(computer.State, terminalID) {
				return toward(computer, func(computer *PairedComputer) InputRoute {
					return computersForwarded(computer, []WorkspaceInput{input}, computersForwardOptions{})
				})
			}
		}
		return computersRefused("That terminal is no longer available.")
	},
	contracts.CommandPlacementSelect: func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		taskID, _ := computersCommandField(input, "TaskID")
		return toward(ComputerOfThread(state, taskID), func(holder *PairedComputer) InputRoute {
			return selecting(state, holder, []WorkspaceInput{contracts.TaskCommandTaskSelect{TaskID: taskID}})
		})
	},
}

// Routes of the commands placed "own", by command type.
var own = map[string]computersRoute{
	"task.new": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		command := input.(contracts.TaskCommandTaskNew)
		holder := ComputerOfProject(state, computersText(command.ProjectID))
		if holder == nil {
			holder = computerOfWorktree(state, computersText(command.WorktreeID))
		}
		return toward(holder, func(holder *PairedComputer) InputRoute {
			return selecting(state, holder, []WorkspaceInput{input})
		})
	},
	"task.send": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		command := input.(contracts.TaskCommandTaskSend)
		return toward(sendHolder(state, &command, active), func(holder *PairedComputer) InputRoute {
			return forwardedSend(state, holder, input)
		})
	},
	"attachments.send": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		return toward(threadHolder(state, input, active), func(holder *PairedComputer) InputRoute {
			return forwardedSend(state, holder, input)
		})
	},
	// The queue is the holder's and the draft is this computer's, so the message leaves there and lands here.
	"task.edit-queued": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		command := input.(contracts.TaskCommandTaskEditQueued)
		return toward(threadHolder(state, input, active), func(holder *PairedComputer) InputRoute {
			taskID := command.TaskID
			if taskID == nil && holder.State != nil {
				taskID = holder.State.CurrentID
			}
			if taskID == nil || *taskID == "" {
				return computersRefused(QueuedSent)
			}
			var queued []*QueuedMessage
			if holder.State != nil {
				queued = holder.State.QueuedMessages[*taskID]
			}
			message, refusal := EditableQueued(queued, command.MessageID)
			if message == nil {
				return computersRefused(refusal)
			}
			if holder.State != nil && DrainingQueued(holder.State, *taskID, message.ID) {
				return computersRefused(QueuedSent)
			}
			// Its images and files are paths on that computer, which a composer here cannot hold.
			if len(message.Attachments) > 0 || len(message.Files) > 0 {
				return computersRefused(QueuedAttachmentsElsewhere)
			}
			return computersForwarded(holder, []WorkspaceInput{contracts.TaskCommandTaskDropQueued{TaskID: taskID, MessageID: command.MessageID}}, computersForwardOptions{Also: true})
		})
	},
	// Whether the user is looking is this window's to know and the other computer's to act on.
	"view.set-focused": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		if active != nil && active.Status == domain.ComputerStatusConnected {
			return computersForwarded(active, []WorkspaceInput{input}, computersForwardOptions{Also: true})
		}
		return local
	},
	// A file or folder is opened on the machine that has it, which is not this one.
	"file.open": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		if threadHolder(state, input, active) != nil {
			return computersRefused(FilesElsewhere)
		}
		return local
	},
	"app.open-folder": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		if active != nil {
			return computersRefused(FilesElsewhere)
		}
		return local
	},
	"worktree.reveal": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		if computerOfWorktreeRoot(state, input.(contracts.WorktreeCommandWorktreeReveal).Root) != nil {
			return computersRefused(FilesElsewhere)
		}
		return local
	},
	// The location menu opens and is searched here; the checkouts it offers, and what it moves or deletes, are the holder's.
	"worktree.menu-open": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		if input.(contracts.WorktreeCommandWorktreeMenuOpen).List == contracts.WorktreeCommandWorktreeMenuOpenListDestinations && active != nil {
			return computersForwarded(active, []WorkspaceInput{input}, computersForwardOptions{Also: true})
		}
		return local
	},
	"worktree.delete": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		command := input.(contracts.WorktreeCommandWorktreeDelete)
		var computer *PairedComputer
		switch {
		case command.WorktreeID != nil:
			computer = computerOfWorktree(state, *command.WorktreeID)
		case command.Root != nil:
			computer = computerOfWorktreeRoot(state, *command.Root)
		case command.TaskID != nil:
			computer = ComputerOfThread(state, *command.TaskID)
		default:
			computer = active
		}
		return toward(computer, func(holder *PairedComputer) InputRoute {
			return computersForwarded(holder, []WorkspaceInput{input}, computersForwardOptions{})
		})
	},
	"app.update": func(state *WorkspaceState, input contracts.AppCommand, _ *PairedComputer) InputRoute {
		return namedComputer(state, input)
	},
	"app.cancel-update": func(state *WorkspaceState, input contracts.AppCommand, _ *PairedComputer) InputRoute {
		return namedComputer(state, input)
	},
	"engine.update": func(state *WorkspaceState, input contracts.AppCommand, _ *PairedComputer) InputRoute {
		return namedComputer(state, input)
	},
	// The confirmation is drawn from the holder's list, which it is asked for when it has none with this checkout.
	"worktree.confirm-delete": func(state *WorkspaceState, input contracts.AppCommand, active *PairedComputer) InputRoute {
		root := input.(contracts.WorktreeCommandWorktreeConfirmDelete).Root
		if root == nil {
			return local
		}
		return toward(computerOfWorktreeRoot(state, *root), func(holder *PairedComputer) InputRoute {
			if holder.State != nil && slices.ContainsFunc(holder.State.ManagedWorktrees, func(worktree *domain.ManagedWorktree) bool { return worktree.Root == *root }) {
				return local
			}
			return computersForwarded(holder, []WorkspaceInput{contracts.WorktreeCommandWorktreeRefresh{}}, computersForwardOptions{Also: true})
		})
	},
}

func computersText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// A command about a whole computer goes to the one it names, without the name, so it lands there as
// that computer's own. None, or `this`, is this computer.
func namedComputer(state *WorkspaceState, input contracts.AppCommand) InputRoute {
	var computerID *string
	var command WorkspaceInput
	switch named := input.(type) {
	case contracts.ExternalAppCommandAppUpdate:
		computerID, named.ComputerID = named.ComputerID, nil
		command = named
	case contracts.ExternalAppCommandAppCancelUpdate:
		computerID, named.ComputerID = named.ComputerID, nil
		command = named
	case contracts.EngineCommandEngineUpdate:
		computerID, named.ComputerID = named.ComputerID, nil
		command = named
	default:
		return local
	}
	return towardNamed(state, computerID, func(holder *PairedComputer) InputRoute {
		return computersForwarded(holder, []WorkspaceInput{command}, computersForwardOptions{})
	})
}

// The computer a command names by id, which must still be paired. None, or `this`, is this computer.
func towardNamed(state *WorkspaceState, computerID *string, route func(computer *PairedComputer) InputRoute) InputRoute {
	if computerID == nil || *computerID == "" || *computerID == "this" {
		return local
	}
	computer := pairedNamed(state.Computers.Paired, computerID)
	if computer == nil {
		return computersRefused("That computer is no longer paired.")
	}
	return toward(computer, route)
}

// Commands that name their computer, which must be refused rather than run here once it is no longer paired.
func namesComputer(input WorkspaceInput) bool {
	switch input.(type) {
	case contracts.ProjectCommandProjectAdd, contracts.ExternalAppCommandAppUpdate, contracts.ExternalAppCommandAppCancelUpdate, contracts.EngineCommandEngineUpdate:
		return true
	}
	return false
}

// A folder is added on the computer the dialog names, whether or not any other is on screen.
func addingProject(state *WorkspaceState, input *contracts.ProjectCommandProjectAdd) InputRoute {
	return towardNamed(state, input.ComputerID, func(holder *PairedComputer) InputRoute {
		if input.Start != nil && *input.Start {
			return selecting(state, holder, []WorkspaceInput{contracts.ProjectCommandProjectAdd{Root: input.Root, Start: new(true)}})
		}
		return computersForwarded(holder, []WorkspaceInput{contracts.ProjectCommandProjectAdd{Root: input.Root}}, computersForwardOptions{})
	})
}

// Where a command goes, by the placement it declares. Events, and everything once no computer is paired, stay here.
func RouteInput(state *WorkspaceState, input WorkspaceInput) InputRoute {
	if namesComputer(input) {
		if add, ok := input.(contracts.ProjectCommandProjectAdd); ok {
			return addingProject(state, &add)
		}
		return namedComputer(state, input.(contracts.AppCommand))
	}
	if len(state.Computers.Paired) == 0 {
		return local
	}
	typ := InputType(input)
	command, ok := input.(contracts.AppCommand)
	if !ok || !contracts.IsAppCommandType(typ) {
		return local
	}
	var route computersRoute
	if at := contracts.WorkspaceViewInputCommandPlacement(typ); at == contracts.CommandPlacementOwn {
		route = own[typ]
	} else {
		route = placed[at]
	}
	if route == nil {
		return local
	}
	return route(state, command, SelectedComputer(state))
}

// Only missing capabilities hide controls. Ordinary refusals still reach dispatch and explain
// themselves, including offline hosts and drafts carrying local file paths.
func ComputerCommandAvailable(state *WorkspaceState, command contracts.AppCommand) bool {
	refused, ok := RouteInput(state, command).(InputRouteRefuse)
	return !ok || refused.Reason == nil || *refused.Reason != "unsupported"
}

// Which commands the paired computers take, as far as a capability subscriber cares: the computer
// on screen and each one's advertised commands.
type ComputerCapabilitySnapshot struct {
	Active *string
	Links  []ComputerCapabilityLink
}

type ComputerCapabilityLink struct {
	ID           string
	Status       domain.ComputerStatus
	Capabilities []string
}

// Streaming state does not change which commands a host knows. Keep capability subscribers still
// until host selection or an advertised list changes; controls read current state when invoked.
// The function returns the same pointer while nothing it compares changes.
func CreateComputerCapabilitySnapshot() func(computers *ComputersState) *ComputerCapabilitySnapshot {
	var previous *ComputerCapabilitySnapshot
	return func(computers *ComputersState) *ComputerCapabilitySnapshot {
		if previous != nil && sameOptionalText(previous.Active, computers.Active) && computersSameCapabilityLinks(previous.Links, computers.Paired) {
			return previous
		}
		links := make([]ComputerCapabilityLink, len(computers.Paired))
		for index, computer := range computers.Paired {
			links[index] = ComputerCapabilityLink{ID: computer.ID, Status: computer.Status, Capabilities: computer.Capabilities}
		}
		previous = &ComputerCapabilitySnapshot{Active: computers.Active, Links: links}
		return previous
	}
}

func computersSameCapabilityLinks(links []ComputerCapabilityLink, paired []*PairedComputer) bool {
	if len(links) != len(paired) {
		return false
	}
	for index, link := range links {
		computer := paired[index]
		if link.ID != computer.ID || link.Status != computer.Status || !jsonx.SameSlice(link.Capabilities, computer.Capabilities) {
			return false
		}
	}
	return true
}

// The computer's state while its line is up; a computer that cannot be reached counts nothing unread.
func reachable(computer *PairedComputer) *WorkspaceState {
	if computer.Status == domain.ComputerStatusConnected {
		return computer.State
	}
	return nil
}

// Which computers the sidebar's filter lets through.
func ShownComputers(computers *ComputersState) []*PairedComputer {
	switch computers.Filter {
	case "this":
		return []*PairedComputer{}
	case "all":
		return computers.Paired
	}
	shown := []*PairedComputer{}
	for _, computer := range computers.Paired {
		if computer.ID == computers.Filter {
			shown = append(shown, computer)
		}
	}
	return shown
}

// How a paired computer is named on what it holds, and whether its line is up. Reads only id, name and status.
func ThreadHostOf(computer *domain.ComputerLink) *ThreadHost {
	return &ThreadHost{ID: computer.ID, Name: computer.Name, Offline: computer.Status != domain.ComputerStatusConnected, Reconnecting: new(computer.Status == domain.ComputerStatusConnecting)}
}

// No work going on, which nothing adds to.
var noActivity = jsonx.Set[string]{}

// Which of a paired computer's threads are working and which wait on an approval, as it says. One
// whose line is down claims no work going on, as its sidebar rows do.
func RemoteActivity(computer *PairedComputer, holder *WorkspaceState) *ThreadActivity {
	activity := ThreadActivityThreadActivity(holder)
	if computer.Status == domain.ComputerStatusConnected {
		return activity
	}
	idle := *activity
	idle.Working, idle.Monitoring = noActivity, noActivity
	return &idle
}

// The threads a paired computer last said are doing work, whether or not its line is up now, so a
// line that drops is never taken for their work ending. One waiting on an approval is not.
func LastWorkingIds(holder *WorkspaceState) jsonx.Set[string] {
	activity := ThreadActivityThreadActivity(holder)
	working := jsonx.Set[string]{}
	for id := range activity.Working {
		if DoingWork(activity.Working, activity.Blocked, id) {
			working[id] = struct{}{}
		}
	}
	return working
}

var NoRemoteCollections = &RemoteCollections{
	Threads: []*domain.Thread{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{},
	WorktreeThreadIDs: jsonx.Set[string]{}, Busy: jsonx.Set[string]{}, Monitoring: jsonx.Set[string]{}, Ranked: jsonx.Set[string]{},
	Blocked: jsonx.Set[string]{}, Stoppable: jsonx.Set[string]{}, ThreadHosts: map[string]*ThreadHost{}, ProjectHosts: map[string]*ThreadHost{},
	SideChatAttention: jsonx.Set[string]{},
}

func ComputersRemoteCollections(computers *ComputersState) *RemoteCollections {
	shown := ShownComputers(computers)
	if len(shown) == 0 {
		return NoRemoteCollections
	}
	gathered := &RemoteCollections{
		Threads: []*domain.Thread{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{},
		WorktreeThreadIDs: jsonx.Set[string]{}, Busy: jsonx.Set[string]{}, Monitoring: jsonx.Set[string]{}, Ranked: jsonx.Set[string]{},
		Blocked: jsonx.Set[string]{}, Stoppable: jsonx.Set[string]{}, ThreadHosts: map[string]*ThreadHost{}, ProjectHosts: map[string]*ThreadHost{},
		SideChatAttention: jsonx.Set[string]{},
	}
	add := func(set jsonx.Set[string], ids jsonx.Set[string]) {
		for id := range ids {
			set[id] = struct{}{}
		}
	}
	for _, computer := range shown {
		remote := computer.State
		if remote == nil {
			continue
		}
		// A computer whose line is down still lists what it last sent, dimmed and taking nothing, so its
		// threads stay where the user left them. It claims no work going on and nothing unread meanwhile.
		live := computer.Status == domain.ComputerStatusConnected
		host := ThreadHostOf(&computer.ComputerLink)
		lists := ThreadActivityThreadLists(remote)
		for _, thread := range lists.VisibleThreads {
			gathered.Threads = append(gathered.Threads, thread)
			gathered.ThreadHosts[thread.ID] = host
		}
		for _, project := range remote.Projects {
			gathered.Projects = append(gathered.Projects, project)
			gathered.ProjectHosts[project.ID] = host
		}
		gathered.Worktrees = append(gathered.Worktrees, remote.Worktrees...)
		activity := RemoteActivity(computer, remote)
		add(gathered.WorktreeThreadIDs, lists.WorktreeThreadIDs)
		add(gathered.Busy, activity.Working)
		add(gathered.Monitoring, activity.Monitoring)
		if live {
			add(gathered.Stoppable, StoppableCoordinators(remote))
		}
		add(gathered.Ranked, activity.Ranked)
		add(gathered.Blocked, activity.Blocked)
		add(gathered.SideChatAttention, lists.SideChatAttention)
		if live {
			gathered.UnreadCount += lists.UnreadCount
		}
	}
	return gathered
}

// How many threads on the paired computers the sidebar shows carry an unseen mark, which the app icon adds to its own.
func RemoteUnreadCount(computers *ComputersState) float64 {
	count := 0.0
	for _, computer := range ShownComputers(computers) {
		if remote := reachable(computer); remote != nil {
			count += ThreadActivityThreadLists(remote).UnreadCount
		}
	}
	return count
}
