package contracts

var ComputerUseCommands = Declarations{
	{Type: "computer-use.read", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "computer-use.enable", Fields: Shape{{"permission", Literals("accessibility", "screenRecording")}}, At: CommandPlacementLocal},
	{Type: "computer-use.restart", Fields: Shape{}, At: CommandPlacementLocal},
}
