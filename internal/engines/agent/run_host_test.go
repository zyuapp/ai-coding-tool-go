package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

type fakeAgent struct {
	mu          sync.Mutex
	posted      []AgentMessage
	killed      bool
	unreachable bool
	listener    AgentProcessListener
}

func (a *fakeAgent) Post(message AgentMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.unreachable {
		panic(errors.New("Agent process is unavailable."))
	}
	a.posted = append(a.posted, message)
}

func (a *fakeAgent) Kill() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.killed = true
}

func (a *fakeAgent) say(message any) { a.listener.Message(message) }

func (a *fakeAgent) messages() []AgentMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AgentMessage(nil), a.posted...)
}

type resolverFunc func(id string) (workspace.WorkspaceResolution, error)

func (f resolverFunc) Resolve(id string) (workspace.WorkspaceResolution, error) { return f(id) }

func defaultResolve(id string) (workspace.WorkspaceResolution, error) {
	switch id {
	case "workspace-1":
		return workspace.WorkspaceResolution{Status: workspace.ResolutionAvailable, Workspace: workspace.WorkspaceRecord{ID: id, Root: "/tmp/project", Kind: workspace.WorkspaceProject}}, nil
	case "worktree-1":
		return workspace.WorkspaceResolution{Status: workspace.ResolutionAvailable, Workspace: workspace.WorkspaceRecord{ID: id, Root: "/worktrees/project-1", Kind: "worktree"}}, nil
	}
	return workspace.WorkspaceResolution{Status: workspace.ResolutionUnavailable, Workspace: workspace.WorkspaceRecord{ID: id, Root: "/tmp/gone", Kind: workspace.WorkspaceProject}, Reason: workspace.ReasonMissing}, nil
}

type hostHarness struct {
	mu      sync.Mutex
	sent    []any
	fires   []contracts.AutomationFire
	agents  []*fakeAgent
	bridge  RunBridge
	resolve func(id string) (workspace.WorkspaceResolution, error)
}

func newHostHarness(t *testing.T, change func(config *RunHostConfig)) *hostHarness {
	h := &hostHarness{resolve: defaultResolve}
	config := RunHostConfig{
		Publish: func(event contracts.AgentEvent) { h.mu.Lock(); h.sent = append(h.sent, event); h.mu.Unlock() },
		Fire: func(fire contracts.AutomationFire) bool {
			h.mu.Lock()
			h.fires = append(h.fires, fire)
			h.mu.Unlock()
			return true
		},
		Ask: func(request contracts.ThreadRequest) bool {
			h.mu.Lock()
			h.sent = append(h.sent, request)
			h.mu.Unlock()
			return true
		},
		Running: func() bool { return true },
		Workspaces: func() (WorkspaceResolver, error) {
			return resolverFunc(func(id string) (workspace.WorkspaceResolution, error) { return h.resolve(id) }), nil
		},
		Scheduler: func() (AutomationScheduler, error) { return nil, errors.New("no scheduler") },
		ComputerUseForRun: func(context.Context) domain.ComputerUseRunConfig {
			return domain.ComputerUseRunConfigUnavailable{Message: "test"}
		},
		Agent: func(listener AgentProcessListener) AgentProcess {
			agent := &fakeAgent{listener: listener}
			h.mu.Lock()
			h.agents = append(h.agents, agent)
			h.mu.Unlock()
			return agent
		},
		ResourceMonitor: func(options ResourceMonitorOptions) *SessionResourceMonitor {
			options.manual = true
			options.Read = func(context.Context) ([]platform.ProcessSample, error) { return nil, nil }
			return NewSessionResourceMonitor(options)
		},
	}
	if change != nil {
		change(&config)
	}
	h.bridge = StartRunHost(config)
	t.Cleanup(h.bridge.KillAgent)
	return h
}

func (h *hostHarness) agent(t *testing.T) *fakeAgent {
	t.Helper()
	eventually(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.agents) > 0 })
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.agents[0]
}

func (h *hostHarness) events() []contracts.AgentEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	var events []contracts.AgentEvent
	for _, entry := range h.sent {
		if event, ok := entry.(contracts.AgentEvent); ok {
			events = append(events, event)
		}
	}
	return events
}

func (h *hostHarness) statuses(runID string) []domain.RunStatus {
	var statuses []domain.RunStatus
	for _, event := range h.events() {
		if status, ok := event.(contracts.RunEventRunStatus); ok && status.RunID == runID {
			statuses = append(statuses, status.Status)
		}
	}
	return statuses
}

