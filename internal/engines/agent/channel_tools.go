package agent

import "github.com/zyuapp/ai-coding-tool-go/internal/contracts"

// Asked through the UI instead, on every channel. A thread's checkout is the app's to keep, so it
// changes through move_to_worktree, never behind the app's back.
var alwaysWithheld = []string{"AskUserQuestion", "EnterWorktree", "ExitWorktree"}

// One of the app's own tools, by the server it is filed under.
type WithheldTool struct {
	Server string
	Tool   string
}

// The one place a tool is granted or withheld by surface. A side chat is discarded when it closes
// and is listed nowhere while it is open, so it must not leave anything scheduled behind: it can
// read automations but not write them. Grant a write here only if closing the chat also undoes it.
var WithheldByChannel = map[contracts.RunChannel][]WithheldTool{
	contracts.RunChannelMain: {},
	contracts.RunChannelSide: {
		{Server: AutomationServerName, Tool: "schedule"},
		{Server: AutomationServerName, Tool: "update"},
		{Server: AutomationServerName, Tool: "stop"},
		// A side chat is in no list the user can reach, so a finding raised there could never be read.
		{Server: AutomationServerName, Tool: "notify"},
		{Server: AutomationServerName, Tool: "nothing_to_report"},
		// A side chat works in its thread's checkout, so it has none of its own to move.
		{Server: ThreadServerName, Tool: "move_to_worktree"},
	},
}

// The names Claude is told not to use on a channel.
func WithheldTools(channel contracts.RunChannel) []string {
	names := append([]string{}, alwaysWithheld...)
	for _, entry := range WithheldByChannel[channel] {
		names = append(names, McpToolName(entry.Server, entry.Tool))
	}
	return names
}

// A server's tools short of what the channel withholds, for a host that serves only what it lists.
func OfferedOn(channel contracts.RunChannel, server string, tools []BoundTool) []BoundTool {
	withheld := WithheldByChannel[channel]
	offered := make([]BoundTool, 0, len(tools))
	for _, tool := range tools {
		blocked := false
		for _, entry := range withheld {
			if entry.Server == server && entry.Tool == tool.Name {
				blocked = true
				break
			}
		}
		if !blocked {
			offered = append(offered, tool)
		}
	}
	return offered
}
