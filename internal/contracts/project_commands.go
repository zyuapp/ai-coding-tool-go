package contracts

import (
	"encoding/json/jsontext"
	"math"
)

// addsProject: a folder on this computer opened as a project, never one on another computer the desktop is linked to.
var addsProject PhoneRule = func(command map[string]jsontext.Value) bool {
	if !PhoneCommandsIsString(command["root"], PhoneCommandsMaxPathLength) {
		return false
	}
	root, _ := declString(command["root"])
	if declBlank(root) || !declUndefined(command["computerId"]) {
		return false
	}
	if declUndefined(command["start"]) {
		return true
	}
	start, ok := declBool(command["start"])
	return ok && start
}

var ProjectCommands = Declarations{
	{Type: "project.open", Fields: Shape{{"start", Optional(Literals(true))}}, At: CommandPlacementLocal},
	{Type: "project.add", Fields: Shape{{"root", func(value jsontext.Value) bool {
		if !PathText(value) {
			return false
		}
		root, _ := declString(value)
		return !declBlank(root)
	}}, {"computerId", Optional(DeviceID)}, {"start", Optional(Literals(true))}}, At: CommandPlacementOwn, Phone: addsProject},
	{Type: "view.add-project-close", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.add-project-device", Fields: Shape{{"computerId", DeviceID}}, At: CommandPlacementLocal},
	{Type: "view.add-project-path", Fields: Shape{{"root", PathText}}, At: CommandPlacementLocal},
	{Type: "view.add-project-pick", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.add-project-submit", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "view.add-project-key", Fields: Shape{{"key", Literals("ArrowUp", "ArrowDown", "Tab", "Enter", "Escape")}}, At: CommandPlacementLocal},
	{Type: "view.add-project-accept", Fields: Shape{{"index", func(value jsontext.Value) bool {
		n, ok := declNumber(value)
		return ok && n == math.Trunc(n) && n >= 0 && n < 20
	}}}, At: CommandPlacementLocal},
	{Type: "project.edit", Fields: Shape{{"projectId", Text}, {"name", Optional(NullableText)}, {"root", OptionalText}}, At: CommandPlacementProject},
	{Type: "project.remove", Fields: Shape{{"projectId", Text}}, At: CommandPlacementProject},
}