func (h *hostHarness) lastMessage() string {
	message := ""
	for _, event := range h.events() {
		if status, ok := event.(contracts.RunEventRunStatus); ok && status.Message != nil {
			message = *status.Message
		}
	}
	return message
}

func startCommand(taskID, runID string, change func(command *contracts.StartRunCommand)) contracts.StartRunCommand {
	command := contracts.StartRunCommand{Channel: contracts.RunChannelMain, TaskID: taskID, Title: "Work", RunID: runID, Prompt: "work", WorkspaceID: "workspace-1", Policy: domain.ExecutionPolicyConfirm, Engine: domain.AgentEngineClaude, Model: "opus", Effort: domain.AgentEffortHigh}
	if change != nil {
		change(&command)
	}
	return command
}

func base(taskID, runID string, sequence float64) contracts.RunEventBase {
	return contracts.RunEventBase{TaskID: taskID, RunID: runID, Sequence: sequence}
}

func describe(message AgentMessage) string {
	switch message := message.(type) {
	case contracts.InternalStartRunCommand:
		return "start:" + message.RunID
	case contracts.ThreadRoleCommand:
		return "role:" + message.TaskID
	}
	return fmt.Sprintf("%T", message)
}

func TestHostStartReachesAgentOnceWorkspaceResolves(t *testing.T) {
	h := newHostHarness(t, nil)
	h.bridge.HandleRunCommand(startCommand("task-1", "run-1", nil))
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 1 })
	equal(t, agent.messages(), []AgentMessage{contracts.InternalStartRunCommand{
		StartRunCommand: startCommand("task-1", "run-1", nil),
		WorkspaceRoot:   "/tmp/project",
		ComputerUse:     domain.ComputerUseRunConfigUnavailable{Message: "test"},
	}})
}

func TestHostMemberStartCarriesItsProjectCheckout(t *testing.T) {
	h := newHostHarness(t, nil)
	member := domain.CoordinationRoleMember
	start := func(runID, project string) contracts.StartRunCommand {
		return startCommand("task-"+runID, runID, func(command *contracts.StartRunCommand) {
			command.WorkspaceID = "worktree-1"
			command.CoordinationRole = &member
			command.ProjectWorkspaceID = &project
		})
	}
	h.bridge.HandleRunCommand(start("kept", "workspace-1"))
	h.bridge.HandleRunCommand(start("gone", "workspace-gone"))
	h.bridge.HandleRunCommand(start("worktree", "worktree-1"))
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 3 })
	roots := map[string][2]any{}
	for _, message := range agent.messages() {
		command := message.(contracts.InternalStartRunCommand)
		var project any
		if command.ProjectRoot != nil {
			project = *command.ProjectRoot
		}
		roots[command.RunID] = [2]any{command.WorkspaceRoot, project}
	}
	equal(t, roots, map[string][2]any{
		"kept":     {"/worktrees/project-1", "/tmp/project"},
		"gone":     {"/worktrees/project-1", nil},
		"worktree": {"/worktrees/project-1", nil},
	})
}

func TestHostDroppedStartNeverReachesAgent(t *testing.T) {
	h := newHostHarness(t, nil)
	gate := make(chan struct{})
	h.resolve = func(id string) (workspace.WorkspaceResolution, error) { <-gate; return defaultResolve(id) }
	h.bridge.HandleRunCommand(startCommand("task-1", "run-cancelled", nil))
	h.bridge.HandleRunCommand(contracts.CancelRunCommand{TaskID: "task-1", RunID: "run-cancelled"})
	h.bridge.HandleRunCommand(startCommand("task-2", "run-old", nil))
	h.bridge.HandleRunCommand(startCommand("task-2", "run-new", nil))
	h.bridge.HandleRunCommand(startCommand("task-3", "run-missing", func(command *contracts.StartRunCommand) { command.WorkspaceID = "workspace-gone" }))
	close(gate)
	eventually(t, func() bool { return len(h.statuses("run-missing")) == 1 })
	equal(t, h.statuses("run-cancelled"), []domain.RunStatus{domain.RunStatusCancelled})
	equal(t, h.statuses("run-old"), []domain.RunStatus{domain.RunStatusCancelled})
	equal(t, h.statuses("run-missing"), []domain.RunStatus{domain.RunStatusFailed})
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 1 })
	time.Sleep(10 * time.Millisecond)
	equal(t, describe(agent.messages()[0]), "start:run-new")
	equal(t, len(agent.messages()), 1)
}

