package agent

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

func start(taskID, runID string) contracts.StartRunCommand {
	return contracts.StartRunCommand{Channel: contracts.RunChannelMain, TaskID: taskID, RunID: runID, Title: "T", WorkspaceID: "w", Policy: domain.ExecutionPolicyConfirm, Engine: domain.AgentEngineClaude, Model: "sonnet", Effort: domain.AgentEffortHigh}
}

func TestLedgerNewStartSupersedesOlderPendingStartsOfTheThread(t *testing.T) {
	ledger := NewRunLedger()
	ledger.BeginStart(start("t", "old"))
	ledger.BeginStart(start("other", "x"))
	ledger.BeginStart(start("t", "new"))
	ledger.BeginStart(start("t", "older"))
	var superseded []string
	for _, command := range ledger.SupersedeStarts("t", "new") {
		superseded = append(superseded, command.RunID)
	}
	equal(t, superseded, []string{"old", "older"})
	equal(t, ledger.PendingStart("t", "new") != nil && ledger.PendingStart("other", "x") != nil, true)
	equal(t, ledger.PendingStart("t", "old"), (*contracts.StartRunCommand)(nil))
}

func TestLedgerTerminalEventsCloseTheGate(t *testing.T) {
	state := &RunEventGateState{}
	base := func(sequence float64) contracts.RunEventBase {
		return contracts.RunEventBase{TaskID: "t", RunID: "r", Sequence: sequence}
	}
	equal(t, AcceptRunEvent(state, contracts.RunEventRunStarted{RunEventBase: base(1)}), true)
	equal(t, AcceptRunEvent(state, contracts.RunEventRunStatus{RunEventBase: base(2), Status: domain.RunStatusFailed}), true)
	equal(t, *state, RunEventGateState{LastSequence: 2, Terminal: true})
	equal(t, AcceptRunEvent(state, contracts.RunEventAssistantDelta{RunEventBase: base(3)}), false)
	equal(t, AcceptRunEvent(state, contracts.RunEventRunStatus{RunEventBase: base(2), Status: domain.RunStatusFailed}), false)
}

func TestLedgerTransportLossFailsOnlyOpenRuns(t *testing.T) {
	events := FailedEventsForTransportLoss([]*CorrelatedRunState{
		{TaskID: "task-1", RunID: "run-1", RunEventGateState: RunEventGateState{LastSequence: 3}},
		{TaskID: "task-2", RunID: "run-2", RunEventGateState: RunEventGateState{LastSequence: 8, Terminal: true}},
	}, "Agent process exited with code 1.")
	equal(t, events, []contracts.RunEvent{contracts.RunEventRunStatus{
		RunEventBase: contracts.RunEventBase{TaskID: "task-1", RunID: "run-1", Sequence: 4},
		Status:       domain.RunStatusFailed,
		Message:      ptr("Agent process exited with code 1."),
	}})
}

func TestSettledWithinCallsANeverSettledRunFailed(t *testing.T) {
	settled := make(chan domain.AutomationRunStatus, 1)
	settled <- domain.AutomationRunStatusSucceeded
	equal(t, SettledWithin(settled, 10*time.Second), domain.AutomationRunStatusSucceeded)
	equal(t, AutomationSettleTimeout >= time.Hour, true)
	equal(t, SettledWithin(make(chan domain.AutomationRunStatus), 5*time.Millisecond), domain.AutomationRunStatusFailed)
}

const gibibyte = 1024 * 1024 * 1024

func sampleRow(pid, parent int, bytes float64, born string) platform.ProcessSample {
	if born == "" {
		born = "Fri Nov 13 12:00:00 2026"
	}
	return platform.ProcessSample{PID: pid, Parent: parent, Bytes: bytes, Born: born}
}

type monitorLog struct {
	mu      sync.Mutex
	stopped []string
	changes []string
	signals []string
}

func (l *monitorLog) add(list *[]string, entry string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*list = append(*list, entry)
}

func (l *monitorLog) get(list *[]string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), *list...)
}

func signalName(signal platform.Signal) string {
	if signal == platform.Kill {
		return "SIGKILL"
	}
	return "SIGTERM"
}

