package jsonx

import (
	"encoding/json/jsontext"
	"fmt"
)

// Literal names one JSON constant: JSONLiteral returns its JSON text, such as `"run.event"`, `2` or `true`.
// The types in package lit implement it.
type Literal interface {
	comparable
	JSONLiteral() string
}

// Tag is a zero-size field that always encodes as its literal L and refuses any other value on
// decode. Union members carry their discriminator as a Tag, so a constructed member never lacks it.
type Tag[L Literal] struct{}

// Value is the literal as a Go value: a string, float64 or bool.
func (Tag[L]) Value() any {
	var l L
	token, err := jsontext.NewDecoder(stringsReader(l.JSONLiteral())).ReadToken()
	if err != nil {
		panic(err)
	}
	switch token.Kind() {
	case '"':
		return token.String()
	case '0':
		f, _ := token.Float()
		return f
	default:
		return token.Bool()
	}
}

func (t Tag[L]) String() string { return fmt.Sprint(t.Value()) }

func (Tag[L]) MarshalJSONTo(enc *jsontext.Encoder) error {
	var l L
	return enc.WriteValue(jsontext.Value(l.JSONLiteral()))
}

func (*Tag[L]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	var l L
	want := jsontext.Value(l.JSONLiteral())
	got := raw.Clone()
	if err := got.Canonicalize(); err != nil {
		return err
	}
	if err := want.Canonicalize(); err != nil {
		return err
	}
	if string(got) != string(want) {
		return fmt.Errorf("jsonx: want %s, got %s", l.JSONLiteral(), raw)
	}
	return nil
}
