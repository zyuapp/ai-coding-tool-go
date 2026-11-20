package host

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// defaultPageText is how much page text a read returns when the caller does not say.
const defaultPageText = 4_000

// threadWaiter is a tool call held open until the state it waits on arrives, such as the thread it
// names stopping work.
type threadWaiter struct {
	pending func(state *application.WorkspaceState) bool
	settle  func(state *application.WorkspaceState)
	timer   *time.Timer
}

// ThreadWaiters are the tool calls held open, released by the commits that settle them. It stands
// for TypeScript's ThreadWaiterList, shared between the runtime and the requests that wait.
type ThreadWaiters struct {
	mu      sync.Mutex
	current []*threadWaiter
}

func (w *ThreadWaiters) remove(waiter *threadWaiter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for index, item := range w.current {
		if item == waiter {
			w.current = append(w.current[:index:index], w.current[index+1:]...)
			return
		}
	}
}

// clear stops every waiter's timer and lets them go unanswered.
func (w *ThreadWaiters) clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, waiter := range w.current {
		waiter.timer.Stop()
	}
	w.current = nil
}

// threadRequestHost is the runtime state and the command execution that answers each tool request.
type threadRequestHost struct {
	state    func() *application.WorkspaceState
	desktop  ThreadRequestHostDesktop
	dispatch func(ctx context.Context, input application.WorkspaceInput) error
	execute  func(input application.WorkspaceInput) application.WorkspaceExecution
	waiters  *ThreadWaiters
}

func (h threadRequestHost) reads() ReadHost {
	return ReadHost{State: h.state, Desktop: h.desktop}
}

// ReleaseThreadWaiters settles every waiter whose thread has settled in state, so the waiting tool call can answer.
func ReleaseThreadWaiters(waiters *ThreadWaiters, state *application.WorkspaceState) {
	waiters.mu.Lock()
	waiting := waiters.current
	if len(waiting) == 0 {
		waiters.mu.Unlock()
		return
	}
	var pending []*threadWaiter
	var settled []*threadWaiter
	for _, waiter := range waiting {
		if waiter.pending(state) {
			pending = append(pending, waiter)
		} else {
			waiter.timer.Stop()
			settled = append(settled, waiter)
		}
	}
	if len(settled) > 0 {
		waiters.current = pending
	}
	waiters.mu.Unlock()
	for _, waiter := range settled {
		waiter.settle(state)
	}
}

// threadRequestID is the requestId every thread request carries.
func threadRequestID(request contracts.ThreadRequest) string {
	value := reflect.ValueOf(request)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if field := value.FieldByName("RequestID"); field.IsValid() {
		return field.String()
	}
	return ""
}

// commandTaskID is the thread a command names, or nil.
func commandTaskID(command any) *string {
	field := reflect.ValueOf(command).FieldByName("TaskID")
	switch {
	case !field.IsValid():
		return nil
	case field.Kind() == reflect.String:
		return new(field.String())
	case field.IsNil():
		return nil
	}
	return new(field.Elem().String())
}

// withTaskID is command naming taskID.
func withTaskID[C any](command C, taskID string) C {
	value := reflect.ValueOf(command)
	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	field := copied.FieldByName("TaskID")
	if field.Kind() == reflect.String {
		field.SetString(taskID)
	} else {
		field.Set(reflect.ValueOf(&taskID))
	}
	return copied.Interface().(C)
}

// outcome reads a command's result: whether it was taken, why not, and the thread it named.
func outcome(result contracts.WorkspaceCommandResult) (ok bool, message string, taskID *string) {
	switch result := result.(type) {
	case contracts.WorkspaceCommandResultTrue:
		return true, "", result.TaskID
	case contracts.WorkspaceCommandResultFalse:
		return false, result.Message, nil
	}
	return true, "", nil
}

// completed runs an input and waits for it and its effects, answering with what it did.
func (h threadRequestHost) completed(ctx context.Context, input application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
	return h.execute(input).Completed(ctx)
}

func findThread(threads []*domain.Thread, id string) *domain.Thread {
	for _, thread := range threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}

func pairedComputer(state *application.WorkspaceState, id string) *application.PairedComputer {
	for _, computer := range state.Computers.Paired {
		if computer.ID == id {
			return computer
		}
	}
	return nil
}

// AnswerThreadRequest answers one tool request: reads come from the runtime projection, and writes use
// the shared command execution path. It never fails; a failure is the response. prepared is nil when
// preparing read nothing.
func AnswerThreadRequest(ctx context.Context, host threadRequestHost, request contracts.ThreadRequest, prepared *PreparedThreadRequest) contracts.ThreadResponse {
	requestID := threadRequestID(request)
	failed := func(message string) contracts.ThreadResponse {
		return contracts.ThreadResponseFalse{RequestID: requestID, Message: message}
	}
	result, err := answerThread(ctx, host, request, prepared)
	if err != nil {
		return failed(errorMessage(err))
	}
	if refusal, refused := result.(threadRefusal); refused {
		return failed(string(refusal))
	}
	raw, err := jsonx.Marshal(result)
	if err != nil {
		return failed(errorMessage(err))
	}
	return contracts.ThreadResponseTrue{RequestID: requestID, Result: raw}
}

// threadRefusal is a request answered with a failure rather than a result.
type threadRefusal string

