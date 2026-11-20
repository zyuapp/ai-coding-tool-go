// Package agent runs agent sessions: the engine-neutral run coordinator, the warm session pool, and
// the contract every engine (claude, codex, opencode) implements. Ports src/main/agent/*.
package agent

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type SteerMessage struct {
	MessageID string
	Prompt    string
}

// Messages the user pushed into a run that had already started.
type SteerQueue interface {
	// Blocks until a message arrives; false once the run is over or ctx ends.
	Next(ctx context.Context) (SteerMessage, bool)
}

// What an engine reports while a turn runs. Members are the Event* structs below.
type ProviderEvent interface {
	isProviderEvent()
}

type EventAssistant struct {
	MessageID string
	Text      string
	Append    bool
	Artifact  bool
}

// The buffered remainder that has not formed a complete block yet, so the UI can type it out.
type EventAssistantTail struct {
	MessageID string
	Text      string
}

// Model is the id the engine reported on the wire, not an AgentModel.
type EventUsage struct {
	Tokens float64
	Limit  float64
	Model  string
}

// Tokens a model response spent, input and output, beyond what an earlier event already counted for it.
type EventSpend struct {
	Tokens float64
}

type EventCompactionStatus struct {
	Compacting bool
	Error      *string
}

type EventRetry struct {
	domain.RetryNotice
}

type EventCompaction struct {
	Trigger    contracts.RunEventContextCompactedTrigger
	PreTokens  float64
	PostTokens *float64
}

type EventTool struct {
	Intent domain.ToolIntent
}

type EventComputerUseSetupRequired struct{}

type EventContinuation struct {
	Continuation domain.Continuation
}

// The continuation the run was given resumes nothing any more, so the thread has to start over.
type EventContinuationLost struct{}

type EventSteered struct {
	MessageID string
}

type EventSteerFailed struct {
	MessageID string
	Message   string
}

// A subagent update; routed to the thread rather than the run.
type EventSubagent struct {
	Report domain.SubagentReport
}

func (EventAssistant) isProviderEvent()                {}
func (EventAssistantTail) isProviderEvent()            {}
func (EventUsage) isProviderEvent()                    {}
func (EventSpend) isProviderEvent()                    {}
func (EventCompactionStatus) isProviderEvent()         {}
func (EventRetry) isProviderEvent()                    {}
func (EventCompaction) isProviderEvent()               {}
func (EventTool) isProviderEvent()                     {}
func (EventComputerUseSetupRequired) isProviderEvent() {}
func (EventContinuation) isProviderEvent()             {}
func (EventContinuationLost) isProviderEvent()         {}
func (EventSteered) isProviderEvent()                  {}
func (EventSteerFailed) isProviderEvent()              {}
func (EventSubagent) isProviderEvent()                 {}

// A turn the agent started itself, after the run that seeded the session had ended.
type AgentTurn interface {
	Emit(event ProviderEvent)
	Authorize(ctx context.Context, intent domain.ToolIntent) ToolDecision
	// The user can steer into it like any run, and what they send has to be taken.
	Steering() SteerQueue
	End(result ProviderResult)
}

// "allow", a plain "deny" (read as the user's), or a denial carrying wording for a run with no user.
type ToolDecision struct {
	Allowed bool
	// Set only on a worded denial.
	Reason string
}

var (
	Allow = ToolDecision{Allowed: true}
	Deny  = ToolDecision{}
)

func DenyWith(reason string) ToolDecision { return ToolDecision{Reason: reason} }

// What the agent is told of a denial, which every engine words the same.
func DenialReason(decision ToolDecision) string {
	if decision.Reason != "" {
		return decision.Reason
	}
	return "The user denied this action."
}

// What a run hands an engine. Shared by pointer and never mutated after Execute is called.
type ProviderRunInput struct {
	// Answers false when the request expired; ending ctx withdraws its unanswered questions.
	AskQuestion func(ctx context.Context, request domain.QuestionRequest) (domain.QuestionAnswers, bool)
	Channel     contracts.RunChannel
	// Which thread is asking, which is what a warm session belongs to.
	TaskID        string
	Title         string
	Prompt        string
	WorkspaceRoot string
	// Set for a member working in a checkout of its own: the project's checkout.
	ProjectRoot *string
	Projectless bool
	ComputerUse domain.ComputerUseRunConfig
	Policy      domain.ExecutionPolicy
	Engine      domain.AgentEngine
	Model       domain.AgentModel
	Effort      domain.AgentEffort
	// Read only by an engine whose capabilities include fast mode. Nil means standard speed.
	FastMode  *bool
	Operation contracts.RunOperation
	// Read only by an engine whose capabilities include driving Chrome.
	ChromeBrowser    bool
	Continuation     *domain.Continuation
	ForkContinuation bool
	Automations      AutomationBridge
	// Only alongside Automations.
	Findings FindingBridge
	Threads  ThreadBridge
	// The part the run plays beside a coordinator, and the tools that come with it.
	CoordinationRole *domain.CoordinationRole
	Coordination     CoordinationBridge
	Browser          BrowserBridge
	Simulator        SimulatorBridge
	Terminal         TerminalBridge
	Steering         SteerQueue
	// Done once the run is cancelled (the TS abort signal).
	Signal context.Context
	// Cancels the run (the TS abortController.abort).
	Abort     context.CancelFunc
	Authorize func(ctx context.Context, intent domain.ToolIntent) ToolDecision
	Emit      func(event ProviderEvent)
	// Kept apart from Emit: a workflow reports to the thread, and outlasts the run that started it.
	ReportWorkflow func(report contracts.WorkflowReport)
	// A shell or a monitor outlives the run that started it.
	ReportBackground func(report contracts.BackgroundReport)
	// A child agent belongs to the thread and may keep reporting after the parent run settles.
	ReportSubagent func(report domain.SubagentReport)
	// A native goal belongs to the thread and can change between model turns.
	ReportGoal func(report contracts.GoalReport)
	// Opens a run for a turn nobody asked for. Nil when the thread already has a run of its own.
	BeginAgentTurn func() AgentTurn
}

// Whether the run was cancelled.
func (input *ProviderRunInput) Aborted() bool {
	return input.Signal != nil && input.Signal.Err() != nil
}

type ProviderResult struct {
	// succeeded, failed or cancelled.
	Status domain.RunStatus
	// Empty means none.
	Message string
	// Set on a failure caused by the account reaching its plan's usage limit.
	Limit *domain.UsageLimit
}

// The handle a continuation holds for engine; another engine's continuation means nothing to it.
func HandleFor(continuation *domain.Continuation, engine domain.AgentEngine) (string, bool) {
	if continuation == nil || continuation.Provider != string(engine) {
		return "", false
	}
	return continuation.Value, true
}

// The run's engine's own handle on the thread.
func ContinuationOf(input *ProviderRunInput) (string, bool) {
	return HandleFor(input.Continuation, input.Engine)
}

type AgentProvider interface {
	// Runs a turn. An error is reported as the run failing with its message.
	Execute(input *ProviderRunInput) (ProviderResult, error)
	// Kills one background process of the thread's session, whether or not a run is going.
	StopProcess(taskID, processID string) bool
	// Offers the thread's title to the engine's own record of it. Engines that keep none say so.
	LabelThread(taskID, title string) bool
	// Files the thread away in the engine's own record of it, or brings it back.
	ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool
	// Lets the thread's session go, once its turn has answered, when it was built for a coordination
	// role the thread no longer has.
	RetireOutOfRole(taskID string, role *domain.CoordinationRole)
}
