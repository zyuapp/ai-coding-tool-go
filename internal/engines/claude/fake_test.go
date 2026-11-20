package claude

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

// fakeQuery stands in for the agent process. A scripted one ends once its messages are taken (and,
// with endAfterSent, once that many inputs arrived); a live one stays open until closed.
type fakeQuery struct {
	options  protocol.Options
	scripted bool
	// endAfterSent holds a scripted session open until this many inputs have arrived.
	endAfterSent int
	failure      error

	mu          sync.Mutex
	queue       []protocol.Message
	wake        chan struct{}
	out         chan protocol.Message
	done        chan struct{}
	sent        []protocol.MessageContent
	priorities  []string
	closed      bool
	interrupted bool
	model       *string
	mode        string
	settings    any
	stopped     []string
	commands    []protocol.SlashCommand
	models      []protocol.ModelInfo
	usage       func(ctx context.Context) (jsontext.Value, error)
	closePanics bool
	feeding     sync.Once
}

func newFake(options protocol.Options, scripted bool, messages ...protocol.Message) *fakeQuery {
	q := &fakeQuery{options: options, scripted: scripted, queue: messages, wake: make(chan struct{}, 1), out: make(chan protocol.Message), done: make(chan struct{})}
	return q
}

func (q *fakeQuery) feed() {
	defer close(q.out)
	for {
		q.mu.Lock()
		if len(q.queue) > 0 {
			next := q.queue[0]
			q.queue = q.queue[1:]
			q.mu.Unlock()
			select {
			case q.out <- next:
				continue
			case <-q.done:
				return
			}
		}
		ended := q.scripted && len(q.sent) >= q.endAfterSent
		q.mu.Unlock()
		if ended {
			return
		}
		select {
		case <-q.wake:
		case <-q.done:
			return
		}
	}
}

func (q *fakeQuery) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// emit delivers a message as the agent process would print it.
func (q *fakeQuery) emit(t *testing.T, raw string) {
	t.Helper()
	q.mu.Lock()
	q.queue = append(q.queue, decode(t, raw))
	q.mu.Unlock()
	q.poke()
}

func decode(t *testing.T, raw string) protocol.Message {
	t.Helper()
	message, err := protocol.DecodeMessage(jsontext.Value(raw))
	if err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return message
}

func decodeAll(t *testing.T, raws ...string) []protocol.Message {
	t.Helper()
	messages := make([]protocol.Message, 0, len(raws))
	for _, raw := range raws {
		messages = append(messages, decode(t, raw))
	}
	return messages
}

func (q *fakeQuery) Messages() <-chan protocol.Message {
	q.feeding.Do(func() { go q.feed() })
	return q.out
}
func (q *fakeQuery) Err() error { return q.failure }

func (q *fakeQuery) SendUser(message protocol.UserMessage) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return protocol.ErrClosed
	}
	q.sent = append(q.sent, message.Message.Content)
	priority := ""
	if message.Priority != nil {
		priority = *message.Priority
	}
	q.priorities = append(q.priorities, priority)
	q.mu.Unlock()
	q.poke()
	return nil
}

func (q *fakeQuery) Interrupt(context.Context) error {
	q.mu.Lock()
	q.interrupted = true
	q.mu.Unlock()
	return nil
}

func (q *fakeQuery) SetModel(_ context.Context, model *string) error {
	q.mu.Lock()
	q.model = model
	q.mu.Unlock()
	return nil
}

func (q *fakeQuery) SetPermissionMode(_ context.Context, mode string) error {
	q.mu.Lock()
	q.mode = mode
	q.mu.Unlock()
	return nil
}

func (q *fakeQuery) ApplyFlagSettings(_ context.Context, settings any) error {
	q.mu.Lock()
	q.settings = settings
	q.mu.Unlock()
	return nil
}

func (q *fakeQuery) StopTask(_ context.Context, taskID string) error {
	q.mu.Lock()
	q.stopped = append(q.stopped, taskID)
	q.mu.Unlock()
	return nil
}