func answerThread(ctx context.Context, host threadRequestHost, request contracts.ThreadRequest, prepared *PreparedThreadRequest) (any, error) {
	switch request := request.(type) {
	case contracts.ThreadRequestList:
		query := contracts.ThreadListQuery{Computer: request.Computer, Project: request.Project, Archived: request.Archived, IdleForMs: request.IdleForMs, Search: request.Search, Attachments: request.Attachments, Limit: request.Limit}
		var stored jsonx.Set[string]
		if prepared != nil {
			stored = prepared.Stored
		}
		return ListAcrossComputers(ctx, host.reads(), request.TaskID, query, stored)
	case contracts.ThreadRequestRead:
		selected := "all"
		if request.Computer != nil {
			selected = *request.Computer
		}
		return ReadAcrossComputers(ctx, host.reads(), request.ThreadID, request.Limit, selected)
	case contracts.ThreadRequestWait:
		return answerWait(ctx, host, request)
	case contracts.ThreadRequestBrowser:
		return answerBrowser(ctx, host, request)
	case contracts.ThreadRequestTerminal:
		return answerTerminal(ctx, host, request)
	case contracts.ThreadRequestSimulator:
		return readSimulator(ctx, host, request.TaskID, request.Read)
	case contracts.ThreadRequestNotify:
		return raiseFinding(ctx, host, request.TaskID, request.Report)
	case contracts.ThreadRequestNothingToReport:
		return reportNothing(ctx, host, request.TaskID, request.Checked)
	case contracts.ThreadRequestReport:
		return reportToCoordinator(ctx, host, request.TaskID, request.State, request.Summary)
	case contracts.ThreadRequestDecision:
		return raiseDecision(ctx, host, request.TaskID, request.Request)
	case contracts.ThreadRequestMemory:
		change, ok := request.Change.(domain.MemoryChange)
		if !ok {
			return nil, errors.New("Invalid memory change.")
		}
		return rememberFor(ctx, host, request.TaskID, change)
	case contracts.ThreadRequestPropose:
		return proposeThreads(ctx, host, request.TaskID, request.Proposals)
	case contracts.ThreadRequestCommand:
		return answerCommand(ctx, host, request)
	}
	return nil, errors.New("Invalid thread request.")
}

func answerWait(ctx context.Context, host threadRequestHost, request contracts.ThreadRequestWait) (any, error) {
	match, err := ResolveThreadRead(host.state(), request.ThreadID, "all")
	if err != nil {
		return nil, err
	}
	threadID := match.Thread.ID
	if match.Computer == nil {
		state, timedOut := waitFor(ctx, host, func(state *application.WorkspaceState) bool { return application.IsWorking(state, threadID) }, request.TimeoutMs)
		waited := application.ThreadWaitResult(state, threadID, timedOut)
		if !timedOut && threadID != request.TaskID {
			if err := host.dispatch(ctx, application.WorkspaceEventTaskResultCollected{TaskID: threadID}); err != nil {
				return nil, err
			}
		}
		return waited, nil
	}
	// A paired computer's thread is watched in the state it keeps publishing, which settles it here as it settles there.
	computerID := match.Computer.ID
	if err := Online(match.Computer, "waited on"); err != nil {
		return nil, err
	}
	state, timedOut := waitFor(ctx, host, func(state *application.WorkspaceState) bool {
		computer := pairedComputer(state, computerID)
		return computer != nil && computer.Status == domain.ComputerStatusConnected && computer.State != nil && application.IsWorking(computer.State, threadID)
	}, request.TimeoutMs)
	computer := pairedComputer(state, computerID)
	if computer == nil {
		return threadRefusal("That computer is no longer paired."), nil
	}
	if err := Online(computer, "waited on"); err != nil {
		return nil, err
	}
	var waited *contracts.ThreadWaitResult
	if computer.State != nil {
		waited = application.ThreadWaitResult(computer.State, threadID, timedOut)
	}
	if waited == nil {
		return threadRefusal(fmt.Sprintf("No thread has the ID %s.", threadID)), nil
	}
	// A thread whose history that computer has not loaded is mirrored without its messages, so its reply is read from there.
	thread := findThread(computer.State.Threads, threadID)
	unloaded := thread != nil && thread.HistorySummary != nil
	result := *waited
	if waited.Reply == nil && unloaded {
		transcript, err := ReadAcrossComputers(ctx, host.reads(), threadID, new(float64(maxRemoteMessages)), computerID)
		if err != nil {
			return nil, err
		}
		result.Reply = lastReply(transcript)
	}
	result.Thread.Computer = Tag(computer)
	return &result, nil
}

