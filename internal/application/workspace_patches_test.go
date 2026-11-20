package application

import (
	"bufio"
	"compress/gzip"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type patchMessage struct {
	ID   float64 `json:"id"`
	Text string  `json:"text"`
}

type patchThread struct {
	ID       string          `json:"id"`
	Messages []*patchMessage `json:"messages"`
}

type patchProject struct {
	ID string `json:"id"`
}

type patchWorkspace struct {
	Threads  []*patchThread  `json:"threads"`
	Projects []*patchProject `json:"projects"`
	Focused  bool            `json:"focused"`
}

// wire is value as the window holds it: decoded JSON.
func wire(t *testing.T, value any) any {
	t.Helper()
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := jsonx.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func patchesOf(t *testing.T, previous, next any) []*contracts.WorkspacePatch {
	t.Helper()
	patches, err := WorkspacePatches(previous, next, nil)
	if err != nil {
		t.Fatal(err)
	}
	return patches
}

func applied(t *testing.T, state any, patches []*contracts.WorkspacePatch) any {
	t.Helper()
	result, err := ApplyWorkspacePatches(state, patches)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func encodedPatches(t *testing.T, patches []*contracts.WorkspacePatch) string {
	t.Helper()
	encoded, err := jsonx.Marshal(patches)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func sameSlice(a, b any) bool {
	return reflect.ValueOf(a).UnsafePointer() == reflect.ValueOf(b).UnsafePointer()
}

func TestStreamingPatchesCarryOnlyChangedTextAndPreserveUnrelatedReferences(t *testing.T) {
	messages := make([]*patchMessage, 1_000)
	for id := range messages {
		messages[id] = &patchMessage{ID: float64(id), Text: fmt.Sprintf("History %d", id)}
	}
	before := &patchWorkspace{
		Threads:  []*patchThread{{ID: "one", Messages: messages}, {ID: "two", Messages: []*patchMessage{}}},
		Projects: []*patchProject{{ID: "project"}},
		Focused:  true,
	}
	grown := *before.Threads[0]
	grown.Messages = append(slices.Clip(messages), &patchMessage{ID: 1_000, Text: "Next"})
	after := *before
	after.Threads = []*patchThread{&grown, before.Threads[1]}

	patches := patchesOf(t, before, &after)
	if length := len(encodedPatches(t, patches)); length >= 250 {
		t.Fatalf("patches are %d bytes", length)
	}
	held := wire(t, before)
	result := applied(t, held, patches)
	if !reflect.DeepEqual(result, wire(t, &after)) {
		t.Fatal("patched state differs from the next one")
	}
	was, now := held.(map[string]any), result.(map[string]any)
	if !sameSlice(was["projects"], now["projects"]) {
		t.Fatal("unchanged projects were copied")
	}
	if !jsonx.Same(was["threads"].([]any)[1].(map[string]any), now["threads"].([]any)[1].(map[string]any)) {
		t.Fatal("unchanged thread was copied")
	}
	if !jsonx.Same(was["threads"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any), now["threads"].([]any)[0].(map[string]any)["messages"].([]any)[0].(map[string]any)) {
		t.Fatal("unchanged message was copied")
	}
}

func TestPatchesSupportRemovalsReorderedArraysSetsAndMaps(t *testing.T) {
	type shape struct {
		Values []float64           `json:"values"`
		Record map[string]bool     `json:"record"`
		Set    jsonx.Set[string]   `json:"set"`
		Map    map[string]float64  `json:"map"`
		Gone   *string             `json:"gone,omitzero"`
		Opt    jsonx.Opt[string]   `json:"opt,omitzero"`
		Nested map[string][]string `json:"nested"`
	}
	text := "here"
	before := &shape{Values: []float64{1, 2, 3}, Record: map[string]bool{"old": true}, Set: jsonx.SetOf("a"), Map: map[string]float64{"a": 1}, Gone: &text, Nested: map[string][]string{"x": {"1"}}}
	after := &shape{Values: []float64{3, 1}, Record: map[string]bool{}, Set: jsonx.SetOf("b"), Map: map[string]float64{"b": 2}, Opt: jsonx.Null[string](), Nested: map[string][]string{"x": {"1", "2"}}}
	held := wire(t, before)
	patches := patchesOf(t, before, after)
	if !reflect.DeepEqual(applied(t, held, patches), wire(t, after)) {
		t.Fatalf("patched state differs: %s", encodedPatches(t, patches))
	}
	if !reflect.DeepEqual(held, wire(t, before)) {
		t.Fatal("patching changed the state it started from")
	}
	removed := slices.ContainsFunc(patches, func(patch *contracts.WorkspacePatch) bool {
		return len(patch.Path) == 1 && patch.Path[0].Key == "gone" && patch.Remove != nil && *patch.Remove
	})
	if !removed {
		t.Fatalf("an omitzero field going zero is not a remove: %s", encodedPatches(t, patches))
	}
}

func TestPatchPathsCannotWriteThroughPrototypes(t *testing.T) {
	for _, path := range [][]contracts.PathKey{
		{contracts.KeyStep("__proto__"), contracts.KeyStep("polluted")},
		{contracts.KeyStep("constructor"), contracts.KeyStep("prototype"), contracts.KeyStep("polluted")},
	} {
		_, err := ApplyWorkspacePatches(map[string]any{}, []*contracts.WorkspacePatch{{Path: path, Value: jsontext.Value("true")}})
		if err == nil || !strings.Contains(err.Error(), "Invalid") {
			t.Fatalf("path %v was written: %v", path, err)
		}
	}
}

func TestPrependingAndRemovingThreadsSendsOnlyTheChangedSlice(t *testing.T) {
	threads := make([]*patchThread, 2_000)
	for id := range threads {
		threads[id] = &patchThread{ID: fmt.Sprintf("thread-%d", id), Messages: []*patchMessage{{Text: "A long existing conversation"}}}
	}
	added := &patchThread{ID: "new", Messages: []*patchMessage{{Text: "Hello"}}}
	before := &patchWorkspace{Threads: threads}
	after := &patchWorkspace{Threads: slices.Concat([]*patchThread{added}, threads)}
	patches := patchesOf(t, before, after)
	if got := encodedPatches(t, patches); got != `[{"path":["threads"],"splice":{"index":0,"deleteCount":0,"items":[{"id":"new","messages":[{"id":0,"text":"Hello"}]}]}}]` {
		t.Fatalf("patches %s", got)
	}
	held := wire(t, before)
	result := applied(t, held, patches)
	heldThreads, resultThreads := held.(map[string]any)["threads"].([]any), result.(map[string]any)["threads"].([]any)
	if !jsonx.Same(resultThreads[1].(map[string]any), heldThreads[0].(map[string]any)) || !jsonx.Same(resultThreads[len(resultThreads)-1].(map[string]any), heldThreads[len(heldThreads)-1].(map[string]any)) {
		t.Fatal("shifted threads were copied")
	}
	removed := patchesOf(t, after, before)
	if got := encodedPatches(t, removed); got != `[{"path":["threads"],"splice":{"index":0,"deleteCount":1,"items":[]}}]` {
		t.Fatalf("removal patches %s", got)
	}
	if !reflect.DeepEqual(applied(t, result, removed), wire(t, before)) {
		t.Fatal("removal did not restore the state")
	}
}

func TestEditingTheFinalStreamingMessageSendsItsChangedFieldOnly(t *testing.T) {
	type shape struct {
		Messages []*patchMessage `json:"messages"`
	}
	messages := make([]*patchMessage, 10_000)
	for id := range messages {
		messages[id] = &patchMessage{ID: float64(id), Text: "existing"}
	}
	last := *messages[len(messages)-1]
	last.Text = "expanded response"
	before := &shape{Messages: messages}
	after := &shape{Messages: append(slices.Clone(messages[:len(messages)-1]), &last)}
	patches := patchesOf(t, before, after)
	if got := encodedPatches(t, patches); got != `[{"path":["messages",9999,"text"],"value":"expanded response"}]` {
		t.Fatalf("patches %s", got)
	}
	held := wire(t, before)
	result := applied(t, held, patches)
	if !reflect.DeepEqual(result, wire(t, after)) {
		t.Fatal("patched state differs")
	}
	if held.(map[string]any)["messages"].([]any)[9_999].(map[string]any)["text"] != "existing" {
		t.Fatal("patching changed the state it started from")
	}
}

func TestLargeHistoryLoadApplies(t *testing.T) {
	type shape struct {
		Messages []float64 `json:"messages"`
	}
	before := &shape{Messages: []float64{}}
	after := &shape{Messages: make([]float64, 150_000)}
	for index := range after.Messages {
		after.Messages[index] = float64(index)
	}
	held := wire(t, before)
	if !reflect.DeepEqual(applied(t, held, patchesOf(t, before, after)), wire(t, after)) {
		t.Fatal("patched state differs")
	}
	if len(held.(map[string]any)["messages"].([]any)) != 0 {
		t.Fatal("patching changed the state it started from")
	}
}

func TestSplicePatchesComposeWithFieldChanges(t *testing.T) {
	row := func(text string) any { return map[string]any{"text": text} }
	rows := []any{row("one"), row("two"), row("three")}
	before := map[string]any{"rows": rows}
	items := func(texts ...string) []jsontext.Value {
		values := []jsontext.Value{}
		for _, text := range texts {
			values = append(values, jsontext.Value(`{"text":"`+text+`"}`))
		}
		return values
	}
	result := applied(t, before, []*contracts.WorkspacePatch{
		{Path: []contracts.PathKey{contracts.KeyStep("rows")}, Splice: &contracts.WorkspaceSplice{Index: 1, DeleteCount: 1, Items: items("inserted", "second")}},
		{Path: []contracts.PathKey{contracts.KeyStep("rows"), contracts.IndexStep(3), contracts.KeyStep("text")}, Value: jsontext.Value(`"changed"`)},
		{Path: []contracts.PathKey{contracts.KeyStep("rows")}, Splice: &contracts.WorkspaceSplice{Index: 1, DeleteCount: 2, Items: items()}},
	})
	got := result.(map[string]any)["rows"].([]any)
	if !reflect.DeepEqual(got, []any{row("one"), row("changed")}) {
		t.Fatalf("rows %v", got)
	}
	if !reflect.DeepEqual(rows, []any{row("one"), row("two"), row("three")}) {
		t.Fatalf("original rows changed: %v", rows)
	}
	if !jsonx.Same(got[0].(map[string]any), rows[0].(map[string]any)) {
		t.Fatal("unchanged row was copied")
	}
}

func TestOwnConstructorAndPrototypeFieldsRemainValidKeys(t *testing.T) {
	type shape struct {
		Dictionary map[string]map[string]map[string]bool `json:"dictionary"`
	}
	before := &shape{Dictionary: map[string]map[string]map[string]bool{"constructor": {"prototype": {"enabled": false}}}}
	after := &shape{Dictionary: map[string]map[string]map[string]bool{"constructor": {"prototype": {"enabled": true}}}}
	empty := &shape{Dictionary: map[string]map[string]map[string]bool{}}
	if !reflect.DeepEqual(applied(t, wire(t, before), patchesOf(t, before, after)), wire(t, after)) {
		t.Fatal("constructor keys did not patch")
	}
	if !reflect.DeepEqual(applied(t, wire(t, after), patchesOf(t, after, empty)), wire(t, empty)) {
		t.Fatal("constructor keys did not remove")
	}
}

func TestUnionMemberChangePatchesByKey(t *testing.T) {
	type shape struct {
		Result contracts.WorkspaceCommandResult `json:"result"`
	}
	taskID := "t1"
	before := &shape{Result: contracts.WorkspaceCommandResultTrue{TaskID: &taskID}}
	after := &shape{Result: contracts.WorkspaceCommandResultFalse{Message: "no"}}
	patches := patchesOf(t, before, after)
	if got := encodedPatches(t, patches); got != `[{"path":["result","ok"],"value":false},{"path":["result","message"],"value":"no"},{"path":["result","taskId"],"remove":true}]` {
		t.Fatalf("patches %s", got)
	}
	if !reflect.DeepEqual(applied(t, wire(t, before), patches), wire(t, after)) {
		t.Fatal("patched state differs")
	}
}

func TestPatchesTakeAStateToTheNext(t *testing.T) {
	states := corpusStates(t, 1_000)
	for index := 1; index < len(states); index++ {
		previous, next := states[index-1], states[index]
		patches := patchesOf(t, previous, next)
		if !reflect.DeepEqual(applied(t, wire(t, previous), patches), wire(t, next)) {
			t.Fatalf("states %d to %d: patched state differs", index-1, index)
		}
	}
	if patches := patchesOf(t, states[0], states[0]); len(patches) != 0 {
		t.Fatalf("a state against itself makes %d patches", len(patches))
	}
	copied := *states[0]
	if patches := patchesOf(t, states[0], &copied); len(patches) != 0 {
		t.Fatalf("a copy sharing every field makes %d patches", len(patches))
	}
}

// corpusStates is up to limit workspace states the TypeScript reducer produced.
func corpusStates(t *testing.T, limit int) []*WorkspaceState {
	t.Helper()
	f, err := os.Open("testdata/corpus.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(zr)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<28)
	var states []*WorkspaceState
	for scanner.Scan() && len(states) < limit {
		var entry struct {
			Of    string         `json:"of"`
			Value jsontext.Value `json:"value"`
		}
		if err := jsonx.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Of != "WorkspaceState" {
			continue
		}
		state := &WorkspaceState{}
		if err := jsonx.Unmarshal(entry.Value, state); err != nil {
			t.Fatal(err)
		}
		states = append(states, state)
	}
	if len(states) < 2 {
		t.Fatal("corpus has too few states")
	}
	return states
}
