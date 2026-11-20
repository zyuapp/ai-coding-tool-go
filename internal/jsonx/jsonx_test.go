package jsonx

import (
	"strings"
	"testing"
)

type litCreated struct{}

func (litCreated) JSONLiteral() string { return `"created"` }

type litRemoved struct{}

func (litRemoved) JSONLiteral() string { return `"removed"` }

type litTrue struct{}

func (litTrue) JSONLiteral() string { return `true` }

type change interface{ isChange() }

type created struct {
	Type Tag[litCreated] `json:"type"`
	ID   string          `json:"id"`
	Note Opt[string]     `json:"note,omitzero"`
}

type removed struct {
	Type Tag[litRemoved] `json:"type"`
	IDs  []string        `json:"ids"`
}

func (created) isChange() {}
func (removed) isChange() {}

type envelope struct {
	Change  change            `json:"change"`
	Later   change            `json:"later,omitzero"`
	Changes []change          `json:"changes"`
	ByID    map[string]change `json:"byId,omitzero"`
	Maybe   Opt[change]       `json:"maybe,omitzero"`
	Set     Set[string]       `json:"set,omitzero"`
}

func init() {
	RegisterUnion[change]("type", map[any]change{"created": created{}, "removed": removed{}})
}

func roundTrip(t *testing.T, in string, out any) string {
	t.Helper()
	if err := Unmarshal([]byte(in), out); err != nil {
		t.Fatalf("decode %s: %v", in, err)
	}
	data, err := Marshal(out)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(data)
}

func TestUnionsDecodeWhereverTheyAppear(t *testing.T) {
	in := `{"change":{"type":"created","id":"a"},"changes":[{"type":"removed","ids":[]},{"type":"created","id":"b","note":null}],"byId":{"x":{"type":"removed","ids":["x"]}},"maybe":{"type":"created","id":"c","note":"n"},"set":["b","a"]}`
	var env envelope
	got := roundTrip(t, in, &env)
	if _, ok := env.Change.(created); !ok {
		t.Fatalf("change decoded as %T", env.Change)
	}
	if env.Later != nil {
		t.Fatalf("absent union decoded as %T", env.Later)
	}
	if c := env.Changes[1].(created); !c.Note.IsNull() {
		t.Fatalf("null note decoded as %+v", c.Note)
	}
	if m, _ := env.Maybe.Get(); m.(created).Note.Or("") != "n" {
		t.Fatalf("opt union decoded as %+v", env.Maybe)
	}
	want := `{"change":{"type":"created","id":"a"},"changes":[{"type":"removed","ids":[]},{"type":"created","id":"b","note":null}],"byId":{"x":{"type":"removed","ids":["x"]}},"maybe":{"type":"created","id":"c","note":"n"},"set":["a","b"]}`
	if got != want {
		t.Fatalf("round trip\n got %s\nwant %s", got, want)
	}
}

func TestUnionRefusesUnknownMembers(t *testing.T) {
	var env envelope
	for _, in := range []string{`{"change":{"type":"renamed"}}`, `{"change":{"id":"a"}}`, `{"change":"created"}`} {
		if err := Unmarshal([]byte(in), &env); err == nil {
			t.Errorf("%s decoded as %T", in, env.Change)
		}
	}
	if err := Unmarshal([]byte(`{"change":null}`), &env); err != nil || env.Change != nil {
		t.Errorf("null change: %v %T", err, env.Change)
	}
}

func TestTagAlwaysEncodesAndValidates(t *testing.T) {
	data, err := Marshal(removed{})
	if err != nil || string(data) != `{"type":"removed","ids":[]}` {
		t.Fatalf("zero member encoded as %s (%v)", data, err)
	}
	var r removed
	if err := Unmarshal([]byte(`{"type":"created","ids":[]}`), &r); err == nil {
		t.Fatal("tag accepted another value")
	}
	var flag struct {
		Ok Tag[litTrue] `json:"ok"`
	}
	if err := Unmarshal([]byte(`{"ok":false}`), &flag); err == nil {
		t.Fatal("true tag accepted false")
	}
	if (Tag[litCreated]{}).Value() != "created" || (Tag[litTrue]{}).Value() != true {
		t.Fatal("tag value")
	}
}