func answerBrowser(ctx context.Context, host threadRequestHost, request contracts.ThreadRequestBrowser) (any, error) {
	state := host.state()
	if approval := state.BrowserApproval; approval != nil && approval.TaskID == request.TaskID {
		return contracts.BrowserReadResultAwaitingApproval{URL: approval.URL}, nil
	}
	// A run reaches its own thread's dock, whichever dock the user has on screen.
	dock := application.DockFor(state, application.DockOwner(state, &request.TaskID))
	if _, tabs := request.Read.(contracts.BrowserReadTabs); tabs {
		return contracts.BrowserReadResultTabs{Tabs: nonNil(dock.BrowserTabs)}, nil
	}
	var tabID *string
	switch read := request.Read.(type) {
	case contracts.BrowserReadSnapshot:
		tabID = read.TabID
	case contracts.BrowserReadScreenshot:
		tabID = read.TabID
	case contracts.BrowserReadConsole:
		tabID = read.TabID
	case contracts.BrowserReadNetwork:
		tabID = read.TabID
	case contracts.BrowserReadWait:
		tabID = read.TabID
	}
	tab := application.BrowserTarget(dock, tabID)
	if tab == nil {
		return contracts.BrowserReadResultNoTab{}, nil
	}
	if err := host.desktop.ConfigureBrowserPermissions(ctx, application.BrowserPermissions(host.state())); err != nil {
		return nil, err
	}
	taskID := &request.TaskID
	var inspection domain.BrowserInspection
	switch read := request.Read.(type) {
	case contracts.BrowserReadScreenshot:
		shot, err := host.desktop.CaptureBrowserPage(ctx, tab.ID, read.FullPage != nil && *read.FullPage, read.TimeoutMs, taskID)
		if err != nil {
			return nil, err
		}
		if shot == nil {
			return contracts.BrowserReadResultNoTab{}, nil
		}
		return contracts.BrowserReadResultShot{Shot: *shot}, nil
	case contracts.BrowserReadConsole:
		inspection = domain.BrowserInspectionConsole{Since: read.Since, Limit: read.Limit, MinimumLevel: read.MinimumLevel}
	case contracts.BrowserReadNetwork:
		inspection = domain.BrowserInspectionNetwork{Since: read.Since, Limit: read.Limit, FailuresOnly: read.FailuresOnly}
	case contracts.BrowserReadWait:
		inspection = domain.BrowserInspectionWait{Condition: read.Condition, Value: read.Value, TimeoutMs: read.TimeoutMs}
	case contracts.BrowserReadSnapshot:
		limit := float64(defaultPageText)
		if read.TextLimit != nil {
			limit = *read.TextLimit
		}
		snapshot, err := host.desktop.ReadBrowserPage(ctx, tab.ID, limit, read.TimeoutMs, taskID)
		if err != nil {
			return nil, err
		}
		if snapshot == nil {
			return contracts.BrowserReadResultNoTab{}, nil
		}
		return contracts.BrowserReadResultSnapshot{Snapshot: *snapshot}, nil
	}
	inspected, err := host.desktop.InspectBrowserPage(ctx, tab.ID, inspection, taskID)
	if err != nil {
		return nil, err
	}
	if inspected == nil {
		return contracts.BrowserReadResultNoTab{}, nil
	}
	return inspected, nil
}

func nonNil[E any](items []E) []E {
	if items == nil {
		return []E{}
	}
	return items
}

func answerTerminal(ctx context.Context, host threadRequestHost, request contracts.ThreadRequestTerminal) (any, error) {
	state := host.state()
	dock := application.DockFor(state, application.DockOwner(state, &request.TaskID))
	read, snapshot := request.Read.(contracts.TerminalReadSnapshot)
	if !snapshot {
		return contracts.TerminalReadResultTerminals{Terminals: nonNil(dock.Terminals)}, nil
	}
	terminal := application.TerminalTarget(dock, read.TerminalID, &request.TaskID)
	if terminal == nil {
		return contracts.TerminalReadResultNoTerminal{}, nil
	}
	options := contracts.TerminalReadOptions{Lines: float64(domain.TerminalLineLimit(read.Lines))}
	if read.Match != nil && *read.Match != "" {
		options.Match = read.Match
	}
	text, err := host.desktop.ReadTerminal(ctx, terminal.ID, options)
	if err != nil {
		return nil, err
	}
	if text == nil {
		return contracts.TerminalReadResultNoTerminal{}, nil
	}
	return contracts.TerminalReadResultSnapshot{Snapshot: domain.TerminalSnapshot{
		TerminalID: terminal.ID,
		Title:      terminal.Title,
		Cwd:        terminal.Cwd,
		Status:     domain.TerminalSnapshotStatus(terminal.Status),
		ExitCode:   terminal.ExitCode,
		Error:      terminal.Error,
		Lines:      nonNil(text.Lines),
		Omitted:    text.Omitted,
		Matched:    text.Matched,
	}}, nil
}

