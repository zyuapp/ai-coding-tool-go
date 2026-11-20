package agent

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func baseStart(taskID, runID string) contracts.InternalStartRunCommand {
	return contracts.InternalStartRunCommand{
		StartRunCommand: contracts.StartRunCommand{Channel: contracts.RunChannelMain, TaskID: taskID, Title: "Do the work", RunID: runID, Prompt: "do the work", WorkspaceID: "workspace-test", Policy: domain.ExecutionPolicyConfirm, Engine: domain.AgentEngineClaude, Model: "opus", Effort: domain.AgentEffortHigh},
		WorkspaceRoot:   "/tmp/project",
		ComputerUse:     domain.ComputerUseRunConfigUnavailable{Message: "test"},
	}
}

type pendingRun struct {
	input  *ProviderRunInput
	result chan ProviderResult
}

func (run pendingRun) resolve(result ProviderResult) { run.result <- result }

type fakeProvider struct {
	mu      sync.Mutex
	runs    []pendingRun
	stopped [][2]string
	execute func(input *ProviderRunInput) (ProviderResult, error)
}

func (p *fakeProvider) Execute(input *ProviderRunInput) (ProviderResult, error) {
	if p.execute != nil {
		return p.execute(input)
	}
	run := pendingRun{input: input, result: make(chan ProviderResult, 1)}
	p.mu.Lock()
	p.runs = append(p.runs, run)
	p.mu.Unlock()
	return <-run.result, nil
}

func (p *fakeProvider) run(t *testing.T, index int) pendingRun {
	t.Helper()
	eventually(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return len(p.runs) > index })
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runs[index]
}

func (p *fakeProvider) holds(taskID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, run := range p.runs {
		if run.input.TaskID == taskID {
			return true
		}
	}
	return false
}

func (p *fakeProvider) StopProcess(taskID, processID string) bool {
	if !p.holds(taskID) {
		return false
	}
	p.mu.Lock()
	p.stopped = append(p.stopped, [2]string{taskID, processID})
	p.mu.Unlock()
	return true
}
func (p *fakeProvider) LabelThread(taskID, _ string) bool                     { return p.holds(taskID) }
func (p *fakeProvider) ArchiveThread(string, *domain.Continuation, bool) bool { return false }
func (p *fakeProvider) RetireOutOfRole(string, *domain.CoordinationRole)      {}

type eventLog struct {
	mu     sync.Mutex
	events []contracts.AgentEvent
}

func (l *eventLog) emit(event contracts.AgentEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) all() []contracts.AgentEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]contracts.AgentEvent(nil), l.events...)
}

func (l *eventLog) forRun(runID string) []contracts.RunEvent {
	var found []contracts.RunEvent
	for _, event := range l.all() {
		if run, ok := event.(contracts.RunEvent); ok && RunEventBaseOf(run).RunID == runID {
			found = append(found, run)
		}
	}
	return found
}

func (l *eventLog) statuses(runID string) []domain.RunStatus {
	var statuses []domain.RunStatus
	for _, event := range l.forRun(runID) {
		if status, ok := event.(contracts.RunEventRunStatus); ok {
			statuses = append(statuses, status.Status)
		}
	}
	return statuses
}

func (l *eventLog) approvals() []contracts.RunEventApprovalRequested {
	var found []contracts.RunEventApprovalRequested
	for _, event := range l.all() {
		if approval, ok := event.(contracts.RunEventApprovalRequested); ok {
			found = append(found, approval)
		}
	}
	return found
}

func settles(t *testing.T, log *eventLog, runID string, count int) {
	t.Helper()
	eventually(t, func() bool { return len(log.statuses(runID)) >= count })
}

func TestCoordinatorSuccessfulRunEmitsOrderedEventsAndOneTerminalState(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-a", "run-a"))
	run := provider.run(t, 0)
	equal(t, run.input.Title, "Do the work")
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-a", 2)
	equal(t, log.statuses("run-a"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusSucceeded})
	var sequences []float64
	for _, event := range log.forRun("run-a") {
		sequences = append(sequences, RunEventBaseOf(event).Sequence)
	}
	equal(t, sequences, []float64{1, 2, 3})
}

