package client

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
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
)

// The test binary stands in for `opencode serve` when FAKE_OPENCODE is set: it serves the few
// routes these tests read, behind a password, and notes its pid.
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_OPENCODE") != "" {
		runFakeOpenCode()
		return
	}
	os.Exit(m.Run())
}

func runFakeOpenCode() {
	if os.Getenv("FAKE_IGNORE_TERM") != "" {
		signal.Ignore(syscall.SIGTERM)
	}
	_ = os.WriteFile(os.Getenv("FAKE_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	secret := fmt.Sprintf("pw-%d", os.Getpid())
	expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("opencode:"+secret))
	cwd, _ := os.Getwd()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("authorization") != expected {
			w.WriteHeader(401)
			w.Write([]byte(`{"_tag":"UnauthorizedError","message":"Authentication required"}`))
			return
		}
		switch r.URL.Path {
		case "/api/model":
			body, _ := json.Marshal(map[string]any{"location": map[string]any{"directory": cwd}, "data": []any{map[string]any{"id": "m", "providerID": "p", "config": os.Getenv("OPENCODE_CONFIG_CONTENT")}}})
			w.Write(body)
		case "/api/missing":
			w.WriteHeader(404)
			w.Write([]byte(`{"_tag":"SessionNotFoundError","message":"Session not found: x"}`))
		case "/api/plain":
			w.WriteHeader(500)
			w.Write([]byte("boom"))
		case "/api/echo":
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			fmt.Fprintf(w, `{"method":%q,"contentType":%q,"body":%s}`, r.Method, r.Header.Get("content-type"), body)
		case "/api/slow":
			time.Sleep(2 * time.Second)
			w.Write([]byte("{}"))
		case "/api/event":
			w.Header().Set("content-type", "text/event-stream")
			flusher := w.(http.Flusher)
			w.Write([]byte("data: {\"type\":\"server.connected\"}\n\n"))
			w.Write([]byte("data: {\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"s\",\"delta\":\"he\"}}\n\ndata: {\"type\":\"session.text.de"))
			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
			w.Write([]byte("lta\",\"data\":{\"sessionID\":\"s\",\"delta\":\"llo\"}}\n\n: heartbeat\n\n"))
			flusher.Flush()
			if os.Getenv("FAKE_FEED_ENDS") != "" {
				return
			}
			<-r.Context().Done()
		default:
			w.WriteHeader(404)
		}
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if os.Getenv("FAKE_SPACED") != "" {
		fmt.Println("Warning: OPENCODE_SERVER_PASSWORD is not set, generating one")
		go func() {
			time.Sleep(50 * time.Millisecond)
			fmt.Printf("Server listening on http://127.0.0.1:%d\n", port)
			time.Sleep(50 * time.Millisecond)
			fmt.Println("server password " + secret)
		}()
	} else {
		fmt.Printf("Server listening on http://127.0.0.1:%d\n", port)
		fmt.Println("server password " + secret)
	}
	_ = http.Serve(listener, handler)
}

type fake struct {
	executable string
	pidFile    string
	env        []string
}

func newFake(t *testing.T, extra ...string) fake {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	return fake{executable: executable, pidFile: pidFile, env: append(append(os.Environ(), "FAKE_OPENCODE=1", "FAKE_PID_FILE="+pidFile), extra...)}
}

func (f fake) pid(t *testing.T) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if data, err := os.ReadFile(f.pidFile); err == nil && len(data) > 0 {
			pid, _ := strconv.Atoi(string(data))
			return pid
		}
	}
	t.Fatal("the fake never noted its pid")
	return 0
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func gone(pid int) bool {
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !alive(pid) {
			return true
		}
	}
	return false
}

func start(t *testing.T, f fake, config any) *Server {
	t.Helper()
	server, err := Start(context.Background(), Options{Cwd: t.TempDir(), Config: config, Executable: f.executable, Env: f.env})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	return server
}

func TestAServerStartsBehindItsPasswordWithItsConfigAndStopsOnClose(t *testing.T) {
	f := newFake(t)
	server := start(t, f, map[string]any{"mcp": map[string]any{"app": map[string]any{"type": "remote"}}})
	ctx := context.Background()
	models, err := RequestAs[[]struct {
		Config string `json:"config"`
	}](ctx, server, "GET", "/api/model", nil, 0)
	if err != nil || len(models) != 1 || models[0].Config != `{"mcp":{"app":{"type":"remote"}}}` {
		t.Fatalf("%v %v", models, err)
	}
	_, err = server.Request(ctx, "GET", "/api/missing", nil, 0)
	var failure *Error
	if !errors.As(err, &failure) || failure.Status != 404 || failure.Tag != "SessionNotFoundError" || failure.Error() != "Session not found: x" {
		t.Fatalf("%v", err)
	}
	_, err = server.Request(ctx, "GET", "/api/plain", nil, 0)
	if !errors.As(err, &failure) || failure.Error() != "500 Internal Server Error" || failure.Tag != "" {
		t.Fatalf("an untagged error is named by its status: %v", err)
	}
	echo, err := RequestAs[map[string]any](ctx, server, "PATCH", "/api/echo", map[string]any{"title": "t"}, 0)
	if err != nil || echo["method"] != "PATCH" || echo["contentType"] != "application/json" || echo["body"].(map[string]any)["title"] != "t" {
		t.Fatalf("%v %v", echo, err)
	}
	if _, err := server.Request(ctx, "GET", "/api/slow", nil, 50*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("a request past its time: %v", err)
	}

	var mu sync.Mutex
	var events []Event
	server.Subscribe(func(event Event) {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		mu.Lock()
		count := len(events)
		mu.Unlock()
		if count >= 3 {
			break
		}
	}
	mu.Lock()
	got := fmt.Sprintf("%v", events)
	mu.Unlock()
	if got != `[{server.connected null} {session.text.delta {"sessionID":"s","delta":"he"}} {session.text.delta {"sessionID":"s","delta":"llo"}}]` {
		t.Fatalf("a frame split across chunks arrives whole, and a comment is skipped: %s", got)
	}
	pid := f.pid(t)
	server.Close()
	if !gone(pid) {
		t.Fatal("closing stops the server")
	}
}

func TestAFeedThatEndsTellsEveryListener(t *testing.T) {
	server := start(t, newFake(t, "FAKE_FEED_ENDS=1"), nil)
	ended := make(chan struct{})
	var once sync.Once
	server.Subscribe(func(event Event) {
		if event.Type == FeedEnded {
			once.Do(func() { close(ended) })
		}
	})
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("no feed-ended event")
	}
}

