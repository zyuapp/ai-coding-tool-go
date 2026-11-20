// Package protocol speaks Claude Code's stream-json protocol as the Agent SDK does: the command line
// a process starts with, the messages it prints, and the control requests that go both ways.
package protocol

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// decodeOptions reads what the process prints the way JSON.parse does: invalid UTF-8 is replaced
// rather than refused.
var decodeOptions = json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))

func marshal(value any) ([]byte, error) {
	return json.Marshal(value)
}

func unmarshal(data []byte, value any) error {
	return json.Unmarshal(data, value, decodeOptions)
}

// ptr returns a pointer to a copy of value.
func ptr[T any](value T) *T { return &value }
