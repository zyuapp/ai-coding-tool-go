package contracts

var DictationCommands = Declarations{
	{Type: "dictation.start", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "dictation.hands-free", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "dictation.stop", Fields: Shape{{"send", OptionalBoolean}}, At: CommandPlacementLocal},
	{Type: "dictation.cancel", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "dictation.download", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "dictation.cancel-download", Fields: Shape{}, At: CommandPlacementLocal},
}
