package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	stderrKeep = 16 * 1024
	// drainTime is how long after exit the stdio pipes get to drain before the client stops
	// waiting for them.
	drainTime = 250 * time.Millisecond
	killGrace = 2 * time.Second
	// MethodNotFound is the JSON-RPC code for a method nobody handles.
	MethodNotFound = -32601
)

// DefaultClientInfo is how the app introduces itself to the server.
var DefaultClientInfo = ClientInfo{Name: "aicodingtool", Title: strPtr("AICodingTool"), Version: "1"}

func strPtr(value string) *string { return &value }

// ExitStatus is how the server process ended.
type ExitStatus struct {
	// Code is the exit code; nil when a signal ended the process or it never started.
	Code *int
	// Signal names the signal that ended the process ("SIGTERM"), if one did.
	Signal string
	Stderr string
}

func (e ExitStatus) how() string {
	if e.Signal != "" {
		return "signal " + e.Signal
	}
	if e.Code == nil {
		return "code null"
	}
	return fmt.Sprintf("code %d", *e.Code)
}

// JSONRPCError is a JSON-RPC error object.
type JSONRPCError struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data,omitzero"`
}

// AppServerError is a request the server answered with a JSON-RPC error.
type AppServerError struct {
	Method  string
	Code    int
	Message string
	Data    jsontext.Value
}

func (e *AppServerError) Error() string { return e.Method + ": " + e.Message }

// AppServerExited is a request that cannot be answered because the server process is gone.
type AppServerExited struct {
	Exit    ExitStatus
	Context string
}

func (e *AppServerExited) Error() string {
	stderr := ""
	if e.Exit.Stderr != "" {
		stderr = "\n" + e.Exit.Stderr
	}
	return fmt.Sprintf("Codex app server exited with %s %s%s", e.Exit.how(), e.Context, stderr)
}

// Command is how a server process is started.
type Command struct {
	Executable string
	Args       []string
	Cwd        string
	// Env is the whole environment; nil inherits it.
	Env []string
	// SharedAuth, when set, starts the server that holds the user's keychain credentials; a
	// private conversation home borrows them from it.
	SharedAuth *Command
	// Make makes the process's command; nil uses exec.Command.
	Make func(name string, args ...string) *exec.Cmd
	// OnSpawn is told the process id once it starts.
	OnSpawn func(pid int)
}

// IncomingRequest is a request the server sent. Exactly one of Respond and Fail must be called.
type IncomingRequest struct {
	ID     RequestID
	Method string
	Params jsontext.Value
	reply  func(body any)
	once   *sync.Once
}

type resultBody struct {
	Result any `json:"result"`
}

type errorBody struct {
	Error JSONRPCError `json:"error"`
}

// Respond answers the request with its result.
func (r *IncomingRequest) Respond(result any) {
	r.once.Do(func() { r.reply(resultBody{Result: result}) })
}

// Fail answers the request with an error.
func (r *IncomingRequest) Fail(err JSONRPCError) {
	r.once.Do(func() { r.reply(errorBody{Error: err}) })
}

// DecodeParams reads the request's params into value.
func (r *IncomingRequest) DecodeParams(value any) error { return Unmarshal(r.Params, value) }

type pendingRequest struct {
	method string
	answer chan rpcResult
}

type rpcResult struct {
	result jsontext.Value
	err    error
}

type handlerEntry[T any] struct {
	id      int
	handler T
}

// Client is one newline-delimited JSON-RPC 2.0 conversation with an app server process. Client
// requests are answered by id; server requests reach OnRequest handlers, which must answer them;
// notifications reach On handlers. The process exiting fails everything still pending.
type Client struct {
	cmd               *exec.Cmd
	stdin             io.WriteCloser
	stdout, stderr    *os.File
	sharedAuthCommand *Command
	connectAuth       func(Command) AuthClient

	writeMu sync.Mutex

	mu            sync.Mutex
	pending       map[int64]pendingRequest
	notifications map[string][]handlerEntry[func(jsontext.Value)]
	requests      []handlerEntry[func(*IncomingRequest)]
	nextID        int64
	nextHandler   int
	exit          *ExitStatus
	sharedAuth    *SharedAuth
	authClosed    chan struct{}
	stderrText    tailBuffer

	exited     chan struct{}
	readers    sync.WaitGroup
	finishOnce sync.Once
}

