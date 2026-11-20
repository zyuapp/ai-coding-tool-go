package tools

import (
	"context"
	"encoding/json/jsontext"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolResult struct {
	Content []ToolResultContent `json:"content"`
	IsError bool                `json:"isError,omitzero"`
}

func textResult(text string) ToolResult {
	return ToolResult{Content: []ToolResultContent{{Type: "text", Text: text}}}
}

func errorResult(text string) ToolResult {
	return ToolResult{Content: []ToolResultContent{{Type: "text", Text: text}}, IsError: true}
}

// Definition is one tool the app offers an agent, described apart from whatever host serves it.
// ReadOnly says a call changes nothing the user can see, which is what lets a host grant it without asking.
type Definition[Bridge any] struct {
	Name        string
	Description string
	Input       Shape
	ReadOnly    bool
	Run         func(ctx context.Context, bridge Bridge, args Args) ToolResult
}

// BoundTool is a definition with its bridge supplied, which is all a host needs to serve it.
type BoundTool struct {
	Name        string
	Description string
	// The JSON Schema of the input object, as the MCP SDK lists it.
	Input jsontext.Value
	// Validates the arguments as the MCP SDK does, then runs the tool. Arguments it refuses answer as
	// an error result naming each field, never as an error.
	Handler func(ctx context.Context, args jsontext.Value) (ToolResult, error)
}

// Bind supplies the bridge to each definition.
func Bind[Bridge any](bridge Bridge, definitions []Definition[Bridge]) []BoundTool {
	tools := make([]BoundTool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, BoundTool{
			Name:        definition.Name,
			Description: definition.Description,
			Input:       definition.Input.JSONSchema(),
			Handler: func(ctx context.Context, raw jsontext.Value) (ToolResult, error) {
				args, refusal := ParseArgs(definition.Name, definition.Input, raw)
				if refusal != "" {
					return errorResult(refusal), nil
				}
				return definition.Run(ctx, bridge, args), nil
			},
		})
	}
	return tools
}

// ParseArgs reads a call's raw arguments against a tool's input. A refusal is the text the MCP SDK
// answers with, and empty when the arguments were taken.
func ParseArgs(tool string, shape Shape, raw jsontext.Value) (Args, string) {
	var value any = map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		if err := jsonx.Unmarshal(raw, &value); err != nil {
			return nil, "MCP error -32602: Input validation error: Invalid arguments for tool " + tool + ": " + err.Error()
		}
	}
	args, issues := shape.Parse(value)
	if len(issues) == 0 {
		return args, ""
	}
	texts := make([]string, len(issues))
	for index, issue := range issues {
		texts[index] = issue.Text()
	}
	return nil, "MCP error -32602: Input validation error: Invalid arguments for tool " + tool + ": " + strings.Join(texts, "\n")
}

func filterDefinitions[Bridge any](definitions []Definition[Bridge], keep func(Definition[Bridge]) bool) []Definition[Bridge] {
	var out []Definition[Bridge]
	for _, definition := range definitions {
		if keep(definition) {
			out = append(out, definition)
		}
	}
	return out
}

// Accessors for parsed arguments. A key the shape declares optional is absent when not given.

func (a Args) Has(key string) bool {
	_, ok := a[key]
	return ok
}

// Str is a string argument, "" when absent or null.
func (a Args) Str(key string) string {
	text, _ := a[key].(string)
	return text
}

// OptStr is a string argument, nil when absent or null.
func (a Args) OptStr(key string) *string {
	text, ok := a[key].(string)
	if !ok {
		return nil
	}
	return &text
}

// Bool is a boolean argument, false when absent.
func (a Args) Bool(key string) bool {
	value, _ := a[key].(bool)
	return value
}

// OptBool is a boolean argument, nil when absent.
func (a Args) OptBool(key string) *bool {
	value, ok := a[key].(bool)
	if !ok {
		return nil
	}
	return &value
}

// Num is a number argument, 0 when absent.
func (a Args) Num(key string) float64 {
	value, _ := a[key].(float64)
	return value
}

// OptNum is a number argument, nil when absent.
func (a Args) OptNum(key string) *float64 {
	value, ok := a[key].(float64)
	if !ok {
		return nil
	}
	return &value
}

// List is an array of objects argument.
func (a Args) List(key string) []Args {
	items, _ := a[key].([]any)
	out := make([]Args, 0, len(items))
	for _, item := range items {
		args, _ := item.(Args)
		out = append(out, args)
	}
	return out
}
