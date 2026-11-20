// Package jsonx holds the JSON conventions every type shared with the TypeScript app follows:
// one set of encoding/json/v2 options, constant discriminators, interface-typed unions,
// tri-state optional fields, and identity checks for copy-on-write state.
package jsonx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"sync"
	"sync/atomic"
)

var (
	registry   sync.RWMutex
	unmarshals []*json.Unmarshalers
	options    atomic.Pointer[json.Options]
)

func init() { rebuild() }

func base() json.Options {
	return json.JoinOptions(
		json.Deterministic(true),
		jsontext.AllowInvalidUTF8(true),
		jsontext.AllowDuplicateNames(true),
	)
}

// rebuild must be called with registry held, or before any other goroutine runs.
func rebuild() {
	opts := base()
	if len(unmarshals) > 0 {
		opts = json.JoinOptions(opts, json.WithUnmarshalers(json.JoinUnmarshalers(unmarshals...)))
	}
	options.Store(&opts)
}

// Options are the options every encode and decode uses: maps sorted by key, invalid UTF-8 and
// duplicate names tolerated as JSON.parse does, and every registered union decodable.
func Options() json.Options { return *options.Load() }

func Marshal(v any) ([]byte, error) { return json.Marshal(v, Options()) }

func MarshalWrite(w io.Writer, v any) error { return json.MarshalWrite(w, v, Options()) }

func Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v, Options()) }

func UnmarshalRead(r io.Reader, v any) error { return json.UnmarshalRead(r, v, Options()) }
