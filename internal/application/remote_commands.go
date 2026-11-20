package application

import "github.com/zyuapp/ai-coding-tool-go/internal/contracts"

// Everything about the bridge is named `remote.`, so one test sorts the whole family out of the way.
func IsRemoteInput(input WorkspaceInput) bool {
	_, ok := input.(RemoteInput)
	return ok
}

func ReduceRemote(_ *WorkspaceState, input RemoteInput) SliceTransition {
	switch input := input.(type) {
	case RemoteEvent:
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Remote = input.Remote
			next.RemoteChecking = false
		}}
	// A read only waits on Tailscale, so it leaves whatever the window last said on screen.
	case contracts.RemoteCommandRemoteRefresh:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.RemoteChecking = true }, Effects: []WorkspaceEffect{WorkspaceEffectRemoteRefresh{}}}
	case contracts.RemoteCommandRemoteSetEnabled:
		return SliceTransition{ClearsError: true, Effects: []WorkspaceEffect{WorkspaceEffectRemoteSetEnabled{Enabled: input.Enabled}}}
	case contracts.RemoteCommandRemoteCreatePairingCode:
		return SliceTransition{ClearsError: true, Effects: []WorkspaceEffect{WorkspaceEffectRemoteCreatePairingCode{}}}
	case contracts.RemoteCommandRemoteRevokeDevice:
		return SliceTransition{ClearsError: true, Effects: []WorkspaceEffect{WorkspaceEffectRemoteRevokeDevice{DeviceID: input.DeviceID}}}
	}
	return SliceTransition{}
}
