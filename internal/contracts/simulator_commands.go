package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// drivesSimulator: a run's gestures go to the device its own dock shows, so it names itself and never a device
// outright. The Mac's clipboard is the person's, so a run never moves text through it.
func drivesSimulator(command map[string]jsontext.Value) bool {
	if !PhoneCommandsIsString(command["taskId"]) || !declUndefined(command["udid"]) {
		return false
	}
	var input domain.SimulatorInput
	if jsonx.Unmarshal(command["input"], &input) != nil || input == nil {
		return false
	}
	return !domain.UsesMacClipboard(input)
}

var SimulatorCommands = Declarations{
	{Type: "simulator.open", Fields: Shape{{"taskId", OptionalText}, {"device", OptionalText}}, At: CommandPlacementPanel, Agent: CommandDeclarationNamesThread},
	{Type: "simulator.refresh", Fields: Shape{}, At: CommandPlacementPanel},
	{Type: "simulator.select", Fields: Shape{{"udid", Text}}, At: CommandPlacementPanel},
	{Type: "simulator.input", Fields: Shape{{"taskId", OptionalText}, {"udid", OptionalText}, {"input", declGuard(domain.IsSimulatorInput)}}, At: CommandPlacementPanel, Agent: drivesSimulator},
}
