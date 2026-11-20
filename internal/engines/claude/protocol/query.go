package protocol

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// closeGrace is how long a closed process has to leave on its own before it is terminated.
	closeGrace = 2 * time.Second
	// killGrace is how long a terminated process has before it is killed.
	killGrace = 5 * time.Second
	// stderrDrain is how long after exit stderr has to reach its end before it is cut off.
	stderrDrain = 200 * time.Millisecond
	// stdoutDrain is how long after exit stdout has to reach its end before it is cut off, for a
	// process that left a child holding the pipe.
	stdoutDrain = 250 * time.Millisecond
)

// ErrClosed is what a control request still waiting when the query ends fails with.
var ErrClosed = errors.New("Query closed before response received")

type controlResult struct {
	response jsontext.Value
	err      error
}

// Query is one live Claude Code process.
type Query struct {
	opts      Options
	cmd       *exec.Cmd
	stdout    *os.File
	stderr    *os.File
	writer    *lineWriter
	hooks     map[string]HookCallback
	mcp       map[string]*SdkMcpServer
	ctx       context.Context
	cancelCtx context.CancelFunc

	mu             sync.Mutex
	closed         bool
	ended          bool
	pending        map[string]chan controlResult
	handling       map[string]context.CancelFunc
	latestCommands []SlashCommand
	stderrText     string
	err            error

	initDone     chan struct{}
	initResponse *InitializeResponse
	initErr      error

	exited     chan struct{}
	exitState  *os.ProcessState
	stderrDone chan struct{}
	queue      *messageQueue
	messages   chan Message
	finished   chan struct{}
}

// Start starts Claude Code with these options and sends initialize. The query closes when ctx is
// done.
func Start(ctx context.Context, opts Options) (*Query, error) {
	args, err := opts.Args()
	if err != nil {
		return nil, err
	}
	command := opts.Command
	if command == nil {
		command = exec.Command
	}
	cmd := command(opts.Executable, args...)
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	base := cmd.Env
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = opts.Environment(base)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, err
	}
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite
	if err := cmd.Start(); err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		stderrRead.Close()
		stderrWrite.Close()
		return nil, spawnError(opts.Executable, err)
	}
	stdoutWrite.Close()
	stderrWrite.Close()
	if opts.OnSpawn != nil {
		opts.OnSpawn(cmd.Process.Pid)
	}

	var manifests map[string]McpManifest
	if len(opts.SdkMcpServers) > 0 && !manifestsDisabled() {
		manifests = map[string]McpManifest{}
		for _, server := range opts.SdkMcpServers {
			manifests[server.Name] = server.Manifest()
		}
	}
	initialize, hooks := opts.initializeRequest(manifests)
	mcp := map[string]*SdkMcpServer{}
	for _, server := range opts.SdkMcpServers {
		mcp[server.Name] = server
	}
	queryCtx, cancel := context.WithCancel(context.Background())
	q := &Query{
		opts: opts, cmd: cmd, stdout: stdoutRead, stderr: stderrRead,
		writer: newLineWriter(stdin), hooks: hooks, mcp: mcp, ctx: queryCtx, cancelCtx: cancel,
		pending: map[string]chan controlResult{}, handling: map[string]context.CancelFunc{},
		initDone: make(chan struct{}), exited: make(chan struct{}), stderrDone: make(chan struct{}),
		queue: newMessageQueue(), messages: make(chan Message), finished: make(chan struct{}),
	}
	_, initResult := q.send(initialize)
	go func() {
		result := <-initResult
		if result.err == nil {
			var response InitializeResponse
			if err := unmarshal(result.response, &response); err != nil {
				result.err = fmt.Errorf("initialize: %w", err)
			} else {
				q.initResponse = &response
			}
		}
		q.initErr = result.err
		close(q.initDone)
	}()
	go q.wait()
	go q.readStderr()
	go q.read()
	go q.pump()
	if ctx != nil && ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				q.Close()
			case <-q.finished:
			}
		}()
	}
	return q, nil
}

func spawnError(executable string, err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		if _, statErr := os.Stat(executable); statErr == nil {
			return fmt.Errorf("Claude Code native binary at %s exists but failed to launch. This usually means the binary does not match this system's libc — e.g. spawning a musl-linked binary on a glibc Linux host fails because the musl dynamic loader (/lib/ld-musl-*) is missing. Specify a matching binary with options.pathToClaudeCodeExecutable.", redact(executable))
		}
		return fmt.Errorf("Claude Code native binary not found at %s. Please ensure Claude Code is installed via native installer or specify a valid path with options.pathToClaudeCodeExecutable.", redact(executable))
	}
	return fmt.Errorf("Failed to spawn Claude Code process: %s", redact(err.Error()))
}

