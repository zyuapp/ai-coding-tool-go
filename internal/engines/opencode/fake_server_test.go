package opencode

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/opencode/client"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type call struct {
	method string
	path   string
	body   string
}

// A server that answers from a script and emits whatever the test says it does.
type fakeServer struct {
	options ServerOptions
	// Answers a request before the script does; false leaves it to the script.
	answer func(method, path string) (any, error, bool)
	// Runs after the script answered.
	after func(server *fakeServer, method, path string)

	mu        sync.Mutex
	calls     []call
	listeners map[int]func(client.Event)
	next      int
	inbox     int
	closed    bool
	missing   map[string]bool
	empty     map[string]bool
	exited    chan struct{}
	exitOnce  sync.Once
	exit      *client.ExitStatus
}

func newFakeServer(options ServerOptions) *fakeServer {
	return &fakeServer{options: options, listeners: map[int]func(client.Event){}, missing: map[string]bool{}, empty: map[string]bool{}, exited: make(chan struct{})}
}

func encode(value any) jsontext.Value {
	if value == nil {
		return nil
	}
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func (s *fakeServer) script(method, path string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	segment := func() string { return strings.Split(path, "/")[3] }
	switch {
	case method == "GET" && strings.HasPrefix(path, "/api/model"):
		return []any{map[string]any{"id": "z-ai/glm-5.3-flash", "providerID": "openrouter", "variants": []any{map[string]string{"id": "low"}, map[string]string{"id": "high"}, map[string]string{"id": "none"}}, "limit": map[string]any{"context": 1000}}}, nil
	case method == "GET" && strings.HasPrefix(path, "/api/skill"):
		return []any{map[string]string{"id": "review-thread"}}, nil
	case method == "GET" && strings.HasPrefix(path, "/api/command"):
		return []any{map[string]string{"name": "init"}}, nil
	case method == "POST" && path == "/api/session":
		return map[string]string{"id": "ses_new"}, nil
	case method == "GET" && strings.HasPrefix(path, "/api/session/"):
		id := segment()
		if s.missing[id] {
			return nil, &client.Error{Status: 404, Tag: "SessionNotFoundError", Message: "Session not found: " + id}
		}
		return map[string]any{"id": id, "location": map[string]string{"directory": "/tmp/project"}}, nil
	case method == "POST" && strings.HasSuffix(path, "/fork"):
		id := segment()
		if s.empty[id] {
			return nil, &client.Error{Status: 400, Tag: "InvalidRequestError", Message: "Cannot fork empty session: " + id}
		}
		return map[string]any{"id": "ses_fork", "location": map[string]string{"directory": "/tmp/project"}}, nil
	case method == "POST" && (strings.HasSuffix(path, "/prompt") || strings.HasSuffix(path, "/command")):
		s.inbox++
		return map[string]string{"id": fmt.Sprintf("msg_%d", s.inbox)}, nil
	}
	return nil, nil
}

func (s *fakeServer) Request(ctx context.Context, method, path string, body any, timeout time.Duration) (jsontext.Value, error) {
	s.mu.Lock()
	s.calls = append(s.calls, call{method, path, string(encode(body))})
	s.mu.Unlock()
	var answer any
	var err error
	handled := false
	if s.answer != nil {
		answer, err, handled = s.answer(method, path)
	}
	if !handled {
		answer, err = s.script(method, path)
	}
	if s.after != nil {
		s.after(s, method, path)
	}
	if err != nil {
		return nil, err
	}
	return encode(answer), nil
}

func (s *fakeServer) Subscribe(listener func(client.Event)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.next
	s.next++
	s.listeners[id] = listener
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

func (s *fakeServer) emit(kind string, data map[string]any) {
	s.mu.Lock()
	listeners := make([]func(client.Event), 0, len(s.listeners))
	for _, listener := range s.listeners {
		listeners = append(listeners, listener)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		listener(client.Event{Type: kind, Data: encode(data)})
	}
}

func (s *fakeServer) sent(method, suffix string) []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found []call
	for _, c := range s.calls {
		if c.method == method && strings.HasSuffix(c.path, suffix) {
			found = append(found, c)
		}
	}
	return found
}

func (s *fakeServer) allCalls() []call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]call(nil), s.calls...)
}

func (s *fakeServer) end(code int, stderr string) {
	s.exitOnce.Do(func() {
		s.mu.Lock()
		s.exit = &client.ExitStatus{Code: &code, Stderr: stderr}
		s.mu.Unlock()
		close(s.exited)
	})
}

func (s *fakeServer) die(stderr string) { s.end(1, stderr) }

func (s *fakeServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeServer) Exited() <-chan struct{} { return s.exited }

func (s *fakeServer) Exit() *client.ExitStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}

func (s *fakeServer) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.end(0, "")
}

func until(t *testing.T, check func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("%s never happened", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func bodyOf(t *testing.T, c call) map[string]any {
	t.Helper()
	var body map[string]any
	if err := jsonx.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatalf("body %q: %v", c.body, err)
	}
	return body
}

func sameJSON(t *testing.T, got any, want string) {
	t.Helper()
	var left, right any
	if err := jsonx.Unmarshal(encode(got), &left); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(want), &right); err != nil {
		t.Fatal(err)
	}
	if string(encode(left)) != string(encode(right)) {
		t.Fatalf("got  %s\nwant %s", encode(left), encode(right))
	}
}

func decodeInto(raw []byte, into any) error {
	return jsonx.Unmarshal(raw, into)
}