func TestHostRoleWaitsBehindAStartStillResolving(t *testing.T) {
	h := newHostHarness(t, nil)
	coordinator := domain.CoordinationRoleCoordinator
	h.bridge.HandleRunCommand(contracts.ThreadRoleCommand{TaskID: "task-1", CoordinationRole: &coordinator})
	h.mu.Lock()
	equal(t, len(h.agents), 0, "no process holds a session to retire")
	h.mu.Unlock()
	h.bridge.HandleRunCommand(startCommand("task-1", "run-1", nil))
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 1 })
	gate := make(chan struct{})
	h.resolve = func(id string) (workspace.WorkspaceResolution, error) { <-gate; return defaultResolve(id) }
	h.bridge.HandleRunCommand(startCommand("task-1", "run-2", nil))
	h.bridge.HandleRunCommand(contracts.ThreadRoleCommand{TaskID: "task-1", CoordinationRole: &coordinator})
	h.bridge.HandleRunCommand(contracts.ThreadRoleCommand{TaskID: "task-2"})
	close(gate)
	eventually(t, func() bool { return len(agent.messages()) == 4 })
	var described []string
	for _, message := range agent.messages() {
		described = append(described, describe(message))
	}
	equal(t, described, []string{"start:run-1", "role:task-2", "start:run-2", "role:task-1"})
}

func TestHostOnlyARunsNextWordReachesTheRenderer(t *testing.T) {
	h := newHostHarness(t, nil)
	h.bridge.HandleRunCommand(startCommand("task-1", "run-1", nil))
	agent := h.agent(t)
	agent.say(contracts.RunEventRunStarted{RunEventBase: base("task-1", "run-1", 1)})
	agent.say(contracts.RunEventAssistantDelta{RunEventBase: base("task-1", "run-1", 3), MessageID: "message-1", Text: "hello"})
	agent.say(contracts.RunEventAssistantDelta{RunEventBase: base("task-1", "run-1", 2), MessageID: "message-1", Text: "stale"})
	agent.say(contracts.RunEventRunStatus{RunEventBase: base("task-1", "run-1", 4), Status: domain.RunStatusSucceeded})
	agent.say(contracts.RunEventAssistantDelta{RunEventBase: base("task-1", "run-1", 5), MessageID: "message-1", Text: "after the end"})
	agent.say(contracts.RunEventRunStarted{RunEventBase: base("task-9", "run-9", 1)})
	var seen []string
	for _, event := range h.events() {
		if run, ok := event.(contracts.RunEvent); ok {
			seen = append(seen, fmt.Sprintf("%T:%v", run, RunEventBaseOf(run).Sequence))
		}
	}
	equal(t, seen, []string{"contracts.RunEventRunStarted:1", "contracts.RunEventAssistantDelta:3", "contracts.RunEventRunStatus:4", "contracts.RunEventRunStarted:1"})
}

func TestHostAgentExitFailsLiveRunsAndClearsTheirWork(t *testing.T) {
	h := newHostHarness(t, nil)
	h.bridge.HandleRunCommand(startCommand("task-1", "run-1", nil))
	agent := h.agent(t)
	agent.say(contracts.RunEventRunStarted{RunEventBase: base("task-1", "run-1", 1)})
	agent.say(contracts.BackgroundEvent{TaskID: "task-1", BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{shell("bash-1")}}})
	agent.say(contracts.SubagentEventSubagentStarted{TaskID: "task-1", ID: "child-live", Description: "Inspect", SessionScoped: ptr(true)})
	agent.say(contracts.SubagentEventSubagentStarted{TaskID: "task-1", ID: "child-idle", Description: "Review", SessionScoped: ptr(true)})
	agent.say(contracts.SubagentEventSubagentStatus{TaskID: "task-1", ID: "child-idle", Status: domain.SubagentLiveStatusIdle})
	code := 9
	agent.listener.Exit(&code)
	equal(t, h.statuses("run-1"), []domain.RunStatus{domain.RunStatusFailed})
	equal(t, h.lastMessage(), "Agent process exited with code 9.")
	var background []int
	var finished []string
	for _, event := range h.events() {
		switch event := event.(type) {
		case contracts.BackgroundEvent:
			background = append(background, len(event.Processes))
		case contracts.SubagentEventSubagentFinished:
			finished = append(finished, event.ID)
		}
	}
	equal(t, background, []int{1, 0})
	equal(t, finished, []string{"child-live"}, "only the child whose turn was live is settled by the crash")
}