// requestID is a control request id as the SDK makes one: 13 base-36 characters.
func requestID() string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 13)
	for index := range out {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		out[index] = digits[n.Int64()]
	}
	return string(out)
}

// send writes a control request and returns its id and where its answer arrives.
func (q *Query) send(request any) (string, <-chan controlResult) {
	answer := make(chan controlResult, 1)
	id := requestID()
	q.mu.Lock()
	if q.closed || q.ended {
		reason := ErrClosed
		if !q.closed && q.err != nil {
			reason = q.err
		}
		q.mu.Unlock()
		answer <- controlResult{err: reason}
		return id, answer
	}
	q.pending[id] = answer
	q.mu.Unlock()
	line, err := marshal(controlRequest{Type: "control_request", RequestID: id, Request: request})
	if err == nil {
		err = q.writer.write(line)
	}
	if err != nil {
		q.mu.Lock()
		delete(q.pending, id)
		q.mu.Unlock()
		answer <- controlResult{err: err}
	}
	return id, answer
}

// Request sends a control request once initialize has been written and returns the body of its
// answer. A request whose ctx ends first is cancelled.
func (q *Query) Request(ctx context.Context, request any) (jsontext.Value, error) {
	id, answer := q.send(request)
	select {
	case result := <-answer:
		return result.response, result.err
	case <-ctx.Done():
		q.mu.Lock()
		_, waiting := q.pending[id]
		delete(q.pending, id)
		q.mu.Unlock()
		if waiting {
			if line, err := marshal(controlCancelRequest{Type: "control_cancel_request", RequestID: id}); err == nil {
				_ = q.writer.write(line)
			}
		}
		return nil, ctx.Err()
	}
}

// Initialization is what Claude Code answered initialize with.
func (q *Query) Initialization(ctx context.Context) (*InitializeResponse, error) {
	select {
	case <-q.initDone:
		return q.initResponse, q.initErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *Query) requestNone(ctx context.Context, request any) error {
	_, err := q.Request(ctx, request)
	return err
}

// Interrupt stops the turn in flight.
func (q *Query) Interrupt(ctx context.Context) error {
	return q.requestNone(ctx, interruptRequest{Subtype: "interrupt"})
}

// SetPermissionMode changes how tool calls are approved from now on.
func (q *Query) SetPermissionMode(ctx context.Context, mode string) error {
	return q.requestNone(ctx, setPermissionModeRequest{Subtype: "set_permission_mode", Mode: mode})
}

// SetModel changes the model the next response is asked of; nil goes back to the default.
func (q *Query) SetModel(ctx context.Context, model *string) error {
	return q.requestNone(ctx, setModelRequest{Subtype: "set_model", Model: model})
}

// ApplyFlagSettings changes settings as if they had been given on the command line.
func (q *Query) ApplyFlagSettings(ctx context.Context, settings any) error {
	return q.requestNone(ctx, applyFlagSettingsRequest{Subtype: "apply_flag_settings", Settings: settings})
}

// StopTask kills one task of the session: a shell, a monitor, a workflow or a subagent.
func (q *Query) StopTask(ctx context.Context, taskID string) error {
	return q.requestNone(ctx, stopTaskRequest{Subtype: "stop_task", TaskID: taskID})
}

// Usage is the plan's rate-limit report (get_usage).
func (q *Query) Usage(ctx context.Context) (jsontext.Value, error) {
	return q.Request(ctx, subtypeOnly{Subtype: "get_usage"})
}

// SupportedCommands are the session's commands, the newest list when it has changed.
func (q *Query) SupportedCommands(ctx context.Context) ([]SlashCommand, error) {
	init, err := q.Initialization(ctx)
	if err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.latestCommands != nil {
		return q.latestCommands, nil
	}
	return init.Commands, nil
}

// SupportedModels are the models the installed Claude Code runs.
func (q *Query) SupportedModels(ctx context.Context) ([]ModelInfo, error) {
	init, err := q.Initialization(ctx)
	if err != nil {
		return nil, err
	}
	return init.Models, nil
}

// AccountInfo is the account the session runs under.
func (q *Query) AccountInfo(ctx context.Context) (*AccountInfo, error) {
	init, err := q.Initialization(ctx)
	if err != nil {
		return nil, err
	}
	return &init.Account, nil
}

// SendUser writes one user message to the session's input.
func (q *Query) SendUser(message UserMessage) error {
	if message.Type == "" {
		message.Type = "user"
	}
	return q.Send(message)
}

// Send writes one message to the session's input as it is.
func (q *Query) Send(message any) error {
	q.mu.Lock()
	closed := q.closed
	q.mu.Unlock()
	if closed {
		return ErrClosed
	}
	line, err := marshal(message)
	if err != nil {
		return err
	}
	return q.writer.write(line)
}

// EndInput closes the session's input once everything written has been sent. The process ends
// when it has answered what it was asked.
func (q *Query) EndInput() { q.writer.end() }

// Messages are the process's messages in order, closed when the query ends.
func (q *Query) Messages() <-chan Message { return q.messages }

// Err is why the process failed, once Messages is closed; nil for a clean end or a close.
func (q *Query) Err() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.err
}

