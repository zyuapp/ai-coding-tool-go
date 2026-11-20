package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var updatesEngineHere PhoneRule = func(command map[string]jsontext.Value) bool {
	return declGuard(domain.IsAgentEngine)(command["engine"]) && declUndefined(command["computerId"])
}

var rereadsEngines PhoneRule = func(command map[string]jsontext.Value) bool {
	refresh, ok := declBool(command["refresh"])
	return ok && refresh
}

var EngineCommands = Declarations{
	{Type: "engine.read", Fields: Shape{{"refresh", OptionalBoolean}}, At: CommandPlacementLocal, Phone: rereadsEngines},
	{Type: "engine.sign-in", Fields: Shape{{"engine", declGuard(domain.IsAgentEngine)}}, At: CommandPlacementLocal},
	{Type: "engine.update", Fields: Shape{{"engine", declGuard(domain.IsAgentEngine)}, {"computerId", OptionalText}}, At: CommandPlacementOwn, Phone: updatesEngineHere},
	{Type: "engine.reload-settings", Fields: Shape{}, At: CommandPlacementLocal},
}
