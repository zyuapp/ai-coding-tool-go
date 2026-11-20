package contracts

import "encoding/json/jsontext"

// updatesHere: the app on this computer, never one on a computer the desktop is linked to.
var updatesHere PhoneRule = func(command map[string]jsontext.Value) bool {
	return declUndefined(command["computerId"])
}

// checksQuietly: a phone looks for a newer build without a dialog, since nobody may be at the computer to close one.
var checksQuietly PhoneRule = func(command map[string]jsontext.Value) bool {
	quiet, ok := declBool(command["quiet"])
	return ok && quiet
}

var ExternalAppCommands = Declarations{
	{Type: "app.list", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "app.open-folder", Fields: Shape{{"appId", Text}}, At: CommandPlacementOwn},
	{Type: "app.check-for-updates", Fields: Shape{{"quiet", Optional(Literals(true))}}, At: CommandPlacementLocal, Phone: checksQuietly},
	{Type: "app.download-update", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "app.install-update", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "app.update", Fields: Shape{{"computerId", OptionalText}}, At: CommandPlacementOwn, Phone: updatesHere},
	{Type: "app.cancel-update", Fields: Shape{{"computerId", OptionalText}}, At: CommandPlacementOwn, Phone: updatesHere},
	{Type: "app.open-source-licenses", Fields: Shape{}, At: CommandPlacementLocal},
}