// Exited is closed once the process has exited.
func (q *Query) Exited() <-chan struct{} { return q.exited }

// Pid is the process id.
func (q *Query) Pid() int { return q.cmd.Process.Pid }

// Closed reports whether Close has been called.
func (q *Query) Closed() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.closed
}

// Close ends the input and stops reading. A process that has not left two seconds later is
// terminated, then killed five seconds after that.
func (q *Query) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	pending := q.pending
	q.pending = map[string]chan controlResult{}
	q.mu.Unlock()
	q.cancelCtx()
	for _, answer := range pending {
		answer <- controlResult{err: ErrClosed}
	}
	q.queue.close()
	q.writer.end()
	go func() {
		select {
		case <-q.exited:
			return
		case <-time.After(closeGrace):
		}
		terminate(q.cmd.Process)
		select {
		case <-q.exited:
		case <-time.After(killGrace):
			_ = q.cmd.Process.Kill()
		}
	}()
}

func (q *Query) wait() {
	_ = q.cmd.Wait()
	q.mu.Lock()
	q.exitState = q.cmd.ProcessState
	q.mu.Unlock()
	close(q.exited)
	select {
	case <-q.stderrDone:
	case <-time.After(stderrDrain):
		q.stderr.Close()
	}
	time.AfterFunc(stdoutDrain, func() { q.stdout.Close() })
}

func (q *Query) readStderr() {
	defer close(q.stderrDone)
	buffer := make([]byte, 4096)
	var partial []byte
	for {
		n, err := q.stderr.Read(buffer)
		if n > 0 {
			chunk := append(partial, buffer[:n]...)
			cut := completeUTF8(chunk)
			partial = append([]byte(nil), chunk[cut:]...)
			q.takeStderr(string(chunk[:cut]))
		}
		if err != nil {
			if len(partial) > 0 {
				q.takeStderr(string(partial))
			}
			return
		}
	}
}

// completeUTF8 is how much of chunk ends on a whole character.
func completeUTF8(chunk []byte) int {
	for back := 1; back <= utf8.UTFMax && back <= len(chunk); back++ {
		start := len(chunk) - back
		if utf8.RuneStart(chunk[start]) {
			if utf8.FullRune(chunk[start:]) {
				return len(chunk)
			}
			return start
		}
	}
	return len(chunk)
}

func (q *Query) takeStderr(text string) {
	if text == "" {
		return
	}
	if q.opts.Stderr != nil {
		q.opts.Stderr(text)
		return
	}
	q.mu.Lock()
	q.stderrText += text
	if len(q.stderrText) > 2*stderrKeep {
		q.stderrText = lastCharacters(redact(q.stderrText), stderrKeep)
	}
	q.mu.Unlock()
}

type lineHeader struct {
	Type        string  `json:"type"`
	Subtype     *string `json:"subtype,omitzero"`
	RequestID   *string `json:"request_id,omitzero"`
	SdkHostOnly *bool   `json:"sdk_host_only,omitzero"`
}

func (q *Query) read() {
	reader := bufio.NewReaderSize(q.stdout, 64*1024)
	var errors errorResults
	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			q.take(trimmed, &errors)
		}
		if err != nil {
			break
		}
	}
	<-q.exited
	select {
	case <-q.stderrDone:
	case <-time.After(stderrDrain):
	}
	failure := q.failure(errors.text())
	q.mu.Lock()
	q.err = failure
	q.ended = true
	pending := q.pending
	q.pending = map[string]chan controlResult{}
	q.mu.Unlock()
	q.cancelCtx()
	reason := failure
	if reason == nil {
		reason = ErrClosed
	}
	for _, answer := range pending {
		answer <- controlResult{err: reason}
	}
	q.queue.finish()
}

