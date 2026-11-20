package claude

import (
	"context"
	"encoding/json/jsontext"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	processStderrLimit = 8192
	subagentLimit      = 1_000
)

var processExited = regexp.MustCompile(`Claude Code process (?:exited|terminated)`)

// stream is everything one turn builds up as it streams, and where what it produces goes.
type stream struct {
	emit func(event agent.ProviderEvent)
	// What the turn is answering with, which is what its context meter is measured against.
	model            *domain.AgentModel
	streamedText     map[string]*agent.MarkdownBuffer
	activeMainStream string
}

func openStream(emit func(event agent.ProviderEvent), model *domain.AgentModel) *stream {
	return &stream{emit: emit, model: model, streamedText: map[string]*agent.MarkdownBuffer{}}
}

// turn is one turn a run asked for: the stream that answers it. steered says the run is owed a turn
// past the one going: steered messages end the turn they interrupt and are answered together in the
// one after it, so however many arrive, they add one turn to wait for.
type turn struct {
	*agent.SessionTurn
	steered bool
	stream  *stream
}

// agentTurn is the turn the agent started itself, once it has a run to report into.
type agentTurn struct {
	turn    agent.AgentTurn
	steered bool
	stream  *stream
}

type openResponse struct {
	id    string
	usage responseUsage
}

// SessionOpener starts the session's process with its input, tool gate and hooks.
type SessionOpener func(canUseTool protocol.CanUseTool, hooks []protocol.HookEvent) (Query, error)

// orderedSet is a set that remembers the order its members came in.
type orderedSet struct {
	order []string
	has   map[string]bool
}

func (set *orderedSet) add(key string) {
	if set.has == nil {
		set.has = map[string]bool{}
	}
	if !set.has[key] {
		set.has[key] = true
		set.order = append(set.order, key)
	}
}

func (set *orderedSet) remove(key string) {
	if !set.has[key] {
		return
	}
	delete(set.has, key)
	for index, member := range set.order {
		if member == key {
			set.order = append(set.order[:index:index], set.order[index+1:]...)
			return
		}
	}
}

func (set *orderedSet) oldest() string { return set.order[0] }

func (set *orderedSet) clear() { *set = orderedSet{} }

// Session is one live Claude session, kept across turns. The agent process, its MCP servers, and
// everything it left running in the background belong to the session, so a second turn neither pays
// for a new process nor replays the transcript to resume it. Each turn takes the session in turn: the
// input stays open between them, and the turn ends on its own result.
//
// State is guarded by mu. Calls out of the session (events, reports, the pool's hooks) are queued
// while it is held and made once it is released, so none of them runs under the lock.
type Session struct {
	key   string
	hooks agent.SessionHooks

	mu  sync.Mutex
	out []func()

	sideChatBoundaryPending bool
	processStderr           string
	query                   Query
	turns                   *agent.TurnSlot[*turn]
	pumpOnce                sync.Once
	ended                   bool
	// How the stream ended, kept for a run that arrives after the session is already over.
	outcome *agent.ProviderResult
	// The turn the agent started itself. Only ever one at a time.
	agentTurn *agentTurn
	// Opens that run. Taken when the session opens: what the agent says between runs is the thread's.
	beginAgentTurn func() agent.AgentTurn
	// The workflows running here, with what each has reported spending. One outlives the turn that
	// started it, so the totals outlive the turn too.
	workflows     agent.RunningTotals
	workflowOrder orderedSet
	// Where those workflows report. They belong to the thread, not to a run.
	reportWorkflow func(report contracts.WorkflowReport)
	// Where subagents report. One launched in the background outlives the turn that launched it.
	reportSubagent func(report domain.SubagentReport)
	// The subagents still running here, by task id, and by the tool call that launched each.
	subagentIDs       orderedSet
	subagentByToolUse map[string]string
	// Launch input and child replies can arrive before task_started connects their tool id to a task.
	subagentMetadata      map[string]domain.SubagentMetadata
	subagentMetadataOrder orderedSet
	subagentEfforts       map[string]string
	subagentEffortOrder   orderedSet
	reportGoal            func(report contracts.GoalReport)
	hasGoal               bool
	// The newest plan limit report; the agent process sends one whenever it changes.
	rateLimit *protocol.RateLimitInfo
	// What each model response this turn has been counted for so far, by its API message id.
	spentByID agent.RunningTotals
	// The response each stream has open and what it read, the main loop's under "" and a subagent's
	// under the call that launched it.
	openResponses map[string]openResponse
	// What the agent process left running here: a shell, a monitor, or a task of its own.
	background *agent.BackgroundWork
	// What the agent process called this session. A later run resumes it by this id.
	sessionID string
	model     domain.AgentModel
	// The effort flag the session is running under, which is nil for a model that takes none.
	effort *domain.ClaudeEffort
	// Ends the session's own waits once it closes.
	ctx    context.Context
	cancel context.CancelFunc
}

