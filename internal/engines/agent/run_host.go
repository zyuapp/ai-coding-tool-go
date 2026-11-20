package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// Resolves a workspace id to its folder; *workspace.WorkspaceService satisfies it.
type WorkspaceResolver interface {
	Resolve(id string) (workspace.WorkspaceResolution, error)
}

// The policy the run asking may grant, or why it may no longer ask.
type AutomationAuthority func() (domain.ExecutionPolicy, error)

// The automation scheduler as the run host asks it (src/main/automation/automation-scheduler.mts).
type AutomationScheduler interface {
	ForThread(taskID string) *domain.AutomationView
	List() []*domain.AutomationView
	Save(ctx context.Context, draft domain.AutomationDraft, authority AutomationAuthority) (*domain.AutomationView, error)
	Update(ctx context.Context, taskID string, patch domain.AutomationPatch, authority AutomationAuthority) (*domain.AutomationView, error)
	Remove(ctx context.Context, taskID string) (bool, error)
}

// What the agent side needs from the host: the runtime it reports to, and the services a run resolves against.
type RunHostConfig struct {
	// Hands the runtime what a run reported. Called in order, with the host's lock held: it must not
	// call back into the RunBridge synchronously.
	Publish func(event contracts.AgentEvent)
	// Hands the runtime a scheduled tick, or reports that nothing is there to take it.
	Fire func(fire contracts.AutomationFire) bool
	// Hands the runtime a tool's question about threads, or reports that nothing is there to answer it.
	Ask               func(request contracts.ThreadRequest) bool
	Running           func() bool
	Workspaces        func() (WorkspaceResolver, error)
	Scheduler         func() (AutomationScheduler, error)
	ComputerUseForRun func(ctx context.Context) domain.ComputerUseRunConfig
	// How the worker itself is started, so what the host does with it can be driven without one.
	Agent           StartAgentProcess
	ResourceJournal string
	// Nil means NewSessionResourceMonitor.
	ResourceMonitor func(options ResourceMonitorOptions) *SessionResourceMonitor
}

// What the host calls on its side of the agent worker.
type RunBridge interface {
	DispatchAutomation(ctx context.Context, automation *domain.Automation, tick domain.TickKind) domain.AutomationRunStatus
	// A command from outside, read defensively before anything acts on it.
	HandleRunCommand(payload any)
	// A command the runtime built itself.
	Submit(command contracts.RunCommand)
	AcknowledgeAutomation(runID string, started bool)
	AnswerThread(response contracts.ThreadResponse)
	KillAgent()
	ClearPendingStarts()
}

type automationDispatch struct {
	acknowledge chan bool
	settle      chan domain.AutomationRunStatus
}

const (
	automationAckTimeout = 30 * time.Second
	// Shorter than the agent's own wait, so a lost answer still comes back as a tool error.
	threadRelayTimeout = 8 * time.Second
	// A wait answers when the thread it names settles, so the relay outlives the wait itself.
	threadWaitSlack = 5 * time.Second
)

type threadRelay struct {
	taskID string
	timer  *time.Timer
}

// The host's side of one agent worker: the runs on its books, and everything they exchange.
type agentRunHost struct {
	host RunHostConfig

	mu     sync.Mutex
	agent  AgentProcess
	ledger *RunLedger
	// Threads the worker last reported background work for, so its death can take that work off the panel.
	backgroundThreads map[string]bool
	// Session-scoped Codex children, and the subset whose turns a worker death would cut short.
	sessionSubagents    map[string]map[string]bool
	liveSubagents       map[string]map[string]bool
	liveSubagentOrder   []string
	automationDispatchs map[string]*automationDispatch
	threadRequests      map[string]*threadRelay
	settingsReload      bool
	// A role a thread took while a start of its own was still being resolved.
	heldRoles     map[string]contracts.ThreadRoleCommand
	resourceStops map[string]string
	resources     *SessionResourceMonitor
}

