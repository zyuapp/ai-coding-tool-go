package contracts

import "encoding/json/jsontext"

// sendReachEntry is who may set one field of a send: "anyone", "starting" or "window".
type sendReachEntry struct {
	Field string
	Reach string
}

// sendReach declares every field of a send once, so a new one is refused from outside until it says who may set it.
var sendReach = []sendReachEntry{
	{"taskId", "anyone"},
	{"project", "anyone"},
	{"text", "anyone"},
	{"steer", "anyone"},
	{"worktree", "anyone"},
	{"worktreeId", "anyone"},
	{"model", "starting"},
	{"engine", "starting"},
	{"effort", "starting"},
	{"role", "starting"},
	{"brief", "starting"},
	{"title", "starting"},
	{"attachments", "window"},
	{"coordinatorId", "window"},
	{"from", "window"},
	{"sender", "window"},
	{"lead", "window"},
	{"memory", "window"},
}

// SendStaysInReach: whether a send sets only what its caller may: the window's fields never, and a new
// thread's shape only an agent starting one.
func SendStaysInReach(command map[string]jsontext.Value, caller SendCaller) bool {
	for _, entry := range sendReach {
		if declUndefined(command[entry.Field]) || entry.Reach == "anyone" {
			continue
		}
		if entry.Reach == "window" || caller == SendCallerPhone || !declUndefined(command["taskId"]) {
			return false
		}
	}
	return true
}