// NewSession is a session the pool holds under key. Open starts it.
func NewSession(key string, hooks agent.SessionHooks) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		key: key, hooks: hooks, ctx: ctx, cancel: cancel,
		workflows: agent.RunningTotals{}, spentByID: agent.RunningTotals{},
		subagentByToolUse: map[string]string{}, subagentMetadata: map[string]domain.SubagentMetadata{},
		subagentEfforts: map[string]string{}, openResponses: map[string]openResponse{},
		reportWorkflow: func(contracts.WorkflowReport) {}, reportSubagent: func(domain.SubagentReport) {},
		reportGoal: func(contracts.GoalReport) {}, beginAgentTurn: func() agent.AgentTurn { return nil },
	}
	s.turns = agent.NewTurnSlot[*turn](agent.InterruptGrace, s.abandon)
	s.background = agent.NewBackgroundWork(s.Busy, func() {
		if s.hooks.Rested != nil {
			s.hooks.Rested()
		}
	})
	return s
}

func (s *Session) lock() { s.mu.Lock() }

// unlock releases the session, then makes the calls queued while it was held, in order.
func (s *Session) unlock() {
	calls := s.out
	s.out = nil
	s.mu.Unlock()
	for _, call := range calls {
		call()
	}
}

// later queues a call out of the session for when it is released.
func (s *Session) later(call func()) { s.out = append(s.out, call) }

func (s *Session) emit(to *stream, event agent.ProviderEvent) {
	emit := to.emit
	s.later(func() { emit(event) })
}

func (s *Session) reportSubagentLater(report domain.SubagentReport) {
	reportSubagent := s.reportSubagent
	s.later(func() { reportSubagent(report) })
}

func (s *Session) reportWorkflowLater(report contracts.WorkflowReport) {
	reportWorkflow := s.reportWorkflow
	s.later(func() { reportWorkflow(report) })
}

func (s *Session) reportGoalLater(goal *domain.ActiveGoal) {
	reportGoal := s.reportGoal
	s.later(func() { reportGoal(contracts.GoalReport{Goal: goal}) })
}

// RecordProcessStderr keeps the tail of what the process wrote to stderr, for a failure to quote.
func (s *Session) RecordProcessStderr(text string) {
	s.lock()
	defer s.unlock()
	tail := s.processStderr + text
	if len(tail) > processStderrLimit {
		tail = tail[len(tail)-processStderrLimit:]
		for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
			tail = tail[1:]
		}
	}
	s.processStderr = tail
}

func (s *Session) Key() string { return s.key }

// Answering says a turn is in flight, so the session owes an answer before it can take another.
func (s *Session) Answering() bool { return s.turns.Answering() }

// Busy says closing the session would cut something short: a turn in flight, or work the agent left
// running behind it.
func (s *Session) Busy() bool {
	s.lock()
	agentTurn := s.agentTurn != nil
	s.mu.Unlock()
	return s.Answering() || agentTurn || s.background.Running()
}

func (s *Session) Live() bool {
	s.lock()
	defer s.mu.Unlock()
	return !s.ended
}

// Continues says whether this session is the one a run means to continue.
func (s *Session) Continues(continuation *string) bool {
	s.lock()
	defer s.mu.Unlock()
	return continuation == nil || *continuation == s.sessionID
}

func (s *Session) currentTurn() *turn {
	current, _ := s.turns.Turn()
	return current
}

// Open starts the session's process for the run that seeded it.
func (s *Session) Open(opener SessionOpener, seed *agent.ProviderRunInput) error {
	s.lock()
	s.sideChatBoundaryPending = agent.NeedsSideChatBoundary(seed)
	s.model = seed.Model
	s.effort = effortFlag(seed.Model, seed.Effort)
	if seed.ReportWorkflow != nil {
		s.reportWorkflow = seed.ReportWorkflow
	}
	if seed.ReportSubagent != nil {
		s.reportSubagent = seed.ReportSubagent
	}
	if seed.ReportGoal != nil {
		s.reportGoal = seed.ReportGoal
	}
	if seed.BeginAgentTurn != nil {
		s.beginAgentTurn = seed.BeginAgentTurn
	}
	reportBackground := seed.ReportBackground
	if reportBackground == nil {
		reportBackground = func(contracts.BackgroundReport) {}
	}
	s.later(func() { s.background.OpenWith(reportBackground) })
	s.reportGoalLater(nil)
	s.hasGoal = false
	s.unlock()

	effort := s.receiveSubagentEffort
	query, err := opener(s.canUseTool, []protocol.HookEvent{
		{Event: "PreToolUse", Matchers: []protocol.HookMatcher{{Hooks: []protocol.HookCallback{effort}}}},
		{Event: "SubagentStop", Matchers: []protocol.HookMatcher{{Hooks: []protocol.HookCallback{effort}}}},
	})
	s.lock()
	if err != nil {
		result := agent.ProviderResult{Status: domain.RunStatusFailed, Message: err.Error()}
		s.outcome = &result
		s.unlock()
		s.close()
		return err
	}
	if s.ended {
		s.unlock()
		query.Close()
		return nil
	}
	s.query = query
	s.unlock()
	return nil
}

// startPump starts reading the session's messages, once a turn is there to take them.
func (s *Session) startPump() {
	s.pumpOnce.Do(func() {
		s.lock()
		query := s.query
		s.mu.Unlock()
		if query != nil {
			go s.pump(query)
		}
	})
}

// Run answers one run's turn on this session.
func (s *Session) Run(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	s.lock()
	if s.ended || s.query == nil {
		result := agent.ProviderResult{Status: domain.RunStatusFailed, Message: "The agent session ended before the run could start."}
		if s.outcome != nil {
			result = *s.outcome
		}
		s.unlock()
		return result, nil
	}
	// The run supersedes whatever the agent had going on its own, so that turn lets go of its own run.
	s.endAgentTurn(agent.ProviderResult{Status: domain.RunStatusCancelled})
	s.unlock()
	result := s.turns.Take(input, turnEngine{s})
	s.startPump()
	return result, nil
}

type turnEngine struct{ s *Session }

func (engine turnEngine) Open(base *agent.SessionTurn) *turn {
	input := base.Input
	model := input.Model
	return &turn{SessionTurn: base, stream: openStream(input.Emit, &model)}
}

func (engine turnEngine) Begin(current *turn) {
	engine.s.startPump()
	go engine.s.begin(current)
}

func (engine turnEngine) Interrupt(*turn) {
	engine.s.lock()
	query := engine.s.query
	engine.s.mu.Unlock()
	if query != nil {
		go func() { _ = query.Interrupt(context.Background()) }()
	}
}

func (s *Session) begin(current *turn) {
	s.lock()
	// A turn the agent opened while this one was being taken lets go of it too.
	if s.currentTurn() == current {
		s.endAgentTurn(agent.ProviderResult{Status: domain.RunStatusCancelled})
	}
	s.unlock()
	s.retune(current.Input)
	s.lock()
	if s.currentTurn() != current {
		s.unlock()
		return
	}
	if _, compact := current.Input.Operation.(contracts.RunOperationCompact); compact {
		s.unlock()
		s.push(userMessage(compactCommand, ""), nil)
		return
	}
	// One message keeps the boundary after inherited history without giving Claude an extra turn.
	prompt := current.Input.Prompt
	if s.sideChatBoundaryPending {
		prompt = agent.SideChatBoundary + "\n\nSide-chat request:\n" + prompt
	}
	s.sideChatBoundaryPending = false
	s.unlock()
	s.push(userMessage(prompt, ""), nil)
	s.drainSteering(current.Input.Steering, current.Input.Emit, func() *bool {
		if s.currentTurn() == current {
			return &current.steered
		}
		return nil
	})
}

// StopProcess kills one task of this session: a shell, a monitor, a workflow, or a subagent.
func (s *Session) StopProcess(processID string) {
	s.lock()
	query := s.query
	s.mu.Unlock()
	if query != nil {
		go func() { _ = query.StopTask(context.Background(), processID) }()
	}
}

// Close ends the session and its process. The channel closes once the process has exited.
func (s *Session) Close() <-chan struct{} {
	s.lock()
	query := s.query
	s.unlock()
	ended := s.closeWith(func(call func()) { go call() })
	if !ended {
		return agent.Settled()
	}
	if exited, ok := query.(interface{ Exited() <-chan struct{} }); ok {
		return exited.Exited()
	}
	return agent.Settled()
}

// abandon gives up a session whose interrupted turn never answered.
func (s *Session) abandon() { s.close() }

func (s *Session) close() { s.closeWith(func(call func()) { call() }) }

// closeWith ends the session, calling the pool's ended hook through call. False when it had already ended.
func (s *Session) closeWith(call func(func())) bool {
	s.lock()
	if s.ended {
		s.unlock()
		return false
	}
	s.ended = true
	s.cancel()
	s.later(s.background.Clear)
	if s.hasGoal {
		s.reportGoalLater(nil)
	}
	s.hasGoal = false
	s.endAgentTurn(agent.ProviderResult{Status: domain.RunStatusCancelled})
	// The session ending is the end of the subagents it holds: their own notification can no longer come.
	for _, id := range s.subagentIDs.order {
		s.reportSubagentLater(domain.SubagentReportSubagentFinished{ID: id, Status: domain.SubagentTerminalStatusStopped, Summary: "The session ended before this subagent finished."})
	}
	s.subagentIDs.clear()
	clear(s.subagentByToolUse)
	clear(s.subagentMetadata)
	s.subagentMetadataOrder.clear()
	clear(s.subagentEfforts)
	s.subagentEffortOrder.clear()
	// The session ending is the end of the workflows it holds: their own notification can no longer come.
	for _, id := range s.workflowOrder.order {
		s.reportWorkflowLater(contracts.WorkflowReportWorkflowFinished{ID: id, Status: contracts.WorkflowReportWorkflowFinishedStatusStopped, Summary: ""})
	}
	clear(s.workflows)
	s.workflowOrder.clear()
	s.turns.Settle(agent.ProviderResult{Status: domain.RunStatusCancelled})
	if query := s.query; query != nil {
		s.later(query.Close)
	}
	s.query = nil
	if ended := s.hooks.Ended; ended != nil {
		s.later(func() { call(ended) })
	}
	s.unlock()
	return true
}

