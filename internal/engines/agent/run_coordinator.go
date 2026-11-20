package agent

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type pendingApproval struct {
	settled bool
	decided chan ToolDecision
	intent  domain.ToolIntent
	// Only an unattended run has one: the deadline after which the question is answered for it.
	deadline *time.Timer
}

type pendingQuestions struct {
	request domain.QuestionRequest
	answers domain.QuestionAnswers
	close   func(answers domain.QuestionAnswers, answered bool)
}

type pendingTail struct {
	messageID string
	text      string
}

type activeRun struct {
	taskID        string
	runID         string
	workspaceRoot string
	projectless   bool
	ctx           context.Context
	cancel        context.CancelFunc
	steering      *SteerChannel
	sequence      float64
	terminal      bool
	// Started by the scheduler with nobody present, until somebody steers into it.
	unattended    bool
	approvals     map[string]*pendingApproval
	approvalOrder []string
	questions     map[string]*pendingQuestions
	questionOrder []string
	// Newest streamed tail, held back until the throttle window opens.
	pendingTail *pendingTail
	tailTimer   *time.Timer
}

type CoordinatorOptions struct {
	IsWritePathInside func(ctx context.Context, root, candidate string) bool
	Automations       func(taskID string, currentRunID func() (string, error)) AutomationBridge
	Findings          func(taskID string) FindingBridge
	Threads           func(taskID string) ThreadBridge
	Coordination      func(taskID string) CoordinationBridge
	Browser           func(taskID string) BrowserBridge
	Simulator         func(taskID string) SimulatorBridge
	Terminal          func(taskID string) TerminalBridge
	// Zero means DefaultTailInterval.
	TailInterval time.Duration
	// Zero means DefaultUnattendedApproval.
	UnattendedApproval time.Duration
}

// Tails arrive per token; this is often enough to read as typing without flooding the renderer.
const DefaultTailInterval = 40 * time.Millisecond

// Long enough that a person who is around still decides; short enough that the next tick is not lost.
const DefaultUnattendedApproval = 10 * time.Minute

const unattendedDenial = "Nobody is watching this scheduled run, so this action was denied rather than left waiting. Take a route that needs no approval, or stop and report what you could not do."

// Turns run commands into provider runs and provider events into run events, one live run per thread.
// emit is called with the coordinator's lock held, in sequence order, and must not call back into it.
type RunCoordinator struct {
	provider AgentProvider
	emit     func(event contracts.AgentEvent)
	options  CoordinatorOptions

	mu   sync.Mutex
	runs map[string]*activeRun
}

func NewRunCoordinator(provider AgentProvider, emit func(event contracts.AgentEvent), options CoordinatorOptions) *RunCoordinator {
	if options.TailInterval <= 0 {
		options.TailInterval = DefaultTailInterval
	}
	if options.UnattendedApproval <= 0 {
		options.UnattendedApproval = DefaultUnattendedApproval
	}
	return &RunCoordinator{provider: provider, emit: emit, options: options, runs: map[string]*activeRun{}}
}

func newActiveRun(taskID, runID, workspaceRoot string, projectless, unattended bool) *activeRun {
	ctx, cancel := context.WithCancel(context.Background())
	return &activeRun{
		taskID: taskID, runID: runID, workspaceRoot: workspaceRoot, projectless: projectless,
		ctx: ctx, cancel: cancel, steering: NewSteerChannel(), unattended: unattended,
		approvals: map[string]*pendingApproval{}, questions: map[string]*pendingQuestions{},
	}
}

func (coordinator *RunCoordinator) Start(command contracts.InternalStartRunCommand) {
	coordinator.mu.Lock()
	if previous := coordinator.runs[command.TaskID]; previous != nil {
		coordinator.cancelActiveLocked(previous)
	}
	active := newActiveRun(command.TaskID, command.RunID, command.WorkspaceRoot, command.Projectless, command.Unattended != nil && *command.Unattended)
	coordinator.runs[command.TaskID] = active
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventRunStarted{RunEventBase: base}
	})
	coordinator.publishStatusLocked(active, domain.RunStatusRunning, "", nil)
	coordinator.mu.Unlock()
	go coordinator.execute(active, command)
}

