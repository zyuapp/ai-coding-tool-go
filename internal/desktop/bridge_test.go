package desktop

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingBackend struct {
	mu     sync.Mutex
	inputs []string
}

func (backend *recordingBackend) Request(input jsontext.Value) Reply {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.inputs = append(backend.inputs, string(input))
	return Settled(jsontext.Value(`{"ok":true,"revision":1}`), nil)
}
func (backend *recordingBackend) Migrate(values map[string]string) Reply { return Settled(nil, nil) }
func (backend *recordingBackend) Subscribe(WorkspaceView) func()         { return func() {} }
func (backend *recordingBackend) SetViewVisible(bool)                    {}
func (backend *recordingBackend) SetViewInUse(bool)                      {}

type recordingCalls struct {
	mu    sync.Mutex
	names []string
}

func (calls *recordingCalls) Call(ctx context.Context, name string, args []jsontext.Value) Reply {
	calls.mu.Lock()
	defer calls.mu.Unlock()
	calls.names = append(calls.names, name)
	return Settled(jsontext.Value(`null`), nil)
}

func TestRequestsStartInTheOrderTheWindowMadeThem(t *testing.T) {
	backend := &recordingBackend{}
	bridge := New(Options{ProfileDir: t.TempDir(), Workspace: backend}).bridge
	var done sync.WaitGroup
	for seq := 5; seq >= 1; seq-- {
		done.Add(1)
		go func() {
			defer done.Done()
			if _, err := bridge.Request(Ticket{Page: "p", Seq: seq}, jsontext.Value(`{"n":`+string(rune('0'+seq))+`}`)); err != nil {
				t.Error(err)
			}
		}()
		time.Sleep(5 * time.Millisecond)
	}
	done.Wait()
	if got := strings.Join(backend.inputs, ""); got != `{"n":1}{"n":2}{"n":3}{"n":4}{"n":5}` {
		t.Fatalf("started as %s", got)
	}
}

func TestAnAbsentInputAsksForASnapshot(t *testing.T) {
	backend := &recordingBackend{}
	bridge := New(Options{ProfileDir: t.TempDir(), Workspace: backend}).bridge
	if _, err := bridge.Request(Ticket{Page: "p", Seq: 1, Absent: []int{0}}, jsontext.Value("null")); err != nil {
		t.Fatal(err)
	}
	if len(backend.inputs) != 1 || backend.inputs[0] != "" {
		t.Fatalf("inputs %q", backend.inputs)
	}
}

func TestCallsPassTheirGuardsFirst(t *testing.T) {
	calls := &recordingCalls{}
	bridge := New(Options{ProfileDir: t.TempDir(), Calls: calls}).bridge
	seq := 0
	call := func(name string, absent []int, args ...string) error {
		seq++
		values := make([]jsontext.Value, len(args))
		for index, arg := range args {
			values[index] = jsontext.Value(arg)
		}
		_, err := bridge.Call(Ticket{Page: "p", Seq: seq, Absent: absent}, name, values)
		return err
	}
	if err := call("nope", nil); err == nil || err.Error() != "Unknown desktop call." {
		t.Fatalf("unknown call: %v", err)
	}
	if err := call(CallDiffPatch, []int{3}, `"w"`, `{"kind":"uncommitted"}`, `"a.go"`, `null`, `true`); err != nil {
		t.Fatalf("an undefined optional argument was refused: %v", err)
	}
	if err := call(CallDiffPatch, nil, `"w"`, `{"kind":"uncommitted"}`, `"a.go"`, `null`); err == nil || err.Error() != "Invalid diffPatch call." {
		t.Fatalf("a null optional argument passed: %v", err)
	}
	if err := call(CallBranches, nil, `"w"`, `"extra"`); err == nil {
		t.Fatal("an extra argument passed")
	}
	if err := call(CallCommits, nil, `"w"`, `["xyz"]`); err == nil {
		t.Fatal("a bad hash passed")
	}
	if err := call(CallSetTheme, nil, `{"variant":"dark","canvas":"#0e1117","follow":null}`); err == nil {
		t.Fatal("a null follow passed")
	}
	if err := call(CallBranches, nil, `"w"`); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls.names, ",") != "diffPatch,branches" {
		t.Fatalf("ran %v", calls.names)
	}
}

func TestCallsWaitForTheServicesButFrameCallsDoNot(t *testing.T) {
	calls := &recordingCalls{}
	ready := make(chan struct{})
	bridge := New(Options{ProfileDir: t.TempDir(), Calls: calls, Ready: ready}).bridge
	results := make(chan string, 3)
	for seq, name := range []string{CallBranches, CallFocusBrowserTab} {
		go func() {
			_, _ = bridge.Call(Ticket{Page: "p", Seq: seq + 1}, name, []jsontext.Value{jsontext.Value(`"id"`)})
			results <- name
		}()
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := bridge.Call(Ticket{Page: "p", Seq: 3}, CallSetBrowserBounds, []jsontext.Value{jsontext.Value(`null`)}); err != nil {
		t.Fatal(err)
	}
	calls.mu.Lock()
	early := strings.Join(calls.names, ",")
	calls.mu.Unlock()
	if early != "setBrowserBounds" {
		t.Fatalf("before the services were up, ran %q", early)
	}
	close(ready)
	<-results
	<-results
	if strings.Join(calls.names, ",") != "setBrowserBounds,branches,focusBrowserTab" {
		t.Fatalf("ran %v", calls.names)
	}
}

func TestMigrateTakesOnlyTheWindowsOwnValues(t *testing.T) {
	bridge := New(Options{ProfileDir: t.TempDir(), Workspace: &recordingBackend{}}).bridge
	if err := bridge.Migrate(Ticket{Page: "p", Seq: 1}, jsontext.Value(`{"aicodingtool.view":"{}"}`)); err != nil {
		t.Fatal(err)
	}
	for seq, values := range []string{`{"other":"x"}`, `{"aicodingtool.view":1}`, `[]`} {
		if err := bridge.Migrate(Ticket{Page: "p", Seq: seq + 2}, jsontext.Value(values)); err == nil || err.Error() != "Invalid stored values." {
			t.Fatalf("%s: %v", values, err)
		}
	}
}

func TestDescribeFiles(t *testing.T) {
	dir := t.TempDir()
	described, err := describeFiles(context.Background(), jsontext.Value(`["`+dir+`","`+dir+`/missing",3]`))
	if err != nil || len(described) != 1 || !described[0].Folder || described[0].Path != dir {
		t.Fatalf("described %+v %v", described, err)
	}
}