// retune takes a live session's settings as changes rather than as a reason to start over.
func (s *Session) retune(input *agent.ProviderRunInput) {
	s.lock()
	query := s.query
	modelChanged := s.model != input.Model
	s.model = input.Model
	effort := effortFlag(input.Model, input.Effort)
	effortChanged := !sameEffort(s.effort, effort)
	s.effort = effort
	s.mu.Unlock()
	if query == nil {
		return
	}
	ctx := s.ctx
	if modelChanged {
		model := string(input.Model)
		_ = query.SetModel(ctx, &model)
	}
	// A run can leave plan mode itself, so the policy is re-stated every turn rather than only when it changes.
	_ = query.SetPermissionMode(ctx, ClaudePermissionMode(input.Policy))
	if effortChanged {
		_ = query.ApplyFlagSettings(ctx, effortSettings{EffortLevel: effort})
	}
}

type effortSettings struct {
	EffortLevel *domain.ClaudeEffort `json:"effortLevel"`
}

func sameEffort(left, right *domain.ClaudeEffort) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// drainSteering feeds one run's steered messages into the session for as long as that run is the one going.
func (s *Session) drainSteering(steering agent.SteerQueue, emit func(event agent.ProviderEvent), owing func() *bool) {
	if steering == nil {
		return
	}
	for {
		steer, ok := steering.Next(s.ctx)
		if !ok {
			return
		}
		s.lock()
		owed := owing()
		if owed == nil {
			s.unlock()
			return
		}
		*owed = true
		s.unlock()
		messageID := steer.MessageID
		s.push(userMessage(steer.Prompt, protocol.PriorityNow), func() { emit(agent.EventSteered{MessageID: messageID}) })
	}
}

// absorbedSteering says whether this result ends a turn a steered message cut short rather than the
// run. The run's verdict is the last turn's: what it says about work the steering called off is not an answer.
func (s *Session) absorbedSteering() bool {
	var owing *bool
	if current := s.currentTurn(); current != nil {
		owing = &current.steered
	} else if s.agentTurn != nil {
		owing = &s.agentTurn.steered
	}
	if owing == nil || !*owing {
		return false
	}
	*owing = false
	return true
}

// push writes a message to the session's input. A steered message only counts as delivered once the
// session has taken it.
func (s *Session) push(message protocol.UserMessage, delivered func()) {
	s.lock()
	query := s.query
	s.mu.Unlock()
	if query == nil || query.SendUser(message) != nil {
		return
	}
	if delivered != nil {
		delivered()
	}
}

func (s *Session) finish(result agent.ProviderResult) {
	s.outcome = &result
	s.turns.Settle(result)
	s.endAgentTurn(result)
}

// conclude ends whichever turn is in flight: the one a run asked for, or the one the agent started itself.
func (s *Session) conclude(result agent.ProviderResult) {
	if s.currentTurn() != nil {
		s.turns.Settle(result)
	} else {
		s.endAgentTurn(result)
	}
}

func messageParent(message protocol.Message) *string {
	switch message := message.(type) {
	case *protocol.AssistantMessage:
		return message.ParentToolUseID
	case *protocol.StreamEvent:
		return message.ParentToolUseID
	}
	return nil
}

// streamFor is where the message goes. Between runs the agent still speaks — a workflow reports what
// it produced, and work that outlived its run still asks before it acts — so a turn nobody asked for
// is given a run of its own rather than dropped.
func (s *Session) streamFor(message protocol.Message) *stream {
	_, assistant := message.(*protocol.AssistantMessage)
	_, streamEvent := message.(*protocol.StreamEvent)
	// What a subagent says is its own: recognized, it was already reported as activity; unrecognized, it belongs nowhere.
	if (assistant || streamEvent) && messageParent(message) != nil && *messageParent(message) != "" {
		return nil
	}
	if current := s.currentTurn(); current != nil {
		return current.stream
	}
	if s.agentTurn != nil {
		return s.agentTurn.stream
	}
	// A session that has ended opens no turn for a message the agent had already sent.
	if s.ended || !assistant && !streamEvent {
		return nil
	}
	if opened := s.openAgentTurn(); opened != nil {
		return opened.stream
	}
	return nil
}

func (s *Session) openAgentTurn() *agentTurn {
	if s.agentTurn != nil {
		return s.agentTurn
	}
	started := s.beginAgentTurn()
	if started == nil {
		return nil
	}
	model := s.model
	opened := &agentTurn{turn: started, stream: openStream(started.Emit, &model)}
	s.agentTurn = opened
	go s.drainSteering(started.Steering(), started.Emit, func() *bool {
		if s.agentTurn == opened {
			return &opened.steered
		}
		return nil
	})
	return opened
}

func (s *Session) endAgentTurn(result agent.ProviderResult) {
	open := s.agentTurn
	if open == nil {
		return
	}
	s.agentTurn = nil
	s.later(func() { open.turn.End(result) })
}

func (s *Session) pump(query Query) {
	for message := range query.Messages() {
		s.lock()
		s.receive(message)
		s.unlock()
	}
	s.lock()
	if err := query.Err(); err != nil {
		reason := err.Error()
		if stderr := strings.TrimSpace(s.processStderr); stderr != "" && processExited.MatchString(reason) {
			reason += ". stderr: " + stderr
		}
		s.finish(agent.ProviderResult{Status: domain.RunStatusFailed, Message: reason, Limit: usageLimitOf(s.rateLimit)})
	} else {
		s.finish(agent.ProviderResult{Status: domain.RunStatusSucceeded})
	}
	s.unlock()
	s.close()
}

