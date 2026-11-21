// Command command-check reads the cases scripts/command-diff/cases.cjs writes and prints each one where
// internal/contracts judges a command or window event differently from the TypeScript checks: the
// window's (IsWorkspaceViewInput), an agent's (IsExternalCommand) and a phone's (IsPhoneCommand).
// The first line holds the TypeScript declarations, which must match WorkspaceCommandDefinitions with
// each command's placement and whether it leaves. Every input the window takes must also decode as an
// application.WorkspaceInput and encode back to the same JSON. Exits 1 when anything differs.
package main

import (
	"bufio"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"reflect"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type verdicts struct {
	View     bool `json:"view"`
	External bool `json:"external"`
	Phone    bool `json:"phone"`
}

type testCase struct {
	Input jsontext.Value `json:"input"`
	TS    verdicts       `json:"ts"`
	Field string         `json:"field,omitzero"`
}

type declared struct {
	Type   string   `json:"type"`
	Fields []string `json:"fields"`
	At     string   `json:"at"`
	Leaves bool     `json:"leaves"`
}

// Commands only the Go app declares, which the TypeScript app has no counterpart of.
var goOnlyCommands = map[string]bool{"view.set-idle-compaction": true}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 1<<20), 1<<28)
	problems := 0
	report := func(format string, args ...any) {
		problems++
		if problems <= 200 {
			fmt.Printf(format+"\n", args...)
		}
	}
	if !in.Scan() {
		fmt.Fprintln(os.Stderr, "no input")
		os.Exit(2)
	}
	var head struct {
		Definitions []declared `json:"definitions"`
	}
	if err := jsonx.Unmarshal(in.Bytes(), &head); err != nil {
		fmt.Fprintln(os.Stderr, "definitions:", err)
		os.Exit(2)
	}
	compareDefinitions(head.Definitions, report)

	cases, decoded := 0, 0
	for in.Scan() {
		var c testCase
		if err := jsonx.Unmarshal(in.Bytes(), &c); err != nil {
			report("unreadable case: %v", err)
			continue
		}
		cases++
		got := verdicts{
			View:     contracts.IsWorkspaceViewInput(c.Input),
			External: contracts.IsExternalCommand(c.Input),
			Phone:    contracts.IsPhoneCommand(c.Input),
		}
		if got != c.TS {
			report("ts=%+v go=%+v field=%s %s", c.TS, got, c.Field, shorten(c.Input))
		}
		if c.TS.View && c.Field != "unknown" {
			decoded++
			if problem := roundTrip(c.Input); problem != "" {
				report("decode field=%s: %s %s", c.Field, problem, shorten(c.Input))
			}
		}
	}
	fmt.Fprintf(os.Stderr, "%d declarations, %d cases (%d decoded, %d of them dropping undeclared keys), %d differ\n", len(head.Definitions), cases, decoded, dropped, problems)
	if problems > 0 {
		os.Exit(1)
	}
}

func compareDefinitions(ts []declared, report func(string, ...any)) {
	goDefinitions := contracts.WorkspaceCommandDefinitions()
	byType := map[string]contracts.WorkspaceCommandDefinition{}
	order := make([]string, len(goDefinitions))
	for i, definition := range goDefinitions {
		byType[definition.Type] = definition
		order[i] = definition.Type
	}
	tsOrder := make([]string, len(ts))
	for i, definition := range ts {
		tsOrder[i] = definition.Type
		mine, ok := byType[definition.Type]
		if !ok {
			report("declaration %s: missing in Go", definition.Type)
			continue
		}
		if !slices.Equal(mine.Fields, definition.Fields) {
			report("declaration %s: fields ts=%v go=%v", definition.Type, definition.Fields, mine.Fields)
		}
		if at := string(contracts.WorkspaceViewInputCommandPlacement(definition.Type)); at != definition.At {
			report("declaration %s: placement ts=%s go=%s", definition.Type, definition.At, at)
		}
		if leaves := contracts.CommandLeaves(definition.Type); leaves != definition.Leaves {
			report("declaration %s: leaves ts=%v go=%v", definition.Type, definition.Leaves, leaves)
		}
		if !contracts.IsAppCommandType(definition.Type) {
			report("declaration %s: not an app command type in Go", definition.Type)
		}
	}
	order = slices.DeleteFunc(slices.Clone(order), func(typ string) bool { return goOnlyCommands[typ] })
	for _, typ := range order {
		if !slices.Contains(tsOrder, typ) {
			report("declaration %s: only in Go", typ)
		}
	}
	if len(order) == len(tsOrder) && !slices.Equal(order, tsOrder) {
		report("declaration order differs")
	}
}

// roundTrip decodes a valid input as the reducer takes it and says how its encoding differs, if it does.
func roundTrip(raw jsontext.Value) string {
	var input application.WorkspaceInput
	if err := jsonx.Unmarshal(raw, &input); err != nil {
		return "does not decode: " + err.Error()
	}
	encoded, err := jsonx.Marshal(input)
	if err != nil {
		return "does not encode: " + err.Error()
	}
	var before, after any
	_ = jsonx.Unmarshal(raw, &before)
	_ = jsonx.Unmarshal(encoded, &after)
	if reflect.DeepEqual(before, after) {
		return ""
	}
	if reflect.DeepEqual(known(before, after), after) {
		dropped++
		return ""
	}
	return "encodes as " + shorten(encoded)
}

// dropped counts inputs whose encoding lost only keys no type declares, which a typed port cannot carry.
var dropped int

// known is value with only the keys shape holds, and an array where shape holds an empty object read as that object.
func known(value, shape any) any {
	switch shape := shape.(type) {
	case map[string]any:
		if _, isArray := value.([]any); isArray && len(shape) == 0 {
			return shape
		}
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		kept := map[string]any{}
		for key, field := range object {
			if inner, ok := shape[key]; ok {
				kept[key] = known(field, inner)
			}
		}
		return kept
	case []any:
		list, ok := value.([]any)
		if !ok || len(list) != len(shape) {
			return value
		}
		kept := make([]any, len(list))
		for i := range list {
			kept[i] = known(list[i], shape[i])
		}
		return kept
	}
	return value
}

func shorten(raw []byte) string {
	if len(raw) > 300 {
		return string(raw[:300]) + "…"
	}
	return string(raw)
}
