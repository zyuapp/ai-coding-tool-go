package jsonx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
)

type optState uint8

const (
	absent optState = iota
	null
	present
)

// Opt is a field TypeScript declares `x?: T | null`: absent, null, or a value. The zero Opt is
// absent, and a field tagged omitzero leaves it out.
type Opt[T any] struct {
	state optState
	value T
}

// Some is a present value.
func Some[T any](value T) Opt[T] { return Opt[T]{state: present, value: value} }

// Null is an explicit null.
func Null[T any]() Opt[T] { return Opt[T]{state: null} }

// OptFrom is value when ok, else absent.
func OptFrom[T any](value T, ok bool) Opt[T] {
	if ok {
		return Some(value)
	}
	return Opt[T]{}
}

// Get returns the value and whether there is one (neither absent nor null).
func (o Opt[T]) Get() (T, bool) { return o.value, o.state == present }

// Or returns the value, or fallback when absent or null.
func (o Opt[T]) Or(fallback T) T {
	if o.state == present {
		return o.value
	}
	return fallback
}

func (o Opt[T]) IsAbsent() bool { return o.state == absent }

func (o Opt[T]) IsNull() bool { return o.state == null }

func (o Opt[T]) IsPresent() bool { return o.state == present }

// IsZero reports absence, which omitzero leaves out.
func (o Opt[T]) IsZero() bool { return o.state == absent }

func (o Opt[T]) MarshalJSONTo(enc *jsontext.Encoder) error {
	if o.state != present {
		return enc.WriteToken(jsontext.Null)
	}
	return json.MarshalEncode(enc, o.value)
}

func (o *Opt[T]) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if dec.PeekKind() == 'n' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		var zero T
		*o = Opt[T]{state: null, value: zero}
		return nil
	}
	var value T
	if err := json.UnmarshalDecode(dec, &value); err != nil {
		return err
	}
	*o = Opt[T]{state: present, value: value}
	return nil
}

func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