func answerCommand(ctx context.Context, host threadRequestHost, request contracts.ThreadRequestCommand) (any, error) {
	command := request.Command
	before := host.state()
	kind := application.InputType(command.(application.WorkspaceInput))
	// A browser or simulator command acts on a panel rather than a thread, so it answers with the panel's own error.
	if strings.HasPrefix(kind, "browser.") || strings.HasPrefix(kind, "simulator.") {
		result, err := host.completed(ctx, command.(application.WorkspaceInput))
		if err != nil {
			return nil, err
		}
		if ok, message, _ := outcome(result); !ok {
			return threadRefusal(message), nil
		}
		return contracts.ThreadCommandResult{}, nil
	}
	if deleting, ok := command.(contracts.WorktreeCommandWorktreeDelete); ok {
		for _, worktree := range before.Worktrees {
			if deleting.WorktreeID != nil && worktree.ID == *deleting.WorktreeID {
				if worktree.Root != "" && contains(before.DeletingWorktrees, worktree.Root) {
					return threadRefusal("That worktree is already being deleted."), nil
				}
				break
			}
		}
		result, err := host.completed(ctx, command.(application.WorkspaceInput))
		if err != nil {
			return nil, err
		}
		if ok, message, _ := outcome(result); !ok {
			return threadRefusal(message), nil
		}
		return contracts.ThreadCommandResult{Notice: host.state().WorktreeManagementNotice}, nil
	}
	// A thread is named the way the tools name it, and found on this computer or a paired one.
	var target *ThreadReadMatch
	if taskID := commandTaskID(command); taskID != nil {
		match, err := ResolveThreadRead(before, *taskID, "all")
		if err != nil {
			return nil, err
		}
		target = &match
	}
	caller := findThread(before.Threads, request.TaskID)
	if (kind == "task.send" || kind == "task.move-worktree" || kind == "task.dismiss") && target == nil && caller == nil {
		return threadRefusal(fmt.Sprintf("No thread has the ID %s.", request.TaskID)), nil
	}
	// A thread moving itself is mid-run, so it goes once that run ends; a move naming no thread is the caller's.
	var ownMove *contracts.TaskCommandTaskMoveWorktree
	if move, ok := command.(contracts.TaskCommandTaskMoveWorktree); ok && caller != nil && (target == nil || target.Thread.ID == caller.ID && target.Computer == nil) {
		move.TaskID, move.AfterRun = &caller.ID, new(true)
		ownMove = &move
	}
	if move, ok := command.(contracts.TaskCommandTaskMoveWorktree); ok && move.Then != nil && ownMove == nil {
		return threadRefusal(moveFollowUpError), nil
	}
	// A dismiss naming no thread is the caller's own.
	named := command
	switch {
	case ownMove != nil:
		named = *ownMove
	case kind == "task.dismiss" && target == nil && caller != nil:
		named = withTaskID(command, caller.ID)
	case target != nil:
		named = withTaskID(command, target.Thread.ID)
	}
	// What no thread asks of a coordinator is refused before it goes anywhere, here or on a paired computer.
	var subject *domain.Thread
	if target != nil && target.Computer != nil {
		subject = target.Thread
	} else if taskID := commandTaskID(named); taskID != nil {
		subject = findThread(before.Threads, *taskID)
	}
	namedKind := application.InputType(named.(application.WorkspaceInput))
	if subject != nil && (namedKind == "task.move-worktree" || namedKind == "task.set-role") {
		creating := (target == nil || target.Computer == nil) && contains(before.CreatingWorktrees, subject.ID)
		if refusal := application.CoordinatorAskRefusal(subject, named.(contracts.AppCommand), creating); refusal != nil && *refusal != "" {
			return threadRefusal(*refusal), nil
		}
	}
	// A new thread starts by the rules every start shares; a command for an existing thread goes where that thread is.
	var start *application.NewThreadStart
	if send, ok := named.(contracts.TaskCommandTaskSend); ok && send.TaskID == nil && caller != nil {
		var asked jsonx.Opt[*application.PairedComputer]
		if request.Computer != nil {
			computer, err := NamedComputer(before, *request.Computer)
			if err != nil {
				return nil, err
			}
			asked = jsonx.Some(computer)
		}
		made := application.ThreadStartsNewThreadSend(before, caller, send, asked)
		start = &made
	}
	if start != nil && start.Error != nil {
		return threadRefusal(*start.Error), nil
	}
	if start != nil && start.Elsewhere != nil {
		if start.Lead != nil {
			return proposeElsewhere(ctx, host, caller, start.Lead, start.Elsewhere, named.(contracts.TaskCommandTaskSend))
		}
		return commandElsewhere(ctx, host, start.Elsewhere, *start.Command, caller)
	}
	if target != nil && target.Computer != nil {
		return commandElsewhere(ctx, host, target.Computer, named, caller)
	}
	// A message a thread sends names the thread it came from, even one to itself, so it is never taken for the user's.
	signed := named
	if start != nil {
		signed = *start.Command
	} else if send, ok := named.(contracts.TaskCommandTaskSend); ok && caller != nil {
		send.From = &caller.ID
		signed = send
	}
	result, err := host.completed(ctx, signed.(application.WorkspaceInput))
	if err != nil {
		return nil, err
	}
	ok, message, resultTask := outcome(result)
	if !ok {
		return threadRefusal(message), nil
	}
	after := host.state()
	taskID := resultTask
	if taskID == nil {
		taskID = commandTaskID(named)
	}
	var thread *domain.Thread
	if taskID != nil {
		thread = findThread(after.Threads, *taskID)
	}
	answer := contracts.ThreadCommandResult{}
	if thread != nil {
		answer.Thread = application.ThreadSummary(after, thread, nil)
		if waiting := after.MovesAfterRun[thread.ID]; ownMove != nil && waiting != nil {
			answer.Notice = new(movesAfterTurn(after, thread, waiting))
		}
	}
	return answer, nil
}

func contains(items []string, item string) bool {
	for _, held := range items {
		if held == item {
			return true
		}
	}
	return false
}

const moveFollowUpError = "Only a thread moving itself passes then. To have another thread go on after its move, message it with message_thread."

// movesAfterTurn is what a thread moving itself is told: where it is until this turn ends, and where
// and how its next turn starts.
func movesAfterTurn(state *application.WorkspaceState, thread *domain.Thread, move *domain.MoveAfterRun) string {
	var project *string
	if found := application.ProjectFor(state, thread); found != nil {
		project = &found.Root
	}
	var there *string
	switch destination := move.Destination.(type) {
	case domain.WorktreeDestinationWorktree:
		if worktree := application.WorktreeByID(state, &destination.ID); worktree != nil {
			there = &worktree.Root
		}
	case domain.WorktreeDestinationNew:
		there = new("a new worktree")
	default:
		there = project
	}
	here := project
	if worktree := application.WorktreeFor(state, thread); worktree != nil {
		here = &worktree.Root
	}
	next := "its next turn starts there"
	if move.Then != nil && *move.Then != "" {
		next = "its next turn starts there on its own with what you passed as then. End this turn now, and do that work there"
	}
	return fmt.Sprintf("This thread moves to %s when this turn ends, and %s. Until then it stays in %s, and file changes already made there stay behind.", jsString(there), next, jsString(here))
}

// jsString is how a template literal writes an optional string.
func jsString(value *string) string {
	if value == nil {
		return "undefined"
	}
	return *value
}

// dockSimulator is the device a thread's own dock shows, which is the only one its run drives; nil for none.
func dockSimulator(state *application.WorkspaceState, taskID string) *domain.SimulatorDevice {
	udid := application.DockFor(state, application.DockOwner(state, &taskID)).SimulatorID
	if udid == nil || *udid == "" {
		return nil
	}
	for _, device := range state.Simulator.Devices {
		if device.UDID == *udid {
			return device
		}
	}
	return nil
}