func StartRunHost(host RunHostConfig) RunBridge {
	runs := &agentRunHost{
		host:                host,
		ledger:              NewRunLedger(),
		backgroundThreads:   map[string]bool{},
		sessionSubagents:    map[string]map[string]bool{},
		liveSubagents:       map[string]map[string]bool{},
		automationDispatchs: map[string]*automationDispatch{},
		threadRequests:      map[string]*threadRelay{},
		heldRoles:           map[string]contracts.ThreadRoleCommand{},
		resourceStops:       map[string]string{},
	}
	monitor := host.ResourceMonitor
	if monitor == nil {
		monitor = NewSessionResourceMonitor
	}
	runs.resources = monitor(ResourceMonitorOptions{
		Journal: host.ResourceJournal,
		Release: func(taskID, sessionID string) {
			runs.post(contracts.TerminateSessionCommand{TaskID: taskID, SessionID: sessionID, IdleOnly: new(true)})
		},
		Stop: runs.stopForMemory,
		Changed: func(taskID string, stop domain.ResourceStop) {
			runs.mu.Lock()
			defer runs.mu.Unlock()
			if strings.HasSuffix(runs.resourceStops[taskID], fmt.Sprintf(":%s", formatJSNumber(stop.At))) {
				runs.send(contracts.ResourceStoppedEvent{TaskID: taskID, Stop: stop})
			}
		},
	})
	return runs
}

func (runs *agentRunHost) post(message AgentMessage) {
	runs.mu.Lock()
	agent := runs.agent
	runs.mu.Unlock()
	if agent != nil {
		agent.Post(message)
	}
}

func (runs *agentRunHost) stopForMemory(taskID, sessionID string, stop domain.ResourceStop) {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	runs.resourceStops[taskID] = fmt.Sprintf("%s:%s", sessionID, formatJSNumber(stop.At))
	runs.send(contracts.ResourceStoppedEvent{TaskID: taskID, Stop: stop})
	for _, event := range runs.ledger.FailuresFor("Stopped due to high memory usage.") {
		if status, ok := event.(contracts.RunEventRunStatus); ok && status.TaskID == taskID {
			status.Status = domain.RunStatusCancelled
			runs.publishRunLocked(status)
		}
	}
	runs.ledger.CancelStarts(taskID)
	for id, pending := range runs.threadRequests {
		if pending.taskID != taskID {
			continue
		}
		pending.timer.Stop()
		delete(runs.threadRequests, id)
		runs.postLocked(contracts.ThreadResponseFalse{RequestID: id, Message: "This session was stopped for high memory usage."})
	}
	runs.postLocked(contracts.TerminateSessionCommand{TaskID: taskID, SessionID: sessionID})
}

func (runs *agentRunHost) postLocked(message AgentMessage) {
	if runs.agent != nil {
		runs.agent.Post(message)
	}
}

func (runs *agentRunHost) send(event contracts.AgentEvent) {
	runs.host.Publish(event)
}

func (runs *agentRunHost) publishRunLocked(event contracts.RunEvent) {
	if !runs.ledger.Accept(event) {
		return
	}
	runs.send(event)
	if status, ok := event.(contracts.RunEventRunStatus); ok && IsTerminalStatus(status.Status) {
		if dispatch := runs.automationDispatchs[status.RunID]; dispatch != nil {
			select {
			case dispatch.settle <- domain.AutomationRunStatus(status.Status):
			default:
			}
		}
	}
}

// Kept apart from the run gate: what the set says outlives whichever run started the work.
func (runs *agentRunHost) publishBackgroundLocked(event contracts.BackgroundEvent) {
	if len(event.Processes) > 0 {
		runs.backgroundThreads[event.TaskID] = true
	} else {
		delete(runs.backgroundThreads, event.TaskID)
	}
	runs.send(event)
}

func idsFor(index map[string]map[string]bool, taskID string) map[string]bool {
	ids := index[taskID]
	if ids == nil {
		ids = map[string]bool{}
		index[taskID] = ids
	}
	return ids
}

