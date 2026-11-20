package contracts

var CliCommands = Declarations{
	{Type: "cli.read", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "cli.install", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "cli.uninstall", Fields: Shape{}, At: CommandPlacementLocal},
}
