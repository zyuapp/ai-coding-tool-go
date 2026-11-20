package contracts

var RemoteCommands = Declarations{
	{Type: "remote.set-enabled", Fields: Shape{{"enabled", Boolean}}, At: CommandPlacementLocal},
	{Type: "remote.create-pairing-code", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "remote.revoke-device", Fields: Shape{{"deviceId", Text}}, At: CommandPlacementLocal},
	{Type: "remote.refresh", Fields: Shape{}, At: CommandPlacementLocal},
}