func TestOptIsTriState(t *testing.T) {
	type holder struct {
		N Opt[float64] `json:"n,omitzero"`
	}
	for in, want := range map[string]string{`{}`: `{}`, `{"n":null}`: `{"n":null}`, `{"n":0}`: `{"n":0}`} {
		var h holder
		if got := roundTrip(t, in, &h); got != want {
			t.Errorf("%s: got %s want %s", in, got, want)
		}
	}
	if v, ok := Some(2.0).Get(); !ok || v != 2 {
		t.Error("Some")
	}
	if !Null[int]().IsNull() || (Opt[int]{}).IsNull() || !(Opt[int]{}).IsAbsent() {
		t.Error("states")
	}
}

func TestNilCollectionsEncodeEmpty(t *testing.T) {
	type holder struct {
		List []string         `json:"list"`
		Map  map[string]int   `json:"map"`
		Opt  []string         `json:"opt,omitzero"`
		Raw  map[string]int64 `json:"raw,omitzero"`
	}
	data, err := Marshal(holder{Opt: []string{}})
	if err != nil || string(data) != `{"list":[],"map":{},"opt":[]}` {
		t.Fatalf("got %s (%v)", data, err)
	}
}

func TestSame(t *testing.T) {
	a := []int{1, 2, 3}
	b := a
	if !Same(a, b) || Same(a, a[:2]) || Same(a, []int{1, 2, 3}) || !SameSlice(a, b) {
		t.Error("slices")
	}
	m := map[string]int{"a": 1}
	if !Same(m, m) || Same(m, map[string]int{"a": 1}) || !SameMap(m, m) {
		t.Error("maps")
	}
	p, q := &struct{ X int }{}, &struct{ X int }{}
	if !Same(p, p) || Same(p, q) {
		t.Error("pointers")
	}
	var x, y change = created{ID: "a"}, created{ID: "a"}
	if !Same(x, y) || Same[change](x, removed{}) || Same[change](nil, x) || !Same[change](nil, nil) {
		t.Error("interfaces")
	}
	if Same([]int{}, make([]int, 0)) == Same(Fresh[int](0), Fresh[int](0)) {
		t.Error("only fresh empty slices have an identity of their own")
	}
	empty := Fresh[int](0)
	if !Same(empty, empty) {
		t.Error("an unchanged fresh slice")
	}
	var r1, r2 change = removed{IDs: []string{"a"}}, removed{IDs: []string{"a"}}
	if Same(r1, r2) {
		t.Error("incomparable values are never the same")
	}
}

func TestInvalidUTF8AndLoneSurrogatesDecode(t *testing.T) {
	var s string
	if err := Unmarshal([]byte(`"\ud800x"`), &s); err != nil || !strings.HasSuffix(s, "x") {
		t.Fatalf("got %q (%v)", s, err)
	}
}

type litPing struct{}

func (litPing) JSONLiteral() string { return `"ping"` }

type litAck struct{}

func (litAck) JSONLiteral() string { return `"ack"` }

type litFalse struct{}

func (litFalse) JSONLiteral() string { return `false` }

type message interface{ isMessage() }

type ack interface {
	isAck()
	isMessage()
}

type ping struct {
	Kind Tag[litPing] `json:"kind"`
}

type ackOk struct {
	Kind Tag[litAck]  `json:"kind"`
	Ok   Tag[litTrue] `json:"ok"`
}

type ackFailed struct {
	Kind    Tag[litAck]   `json:"kind"`
	Ok      Tag[litFalse] `json:"ok"`
	Message string        `json:"message"`
}

func (ping) isMessage()      {}
func (ackOk) isMessage()     {}
func (ackFailed) isMessage() {}
func (ackOk) isAck()         {}
func (ackFailed) isAck()     {}

func init() {
	RegisterUnion[message]("kind", map[any]message{"ping": ping{}})
	RegisterUnion[ack]("ok", map[any]ack{true: ackOk{}, false: ackFailed{}})
	RegisterNested[message, ack]("ack")
}

func TestNestedUnionDecodesThroughItsOwnKey(t *testing.T) {
	for in, want := range map[string]message{
		`{"kind":"ping"}`:                          ping{},
		`{"kind":"ack","ok":true}`:                 ackOk{},
		`{"kind":"ack","ok":false,"message":"no"}`: ackFailed{Message: "no"},
	} {
		var got message
		if err := Unmarshal([]byte(in), &got); err != nil || got != want {
			t.Errorf("%s: got %#v (%v)", in, got, err)
		}
	}
}