func TestCoordinatorCarriesOperationAndProjectRoot(t *testing.T) {
	provider := &fakeProvider{}
	coordinator := NewRunCoordinator(provider, func(contracts.AgentEvent) {}, CoordinatorOptions{})
	compact := baseStart("task-c", "run-c")
	compact.Engine = domain.AgentEngineCodex
	compact.Operation = contracts.RunOperationCompact{PreTokens: 125_000}
	coordinator.Start(compact)
	equal(t, provider.run(t, 0).input.Operation, contracts.RunOperation(contracts.RunOperationCompact{PreTokens: 125_000}))
	worktree := baseStart("task-w", "run-w")
	worktree.WorkspaceRoot = "/tmp/worktrees/task-w"
	worktree.ProjectRoot = ptr("/tmp/project")
	coordinator.Start(worktree)
	run := provider.run(t, 1)
	equal(t, run.input.WorkspaceRoot, "/tmp/worktrees/task-w")
	equal(t, *run.input.ProjectRoot, "/tmp/project")
	coordinator.Start(baseStart("task-p", "run-p"))
	equal(t, provider.run(t, 2).input.ProjectRoot, (*string)(nil))
	equal(t, provider.run(t, 2).input.FastMode != nil && !*provider.run(t, 2).input.FastMode, domain.CapabilitiesFor("claude").FastMode)
}

func TestCoordinatorProviderFailureStaysCorrelated(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-f", "run-f"))
	provider.run(t, 0).resolve(ProviderResult{Status: domain.RunStatusFailed, Message: "provider failed"})
	settles(t, log, "run-f", 2)
	equal(t, log.statuses("run-f"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusFailed})
	events := log.all()
	equal(t, *events[len(events)-1].(contracts.RunEventRunStatus).Message, "provider failed")
}

func TestCoordinatorLateOutputOfCancelledRunIsDropped(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-old", "run-old"))
	old := provider.run(t, 0)
	equal(t, coordinator.Cancel("task-old", "run-old"), true)
	coordinator.Start(baseStart("task-new", "run-new"))
	next := provider.run(t, 1)
	old.input.Emit(EventAssistant{MessageID: "late", Text: "late output"})
	old.input.Emit(EventSubagent{Report: domain.SubagentReportSubagentStarted{ID: "late-child", Description: "Late child"}})
	old.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	next.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-new", 2)
	for _, event := range log.all() {
		if _, ok := event.(contracts.RunEventAssistantDelta); ok {
			t.Fatal("late output reached the renderer")
		}
		if _, ok := event.(contracts.SubagentEventSubagentStarted); ok {
			t.Fatal("a late child reached the renderer")
		}
	}
	equal(t, log.statuses("run-old"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusCancelled})
	equal(t, old.input.Aborted(), true)
}

func TestCoordinatorThreadReportsOutliveTheRun(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-w", "run-w"))
	run := provider.run(t, 0)
	run.input.ReportWorkflow(contracts.WorkflowReportWorkflowStarted{ID: "wf-1", Name: "review-changes", Description: "Review changed files"})
	run.input.ReportSubagent(domain.SubagentReportSubagentStarted{ID: "child-1", Description: "Inspect", SessionScoped: ptr(true)})
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-w", 2)
	run.input.ReportWorkflow(contracts.WorkflowReportWorkflowFinished{ID: "wf-1", Status: contracts.WorkflowReportWorkflowFinishedStatusCompleted, Summary: "Dynamic workflow completed"})
	run.input.ReportSubagent(domain.SubagentReportSubagentStatus{ID: "child-1", Status: domain.SubagentLiveStatusIdle, Summary: ptr("Ready")})
	var thread []contracts.AgentEvent
	for _, event := range log.all() {
		if _, ok := event.(contracts.RunEvent); !ok {
			thread = append(thread, event)
		}
	}
	equal(t, thread, []contracts.AgentEvent{
		contracts.WorkflowEventWorkflowStarted{ID: "wf-1", Name: "review-changes", Description: "Review changed files", TaskID: "task-w"},
		contracts.SubagentEventSubagentStarted{ID: "child-1", Description: "Inspect", SessionScoped: ptr(true), TaskID: "task-w"},
		contracts.WorkflowEventWorkflowFinished{ID: "wf-1", Status: contracts.WorkflowEventWorkflowFinishedStatusCompleted, Summary: "Dynamic workflow completed", TaskID: "task-w"},
		contracts.SubagentEventSubagentStatus{ID: "child-1", Status: domain.SubagentLiveStatusIdle, Summary: ptr("Ready"), TaskID: "task-w"},
	})
}

