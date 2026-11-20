package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var cancels PhoneRule = PhoneCommandsNamed

var decides PhoneRule = func(command map[string]jsontext.Value) bool {
	_, allow := declBool(command["allow"])
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsString(command["runId"]) && PhoneCommandsIsString(command["approvalId"]) && allow
}

var stopsProcess PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["processId"])
}

var answers PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsString(command["runId"]) &&
		PhoneCommandsIsString(command["requestId"]) && PhoneCommandsIsString(command["questionId"]) &&
		(declUndefined(command["text"]) || PhoneCommandsIsString(command["text"], PhoneCommandsMaxPromptLength)) && declUndefined(command["attachments"])
}

var RunControlCommands = Declarations{
	{Type: "resource.resume", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
	{Type: "run.cancel", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread, Agent: AgentThread, Phone: cancels},
	{Type: "run.compact", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "question.set-answer", Fields: Shape{{"taskId", Text}, {"runId", Text}, {"requestId", Text}, {"questionId", Text}, {"text", Text}}, At: CommandPlacementThread},
	{Type: "question.answer", Fields: Shape{{"taskId", Text}, {"runId", Text}, {"requestId", Text}, {"questionId", Text}, {"text", OptionalText}}, At: CommandPlacementThread, Phone: answers},
	{Type: "run.decide", Fields: Shape{{"allow", Boolean}, {"taskId", Text}, {"runId", Text}, {"approvalId", Text}}, At: CommandPlacementThread, Phone: decides},
	{Type: "run.stop-process", Fields: Shape{{"taskId", OptionalText}, {"processId", Text}}, At: CommandPlacementThread, Phone: stopsProcess},
	{Type: "limit.resume", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
	{Type: "limit.cancel", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
}

var ReviewCommands = Declarations{
	{Type: "review.open", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread},
	{Type: "review.close", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "review.set-step", Fields: Shape{{"step", Literals("targets", "base", "commit", "custom")}}, At: CommandPlacementThread},
	{Type: "review.start", Fields: Shape{{"taskId", OptionalText}, {"target", declGuard(domain.IsReviewTarget)}}, At: CommandPlacementThread},
}
