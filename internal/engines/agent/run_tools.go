package agent

import "slices"

// The app tool servers' names, as every engine files their tools.
const (
	AutomationServerName       = "aicodingtool-automation"
	BrowserServerName          = "aicodingtool-browser"
	ComputerUseSetupServerName = "aicodingtool-computer-use"
	TerminalServerName         = "aicodingtool-terminal"
	SimulatorServerName        = "aicodingtool-simulator"
	CoordinationServerName     = "aicodingtool-coordination"
	ThreadServerName           = "aicodingtool-threads"
)

type ServedToolSet struct {
	Server string
	Tools  []BoundTool
}

// Every app tool a run reaches, grouped by the server it is filed under and already short of what
// the channel withholds. An engine that serves this list flat offers the same tools Claude sees.
func RunTools(input *ProviderRunInput) []ServedToolSet {
	var sets []ServedToolSet
	for _, server := range ToolServers() {
		if server.Serve == nil {
			continue
		}
		tools, ok := server.Serve(input)
		if !ok {
			continue
		}
		sets = append(sets, ServedToolSet{Server: server.Name, Tools: OfferedOn(input.Channel, server.Name, slices.Clip(tools))})
	}
	return sets
}
