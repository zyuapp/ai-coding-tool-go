package host

import "maps"

// modules lists every capability's host module, in the order their subscriptions start.
var modules = []HostModule{
	EngineHost, SimulatorHost, RunHost, MobileHost, UpdateHost, DictationHost, BrowserHost, TerminalHost, FindHost,
	AutomationHost, ProjectHost, ComputerHost, AttachmentHost, SurfaceHost, SystemHost,
}

var HostModules = modules

// HostModulesEffectHandlers holds every effect the reducer can describe, each with the one handler that carries it out.
var HostModulesEffectHandlers = func() EffectHandlers {
	handlers := EffectHandlers{}
	for _, module := range modules {
		maps.Copy(handlers, module.Effects)
	}
	return handlers
}()