func authorizing(intent domain.ToolIntent) func(input *ProviderRunInput) (ProviderResult, error) {
	return func(input *ProviderRunInput) (ProviderResult, error) {
		if input.Authorize(context.Background(), intent).Allowed {
			return ProviderResult{Status: domain.RunStatusSucceeded}, nil
		}
		return ProviderResult{Status: domain.RunStatusFailed}, nil
	}
}

func TestCoordinatorApprovalResumesOnlyAfterItsDecision(t *testing.T) {
	log := &eventLog{}
	provider := &fakeProvider{execute: authorizing(domain.ToolIntent{ToolID: "tool-1", Name: "Write", WritePath: ptr("/tmp/project/file.txt")})}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{IsWritePathInside: func(context.Context, string, string) bool { return true }})
	coordinator.Start(baseStart("task-p", "run-p"))
	eventually(t, func() bool { return len(log.approvals()) == 1 })
	equal(t, log.statuses("run-p"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval})
	equal(t, log.approvals()[0].Title, "Write needs approval")
	equal(t, coordinator.DecideApproval("task-p", "run-p", log.approvals()[0].ApprovalID, true), true)
	settles(t, log, "run-p", 4)
	equal(t, log.statuses("run-p"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval, domain.RunStatusRunning, domain.RunStatusSucceeded})
}

func TestCoordinatorApprovalsShownOneAtATime(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-m", "run-m"))
	input := provider.run(t, 0).input
	first, second := make(chan ToolDecision, 1), make(chan ToolDecision, 1)
	go func() {
		first <- input.Authorize(context.Background(), domain.ToolIntent{ToolID: "tool-1", Name: "Bash"})
	}()
	eventually(t, func() bool { return len(log.approvals()) == 1 })
	go func() {
		second <- input.Authorize(context.Background(), domain.ToolIntent{ToolID: "tool-2", Name: "Write"})
	}()
	time.Sleep(10 * time.Millisecond)
	equal(t, len(log.approvals()), 1)
	equal(t, log.statuses("run-m"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval})
	equal(t, coordinator.DecideApproval("task-m", "run-m", log.approvals()[0].ApprovalID, true), true)
	equal(t, <-first, Allow)
	equal(t, log.approvals()[1].Intent.ToolID, "tool-2")
	equal(t, log.statuses("run-m"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval, domain.RunStatusAwaitingApproval})
	equal(t, coordinator.DecideApproval("task-m", "run-m", log.approvals()[1].ApprovalID, false), true)
	equal(t, <-second, Deny)
	equal(t, log.statuses("run-m"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval, domain.RunStatusAwaitingApproval, domain.RunStatusRunning})
}

func TestCoordinatorCancellingExpiresApproval(t *testing.T) {
	log := &eventLog{}
	decided := make(chan ToolDecision, 1)
	provider := &fakeProvider{execute: func(input *ProviderRunInput) (ProviderResult, error) {
		decision := input.Authorize(context.Background(), domain.ToolIntent{ToolID: "tool-2", Name: "Edit", WritePath: ptr("/tmp/project/file.txt")})
		decided <- decision
		return ProviderResult{Status: domain.RunStatusFailed}, nil
	}}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{IsWritePathInside: func(context.Context, string, string) bool { return true }})
	coordinator.Start(baseStart("task-c", "run-c"))
	eventually(t, func() bool { return len(log.approvals()) == 1 })
	equal(t, coordinator.Cancel("task-c", "run-c"), true)
	equal(t, coordinator.DecideApproval("task-c", "run-c", log.approvals()[0].ApprovalID, true), false)
	equal(t, <-decided, Deny)
	time.Sleep(5 * time.Millisecond)
	equal(t, log.statuses("run-c"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval, domain.RunStatusCancelled})
}

func TestCoordinatorScheduledRunAnswersItsOwnApproval(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{UnattendedApproval: 10 * time.Millisecond})
	command := baseStart("task-u", "run-u")
	command.Unattended = ptr(true)
	coordinator.Start(command)
	answer := provider.run(t, 0).input.Authorize(context.Background(), domain.ToolIntent{ToolID: "tool-u", Name: "Bash"})
	equal(t, answer.Allowed, false)
	equal(t, regexp.MustCompile(`scheduled run`).MatchString(answer.Reason), true)
	equal(t, log.statuses("run-u"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusAwaitingApproval, domain.RunStatusRunning})
}

