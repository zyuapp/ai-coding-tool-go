package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// AppUpdateInputs are the input types ReduceAppUpdate takes.
var AppUpdateInputs = []string{"app.update", "app.cancel-update", "app.build", "app.launched", "app.check-for-updates", "app.download-update", "app.install-update", "app.update-changed"}

const NoUpdateToInstall = "There is no newer version to install."

// RunningThreads is how many threads an install would cut off. One paused on a usage limit is left
// out: it resumes on its own after the restart, and waiting for it could take hours.
func RunningThreads(state *WorkspaceState) int {
	activity := ThreadActivityThreadActivity(state)
	count := 0
	for id := range activity.Working {
		if !activity.Paused.Has(id) {
			count++
		}
	}
	return count
}

// ReduceAppUpdate takes an input whose type is one of AppUpdateInputs.
func ReduceAppUpdate(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.ExternalAppCommandAppCheckForUpdates:
		effect := WorkspaceEffectAppCheckForUpdates{}
		if input.Quiet != nil && *input.Quiet {
			effect.Quiet = new(true)
		}
		return SliceTransition{Effects: []WorkspaceEffect{effect}}
	// Main answers with the stage it moved to, so neither command writes the update itself.
	case contracts.ExternalAppCommandAppDownloadUpdate:
		if _, ok := state.AppUpdate.(domain.AppUpdateAvailable); ok {
			return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAppDownloadUpdate{}}}
		}
		return SliceTransition{Effects: []WorkspaceEffect{}}
	case contracts.ExternalAppCommandAppInstallUpdate:
		if _, ok := state.AppUpdate.(domain.AppUpdateReady); ok {
			return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAppInstallUpdate{}}}
		}
		return SliceTransition{Effects: []WorkspaceEffect{}}
	case AppUpdateEventAppUpdateChanged:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.AppUpdate = input.Update }}
	case AppUpdateEventAppBuild:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.AppBuild = input.Build }}
	// The launch an update brought owes the engines that build needs, once they have been read.
	case AppUpdateEventAppLaunched:
		toasts, effects := ToastIn(ToastsOf(state), &domain.Toast{Tone: domain.ToastToneSuccess, Title: "AI Coding Tool updated", Message: new("Now on " + input.Version + ".")}, Now())
		return SliceTransition{Patch: func(next *WorkspaceState) { next.EngineUpgradesOwed = true }, Edits: []OwnerEdit{toasts}, Effects: toastDismissals(effects)}
	// An install already under way cannot be called back.
	case contracts.ExternalAppCommandAppCancelUpdate:
		if _, installing := state.AppUpdateRequest.(domain.AppUpdateRequestInstalling); installing {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.AppUpdateRequest = nil }}
	case contracts.ExternalAppCommandAppUpdate:
		if _, idle := state.AppUpdate.(domain.AppUpdateIdle); idle || state.AppUpdate == nil {
			return SliceTransition{Refusal: new(NoUpdateToInstall)}
		}
		switch state.AppUpdateRequest.(type) {
		case domain.AppUpdateRequestWaiting, domain.AppUpdateRequestInstalling:
			return SliceTransition{}
		}
		effects := []WorkspaceEffect{}
		if _, available := state.AppUpdate.(domain.AppUpdateAvailable); available {
			effects = []WorkspaceEffect{WorkspaceEffectAppDownloadUpdate{Unattended: new(true)}}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.AppUpdateRequest = domain.AppUpdateRequestWaiting{} }, Effects: effects}
	}
	return SliceTransition{}
}

// upgradeNeededEngines starts the upgrade of every engine this build needs and can upgrade itself,
// one after another.
func upgradeNeededEngines(transition WorkspaceTransition) WorkspaceTransition {
	owed := transition.State
	owed.EngineUpgradesOwed = false
	current := transition
	current.State = owed
	for _, engine := range domain.AgentEngines {
		readiness := EngineReadinessOf(&current.State, engine)
		if update := domain.AgentEngineEngineUpdate(&readiness); readiness.Required == nil || *readiness.Required == "" || update == nil || update.Command == nil {
			continue
		}
		state := current.State
		next := Applied(&state, ReduceEngine(&state, contracts.EngineCommandEngineUpdate{Engine: engine}))
		current.State = next.State
		current.Effects = slices.Concat(current.Effects, next.Effects)
	}
	return current
}

// ReconcileAppUpdate moves an asked-for update along whatever input arrives. A download that falls
// back to offering the version has failed; a downloaded build installs the moment nothing is
// running. Engines owed by the launch an update brought are upgraded once the launch's first read of
// them is in.
func ReconcileAppUpdate(before *WorkspaceState, transition WorkspaceTransition) WorkspaceTransition {
	state := &transition.State
	next := transition
	if state.EngineUpgradesOwed && state.EngineUpdatesOffered && !state.EngineChecking {
		next = upgradeNeededEngines(next)
	}
	request := state.AppUpdateRequest
	update := state.AppUpdate
	// An install that left the build no longer ready failed, since a good one quits before saying anything.
	if installing, ok := request.(domain.AppUpdateRequestInstalling); ok {
		if _, ready := update.(domain.AppUpdateReady); !ready {
			next.State.AppUpdateRequest = domain.AppUpdateRequestFailed{Message: "Couldn't install " + installing.Version + "."}
			return next
		}
	}
	if _, waiting := request.(domain.AppUpdateRequestWaiting); !waiting {
		return next
	}
	if _, downloading := before.AppUpdate.(domain.AppUpdateDownloading); downloading {
		if available, ok := update.(domain.AppUpdateAvailable); ok {
			next.State.AppUpdateRequest = domain.AppUpdateRequestFailed{Message: "Couldn't download " + available.Version + "."}
			return next
		}
	}
	ready, ok := update.(domain.AppUpdateReady)
	if !ok || RunningThreads(state) > 0 {
		return next
	}
	next.State.AppUpdateRequest = domain.AppUpdateRequestInstalling{Version: ready.Version}
	next.Effects = slices.Concat(next.Effects, []WorkspaceEffect{WorkspaceEffectAppInstallUpdate{Unattended: new(true)}})
	return next
}
