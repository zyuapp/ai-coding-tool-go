package contracts

var PullRequestCommands = Declarations{
	{Type: "pull-request.read", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "pull-request.read-members", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "pull-request.read-member", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread},
}