func readSimulator(ctx context.Context, host threadRequestHost, taskID string, read contracts.SimulatorRead) (contracts.SimulatorReadResult, error) {
	before := host.state()
	if unsupported, ok := before.Simulator.Availability.(domain.SimulatorAvailabilityUnsupported); ok {
		return contracts.SimulatorReadResultNoSimulator{Reason: unsupported.Reason}, nil
	}
	if len(before.Simulator.Devices) == 0 {
		return contracts.SimulatorReadResultNoSimulator{Reason: "This Mac has no iOS simulators. Add one in Xcode."}, nil
	}
	if dockSimulator(before, taskID) == nil {
		return contracts.SimulatorReadResultNoSimulator{Reason: "Open the simulator first with simulator_open."}, nil
	}
	switch read := read.(type) {
	case contracts.SimulatorReadStatus:
		settledBoot := func(state *application.WorkspaceState) bool {
			device := dockSimulator(state, taskID)
			return device != nil && contains(state.Simulator.Booting, device.UDID)
		}
		state, _ := waitFor(ctx, host, settledBoot, read.TimeoutMs)
		device := dockSimulator(state, taskID)
		if device == nil {
			return contracts.SimulatorReadResultNoSimulator{Reason: "Open the simulator first with simulator_open."}, nil
		}
		result := contracts.SimulatorReadResultStatus{Device: *device, Booted: device.State == domain.SimulatorDeviceStateBooted}
		if failure := state.Simulator.Errors[device.UDID]; failure != "" {
			result.Error = &failure
		}
		return result, nil
	case contracts.SimulatorReadSnapshot:
		device := dockSimulator(before, taskID)
		if device.State != domain.SimulatorDeviceStateBooted {
			return contracts.SimulatorReadResultNoSimulator{Reason: device.Name + " is not running yet. Call simulator_open and wait for it."}, nil
		}
		var (
			snapshot    *domain.SimulatorSnapshot
			shot        *domain.SimulatorShot
			snapshotErr error
			shotErr     error
			wait        sync.WaitGroup
		)
		wait.Add(1)
		go func() {
			defer wait.Done()
			snapshot, snapshotErr = host.desktop.DescribeSimulator(ctx, device.UDID, device.Name)
		}()
		if read.Screenshot {
			wait.Add(1)
			go func() {
				defer wait.Done()
				shot, shotErr = host.desktop.CaptureSimulator(ctx, device.UDID)
			}()
		}
		wait.Wait()
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		if shotErr != nil {
			return nil, shotErr
		}
		result := contracts.SimulatorReadResultSnapshot{Shot: shot}
		if snapshot != nil {
			result.Snapshot = *snapshot
		}
		return result, nil
	}
	return nil, errors.New("Invalid simulator read.")
}

// waitFor holds the call until pending stops holding, or until timeoutMs runs out, and answers with
// the state that settled it.
func waitFor(ctx context.Context, host threadRequestHost, pending func(state *application.WorkspaceState) bool, timeoutMs float64) (state *application.WorkspaceState, timedOut bool) {
	type settled struct {
		state    *application.WorkspaceState
		timedOut bool
	}
	answer := make(chan settled, 1)
	waiters := host.waiters
	waiters.mu.Lock()
	if current := host.state(); !pending(current) {
		waiters.mu.Unlock()
		return current, false
	}
	waiter := &threadWaiter{pending: pending}
	waiter.settle = func(state *application.WorkspaceState) {
		select {
		case answer <- settled{state, false}:
		default:
		}
	}
	waiter.timer = time.AfterFunc(time.Duration(max(0, timeoutMs))*time.Millisecond, func() {
		waiters.remove(waiter)
		select {
		case answer <- settled{host.state(), true}:
		default:
		}
	})
	waiters.current = append(waiters.current, waiter)
	waiters.mu.Unlock()
	select {
	case result := <-answer:
		return result.state, result.timedOut
	case <-ctx.Done():
		waiter.timer.Stop()
		waiters.remove(waiter)
		return host.state(), true
	}
}

// maxRemoteMessages is as many messages as a paired computer hands over in one read.
const maxRemoteMessages = 200

// lastReply is the newest assistant message's text, or nil for none.
func lastReply(transcript *contracts.ThreadTranscript) *string {
	for index := len(transcript.Messages) - 1; index >= 0; index-- {
		if transcript.Messages[index].Kind == domain.ConversationMessageKindAssistant {
			return &transcript.Messages[index].Text
		}
	}
	return nil
}

const (
	signedSend = "command:task.send:sender"
	signedMove = "command:task.move-worktree:sender"
	signedRole = "command:task.set-role:sender"
)

// arrivalMs is how long a thread just started on a paired computer is given to show up in the state that computer publishes.
const arrivalMs = 5_000

