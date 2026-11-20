package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

var CliInputs = []string{"cli.read", "cli.install", "cli.uninstall", "cli.read-status", "cli.failed"}

func ReduceCli(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	cli := state.Cli
	switch input := input.(type) {
	case contracts.CliCommandCliRead:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectCliRead{}}}
	case contracts.CliCommandCliInstall, contracts.CliCommandCliUninstall:
		var effect WorkspaceEffect = WorkspaceEffectCliInstall{}
		if _, uninstall := input.(contracts.CliCommandCliUninstall); uninstall {
			effect = WorkspaceEffectCliUninstall{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Cli = CliState{Status: cli.Status, Busy: true, Error: nil}
		}, Effects: []WorkspaceEffect{effect}}
	case CliEventCliReadStatus:
		status := input.Status
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Cli = CliState{Status: &status, Busy: false, Error: cli.Error}
		}}
	case CliEventCliFailed:
		message := input.Message
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Cli = CliState{Status: cli.Status, Busy: false, Error: &message}
		}}
	}
	return SliceTransition{}
}
