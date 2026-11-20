package mobile

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// fakeWorkspace is a workspace another computer reads and drives.
type fakeWorkspace struct {
	mu        sync.Mutex
	revision  float64
	listeners map[int]func(application.WorkspaceUpdate)
	next      int
	inputs    [][]application.WorkspaceInput
	queries   []contracts.ComputerQuery
}

func (w *fakeWorkspace) Name() string { return "Studio" }

func (w *fakeWorkspace) Snapshot() application.WorkspaceUpdate {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := &application.WorkspaceState{}
	state.Computers.Paired = []*application.PairedComputer{{State: &application.WorkspaceState{}}}
	return application.WorkspaceUpdate{Revision: w.revision, State: state}
}

func (w *fakeWorkspace) Subscribe(listener func(application.WorkspaceUpdate)) func() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.listeners == nil {
		w.listeners = map[int]func(application.WorkspaceUpdate){}
	}
	id := w.next
	w.next++
	w.listeners[id] = listener
	return func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		delete(w.listeners, id)
	}
}

func (w *fakeWorkspace) publish(patches []*contracts.WorkspacePatch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.revision++
	for _, listener := range w.listeners {
		listener(application.WorkspaceUpdate{Revision: w.revision, Patches: patches})
	}
}

func (w *fakeWorkspace) Input(_ context.Context, inputs []application.WorkspaceInput) (contracts.ComputerServerMessageResultResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inputs = append(w.inputs, inputs)
	return contracts.ComputerServerMessageResultResultTrue{Revision: w.revision}, nil
}

func (w *fakeWorkspace) Query(_ context.Context, query contracts.ComputerQuery) (any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.queries = append(w.queries, query)
	return []string{"/work"}, nil
}

func (w *fakeWorkspace) inputCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inputs)
}

func TestAComputerPairsIsHandedTheWorkspaceAndDrivesIt(t *testing.T) {
	devices := NewPairingStore(filepath.Join(t.TempDir(), "mobile-devices.v1.json"))
	workspace := &fakeWorkspace{}
	server := NewServer(ServerOptions{
		Devices:        devices,
		AllowedOrigins: func() []string { return nil },
		Snapshot:       func(context.Context, string) (*contracts.MobileView, error) { return nil, fmt.Errorf("no view") },
		Command:        func(context.Context, string, contracts.MobileCommand) error { return nil },
		Query:          func(context.Context, string, contracts.MobileQuery) (jsontext.Value, error) { return nil, nil },
		Workspace:      workspace,
	})
	if err := server.Start("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	computer := newPhone(t, fmt.Sprintf("ws://127.0.0.1:%d/m/workspace", *server.Port()), nil)
	computer.send(message{"kind": "pair", "version": contracts.ComputerProtocolVersion, "code": devices.Mint(application.Now()).Code, "deviceName": "Laptop"})

	paired := computer.waitFor("paired")
	if devices.List()[0].Kind != "computer" || paired["sequence"] != 1.0 {
		t.Fatalf("paired %v", paired)
	}
	if capabilities := computer.waitFor("capabilities"); len(capabilities["capabilities"].([]any)) == 0 || capabilities["instance"] == nil {
		t.Fatalf("capabilities %v", capabilities)
	}
	if name := computer.waitFor("name"); name["name"] != "Studio" {
		t.Fatalf("name %v", name)
	}
	snapshot := computer.waitFor("workspace")
	if snapshot["sessionId"] == nil {
		t.Fatal("the opening workspace names no session")
	}
	paired0 := snapshot["update"].(message)["state"].(message)["computers"].(message)["paired"].([]any)[0].(message)
	if state, has := paired0["state"]; !has || state != nil {
		t.Fatalf("the paired computer's own state was handed over: %v", paired0)
	}

	value, _ := jsonx.Marshal(1)
	workspace.publish([]*contracts.WorkspacePatch{
		{Path: []contracts.PathKey{contracts.KeyStep("computers"), contracts.KeyStep("found")}, Value: value},
		{Path: []contracts.PathKey{contracts.KeyStep("revision")}, Value: value},
	})
	update := computer.find(func(held message) bool {
		return held["kind"] == "workspace" && held["sessionId"] == nil
	}, "the update never arrived")
	patches := update["update"].(message)["patches"].([]any)
	if len(patches) != 1 || patches[0].(message)["path"].([]any)[0] != "revision" {
		t.Fatalf("the computers patch was handed over: %v", patches)
	}

	computer.send(message{"kind": "input", "requestId": "input-1", "inputs": []any{message{"type": "task.select", "taskId": "task-1"}}})
	result := computer.find(func(held message) bool { return held["kind"] == "result" && held["requestId"] == "input-1" }, "no result")
	if result["result"].(message)["ok"] != true || workspace.inputCount() != 1 {
		t.Fatalf("result %v", result)
	}
	computer.send(message{"kind": "input", "requestId": "input-1", "inputs": []any{message{"type": "task.select", "taskId": "task-1"}}})
	until(t, func() bool { return len(computer.all("result")) == 2 }, "the resend went unanswered")
	if workspace.inputCount() != 1 {
		t.Fatal("a resent input ran twice")
	}

	computer.send(message{"kind": "input", "requestId": "input-2", "inputs": []any{message{"type": "no.such-command"}}})
	refused := computer.find(func(held message) bool { return held["kind"] == "result" && held["requestId"] == "input-2" }, "no refusal")
	if refused["result"].(message)["ok"] != false || refused["result"].(message)["message"] != contracts.RemoteUnsupported {
		t.Fatalf("refused %v", refused)
	}

	computer.send(message{"kind": "query", "requestId": "query-1", "query": message{"kind": "directories", "prefix": "/"}})
	answer := computer.find(func(held message) bool { return held["kind"] == "answer" && held["requestId"] == "query-1" }, "no answer")
	if answer["ok"] != true || fmt.Sprint(answer["result"]) != "[/work]" {
		t.Fatalf("answer %v", answer)
	}

	server.Notice(contracts.ThreadNotice{TaskID: "task-1", Title: "Fix", Headline: "Done"})
	computer.waitFor("notice")

	computer.close()
	until(t, func() bool {
		workspace.mu.Lock()
		defer workspace.mu.Unlock()
		for _, inputs := range workspace.inputs {
			if focused, ok := inputs[0].(contracts.ViewCommandViewSetFocused); ok && !focused.Focused {
				return true
			}
		}
		return false
	}, "a computer that hung up still counts as looking")
}

func TestAPhoneCannotOpenTheComputerSocketOnAHostThatTakesNoComputers(t *testing.T) {
	h := newHarness(t)
	_, response, err := dial(t, strings.Replace(h.socketURL(), "/m/socket", "/m/workspace", 1), nil)
	if err == nil || response.StatusCode != 400 {
		t.Fatal("a host with no workspace took a computer")
	}
}
