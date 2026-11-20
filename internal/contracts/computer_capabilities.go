package contracts

import (
	"encoding/json/jsontext"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// ComputerCapabilitiesComputerCapabilities is every capability name this build advertises, built on first use.
var ComputerCapabilitiesComputerCapabilities = sync.OnceValue(func() []string {
	var names []string
	for _, definition := range WorkspaceCommandDefinitions() {
		names = append(names, "command:"+definition.Type)
		for _, field := range definition.Fields {
			names = append(names, "command:"+definition.Type+":"+field)
		}
	}
	for _, definition := range ComputerQueryDefinitions() {
		names = append(names, "query:"+definition.Kind)
		for _, field := range definition.Fields {
			names = append(names, "query:"+definition.Kind+":"+field)
		}
	}
	return slices.Clip(names)
})

const RemoteUnsupported = "This feature is unavailable on that computer. Updating AI Coding Tool there may help."

// supports treats nil capabilities as a host from before capability discovery; a non-nil empty list supports nothing.
// A requirement list is a handful of names, so scanning beats building a set of the advertised ones.
func supports(capabilities ComputerCapabilities, names []string) bool {
	if capabilities == nil {
		return true
	}
	for _, name := range names {
		if !slices.Contains(capabilities, name) {
			return false
		}
	}
	return true
}

// requirements names scope:<discriminator value> and that name :<field> for every other field of value's JSON
// object. A value that does not encode as an object requires a name no host advertises.
func requirements(scope string, value any, discriminator string) []string {
	encoded, err := jsonx.Marshal(value)
	if err != nil {
		return []string{scope + ":"}
	}
	var fields map[string]jsontext.Value
	if jsonx.Unmarshal(encoded, &fields) != nil {
		return []string{scope + ":"}
	}
	var kind string
	if jsonx.Unmarshal(fields[discriminator], &kind) != nil {
		return []string{scope + ":"}
	}
	prefix := scope + ":" + kind
	names := make([]string, 1, len(fields))
	names[0] = prefix
	for key := range fields {
		if key != discriminator {
			names = append(names, prefix+":"+key)
		}
	}
	return names
}

// SupportsComputerCommand is shared by routing, controls and the socket, so a new option cannot silently
// disappear on an old host.
func SupportsComputerCommand(capabilities ComputerCapabilities, command AppCommand) bool {
	return supports(capabilities, requirements("command", command, "type"))
}

func SupportsComputerQuery(capabilities ComputerCapabilities, query ComputerQuery) bool {
	return supports(capabilities, requirements("query", query, "kind"))
}
