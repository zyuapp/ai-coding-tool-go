package application

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// corePorted runs body, skipping the test while it reaches a function another group has yet to port.
func corePorted(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if text := fmt.Sprint(recovered); strings.Contains(text, "unported: ") {
				t.Skipf("blocked: %s", text)
			}
			panic(recovered)
		}
	}()
	body()
}

// coreTask is a thread as the TS fixtures' task() makes one.
func coreTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title = id, id
	thread.Engine = domain.AgentEngineClaude
	thread.ExecutionPolicy = domain.ExecutionPolicyConfirm
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt = 1
	for _, edit := range edits {
		edit(thread)
	}
	if thread.RankedAt == 0 {
		thread.RankedAt = domain.ThreadLastOutputAt(thread)
	}
	return thread
}

// coreActive is a live run as the TS fixtures' activeRun() makes one.
func coreActive(taskID, runID string) ActiveRun {
	return ActiveRunRunning{
		TaskID:         taskID,
		RunID:          runID,
		Status:         ActiveRunRunningStatusRunning,
		Origin:         ActiveRunRunningOriginComposer,
		ReportedIssues: []string{},
		Before:         ThreadMark{UpdatedAt: 1},
	}
}

// coreWorkspace is the empty workspace with edits made to it.
func coreWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(state)
	}
	return state
}

// coreStored is thread as the store holds it.
func coreStored(t *testing.T, thread *domain.Thread) *domain.StoredThread {
	t.Helper()
	encoded, err := jsonx.Marshal(thread)
	if err != nil {
		t.Fatal(err)
	}
	stored := &domain.StoredThread{}
	if err := jsonx.Unmarshal(encoded, stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// coreRun reduces each input in turn, the way the renderer drives the reducer.
func coreRun(state *WorkspaceState, inputs ...WorkspaceInput) *WorkspaceState {
	for _, input := range inputs {
		transition := Reduce(state, input)
		state = &transition.State
	}
	return state
}

// coreJSON is value as JSON, for comparing what TypeScript compares with deepEqual.
func coreJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// coreEqualJSON fails unless got and want encode to the same JSON, up to key order. want may be a JSON text.
func coreEqualJSON(t *testing.T, got, want any, what string) {
	t.Helper()
	g := coreJSON(t, got)
	w, isText := want.(string)
	if !isText {
		w = coreJSON(t, want)
	}
	var gotValue, wantValue any
	if err := jsonx.Unmarshal([]byte(g), &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(w), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("%s:\n got %s\nwant %s", what, g, w)
	}
}

// coreStorage is a key-value storage held in memory.
type coreStorage map[string]string

func (s coreStorage) GetItem(key string) *string {
	if value, ok := s[key]; ok {
		return &value
	}
	return nil
}

func (s coreStorage) SetItem(key, value string) { s[key] = value }

// coreEffect is the first effect of type E a transition carries.
func coreEffect[E WorkspaceEffect](t *testing.T, transition WorkspaceTransition) E {
	t.Helper()
	for _, effect := range transition.Effects {
		if found, ok := effect.(E); ok {
			return found
		}
	}
	var zero E
	t.Fatalf("expected effect %T in %s", zero, coreJSON(t, transition.Effects))
	return zero
}

func corePin(t *testing.T, at float64) {
	t.Helper()
	held := Now
	Now = func() float64 { return at }
	t.Cleanup(func() { Now = held })
}
