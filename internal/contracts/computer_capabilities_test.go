package contracts

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func restCommand(t *testing.T, json string) AppCommand {
	t.Helper()
	var command AppCommand
	if err := jsonx.Unmarshal([]byte(json), &command); err != nil {
		t.Fatalf("decode command %s: %v", json, err)
	}
	return command
}

func restQuery(t *testing.T, json string) ComputerQuery {
	t.Helper()
	var query ComputerQuery
	if err := jsonx.Unmarshal([]byte(json), &query); err != nil {
		t.Fatalf("decode query %s: %v", json, err)
	}
	return query
}

func TestDismissAllScopeIsValidatedAndAdvertisedAcrossCommandEntryPoints(t *testing.T) {
	for _, localOnly := range []any{restUndefined, false, true, "true", 1, nil} {
		command := restWith(t, map[string]any{"type": "task.dismiss-all"}, "localOnly", localOnly)
		_, boolean := localOnly.(bool)
		valid := localOnly == restUndefined || boolean
		restExpect(t, "IsWorkspaceViewInput", IsWorkspaceViewInput, command, valid)
		restExpect(t, "IsPhoneCommand", IsPhoneCommand, command, valid)
		restExpect(t, "IsComputerClientRequest", IsComputerClientRequest, map[string]any{"kind": "input", "requestId": "dismiss", "inputs": []any{command}}, valid)
	}
	if !SupportsComputerCommand(ComputerCapabilitiesComputerCapabilities(), restCommand(t, `{"type":"task.dismiss-all","localOnly":true}`)) {
		t.Fatal("dismiss-all localOnly is not advertised")
	}
}

func TestActionsAndTheirOptionalFieldsAreAdvertisedFromTheValidators(t *testing.T) {
	capabilities := ComputerCapabilitiesComputerCapabilities()
	for _, engine := range []string{"claude", "codex"} {
		command := restObj(t, `{"type":"task.set-fast-mode","fastMode":true}`)
		restExpect(t, "IsWorkspaceViewInput", IsWorkspaceViewInput, command, true)
		if !SupportsComputerCommand(capabilities, restCommand(t, `{"type":"task.set-fast-mode","fastMode":true}`)) {
			t.Error("task.set-fast-mode is not advertised")
		}
		query := `{"kind":"commands","workspaceId":"ws","engine":"` + engine + `"}`
		restExpect(t, "IsComputerQuery", IsComputerQuery, restObj(t, query), true)
		if !SupportsComputerQuery(capabilities, restQuery(t, query)) {
			t.Errorf("commands query for %s is not advertised", engine)
		}
	}
	older := slices.DeleteFunc(slices.Clone(capabilities), func(name string) bool { return name == "command:task.send:role" })
	if !SupportsComputerCommand(older, restCommand(t, `{"type":"task.send","text":"hello"}`)) {
		t.Error("an older host lost task.send")
	}
	if SupportsComputerCommand(older, restCommand(t, `{"type":"task.send","text":"hello","role":"reviewer"}`)) {
		t.Error("an older host claims task.send:role")
	}
}

func TestUnknownNamesAreHarmlessAbsentDiscoveryIsBestEffortAndEmptySupportsNothing(t *testing.T) {
	command := restCommand(t, `{"type":"task.rename","taskId":"t","title":"new"}`)
	if !SupportsComputerCommand(append(slices.Clone(ComputerCapabilitiesComputerCapabilities()), "future:feature"), command) {
		t.Error("an unknown name broke support")
	}
	if !SupportsComputerCommand(nil, command) {
		t.Error("absent discovery refused")
	}
	if SupportsComputerCommand([]string{}, command) {
		t.Error("an empty list supports a command")
	}
	if SupportsComputerCommand([]string{"command:task.rename"}, command) {
		t.Error("a command is supported without its fields")
	}
	restExpect(t, "IsComputerClientRequest", IsComputerClientRequest, restObj(t, `{"kind":"input","requestId":"id","inputs":[{"type":"action.failed","message":"injected"}]}`), false)
}

func TestAComputerAdvertisesCoordinationFieldsByName(t *testing.T) {
	capabilities := ComputerCapabilitiesComputerCapabilities()
	for _, name := range []string{"command:task.send:lead", "command:task.send:memory", "command:pull-request.read-member", "command:proposal.start:place"} {
		if !slices.Contains(capabilities, name) {
			t.Errorf("%s is not advertised", name)
		}
	}
	for _, name := range []string{"query:thread-read", "query:thread-read:limit", "query:message-image:thumbnail", "query:commands:engine"} {
		if !slices.Contains(capabilities, name) {
			t.Errorf("%s is not advertised", name)
		}
	}
}

func TestANullFieldIsARequirementAsMuchAsAValue(t *testing.T) {
	requirements := requirements("command", restCommand(t, `{"type":"task.set-role","taskId":"t","role":null}`), "type")
	slices.Sort(requirements)
	if !slices.Equal(requirements, []string{"command:task.set-role", "command:task.set-role:role", "command:task.set-role:taskId"}) {
		t.Fatalf("requirements = %v", requirements)
	}
}