func TestAServerOutlivesNothing(t *testing.T) {
	f := newFake(t)
	cmd := exec.Command("/bin/sh", "-c", WatchedServe, f.executable)
	cmd.Env = f.env
	stdin, _ := cmd.StdinPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := f.pid(t)
	stdin.Close()
	if !gone(pid) {
		t.Fatal("the server stopped once the app was gone")
	}
	_ = cmd.Wait()
}

func TestAServerThatIgnoresBeingAskedToStopIsKilled(t *testing.T) {
	f := newFake(t, "FAKE_IGNORE_TERM=1")
	server := start(t, f, nil)
	pid := f.pid(t)
	server.Close()
	if !gone(pid) {
		t.Fatal("a server deaf to SIGTERM is killed after its grace")
	}
}

func TestAServerThatMentionsItsPasswordElsewhereStillStarts(t *testing.T) {
	server := start(t, newFake(t, "FAKE_SPACED=1"), nil)
	if _, err := server.Request(context.Background(), "GET", "/api/model", nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestAServerThatDiesBeforeItIsReadyReportsWhy(t *testing.T) {
	script := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'config is invalid' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Start(context.Background(), Options{Cwd: t.TempDir(), Executable: script})
	var exited *Exited
	if !errors.As(err, &exited) || err.Error() != "config is invalid" {
		t.Fatalf("%v", err)
	}
}

type scripted struct {
	mu      sync.Mutex
	answers []string
	asked   int
}

func (s *scripted) Request(context.Context, string, string, any, time.Duration) (jsontext.Value, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer := s.answers[min(s.asked, len(s.answers)-1)]
	s.asked++
	return jsontext.Value(answer), nil
}

func TestAListReadWhileTheServerLoadsIsReadUntilItSettles(t *testing.T) {
	ctx := context.Background()
	growing := &scripted{answers: []string{`[]`, `["a"]`, `["a","b"]`, `["a","b"]`}}
	list, _ := ReadLoaded[string](ctx, growing, "/api/skill", time.Millisecond)
	if strings.Join(list, ",") != "a,b" || growing.asked != 4 {
		t.Fatalf("%v %d", list, growing.asked)
	}
	empty := &scripted{answers: []string{`[]`}}
	list, _ = ReadLoaded[string](ctx, empty, "/api/skill", time.Millisecond)
	if len(list) != 0 || empty.asked != 10 {
		t.Fatalf("an empty folder is believed after a few reads: %d", empty.asked)
	}
	steady := &scripted{answers: []string{`["a"]`}}
	_, _ = ReadLoaded[string](ctx, steady, "/api/command", time.Millisecond)
	if steady.asked != 2 {
		t.Fatal(steady.asked)
	}
	list, _ = ReadLoaded[string](ctx, steady, "/api/command", time.Millisecond)
	if steady.asked != 3 || strings.Join(list, ",") != "a" {
		t.Fatalf("a list the server has loaded is read once from then on: %d", steady.asked)
	}
	_, _ = ReadLoaded[string](ctx, steady, "/api/skill", time.Millisecond)
	if steady.asked != 5 {
		t.Fatalf("another list is still waited on: %d", steady.asked)
	}
}

func TestAStartThatNeverSaidHowToReachItBlamesTheLastLineNamingWhatIsMissing(t *testing.T) {
	cases := []struct {
		output, want string
		ok           bool
	}{
		{"Loading models…\n", "", false},
		{"Server listening on http://127.0.0.1:5555\n", "", false},
		{"Ready, listening at port 5555\nserver password abc\n", "Ready, listening at port 5555", true},
		{"Warning: OPENCODE_SERVER_PASSWORD is not set\nServer listening on http://127.0.0.1:5555\npassword: abc\n", "password: abc", true},
		{"Warning: OPENCODE_SERVER_PASSWORD is not set\nReady, listening at port 5555\nserver password abc\n", "Ready, listening at port 5555", true},
	}
	for _, c := range cases {
		if got, ok := UnreadLine(c.output); got != c.want || ok != c.ok {
			t.Errorf("%q: got %q %v", c.output, got, ok)
		}
	}
}

func TestParseFrame(t *testing.T) {
	event, ok := ParseFrame("event: x\ndata: {\"type\":\"a\",\ndata:  \"data\":[1]}")
	if !ok || event.Type != "a" || event.Data != nil {
		t.Fatalf("%v %v", event, ok)
	}
	if _, ok := ParseFrame(": heartbeat"); ok {
		t.Fatal("a comment is no event")
	}
	if _, ok := ParseFrame(`data: {"data":{}}`); ok {
		t.Fatal("an event needs a type")
	}
}