func TestMonitorStoppedOwnerIncludesReparentedChildrenAndCleanupIsVerified(t *testing.T) {
	var now float64
	var rowsMu sync.Mutex
	rows := []platform.ProcessSample{sampleRow(101, 1, gibibyte/2, ""), sampleRow(102, 101, gibibyte/2, ""), sampleRow(201, 1, 1, "")}
	log := &monitorLog{}
	monitor := NewSessionResourceMonitor(ResourceMonitorOptions{
		manual: true,
		Read: func(context.Context) ([]platform.ProcessSample, error) {
			rowsMu.Lock()
			defer rowsMu.Unlock()
			return rows, nil
		},
		Now: func() float64 { return now },
		Memory: func(context.Context) SystemMemory {
			return SystemMemory{Total: 4 * gibibyte, Available: 300 * 1024 * 1024}
		},
		Stop:    func(taskID, _ string, _ domain.ResourceStop) { log.add(&log.stopped, taskID) },
		Changed: func(taskID string, stop domain.ResourceStop) { log.add(&log.changes, taskID+":"+string(stop.Cleanup)) },
		Signal:  func(pid int, signal platform.Signal) { log.add(&log.signals, strconv.Itoa(pid)+":"+signalName(signal)) },
	})
	defer monitor.Close()
	ctx := context.Background()
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "a", SessionID: "one", Pid: 101})
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "b", SessionID: "two", Pid: 201})
	for now = 0; now <= 15000; now += 5000 {
		monitor.Sample(ctx)
	}
	equal(t, log.get(&log.stopped), []string{"a"})
	rowsMu.Lock()
	rows = []platform.ProcessSample{sampleRow(101, 1, 1, "Fri Nov 13 12:01:00 2026"), sampleRow(102, 1, gibibyte/2, ""), sampleRow(201, 1, 1, "")}
	rowsMu.Unlock()
	now = 20000
	monitor.Sample(ctx)
	equal(t, log.get(&log.signals), []string{"102:SIGKILL"})
	rowsMu.Lock()
	rows = []platform.ProcessSample{sampleRow(101, 1, 1, "Fri Nov 13 12:01:00 2026"), sampleRow(201, 1, 1, "")}
	rowsMu.Unlock()
	now = 25000
	monitor.Sample(ctx)
	equal(t, log.get(&log.changes), []string{"a:complete"})
	equal(t, log.get(&log.stopped), []string{"a"})
}

func TestMonitorEnablingRestartsWindowAndStopsOneOwnerAtATime(t *testing.T) {
	var now float64
	log := &monitorLog{}
	monitor := NewSessionResourceMonitor(ResourceMonitorOptions{
		manual: true,
		Read: func(context.Context) ([]platform.ProcessSample, error) {
			return []platform.ProcessSample{sampleRow(101, 1, gibibyte, ""), sampleRow(201, 1, gibibyte/2, "")}, nil
		},
		Now: func() float64 { return now },
		Memory: func(context.Context) SystemMemory {
			return SystemMemory{Total: 4 * gibibyte, Available: 300 * 1024 * 1024}
		},
		Stop:    func(taskID, _ string, _ domain.ResourceStop) { log.add(&log.stopped, taskID) },
		Changed: func(string, domain.ResourceStop) {},
		Signal:  func(int, platform.Signal) {},
	})
	defer monitor.Close()
	ctx := context.Background()
	monitor.SetEnabled(false)
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "a", SessionID: "a", Pid: 101})
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "b", SessionID: "b", Pid: 201})
	for now = 0; now <= 20000; now += 5000 {
		monitor.Sample(ctx)
	}
	monitor.SetEnabled(true)
	monitor.Sample(ctx)
	equal(t, log.get(&log.stopped), []string(nil))
	for i := 0; i < 3; i++ {
		now += 5000
		monitor.Sample(ctx)
	}
	equal(t, log.get(&log.stopped), []string{"a"})
	now += 5000
	monitor.Sample(ctx)
	equal(t, log.get(&log.stopped), []string{"a"}, "do not stop a second owner while waiting for cleanup")
	now += 15000
	monitor.Sample(ctx)
	equal(t, log.get(&log.stopped), []string{"a", "b"}, "an owner that cannot be killed stops deferring")
}

func TestMonitorJournalCleansOnlyMatchingOldProcesses(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "processes.json")
	var now float64
	var rowsMu sync.Mutex
	rows := []platform.ProcessSample{sampleRow(101, 1, 1, ""), sampleRow(102, 101, 1, "")}
	var signalsMu sync.Mutex
	var signals []int
	options := ResourceMonitorOptions{
		manual:  true,
		Journal: journal,
		Read: func(context.Context) ([]platform.ProcessSample, error) {
			rowsMu.Lock()
			defer rowsMu.Unlock()
			return rows, nil
		},
		Now:     func() float64 { return now },
		Memory:  func(context.Context) SystemMemory { return SystemMemory{Total: 4 * gibibyte, Available: gibibyte} },
		Stop:    func(string, string, domain.ResourceStop) {},
		Changed: func(string, domain.ResourceStop) {},
		Signal: func(pid int, _ platform.Signal) {
			signalsMu.Lock()
			signals = append(signals, pid)
			signalsMu.Unlock()
		},
	}
	ctx := context.Background()
	first := NewSessionResourceMonitor(options)
	first.Receive(contracts.SessionProcessEvent{TaskID: "a", SessionID: "old", Pid: 101})
	first.Sample(ctx)
	first.Close()
	data, _ := os.ReadFile(journal)
	equal(t, strings.Contains(string(data), "old"), true)
	rowsMu.Lock()
	rows = []platform.ProcessSample{sampleRow(101, 1, 1, "Fri Nov 13 12:01:00 2026"), sampleRow(102, 1, 1, "")}
	rowsMu.Unlock()
	second := NewSessionResourceMonitor(options)
	defer second.Close()
	second.SetEnabled(false)
	second.Sample(ctx)
	now = 6000
	second.Sample(ctx)
	signalsMu.Lock()
	equal(t, signals, []int{102})
	signalsMu.Unlock()
	rowsMu.Lock()
	rows = nil
	rowsMu.Unlock()
	second.Sample(ctx)
	if err := second.BeforeStart(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(journal)
	equal(t, string(data), "[]")
}

