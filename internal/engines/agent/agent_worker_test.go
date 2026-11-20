package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type fakeModule struct {
	provider func(ProviderContext) (EngineProvider, error)
}

func (m fakeModule) Binary() EngineCommand    { return EngineCommand{Command: "fake"} }
func (m fakeModule) Services() EngineServices { return stubServices{} }
func (m fakeModule) CreateProvider(_ context.Context, context ProviderContext) (EngineProvider, error) {
	return m.provider(context)
}

type poolProvider struct {
	*fakeProvider
	pool   *SessionPool
	closed *sync.WaitGroup
}

func (p poolProvider) CloseAll() { p.pool.CloseAll(); p.closed.Done() }

type workerHarness struct {
	mu       sync.Mutex
	messages []any
	exit     chan int
	agent    AgentProcess
}

func (h *workerHarness) received() []any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]any(nil), h.messages...)
}

func startWorker(t *testing.T, module Engine) *workerHarness {
	h := &workerHarness{exit: make(chan int, 1)}
	start := InProcessAgent(WorkerConfig{Engines: map[domain.AgentEngine]Engine{domain.AgentEngineClaude: module}})
	h.agent = start(AgentProcessListener{
		Message: func(message any) { h.mu.Lock(); h.messages = append(h.messages, message); h.mu.Unlock() },
		Exit:    func(code *int) { h.exit <- *code },
	})
	return h
}

func TestWorkerRunsAStartAndReportsBack(t *testing.T) {
	provider := &fakeProvider{}
	closed := &sync.WaitGroup{}
	closed.Add(2)
	h := startWorker(t, fakeModule{provider: func(context ProviderContext) (EngineProvider, error) {
		return poolProvider{fakeProvider: provider, pool: context.Pool, closed: closed}, nil
	}})
	h.agent.Post(baseStart("task", "run"))
	run := provider.run(t, 0)
	equal(t, run.input.Automations != nil && run.input.Threads != nil && run.input.Browser != nil, true)
	result := make(chan contracts.ThreadTranscript, 1)
	go func() {
		transcript, _ := run.input.Threads.Read(context.Background(), "other", nil, ptr("this"))
		result <- transcript
	}()
	var request contracts.ThreadRequestRead
	eventually(t, func() bool {
		for _, message := range h.received() {
			if read, ok := message.(contracts.ThreadRequestRead); ok {
				request = read
				return true
			}
		}
		return false
	})
	equal(t, request.TaskID, "task")
	equal(t, request.ThreadID, "other")
	h.agent.Post(contracts.ThreadResponseTrue{RequestID: request.RequestID, Result: []byte(`{"thread":{"id":"other"},"messages":[]}`)})
	transcript := <-result
	equal(t, transcript.Thread.ID, "other")
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	h.agent.Post(contracts.ReloadAgentSettingsCommand{})
	eventually(t, func() bool {
		count := 0
		for _, message := range h.received() {
			if reload, ok := message.(contracts.AgentSettingsReloadEvent); ok && (reload.Status == "pending" || reload.Status == "reloaded") {
				count++
			}
		}
		return count == 2
	})
	h.agent.Kill()
	equal(t, <-h.exit, 0)
	closed.Wait()
	var statuses []domain.RunStatus
	for _, message := range h.received() {
		if status, ok := message.(contracts.RunEventRunStatus); ok {
			statuses = append(statuses, status.Status)
		}
	}
	equal(t, statuses, []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusSucceeded})
}

func TestWorkerThatCannotStartEndsAsACrash(t *testing.T) {
	h := startWorker(t, fakeModule{provider: func(ProviderContext) (EngineProvider, error) {
		return nil, errors.New("no binary")
	}})
	equal(t, <-h.exit, 1)
	h.agent.Post(baseStart("task", "run"))
	equal(t, len(h.received()), 0)
}

func TestWorkerAndHostTogether(t *testing.T) {
	provider := &fakeProvider{}
	closed := &sync.WaitGroup{}
	closed.Add(2)
	h := newHostHarness(t, func(config *RunHostConfig) {
		config.Agent = InProcessAgent(WorkerConfig{Engines: map[domain.AgentEngine]Engine{domain.AgentEngineClaude: fakeModule{provider: func(context ProviderContext) (EngineProvider, error) {
			return poolProvider{fakeProvider: provider, pool: context.Pool, closed: closed}, nil
		}}}})
	})
	h.bridge.Submit(startCommand("task-1", "run-1", nil))
	run := provider.run(t, 0)
	run.input.Emit(EventAssistant{MessageID: "m", Text: "hello"})
	run.resolve(ProviderResult{Status: domain.RunStatusSucceeded})
	eventually(t, func() bool { return len(h.statuses("run-1")) == 2 })
	equal(t, h.statuses("run-1"), []domain.RunStatus{domain.RunStatusRunning, domain.RunStatusSucceeded})
	h.bridge.KillAgent()
	closed.Wait()
}