func TestCoordinatorApprovalAnswersItselfOnlyForUnwatchedRuns(t *testing.T) {
	provider := &fakeProvider{}
	coordinator := NewRunCoordinator(provider, func(contracts.AgentEvent) {}, CoordinatorOptions{UnattendedApproval: 10 * time.Millisecond})
	coordinator.Start(baseStart("task-a", "run-a"))
	scheduled := baseStart("task-s", "run-s")
	scheduled.Unattended = ptr(true)
	coordinator.Start(scheduled)
	provider.run(t, 1)
	equal(t, coordinator.Steer("task-s", "run-s", "message-1", "check staging too"), true)
	answered := make(chan string, 2)
	for index := range 2 {
		run := provider.run(t, index)
		go func() {
			run.input.Authorize(context.Background(), domain.ToolIntent{ToolID: "tool-1", Name: "Bash"})
			answered <- run.input.TaskID
		}()
	}
	select {
	case taskID := <-answered:
		t.Fatalf("%s answered itself", taskID)
	case <-time.After(40 * time.Millisecond):
	}
	coordinator.Cancel("task-a", "run-a")
	coordinator.Cancel("task-s", "run-s")
}

func TestCoordinatorWritePathOutsideIsDeniedBeforeAsking(t *testing.T) {
	log := &eventLog{}
	provider := &fakeProvider{execute: authorizing(domain.ToolIntent{ToolID: "tool-3", Name: "Write", WritePath: ptr("/tmp/elsewhere/file.txt")})}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{IsWritePathInside: func(_ context.Context, root, candidate string) bool {
		return root == "/tmp/project" && len(candidate) > len(root) && candidate[:len(root)+1] == root+"/"
	}})
	coordinator.Start(baseStart("task-w", "run-w"))
	settles(t, log, "run-w", 2)
	equal(t, len(log.approvals()), 0)
	equal(t, log.statuses("run-w"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusFailed})
}

func TestCoordinatorForwardsEveryProviderEventInOneSequence(t *testing.T) {
	log := &eventLog{}
	provider := &fakeProvider{execute: func(input *ProviderRunInput) (ProviderResult, error) {
		for _, event := range []ProviderEvent{
			EventAssistant{MessageID: "message-1", Text: "hello"},
			EventAssistant{MessageID: "image-1", Text: "[Generated image](/tmp/cat.png)", Artifact: true},
			EventUsage{Tokens: 10, Limit: 200_000, Model: "claude"},
			EventSpend{Tokens: 1_250},
			EventCompactionStatus{Compacting: true},
			EventCompaction{Trigger: "manual", PreTokens: 10},
			EventTool{Intent: domain.ToolIntent{ToolID: "tool-1", Name: "Read"}},
			EventContinuation{Continuation: domain.Continuation{Provider: "claude", Value: "session-1"}},
		} {
			input.Emit(event)
		}
		return ProviderResult{Status: domain.RunStatusSucceeded}, nil
	}}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-events", "run-events"))
	settles(t, log, "run-events", 2)
	events := log.all()
	equal(t, events[3], contracts.AgentEvent(contracts.RunEventAssistantDelta{RunEventBase: base("task-events", "run-events", 4), MessageID: "image-1", Text: "[Generated image](/tmp/cat.png)", Artifact: ptr(true)}))
	equal(t, events[5], contracts.AgentEvent(contracts.RunEventContextSpent{RunEventBase: base("task-events", "run-events", 6), Tokens: 1_250}))
	for index, event := range events {
		equal(t, RunEventBaseOf(event.(contracts.RunEvent)).Sequence, float64(index+1))
	}
	equal(t, len(events), 11)
	equal(t, events[10].(contracts.RunEventRunStatus).Status, domain.RunStatusSucceeded)
}

