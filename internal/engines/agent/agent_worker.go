package agent

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// Everything the host says to the agent side: InternalRunCommand, AutomationResponse, ThreadResponse,
// or TerminateSessionCommand.
type AgentMessage any

// The host's end of one agent worker.
type AgentProcess interface {
	Post(message AgentMessage)
	Kill()
}

// What the host hears back. Exit is reported once, whatever ended the worker.
type AgentProcessListener struct {
	// One of AgentEvent, AutomationRequest, ThreadRequest or SessionResourceMessage.
	Message func(message any)
	// Nil code: ended by a signal (never used in-process); 0: killed; 1: crashed.
	Exit func(code *int)
}

// Starts one agent worker. The run host holds at most one at a time.
type StartAgentProcess func(listener AgentProcessListener) AgentProcess

// What the in-process worker is built with.
type WorkerConfig struct {
	Engines map[domain.AgentEngine]Engine
	// Serves the app's tools to every session of the worker.
	Tools           ToolHost
	GeneratedImages string
	PluginRoot      string
	// Nil means workspace.IsWritePathInside.
	IsWritePathInside func(ctx context.Context, root, candidate string) bool
	// Zero means the default.
	IdleSession time.Duration
}

// The agent worker, run in-process: the Go port of agent-worker.mts. Messages each way pass through a
// queue, as they did over the process boundary, and a panic while handling one ends the worker, its
// sessions with it, the way a crash of the forked process did, instead of the app.
func InProcessAgent(config WorkerConfig) StartAgentProcess {
	return func(listener AgentProcessListener) AgentProcess {
		worker := &worker{config: config, listener: listener, inbox: newQueue[AgentMessage](), outbox: newQueue[any]()}
		go worker.deliver()
		go worker.serve()
		return worker
	}
}

type worker struct {
	config   WorkerConfig
	listener AgentProcessListener
	inbox    *queue[AgentMessage]
	outbox   *queue[any]

	pools        []*SessionPool
	providers    map[contracts.RunChannel]*EngineRouter
	coordinators map[contracts.RunChannel]*RunCoordinator
	automations  *AutomationChannel
	threads      *ThreadChannel

	exitOnce  sync.Once
	closeOnce sync.Once
}

func (worker *worker) Post(message AgentMessage) { worker.inbox.push(message) }

func (worker *worker) Kill() {
	go worker.exit(0)
}

func (worker *worker) send(message any) { worker.outbox.push(message) }

func (worker *worker) deliver() {
	for {
		message, ok := worker.outbox.pop()
		if !ok {
			return
		}
		if notice, ended := message.(exitNotice); ended {
			if worker.listener.Exit != nil {
				code := notice.code
				worker.listener.Exit(&code)
			}
			continue
		}
		worker.listener.Message(message)
	}
}

// Ends the worker once: its sessions close, nothing more is read, and the host hears the code after
// every message sent before it.
func (worker *worker) exit(code int) {
	worker.exitOnce.Do(func() {
		worker.closeSessions()
		worker.inbox.close()
		worker.outbox.push(exitNotice{code})
		worker.outbox.close()
	})
}

type exitNotice struct{ code int }

func (worker *worker) closeSessions() {
	worker.closeOnce.Do(func() {
		for _, router := range worker.providers {
			router.CloseAll()
		}
		if closer, ok := worker.config.Tools.(interface{ Close() }); ok {
			closer.Close()
		}
	})
}

func (worker *worker) serve() {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("Agent worker crashed: %v\n%s", recovered, debug.Stack())
			worker.exit(1)
		}
	}()
	worker.boot()
	for {
		message, ok := worker.inbox.pop()
		if !ok {
			return
		}
		worker.handle(message)
	}
}

func (worker *worker) boot() {
	report := func(message contracts.SessionResourceMessage) { worker.send(message) }
	post := func(message any) error { worker.send(message); return nil }
	worker.automations = NewAutomationChannel(func(request contracts.AutomationRequest) error { return post(request) }, 0)
	worker.threads = NewThreadChannel(func(request contracts.ThreadRequest) error { return post(request) }, 0, 0)
	isWritePathInside := worker.config.IsWritePathInside
	if isWritePathInside == nil {
		isWritePathInside = func(_ context.Context, root, candidate string) bool {
			return workspace.IsWritePathInside(root, candidate)
		}
	}
	// Both channels get the same tools; a side chat's automations are retired when its thread closes.
	options := CoordinatorOptions{
		IsWritePathInside: isWritePathInside,
		Automations:       worker.automations.BridgeFor,
		Findings:          worker.threads.FindingsFor,
		Threads:           worker.threads.BridgeFor,
		Coordination:      worker.threads.CoordinationFor,
		Browser:           worker.threads.BrowserFor,
		Simulator:         worker.threads.SimulatorFor,
		Terminal:          worker.threads.TerminalFor,
	}
	worker.providers = map[contracts.RunChannel]*EngineRouter{}
	worker.coordinators = map[contracts.RunChannel]*RunCoordinator{}
	// A channel's engines share one pool, so the warm sessions of a channel are capped together.
	for _, channel := range []contracts.RunChannel{contracts.RunChannelMain, contracts.RunChannelSide} {
		pool := NewSessionPool(report, worker.config.IdleSession)
		worker.pools = append(worker.pools, pool)
		providers := map[domain.AgentEngine]EngineProvider{}
		for _, engine := range domain.AgentEngines {
			module := worker.config.Engines[engine]
			if module == nil {
				continue
			}
			provider, err := module.CreateProvider(context.Background(), ProviderContext{Host: worker.config.Tools, Pool: pool, GeneratedImages: worker.config.GeneratedImages, PluginRoot: worker.config.PluginRoot})
			if err != nil {
				panic(fmt.Errorf("could not start the %s engine: %w", engine, err))
			}
			providers[engine] = provider
		}
		worker.providers[channel] = NewEngineRouter(providers)
		worker.coordinators[channel] = NewRunCoordinator(worker.providers[channel], func(event contracts.AgentEvent) { worker.send(event) }, options)
	}
}