func TestMonitorResumeWaitsThroughTransientSamplingFailure(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "processes.json")
	os.WriteFile(journal, []byte(`[{"taskId":"a","sessionId":"old","known":[[101,"Fri Nov 13 12:00:00 2026"]]}]`), 0o600)
	var reads int
	var mu sync.Mutex
	monitor := NewSessionResourceMonitor(ResourceMonitorOptions{
		manual:     true,
		Journal:    journal,
		RetryDelay: time.Millisecond,
		Read: func(context.Context) ([]platform.ProcessSample, error) {
			mu.Lock()
			defer mu.Unlock()
			reads++
			if reads == 1 {
				return nil, os.ErrDeadlineExceeded
			}
			return nil, nil
		},
		Memory:  func(context.Context) SystemMemory { return SystemMemory{Total: 4 * gibibyte, Available: gibibyte} },
		Stop:    func(string, string, domain.ResourceStop) {},
		Changed: func(string, domain.ResourceStop) {},
	})
	defer monitor.Close()
	if err := monitor.BeforeStart(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	equal(t, reads >= 2, true)
	mu.Unlock()
}

func TestMonitorRealSamplingKillsRunawaySubtreeOnly(t *testing.T) {
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
	defer func() {
		healthy.Process.Kill()
		hog.Process.Kill()
	}()
	buffer := make([]byte, 32)
	n, _ := out.Read(buffer)
	descendant, _ := strconv.Atoi(strings.TrimSpace(string(buffer[:n])))
	defer syscall.Kill(descendant, syscall.SIGKILL)
	var now float64
	log := &monitorLog{}
	monitor := NewSessionResourceMonitor(ResourceMonitorOptions{
		manual:  true,
		Now:     func() float64 { return now },
		Memory:  func(context.Context) SystemMemory { return SystemMemory{Total: 4 * gibibyte, Available: 0} },
		Stop:    func(taskID, _ string, _ domain.ResourceStop) { log.add(&log.stopped, taskID) },
		Changed: func(taskID string, stop domain.ResourceStop) { log.add(&log.changes, taskID+":"+string(stop.Cleanup)) },
	})
	defer monitor.Close()
	ctx := context.Background()
	// Only the sampled bytes decide which owner is largest; the hog's subtree is two processes.
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "runaway", SessionID: "hog", Pid: float64(hog.Process.Pid)})
	monitor.Receive(contracts.SessionProcessEvent{TaskID: "healthy", SessionID: "healthy", Pid: float64(healthy.Process.Pid)})
	monitor.Sample(ctx)
	equal(t, log.get(&log.stopped), []string{"runaway"})
	exited := make(chan struct{})
	go func() { hog.Wait(); close(exited) }()
	now = 6000
	monitor.Sample(ctx)
	<-exited
	eventually(t, func() bool { return !platform.Alive(descendant) || zombie(descendant) })
	monitor.Sample(ctx)
	equal(t, log.get(&log.changes), []string{"runaway:complete"})
	equal(t, platform.Alive(healthy.Process.Pid), true)
}

// Holds 96 MiB and a child, ignores SIGTERM, and prints the child's pid: a runaway session's subtree.
func TestHelperHog(t *testing.T) {
	if os.Getenv("AGENT_TEST_HOG") != "1" {
		t.Skip("helper process")
	}
	signal.Ignore(syscall.SIGTERM)
	hold := make([]byte, 96*1024*1024)
	for index := range hold {
		hold[index] = 1
	}
	child := exec.Command("/bin/sleep", "30")
	child.Start()
	os.Stdout.WriteString(strconv.Itoa(child.Process.Pid) + "\n")
	time.Sleep(30 * time.Second)
	_ = hold[len(hold)-1]
}

// A killed child of a dead parent may linger until init reaps it; ps no longer lists it once reaped.
func zombie(pid int) bool {
	rows, err := platform.ReadProcessSamples(context.Background())
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.PID == pid {
			return false
		}
	}
	return true
}
