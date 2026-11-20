package contracts

import "encoding/json/jsontext"

// deletesWorktreeById: an id, never a path or the thread on screen, and never the folder-is-gone shortcut.
func deletesWorktreeById(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["worktreeId"]) && declUndefined(command["taskId"]) && declUndefined(command["root"]) && declUndefined(command["missingOnly"])
}

var WorktreeCommands = Declarations{
	{Type: "worktree.menu-open", Fields: Shape{{"list", Literals("threads", "destinations")}}, At: CommandPlacementOwn},
	{Type: "worktree.menu-search", Fields: Shape{{"list", Literals("threads", "destinations")}, {"query", Text}}, At: CommandPlacementLocal},
	{Type: "worktree.refresh", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "worktree.filter-project", Fields: Shape{{"project", NullableText}}, At: CommandPlacementLocal},
	{Type: "worktree.confirm-delete", Fields: Shape{{"root", NullableText}}, At: CommandPlacementOwn},
	{Type: "worktree.confirm-delete-safe", Fields: Shape{{"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "worktree.delete-safe", Fields: Shape{{"roots", Array(Text)}}, At: CommandPlacementLocal},
	{Type: "worktree.set-missing-open", Fields: Shape{{"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "worktree.set-threads-open", Fields: Shape{{"root", Text}, {"open", Boolean}}, At: CommandPlacementLocal},
	{Type: "worktree.open-thread", Fields: Shape{{"taskId", Text}}, At: CommandPlacementSelect, Leaves: true},
	{Type: "worktree.reveal", Fields: Shape{{"root", Text}}, At: CommandPlacementOwn},
	{Type: "worktree.delete", Fields: Shape{{"taskId", OptionalText}, {"root", OptionalText}, {"worktreeId", OptionalText}, {"missingOnly", OptionalBoolean}}, At: CommandPlacementOwn, Agent: deletesWorktreeById},
}
