package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func isScheduleDraft(value jsontext.Value) bool {
	if _, ok := declRecord(value); !ok {
		return false
	}
	decoded, ok := declAny(value)
	if !ok {
		return false
	}
	draft := decoded.(map[string]any)
	draft["taskId"] = "view"
	return domain.IsAutomationDraft(draft)
}

var AutomationCommands = Declarations{
	{Type: "automation.notify", Fields: Shape{{"taskId", Text}, {"headline", Text}, {"detail", OptionalText}, {"key", OptionalText}}, At: CommandPlacementThread},
	{Type: "automation.nothing-to-report", Fields: Shape{{"taskId", Text}, {"checked", Text}}, At: CommandPlacementThread},
	{Type: "automation.save", Fields: Shape{{"taskId", OptionalText}, {"draft", isScheduleDraft}}, At: CommandPlacementThread},
	{Type: "automation.update", Fields: Shape{{"taskId", OptionalText}, {"patch", declGuard(domain.IsAutomationPatch)}}, At: CommandPlacementThread},
	{Type: "automation.delete", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "automation.run-now", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
}
