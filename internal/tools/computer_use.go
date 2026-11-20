package tools

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const ComputerUseSetupServerName = "aicodingtool-computer-use"

// ComputerUseSetupBridge opens the app's computer-use settings for the user; the run itself can do
// nothing more until they finish.
type ComputerUseSetupBridge interface {
	RequestSetup()
}

type requestSetupFunc func()

func (f requestSetupFunc) RequestSetup() { f() }

// ComputerUseSetupTools is the one tool a run gets in place of computer use while the user still has to enable it.
var ComputerUseSetupTools = []Definition[ComputerUseSetupBridge]{
	{
		Name:        "request_setup",
		Description: "Use when a task requires operating another application's interface but computer use needs to be enabled in AICodingTool. Call this rather than telling the user to install or configure anything.",
		Input:       Shape{},
		ReadOnly:    true,
		Run: func(_ context.Context, bridge ComputerUseSetupBridge, _ Args) ToolResult {
			bridge.RequestSetup()
			return textResult("AICodingTool opened Settings → Computer use. Ask the user to complete the required permissions, then retry after AICodingTool restarts.")
		},
	},
}

func ComputerUseSetupToolsFor(bridge ComputerUseSetupBridge) []BoundTool {
	return Bind(bridge, ComputerUseSetupTools)
}

// ComputerUseSetupServer is offered while the user still has to enable computer use, so the run can ask them to.
var ComputerUseSetupServer = Server{
	Name:        ComputerUseSetupServerName,
	Definitions: infos(ComputerUseSetupTools),
	Grants:      GrantsReadOnly,
	Serve: func(run *Run) ([]BoundTool, bool) {
		if _, setup := run.ComputerUse.(domain.ComputerUseRunConfigSetupRequired); !setup {
			return nil, false
		}
		emit := run.EmitSetupRequired
		return ComputerUseSetupToolsFor(requestSetupFunc(func() {
			if emit != nil {
				emit()
			}
		})), true
	},
}
