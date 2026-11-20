package contracts

import (
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// MobileProtocolVersion is bumped when either side needs to know the other has a change.
const MobileProtocolVersion = 8

// OldestPhoneProtocolVersion is the oldest phone app this computer serves.
const OldestPhoneProtocolVersion = 8

const (
	mobileMaxDeviceNameLength = 128
	mobileMaxTokenLength      = 512
)

func IsMobileQuery(value any) bool {
	query, ok := ipcRecord(value)
	if !ok {
		return false
	}
	switch query["kind"] {
	case "history":
		return PhoneCommandsIsString(query["taskId"]) && ipcSafeInteger(query["before"]) && ipcNumber(query["before"]) >= 0
	case "directories":
		prefix, ok := query["prefix"].(string)
		return ok && ipcLength(prefix) <= PhoneCommandsMaxPathLength && !strings.Contains(prefix, "\x00")
	case "thread":
		return PhoneCommandsIsString(query["taskId"])
	case "archived":
		return true
	case "commands":
		return !ipcHas(query, "taskId") || PhoneCommandsIsString(query["taskId"])
	case "transcribe":
		return domain.IsDictationAudio(query["audio"])
	case "image":
		_, preview := query["preview"].(bool)
		return PhoneCommandsIsString(query["taskId"]) && PhoneCommandsIsString(query["messageId"]) && PhoneCommandsIsString(query["href"], PhoneCommandsMaxPathLength) &&
			(!ipcHas(query, "preview") || preview)
	}
	if !PhoneCommandsIsString(query["taskId"]) || !domain.IsDiffRange(query["range"]) {
		return false
	}
	if query["kind"] == "diff-summary" {
		return true
	}
	return query["kind"] == "diff-patch" && PhoneCommandsIsString(query["path"], PhoneCommandsMaxPathLength) &&
		(!ipcHas(query, "previousPath") || PhoneCommandsIsString(query["previousPath"], PhoneCommandsMaxPathLength))
}

// IsMobileClientMessage reads a phone's message defensively: it is the security boundary.
func IsMobileClientMessage(value any) bool {
	message, ok := ipcRecord(value)
	if !ok {
		return false
	}
	switch message["kind"] {
	case "pair":
		return PhoneCommandsIsCount(message["version"]) &&
			(!ipcHas(message, "oldestComputer") || PhoneCommandsIsCount(message["oldestComputer"])) &&
			PhoneCommandsIsString(message["code"], mobileMaxTokenLength) &&
			PhoneCommandsIsString(message["deviceName"], mobileMaxDeviceNameLength)
	case "resume":
		return PhoneCommandsIsCount(message["version"]) &&
			(!ipcHas(message, "oldestComputer") || PhoneCommandsIsCount(message["oldestComputer"])) &&
			PhoneCommandsIsString(message["token"], mobileMaxTokenLength) &&
			(!ipcHas(message, "sessionId") || PhoneCommandsIsString(message["sessionId"])) &&
			PhoneCommandsIsCount(message["lastSequence"])
	case "command":
		return PhoneCommandsIsString(message["requestId"]) && IsPhoneCommand(message["command"])
	case "query":
		return PhoneCommandsIsString(message["requestId"]) && IsMobileQuery(message["query"])
	case "pong", "ping":
		return PhoneCommandsIsCount(message["at"])
	case "push-token":
		return ipcHas(message, "push") && message["push"] == nil || domain.IsPhonePush(message["push"])
	}
	return false
}

// IsMobileRequest is a phone's message on its way to the window, which is the only process holding workspace
// state. The server never answers one itself: it forwards, and the window says what happened.
func IsMobileRequest(value any) bool {
	request, ok := ipcRecord(value)
	if !ok || request["type"] != "mobile.request" || !PhoneCommandsIsString(request["requestId"]) || !PhoneCommandsIsString(request["sessionId"]) {
		return false
	}
	if ipcHas(request, "answerBy") && !ipcFinite(request["answerBy"]) {
		return false
	}
	switch request["op"] {
	case "snapshot":
		return true
	case "command":
		return IsPhoneCommand(request["command"])
	case "query":
		return IsMobileQuery(request["query"])
	}
	return false
}

func IsMobileResponse(value any) bool {
	response, ok := ipcRecord(value)
	if !ok || response["type"] != "mobile.response" || !PhoneCommandsIsString(response["requestId"]) {
		return false
	}
	return response["ok"] == true || (response["ok"] == false && PhoneCommandsIsBlankable(response["message"], PhoneCommandsMaxPromptLength))
}
