package application

import (
	"bufio"
	"compress/gzip"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// testdata/corpus.jsonl.gz is what the TypeScript reducer took and produced under random inputs
// (tools/tsgo/corpus/capture.cjs). Every value must survive a decode into the generated types and
// an encode back, up to key order, apart from the state this app keeps that the TypeScript app has none of.
func TestGeneratedTypesRoundTripTypeScriptJSON(t *testing.T) {
	path := os.Getenv("ACT_TYPES_CORPUS")
	if path == "" {
		path = "testdata/corpus.jsonl.gz"
	}
	f, err := os.Open(path)
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
	counts := map[string]int{}
	failures := map[string]int{}
	for line := 1; scanner.Scan(); line++ {
		var entry struct {
			Of    string         `json:"of"`
			Value jsontext.Value `json:"value"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("line %d: %v", line, err)
		}
		var problem error
		switch entry.Of {
		case "WorkspaceState":
			problem = roundTrip[WorkspaceState](entry.Value, "idleCompaction", "idleCompactions")
		case "WorkspaceInput":
			problem = roundTrip[WorkspaceInput](entry.Value)
		case "WorkspaceEffect":
			problem = roundTrip[WorkspaceEffect](entry.Value)
		case "AppCommand":
			problem = roundTrip[contracts.AppCommand](entry.Value)
		case "WorkspaceCommandResult":
			problem = roundTrip[contracts.WorkspaceCommandResult](entry.Value)
		default:
			t.Fatalf("line %d: unknown type %q", line, entry.Of)
		}
		counts[entry.Of]++
		if problem != nil {
			failures[entry.Of]++
			if failures[entry.Of] <= 5 {
				t.Errorf("line %d (%s): %v", line, entry.Of, problem)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for _, of := range []string{"WorkspaceState", "WorkspaceInput", "WorkspaceEffect", "AppCommand", "WorkspaceCommandResult"} {
		if counts[of] == 0 {
			t.Errorf("corpus has no %s", of)
		}
	}
	t.Logf("round-tripped %v; failed %v", counts, failures)
}

// roundTrip encodes raw back through T. goOnly names top-level fields the encoding may add.
func roundTrip[T any](raw jsontext.Value, goOnly ...string) error {
	var value T
	if err := jsonx.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	back, err := jsonx.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(back, &got); err != nil {
		return err
	}
	if fields, ok := got.(map[string]any); ok {
		for _, field := range goOnly {
			delete(fields, field)
		}
	}
	if diffs := jsonDiff(want, got, "$", nil); len(diffs) > 0 {
		return fmt.Errorf("%d differences, first: %v", len(diffs), diffs[:min(len(diffs), 3)])
	}
	return nil
}

func jsonDiff(want, got any, at string, out []string) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return append(out, fmt.Sprintf("%s: want object, got %s", at, short(got)))
		}
		keys := make([]string, 0, len(w)+len(g))
		for k := range w {
			keys = append(keys, k)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			wv, wok := w[k]
			gv, gok := g[k]
			switch {
			case !gok:
				out = append(out, fmt.Sprintf("%s.%s: dropped %s", at, k, short(wv)))
			case !wok:
				out = append(out, fmt.Sprintf("%s.%s: added %s", at, k, short(gv)))
			default:
				out = jsonDiff(wv, gv, at+"."+k, out)
			}
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return append(out, fmt.Sprintf("%s: want %s, got %s", at, short(want), short(got)))
		}
		for i := range w {
			out = jsonDiff(w[i], g[i], fmt.Sprintf("%s[%d]", at, i), out)
		}
		return out
	}
	if want != got {
		out = append(out, fmt.Sprintf("%s: want %s, got %s", at, short(want), short(got)))
	}
	return out
}

func short(v any) string {
	data, _ := json.Marshal(v, json.Deterministic(true))
	if len(data) > 160 {
		return string(slices.Concat(data[:160], []byte("…")))
	}
	return string(data)
}

// The union forms the reducer corpus does not reach: a union nested under another's key, members
// sharing a key merged into one struct, and unions told apart by which fields they have.
func TestGeneratedUnionForms(t *testing.T) {
	cases := []struct {
		json  string
		check func(t *testing.T, raw jsontext.Value)
	}{
		{`{"sequence":3,"kind":"ack","requestId":"r","ok":false,"message":"no"}`, func(t *testing.T, raw jsontext.Value) {
			var m contracts.MobileServerMessage
			decodeInto(t, raw, &m)
			if ack, ok := m.(contracts.MobileAckMessageFalse); !ok || ack.Message != "no" {
				t.Errorf("decoded as %#v", m)
			}
		}},
		{`{"sequence":4,"kind":"ack","requestId":"r","ok":true}`, func(t *testing.T, raw jsontext.Value) {
			var m contracts.MobileServerMessage
			decodeInto(t, raw, &m)
			if _, ok := m.(contracts.MobileAckMessageTrue); !ok {
				t.Errorf("decoded as %#v", m)
			}
		}},
		{`{"sequence":5,"kind":"answer","requestId":"r","ok":false,"message":"no"}`, func(t *testing.T, raw jsontext.Value) {
			var m contracts.ComputerServerMessage
			decodeInto(t, raw, &m)
			if a, ok := m.(contracts.ComputerServerMessageAnswer); !ok || a.Ok || a.Message == nil {
				t.Errorf("decoded as %#v", m)
			}
		}},
		{`{"path":["threads",0,"title"],"value":null}`, func(t *testing.T, raw jsontext.Value) {
			var p contracts.WorkspacePatch
			decodeInto(t, raw, &p)
			if len(p.Path) != 3 || !p.Path[1].IsIndex || p.Path[2].Key != "title" || string(p.Value) != "null" {
				t.Errorf("decoded as %#v", p)
			}
		}},
		{`{"path":["toasts"],"splice":{"index":0,"deleteCount":1,"items":[{"id":1}]}}`, nil},
		{`{"revision":2,"patches":[]}`, nil},
		{`{"revision":2,"state":{"threads":[]}}`, nil},
	}
	for _, c := range cases {
		raw := jsontext.Value(c.json)
		if c.check != nil {
			c.check(t, raw)
		}
	}
	for _, c := range []string{cases[0].json, cases[1].json} {
		if err := roundTrip[contracts.MobileServerMessage](jsontext.Value(c)); err != nil {
			t.Error(err)
		}
	}
	if err := roundTrip[contracts.ComputerServerMessage](jsontext.Value(cases[2].json)); err != nil {
		t.Error(err)
	}
	for _, c := range cases[3:5] {
		if err := roundTrip[contracts.WorkspacePatch](jsontext.Value(c.json)); err != nil {
			t.Error(err)
		}
	}
	for _, c := range cases[5:] {
		if err := roundTrip[contracts.ComputerWorkspaceUpdate](jsontext.Value(c.json)); err != nil {
			t.Error(err)
		}
	}
}

func decodeInto(t *testing.T, raw jsontext.Value, out any) {
	t.Helper()
	if err := jsonx.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}
