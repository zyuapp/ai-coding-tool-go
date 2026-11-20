package tools

// Servers is every app tool server, in the order a run is offered them and told of them.
func Servers() []Server {
	return []Server{
		ComputerUseSetupServer,
		AutomationServer,
		ThreadServer,
		CoordinationServer,
		BrowserServer,
		SimulatorServer,
		TerminalServer,
	}
}
