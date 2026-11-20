package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// hostAddress: a host, with an optional port, already in the form it is dialled in.
var hostAddress Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	if !ok {
		return false
	}
	address, ok := domain.ComputerAddress(s)
	return ok && address == s
}

var ComputerCommands = Declarations{
	{Type: "computers.discover", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "computers.pair", Fields: Shape{{"host", hostAddress}, {"name", Text}, {"code", Text}}, At: CommandPlacementLocal},
	{Type: "computers.cancel-pairing", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "computers.forget", Fields: Shape{{"id", Text}}, At: CommandPlacementLocal},
	{Type: "computers.reconnect", Fields: Shape{{"id", OptionalText}}, At: CommandPlacementLocal},
	{Type: "computers.rename", Fields: Shape{{"name", Text}}, At: CommandPlacementLocal},
	{Type: "computers.label", Fields: Shape{{"id", Text}, {"name", Text}}, At: CommandPlacementLocal},
	{Type: "computers.filter", Fields: Shape{{"filter", Text}}, At: CommandPlacementLocal},
}
