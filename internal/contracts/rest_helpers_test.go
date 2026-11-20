package contracts

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// restUndefined in restWith removes the key, as a spread of undefined leaves a key JSON never writes.
type restUndefinedKey struct{}

var restUndefined = restUndefinedKey{}

// restJSON decodes a JSON literal into the plain values the guards read.
func restJSON(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := jsonx.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return value
}

func restObj(t *testing.T, text string) map[string]any {
	t.Helper()
	return restJSON(t, text).(map[string]any)
}

// restWith is {...base, key: value, ...}; values are Go values or restUndefined, and numbers become float64.
func restWith(t *testing.T, base map[string]any, pairs ...any) map[string]any {
	t.Helper()
	next := make(map[string]any, len(base)+len(pairs)/2)
	for key, value := range base {
		next[key] = value
	}
	for i := 0; i < len(pairs); i += 2 {
		key := pairs[i].(string)
		if pairs[i+1] == restUndefined {
			delete(next, key)
			continue
		}
		next[key] = restNormalize(t, pairs[i+1])
	}
	return next
}

func restNormalize(t *testing.T, value any) any {
	t.Helper()
	switch value.(type) {
	case nil, string, bool, float64:
		return value
	}
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	var decoded any
	if err := jsonx.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode %s: %v", encoded, err)
	}
	return decoded
}

func restRepeat(s string, n int) string { return strings.Repeat(s, n) }

func restExpect(t *testing.T, name string, guard func(any) bool, value any, want bool) {
	t.Helper()
	if got := guard(value); got != want {
		encoded, _ := jsonx.Marshal(value)
		t.Errorf("%s(%s) = %v, want %v", name, restClip(string(encoded)), got, want)
	}
}

func restClip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
