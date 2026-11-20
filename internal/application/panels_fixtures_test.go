package application

import (
	"encoding/json/jsontext"
	"fmt"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// panelsPorted runs body, skipping the test while it reaches a function another group has yet to port.
func panelsPorted(t *testing.T, body func()) {
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

// panelsTask is a thread as the TS fixtures' task() makes one.
func panelsTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
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

// panelsWorkspace is the empty workspace with edits made to it.
func panelsWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(state)
	}
	return state
}

// panelsOn is a workspace holding threads, the first of them on screen.
func panelsOn(threads ...*domain.Thread) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.Threads = threads
		if len(threads) > 0 {
			state.CurrentID = new(threads[0].ID)
		}
	}
}

func panelsIn(t *testing.T, text string) WorkspaceInput {
	t.Helper()
	var input WorkspaceInput
	if err := jsonx.Unmarshal([]byte(text), &input); err != nil {
		t.Fatalf("input %s: %v", text, err)
	}
	return input
}

func panelsReduce(t *testing.T, state *WorkspaceState, text string) (*WorkspaceState, WorkspaceTransition) {
	t.Helper()
	transition := Reduce(state, panelsIn(t, text))
	return &transition.State, transition
}

// panelsRun drives inputs one after another, the way the TS fixtures' run() does.
func panelsRun(t *testing.T, state *WorkspaceState, texts ...string) *WorkspaceState {
	t.Helper()
	for _, text := range texts {
		state, _ = panelsReduce(t, state, text)
	}
	return state
}

// panelsDock is the dock a thread was left in: the one on screen unless a thread is named.
func panelsDock(state *WorkspaceState, owner ...string) *ThreadDock {
	if len(owner) > 0 {
		return DockFor(state, owner[0])
	}
	return DockFor(state, DockOwner(state, nil))
}

// panelsGeneric is value as JSON decodes it untyped, for comparing with what a test expects.
func panelsGeneric(t *testing.T, value any) any {
	t.Helper()
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := jsonx.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	return generic
}

func panelsParse(t *testing.T, text string) any {
	t.Helper()
	var generic any
	if err := jsonx.Unmarshal([]byte(text), &generic); err != nil {
		t.Fatalf("expected %s: %v", text, err)
	}
	return generic
}

// panelsEqual fails unless value encodes as want does.
func panelsEqual(t *testing.T, value any, want string, message string) {
	t.Helper()
	got, expected := panelsGeneric(t, value), panelsParse(t, want)
	if !reflect.DeepEqual(got, expected) {
		encoded, _ := jsonx.Marshal(value)
		t.Errorf("%s\n got: %s\nwant: %s", message, encoded, want)
	}
}

// panelsSubset is whether got holds every field want names, as TS's toMatchObject reads.
func panelsSubset(got, want any) bool {
	switch want := want.(type) {
	case map[string]any:
		held, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range want {
			if !panelsSubset(held[key], value) {
				return false
			}
		}
		return true
	case []any:
		held, ok := got.([]any)
		if !ok || len(held) != len(want) {
			return false
		}
		for index := range want {
			if !panelsSubset(held[index], want[index]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(got, want)
}

// panelsEffects is the transition's effects, untyped, keeping those whose type passes keep (nil keeps all).
func panelsEffects(t *testing.T, transition WorkspaceTransition, keep func(kind string) bool) []any {
	t.Helper()
	var effects []any
	for _, effect := range panelsGeneric(t, transition.Effects).([]any) {
		kind, _ := effect.(map[string]any)["type"].(string)
		if keep == nil || keep(kind) {
			effects = append(effects, effect)
		}
	}
	if effects == nil {
		effects = []any{}
	}
	return effects
}

// panelsHasEffect is whether some effect holds every field of want.
func panelsHasEffect(t *testing.T, transition WorkspaceTransition, want string) bool {
	t.Helper()
	expected := panelsParse(t, want)
	for _, effect := range panelsEffects(t, transition, nil) {
		if panelsSubset(effect, expected) {
			return true
		}
	}
	return false
}

func panelsEffectOf(t *testing.T, transition WorkspaceTransition, kind string) map[string]any {
	t.Helper()
	for _, effect := range panelsEffects(t, transition, nil) {
		if effect.(map[string]any)["type"] == kind {
			return effect.(map[string]any)
		}
	}
	t.Fatalf("expected effect %s", kind)
	return nil
}

func panelsLastEffect(t *testing.T, transition WorkspaceTransition) any {
	t.Helper()
	effects := panelsEffects(t, transition, nil)
	if len(effects) == 0 {
		t.Fatal("expected an effect")
	}
	return effects[len(effects)-1]
}

func panelsThread(state *WorkspaceState, id string) *domain.Thread {
	return threadsFind(state.Threads, id)
}

func jsonxMarshal(value any) ([]byte, error)      { return jsonx.Marshal(value) }
func jsonxUnmarshal(data []byte, value any) error { return jsonx.Unmarshal(data, value) }

func sameValueOf[T any](a, b T) bool { return jsonx.Same(a, b) }

// panelsPatched is a copy of state with the fields json names replaced by its values, nothing else
// it holds written to.
func panelsPatched(t *testing.T, state *WorkspaceState, text string) *WorkspaceState {
	t.Helper()
	var fields map[string]jsontext.Value
	if err := jsonx.Unmarshal([]byte(text), &fields); err != nil {
		t.Fatalf("patch %s: %v", text, err)
	}
	next := *state
	target := reflect.ValueOf(&next).Elem()
	for name, value := range fields {
		found := false
		for index := range target.NumField() {
			tag, _, _ := strings.Cut(target.Type().Field(index).Tag.Get("json"), ",")
			if tag != name {
				continue
			}
			field := reflect.New(target.Field(index).Type())
			if err := jsonx.Unmarshal(value, field.Interface()); err != nil {
				t.Fatalf("patch %s: %v", name, err)
			}
			target.Field(index).Set(field.Elem())
			found = true
			break
		}
		if !found {
			t.Fatalf("no WorkspaceState field %s", name)
		}
	}
	return &next
}

func panelsCapabilities() []string { return contracts.ComputerCapabilitiesComputerCapabilities() }