func TestCoordinatorThrownOrPanickedProviderErrorIsOneFailure(t *testing.T) {
	for _, execute := range []func(*ProviderRunInput) (ProviderResult, error){
		func(*ProviderRunInput) (ProviderResult, error) {
			return ProviderResult{}, errors.New("provider exploded")
		},
		func(*ProviderRunInput) (ProviderResult, error) { panic(errors.New("provider exploded")) },
	} {
		log := &eventLog{}
		coordinator := NewRunCoordinator(&fakeProvider{execute: execute}, log.emit, CoordinatorOptions{})
		coordinator.Start(baseStart("task-throw", "run-throw"))
		settles(t, log, "run-throw", 2)
		equal(t, log.statuses("run-throw"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusFailed})
		events := log.all()
		equal(t, *events[len(events)-1].(contracts.RunEventRunStatus).Message, "provider exploded")
	}
}

func TestCoordinatorNewRunForSameTaskSupersedesThePrevious(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-1", "run-first"))
	first := provider.run(t, 0)
	coordinator.Start(baseStart("task-1", "run-second"))
	second := provider.run(t, 1)
	first.input.Emit(EventAssistant{MessageID: "late", Text: "late output"})
	first.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	second.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-second", 2)
	equal(t, first.input.Aborted(), true)
	equal(t, log.statuses("run-first"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusCancelled})
	equal(t, log.statuses("run-second"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusSucceeded})
	equal(t, coordinator.Cancel("task-1", "run-second"), false)
}

func TestCoordinatorSteeringReachesOnlyTheNamedRun(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-a", "run-a"))
	run := provider.run(t, 0)
	equal(t, coordinator.Steer("task-a", "run-b", "message-1", "later"), false)
	equal(t, coordinator.Steer("task-b", "run-a", "message-1", "later"), false)
	equal(t, coordinator.Steer("task-a", "run-a", "message-1", "check the tests"), true)
	message, ok := run.input.Steering.Next(context.Background())
	equal(t, ok, true)
	equal(t, message, SteerMessage{MessageID: "message-1", Prompt: "check the tests"})
	run.input.Emit(EventSteered{MessageID: "message-1"})
	run.input.Emit(EventSteerFailed{MessageID: "message-2", Message: "Could not steer. Your message is still queued."})
	events := log.all()
	equal(t, events[len(events)-2], contracts.AgentEvent(contracts.RunEventQueuedDelivered{RunEventBase: base("task-a", "run-a", 3), MessageID: "message-1"}))
	equal(t, events[len(events)-1], contracts.AgentEvent(contracts.RunEventQueuedSteerFailed{RunEventBase: base("task-a", "run-a", 4), MessageID: "message-2", Message: "Could not steer. Your message is still queued."}))
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-a", 2)
	equal(t, coordinator.Steer("task-a", "run-a", "message-2", "too late"), false)
	_, ok = run.input.Steering.Next(context.Background())
	equal(t, ok, false, "a finished run stops waiting for more")
}

func tails(log *eventLog) [][2]string {
	var seen [][2]string
	for _, event := range log.all() {
		switch event := event.(type) {
		case contracts.RunEventAssistantTail:
			seen = append(seen, [2]string{"assistant.tail", event.Text})
		case contracts.RunEventAssistantDelta:
			seen = append(seen, [2]string{"assistant.delta", event.Text})
		}
	}
	return seen
}

func TestCoordinatorTailBurstCollapsesToLeadingAndTrailing(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{TailInterval: 20 * time.Millisecond})
	coordinator.Start(baseStart("task-t", "run-t"))
	emit := provider.run(t, 0).input.Emit
	for _, text := range []string{"The", "The first", "The first thing", "The first thing to"} {
		emit(EventAssistantTail{MessageID: "message-1", Text: text})
	}
	equal(t, tails(log), [][2]string{{"assistant.tail", "The"}})
	time.Sleep(40 * time.Millisecond)
	equal(t, tails(log), [][2]string{{"assistant.tail", "The"}, {"assistant.tail", "The first thing to"}})
	provider.run(t, 0).resolve(ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestCoordinatorCommittedBlockCancelsItsTail(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{TailInterval: 20 * time.Millisecond})
	coordinator.Start(baseStart("task-c", "run-c"))
	emit := provider.run(t, 0).input.Emit
	emit(EventAssistantTail{MessageID: "message-1", Text: "Opening"})
	emit(EventAssistantTail{MessageID: "message-1", Text: "Opening line."})
	emit(EventAssistant{MessageID: "message-1", Text: "Opening line.\n\n", Append: true})
	emit(EventAssistantTail{MessageID: "message-1", Text: ""})
	time.Sleep(40 * time.Millisecond)
	equal(t, tails(log), [][2]string{{"assistant.tail", "Opening"}, {"assistant.delta", "Opening line.\n\n"}, {"assistant.tail", ""}})
	provider.run(t, 0).resolve(ProviderResult{Status: domain.RunStatusSucceeded})
}