// Kept outside the run gate because a Codex child thread may work between parent turns.
func (runs *agentRunHost) publishSubagentLocked(event contracts.AgentEvent) {
	switch event := event.(type) {
	case contracts.SubagentEventSubagentStarted:
		if event.SessionScoped != nil && *event.SessionScoped {
			idsFor(runs.sessionSubagents, event.TaskID)[event.ID] = true
			runs.markLive(event.TaskID, event.ID)
		}
	case contracts.SubagentEventSubagentStatus:
		if runs.sessionSubagents[event.TaskID][event.ID] {
			if event.Status == domain.SubagentLiveStatusWorking {
				runs.markLive(event.TaskID, event.ID)
			} else {
				delete(runs.liveSubagents[event.TaskID], event.ID)
			}
		}
	case contracts.SubagentEventSubagentFinished:
		delete(runs.liveSubagents[event.TaskID], event.ID)
	}
	runs.send(event)
}

func (runs *agentRunHost) markLive(taskID, id string) {
	if runs.liveSubagents[taskID] == nil {
		runs.liveSubagentOrder = append(runs.liveSubagentOrder, taskID)
	}
	idsFor(runs.liveSubagents, taskID)[id] = true
}

// Hands the tick to the runtime, which owns the transcript, then waits for that run to settle.
func (runs *agentRunHost) DispatchAutomation(ctx context.Context, automation *domain.Automation, tick domain.TickKind) domain.AutomationRunStatus {
	runID := application.NewID()
	// Armed before the tick leaves the host so a run that settles immediately still reports back.
	dispatch := &automationDispatch{acknowledge: make(chan bool, 1), settle: make(chan domain.AutomationRunStatus, 1)}
	runs.mu.Lock()
	runs.automationDispatchs[runID] = dispatch
	runs.mu.Unlock()
	defer func() {
		runs.mu.Lock()
		delete(runs.automationDispatchs, runID)
		runs.mu.Unlock()
	}()
	fire := AutomationFireFor(automation, runID, tick)
	if !runs.host.Fire(fire) {
		return domain.AutomationRunStatusSkipped
	}
	ack := time.NewTimer(automationAckTimeout)
	defer ack.Stop()
	var started bool
	select {
	case started = <-dispatch.acknowledge:
	case <-ack.C:
	case <-ctx.Done():
	}
	if !started {
		return domain.AutomationRunStatusSkipped
	}
	return SettledWithin(dispatch.settle, AutomationSettleTimeout)
}

func (runs *agentRunHost) AcknowledgeAutomation(runID string, started bool) {
	runs.mu.Lock()
	dispatch := runs.automationDispatchs[runID]
	runs.mu.Unlock()
	if dispatch != nil {
		select {
		case dispatch.acknowledge <- started:
		default:
		}
	}
}

func automationDraftOf(taskID string, draft contracts.AutomationRequestSaveDraft) domain.AutomationDraft {
	result := domain.AutomationDraft{TaskID: taskID, Prompt: draft.Prompt, Schedule: draft.Schedule, Timezone: draft.Timezone, SurfaceWhen: draft.SurfaceWhen, EndsWhen: draft.EndsWhen, Paused: draft.Paused}
	if draft.Policy != nil {
		policy := domain.ExecutionPolicy(*draft.Policy)
		result.Policy = &policy
	}
	return result
}

