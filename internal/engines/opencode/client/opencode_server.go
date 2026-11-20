// Package client starts a private `opencode serve` and speaks its HTTP API and event feed.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// startTimeout is long enough for a cold start that loads the model catalogue, short enough
	// that a stuck one is noticed.
	startTimeout = 30 * time.Second
	// stopGrace is how long a server asked to stop has before it is killed.
	stopGrace = 5 * time.Second
	// RequestTimeout is how long a request waits: no request the app makes waits on a model.
	RequestTimeout = 60 * time.Second
	// stderrLimit is the stderr kept for a failure, enough for the error a server dies with.
	stderrLimit = 8192
	stderrDrain = 250 * time.Millisecond
)

// FeedEnded is the event every listener is told when the feed ends while the server is still meant
// to be up, after which nothing it does is heard.
const FeedEnded = "aicodingtool.feed.ended"

var (
	listening     = regexp.MustCompile(`(?i)listening on (https?://\S+)`)
	password      = regexp.MustCompile(`(?i)server password (\S+)`)
	listeningWord = regexp.MustCompile(`(?i)listening`)
	passwordWord  = regexp.MustCompile(`(?i)password`)
)

// WatchedServe is the shell script a server runs under. The server does not notice the app going
// away, so the shell stops it once the app's end of stdin closes, whether the app closed it or
// died, and kills it if it will not stop. The shell leaves with the server.
var WatchedServe = strings.Join([]string{
	"exec 3<&0",
	`"$0" serve --hostname 127.0.0.1 --port 0 </dev/null &`,
	"server=$!",
	"( read _ <&3; kill $server 2>/dev/null; sleep 3; kill -9 $server 2>/dev/null ) &",
	"watcher=$!",
	"wait $server",
	"status=$?",
	"kill $watcher 2>/dev/null",
	"exit $status",
}, "\n")

// ExitStatus is how the server process ended.
type ExitStatus struct {
	// Code is the exit code; nil when a signal ended the process or it never started.
	Code   *int
	Signal string
	Stderr string
}

// Event is one frame of the server's event feed: a type, and what it carries.
type Event struct {
	Type string
	// Data is the event's data when it is an object, else nil.
	Data jsontext.Value
}

// Error is a request the server answered with an error, kept with the tag OpenCode gave it.
type Error struct {
	Status  int
	Tag     string
	Message string
}

func (e *Error) Error() string { return e.Message }

// Exited is a server that is gone.
type Exited struct {
	Exit ExitStatus
}

func (e *Exited) Error() string {
	if line := firstLine(e.Exit.Stderr); line != "" {
		return line
	}
	if e.Exit.Signal != "" {
		return "signal " + e.Exit.Signal
	}
	if e.Exit.Code == nil {
		return "exit code null"
	}
	return fmt.Sprintf("exit code %d", *e.Exit.Code)
}

// ErrTimeout is a request that took longer than it was given.
var ErrTimeout = errors.New("The operation was aborted due to timeout")

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

// UnreadLine is, for a server that never said how to reach it in words this build reads, the last
// line that names what is still missing: where it listens or its password, in other words. None
// for a slow start.
func UnreadLine(output string) (string, bool) {
	var missing []*regexp.Regexp
	if !listening.MatchString(output) {
		missing = append(missing, listeningWord)
	}
	if !password.MatchString(output) {
		missing = append(missing, passwordWord)
	}
	found, ok := "", false
	for _, line := range strings.Split(output, "\n") {
		for _, word := range missing {
			if word.MatchString(line) {
				found, ok = strings.TrimSpace(line), true
				break
			}
		}
	}
	return found, ok
}

func unreadStartup(line string) string {
	return fmt.Sprintf(`OpenCode started, but this version of the app cannot read how to reach it ("%s"). Update AI Coding Tool, or use the OpenCode version it was built for.`, line)
}

// Requester is what reading the server needs; *Server is one, and a scripted one can stand in.
type Requester interface {
	// Request sends one request; body nil sends none and timeout 0 waits RequestTimeout. Most
	// answers wrap their value with the location they were read in, and that value is returned.
	Request(ctx context.Context, method, path string, body any, timeout time.Duration) (jsontext.Value, error)
}

// Options say how a server starts.
type Options struct {
	// Cwd is the folder the server starts in, which is the location a request names none for.
	Cwd string
	// Config is inline configuration, merged over the user's own.
	Config any
	// Executable is the opencode binary; empty runs "opencode" from the search path.
	Executable string
	// Env is the whole environment; nil inherits it.
	Env []string
	// Make makes the process's command; nil uses exec.Command.
	Make func(name string, args ...string) *exec.Cmd
	// OnSpawn is told the process id once it starts.
	OnSpawn func(pid int)
}