func TestCoordinatorRunEndingMidStreamPublishesNoFurtherTail(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{TailInterval: 20 * time.Millisecond})
	coordinator.Start(baseStart("task-e", "run-e"))
	run := provider.run(t, 0)
	run.input.Emit(EventAssistantTail{MessageID: "message-1", Text: "Started"})
	run.input.Emit(EventAssistantTail{MessageID: "message-1", Text: "Started writing"})
	run.resolve(ProviderResult{Status: domain.RunStatusCancelled})
	settles(t, log, "run-e", 2)
	time.Sleep(40 * time.Millisecond)
	equal(t, tails(log), [][2]string{{"assistant.tail", "Started"}})
	events := log.all()
	_, last := events[len(events)-1].(contracts.RunEventRunStatus)
	equal(t, last, true)
}

type scopedThreads struct {
	ThreadBridge
	taskID string
}

func TestCoordinatorBridgesAreScopedToTheirThread(t *testing.T) {
	provider := &fakeProvider{}
	var mu sync.Mutex
	var bridges []string
	coordinator := NewRunCoordinator(provider, func(contracts.AgentEvent) {}, CoordinatorOptions{Threads: func(taskID string) ThreadBridge {
		mu.Lock()
		bridges = append(bridges, taskID)
		mu.Unlock()
		return scopedThreads{taskID: taskID}
	}})
	coordinator.Start(baseStart("task-a", "run-a"))
	provider.run(t, 0)
	coordinator.Start(baseStart("task-b", "run-b"))
	provider.run(t, 1)
	equal(t, bridges, []string{"task-a", "task-b"})
	equal(t, provider.run(t, 0).input.Threads.(scopedThreads).taskID, "task-a")
	equal(t, provider.run(t, 1).input.Threads.(scopedThreads).taskID, "task-b")
}

func TestCoordinatorStopProcessReachesSessionWithOrWithoutRun(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	equal(t, coordinator.StopProcess("task-p", "bash-1"), false)
	coordinator.Start(baseStart("task-p", "run-p"))
	run := provider.run(t, 0)
	equal(t, coordinator.StopProcess("task-p", "bash-1"), true)
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-p", 2)
	equal(t, coordinator.StopProcess("task-p", "wf-1"), true)
	equal(t, provider.stopped, [][2]string{{"task-p", "bash-1"}, {"task-p", "wf-1"}})
}

func TestCoordinatorAgentTurnGetsARunOfItsOwn(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-u", "run-u"))
	run := provider.run(t, 0)
	equal(t, run.input.BeginAgentTurn(), nil, "the thread already has a run of its own")
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-u", 2)
	turn := run.input.BeginAgentTurn()
	turn.Emit(EventAssistant{MessageID: "m-1", Text: "The workflow finished."})
	turn.End(ProviderResult{Status: domain.RunStatusSucceeded})
	var opened contracts.RunEventRunStarted
	for _, event := range log.all() {
		if started, ok := event.(contracts.RunEventRunStarted); ok && started.RunID != "run-u" {
			opened = started
		}
	}
	equal(t, *opened.AgentInitiated, true)
	var kinds []string
	for _, event := range log.forRun(opened.RunID) {
		kinds = append(kinds, typeName(event))
	}
	equal(t, kinds, []string{"RunEventRunStarted:1", "RunEventRunStatus:2", "RunEventAssistantDelta:3", "RunEventRunStatus:4"})
	equal(t, log.statuses(opened.RunID), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusSucceeded})
}

func typeName(event contracts.RunEvent) string {
	name := ""
	switch event.(type) {
	case contracts.RunEventRunStarted:
		name = "RunEventRunStarted"
	case contracts.RunEventRunStatus:
		name = "RunEventRunStatus"
	case contracts.RunEventAssistantDelta:
		name = "RunEventAssistantDelta"
	}
	return name + ":" + formatJSNumber(RunEventBaseOf(event).Sequence)
}