// Only the run the message was queued against can take it; anything else leaves it for the next run.
func (coordinator *RunCoordinator) Steer(taskID, runID, messageID, prompt string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	active := coordinator.runs[taskID]
	if active == nil || active.runID != runID || active.terminal {
		return false
	}
	// Somebody is here after all, so the run's questions go back to waiting for them.
	active.unattended = false
	return active.steering.Push(SteerMessage{MessageID: messageID, Prompt: prompt})
}

func (coordinator *RunCoordinator) Cancel(taskID, runID string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	active := coordinator.runs[taskID]
	if active == nil || active.runID != runID {
		return false
	}
	coordinator.cancelActiveLocked(active)
	return true
}

// Kills one background process of the thread's session. It is addressed to the session rather than
// to a run, because a workflow outlives the run that started it and still has to be stoppable.
func (coordinator *RunCoordinator) StopProcess(taskID, processID string) bool {
	return coordinator.provider.StopProcess(taskID, processID)
}

// The thread runs as something else now, so a session built for what it was, and holding its tools, goes.
func (coordinator *RunCoordinator) RetireOutOfRole(taskID string, role *domain.CoordinationRole) {
	coordinator.provider.RetireOutOfRole(taskID, role)
}

func (coordinator *RunCoordinator) LabelThread(taskID, title string) bool {
	return coordinator.provider.LabelThread(taskID, title)
}

func (coordinator *RunCoordinator) ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool {
	return coordinator.provider.ArchiveThread(taskID, continuation, archived)
}

func (coordinator *RunCoordinator) DecideApproval(taskID, runID, approvalID string, allow bool) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	active := coordinator.runs[taskID]
	if active == nil || active.runID != runID || active.terminal {
		return false
	}
	pending := active.approvals[approvalID]
	if pending == nil {
		return false
	}
	// Answering one is somebody being here, so the run's later questions wait for them too.
	active.unattended = false
	decision := Deny
	if allow {
		decision = Allow
	}
	if !coordinator.settleApprovalLocked(active, approvalID, pending, decision) {
		return false
	}
	coordinator.showApprovalsLocked(active)
	return true
}

func (coordinator *RunCoordinator) execute(active *activeRun, command contracts.InternalStartRunCommand) {
	input := coordinator.runInput(active, command)
	result, err := coordinator.safeExecute(input)
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.isCurrentLocked(active) || active.terminal {
		return
	}
	if err != nil {
		coordinator.finishLocked(active, domain.RunStatusFailed, err.Error(), nil)
		return
	}
	coordinator.finishLocked(active, result.Status, result.Message, result.Limit)
}

// A provider that panics fails its run rather than the app.
func (coordinator *RunCoordinator) safeExecute(input *ProviderRunInput) (result ProviderResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered)
		}
	}()
	return coordinator.provider.Execute(input)
}