func (runs *agentRunHost) answerAutomation(request contracts.AutomationRequest) {
	var requestID, taskID, runID string
	switch request := request.(type) {
	case contracts.AutomationRequestRead:
		requestID, taskID = request.RequestID, request.TaskID
	case contracts.AutomationRequestList:
		requestID, taskID = request.RequestID, request.TaskID
	case contracts.AutomationRequestSave:
		requestID, taskID, runID = request.RequestID, request.TaskID, request.RunID
	case contracts.AutomationRequestUpdate:
		requestID, taskID, runID = request.RequestID, request.TaskID, request.RunID
	case contracts.AutomationRequestDelete:
		requestID, taskID = request.RequestID, request.TaskID
	default:
		return
	}
	authority := func() (domain.ExecutionPolicy, error) {
		runs.mu.Lock()
		defer runs.mu.Unlock()
		session, ok := runs.ledger.Session(taskID)
		state := runs.ledger.State(taskID, runID)
		if runID == "" || state == nil || state.Terminal || !ok || session.RunID != runID {
			return "", errors.New("This run is no longer authorized to change the task's automation.")
		}
		return session.Policy, nil
	}
	ctx := context.Background()
	result, err := func() (any, error) {
		scheduler, err := runs.host.Scheduler()
		if err != nil {
			return nil, err
		}
		switch request := request.(type) {
		case contracts.AutomationRequestRead:
			return scheduler.ForThread(taskID), nil
		case contracts.AutomationRequestList:
			return scheduler.List(), nil
		case contracts.AutomationRequestSave:
			return scheduler.Save(ctx, automationDraftOf(taskID, request.Draft), authority)
		case contracts.AutomationRequestUpdate:
			return scheduler.Update(ctx, taskID, request.Patch, authority)
		default:
			return scheduler.Remove(ctx, taskID)
		}
	}()
	var response contracts.AutomationResponse
	if err != nil {
		response = contracts.AutomationResponseFalse{RequestID: requestID, Message: err.Error()}
	} else if encoded, encodeErr := jsonx.Marshal(result); encodeErr != nil {
		response = contracts.AutomationResponseFalse{RequestID: requestID, Message: encodeErr.Error()}
	} else {
		response = contracts.AutomationResponseTrue{RequestID: requestID, Result: encoded}
	}
	runs.post(response)
}

func threadRequestFields(request contracts.ThreadRequest) (requestID, taskID, op string) {
	encoded, err := jsonx.Marshal(request)
	if err != nil {
		return "", "", ""
	}
	var header struct {
		RequestID string `json:"requestId"`
		TaskID    string `json:"taskId"`
		Op        string `json:"op"`
	}
	_ = jsonx.Unmarshal(encoded, &header)
	return header.RequestID, header.TaskID, header.Op
}

// How long the host waits on the runtime's answer to a thread request.
func relayPatience(request contracts.ThreadRequest) time.Duration {
	remote := contracts.RemoteThreadReadTimeoutMs * time.Millisecond
	switch request := request.(type) {
	case contracts.ThreadRequestWait:
		return milliseconds(request.TimeoutMs) + threadWaitSlack
	case contracts.ThreadRequestBrowser:
		switch read := request.Read.(type) {
		case contracts.BrowserReadSnapshot:
			return milliseconds(read.TimeoutMs) + threadWaitSlack
		case contracts.BrowserReadScreenshot:
			return milliseconds(read.TimeoutMs) + threadWaitSlack
		case contracts.BrowserReadWait:
			return milliseconds(read.TimeoutMs) + threadWaitSlack
		}
	case contracts.ThreadRequestRead:
		if request.Computer == nil || *request.Computer != "this" {
			return remote
		}
	case contracts.ThreadRequestList:
		if request.Computer != nil && *request.Computer != "" && *request.Computer != "this" {
			return remote
		}
	case contracts.ThreadRequestCommand:
		// A named thread may live on a paired computer, which only the workspace knows.
		kind := CommandType(request.Command)
		if !strings.HasPrefix(kind, "browser.") && kind != "worktree.delete" {
			return remote
		}
	}
	return threadRelayTimeout
}

// The runtime owns workspace state, so thread requests are relayed to it rather than answered here.
func (runs *agentRunHost) relayThreadLocked(request contracts.ThreadRequest) {
	requestID, taskID, op := threadRequestFields(request)
	patience := relayPatience(request)
	relay := &threadRelay{taskID: taskID}
	relay.timer = time.AfterFunc(patience, func() {
		runs.mu.Lock()
		defer runs.mu.Unlock()
		if runs.threadRequests[requestID] != relay {
			return
		}
		delete(runs.threadRequests, requestID)
		runs.postLocked(contracts.ThreadResponseFalse{RequestID: requestID, Message: fmt.Sprintf("AI Coding Tool did not answer the thread \"%s\" request within %dms.", op, patience.Milliseconds())})
	})
	runs.threadRequests[requestID] = relay
	if runs.host.Ask(request) {
		return
	}
	relay.timer.Stop()
	delete(runs.threadRequests, requestID)
	runs.postLocked(contracts.ThreadResponseFalse{RequestID: requestID, Message: "AI Coding Tool is not running its workspace."})
}

