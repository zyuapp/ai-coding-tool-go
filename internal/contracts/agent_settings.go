package contracts

// IsAgentSettingsReloadEvent reports whether a decoded JSON value (map[string]any, []any, float64, string, bool, nil) is an AgentSettingsReloadEvent.
func IsAgentSettingsReloadEvent(value any) bool {
	event, ok := ipcRecord(value)
	if !ok {
		return false
	}
	status := event["status"]
	return event["type"] == "engine.settings-reload-status" &&
		(status == "pending" || status == "reloaded" || status == "failed") &&
		(!ipcHas(event, "message") || IpcIsString(event["message"], 100_000))
}