// Server is one private OpenCode server on the loopback interface, behind the password it made up.
type Server struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	http          *http.Client
	url           string
	authorization string

	mu            sync.Mutex
	startupOutput string
	stderr        string
	listeners     map[int]func(Event)
	nextID        int
	feed          context.CancelFunc
	closing       bool
	exit          *ExitStatus

	exited     chan struct{}
	stderrDone chan struct{}
	ready      chan struct{}
	readyErr   error
}

// Start starts a server in opts.Cwd, on a port of the system's choosing, and connects to it.
func Start(ctx context.Context, opts Options) (*Server, error) {
	executable := opts.Executable
	if executable == "" {
		executable = "opencode"
	}
	build := opts.Make
	if build == nil {
		build = exec.Command
	}
	cmd := build("/bin/sh", "-c", WatchedServe, executable)
	cmd.Dir = opts.Cwd
	env := opts.Env
	if env == nil {
		env = cmd.Env
	}
	if env == nil {
		env = os.Environ()
	}
	if opts.Config != nil {
		config, err := json.Marshal(opts.Config)
		if err != nil {
			return nil, err
		}
		env = append(append([]string(nil), env...), "OPENCODE_CONFIG_CONTENT="+string(config))
	}
	cmd.Env = env
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
	cmd.Stdout, cmd.Stderr = stdoutWrite, stderrWrite
	err = cmd.Start()
	stdoutWrite.Close()
	stderrWrite.Close()
	if err != nil {
		stdoutRead.Close()
		stderrRead.Close()
		return nil, &Exited{Exit: ExitStatus{Stderr: err.Error()}}
	}
	if opts.OnSpawn != nil {
		opts.OnSpawn(cmd.Process.Pid)
	}
	s := &Server{
		cmd: cmd, stdin: stdin, http: &http.Client{}, listeners: map[int]func(Event){},
		exited: make(chan struct{}), stderrDone: make(chan struct{}), ready: make(chan struct{}),
	}
	go s.readStderr(stderrRead)
	go s.wait()
	go s.readStartup(stdoutRead)
	timer := time.NewTimer(startTimeout)
	defer timer.Stop()
	select {
	case <-s.ready:
		err = s.readyErr
	case <-s.exited:
		s.mu.Lock()
		err = &Exited{Exit: *s.exit}
		s.mu.Unlock()
	case <-timer.C:
		s.mu.Lock()
		err = errors.New("OpenCode did not start in time.")
		if line, ok := UnreadLine(s.startupOutput); ok {
			err = errors.New(unreadStartup(line))
		}
		s.mu.Unlock()
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Server) readStartup(stdout *os.File) {
	defer stdout.Close()
	buffer := make([]byte, 4096)
	output := ""
	for {
		n, err := stdout.Read(buffer)
		if n > 0 {
			s.mu.Lock()
			if s.url == "" {
				output += string(buffer[:n])
				s.startupOutput = output
				url, secret := listening.FindStringSubmatch(output), password.FindStringSubmatch(output)
				if url != nil && secret != nil {
					s.url = strings.TrimSuffix(url[1], "/")
					s.authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+secret[1]))
					close(s.ready)
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) readStderr(stderr *os.File) {
	defer close(s.stderrDone)
	defer stderr.Close()
	buffer := make([]byte, 4096)
	for {
		n, err := stderr.Read(buffer)
		if n > 0 {
			s.mu.Lock()
			s.stderr += string(buffer[:n])
			if len(s.stderr) > stderrLimit {
				s.stderr = s.stderr[len(s.stderr)-stderrLimit:]
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) wait() {
	_ = s.cmd.Wait()
	select {
	case <-s.stderrDone:
	case <-time.After(stderrDrain):
	}
	state := s.cmd.ProcessState
	exit := ExitStatus{Signal: exitSignal(state)}
	if exit.Signal == "" {
		code := state.ExitCode()
		exit.Code = &code
	}
	s.mu.Lock()
	exit.Stderr = s.stderr
	s.exit = &exit
	s.mu.Unlock()
	close(s.exited)
}

// URL is where the server listens.
func (s *Server) URL() string { return s.url }

// Exited is closed once the server has gone.
func (s *Server) Exited() <-chan struct{} { return s.exited }

// Exit is how the server ended, once it has.
func (s *Server) Exit() *ExitStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exit == nil {
		return nil
	}
	exit := *s.exit
	return &exit
}

// Request sends one request to the server.
func (s *Server) Request(ctx context.Context, method, path string, body any, timeout time.Duration) (jsontext.Value, error) {
	if timeout <= 0 {
		timeout = RequestTimeout
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, ErrTimeout)
	defer cancel()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, s.url+path, reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("authorization", s.authorization)
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}
	response, err := s.http.Do(request)
	if err != nil {
		if cause := context.Cause(ctx); errors.Is(cause, ErrTimeout) {
			return nil, ErrTimeout
		}
		return nil, err
	}
	defer response.Body.Close()
	text, err := io.ReadAll(response.Body)
	if err != nil {
		if errors.Is(context.Cause(ctx), ErrTimeout) {
			return nil, ErrTimeout
		}
		return nil, err
	}
	var parsed jsontext.Value
	if len(text) > 0 {
		if value := jsontext.Value(text); value.IsValid() {
			parsed = value
		} else {
			parsed, _ = json.Marshal(string(text))
		}
	}
	var record map[string]jsontext.Value
	if parsed.Kind() == '{' {
		_ = json.Unmarshal(parsed, &record)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		failure := &Error{Status: response.StatusCode, Message: response.Status}
		var tag, message string
		if json.Unmarshal(record["_tag"], &tag) == nil {
			failure.Tag = tag
		}
		if json.Unmarshal(record["message"], &message) == nil {
			failure.Message = message
		}
		return nil, failure
	}
	if data, ok := record["data"]; ok {
		return data, nil
	}
	return parsed, nil
}

// Subscribe calls listener for every event until the server goes; the returned function stops
// listening. Every listener shares one feed.
func (s *Server) Subscribe(listener func(Event)) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = listener
	start := s.feed == nil && !s.closing
	var ctx context.Context
	if start {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		s.feed = cancel
	}
	s.mu.Unlock()
	if start {
		go s.listen(ctx)
	}
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *Server) listen(ctx context.Context) {
	s.readFeed(ctx)
	s.mu.Lock()
	s.feed = nil
	closing := s.closing
	listeners := s.listenerList()
	s.mu.Unlock()
	if !closing {
		for _, listener := range listeners {
			listener(Event{Type: FeedEnded})
		}
	}
}

func (s *Server) listenerList() []func(Event) {
	ids := slices.Sorted(maps.Keys(s.listeners))
	listeners := make([]func(Event), 0, len(ids))
	for _, id := range ids {
		listeners = append(listeners, s.listeners[id])
	}
	return listeners
}

func (s *Server) readFeed(ctx context.Context) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/event", nil)
	if err != nil {
		return
	}
	request.Header.Set("authorization", s.authorization)
	request.Header.Set("accept", "text/event-stream")
	response, err := s.http.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var buffer []byte
	chunk := make([]byte, 32*1024)
	for {
		n, err := reader.Read(chunk)
		buffer = append(buffer, chunk[:n]...)
		for {
			end := bytes.Index(buffer, []byte("\n\n"))
			if end < 0 {
				break
			}
			frame := string(buffer[:end])
			buffer = buffer[end+2:]
			s.dispatch(frame)
		}
		if err != nil {
			return
		}
	}
}

// ParseFrame reads one event-stream frame: its data lines, joined, as an event with a type.
func ParseFrame(frame string) (Event, bool) {
	var data []string
	for _, line := range strings.Split(frame, "\n") {
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimLeft(rest, " \t\n\r\v\f"))
		}
	}
	if len(data) == 0 {
		return Event{}, false
	}
	joined := strings.Join(data, "\n")
	if joined == "" {
		return Event{}, false
	}
	var event struct {
		Type *string        `json:"type"`
		Data jsontext.Value `json:"data"`
	}
	if json.Unmarshal([]byte(joined), &event) != nil || event.Type == nil {
		return Event{}, false
	}
	parsed := Event{Type: *event.Type}
	if event.Data.Kind() == '{' {
		parsed.Data = event.Data
	}
	return parsed, true
}

func (s *Server) dispatch(frame string) {
	event, ok := ParseFrame(frame)
	if !ok {
		return
	}
	s.mu.Lock()
	listeners := s.listenerList()
	s.mu.Unlock()
	for _, listener := range listeners {
		listener(event)
	}
}

// Close stops listening and asks the server to stop by closing its input; one that has not gone
// five seconds later is killed.
func (s *Server) Close() {
	forgetLoaded(s)
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		<-s.exited
		return
	}
	s.closing = true
	feed := s.feed
	s.listeners = map[int]func(Event){}
	running := s.exit == nil
	s.mu.Unlock()
	if feed != nil {
		feed()
	}
	if running {
		s.stdin.Close()
		select {
		case <-s.exited:
		case <-time.After(stopGrace):
			_ = s.cmd.Process.Kill()
			<-s.exited
		}
	}
}