func (coordinator *RunCoordinator) runInput(active *activeRun, command contracts.InternalStartRunCommand) *ProviderRunInput {
	taskID := command.TaskID
	input := &ProviderRunInput{
		Channel:       command.Channel,
		TaskID:        taskID,
		Title:         command.Title,
		Prompt:        command.Prompt,
		WorkspaceRoot: active.workspaceRoot,
		Projectless:   active.projectless,
		ComputerUse:   command.ComputerUse,
		Policy:        command.Policy,
		Engine:        command.Engine,
		Model:         command.Model,
		Effort:        command.Effort,
		Operation:     command.Operation,
		ChromeBrowser: command.ChromeBrowser != nil && *command.ChromeBrowser,
		Continuation:  command.Continuation,
		Steering:      active.steering,
		Signal:        active.ctx,
		Abort:         active.cancel,
	}
	if command.ProjectRoot != nil && *command.ProjectRoot != "" {
		input.ProjectRoot = command.ProjectRoot
	}
	if domain.CapabilitiesFor(string(command.Engine)).FastMode {
		fast := command.FastMode != nil && *command.FastMode
		input.FastMode = &fast
	}
	input.ForkContinuation = command.ForkContinuation != nil && *command.ForkContinuation
	if coordinator.options.Automations != nil {
		input.Automations = coordinator.options.Automations(taskID, func() (string, error) {
			coordinator.mu.Lock()
			defer coordinator.mu.Unlock()
			current := coordinator.runs[taskID]
			if current == nil || current.terminal || current.ctx.Err() != nil {
				return "", errors.New("This task has no active run to change its automation.")
			}
			return current.runID, nil
		})
	}
	if coordinator.options.Findings != nil {
		input.Findings = coordinator.options.Findings(taskID)
	}
	if coordinator.options.Threads != nil {
		input.Threads = coordinator.options.Threads(taskID)
	}
	if command.CoordinationRole != nil {
		input.CoordinationRole = command.CoordinationRole
		if coordinator.options.Coordination != nil {
			input.Coordination = coordinator.options.Coordination(taskID)
		}
	}
	if (command.BrowserTools == nil || *command.BrowserTools) && coordinator.options.Browser != nil {
		input.Browser = coordinator.options.Browser(taskID)
	}
	if (command.SimulatorTools == nil || *command.SimulatorTools) && coordinator.options.Simulator != nil {
		input.Simulator = coordinator.options.Simulator(taskID)
	}
	if coordinator.options.Terminal != nil {
		input.Terminal = coordinator.options.Terminal(taskID)
	}
	input.Authorize = func(ctx context.Context, intent domain.ToolIntent) ToolDecision {
		return coordinator.authorize(ctx, active, intent)
	}
	input.AskQuestion = func(ctx context.Context, request domain.QuestionRequest) (domain.QuestionAnswers, bool) {
		return coordinator.askQuestion(ctx, active, request)
	}
	input.Emit = func(event ProviderEvent) { coordinator.handleProviderEvent(active, event) }
	input.ReportWorkflow = func(report contracts.WorkflowReport) { coordinator.reportWorkflow(taskID, report) }
	input.ReportBackground = func(report contracts.BackgroundReport) { coordinator.reportBackground(taskID, report) }
	input.ReportSubagent = func(report domain.SubagentReport) { coordinator.reportSubagent(taskID, report) }
	input.ReportGoal = func(report contracts.GoalReport) { coordinator.reportGoal(taskID, report) }
	input.BeginAgentTurn = func() AgentTurn { return coordinator.beginAgentTurn(active) }
	return input
}

func (coordinator *RunCoordinator) handleProviderEvent(active *activeRun, event ProviderEvent) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if !coordinator.isCurrentLocked(active) || active.terminal {
		return
	}
	publish := func(build func(base contracts.RunEventBase) contracts.RunEvent) {
		coordinator.publishLocked(active, build)
	}
	switch event := event.(type) {
	case EventSubagent:
		coordinator.emit(subagentEvent(active.taskID, event.Report))
	case EventAssistant:
		// The committed block already contains whatever a waiting tail was holding.
		if !event.Artifact {
			active.pendingTail = nil
		}
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			delta := contracts.RunEventAssistantDelta{RunEventBase: base, MessageID: event.MessageID, Text: event.Text}
			if event.Append {
				delta.Append = new(true)
			}
			if event.Artifact {
				delta.Artifact = new(true)
			}
			return delta
		})
	case EventAssistantTail:
		coordinator.queueTailLocked(active, event.MessageID, event.Text)
	case EventUsage:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContextUsage{RunEventBase: base, Tokens: event.Tokens, Limit: event.Limit, Model: event.Model}
		})
	case EventSpend:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContextSpent{RunEventBase: base, Tokens: event.Tokens}
		})
	case EventRetry:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventRunRetrying{RunEventBase: base, RetryNotice: event.RetryNotice}
		})
	case EventCompactionStatus:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContextCompactionStatus{RunEventBase: base, Compacting: event.Compacting, Error: event.Error}
		})
	case EventCompaction:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContextCompacted{RunEventBase: base, Trigger: event.Trigger, PreTokens: event.PreTokens, PostTokens: event.PostTokens}
		})
	case EventTool:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventToolIntent{RunEventBase: base, Intent: event.Intent}
		})
	case EventComputerUseSetupRequired:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventComputerUseSetupRequired{RunEventBase: base}
		})
	case EventContinuation:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContinuationUpdated{RunEventBase: base, Continuation: event.Continuation}
		})
	case EventContinuationLost:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventContinuationLost{RunEventBase: base}
		})
	case EventSteered:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventQueuedDelivered{RunEventBase: base, MessageID: event.MessageID}
		})
	case EventSteerFailed:
		publish(func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventQueuedSteerFailed{RunEventBase: base, MessageID: event.MessageID, Message: event.Message}
		})
	}
}