func TestCoordinatorAgentTurnCanBeSteeredInto(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task-v", "run-v"))
	run := provider.run(t, 0)
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	settles(t, log, "run-v", 2)
	turn := run.input.BeginAgentTurn()
	var opened string
	for _, event := range log.all() {
		if started, ok := event.(contracts.RunEventRunStarted); ok && started.RunID != "run-v" {
			opened = started.RunID
		}
	}
	equal(t, coordinator.Steer("task-v", opened, "message-1", "stop and read this"), true)
	message, ok := turn.Steering().Next(context.Background())
	equal(t, ok, true)
	equal(t, message, SteerMessage{MessageID: "message-1", Prompt: "stop and read this"})
	turn.End(ProviderResult{Status: domain.RunStatusSucceeded})
	_, ok = turn.Steering().Next(context.Background())
	equal(t, ok, false)
}

func question(ids ...string) domain.QuestionRequest {
	request := domain.QuestionRequest{Questions: []*domain.AgentQuestion{}}
	for _, id := range ids {
		request.Questions = append(request.Questions, &domain.AgentQuestion{ID: id, Header: id, Question: "Which " + id + "?", Options: []*domain.QuestionOption{}})
	}
	return request
}

func TestCoordinatorQuestionsCollectAnswersAndRejectStaleOrDuplicate(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{})
	coordinator.Start(baseStart("task", "run"))
	input := provider.run(t, 0).input
	type answer struct {
		answers domain.QuestionAnswers
		ok      bool
	}
	result := make(chan answer, 1)
	go func() {
		answers, ok := input.AskQuestion(context.Background(), question("region", "color"))
		result <- answer{answers, ok}
	}()
	var requestID string
	eventually(t, func() bool {
		for _, event := range log.all() {
			if asked, ok := event.(contracts.RunEventQuestionRequested); ok {
				requestID = asked.RequestID
				return true
			}
		}
		return false
	})
	equal(t, coordinator.AnswerQuestion("task", "old", requestID, "region", "Chicago"), false)
	equal(t, coordinator.AnswerQuestion("task", "run", requestID, "missing", "Chicago"), false)
	equal(t, coordinator.AnswerQuestion("task", "run", requestID, "region", "  "), false)
	equal(t, coordinator.AnswerQuestion("task", "run", requestID, "region", "Chicago"), true)
	equal(t, coordinator.AnswerQuestion("task", "run", requestID, "region", "London"), false)
	equal(t, coordinator.AnswerQuestion("task", "run", requestID, "color", "Blue"), true)
	got := <-result
	equal(t, got.ok, true)
	equal(t, got.answers, domain.QuestionAnswers{"region": "Chicago", "color": "Blue"})
	equal(t, log.statuses("run"), []domain.RunStatus{domain.RunStatusRunning})
	coordinator.Cancel("task", "run")
}

func TestCoordinatorQuestionsCloseOnWithdrawalCancellationAndDeadline(t *testing.T) {
	provider := &fakeProvider{}
	log := &eventLog{}
	coordinator := NewRunCoordinator(provider, log.emit, CoordinatorOptions{UnattendedApproval: 5 * time.Millisecond})
	request := domain.QuestionRequest{Blocking: true, Questions: []*domain.AgentQuestion{{ID: "q", Question: "Continue?", Options: []*domain.QuestionOption{}}}}
	coordinator.Start(baseStart("task", "run"))
	input := provider.run(t, 0).input
	ctx, withdraw := context.WithCancel(context.Background())
	withdrawn := make(chan bool, 1)
	go func() { _, ok := input.AskQuestion(ctx, request); withdrawn <- ok }()
	eventually(t, func() bool { return len(log.all()) >= 3 })
	withdraw()
	equal(t, <-withdrawn, false)
	cancelled := make(chan bool, 1)
	go func() { _, ok := input.AskQuestion(context.Background(), request); cancelled <- ok }()
	eventually(t, func() bool { return len(log.all()) >= 5 })
	coordinator.Cancel("task", "run")
	equal(t, <-cancelled, false)
	next := baseStart("task", "next")
	next.Unattended = ptr(true)
	coordinator.Start(next)
	_, ok := provider.run(t, 1).input.AskQuestion(context.Background(), request)
	equal(t, ok, false)
	closed := 0
	for _, event := range log.all() {
		if _, ok := event.(contracts.RunEventQuestionClosed); ok {
			closed++
		}
	}
	equal(t, closed, 3)
	coordinator.Cancel("task", "next")
}
