package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// ComputerUseInputs are the input types ReduceComputerUse takes.
var ComputerUseInputs = []string{"computer-use.read", "computer-use.enable", "computer-use.restart", "computer-use.permissions", "computer-use.failed"}

// What the app may see and operate as one input leaves it. Only the functions here make one.
type computerUseChange = ComputerUseEdit

func ComputerUseOf(state *WorkspaceState) ComputerUseEdit {
	return ComputerUseEdit{View: ComputerUseSliceState{ComputerUsePermissions: state.ComputerUsePermissions, ComputerUseSetup: state.ComputerUseSetup}}
}

// SetupShownIn is the setup that holds settings open on its own, shown because a run needs it, or put away.
func SetupShownIn(edit ComputerUseEdit, shown bool) ComputerUseEdit {
	edit.View.ComputerUseSetup = shown
	return edit
}

// ReduceComputerUse takes an input whose type is one of ComputerUseInputs.
func ReduceComputerUse(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	access := state.ComputerUsePermissions
	switch input := input.(type) {
	case contracts.ComputerUseAccessCommandComputerUseRead:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectComputerUseRead{}}}

	case contracts.ComputerUseAccessCommandComputerUseRestart:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectComputerUseRestart{}}}

	// The platform's own dialog answers for this one, so the row waits on it rather than on a poll.
	case contracts.ComputerUseAccessCommandComputerUseEnable:
		next := access
		next.Busy = new(input.Permission)
		next.Error = nil
		next.Requested = true
		return SliceTransition{
			Patch:   func(state *WorkspaceState) { state.ComputerUsePermissions = next },
			Effects: []WorkspaceEffect{WorkspaceEffectComputerUseEnable{Permission: input.Permission}},
		}

	case ComputerUseEventComputerUsePermissions:
		enabling := input.Enabling != nil && *input.Enabling
		// A full set is only worth a restart to a window that sent the user off to grant one.
		restartRequired := access.RestartRequired || (access.Requested && input.Permissions.Accessibility && input.Permissions.ScreenRecording)
		// The poll asks every second; an answer that says what the last one said leaves the window alone.
		unchanged := access.Permissions != nil &&
			domain.SameComputerUsePermissions(*access.Permissions, input.Permissions) &&
			restartRequired == access.RestartRequired &&
			!(enabling && access.Busy != nil)
		if unchanged {
			return SliceTransition{}
		}
		next := access
		next.Permissions = new(input.Permissions)
		next.RestartRequired = restartRequired
		if enabling {
			next.Busy = nil
		}
		return SliceTransition{Patch: func(state *WorkspaceState) { state.ComputerUsePermissions = next }}

	case ComputerUseEventComputerUseFailed:
		next := access
		next.Error = new(input.Message)
		if input.Enabling != nil && *input.Enabling {
			next.Busy = nil
		}
		return SliceTransition{Patch: func(state *WorkspaceState) { state.ComputerUsePermissions = next }}
	}
	return SliceTransition{}
}