// A workflow answers to the thread rather than to a run, so nothing about a run's state holds it back.
func (coordinator *RunCoordinator) reportWorkflow(taskID string, report contracts.WorkflowReport) {
	var event contracts.AgentEvent
	switch report := report.(type) {
	case contracts.WorkflowReportWorkflowStarted:
		event = contracts.WorkflowEventWorkflowStarted{ID: report.ID, Name: report.Name, Description: report.Description, TaskID: taskID}
	case contracts.WorkflowReportWorkflowProgress:
		event = contracts.WorkflowEventWorkflowProgress{ID: report.ID, Phases: report.Phases, Agents: report.Agents, TotalTokens: report.TotalTokens, TotalToolCalls: report.TotalToolCalls, TaskID: taskID}
	case contracts.WorkflowReportWorkflowFinished:
		event = contracts.WorkflowEventWorkflowFinished{ID: report.ID, Status: contracts.WorkflowEventWorkflowFinishedStatus(report.Status), Summary: report.Summary, TaskID: taskID}
	default:
		return
	}
	coordinator.emitLocked(event)
}

// A shell or a monitor outlives the run that started it, so its set answers to the thread too.
func (coordinator *RunCoordinator) reportBackground(taskID string, report contracts.BackgroundReport) {
	coordinator.emitLocked(contracts.BackgroundEvent{BackgroundReport: report, TaskID: taskID})
}

// A subagent answers to the thread even when the run that launched it is no longer current.
func (coordinator *RunCoordinator) reportSubagent(taskID string, report domain.SubagentReport) {
	if event := subagentEvent(taskID, report); event != nil {
		coordinator.emitLocked(event)
	}
}

func (coordinator *RunCoordinator) reportGoal(taskID string, report contracts.GoalReport) {
	coordinator.emitLocked(contracts.GoalEvent{GoalReport: report, TaskID: taskID})
}

// Emits under the lock so thread events stay in order with the run events around them.
func (coordinator *RunCoordinator) emitLocked(event contracts.AgentEvent) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.emit(event)
}

func subagentEvent(taskID string, report domain.SubagentReport) contracts.AgentEvent {
	switch report := report.(type) {
	case domain.SubagentReportSubagentStarted:
		return contracts.SubagentEventSubagentStarted{ID: report.ID, Description: report.Description, AgentType: report.AgentType, SessionScoped: report.SessionScoped, Model: report.Model, Effort: report.Effort, Prompt: report.Prompt, TaskID: taskID}
	case domain.SubagentReportSubagentMetadata:
		return contracts.SubagentEventSubagentMetadata{ID: report.ID, Model: report.Model, Effort: report.Effort, Prompt: report.Prompt, TaskID: taskID}
	case domain.SubagentReportSubagentStatus:
		return contracts.SubagentEventSubagentStatus{ID: report.ID, Status: report.Status, Summary: report.Summary, TaskID: taskID}
	case domain.SubagentReportSubagentProgress:
		return contracts.SubagentEventSubagentProgress{ID: report.ID, Description: report.Description, AgentType: report.AgentType, LastToolName: report.LastToolName, Summary: report.Summary, TotalTokens: report.TotalTokens, TaskID: taskID}
	case domain.SubagentReportSubagentActivity:
		return contracts.SubagentEventSubagentActivity{ID: report.ID, ActivityID: report.ActivityID, Kind: contracts.SubagentEventSubagentActivityKind(report.Kind), Title: report.Title, Text: report.Text, TaskID: taskID}
	case domain.SubagentReportSubagentFinished:
		return contracts.SubagentEventSubagentFinished{ID: report.ID, Status: report.Status, Summary: report.Summary, TaskID: taskID}
	}
	return nil
}