// Connect starts a server process and speaks to it. A process that cannot start is reported as an
// exit, as every request then says.
func Connect(command Command) *Client {
	c := &Client{
		sharedAuthCommand: command.SharedAuth,
		pending:           map[int64]pendingRequest{},
		notifications:     map[string][]handlerEntry[func(jsontext.Value)]{},
		nextID:            1,
		exited:            make(chan struct{}),
	}
	c.connectAuth = func(command Command) AuthClient { return Connect(command) }
	build := command.Make
	if build == nil {
		build = exec.Command
	}
	cmd := build(command.Executable, command.Args...)
	c.cmd = cmd
	if command.Cwd != "" {
		cmd.Dir = command.Cwd
	}
	if command.Env != nil {
		cmd.Env = command.Env
	}
	ownProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err == nil {
		c.stdin = stdin
		var stdoutWrite, stderrWrite *os.File
		c.stdout, stdoutWrite, err = os.Pipe()
		if err == nil {
			c.stderr, stderrWrite, err = os.Pipe()
			if err != nil {
				c.stdout.Close()
				stdoutWrite.Close()
			}
		}
		if err == nil {
			cmd.Stdout, cmd.Stderr = stdoutWrite, stderrWrite
			err = cmd.Start()
			stdoutWrite.Close()
			stderrWrite.Close()
			if err != nil {
				c.stdout.Close()
				c.stderr.Close()
			}
		}
	}
	if err != nil {
		c.stderrText.push([]byte(spawnMessage(command.Executable, err) + "\n"))
		c.finish(nil, "")
		return c
	}
	if command.OnSpawn != nil {
		command.OnSpawn(cmd.Process.Pid)
	}
	c.readers.Add(2)
	go c.readStdout()
	go c.readStderr()
	go c.wait()
	return c
}

// spawnMessage says why a process did not start, as Node's spawn error does.
func spawnMessage(executable string, err error) string {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
		return "spawn " + executable + " ENOENT"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "spawn " + executable + " EACCES"
	}
	return err.Error()
}

func (c *Client) wait() {
	_ = c.cmd.Wait()
	state := c.cmd.ProcessState
	drained := make(chan struct{})
	go func() {
		c.readers.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(drainTime):
	}
	var code *int
	signal := exitSignal(state)
	if signal == "" {
		value := state.ExitCode()
		code = &value
	}
	c.finish(code, signal)
}