func (runs *agentRunHost) AnswerThread(response contracts.ThreadResponse) {
	var requestID string
	switch response := response.(type) {
	case contracts.ThreadResponseTrue:
		requestID = response.RequestID
	case contracts.ThreadResponseFalse:
		requestID = response.RequestID
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	pending := runs.threadRequests[requestID]
	if pending == nil {
		return
	}
	pending.timer.Stop()
	delete(runs.threadRequests, requestID)
	runs.postLocked(response)
}

func (runs *agentRunHost) emitSyntheticTerminalLocked(command *contracts.StartRunCommand, status domain.RunStatus, message string) {
	state := runs.ledger.Reopen(command.TaskID, command.RunID)
	if state.LastSequence == 0 {
		runs.publishRunLocked(contracts.RunEventRunStarted{RunEventBase: contracts.RunEventBase{TaskID: command.TaskID, RunID: command.RunID, Sequence: 1}})
	}
	text := message
	runs.publishRunLocked(contracts.RunEventRunStatus{RunEventBase: contracts.RunEventBase{TaskID: command.TaskID, RunID: command.RunID, Sequence: state.LastSequence + 1}, Status: status, Message: &text})
}

func (runs *agentRunHost) connectLocked() {
	if runs.agent != nil {
		return
	}
	holder := &struct{ agent AgentProcess }{}
	holder.agent = runs.host.Agent(AgentProcessListener{
		Message: func(message any) { runs.receive(message) },
		// Exit waits on the lock this connect holds, so the holder is filled by then.
		Exit: func(code *int) { runs.disconnect(holder, code) },
	})
	runs.agent = holder.agent
}

func (runs *agentRunHost) receive(message any) {
	if resource, ok := message.(contracts.SessionResourceMessage); ok {
		runs.resources.Receive(resource)
		return
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if taskID, ok := messageTaskID(message, len(runs.resourceStops) > 0); ok {
		if _, stopped := runs.resourceStops[taskID]; stopped {
			if refusal := unreadable(message); refusal != nil {
				runs.postLocked(refusal)
			}
			return
		}
	}
	switch message := message.(type) {
	case contracts.AgentSettingsReloadEvent:
		runs.settingsReload = message.Status == contracts.AgentSettingsReloadEventStatusPending
		runs.send(message)
	case contracts.RunEvent:
		runs.publishRunLocked(message)
	// No run to gate them: workflows, background work, and child agents can all outlive a parent turn.
	case contracts.WorkflowEvent:
		runs.send(message)
	case contracts.BackgroundEvent:
		runs.publishBackgroundLocked(message)
	case contracts.SubagentEvent:
		runs.publishSubagentLocked(message)
	case contracts.GoalEvent:
		runs.send(message)
	case contracts.AutomationRequest:
		go runs.answerAutomation(message)
	case contracts.ThreadRequest:
		runs.relayThreadLocked(message)
	default:
		// A request no guard could read is answered rather than dropped: a dropped one hangs the tool call.
		if refusal := unreadable(message); refusal != nil {
			runs.postLocked(refusal)
		}
	}
}

// The refusal contracts.UnreadableRequest writes for a request, read as the message the worker takes.
func unreadable(message any) AgentMessage {
	refusal := contracts.UnreadableRequest(asJSONValue(message))
	if refusal == nil {
		return nil
	}
	return refusal
}

func asJSONValue(message any) any {
	encoded, err := jsonx.Marshal(message)
	if err != nil {
		return message
	}
	var decoded any
	if jsonx.Unmarshal(encoded, &decoded) != nil {
		return message
	}
	return decoded
}

func messageTaskID(message any, wanted bool) (string, bool) {
	if !wanted {
		return "", false
	}
	value, ok := asJSONValue(message).(map[string]any)
	if !ok {
		return "", false
	}
	taskID, present := value["taskId"]
	if !present {
		return "", false
	}
	if text, ok := taskID.(string); ok {
		return text, true
	}
	return fmt.Sprint(taskID), true
}

func (runs *agentRunHost) disconnect(holder *struct{ agent AgentProcess }, code *int) {
	runs.resources.ReleaseAll()
	runs.mu.Lock()
	defer runs.mu.Unlock()
	if runs.agent != holder.agent {
		return
	}
	runs.agent = nil
	runs.ledger.ForgetSessions()
	// The sessions went with the worker, so no role is left to retire one for.
	runs.heldRoles = map[string]contracts.ThreadRoleCommand{}
	if runs.settingsReload {
		runs.settingsReload = false
		message := "Agent process exited while reloading settings. Try again."
		runs.send(contracts.AgentSettingsReloadEvent{Status: contracts.AgentSettingsReloadEventStatusFailed, Message: &message})
	}
	if runs.host.Running() {
		runs.ledger.ClearStarts()
		message := "Agent process exited."
		if code != nil {
			message = fmt.Sprintf("Agent process exited with code %d.", *code)
		}
		for _, event := range runs.ledger.FailuresFor(message) {
			runs.publishRunLocked(event)
		}
		// The processes died with it, and no session is left to say so.
		for taskID := range runs.backgroundThreads {
			runs.send(contracts.BackgroundEvent{TaskID: taskID, BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{}}})
		}
		runs.backgroundThreads = map[string]bool{}
		for _, taskID := range runs.liveSubagentOrder {
			for id := range runs.liveSubagents[taskID] {
				runs.send(contracts.SubagentEventSubagentFinished{TaskID: taskID, ID: id, Status: domain.SubagentTerminalStatusStopped, Summary: "The agent process stopped before this subagent finished."})
			}
		}
	}
	runs.sessionSubagents = map[string]map[string]bool{}
	runs.liveSubagents = map[string]map[string]bool{}
	runs.liveSubagentOrder = nil
}

func (runs *agentRunHost) KillAgent() {
	runs.resources.ReleaseAll()
	runs.mu.Lock()
	agent := runs.agent
	runs.mu.Unlock()
	if agent != nil {
		agent.Kill()
	}
}

func (runs *agentRunHost) ClearPendingStarts() {
	runs.mu.Lock()
	defer runs.mu.Unlock()
	runs.ledger.ClearStarts()
}

func (runs *agentRunHost) resolveStart(ctx context.Context, command *contracts.StartRunCommand) (contracts.InternalStartRunCommand, error) {
	type rootAnswer struct {
		resolution workspace.WorkspaceResolution
		err        error
	}
	resolved := make(chan rootAnswer, 1)
	go func() {
		workspaces, err := runs.host.Workspaces()
		if err != nil {
			resolved <- rootAnswer{err: err}
			return
		}
		resolution, err := workspaces.Resolve(command.WorkspaceID)
		resolved <- rootAnswer{resolution, err}
	}()
	computerUse := make(chan domain.ComputerUseRunConfig, 1)
	go func() {
		// Off in settings never reaches the driver, so no permission is asked for and no host is started.
		// Nor does a coordinator's run: it changes nothing itself, in another app no more than in a file.
		switch {
		case command.ComputerUseTools != nil && !*command.ComputerUseTools:
			computerUse <- domain.ComputerUseRunConfigUnavailable{Message: "Computer use is turned off in Settings."}
		case command.CoordinationRole != nil && *command.CoordinationRole == domain.CoordinationRoleCoordinator:
			computerUse <- domain.ComputerUseRunConfigUnavailable{Message: "A coordinator changes nothing itself."}
		default:
			computerUse <- runs.host.ComputerUseForRun(ctx)
		}
	}()
	projectRoot := make(chan *string, 1)
	go func() {
		if command.ProjectWorkspaceID == nil {
			projectRoot <- nil
			return
		}
		projectRoot <- runs.projectRoot(*command.ProjectWorkspaceID)
	}()
	answer, config, project := <-resolved, <-computerUse, <-projectRoot
	if answer.err != nil {
		return contracts.InternalStartRunCommand{}, answer.err
	}
	if answer.resolution.Status != workspace.ResolutionAvailable {
		return contracts.InternalStartRunCommand{}, fmt.Errorf("Workspace is unavailable (%s).", answer.resolution.Reason)
	}
	internal := contracts.InternalStartRunCommand{
		StartRunCommand: *command,
		WorkspaceRoot:   answer.resolution.Workspace.Root,
		Projectless:     answer.resolution.Workspace.Kind == workspace.WorkspaceProjectless,
		ComputerUse:     config,
	}
	if project != nil && *project != answer.resolution.Workspace.Root {
		internal.ProjectRoot = project
	}
	return internal, nil
}

// A project's own checkout, while it is one; a checkout that is gone has nothing left to keep a run out of.
func (runs *agentRunHost) projectRoot(workspaceID string) *string {
	workspaces, err := runs.host.Workspaces()
	if err != nil {
		return nil
	}
	resolution, err := workspaces.Resolve(workspaceID)
	if err != nil || resolution.Status != workspace.ResolutionAvailable || resolution.Workspace.Kind != workspace.WorkspaceProject {
		return nil
	}
	root := resolution.Workspace.Root
	return &root
}

func (runs *agentRunHost) postCommandLocked(command contracts.RunCommand) {
	_, reload := command.(contracts.ReloadAgentSettingsCommand)
	if reload {
		runs.settingsReload = true
	}
	err := func() (err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = panicError(recovered)
			}
		}()
		runs.connectLocked()
		if runs.agent == nil {
			return errors.New("Agent process is unavailable.")
		}
		runs.agent.Post(command)
		return nil
	}()
	if err == nil {
		return
	}
	if reload {
		runs.settingsReload = false
		message := err.Error()
		runs.send(contracts.AgentSettingsReloadEvent{Status: contracts.AgentSettingsReloadEventStatusFailed, Message: &message})
		return
	}
	// A stop, a label, an archive or a role belongs to no run, so a failure to send it has no run to report against.
	var taskID, runID string
	switch command := command.(type) {
	case contracts.CancelRunCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.AnswerQuestionCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.ApprovalDecisionCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.SteerRunCommand:
		taskID, runID = command.TaskID, command.RunID
	default:
		return
	}
	state := runs.ledger.State(taskID, runID)
	if state == nil || state.Terminal {
		return
	}
	base := contracts.RunEventBase{TaskID: taskID, RunID: runID, Sequence: state.LastSequence + 1}
	// A stop that cannot reach the worker still leaves the run stopped, as the user asked.
	if _, cancel := command.(contracts.CancelRunCommand); cancel {
		runs.publishRunLocked(contracts.RunEventRunStatus{RunEventBase: base, Status: domain.RunStatusCancelled})
		return
	}
	message := err.Error()
	runs.publishRunLocked(contracts.RunEventRunStatus{RunEventBase: base, Status: domain.RunStatusFailed, Message: &message})
}

