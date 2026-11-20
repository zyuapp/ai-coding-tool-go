package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var imageGallery Validator = func(value jsontext.Value) bool {
	if !Array(declGuard(domain.IsImageSource))(value) {
		return false
	}
	items, _ := declArray(value)
	return len(items) <= 500
}

var pastedText = Object(Shape{{"id", Text}, {"text", Text}})
var attachedFileDraft = Object(Shape{{"path", Text}, {"name", Text}, {"folder", Optional(Literals(true))}})
var attachedFile = Object(Shape{{"id", Text}, {"path", Text}, {"name", Text}, {"folder", Optional(Literals(true))}})
var annotation = Object(Shape{{"id", Text}, {"quote", Text}, {"note", Text}, {"anchor", Optional(isAnnotationAnchor)}})

func isAnnotationAnchor(value jsontext.Value) bool {
	record, ok := declRecord(value)
	if !ok {
		return false
	}
	if declIsKind(record, "message") {
		return Text(record["messageId"]) && Number(record["start"]) && Number(record["end"])
	}
	if declIsKind(record, "diff") {
		side, _ := declString(record["side"])
		return Text(record["comparison"]) && Text(record["path"]) && Text(record["start"]) && Text(record["end"]) && (side == "old" || side == "new")
	}
	return false
}

const maxQuoteLength = 100_000

// maxRecall is how many drafted annotations or pastes one recall may carry.
const maxRecall = 100

func isAnchor(value jsontext.Value) bool {
	record, ok := declRecordOf(value)
	if !ok {
		return false
	}
	if declIsKind(record, "message") {
		return PhoneCommandsIsString(record["messageId"]) && PhoneCommandsIsCount(record["start"]) && PhoneCommandsIsCount(record["end"])
	}
	if !declIsKind(record, "diff") {
		return false
	}
	side, _ := declString(record["side"])
	return PhoneCommandsIsString(record["comparison"], 4_096) &&
		PhoneCommandsIsString(record["path"], 4_096) &&
		PhoneCommandsIsString(record["start"]) &&
		PhoneCommandsIsString(record["end"]) &&
		(side == "old" || side == "new")
}

func isAnnotation(value jsontext.Value) bool {
	record, ok := declRecordOf(value)
	if !ok {
		return false
	}
	return PhoneCommandsIsString(record["id"]) &&
		PhoneCommandsIsString(record["quote"], maxQuoteLength) &&
		PhoneCommandsIsBlankable(record["note"], maxQuoteLength) &&
		(declUndefined(record["anchor"]) || isAnchor(record["anchor"]))
}

func isPastedText(value jsontext.Value) bool {
	record, ok := declRecordOf(value)
	return ok && PhoneCommandsIsString(record["id"]) && PhoneCommandsIsString(record["text"], PhoneCommandsMaxPromptLength)
}

func isBoundedList(value jsontext.Value, item func(entry jsontext.Value) bool) bool {
	items, ok := declArray(value)
	if !ok || len(items) > maxRecall {
		return false
	}
	for _, entry := range items {
		if !item(entry) {
			return false
		}
	}
	return true
}

var annotates PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) &&
		PhoneCommandsIsString(command["quote"], maxQuoteLength) &&
		(declUndefined(command["note"]) || PhoneCommandsIsBlankable(command["note"], maxQuoteLength)) &&
		(declUndefined(command["anchor"]) || isAnchor(command["anchor"]))
}

var notesAnnotation PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["annotationId"]) && PhoneCommandsIsBlankable(command["note"], maxQuoteLength)
}

var removesAnnotation PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["annotationId"])
}

var recallsAnnotations PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && isBoundedList(command["annotations"], isAnnotation)
}

var pastes PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["text"], PhoneCommandsMaxPromptLength)
}

var removesPaste PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["pasteId"])
}

var recallsPastes PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && isBoundedList(command["pastes"], isPastedText)
}

var AnnotationCommands = Declarations{
	{Type: "annotation.add", Fields: Shape{{"taskId", OptionalText}, {"quote", Text}, {"note", OptionalText}, {"anchor", Optional(isAnnotationAnchor)}}, At: CommandPlacementLocal, Phone: annotates},
	{Type: "annotation.note", Fields: Shape{{"taskId", OptionalText}, {"annotationId", Text}, {"note", Text}}, At: CommandPlacementLocal, Phone: notesAnnotation},
	{Type: "annotation.remove", Fields: Shape{{"taskId", OptionalText}, {"annotationId", Text}}, At: CommandPlacementLocal, Phone: removesAnnotation},
	{Type: "annotation.recall", Fields: Shape{{"taskId", OptionalText}, {"annotations", Array(annotation)}}, At: CommandPlacementLocal, Phone: recallsAnnotations},
	{Type: "annotation.reveal", Fields: Shape{{"taskId", OptionalText}, {"annotationId", Text}}, At: CommandPlacementLocal},
}

var PasteCommands = Declarations{
	{Type: "paste.add", Fields: Shape{{"taskId", OptionalText}, {"text", Text}}, At: CommandPlacementLocal, Phone: pastes},
	{Type: "paste.remove", Fields: Shape{{"taskId", OptionalText}, {"pasteId", Text}}, At: CommandPlacementLocal, Phone: removesPaste},
	{Type: "paste.recall", Fields: Shape{{"taskId", OptionalText}, {"pastes", Array(pastedText)}}, At: CommandPlacementLocal, Phone: recallsPastes},
}

var ImageCommands = Declarations{
	{Type: "image.add", Fields: Shape{{"taskId", OptionalText}, {"path", Text}, {"label", Text}, {"source", OptionalText}}, At: CommandPlacementLocal},
	{Type: "image.remove", Fields: Shape{{"taskId", OptionalText}, {"imageId", Text}}, At: CommandPlacementLocal},
	{Type: "image.recall", Fields: Shape{{"taskId", OptionalText}, {"paths", Array(Text)}}, At: CommandPlacementLocal},
}

var ImageViewCommands = Declarations{
	{Type: "image.open", Fields: Shape{{"source", declGuard(domain.IsImageSource)}, {"gallery", Optional(imageGallery)}}, At: CommandPlacementLocal},
	{Type: "image.step", Fields: Shape{{"delta", Step}}, At: CommandPlacementLocal},
	{Type: "image.close", Fields: Shape{}, At: CommandPlacementLocal},
	{Type: "image.download", Fields: Shape{}, At: CommandPlacementLocal},
}

var FileCommands = Declarations{
	{Type: "file.open", Fields: Shape{{"taskId", OptionalText}, {"path", Text}, {"line", Optional(Number)}}, At: CommandPlacementOwn},
	{Type: "file.attach", Fields: Shape{{"taskId", OptionalText}, {"files", Array(attachedFileDraft)}}, At: CommandPlacementLocal},
	{Type: "file.detach", Fields: Shape{{"taskId", OptionalText}, {"fileId", Text}}, At: CommandPlacementLocal},
	{Type: "file.recall", Fields: Shape{{"taskId", OptionalText}, {"files", Array(attachedFile)}}, At: CommandPlacementLocal},
}