func (s *Session) receive(message protocol.Message) {
	if goal, ok := message.(*protocol.ActiveGoalMessage); ok {
		s.hasGoal = goal.Value != nil
		var reported *domain.ActiveGoal
		if value := goal.Value; value != nil {
			iterations := value.Iterations
			reported = &domain.ActiveGoal{Objective: value.Condition, Status: domain.ActiveGoalStatusActive, Iterations: &iterations}
			if value.LastReason != nil && *value.LastReason != "" {
				reported.LastReason = value.LastReason
			}
		}
		s.reportGoalLater(reported)
		return
	}
	if init, ok := message.(*protocol.SystemInit); ok {
		s.sessionID = init.SessionID
	}
	if limit, ok := message.(*protocol.RateLimitEvent); ok {
		info := limit.RateLimitInfo
		s.rateLimit = &info
		return
	}
	// Taken before the stream guard: what the agent leaves running outlives the turn that started it.
	if changed, ok := message.(*protocol.BackgroundTasksChanged); ok {
		ids := make([]string, 0, len(changed.Tasks))
		for _, task := range changed.Tasks {
			ids = append(ids, task.TaskID)
		}
		processes := backgroundProcesses(changed.Tasks)
		s.later(func() { s.background.Replace(processes, ids) })
	}
	// Taken before subagents claim their messages, since what they spend is the run's too.
	s.receiveSpend(message)
	if s.receiveWorkflow(message) {
		return
	}
	s.receiveSubagentMetadata(message)
	if s.receiveSubagent(message) {
		return
	}
	target := s.streamFor(message)
	if target == nil {
		return
	}
	switch message := message.(type) {
	case *protocol.SystemInit:
		s.emit(target, agent.EventContinuation{Continuation: domain.Continuation{Provider: string(domain.AgentEngineClaude), Value: message.SessionID}})
	case *protocol.CompactBoundary:
		metadata := message.CompactMetadata
		s.emit(target, agent.EventCompaction{Trigger: contracts.RunEventContextCompactedTrigger(metadata.Trigger), PreTokens: metadata.PreTokens, PostTokens: metadata.PostTokens})
	case *protocol.APIRetry:
		attempt, maxRetries := message.Attempt, message.MaxRetries
		s.emit(target, agent.EventRetry{RetryNotice: domain.RetryNotice{Message: retryReason(message), Attempt: &attempt, MaxRetries: &maxRetries}})
	case *protocol.SystemStatus:
		compacting := message.Status != nil && *message.Status == "compacting"
		if !compacting && (message.CompactResult == nil || *message.CompactResult == "") {
			return
		}
		event := agent.EventCompactionStatus{Compacting: compacting}
		if message.CompactResult != nil && *message.CompactResult == "failed" {
			failure := "Context compaction failed."
			if message.CompactError != nil {
				failure = *message.CompactError
			}
			event.Error = &failure
		}
		s.emit(target, event)
	case *protocol.StreamEvent:
		s.receiveStreamEvent(target, message)
	case *protocol.AssistantMessage:
		s.receiveAssistant(target, message)
	case *protocol.ResultMessage:
		// The open input stream keeps the session alive, so the turn's result is what ends the run.
		if s.absorbedSteering() {
			return
		}
		if message.Subtype != protocol.ResultSubtypeSuccess || message.IsError {
			failure := strings.Join(message.Errors, "\n")
			if message.Subtype == protocol.ResultSubtypeSuccess {
				failure = ""
				if message.Result != nil {
					failure = *message.Result
				}
			}
			s.conclude(agent.ProviderResult{Status: domain.RunStatusFailed, Message: failure, Limit: usageLimitOf(s.rateLimit)})
			// A turn that broke leaves a session nobody can vouch for; the next run resumes it instead.
			s.later(s.close)
			return
		}
		s.conclude(agent.ProviderResult{Status: domain.RunStatusSucceeded})
	}
}

func (s *Session) receiveStreamEvent(target *stream, message *protocol.StreamEvent) {
	if deref(message.ParentToolUseID) != "" {
		return
	}
	event := message.Event
	switch {
	case event.Type == "message_start" && event.Message != nil:
		target.activeMainStream = event.Message.ID
		target.streamedText[target.activeMainStream] = agent.OpenMarkdownBuffer()
	case target.activeMainStream != "" && event.Type == "content_block_delta" && event.Delta != nil && event.Delta.Type != nil && *event.Delta.Type == "text_delta":
		buffered := target.streamedText[target.activeMainStream]
		if buffered == nil {
			return
		}
		text := ""
		if event.Delta.Text != nil {
			text = *event.Delta.Text
		}
		id := target.activeMainStream
		if complete := agent.AppendCompleteMarkdown(buffered, text); complete != "" {
			s.emit(target, agent.EventAssistant{MessageID: id, Text: complete, Append: true})
		}
		s.emit(target, agent.EventAssistantTail{MessageID: id, Text: buffered.Text})
	}
}