// Resolves a start already on the books against its workspace, then sends it on unless it was dropped meanwhile.
func (runs *agentRunHost) dispatchStart(command contracts.StartRunCommand, pending *contracts.StartRunCommand) {
	ctx := context.Background()
	err := runs.resources.BeforeStart(ctx, command.TaskID)
	var internal contracts.InternalStartRunCommand
	if err == nil {
		// The ledger's copy carries any title a rename gave the thread while this resolved.
		internal, err = runs.resolveStart(ctx, pending)
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	defer runs.sayRoleLocked(command.TaskID, nil)
	if err != nil {
		runs.ledger.TakeStart(command.TaskID, command.RunID)
		runs.emitSyntheticTerminalLocked(&command, domain.RunStatusFailed, err.Error())
		return
	}
	taken := runs.ledger.TakeStart(command.TaskID, command.RunID)
	if taken == nil {
		return
	}
	internal.Title = taken.Title
	runs.connectLocked()
	runs.postLocked(internal)
}

// Says a thread's role once no start of its own is still being resolved. Without a worker there is
// no session to retire.
func (runs *agentRunHost) sayRoleLocked(taskID string, role *contracts.ThreadRoleCommand) {
	if role == nil {
		held, ok := runs.heldRoles[taskID]
		if !ok {
			return
		}
		role = &held
	}
	if runs.ledger.StartsFor(taskID) {
		runs.heldRoles[taskID] = *role
		return
	}
	delete(runs.heldRoles, taskID)
	if runs.agent != nil {
		runs.postCommandLocked(*role)
	}
}

func (runs *agentRunHost) HandleRunCommand(payload any) {
	var raw []byte
	switch payload := payload.(type) {
	case []byte:
		raw = payload
	case jsontext.Value:
		raw = payload
	default:
		encoded, err := jsonx.Marshal(payload)
		if err != nil {
			return
		}
		raw = encoded
	}
	var decoded any
	if jsonx.Unmarshal(raw, &decoded) != nil || !contracts.IsRunCommand(decoded) {
		return
	}
	var command contracts.RunCommand
	if jsonx.Unmarshal(raw, &command) != nil || command == nil {
		return
	}
	runs.Submit(command)
}

func (runs *agentRunHost) Submit(payload contracts.RunCommand) {
	if !runs.host.Running() {
		return
	}
	switch command := payload.(type) {
	case contracts.RunCommandMemoryProtection:
		runs.resources.SetEnabled(command.Enabled)
		return
	case contracts.StartRunCommand:
		if command.MemoryProtection != nil {
			runs.resources.SetEnabled(*command.MemoryProtection)
		}
		runs.mu.Lock()
		delete(runs.resourceStops, command.TaskID)
		if runs.ledger.Knows(command.TaskID, command.RunID) {
			runs.mu.Unlock()
			return
		}
		for _, superseded := range runs.ledger.SupersedeStarts(command.TaskID, command.RunID) {
			runs.emitSyntheticTerminalLocked(superseded, domain.RunStatusCancelled, "The run was superseded before it started.")
		}
		runs.ledger.BeginStart(command)
		pending := runs.ledger.PendingStart(command.TaskID, command.RunID)
		runs.mu.Unlock()
		go runs.dispatchStart(command, pending)
		return
	}
	runs.mu.Lock()
	defer runs.mu.Unlock()
	switch command := payload.(type) {
	case contracts.ReloadAgentSettingsCommand:
		if runs.agent == nil {
			runs.send(contracts.AgentSettingsReloadEvent{Status: contracts.AgentSettingsReloadEventStatusReloaded})
			return
		}
		runs.postCommandLocked(command)
		return
	// A stop or a label names the thread's session, which outlives its runs, so no run has to be live.
	case contracts.LabelThreadCommand:
		runs.ledger.RetitleStarts(command.TaskID, command.Title)
		runs.postCommandLocked(command)
		return
	case contracts.StopProcessCommand, contracts.ArchiveThreadCommand:
		runs.postCommandLocked(command)
		return
	case contracts.ThreadRoleCommand:
		runs.sayRoleLocked(command.TaskID, &command)
		return
	}
	var taskID, runID string
	switch command := payload.(type) {
	case contracts.CancelRunCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.AnswerQuestionCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.ApprovalDecisionCommand:
		taskID, runID = command.TaskID, command.RunID
	case contracts.SteerRunCommand:
		taskID, runID = command.TaskID, command.RunID
	default:
		return
	}
	if pending := runs.ledger.PendingStart(taskID, runID); pending != nil {
		if _, cancel := payload.(contracts.CancelRunCommand); cancel {
			runs.ledger.TakeStart(taskID, runID)
			runs.emitSyntheticTerminalLocked(pending, domain.RunStatusCancelled, "The run was cancelled before it started.")
			return
		}
	}
	state := runs.ledger.State(taskID, runID)
	if state == nil || state.Terminal {
		return
	}
	runs.postCommandLocked(payload)
}