func (c *Client) readStderr() {
	defer c.readers.Done()
	buffer := make([]byte, 32*1024)
	for {
		n, err := c.stderr.Read(buffer)
		if n > 0 {
			c.mu.Lock()
			c.stderrText.push(append([]byte(nil), buffer[:n]...))
			c.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) readStdout() {
	defer c.readers.Done()
	reader := bufio.NewReaderSize(c.stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		line = bytes.TrimSuffix(line, []byte("\n"))
		if len(line) > 0 {
			c.handleLine(line)
		}
		if err != nil {
			return
		}
	}
}

type incomingMessage struct {
	ID     jsontext.Value `json:"id,omitzero"`
	Method *string        `json:"method,omitzero"`
	Params jsontext.Value `json:"params,omitzero"`
	Result jsontext.Value `json:"result,omitzero"`
	Error  *JSONRPCError  `json:"error,omitzero"`
}

func (c *Client) handleLine(line []byte) {
	var message incomingMessage
	if Unmarshal(line, &message) != nil {
		return
	}
	if message.Method != nil {
		if len(message.ID) == 0 {
			c.dispatchNotification(*message.Method, message.Params)
		} else {
			var id RequestID
			if Unmarshal(message.ID, &id) != nil {
				id = RequestID{raw: append(jsontext.Value(nil), message.ID...)}
			}
			c.dispatchRequest(id, *message.Method, message.Params)
		}
		return
	}
	if message.ID.Kind() != '0' {
		return
	}
	var id int64
	if Unmarshal(message.ID, &id) != nil {
		return
	}
	c.mu.Lock()
	pending, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return
	}
	if message.Error != nil {
		pending.answer <- rpcResult{err: &AppServerError{Method: pending.method, Code: message.Error.Code, Message: message.Error.Message, Data: message.Error.Data}}
		return
	}
	result := message.Result
	if len(result) == 0 {
		result = jsontext.Value("null")
	}
	pending.answer <- rpcResult{result: append(jsontext.Value(nil), result...)}
}

func (c *Client) dispatchNotification(method string, params jsontext.Value) {
	c.mu.Lock()
	handlers := append([]handlerEntry[func(jsontext.Value)](nil), c.notifications[method]...)
	c.mu.Unlock()
	if len(params) == 0 {
		params = jsontext.Value("null")
	}
	params = append(jsontext.Value(nil), params...)
	for _, entry := range handlers {
		entry.handler(params)
	}
}

func (c *Client) dispatchRequest(id RequestID, method string, params jsontext.Value) {
	if len(params) == 0 {
		params = jsontext.Value("null")
	}
	request := &IncomingRequest{ID: id, Method: method, Params: append(jsontext.Value(nil), params...), once: &sync.Once{}}
	request.reply = func(body any) {
		c.mu.Lock()
		exited := c.exit != nil
		c.mu.Unlock()
		if exited {
			return
		}
		c.send(replyMessage{JSONRPC: "2.0", ID: id, Body: body})
	}
	c.mu.Lock()
	auth := c.sharedAuth
	handlers := append([]handlerEntry[func(*IncomingRequest)](nil), c.requests...)
	c.mu.Unlock()
	if method == RequestAuthTokensRefresh && auth != nil {
		go c.refreshAuth(auth, request)
		return
	}
	if len(handlers) == 0 {
		request.Fail(JSONRPCError{Code: MethodNotFound, Message: "No handler for " + method})
		return
	}
	for _, entry := range handlers {
		entry.handler(request)
	}
}

func (c *Client) refreshAuth(auth *SharedAuth, request *IncomingRequest) {
	fail := func() {
		request.Fail(JSONRPCError{Code: -32603, Message: "Could not refresh the shared Codex account. Check Codex sign-in and start a new run."})
	}
	login, err := auth.Read(context.Background(), true)
	tokens, ok := login.(ChatGPTAuthTokensLogin)
	if err != nil || !ok {
		fail()
		return
	}
	var params ChatgptAuthTokensRefreshParams
	_ = request.DecodeParams(&params)
	if params.PreviousAccountID != nil && *params.PreviousAccountID != "" && *params.PreviousAccountID != tokens.ChatgptAccountID {
		fail()
		return
	}
	request.Respond(ChatgptAuthTokensRefreshResponse{AccessToken: tokens.AccessToken, ChatgptAccountID: tokens.ChatgptAccountID, ChatgptPlanType: tokens.ChatgptPlanType})
}

type replyMessage struct {
	JSONRPC string
	ID      RequestID
	Body    any
}

// MarshalJSONTo writes {"jsonrpc","id"} followed by the body's result or error.
func (r replyMessage) MarshalJSONTo(enc *jsontext.Encoder) error {
	body, err := Marshal(r.Body)
	if err != nil {
		return err
	}
	id, err := Marshal(r.ID)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.WriteString(`{"jsonrpc":"2.0","id":`)
	out.Write(id)
	if len(body) > 2 {
		out.WriteByte(',')
		out.Write(body[1 : len(body)-1])
	}
	out.WriteByte('}')
	return enc.WriteValue(out.Bytes())
}

type requestMessage struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitzero"`
}

type notificationMessage struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func (c *Client) send(message any) error {
	line, err := Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stdin == nil {
		return nil
	}
	_, _ = c.stdin.Write(append(line, '\n'))
	return nil
}