func TestHostStopThatCannotReachAgentStillStops(t *testing.T) {
	h := newHostHarness(t, nil)
	for _, runID := range []string{"run-1", "run-2"} {
		h.bridge.HandleRunCommand(startCommand("task-"+runID, runID, nil))
		agent := h.agent(t)
		eventually(t, func() bool {
			for _, message := range agent.messages() {
				if describe(message) == "start:"+runID {
					return true
				}
			}
			return false
		})
		agent.say(contracts.RunEventRunStarted{RunEventBase: base("task-"+runID, runID, 1)})
	}
	agent := h.agent(t)
	agent.mu.Lock()
	agent.unreachable = true
	agent.mu.Unlock()
	h.bridge.HandleRunCommand(contracts.CancelRunCommand{TaskID: "task-run-1", RunID: "run-1"})
	h.bridge.HandleRunCommand(contracts.SteerRunCommand{TaskID: "task-run-2", RunID: "run-2", MessageID: "m1", Prompt: "also"})
	equal(t, h.statuses("run-1"), []domain.RunStatus{domain.RunStatusCancelled})
	equal(t, h.statuses("run-2"), []domain.RunStatus{domain.RunStatusFailed})
	equal(t, h.lastMessage(), "Agent process is unavailable.")
}

func TestHostScheduledTickWaitsForItsRun(t *testing.T) {
	h := newHostHarness(t, nil)
	automation := &domain.Automation{ID: "automation-1", TaskID: "task-1", Prompt: "check", Schedule: "* * * * *", RunCount: 3}
	lastFire := func() contracts.AutomationFire {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.fires[len(h.fires)-1]
	}
	fired := func(count int) func() bool {
		return func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.fires) == count }
	}
	skipped := make(chan domain.AutomationRunStatus, 1)
	go func() {
		skipped <- h.bridge.DispatchAutomation(context.Background(), automation, domain.TickKind{Unattended: true})
	}()
	eventually(t, fired(1))
	h.bridge.AcknowledgeAutomation(lastFire().RunID, false)
	equal(t, <-skipped, domain.AutomationRunStatusSkipped)

	settled := make(chan domain.AutomationRunStatus, 1)
	go func() {
		settled <- h.bridge.DispatchAutomation(context.Background(), automation, domain.TickKind{Unattended: true})
	}()
	eventually(t, fired(2))
	fire := lastFire()
	equal(t, fire.RunNumber, 4.0)
	equal(t, *fire.Unattended, true)
	h.bridge.AcknowledgeAutomation(fire.RunID, true)
	h.bridge.HandleRunCommand(startCommand("task-1", fire.RunID, nil))
	agent := h.agent(t)
	agent.say(contracts.RunEventRunStarted{RunEventBase: base("task-1", fire.RunID, 1)})
	agent.say(contracts.RunEventRunStatus{RunEventBase: base("task-1", fire.RunID, 2), Status: domain.RunStatusSucceeded})
	equal(t, <-settled, domain.AutomationRunStatusSucceeded)
}

func TestHostUnreadableRequestIsRefused(t *testing.T) {
	h := newHostHarness(t, nil)
	h.bridge.HandleRunCommand(startCommand("task-1", "run-1", nil))
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 1 })
	agent.say(map[string]any{"type": "thread.request", "requestId": "malformed", "taskId": "task-1", "op": "list", "limit": -1})
	agent.say(map[string]any{"type": "nonsense"})
	equal(t, agent.messages()[1:], []AgentMessage{contracts.ThreadResponseFalse{RequestID: "malformed", Message: "AI Coding Tool could not read that request: one of its fields is not what the request allows."}})
	h.bridge.KillAgent()
	equal(t, agent.killed, true)
}