// Gives a turn the agent started itself somewhere to land. It is a run like any other, so what the
// agent says is read where the thread's other turns are, and what it asks for is approved there too.
func (coordinator *RunCoordinator) beginAgentTurn(seed *activeRun) AgentTurn {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.runs[seed.taskID] != nil {
		return nil
	}
	active := newActiveRun(seed.taskID, application.NewID(), seed.workspaceRoot, seed.projectless, false)
	coordinator.runs[active.taskID] = active
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventRunStarted{RunEventBase: base, AgentInitiated: new(true)}
	})
	coordinator.publishStatusLocked(active, domain.RunStatusRunning, "", nil)
	return &agentTurn{coordinator: coordinator, active: active}
}

type agentTurn struct {
	coordinator *RunCoordinator
	active      *activeRun
}

func (turn *agentTurn) Emit(event ProviderEvent) {
	turn.coordinator.handleProviderEvent(turn.active, event)
}

func (turn *agentTurn) Authorize(ctx context.Context, intent domain.ToolIntent) ToolDecision {
	return turn.coordinator.authorize(ctx, turn.active, intent)
}

func (turn *agentTurn) Steering() SteerQueue { return turn.active.steering }

func (turn *agentTurn) End(result ProviderResult) {
	turn.coordinator.mu.Lock()
	defer turn.coordinator.mu.Unlock()
	turn.coordinator.finishLocked(turn.active, result.Status, result.Message, result.Limit)
}

func (coordinator *RunCoordinator) queueTailLocked(active *activeRun, messageID, text string) {
	active.pendingTail = &pendingTail{messageID: messageID, text: text}
	if active.tailTimer != nil {
		return
	}
	coordinator.flushTailLocked(active)
	var timer *time.Timer
	timer = time.AfterFunc(coordinator.options.TailInterval, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if active.tailTimer != timer {
			return
		}
		active.tailTimer = nil
		if pending := active.pendingTail; pending != nil {
			coordinator.queueTailLocked(active, pending.messageID, pending.text)
		}
	})
	active.tailTimer = timer
}

func (coordinator *RunCoordinator) flushTailLocked(active *activeRun) {
	pending := active.pendingTail
	if pending == nil || active.terminal {
		return
	}
	active.pendingTail = nil
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventAssistantTail{RunEventBase: base, MessageID: pending.messageID, Text: pending.text}
	})
}

func (coordinator *RunCoordinator) AnswerQuestion(taskID, runID, requestID, questionID, text string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	active := coordinator.runs[taskID]
	if active == nil || active.runID != runID || active.terminal || jsTrim(text) == "" {
		return false
	}
	pending := active.questions[requestID]
	if pending == nil {
		return false
	}
	if _, answered := pending.answers[questionID]; answered || !slices.ContainsFunc(pending.request.Questions, func(question *domain.AgentQuestion) bool { return question.ID == questionID }) {
		return false
	}
	active.unattended = false
	pending.answers[questionID] = text
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventQuestionAnswered{RunEventBase: base, RequestID: requestID, QuestionID: questionID, Text: text}
	})
	for _, question := range pending.request.Questions {
		if _, answered := pending.answers[question.ID]; !answered {
			return true
		}
	}
	pending.close(pending.answers, true)
	return true
}

func validQuestionRequest(request domain.QuestionRequest) bool {
	encoded, err := jsonx.Marshal(request)
	if err != nil {
		return false
	}
	var decoded any
	if jsonx.Unmarshal(encoded, &decoded) != nil {
		return false
	}
	return domain.IsQuestionRequest(decoded)
}

