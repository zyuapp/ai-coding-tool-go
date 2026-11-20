package agent

import "github.com/zyuapp/ai-coding-tool-go/internal/tools"

// The bridges app tools call through, scoped to one thread.
type (
	AutomationBridge   = tools.AutomationBridge
	FindingBridge      = tools.FindingBridge
	ThreadBridge       = tools.ThreadBridge
	MemoryNote         = tools.MemoryNote
	CoordinationBridge = tools.CoordinationBridge
	BrowserBridge      = tools.BrowserBridge
	SimulatorBridge    = tools.SimulatorBridge
	TerminalBridge     = tools.TerminalBridge
)