// commandElsewhere sends a command for a paired computer's thread, or for a new thread there, through
// the reducer, which carries it to that computer. The message says which thread sent it, since that
// computer may not hold this one's threads, and a new thread starts in the project there that this
// one's own project matches unless another is named. caller is nil when no thread of this computer asked.
func commandElsewhere(ctx context.Context, host threadRequestHost, computer *application.PairedComputer, command contracts.ExternalCommand, caller *domain.Thread) (*contracts.ThreadCommandResult, error) {
	if err := Online(computer, "reached"); err != nil {
		return nil, err
	}
	if computer.State == nil {
		return nil, fmt.Errorf("%s has not sent its threads yet. Try again once it is connected.", computer.Name)
	}
	state := host.state()
	var sender *domain.RemoteSender
	if caller != nil {
		name := state.Computers.Name
		if name == "" {
			name = "another computer"
		}
		sender = &domain.RemoteSender{ThreadID: caller.ID, Title: caller.Title, Computer: name}
	}
	// A build from before capabilities were told names none and would take the message as the user's, so a signed one needs the capability named.
	send, sending := command.(contracts.TaskCommandTaskSend)
	if sending && sender != nil && !contains(computer.Capabilities, signedSend) {
		return nil, errors.New(contracts.RemoteUnsupported)
	}
	if sending && send.TaskID == nil {
		var own *domain.Project
		if caller != nil {
			for _, project := range state.Projects {
				if caller.ProjectID != nil && project.ID == *caller.ProjectID {
					own = project
					break
				}
			}
		}
		project, err := application.ProjectOnComputer(computer, send.Project, own)
		if err != nil {
			return nil, err
		}
		send.Project = &project.ID
		if sender != nil {
			send.Sender = sender
		}
		result, err := host.completed(ctx, send)
		if err != nil {
			return nil, err
		}
		ok, message, taskID := outcome(result)
		if !ok {
			return nil, errors.New(message)
		}
		if taskID == nil {
			return nil, fmt.Errorf("%s did not say which thread it started.", computer.Name)
		}
		arrived, _ := waitFor(ctx, host, func(state *application.WorkspaceState) bool { return RemoteSummary(state, computer.ID, *taskID) == nil }, arrivalMs)
		if thread := RemoteSummary(arrived, computer.ID, *taskID); thread != nil {
			return &contracts.ThreadCommandResult{Thread: thread}, nil
		}
		return &contracts.ThreadCommandResult{Notice: new(fmt.Sprintf("Started thread %s on %s.", *taskID, computer.Name))}, nil
	}
	// A move or a role says which thread asked where that computer can hear it, so it holds them to the rules a thread keeps there too.
	signed := command
	switch command := command.(type) {
	case contracts.TaskCommandTaskSend:
		if sender != nil {
			command.Sender = sender
			signed = command
		}
	case contracts.TaskCommandTaskMoveWorktree:
		if sender != nil && contains(computer.Capabilities, signedMove) {
			command.Sender = sender
			signed = command
		}
	case contracts.TaskCommandTaskSetRole:
		if sender != nil && contains(computer.Capabilities, signedRole) {
			command.Sender = sender
			signed = command
		}
	}
	result, err := host.completed(ctx, signed.(application.WorkspaceInput))
	if err != nil {
		return nil, err
	}
	if ok, message, _ := outcome(result); !ok {
		return nil, errors.New(message)
	}
	answer := &contracts.ThreadCommandResult{}
	if taskID := commandTaskID(command); taskID != nil {
		answer.Thread = RemoteSummary(host.state(), computer.ID, *taskID)
	}
	return answer, nil
}

// proposeElsewhere files a thread a coordinator, or one of the threads under it, asks to start on a
// paired computer. Nothing starts there until the user allows it: the request is checked as its start
// would be, placed in the project there it would start in, and waits beside the coordinator's
// suggestions for the user.
func proposeElsewhere(ctx context.Context, host threadRequestHost, caller, lead *domain.Thread, computer *application.PairedComputer, command application.NewThreadSend) (*contracts.ThreadCommandResult, error) {
	prompt := ""
	if command.Text != nil {
		prompt = *command.Text
	}
	title := ""
	if command.Title != nil {
		title = *command.Title
	}
	asked := domain.ProposalRequest{
		Title: title, Prompt: prompt, Brief: command.Brief, Project: command.Project, Worktree: command.Worktree, WorktreeID: command.WorktreeID,
		Engine: command.Engine, Model: command.Model, Effort: command.Effort, Role: command.Role,
	}
	proposal, err := application.ThreadProposal(host.state(), caller, asked, application.Now(), jsonx.Some(computer))
	if err != nil {
		return nil, err
	}
	kept, err := fileProposals(ctx, host, lead.ID, []*domain.ThreadProposal{proposal})
	if err != nil {
		return nil, err
	}
	if !kept[proposal.ID] {
		return nil, errors.New(proposalsFull)
	}
	folder := "its project"
	if computer.State != nil {
		for _, project := range computer.State.Projects {
			if proposal.Place.Project != nil && project.ID == *proposal.Place.Project {
				if project.Root != "" {
					folder = project.Root
				}
				break
			}
		}
	}
	news := "Your coordinator hears once it starts."
	if caller.ID == lead.ID {
		news = "You hear once it starts and when it ends a turn."
	}
	return &contracts.ThreadCommandResult{Notice: new(fmt.Sprintf("Waiting for the user to allow it on %s in %s. %s", computer.Name, folder, news))}, nil
}

// unscheduled is what the tools say when the caller is not a scheduled run at all. Nothing is written for one.
const unscheduled = "This turn is not a scheduled run, so there is nothing to surface or to silence and nothing was recorded. Say what you found in your reply instead; these two tools are only for a run the automation's schedule started."

func unreadCount(host threadRequestHost, threadID string) int {
	if thread := findThread(host.state().Threads, threadID); thread != nil {
		return len(domain.UnreadFindings(thread))
	}
	return 0
}

// newestFindingId names the newest finding a thread carries, so two reads of it can be compared; nil for none.
func newestFindingId(state *application.WorkspaceState, threadID string) *string {
	thread := findThread(state.Threads, threadID)
	if thread == nil || len(thread.Findings) == 0 {
		return nil
	}
	return &thread.Findings[len(thread.Findings)-1].ID
}