// Request sends a request and waits for its result. Params nil sends none. A ctx that ends first
// stops the wait; the server is not told.
func (c *Client) Request(ctx context.Context, method string, params any) (jsontext.Value, error) {
	c.mu.Lock()
	if c.exit != nil {
		exit := *c.exit
		c.mu.Unlock()
		return nil, &AppServerExited{Exit: exit, Context: "before " + method}
	}
	id := c.nextID
	c.nextID++
	answer := make(chan rpcResult, 1)
	c.pending[id] = pendingRequest{method: method, answer: answer}
	c.mu.Unlock()
	if err := c.send(requestMessage{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case result := <-answer:
		return result.result, result.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// Call sends a request and decodes its result.
func Call[R any](ctx context.Context, c *Client, method string, params any) (R, error) {
	var result R
	raw, err := c.Request(ctx, method, params)
	if err != nil {
		return result, err
	}
	if err := Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

// Initialize runs the initialize / initialized handshake the server needs before any other method.
// Nil capabilities asks for the experimental API.
func (c *Client) Initialize(ctx context.Context, info ClientInfo, capabilities *InitializeCapabilities) (*InitializeResponse, error) {
	if capabilities == nil {
		capabilities = &InitializeCapabilities{ExperimentalAPI: true, RequestAttestation: false}
	}
	server, err := Call[InitializeResponse](ctx, c, "initialize", InitializeParams{ClientInfo: info, Capabilities: capabilities})
	if err != nil {
		return nil, err
	}
	_ = c.send(notificationMessage{JSONRPC: "2.0", Method: "initialized", Params: Empty{}})
	if c.sharedAuthCommand != nil {
		if err := c.borrowAuth(ctx); err != nil {
			c.Close()
			return nil, err
		}
	}
	return &server, nil
}

func (c *Client) borrowAuth(ctx context.Context) error {
	auth, err := AcquireSharedAuth(ctx, *c.sharedAuthCommand, c.connectAuth)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.exit != nil {
		exit := *c.exit
		c.mu.Unlock()
		auth.Release()
		return &AppServerExited{Exit: exit, Context: "during authentication"}
	}
	c.sharedAuth = auth
	c.mu.Unlock()
	login, err := auth.Read(ctx, false)
	if err != nil {
		return err
	}
	if login != nil {
		if _, err := c.Request(ctx, "account/login/start", login); err != nil {
			return err
		}
	}
	return nil
}

// On calls handler with the params of every notification of method, in the order they arrive,
// on the goroutine that reads the server. The returned function stops it.
func (c *Client) On(method string, handler func(params jsontext.Value)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextHandler
	c.nextHandler++
	c.notifications[method] = append(c.notifications[method], handlerEntry[func(jsontext.Value)]{id, handler})
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		kept := c.notifications[method][:0:0]
		for _, entry := range c.notifications[method] {
			if entry.id != id {
				kept = append(kept, entry)
			}
		}
		c.notifications[method] = kept
	}
}

// OnNotification calls handler with each notification of method decoded; one that does not
// decode is skipped.
func OnNotification[P any](c *Client, method string, handler func(P)) func() {
	return c.On(method, func(raw jsontext.Value) {
		var params P
		if Unmarshal(raw, &params) == nil {
			handler(params)
		}
	})
}

// OnRequest calls handler with every server request. With no handler a request is refused with a
// method-not-found error, so the server never waits on the client.
func (c *Client) OnRequest(handler func(*IncomingRequest)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextHandler
	c.nextHandler++
	c.requests = append(c.requests, handlerEntry[func(*IncomingRequest)]{id, handler})
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		kept := c.requests[:0:0]
		for _, entry := range c.requests {
			if entry.id != id {
				kept = append(kept, entry)
			}
		}
		c.requests = kept
	}
}

// Exited is closed once the process has gone.
func (c *Client) Exited() <-chan struct{} { return c.exited }

// Exit is how the process ended, once it has.
func (c *Client) Exit() *ExitStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exit == nil {
		return nil
	}
	exit := *c.exit
	return &exit
}

// Pid is the server's process id, or 0 when it never started.
func (c *Client) Pid() int {
	if c.cmd == nil || c.cmd.Process == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Close ends the conversation and, with it, the process group: a healthy server leaves on its own,
// the rest is signalled.
func (c *Client) Close() ExitStatus {
	c.mu.Lock()
	running := c.exit == nil
	c.mu.Unlock()
	if running {
		c.writeMu.Lock()
		if c.stdin != nil {
			c.stdin.Close()
		}
		c.writeMu.Unlock()
		signalGroup(c.cmd.Process, terminateSignal)
		select {
		case <-c.exited:
		case <-time.After(killGrace):
			signalGroup(c.cmd.Process, killSignal)
			<-c.exited
		}
	}
	<-c.exited
	c.mu.Lock()
	closed := c.authClosed
	exit := *c.exit
	c.mu.Unlock()
	if closed != nil {
		<-closed
	}
	return exit
}

func (c *Client) finish(code *int, signal string) {
	c.finishOnce.Do(func() {
		c.mu.Lock()
		exit := ExitStatus{Code: code, Signal: signal, Stderr: c.stderrText.text()}
		c.exit = &exit
		pending := c.pending
		c.pending = map[int64]pendingRequest{}
		auth := c.sharedAuth
		c.sharedAuth = nil
		if auth != nil {
			c.authClosed = make(chan struct{})
		}
		closed := c.authClosed
		c.mu.Unlock()
		if auth != nil {
			go func() {
				auth.Release()
				close(closed)
			}()
		}
		for _, request := range pending {
			request.answer <- rpcResult{err: &AppServerExited{Exit: exit, Context: "while " + request.method + " was pending"}}
		}
		close(c.exited)
	})
}

// tailBuffer keeps the newest bytes a stream produced, dropping whole chunks from the front.
type tailBuffer struct {
	chunks [][]byte
	bytes  int
}

func (t *tailBuffer) push(chunk []byte) {
	t.chunks = append(t.chunks, chunk)
	t.bytes += len(chunk)
	for len(t.chunks) > 1 && t.bytes > stderrKeep {
		t.bytes -= len(t.chunks[0])
		t.chunks = t.chunks[1:]
	}
}

func (t *tailBuffer) text() string {
	return strings.TrimSpace(string(bytes.Join(t.chunks, nil)))
}