func (q *Query) take(line []byte, errors *errorResults) {
	var header lineHeader
	if unmarshal(line, &header) != nil {
		return
	}
	subtype := ""
	if header.Subtype != nil {
		subtype = *header.Subtype
	}
	raw := jsontext.Value(append([]byte(nil), line...))
	switch header.Type {
	case "control_response":
		q.receiveResponse(raw)
		return
	case "control_request":
		q.receiveRequest(raw)
		return
	case "control_cancel_request":
		if header.RequestID != nil {
			q.mu.Lock()
			cancel := q.handling[*header.RequestID]
			delete(q.handling, *header.RequestID)
			q.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		}
		return
	case "keep_alive", "transcript_mirror":
		return
	}
	if header.Type == "system" && subtype == "commands_changed" {
		var changed struct {
			Commands []SlashCommand `json:"commands"`
		}
		if unmarshal(raw, &changed) == nil && changed.Commands != nil {
			q.mu.Lock()
			q.latestCommands = changed.Commands
			q.mu.Unlock()
		}
	}
	switch {
	case header.Type == "system" && (subtype == "post_turn_summary" || subtype == "task_summary" || subtype == "session_metadata"):
	case header.Type == "active_goal" || header.Type == "autocompact_state":
	case header.Type == "result":
		errors.result(raw)
	case header.Type == "system" && subtype == "session_state_changed":
		if header.SdkHostOnly != nil && *header.SdkHostOnly {
			return
		}
	case header.Type == "system" && (subtype == "session_writes_lost" || subtype == "plugin_install"):
	default:
		errors.message(header.Type, subtype)
	}
	message, err := DecodeMessage(raw)
	if err != nil {
		message = &UnknownMessage{envelope: envelope{raw}, Type: header.Type, Subtype: subtype}
	}
	q.queue.push(message)
}

func (q *Query) receiveResponse(raw jsontext.Value) {
	var envelope struct {
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  jsontext.Value `json:"response,omitzero"`
			Error     *string        `json:"error,omitzero"`
			ErrorCode *string        `json:"error_code,omitzero"`
		} `json:"response"`
	}
	if unmarshal(raw, &envelope) != nil {
		return
	}
	response := envelope.Response
	q.mu.Lock()
	answer := q.pending[response.RequestID]
	delete(q.pending, response.RequestID)
	q.mu.Unlock()
	if answer == nil {
		return
	}
	if response.Subtype == "success" {
		answer <- controlResult{response: response.Response}
		return
	}
	failure := &ControlError{Subtype: response.Subtype}
	if response.Error != nil {
		failure.Message = *response.Error
	}
	if response.ErrorCode != nil {
		failure.Code = *response.ErrorCode
	}
	answer <- controlResult{err: failure}
}

func (q *Query) failure(errorText *string) error {
	q.mu.Lock()
	closed := q.closed
	state := q.exitState
	tail := ""
	if q.opts.Stderr == nil {
		tail = stderrTail(q.stderrText)
	}
	q.mu.Unlock()
	if closed || state == nil {
		return nil
	}
	suffix := ""
	if tail != "" {
		suffix = ". stderr: " + tail
	}
	var failure error
	if code := state.ExitCode(); code > 0 {
		failure = fmt.Errorf("Claude Code process exited with code %d%s", code, suffix)
	} else if signal := exitSignal(state); signal != "" {
		failure = fmt.Errorf("Claude Code process terminated by signal %s%s", signal, suffix)
	}
	if failure != nil && errorText != nil {
		return fmt.Errorf("Claude Code returned an error result: %s", *errorText)
	}
	return failure
}

// errorResults tracks the error a failed process is reported by: the last error result, or the
// one before a run of turns that never reached the model, as the SDK reads them.
type errorResults struct {
	last           *string
	beforeModeless *string
	modelessEnded  bool
}

type resultFields struct {
	Subtype  string   `json:"subtype"`
	IsError  bool     `json:"is_error"`
	NumTurns *float64 `json:"num_turns,omitzero"`
	Result   *string  `json:"result,omitzero"`
	Errors   []string `json:"errors,omitzero"`
}

