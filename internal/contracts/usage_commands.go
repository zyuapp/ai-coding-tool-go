package contracts

import "encoding/json/jsontext"

// readsUsageHere: this computer's own limits, never a paired computer's.
var readsUsageHere PhoneRule = func(command map[string]jsontext.Value) bool {
	return declUndefined(command["everywhere"])
}

var dismissesUsageWarning PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["warning"])
}

var UsageCommands = Declarations{
	{Type: "usage.read", Fields: Shape{{"everywhere", OptionalBoolean}}, At: CommandPlacementLocal, Phone: readsUsageHere},
	{Type: "usage.dismiss-warning", Fields: Shape{{"warning", Text}}, At: CommandPlacementLocal, Phone: dismissesUsageWarning},
}
