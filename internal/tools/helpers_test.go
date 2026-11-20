package tools

import (
	"context"
	"encoding/json/jsontext"
	"regexp"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// argsOf reads a JSON object as handed to a handler without validation, as the TypeScript tests call
// handlers directly.
func argsOf(t *testing.T, text string) Args {
	t.Helper()
	var value any
	if err := jsonx.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return toArgs(value).(Args)
}

func toArgs(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := Args{}
		for key, item := range value {
			out[key] = toArgs(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, item := range value {
			out[index] = toArgs(item)
		}
		return out
	}
	return value
}

// runDefinition runs the named definition's handler on unvalidated arguments.
func runDefinition[B any](t *testing.T, definitions []Definition[B], bridge B, name, args string) ToolResult {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name == name {
			return definition.Run(context.Background(), bridge, argsOf(t, args))
		}
	}
	t.Fatalf("no %s tool", name)
	return ToolResult{}
}

// callBound calls a bound tool by name through its validating handler.
func callBound(t *testing.T, tools []BoundTool, name, args string) ToolResult {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			result, err := tool.Handler(context.Background(), jsontext.Value(args))
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatalf("no %s tool", name)
	return ToolResult{}
}

// parses reports whether the named tool's input takes the arguments, and what it reads them as.
func parses(t *testing.T, tools []BoundTool, shapes map[string]Shape, name, args string) (Args, bool) {
	t.Helper()
	shape, ok := shapes[name]
	if !ok {
		t.Fatalf("no %s input", name)
	}
	parsed, refusal := ParseArgs(name, shape, jsontext.Value(args))
	return parsed, refusal == ""
}

func shapesOf[B any](definitions []Definition[B]) map[string]Shape {
	out := map[string]Shape{}
	for _, definition := range definitions {
		out[definition.Name] = definition.Input
	}
	return out
}

func textOf(result ToolResult) string {
	var b strings.Builder
	for _, block := range result.Content {
		b.WriteString(block.Text)
	}
	return b.String()
}

func names(tools []BoundTool) []string {
	out := make([]string, len(tools))
	for index, tool := range tools {
		out[index] = tool.Name
	}
	return out
}

func assertMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%q does not match /%s/", text, pattern)
	}
}

func assertNoMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(text) {
		t.Errorf("%q matches /%s/", text, pattern)
	}
}

// assertJSON compares a value's encoding with the expected JSON, ignoring key order.
func assertJSON(t *testing.T, got any, want string) {
	t.Helper()
	encoded, err := jsonx.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue, wantValue any
	if err := jsonx.Unmarshal(encoded, &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("bad expectation %s: %v", want, err)
	}
	if !jsonEqual(gotValue, wantValue) {
		t.Errorf("got  %s\nwant %s", encoded, want)
	}
}

func jsonEqual(a, b any) bool {
	left, _ := jsonx.Marshal(a)
	right, _ := jsonx.Marshal(b)
	return string(left) == string(right)
}

func ptr[T any](value T) *T { return &value }