func (s *Session) receiveAssistant(target *stream, message *protocol.AssistantMessage) {
	streamID := message.Message.ID
	streamed, wasStreamed := target.streamedText[streamID]
	if wasStreamed {
		if streamed.Text != "" {
			s.emit(target, agent.EventAssistant{MessageID: streamID, Text: streamed.Text, Append: true})
		}
		delete(target.streamedText, streamID)
		if target.activeMainStream == streamID {
			target.activeMainStream = ""
		}
	}
	syntheticError := message.Error != nil && *message.Error != "" && message.Message.Model == syntheticModel
	for _, block := range message.Message.Content {
		switch {
		case block.Type == "text":
			// A synthetic error repeats what the run's failure reports.
			if block.Text != nil && !wasStreamed && strings.TrimSpace(*block.Text) != "" && !syntheticError {
				s.emit(target, agent.EventAssistant{MessageID: message.UUID, Text: *block.Text})
			}
		case block.Type == "tool_use":
			s.emit(target, agent.EventTool{Intent: normalizeToolIntent(deref(block.Name), block.Input, deref(block.ID))})
		}
	}
	// A synthetic reply costs no context and counts none, so reporting it would blank the meter.
	if message.Message.Model != syntheticModel && target.model != nil && message.Message.Usage != nil {
		usage := message.Message.Usage
		s.emit(target, agent.EventUsage{
			Tokens: usage.InputTokens + orZero(usage.CacheCreationInputTokens) + orZero(usage.CacheReadInputTokens),
			Limit:  domain.ContextWindowLimit(domain.BuiltInModels, domain.AgentEngineClaude, *target.model),
			Model:  message.Message.Model,
		})
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// receiveSpend counts what a model response spent on the run in flight, whether the main loop or a
// subagent asked for it. The SDK sends one message per content block, each under the response's id
// with its usage so far, and only the stream's closing delta carries all the response wrote, so a
// response adds only what it grew by since it was last seen. Work going on between runs has no run
// to count it, and a synthetic reply spent nothing.
func (s *Session) receiveSpend(message protocol.Message) {
	switch message := message.(type) {
	case *protocol.ResultMessage:
		clear(s.spentByID)
		clear(s.openResponses)
	case *protocol.AssistantMessage:
		if message.Message.Model != syntheticModel && message.Message.Usage != nil {
			s.spend(message, message.ParentToolUseID, message.Message.ID, tokensOf(messageUsage(message.Message.Usage)))
		}
	case *protocol.StreamEvent:
		event := message.Event
		key := deref(message.ParentToolUseID)
		switch {
		case event.Type == "message_start" && event.Message != nil && event.Message.Model != syntheticModel:
			if event.Message.ID != "" && event.Message.Usage != nil {
				s.openResponses[key] = openResponse{id: event.Message.ID, usage: messageUsage(event.Message.Usage)}
			}
		case event.Type == "message_delta":
			response, ok := s.openResponses[key]
			if !ok {
				return
			}
			delete(s.openResponses, key)
			var usage protocol.DeltaUsage
			if event.Usage != nil {
				usage = *event.Usage
			}
			s.spend(message, message.ParentToolUseID, response.id, tokensOf(responseUsage{
				InputTokens:              firstSet(usage.InputTokens, response.usage.InputTokens),
				CacheCreationInputTokens: firstSet(usage.CacheCreationInputTokens, response.usage.CacheCreationInputTokens),
				CacheReadInputTokens:     firstSet(usage.CacheReadInputTokens, response.usage.CacheReadInputTokens),
				OutputTokens:             usage.OutputTokens,
			}))
		}
	}
}

func firstSet(values ...*float64) *float64 {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// spend adds what a response grew to since it was last counted. A subagent's spend is the run's,
// though its messages are its own.
func (s *Session) spend(message protocol.Message, parent *string, responseID string, total float64) {
	var target *stream
	if parent != nil && *parent != "" {
		if current := s.currentTurn(); current != nil {
			target = current.stream
		} else if s.agentTurn != nil {
			target = s.agentTurn.stream
		}
	} else {
		target = s.streamFor(message)
	}
	if target == nil {
		return
	}
	if grown := s.spentByID.Grow(responseID, total, 0); grown != 0 {
		s.emit(target, agent.EventSpend{Tokens: grown})
	}
}

// spendWorkflow counts what a workflow spent on the run in flight. Its agents' replies never reach
// this stream, so the running total its progress reports is all there is to read; each report adds
// what it grew by, and one arriving between runs moves the total without counting.
func (s *Session) spendWorkflow(id string, total float64) {
	grown := s.workflows.Grow(id, total, 0)
	if grown == 0 {
		return
	}
	if current := s.currentTurn(); current != nil {
		s.emit(current.stream, agent.EventSpend{Tokens: grown})
	} else if s.agentTurn != nil {
		s.emit(s.agentTurn.stream, agent.EventSpend{Tokens: grown})
	}
}

// receiveSubagentEffort hears the child's effective effort from hooks, including model downgrades and
// agent-definition overrides.
func (s *Session) receiveSubagentEffort(_ context.Context, input protocol.HookInput, _ *string) (any, error) {
	s.lock()
	defer s.unlock()
	if s.ended || input.AgentID == nil || *input.AgentID == "" || input.Effort == nil || input.Effort.Level == "" {
		return map[string]any{}, nil
	}
	id, effort := *input.AgentID, input.Effort.Level
	if previous, ok := s.subagentEfforts[id]; ok && previous == effort {
		return map[string]any{}, nil
	}
	s.subagentEfforts[id] = effort
	s.subagentEffortOrder.add(id)
	if s.subagentIDs.has[id] {
		s.reportSubagentLater(domain.SubagentReportSubagentMetadata{ID: id, SubagentMetadata: domain.SubagentMetadata{Effort: &effort}})
	}
	if len(s.subagentEfforts) > subagentLimit {
		oldest := s.subagentEffortOrder.oldest()
		delete(s.subagentEfforts, oldest)
		s.subagentEffortOrder.remove(oldest)
	}
	return map[string]any{}, nil
}

func sameText(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// rememberSubagent keeps what a launch or a child reply said about a subagent, reporting it once
// the subagent is known.
func (s *Session) rememberSubagent(toolID string, metadata domain.SubagentMetadata) {
	previous, _ := s.subagentMetadata[toolID]
	unchanged := (metadata.Model == nil || sameText(metadata.Model, previous.Model)) &&
		(metadata.Effort == nil || sameText(metadata.Effort, previous.Effort)) &&
		(metadata.Prompt == nil || sameText(metadata.Prompt, previous.Prompt))
	if unchanged {
		return
	}
	merged := previous
	if metadata.Model != nil {
		merged.Model = metadata.Model
	}
	if metadata.Effort != nil {
		merged.Effort = metadata.Effort
	}
	if metadata.Prompt != nil {
		merged.Prompt = metadata.Prompt
	}
	s.subagentMetadata[toolID] = merged
	s.subagentMetadataOrder.add(toolID)
	if id, ok := s.subagentByToolUse[toolID]; ok {
		s.reportSubagentLater(domain.SubagentReportSubagentMetadata{ID: id, SubagentMetadata: metadata})
	}
	// Calls that fail before task_started have no finish event to release them.
	if len(s.subagentMetadata) > subagentLimit {
		oldest := s.subagentMetadataOrder.oldest()
		delete(s.subagentMetadata, oldest)
		s.subagentMetadataOrder.remove(oldest)
	}
}

// receiveSubagentMetadata reads the child's actual model from its replies; the parent's settings
// cannot account for agent-definition overrides.
func (s *Session) receiveSubagentMetadata(message protocol.Message) {
	switch message := message.(type) {
	case *protocol.AssistantMessage:
		for _, block := range message.Message.Content {
			if block.Type != "tool_use" || block.Name == nil || (*block.Name != "Agent" && *block.Name != "Task") {
				continue
			}
			var input struct {
				Prompt any `json:"prompt"`
			}
			if len(block.Input) > 0 && jsonx.Unmarshal(block.Input, &input) == nil {
				if prompt, ok := input.Prompt.(string); ok {
					s.rememberSubagent(deref(block.ID), domain.SubagentMetadata{Prompt: &prompt})
				}
			}
		}
		if parent := deref(message.ParentToolUseID); parent != "" && message.Message.Model != "" && message.Message.Model != syntheticModel {
			model := message.Message.Model
			s.rememberSubagent(parent, domain.SubagentMetadata{Model: &model})
		}
	case *protocol.StreamEvent:
		if parent := deref(message.ParentToolUseID); parent != "" && message.Event.Type == "message_start" && message.Event.Message != nil {
			if model := message.Event.Message.Model; model != "" && model != syntheticModel {
				s.rememberSubagent(parent, domain.SubagentMetadata{Model: &model})
			}
		}
	}
}

func prettyJSON(value jsontext.Value) string {
	if len(value) == 0 {
		return ""
	}
	pretty := value.Clone()
	if err := pretty.Indent(jsontext.WithIndent("  ")); err != nil {
		return string(value)
	}
	return string(pretty)
}

// receiveSubagent says whether the message belonged to a subagent. Read before the stream guard, so
// one running in the background reports between turns too.
func (s *Session) receiveSubagent(message protocol.Message) bool {
	switch message := message.(type) {
	case *protocol.AssistantMessage:
		parent := deref(message.ParentToolUseID)
		if parent == "" {
			return false
		}
		id, ok := s.subagentByToolUse[parent]
		if !ok {
			return false
		}
		for _, block := range message.Message.Content {
			switch {
			case block.Type == "text" && block.Text != nil && strings.TrimSpace(*block.Text) != "":
				s.reportSubagentLater(domain.SubagentReportSubagentActivity{ID: id, ActivityID: message.UUID + ":text", Kind: domain.SubagentReportSubagentActivityKindText, Text: *block.Text})
			case block.Type == "tool_use":
				title := ToolDisplayName(deref(block.Name), block.Input)
				s.reportSubagentLater(domain.SubagentReportSubagentActivity{ID: id, ActivityID: deref(block.ID), Kind: domain.SubagentReportSubagentActivityKindTool, Title: &title, Text: prettyJSON(block.Input)})
			}
		}
		return true
	case *protocol.TaskStarted:
		if message.SubagentType == nil || *message.SubagentType == "" {
			return false
		}
		s.subagentIDs.add(message.TaskID)
		toolUseID := deref(message.ToolUseID)
		if toolUseID != "" {
			s.subagentByToolUse[toolUseID] = message.TaskID
		}
		sessionScoped := true
		started := domain.SubagentReportSubagentStarted{ID: message.TaskID, Description: message.Description, AgentType: message.SubagentType, SessionScoped: &sessionScoped}
		if toolUseID != "" {
			started.SubagentMetadata = s.subagentMetadata[toolUseID]
		}
		if message.Prompt != nil {
			started.Prompt = message.Prompt
		}
		if effort, ok := s.subagentEfforts[message.TaskID]; ok {
			started.Effort = &effort
		}
		s.reportSubagentLater(started)
		return true
	case *protocol.TaskProgress:
		if (message.SubagentType == nil || *message.SubagentType == "") && !s.subagentIDs.has[message.TaskID] {
			return false
		}
		progress := domain.SubagentReportSubagentProgress{ID: message.TaskID, Description: message.Description, TotalTokens: message.Usage.TotalTokens}
		if message.LastToolName != nil && *message.LastToolName != "" {
			progress.LastToolName = message.LastToolName
		}
		if message.Summary != nil && *message.Summary != "" {
			progress.Summary = message.Summary
		}
		s.reportSubagentLater(progress)
		return true
	case *protocol.TaskNotification:
		if !s.subagentIDs.has[message.TaskID] {
			return false
		}
		s.subagentIDs.remove(message.TaskID)
		if _, ok := s.subagentEfforts[message.TaskID]; ok {
			delete(s.subagentEfforts, message.TaskID)
			s.subagentEffortOrder.remove(message.TaskID)
		}
		for toolUseID, id := range s.subagentByToolUse {
			if id == message.TaskID {
				delete(s.subagentByToolUse, toolUseID)
				delete(s.subagentMetadata, toolUseID)
				s.subagentMetadataOrder.remove(toolUseID)
			}
		}
		status := domain.SubagentTerminalStatus(message.Status)
		s.reportSubagentLater(domain.SubagentReportSubagentFinished{ID: message.TaskID, Status: status, Summary: message.Summary})
		return true
	}
	return false
}

// receiveWorkflow says whether the message belonged to a workflow. Read before the stream guard, so
// a workflow reports between turns too.
func (s *Session) receiveWorkflow(message protocol.Message) bool {
	switch message := message.(type) {
	case *protocol.TaskStarted:
		if message.TaskType == nil || *message.TaskType != "local_workflow" {
			return false
		}
		s.workflows[message.TaskID] = 0
		s.workflowOrder.add(message.TaskID)
		name := message.Description
		if message.WorkflowName != nil {
			name = *message.WorkflowName
		}
		s.reportWorkflowLater(contracts.WorkflowReportWorkflowStarted{ID: message.TaskID, Name: name, Description: message.Description})
		return true
	case *protocol.TaskProgress:
		if _, ok := s.workflows[message.TaskID]; !ok {
			return false
		}
		s.spendWorkflow(message.TaskID, message.Usage.TotalTokens)
		// The tree rides along with some frames and not others; a frame without one has nothing to say.
		if progress, ok := agent.ParseWorkflowProgress(message.WorkflowProgress); ok {
			s.reportWorkflowLater(contracts.WorkflowReportWorkflowProgress{
				ID: message.TaskID, Phases: progress.Phases, Agents: progress.Agents,
				TotalTokens: message.Usage.TotalTokens, TotalToolCalls: message.Usage.ToolUses,
			})
		}
		return true
	case *protocol.TaskNotification:
		if _, ok := s.workflows[message.TaskID]; !ok {
			return false
		}
		if message.Usage != nil {
			s.spendWorkflow(message.TaskID, message.Usage.TotalTokens)
		}
		delete(s.workflows, message.TaskID)
		s.workflowOrder.remove(message.TaskID)
		status := contracts.WorkflowReportWorkflowFinishedStatusStopped
		switch message.Status {
		case "completed":
			status = contracts.WorkflowReportWorkflowFinishedStatusCompleted
		case "failed":
			status = contracts.WorkflowReportWorkflowFinishedStatusFailed
		}
		s.reportWorkflowLater(contracts.WorkflowReportWorkflowFinished{ID: message.TaskID, Status: status, Summary: message.Summary})
		return true
	}
	return false
}

const runOver = "The run this call belongs to is over."

// canUseTool is the tool gate. Work that outlived its run still has to ask, so the turn the agent
// started takes the question.
func (s *Session) canUseTool(ctx context.Context, request protocol.PermissionRequest) (*protocol.PermissionResult, error) {
	intent := normalizeToolIntent(request.ToolName, request.Input, request.ToolUseID)
	answered := func(decision agent.ToolDecision) *protocol.PermissionResult {
		if decision.Allowed {
			return protocol.Allow(request.Input)
		}
		return protocol.Deny(agent.DenialReason(decision))
	}
	reach := agent.ToolReachOf(request.ToolName)
	s.lock()
	current := s.currentTurn()
	var scope *agent.ApprovalScope
	if current != nil {
		scope = &agent.ApprovalScope{Policy: current.Input.Policy, Channel: current.Input.Channel}
	}
	if agent.GrantsTool(reach, scope) {
		s.unlock()
		return protocol.Allow(request.Input), nil
	}
	if current != nil {
		s.unlock()
		return answered(current.Input.Authorize(ctx, intent)), nil
	}
	opened := s.openAgentTurn()
	s.unlock()
	if opened == nil {
		return protocol.Deny(runOver), nil
	}
	return answered(opened.turn.Authorize(ctx, intent)), nil
}