func (worker *worker) reloadSettings() {
	reloaded := make([]<-chan struct{}, 0, len(worker.pools))
	for _, pool := range worker.pools {
		reloaded = append(reloaded, pool.ReloadSettings())
	}
	worker.send(contracts.AgentSettingsReloadEvent{Status: contracts.AgentSettingsReloadEventStatusPending})
	go func() {
		for _, done := range reloaded {
			<-done
		}
		worker.send(contracts.AgentSettingsReloadEvent{Status: contracts.AgentSettingsReloadEventStatusReloaded})
	}()
}

// A command names a thread rather than a channel, so it is offered to each until one owns the run.
func (worker *worker) whichever(act func(coordinator *RunCoordinator) bool) {
	for _, channel := range []contracts.RunChannel{contracts.RunChannelMain, contracts.RunChannelSide} {
		if act(worker.coordinators[channel]) {
			return
		}
	}
}

func (worker *worker) handle(message AgentMessage) {
	switch message := message.(type) {
	case contracts.TerminateSessionCommand:
		for _, pool := range worker.pools {
			pool.Terminate(message.TaskID, message.SessionID, message.IdleOnly != nil && *message.IdleOnly)
		}
	case contracts.AutomationResponse:
		worker.automations.Settle(message)
	case contracts.ThreadResponse:
		worker.threads.Settle(message)
	case contracts.RunCommandMemoryProtection:
	case contracts.ReloadAgentSettingsCommand:
		worker.reloadSettings()
	case contracts.InternalStartRunCommand:
		if coordinator := worker.coordinators[message.Channel]; coordinator != nil {
			coordinator.Start(message)
		}
	case contracts.CancelRunCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool { return coordinator.Cancel(message.TaskID, message.RunID) })
	case contracts.AnswerQuestionCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool {
			return coordinator.AnswerQuestion(message.TaskID, message.RunID, message.RequestID, message.QuestionID, message.Text)
		})
	case contracts.SteerRunCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool {
			return coordinator.Steer(message.TaskID, message.RunID, message.MessageID, message.Prompt)
		})
	case contracts.StopProcessCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool {
			return coordinator.StopProcess(message.TaskID, message.ProcessID)
		})
	case contracts.LabelThreadCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool { return coordinator.LabelThread(message.TaskID, message.Title) })
	case contracts.ArchiveThreadCommand:
		if coordinator := worker.coordinators[message.Channel]; coordinator != nil {
			coordinator.ArchiveThread(message.TaskID, message.Continuation, message.Archived)
		}
	case contracts.ThreadRoleCommand:
		for _, channel := range []contracts.RunChannel{contracts.RunChannelMain, contracts.RunChannelSide} {
			worker.coordinators[channel].RetireOutOfRole(message.TaskID, message.CoordinationRole)
		}
	case contracts.ApprovalDecisionCommand:
		worker.whichever(func(coordinator *RunCoordinator) bool {
			return coordinator.DecideApproval(message.TaskID, message.RunID, message.ApprovalID, message.Allow)
		})
	}
}

// An unbounded FIFO between goroutines, standing in for a process's message port.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	wake   chan struct{}
	closed bool
}

func newQueue[T any]() *queue[T] { return &queue[T]{wake: make(chan struct{}, 1)} }

func (q *queue[T]) push(item T) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.items = append(q.items, item)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Drains what was pushed before close, then answers false.
func (q *queue[T]) pop() (T, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			var zero T
			q.items[0] = zero
			q.items = q.items[1:]
			q.mu.Unlock()
			return item, true
		}
		if q.closed {
			q.mu.Unlock()
			var zero T
			return zero, false
		}
		q.mu.Unlock()
		<-q.wake
	}
}