func sameText(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// takenOver: a run the user answered or steered into is theirs from then on, and what it finds answers them.
const takenOver = "The user joined this run while the report was going in, so it is theirs to answer now and nothing was recorded. Tell them what you found in your reply instead."

// raiseFinding records what a run found. The command goes in whatever the thread does with it, so a
// run that raises an issue the thread has not heard of can never afterwards be settled unseen. The
// answer reports what the thread did, read back after the fact.
func raiseFinding(ctx context.Context, host threadRequestHost, threadID string, report contracts.FindingReport) (*contracts.FindingResult, error) {
	before := host.state()
	thread := findThread(before.Threads, threadID)
	if thread == nil || application.ScheduledRun(before, threadID) == nil {
		return &contracts.FindingResult{Recorded: false, Note: unscheduled}, nil
	}
	key := ""
	if report.Key != nil {
		key = *report.Key
	}
	known := !domain.IsNews(thread, key)
	newestBefore := newestFindingId(before, threadID)
	if err := host.dispatch(ctx, contracts.AutomationCommandAutomationNotify{TaskID: threadID, Headline: report.Headline, Detail: report.Detail, Key: report.Key}); err != nil {
		return nil, err
	}
	if sameText(newestFindingId(host.state(), threadID), newestBefore) {
		if known {
			return &contracts.FindingResult{Recorded: false, Note: fmt.Sprintf(`This thread already carries a finding keyed "%s", so the same one was held back. Raising only what it already knows lets this run settle unseen.`, jsString(report.Key))}, nil
		}
		return &contracts.FindingResult{Recorded: false, Note: takenOver}, nil
	}
	unread := unreadCount(host, threadID)
	carried := "The user is looking at this thread, so it is already seen"
	if unread != 0 {
		noun := "findings"
		if unread == 1 {
			noun = "finding"
		}
		carried = fmt.Sprintf("This thread now carries %d unread %s", unread, noun)
	}
	return &contracts.FindingResult{Recorded: true, Note: fmt.Sprintf("Raised. %s, and the run surfaces when it settles.", carried)}, nil
}

// reportNothing is a run saying it looked and found nothing: it answers for the tick without raising
// anything, which is what leaves a quiet one its silence.
func reportNothing(ctx context.Context, host threadRequestHost, threadID, checked string) (*contracts.FindingResult, error) {
	active := application.RunRecord(application.ScheduledRun(host.state(), threadID))
	if active == nil {
		return &contracts.FindingResult{Recorded: false, Note: unscheduled}, nil
	}
	if err := host.dispatch(ctx, contracts.AutomationCommandAutomationNothingToReport{TaskID: threadID, Checked: checked}); err != nil {
		return nil, err
	}
	if active.Notified {
		return &contracts.FindingResult{Recorded: false, Note: "This run already raised something new, so it surfaces anyway and what it found stands."}, nil
	}
	if !active.Quiet {
		return &contracts.FindingResult{Recorded: true, Note: "Noted. " + loudRunNote(host.state(), threadID)}, nil
	}
	return &contracts.FindingResult{Recorded: true, Note: "Noted. This run settles without reaching the user."}, nil
}

// loudRunNote says why a scheduled run surfaces however it ends, so the run tells the user that rather
// than calling itself quiet.
func loudRunNote(state *application.WorkspaceState, threadID string) string {
	var automation *domain.AutomationView
	for _, item := range state.Automations {
		if item.TaskID == threadID {
			automation = item
			break
		}
	}
	if automation != nil && domain.IsOneShotSchedule(automation.Schedule) {
		return "A one-time run always surfaces, this one included."
	}
	if automation != nil && automation.SurfaceWhen != nil && *automation.SurfaceWhen != "" {
		return "The user started this run by hand, so it surfaces however it ends."
	}
	return "This automation has no quiet sentence, so every run of it surfaces, this one included."
}

// answered hands a thread's own report, decision or memory change (a coordination.reported,
// coordination.decision-raised or coordination.remembered input) to the reducer, and answers with
// what it recorded or why it recorded nothing as of when it takes the request, whatever waited ahead of it.
func answered(ctx context.Context, host threadRequestHost, input application.WorkspaceInput) (contracts.CoordinationAnswer, error) {
	execution := host.execute(input)
	accepted, err := execution.Accepted(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := execution.Completed(ctx); err != nil {
		return nil, err
	}
	switch accepted := accepted.(type) {
	case contracts.WorkspaceCommandResultFalse:
		return nil, errors.New(accepted.Message)
	case contracts.WorkspaceCommandResultTrue:
		if accepted.Answer != nil {
			return accepted.Answer, nil
		}
	}
	return contracts.CoordinationAnswerTrue{}, nil
}

// reportToCoordinator is a thread under a coordinator saying where its work stands. Its coordinator hears it when this turn ends.
func reportToCoordinator(ctx context.Context, host threadRequestHost, threadID string, state domain.CoordinationState, summary string) (*contracts.FindingResult, error) {
	answer, err := answered(ctx, host, application.WorkspaceEventCoordinationReported{TaskID: threadID, State: state, Summary: summary})
	if err != nil {
		return nil, err
	}
	if refusal, ok := answer.(contracts.CoordinationAnswerFalse); ok {
		return &contracts.FindingResult{Recorded: false, Note: refusal.Reason}, nil
	}
	if state == domain.CoordinationStateWorking {
		return &contracts.FindingResult{Recorded: true, Note: "Noted. Progress is not passed on; report again when you are blocked, done, or failed."}, nil
	}
	return &contracts.FindingResult{Recorded: true, Note: "Reported. Your coordinator hears it when this turn ends."}, nil
}

// raiseDecision puts a choice to the user. Only a coordinator and the threads under it have somewhere to show one.
func raiseDecision(ctx context.Context, host threadRequestHost, threadID string, request domain.DecisionRequest) (*contracts.FindingResult, error) {
	answer, err := answered(ctx, host, application.WorkspaceEventCoordinationDecisionRaised{TaskID: threadID, Request: request})
	if err != nil {
		return nil, err
	}
	if refusal, ok := answer.(contracts.CoordinationAnswerFalse); ok {
		return &contracts.FindingResult{Recorded: false, Note: refusal.Reason}, nil
	}
	return &contracts.FindingResult{Recorded: true, Note: "Raised. The user sees it now, and their answer arrives in this thread as a message. If you cannot go on without it, end your turn."}, nil
}

// readFromNow is what a coordinator and every thread it starts from now on read, said back as the coordinator's tools answer.
const readFromNow = "You and every thread you start from now on read it."

// noteResult is the `{ note }` a coordinator's own tools answer with.
type noteResult struct {
	Note string `json:"note"`
}

// rememberFor is a coordinator changing its own memory. A refusal fails the tool rather than reaching
// the user; once made, the saved note is read back.
func rememberFor(ctx context.Context, host threadRequestHost, threadID string, change domain.MemoryChange) (noteResult, error) {
	answer, err := answered(ctx, host, application.WorkspaceEventCoordinationRemembered{TaskID: threadID, Change: change})
	if err != nil {
		return noteResult{}, err
	}
	if refusal, ok := answer.(contracts.CoordinationAnswerFalse); ok {
		return noteResult{}, errors.New(refusal.Reason)
	}
	var memory *domain.CoordinatorMemory
	if thread := findThread(host.state().Threads, threadID); thread != nil {
		memory = thread.Memory
	}
	switch change := change.(type) {
	case domain.MemoryChangePurpose:
		if memory != nil && memory.Purpose != nil && *memory.Purpose != "" {
			return noteResult{"Purpose set. " + readFromNow}, nil
		}
		return noteResult{"Purpose cleared."}, nil
	case domain.MemoryChangeForget:
		return noteResult{fmt.Sprintf("Forgot [%s].", change.NoteID)}, nil
	}
	// The window named the note the change saved, so a note that matches it only in text is never taken for it.
	var noteID *string
	if recorded, ok := answer.(contracts.CoordinationAnswerTrue); ok {
		noteID = recorded.NoteID
	}
	var saved *domain.MemoryNote
	if noteID != nil && memory != nil {
		for _, note := range memory.Notes {
			if note.ID == *noteID {
				saved = note
				break
			}
		}
	}
	var text string
	switch change := change.(type) {
	case domain.MemoryChangeRemember:
		text = change.Text
	case domain.MemoryChangeEdit:
		text = change.Text
	}
	if saved == nil || saved.Text != jsTrim(text) {
		return noteResult{}, errors.New("The note could not be saved. Try again.")
	}
	return noteResult{fmt.Sprintf("Saved as [%s]. %s", saved.ID, readFromNow)}, nil
}

var proposalsFull = fmt.Sprintf("At most %d suggestions wait at once. Wait for the user to start or dismiss some.", domain.MaxProposals)

// checkedProposal checks one suggestion as its start would be, on the paired computer it names when
// it names one. The error is the refusal.
func checkedProposal(state *application.WorkspaceState, lead *domain.Thread, request *domain.ProposalRequest, at float64) (*domain.ThreadProposal, error) {
	var asked jsonx.Opt[*application.PairedComputer]
	if request.Computer != nil {
		computer, err := NamedComputer(state, *request.Computer)
		if err != nil {
			return nil, err
		}
		asked = jsonx.Some(computer)
	}
	return application.ThreadProposal(state, lead, *request, at, asked)
}

// fileProposals files suggestions on their coordinator, as many as it has room for, and reads back
// the ids of the ones it kept, since the user may have filled the room meanwhile.
func fileProposals(ctx context.Context, host threadRequestHost, leadID string, proposals []*domain.ThreadProposal) (map[string]bool, error) {
	if len(proposals) > 0 {
		if err := host.dispatch(ctx, application.WorkspaceEventCoordinationProposed{TaskID: leadID, Proposals: proposals}); err != nil {
			return nil, err
		}
	}
	held := map[string]bool{}
	if lead := findThread(host.state().Threads, leadID); lead != nil {
		for _, proposal := range lead.Proposals {
			held[proposal.ID] = true
		}
	}
	kept := map[string]bool{}
	for _, proposal := range proposals {
		if held[proposal.ID] {
			kept[proposal.ID] = true
		}
	}
	return kept, nil
}

// proposeThreads is a coordinator suggesting threads. Each is checked now as its start would be, so
// what cannot start comes straight back, item by item, and only what can waits for the user. Nothing
// starts here.
func proposeThreads(ctx context.Context, host threadRequestHost, threadID string, requests []*domain.ProposalRequest) (noteResult, error) {
	state := host.state()
	lead := findThread(state.Threads, threadID)
	if lead == nil || !domain.IsCoordinator(lead) {
		return noteResult{}, errors.New("Only a coordinator suggests threads.")
	}
	at := application.Now()
	type checked struct {
		request  *domain.ProposalRequest
		proposal *domain.ThreadProposal
		err      error
	}
	checks := make([]checked, len(requests))
	var made []*domain.ThreadProposal
	for index, request := range requests {
		proposal, err := checkedProposal(state, lead, request, at)
		checks[index] = checked{request, proposal, err}
		if err == nil {
			made = append(made, proposal)
		}
	}
	kept, err := fileProposals(ctx, host, threadID, made)
	if err != nil {
		return noteResult{}, err
	}
	var lines []string
	for _, check := range checks {
		if check.err != nil {
			lines = append(lines, fmt.Sprintf(`Not suggested "%s": %s`, check.request.Title, errorMessage(check.err)))
		} else if !kept[check.proposal.ID] {
			lines = append(lines, fmt.Sprintf(`Not suggested "%s": %s`, check.proposal.Title, proposalsFull))
		}
	}
	if len(kept) == 0 {
		return noteResult{}, errors.New(strings.Join(lines, "\n"))
	}
	count := "1 thread"
	if len(kept) != 1 {
		count = fmt.Sprintf("%d threads", len(kept))
	}
	said := fmt.Sprintf("Suggested %s. The user starts the ones they want; you hear from each once it ends a turn.", count)
	return noteResult{strings.Join(append([]string{said}, lines...), "\n")}, nil
}