func (coordinator *RunCoordinator) askQuestion(ctx context.Context, active *activeRun, request domain.QuestionRequest) (domain.QuestionAnswers, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	if !coordinator.isCurrentLocked(active) || active.terminal || ctx.Err() != nil || !validQuestionRequest(request) {
		coordinator.mu.Unlock()
		return nil, false
	}
	requestID := application.NewID()
	type outcome struct {
		answers  domain.QuestionAnswers
		answered bool
	}
	result := make(chan outcome, 1)
	var (
		deadline  *time.Timer
		stopAbort func() bool
	)
	pending := &pendingQuestions{request: request, answers: domain.QuestionAnswers{}}
	pending.close = func(answers domain.QuestionAnswers, answered bool) {
		if active.questions[requestID] == nil {
			return
		}
		delete(active.questions, requestID)
		active.questionOrder = slices.DeleteFunc(active.questionOrder, func(id string) bool { return id == requestID })
		if deadline != nil {
			deadline.Stop()
		}
		if stopAbort != nil {
			stopAbort()
		}
		coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
			return contracts.RunEventQuestionClosed{RunEventBase: base, RequestID: requestID}
		})
		result <- outcome{answers, answered}
	}
	active.questions[requestID] = pending
	active.questionOrder = append(active.questionOrder, requestID)
	stopAbort = context.AfterFunc(ctx, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		pending.close(nil, false)
	})
	if active.unattended {
		deadline = time.AfterFunc(coordinator.options.UnattendedApproval, func() {
			coordinator.mu.Lock()
			defer coordinator.mu.Unlock()
			if active.unattended {
				pending.close(nil, false)
			}
		})
	}
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventQuestionRequested{RunEventBase: base, RequestID: requestID, Request: request}
	})
	coordinator.mu.Unlock()
	answer := <-result
	return answer.answers, answer.answered
}

func (coordinator *RunCoordinator) authorize(ctx context.Context, active *activeRun, intent domain.ToolIntent) ToolDecision {
	if ctx == nil {
		ctx = context.Background()
	}
	coordinator.mu.Lock()
	if !coordinator.isCurrentLocked(active) || active.terminal || active.ctx.Err() != nil {
		coordinator.mu.Unlock()
		return Deny
	}
	coordinator.mu.Unlock()
	if intent.WritePath != nil && coordinator.options.IsWritePathInside != nil {
		if !coordinator.options.IsWritePathInside(ctx, active.workspaceRoot, *intent.WritePath) {
			return Deny
		}
	}
	coordinator.mu.Lock()
	if !coordinator.isCurrentLocked(active) || active.terminal || active.ctx.Err() != nil {
		coordinator.mu.Unlock()
		return Deny
	}
	approvalID := application.NewID()
	pending := &pendingApproval{decided: make(chan ToolDecision, 1), intent: intent}
	active.approvals[approvalID] = pending
	active.approvalOrder = append(active.approvalOrder, approvalID)
	// Only a run the scheduler started answers for itself, and only while it stays unattended: a
	// question left standing would hold the thread busy and cost the automation every later tick.
	if active.unattended {
		pending.deadline = time.AfterFunc(coordinator.options.UnattendedApproval, func() {
			coordinator.mu.Lock()
			defer coordinator.mu.Unlock()
			if !active.unattended || !coordinator.settleApprovalLocked(active, approvalID, pending, DenyWith(unattendedDenial)) {
				return
			}
			coordinator.showApprovalsLocked(active)
		})
	}
	stopRunAbort := context.AfterFunc(active.ctx, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		coordinator.settleApprovalLocked(active, approvalID, pending, Deny)
	})
	// The caller giving up withdraws the question.
	stopCallerAbort := context.AfterFunc(ctx, func() {
		coordinator.mu.Lock()
		defer coordinator.mu.Unlock()
		if coordinator.settleApprovalLocked(active, approvalID, pending, Deny) {
			coordinator.showApprovalsLocked(active)
		}
	})
	if len(active.approvals) == 1 {
		coordinator.showApprovalsLocked(active)
	}
	coordinator.mu.Unlock()
	decision := <-pending.decided
	stopRunAbort()
	stopCallerAbort()
	return decision
}