func (e *errorResults) result(raw jsontext.Value) {
	var result resultFields
	_ = unmarshal(raw, &result)
	e.last = nil
	if result.IsError {
		text := ""
		if result.Subtype == ResultSubtypeSuccess {
			if result.Result != nil {
				text = *result.Result
			}
		} else {
			var kept []string
			for _, message := range result.Errors {
				if trimmed := strings.TrimSpace(message); trimmed != "" {
					kept = append(kept, trimmed)
				}
			}
			text = strings.Join(kept, "; ")
		}
		if text != "" {
			e.last = &text
		}
	}
	e.modelessEnded = result.Subtype == ResultSubtypeSuccess && !result.IsError &&
		result.NumTurns != nil && *result.NumTurns == 0 && result.Result != nil && *result.Result == ""
	if !e.modelessEnded {
		e.beforeModeless = e.last
	}
}

func (e *errorResults) message(kind, subtype string) {
	e.last = nil
	if kind == "system" && subtype == "init" {
		e.modelessEnded = false
	} else if kind != "system" || subtype == "compact_boundary" {
		e.beforeModeless = nil
		e.modelessEnded = false
	}
}

func (e *errorResults) text() *string {
	if e.last != nil {
		return e.last
	}
	if e.modelessEnded {
		return e.beforeModeless
	}
	return nil
}

// pump hands messages to the consumer one at a time, so reading never waits on it.
func (q *Query) pump() {
	defer close(q.finished)
	defer close(q.messages)
	for {
		message, ok := q.queue.pop()
		if !ok {
			return
		}
		select {
		case q.messages <- message:
		case <-q.queue.closed:
			return
		}
	}
}

// messageQueue is an unbounded queue of messages read but not yet taken.
type messageQueue struct {
	mu       sync.Mutex
	items    []Message
	ready    chan struct{}
	done     bool
	closed   chan struct{}
	closeOne sync.Once
}

func newMessageQueue() *messageQueue {
	return &messageQueue{ready: make(chan struct{}, 1), closed: make(chan struct{})}
}

func (m *messageQueue) push(message Message) {
	m.mu.Lock()
	m.items = append(m.items, message)
	m.mu.Unlock()
	select {
	case m.ready <- struct{}{}:
	default:
	}
}

// finish lets the consumer take what is queued, then ends.
func (m *messageQueue) finish() {
	m.mu.Lock()
	m.done = true
	m.mu.Unlock()
	select {
	case m.ready <- struct{}{}:
	default:
	}
}

// close ends the queue now, dropping what is queued.
func (m *messageQueue) close() { m.closeOne.Do(func() { close(m.closed) }) }

func (m *messageQueue) pop() (Message, bool) {
	for {
		m.mu.Lock()
		if len(m.items) > 0 {
			message := m.items[0]
			m.items[0] = nil
			m.items = m.items[1:]
			m.mu.Unlock()
			return message, true
		}
		done := m.done
		m.mu.Unlock()
		if done {
			return nil, false
		}
		select {
		case <-m.ready:
		case <-m.closed:
			return nil, false
		}
	}
}

// lineWriter writes lines to the process's stdin in order without ever making the writer wait.
type lineWriter struct {
	mu      sync.Mutex
	out     io.WriteCloser
	lines   [][]byte
	ending  bool
	broken  error
	wake    chan struct{}
	stopped chan struct{}
}

func newLineWriter(out io.WriteCloser) *lineWriter {
	w := &lineWriter{out: out, wake: make(chan struct{}, 1), stopped: make(chan struct{})}
	go w.run()
	return w
}

var errInputEnded = errors.New("the session's input has ended")

func (w *lineWriter) write(line []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.broken != nil {
		return w.broken
	}
	if w.ending {
		return errInputEnded
	}
	w.lines = append(w.lines, append(line, '\n'))
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return nil
}

func (w *lineWriter) end() {
	w.mu.Lock()
	w.ending = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *lineWriter) run() {
	defer close(w.stopped)
	for {
		w.mu.Lock()
		lines := w.lines
		w.lines = nil
		ending := w.ending
		w.mu.Unlock()
		for _, line := range lines {
			if _, err := w.out.Write(line); err != nil {
				w.mu.Lock()
				w.broken = fmt.Errorf("Failed to write to process stdin: %w", err)
				w.lines = nil
				w.mu.Unlock()
				w.out.Close()
				return
			}
		}
		if len(lines) > 0 {
			continue
		}
		if ending {
			w.out.Close()
			return
		}
		<-w.wake
	}
}