func TestHostHandlesARealMonitoredStop(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	healthy := exec.Command("/bin/sleep", "30")
	hog := exec.Command(os.Args[0], "-test.run=^TestHelperHog$")
	hog.Env = append(os.Environ(), "AGENT_TEST_HOG=1")
	out, _ := hog.StdoutPipe()
	if err := healthy.Start(); err != nil {
		t.Fatal(err)
	}
	if err := hog.Start(); err != nil {
		t.Fatal(err)
	}
	defer healthy.Process.Kill()
	defer hog.Process.Kill()
	ready := make([]byte, 32)
	out.Read(ready)

	var nowMu sync.Mutex
	var now float64
	var monitor *SessionResourceMonitor
	pending := make(chan struct{})
	h := newHostHarness(t, func(config *RunHostConfig) {
		config.ResourceMonitor = func(options ResourceMonitorOptions) *SessionResourceMonitor {
			options.manual = true
			options.Now = func() float64 { nowMu.Lock(); defer nowMu.Unlock(); return now }
			options.Memory = func(context.Context) SystemMemory { return SystemMemory{Total: 4 * gibibyte, Available: 0} }
			monitor = NewSessionResourceMonitor(options)
			return monitor
		}
	})
	h.resolve = func(id string) (workspace.WorkspaceResolution, error) {
		if id == "pending-workspace" {
			<-pending
		}
		return workspace.WorkspaceResolution{Status: workspace.ResolutionAvailable, Workspace: workspace.WorkspaceRecord{ID: id, Root: "/tmp/project", Kind: workspace.WorkspaceProject}}, nil
	}
	h.bridge.Submit(startCommand("a", "run-a", nil))
	h.bridge.Submit(startCommand("b", "run-b", nil))
	agent := h.agent(t)
	eventually(t, func() bool { return len(agent.messages()) == 2 })
	for _, taskID := range []string{"a", "b"} {
		agent.say(contracts.RunEventRunStarted{RunEventBase: base(taskID, "run-"+taskID, 1)})
	}
	agent.say(contracts.ThreadRequestList{RequestID: "pending-request", TaskID: "a"})
	h.bridge.Submit(startCommand("a", "pending-run", func(command *contracts.StartRunCommand) { command.WorkspaceID = "pending-workspace" }))
	agent.say(contracts.SessionProcessEvent{TaskID: "a", SessionID: "generation-a", Pid: float64(hog.Process.Pid)})
	agent.say(contracts.SessionProcessEvent{TaskID: "b", SessionID: "generation-b", Pid: float64(healthy.Process.Pid)})
	ctx := context.Background()
	monitor.Sample(ctx)
	equal(t, h.statuses("run-a"), []domain.RunStatus{domain.RunStatusCancelled})
	equal(t, h.statuses("pending-run"), []domain.RunStatus{domain.RunStatusCancelled})
	equal(t, h.statuses("run-b"), []domain.RunStatus(nil))
	stopped := func(cleanup domain.ResourceStopCleanup) bool {
		for _, event := range h.events() {
			if stop, ok := event.(contracts.ResourceStoppedEvent); ok && stop.TaskID == "a" && stop.Stop.Cleanup == cleanup {
				return true
			}
		}
		return false
	}
	equal(t, stopped(domain.ResourceStopCleanupStopping), true)
	terminated, refused := false, false
	for _, message := range agent.messages() {
		if command, ok := message.(contracts.TerminateSessionCommand); ok && command.SessionID == "generation-a" {
			terminated = true
		}
		if response, ok := message.(contracts.ThreadResponseFalse); ok && response.RequestID == "pending-request" {
			refused = true
		}
	}
	equal(t, terminated && refused, true)
	beforeLate := len(h.events())
	agent.say(contracts.RunEventAssistantDelta{RunEventBase: base("a", "run-a", 2), MessageID: "late", Text: "late output"})
	agent.say(contracts.BackgroundEvent{TaskID: "a", BackgroundReport: contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{shell("late")}}})
	equal(t, len(h.events()), beforeLate)
	close(pending)
	time.Sleep(20 * time.Millisecond)
	for _, message := range agent.messages() {
		if describe(message) == "start:pending-run" {
			t.Fatal("a start dropped by the stop went out")
		}
	}
	exited := make(chan struct{})
	go func() { hog.Wait(); close(exited) }()
	nowMu.Lock()
	now = 6000
	nowMu.Unlock()
	monitor.Sample(ctx)
	<-exited
	monitor.Sample(ctx)
	equal(t, stopped(domain.ResourceStopCleanupComplete), true)
	equal(t, platform.Alive(healthy.Process.Pid), true)
	h.bridge.Submit(startCommand("a", "resumed-run", func(command *contracts.StartRunCommand) {
		command.Continuation = &domain.Continuation{Provider: "claude", Value: "saved-session"}
	}))
	eventually(t, func() bool {
		for _, message := range agent.messages() {
			if command, ok := message.(contracts.InternalStartRunCommand); ok && command.RunID == "resumed-run" && command.Continuation.Value == "saved-session" {
				return true
			}
		}
		return false
	})
}
