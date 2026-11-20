package contracts

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

// sessionResourcesNamed returns value as an object when it carries a taskId of 1 to 256 characters.
func sessionResourcesNamed(value any) (map[string]any, bool) {
	record, ok := ipcRecord(value)
	return record, ok && IpcIsString(record["taskId"])
}

func IsSessionResourceMessage(value any) bool {
	message, ok := sessionResourcesNamed(value)
	if !ok || !IpcIsString(message["sessionId"]) {
		return false
	}
	_, busy := message["busy"].(bool)
	switch message["type"] {
	case "session.state":
		return busy
	case "session.released":
		return true
	case "session.process":
		return ipcSafeInteger(message["pid"]) && ipcNumber(message["pid"]) > 1
	}
	return false
}

func IsTerminateSessionCommand(value any) bool {
	command, ok := sessionResourcesNamed(value)
	return ok && (!ipcHas(command, "idleOnly") || command["idleOnly"] == true) && command["type"] == "terminate-session" && IpcIsString(command["sessionId"])
}

func IsResourceStoppedEvent(value any) bool {
	event, ok := sessionResourcesNamed(value)
	return ok && event["type"] == "resource.stopped" && domain.IsResourceStop(event["stop"])
}
