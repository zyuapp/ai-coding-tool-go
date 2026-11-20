package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// drivesBrowser: a run drives the browser as itself, so every browser command names the thread that asked.
func drivesBrowser(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && AgentID(command["tabId"]) &&
		(declUndefined(command["url"]) || PhoneCommandsIsString(command["url"], MaxURLLength))
}

var BrowserCommands = Declarations{
	{Type: "browser.open", Fields: Shape{{"taskId", OptionalText}, {"url", Text}, {"tabId", OptionalText}, {"newTab", OptionalBoolean}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.new-tab", Fields: Shape{}, At: CommandPlacementPanel},
	{Type: "browser.close-tab", Fields: Shape{{"taskId", OptionalText}, {"tabId", Text}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.select-tab", Fields: Shape{{"taskId", OptionalText}, {"tabId", Text}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.go", Fields: Shape{{"taskId", OptionalText}, {"tabId", OptionalText}, {"delta", Step}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.reload", Fields: Shape{{"taskId", OptionalText}, {"tabId", OptionalText}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.act", Fields: Shape{{"taskId", OptionalText}, {"tabId", OptionalText}, {"action", declGuard(IsBrowserAction)}}, At: CommandPlacementPanel, Agent: drivesBrowser},
	{Type: "browser.decide", Fields: Shape{{"allow", Boolean}, {"approvalId", Text}}, At: CommandPlacementPanel},
	{Type: "browser.clear-data", Fields: Shape{}, At: CommandPlacementPanel},
}

var BrowserImportCommands = Declarations{
	{Type: "browser-import.read", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "browser-import.choose", Fields: Shape{{"sourceId", Nullable(declGuard(domain.IsImportSourceID))}}, At: CommandPlacementLocal},
	{Type: "browser-import.toggle", Fields: Shape{{"site", declGuard(domain.IsImportSite)}}, At: CommandPlacementLocal},
	{Type: "browser-import.filter", Fields: Shape{{"text", func(value jsontext.Value) bool {
		s, ok := declString(value)
		return ok && declUTF16Len(s) <= 256
	}}}, At: CommandPlacementLocal},
	{Type: "browser-import.run", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "browser-import.again", Fields: Shape{}, At: CommandPlacementLocal},
}
