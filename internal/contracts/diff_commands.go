package contracts

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

var DiffCommands = Declarations{
	{Type: "diff.toggle", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "diff.refresh", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "diff.open-commit", Fields: Shape{{"commit", declGuard(domain.IsCommitHash)}, {"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "diff.set-range", Fields: Shape{{"range", declGuard(domain.IsDiffRange)}}, At: CommandPlacementThread},
	{Type: "diff.set-mode", Fields: Shape{{"mode", Literals("uncommitted", "branch")}}, At: CommandPlacementThread},
	{Type: "diff.set-collapsed", Fields: Shape{{"path", Text}, {"collapsed", Boolean}}, At: CommandPlacementThread},
	{Type: "diff.set-viewed", Fields: Shape{{"path", Text}, {"viewed", Boolean}}, At: CommandPlacementThread},
	{Type: "diff.set-split", Fields: Shape{{"split", Boolean}}, At: CommandPlacementThread},
	{Type: "diff.set-ignore-whitespace", Fields: Shape{{"ignore", Boolean}}, At: CommandPlacementThread},
}
