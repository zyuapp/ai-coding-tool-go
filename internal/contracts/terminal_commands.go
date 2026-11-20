package contracts

import "encoding/json/jsontext"

var TerminalCommands = Declarations{
	{Type: "terminal.open", Fields: Shape{{"cwd", OptionalText}}, At: CommandPlacementThread},
	{Type: "terminal.select", Fields: Shape{{"terminalId", Text}}, At: CommandPlacementTerminal},
	{Type: "terminal.close", Fields: Shape{{"terminalId", Text}}, At: CommandPlacementTerminal},
	{Type: "terminal.restart", Fields: Shape{{"terminalId", Text}}, At: CommandPlacementTerminal},
	{Type: "terminal.input", Fields: Shape{{"terminalId", Text}, {"data", func(value jsontext.Value) bool {
		s, ok := declString(value)
		return ok && declUTF16Len(s) <= MaxTerminalInput
	}}}, At: CommandPlacementTerminal},
	{Type: "terminal.resize", Fields: Shape{{"terminalId", Text}, {"cols", declGuard(IsTerminalDimension)}, {"rows", declGuard(IsTerminalDimension)}}, At: CommandPlacementTerminal},
}