func (q *fakeQuery) SupportedCommands(context.Context) ([]protocol.SlashCommand, error) {
	return q.commands, nil
}

func (q *fakeQuery) SupportedModels(context.Context) ([]protocol.ModelInfo, error) {
	if q.models == nil {
		return nil, errors.New("no models")
	}
	return q.models, nil
}

func (q *fakeQuery) Usage(ctx context.Context) (jsontext.Value, error) {
	if q.usage == nil {
		return nil, errors.New("no usage")
	}
	return q.usage(ctx)
}

func (q *fakeQuery) EndInput() {}

func (q *fakeQuery) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.done)
	}
	panics := q.closePanics
	q.mu.Unlock()
	if panics {
		panic("already gone")
	}
}

func (q *fakeQuery) isClosed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

func (q *fakeQuery) sentTexts() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	texts := make([]string, 0, len(q.sent))
	for _, content := range q.sent {
		texts = append(texts, content.Text)
	}
	return texts
}

// fakes opens a fake per session and keeps every one.
type fakes struct {
	mu       sync.Mutex
	sessions []*fakeQuery
	make     func(options protocol.Options) *fakeQuery
}

func scripted(t *testing.T, raws ...string) *fakes {
	messages := decodeAll(t, raws...)
	return &fakes{make: func(options protocol.Options) *fakeQuery { return newFake(options, true, messages...) }}
}

func live() *fakes {
	return &fakes{make: func(options protocol.Options) *fakeQuery { return newFake(options, false) }}
}

func (f *fakes) factory(_ context.Context, options protocol.Options) (Query, error) {
	q := f.make(options)
	f.mu.Lock()
	f.sessions = append(f.sessions, q)
	f.mu.Unlock()
	return q, nil
}

func (f *fakes) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakes) last() *fakeQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		return nil
	}
	return f.sessions[len(f.sessions)-1]
}

func (f *fakes) options(t *testing.T) protocol.Options {
	t.Helper()
	q := f.last()
	if q == nil {
		t.Fatal("no session was opened")
	}
	return q.options
}

// recorder collects what a run reports.
type recorder struct {
	mu         sync.Mutex
	events     []agent.ProviderEvent
	workflows  []contracts.WorkflowReport
	subagents  []domain.SubagentReport
	goals      []contracts.GoalReport
	background []contracts.BackgroundReport
	intents    []domain.ToolIntent
}

func (r *recorder) emit(event agent.ProviderEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recorder) all() []agent.ProviderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.ProviderEvent(nil), r.events...)
}

func eventsOf[T agent.ProviderEvent](r *recorder) []T {
	var found []T
	for _, event := range r.all() {
		if typed, ok := event.(T); ok {
			found = append(found, typed)
		}
	}
	return found
}

func (r *recorder) workflowReports() []contracts.WorkflowReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contracts.WorkflowReport(nil), r.workflows...)
}

// neverSteered is a steering queue nothing is ever pushed into.
type neverSteered struct{}

func (neverSteered) Next(ctx context.Context) (agent.SteerMessage, bool) {
	<-ctx.Done()
	return agent.SteerMessage{}, false
}

// steerList hands out its messages, then reports the run over.
type steerList struct {
	mu       sync.Mutex
	messages []agent.SteerMessage
}

func (s *steerList) Next(context.Context) (agent.SteerMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return agent.SteerMessage{}, false
	}
	next := s.messages[0]
	s.messages = s.messages[1:]
	return next, true
}

