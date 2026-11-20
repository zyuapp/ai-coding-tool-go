package contracts

import (
	"slices"
	"testing"
)

func TestEverySendFieldSaysWhoMaySetIt(t *testing.T) {
	var declared []string
	for _, field := range declarations["task.send"].Fields {
		declared = append(declared, field.Name)
	}
	var reached []string
	for _, entry := range sendReach {
		reached = append(reached, entry.Field)
	}
	slices.Sort(declared)
	slices.Sort(reached)
	if !slices.Equal(declared, reached) {
		t.Errorf("declared %v, reached %v", declared, reached)
	}
}

func TestGuardsReadTypedCommands(t *testing.T) {
	if !IsPhoneCommand(TaskCommandTaskSelect{TaskID: "task-1"}) || IsPhoneCommand(TaskCommandTaskSelect{}) {
		t.Error("a typed task.select is read as it encodes")
	}
	if !IsWorkspaceViewInput(&TaskCommandTaskSelect{TaskID: "task-1"}) || IsWorkspaceViewInput((*TaskCommandTaskSelect)(nil)) {
		t.Error("a pointer to a command is read as it encodes, and nil as null")
	}
	if IsPhoneCommand("task.new") || IsPhoneCommand(nil) || IsPhoneCommand([]any{map[string]any{"type": "task.new"}}) {
		t.Error("only an object is a command")
	}
}
