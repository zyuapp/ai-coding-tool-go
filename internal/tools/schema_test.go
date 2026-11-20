package tools

import (
	"bufio"
	"compress/gzip"
	"encoding/json/jsontext"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The tool sets the TypeScript app builds, by the names testdata/capture.mjs files them under.
func toolSets() map[string][]BoundTool {
	coordinator, member := domain.CoordinationRoleCoordinator, domain.CoordinationRoleMember
	return map[string][]BoundTool{
		"threads":                 ThreadToolsFor(nil, nil, ""),
		"threadsCoordinator":      ThreadToolsFor(nil, nil, coordinator),
		"threadsMember":           ThreadToolsFor(nil, nil, member),
		"coordinationCoordinator": CoordinationToolsFor(nil, coordinator),
		"coordinationMember":      CoordinationToolsFor(nil, member),
		"automation":              AutomationToolsFor(nil, ""),
		"automationCoordinator":   AutomationToolsFor(nil, coordinator),
		"findings":                FindingToolsFor(nil),
		"browser":                 BrowserToolsFor(nil, false),
		"browserReadOnly":         BrowserToolsFor(nil, true),
		"simulator":               SimulatorToolsFor(nil, false),
		"simulatorReadOnly":       SimulatorToolsFor(nil, true),
		"terminal":                TerminalToolsFor(nil),
		"computerUseSetup":        ComputerUseSetupToolsFor(nil),
	}
}

type listedTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema string `json:"inputSchema"`
}

func TestToolsListAsTheTypeScriptAppListsThem(t *testing.T) {
	data, err := os.ReadFile("testdata/tools_list.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string][]listedTool
	if err := jsonx.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	sets := toolSets()
	if len(sets) != len(golden) {
		t.Fatalf("%d sets, golden has %d", len(sets), len(golden))
	}
	for name, want := range golden {
		got := sets[name]
		if len(got) != len(want) {
			t.Errorf("%s: %d tools, want %d", name, len(got), len(want))
			continue
		}
		for index, tool := range got {
			if tool.Name != want[index].Name {
				t.Errorf("%s[%d]: name %q, want %q", name, index, tool.Name, want[index].Name)
			}
			if tool.Description != want[index].Description {
				t.Errorf("%s %s: description\n got %q\nwant %q", name, tool.Name, tool.Description, want[index].Description)
			}
			if string(tool.Input) != want[index].InputSchema {
				t.Errorf("%s %s: input schema\n got %s\nwant %s", name, tool.Name, tool.Input, want[index].InputSchema)
			}
		}
	}
}

type corpusCase struct {
	Set     string         `json:"set"`
	Tool    string         `json:"tool"`
	Args    jsontext.Value `json:"args"`
	Text    string         `json:"text"`
	IsError bool           `json:"isError"`
}

// expand turns {"$repeat": c, "n": n} back into the string it stands for.
func expand(value any) any {
	switch value := value.(type) {
	case map[string]any:
		if char, ok := value["$repeat"].(string); ok {
			return strings.Repeat(char, int(value["n"].(float64)))
		}
		out := map[string]any{}
		for key, item := range value {
			out[key] = expand(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, item := range value {
			out[index] = expand(item)
		}
		return out
	}
	return value
}

// normalize is the capture's norm: long strings by their length, and parsed objects as plain maps.
func normalize(value any) any {
	switch value := value.(type) {
	case string:
		if n := utf16Len(value); n > 80 {
			return "<" + jsNumber(float64(n)) + " chars>"
		}
		return value
	case Args:
		return normalize(map[string]any(value))
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			out[key] = normalize(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, item := range value {
			out[index] = normalize(item)
		}
		return out
	}
	return value
}

func shapesBySet() map[string]map[string]Shape {
	shapes := map[string]map[string]Shape{}
	add := func(set string, names []string, inputs []Shape) {
		shapes[set] = map[string]Shape{}
		for index, name := range names {
			shapes[set][name] = inputs[index]
		}
	}
	collect := func(set string, list any) {
		var names []string
		var inputs []Shape
		switch list := list.(type) {
		case []Definition[ThreadToolContext]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[CoordinationBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[AutomationBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[FindingBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[BrowserBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[SimulatorBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		case []Definition[TerminalBridge]:
			for _, d := range list {
				names, inputs = append(names, d.Name), append(inputs, d.Input)
			}
		}
		add(set, names, inputs)
	}
	collect("threads", ThreadTools)
	collect("threadsCoordinator", coordinatorThreadTools)
	collect("threadsMember", memberThreadTools)
	collect("coordinationCoordinator", coordinatorCoordinationTools)
	collect("coordinationMember", memberCoordinationTools)
	collect("automation", AutomationTools)
	collect("findings", FindingTools)
	collect("browser", BrowserTools)
	collect("simulator", SimulatorTools)
	collect("terminal", TerminalTools)
	return shapes
}

// Every argument set the capture sent the TypeScript tools is taken or refused here as it was there:
// the same parsed arguments, or the same refusal, word for word.
func TestArgumentsParseAsZodParsesThem(t *testing.T) {
	file, err := os.Open("testdata/zod_corpus.jsonl.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	shapes := shapesBySet()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(nil, 1<<24)
	count, failures := 0, 0
	for scanner.Scan() {
		var c corpusCase
		if err := jsonx.Unmarshal(scanner.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		count++
		var args any
		if err := jsonx.Unmarshal(c.Args, &args); err != nil {
			t.Fatal(err)
		}
		raw, err := jsonx.Marshal(expand(args))
		if err != nil {
			t.Fatal(err)
		}
		if c.Tool == "no_such_tool" {
			continue
		}
		shape, ok := shapes[c.Set][c.Tool]
		if !ok {
			t.Fatalf("no tool %s in %s", c.Tool, c.Set)
		}
		parsed, refusal := ParseArgs(c.Tool, shape, raw)
		var mismatch bool
		var got string
		if c.IsError {
			got = refusal
			mismatch = refusal != c.Text
		} else if refusal != "" {
			got, mismatch = refusal, true
		} else {
			var want any
			if err := jsonx.Unmarshal([]byte(c.Text), &want); err != nil {
				t.Fatal(err)
			}
			normalized := normalize(parsed)
			encoded, _ := jsonx.Marshal(normalized)
			var gotValue any
			_ = jsonx.Unmarshal(encoded, &gotValue)
			got = string(encoded)
			mismatch = !reflect.DeepEqual(gotValue, want)
		}
		if mismatch {
			failures++
			if failures <= 25 {
				t.Errorf("%s %s %s\n got %s\nwant %s", c.Set, c.Tool, c.Args, got, c.Text)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 4000 {
		t.Fatalf("only %d cases", count)
	}
	if failures > 0 {
		t.Fatalf("%d of %d cases differ", failures, count)
	}
}