func runInput(r *recorder, change ...func(input *agent.ProviderRunInput)) *agent.ProviderRunInput {
	signal, abort := context.WithCancel(context.Background())
	input := &agent.ProviderRunInput{
		Channel: contracts.RunChannelMain, TaskID: "task-1", Title: "Inspect the app", Prompt: "inspect the app",
		WorkspaceRoot: "/tmp/project", ComputerUse: domain.ComputerUseRunConfigUnavailable{Message: "test"},
		Policy: domain.ExecutionPolicyConfirm, Engine: domain.AgentEngineClaude, Model: "opus", Effort: "high",
		Steering: neverSteered{}, Signal: signal, Abort: abort,
		Authorize: func(context.Context, domain.ToolIntent) agent.ToolDecision { return agent.Allow },
		Emit:      r.emit,
		ReportWorkflow: func(report contracts.WorkflowReport) {
			r.mu.Lock()
			r.workflows = append(r.workflows, report)
			r.mu.Unlock()
		},
		ReportBackground: func(report contracts.BackgroundReport) {
			r.mu.Lock()
			r.background = append(r.background, report)
			r.mu.Unlock()
		},
		ReportSubagent: func(report domain.SubagentReport) {
			r.mu.Lock()
			r.subagents = append(r.subagents, report)
			r.mu.Unlock()
		},
		ReportGoal:     func(report contracts.GoalReport) { r.mu.Lock(); r.goals = append(r.goals, report); r.mu.Unlock() },
		BeginAgentTurn: func() agent.AgentTurn { return nil },
	}
	for _, apply := range change {
		apply(input)
	}
	return input
}

type outcome struct {
	result agent.ProviderResult
	err    error
}

func execute(provider *Provider, input *agent.ProviderRunInput) <-chan outcome {
	done := make(chan outcome, 1)
	go func() {
		result, err := provider.Execute(input)
		done <- outcome{result, err}
	}()
	return done
}

func await(t *testing.T, done <-chan outcome) agent.ProviderResult {
	t.Helper()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("execute: %v", got.err)
		}
		return got.result
	case <-time.After(5 * time.Second):
		t.Fatal("the run never settled")
	}
	return agent.ProviderResult{}
}

func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

const resultSuccess = `{"type":"result","subtype":"success","is_error":false,"result":"done"}`

// runTurn runs one turn on a live session: once the run's input reached the newest session, the
// messages arrive, then the turn's result.
func runTurn(t *testing.T, f *fakes, provider *Provider, input *agent.ProviderRunInput, raws ...string) agent.ProviderResult {
	t.Helper()
	before := 0
	if q := f.last(); q != nil {
		before = len(q.sentTexts())
	}
	opens := f.count()
	done := execute(provider, input)
	eventually(t, "the turn's input", func() bool {
		q := f.last()
		if q == nil {
			return false
		}
		if f.count() > opens {
			return len(q.sentTexts()) > 0
		}
		return len(q.sentTexts()) > before
	})
	q := f.last()
	for _, raw := range raws {
		q.emit(t, raw)
	}
	q.emit(t, resultSuccess)
	return await(t, done)
}

// liveTurn opens a live session and holds its turn, so the tool gate can be asked the way the agent process asks it.
func liveTurn(t *testing.T, r *recorder, change ...func(input *agent.ProviderRunInput)) (*fakes, *Provider, func()) {
	t.Helper()
	f := live()
	provider := NewProvider(nil, f.factory, "")
	done := execute(provider, runInput(r, change...))
	eventually(t, "the session opened", func() bool { q := f.last(); return q != nil && len(q.sentTexts()) > 0 })
	return f, provider, func() {
		f.last().emit(t, resultSuccess)
		await(t, done)
		provider.CloseAll()
	}
}

func useTool(t *testing.T, f *fakes, name, input, toolUseID string) *protocol.PermissionResult {
	t.Helper()
	result, err := f.options(t).CanUseTool(context.Background(), protocol.PermissionRequest{ToolName: name, Input: jsontext.Value(input), ToolUseID: toolUseID})
	if err != nil || result == nil {
		t.Fatalf("canUseTool %s: %v", name, err)
	}
	return result
}

// settle waits until the session has taken every message emitted so far.
func (q *fakeQuery) settle(t *testing.T) {
	t.Helper()
	eventually(t, "messages taken", func() bool { q.mu.Lock(); defer q.mu.Unlock(); return len(q.queue) == 0 })
	time.Sleep(20 * time.Millisecond)
}