// A run shows one approval at a time, oldest first, and stays waiting until none is left; the rest
// are announced as each one before them is answered.
func (coordinator *RunCoordinator) showApprovalsLocked(active *activeRun) {
	if active.terminal || !coordinator.isCurrentLocked(active) {
		return
	}
	if len(active.approvalOrder) == 0 {
		coordinator.publishStatusLocked(active, domain.RunStatusRunning, "", nil)
		return
	}
	approvalID := active.approvalOrder[0]
	intent := active.approvals[approvalID].intent
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		return contracts.RunEventApprovalRequested{
			RunEventBase: base,
			ApprovalID:   approvalID,
			Intent:       intent,
			Title:        domain.ToolLabel(intent.Name) + " needs approval",
			Description:  "Review this action before it runs.",
		}
	})
	coordinator.publishStatusLocked(active, domain.RunStatusAwaitingApproval, "", nil)
}

func (coordinator *RunCoordinator) cancelActiveLocked(active *activeRun) {
	if !active.terminal {
		coordinator.finishLocked(active, domain.RunStatusCancelled, "", nil)
	}
	active.cancel()
}

func (coordinator *RunCoordinator) finishLocked(active *activeRun, status domain.RunStatus, message string, limit *domain.UsageLimit) {
	if active.terminal {
		return
	}
	active.terminal = true
	if active.tailTimer != nil {
		active.tailTimer.Stop()
		active.tailTimer = nil
	}
	active.pendingTail = nil
	active.steering.Close()
	coordinator.expireApprovalsLocked(active)
	for _, requestID := range slices.Clone(active.questionOrder) {
		if pending := active.questions[requestID]; pending != nil {
			pending.close(nil, false)
		}
	}
	if coordinator.isCurrentLocked(active) {
		if status != domain.RunStatusFailed {
			limit = nil
		}
		coordinator.publishStatusLocked(active, status, message, limit)
		delete(coordinator.runs, active.taskID)
	}
}

// Answers one pending approval exactly once, and stops any deadline it was under.
func (coordinator *RunCoordinator) settleApprovalLocked(active *activeRun, approvalID string, pending *pendingApproval, decision ToolDecision) bool {
	if pending.settled {
		return false
	}
	pending.settled = true
	if pending.deadline != nil {
		pending.deadline.Stop()
	}
	delete(active.approvals, approvalID)
	active.approvalOrder = slices.DeleteFunc(active.approvalOrder, func(id string) bool { return id == approvalID })
	pending.decided <- decision
	return true
}

func (coordinator *RunCoordinator) expireApprovalsLocked(active *activeRun) {
	for _, approvalID := range slices.Clone(active.approvalOrder) {
		if pending := active.approvals[approvalID]; pending != nil {
			coordinator.settleApprovalLocked(active, approvalID, pending, Deny)
		}
	}
}

func (coordinator *RunCoordinator) isCurrentLocked(active *activeRun) bool {
	return coordinator.runs[active.taskID] == active
}

func (coordinator *RunCoordinator) publishStatusLocked(active *activeRun, status domain.RunStatus, message string, limit *domain.UsageLimit) {
	coordinator.publishLocked(active, func(base contracts.RunEventBase) contracts.RunEvent {
		event := contracts.RunEventRunStatus{RunEventBase: base, Status: status, Limit: limit}
		if message != "" {
			event.Message = &message
		}
		return event
	})
}

func (coordinator *RunCoordinator) publishLocked(active *activeRun, build func(base contracts.RunEventBase) contracts.RunEvent) {
	if !coordinator.isCurrentLocked(active) {
		return
	}
	active.sequence++
	coordinator.emit(build(contracts.RunEventBase{TaskID: active.taskID, RunID: active.runID, Sequence: active.sequence}))
}

type panicked struct{ value any }

func (p panicked) Error() string {
	if err, ok := p.value.(error); ok {
		return err.Error()
	}
	if text, ok := p.value.(string); ok {
		return text
	}
	return strings.TrimSpace("panic: " + jsonString(p.value))
}

func panicError(value any) error { return panicked{value} }

func jsonString(value any) string {
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		return "unknown"
	}
	return string(encoded)
}
