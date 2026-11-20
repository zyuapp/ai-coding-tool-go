package computers

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// electronCheckout is the TypeScript app beside this repository, built, or "" when it is not there.
func electronCheckout(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	checkout := filepath.Join(filepath.Dir(file), "..", "..", "..", "ai-coding-tool")
	if _, err := os.Stat(filepath.Join(checkout, "dist", "main", "main", "mobile", "mobile-server.mjs")); err != nil {
		t.Skip("needs ../ai-coding-tool built (npm run build)")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("needs node")
	}
	return checkout
}

// electronHost is a host on the Electron app's own bridge, driven from the test.
type electronHost struct {
	t     *testing.T
	mu    sync.Mutex
	in    io.WriteCloser
	out   *bufio.Scanner
	next  int
	url   string
	child *exec.Cmd
}

func startElectronHost(t *testing.T) *electronHost {
	t.Helper()
	checkout := electronCheckout(t)
	_, file, _, _ := runtime.Caller(0)
	child := exec.Command("node", filepath.Join(filepath.Dir(file), "testdata", "host.mjs"), checkout)
	child.Stderr = os.Stderr
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	out := bufio.NewScanner(stdout)
	out.Buffer(make([]byte, 1<<20), 64<<20)
	if !out.Scan() {
		t.Fatal("the host never started")
	}
	host := &electronHost{t: t, in: in, out: out, child: child}
	t.Cleanup(func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = child.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
		}
	})
	host.url = host.call("url", nil).(string)
	return host
}

func (h *electronHost) call(op string, args map[string]any) any {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	request := map[string]any{"id": h.next, "op": op}
	for key, value := range args {
		request[key] = value
	}
	data, err := jsonx.Marshal(request)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.in.Write(append(data, '\n')); err != nil {
		h.t.Fatal(err)
	}
	if !h.out.Scan() {
		h.t.Fatalf("the host stopped answering %s", op)
	}
	var reply struct {
		OK      bool           `json:"ok"`
		Value   jsontext.Value `json:"value"`
		Message string         `json:"message"`
	}
	if err := jsonx.Unmarshal(h.out.Bytes(), &reply); err != nil {
		h.t.Fatal(err)
	}
	if !reply.OK {
		h.t.Fatalf("%s failed on the host: %s", op, reply.Message)
	}
	var value any
	_ = jsonx.Unmarshal(reply.Value, &value)
	return value
}

func (h *electronHost) mint() string { return h.call("mint", nil).(string) }

func (h *electronHost) inputs() []map[string]any {
	var inputs []map[string]any
	for _, input := range h.call("inputs", nil).([]any) {
		inputs = append(inputs, input.(map[string]any))
	}
	return inputs
}

func (h *electronHost) renames() int {
	count := 0
	for _, input := range h.inputs() {
		if input["type"] == "task.rename" {
			count++
		}
	}
	return count
}

// until polls check until it holds.
func until(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// cuttable is a line between a computer and its host that can be cut the way a dropped network cuts
// it, with neither end told. target is read for every new connection.
type cuttable struct {
	url  string
	mu   sync.Mutex
	open map[net.Conn]bool
}

func newCuttable(t *testing.T, target func() string) *cuttable {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	where, err := url.Parse(target())
	if err != nil {
		t.Fatal(err)
	}
	line := &cuttable{url: "ws://" + listener.Addr().String() + where.Path, open: map[net.Conn]bool{}}
	go func() {
		for {
			inbound, err := listener.Accept()
			if err != nil {
				return
			}
			now, _ := url.Parse(target())
			outbound, err := net.Dial("tcp", now.Host)
			if err != nil {
				_ = inbound.Close()
				continue
			}
			line.mu.Lock()
			line.open[inbound], line.open[outbound] = true, true
			line.mu.Unlock()
			pipe := func(to, from net.Conn) {
				_, _ = io.Copy(to, from)
				_ = to.Close()
				_ = from.Close()
			}
			go pipe(inbound, outbound)
			go pipe(outbound, inbound)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		line.cut()
	})
	return line
}

func (c *cuttable) cut() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for conn := range c.open {
		_ = conn.Close()
	}
	c.open = map[net.Conn]bool{}
}

type statusSeen struct {
	status domain.ComputerStatus
	err    string
}

// recorded is a client and everything it told.
type recorded struct {
	client   *ComputerClient
	mu       sync.Mutex
	statuses []statusSeen
	states   []*application.WorkspaceState
	notices  []contracts.ThreadNotice
	caps     []contracts.ComputerCapabilities
	deviceID string
	token    string
}

func record(options ClientOptions) *recorded {
	r := &recorded{}
	if options.Host == "" {
		options.Host = "other.tail.ts.net"
	}
	if options.DeviceName == "" {
		options.DeviceName = "This Mac"
	}
	options.OnStatus = func(status domain.ComputerStatus, err *string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		seen := statusSeen{status: status}
		if err != nil {
			seen.err = *err
		}
		r.statuses = append(r.statuses, seen)
	}
	options.OnPaired = func(deviceID, _ string, token string) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.deviceID, r.token = deviceID, token
	}
	options.OnState = func(state *application.WorkspaceState) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.states = append(r.states, state)
	}
	options.OnNotice = func(notice contracts.ThreadNotice) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.notices = append(r.notices, notice)
	}
	options.OnCapabilities = func(capabilities contracts.ComputerCapabilities) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.caps = append(r.caps, capabilities)
	}
	r.client = NewComputerClient(options)
	return r
}

func (r *recorded) paired() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.token, r.token != ""
}

func (r *recorded) count(status domain.ComputerStatus) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, seen := range r.statuses {
		if seen.status == status {
			count++
		}
	}
	return count
}

func (r *recorded) saw(check func(statusSeen) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.statuses {
		if check(seen) {
			return true
		}
	}
	return false
}

func (r *recorded) lastState() *application.WorkspaceState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) == 0 {
		return nil
	}
	return r.states[len(r.states)-1]
}

func (r *recorded) stateCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.states)
}

func rename(taskID, title string) application.WorkspaceInput {
	return contracts.TaskCommandTaskRename{TaskID: taskID, Title: title}
}

func background() context.Context { return context.Background() }

func itoa(n int) string { return strconv.Itoa(n) }

func sleep(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }
