package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// IsEngineInput: everything about engine access is named `engine.`, so one test sorts the whole
// family out of the way.
func IsEngineInput(input WorkspaceInput) bool {
	_, ok := input.(EngineInput)
	return ok
}

func EngineAccessOf(state *WorkspaceState, engine domain.AgentEngine) domain.EngineAccess {
	return EngineReadinessOf(state, engine).Access
}

// RefreshEngines asks main about the engines again, for a user who has just gone and installed or
// upgraded one. Nothing is asked while every engine is fine, or while an earlier ask is still out.
func RefreshEngines(state *WorkspaceState) WorkspaceTransition {
	asked, effects := RefreshedEnginesIn(EnginesOf(state))
	if len(effects) == 0 {
		return WorkspaceTransition{State: *state, Effects: []WorkspaceEffect{}}
	}
	next := *state
	writeEngines(&next, asked.View)
	return WorkspaceTransition{State: next, Effects: effects}
}

// The engines as one input leaves them. Only the functions here make one.
type enginesChange = EnginesEdit

func EnginesOf(state *WorkspaceState) EnginesEdit {
	return EnginesEdit{View: EnginesState{
		EngineStatus:         state.EngineStatus,
		EngineChecking:       state.EngineChecking,
		EngineUpdating:       state.EngineUpdating,
		EngineUpdateQueue:    state.EngineUpdateQueue,
		EngineUpdatesOffered: state.EngineUpdatesOffered,
		AgentSettingsReload:  EnginesStateAgentSettingsReload(state.AgentSettingsReload),
	}}
}

// RefreshedEnginesIn is RefreshEngines on the engines an input is changing.
func RefreshedEnginesIn(edit EnginesEdit) (EnginesEdit, []WorkspaceEffect) {
	if edit.View.EngineChecking || !domain.EngineNeedsAttention(edit.View.EngineStatus) {
		return edit, []WorkspaceEffect{}
	}
	view := edit.View
	view.EngineChecking = true
	return EnginesEdit{View: view}, []WorkspaceEffect{WorkspaceEffectEngineRead{Refresh: new(true)}}
}

// nothingBlocked is true while no engine stands between the user and a run, which is when an engine
// error is stale.
func nothingBlocked(status domain.EngineStatus) bool {
	for _, engine := range domain.AgentEngines {
		if readiness := status.Get(engine); readiness != nil && domain.EngineIsBlocked(engine, readiness) {
			return false
		}
	}
	return true
}

// EngineUpdateKey is the key a closed offer is remembered by, so the same release is not offered
// again but the next one is.
func EngineUpdateKey(engine domain.AgentEngine, target string) string {
	return "engine-update:" + string(engine) + "@" + target
}

// engineWork is what an input has made of the engines, their toasts and the error banner so far,
// and the work it asks for (TS: engine-access.ts Work).
type engineWork struct {
	Engines EnginesState
	Toasts  ToastsEdit
	Effects []WorkspaceEffect
	Error   *SliceError
}

func engineAccessWorkOf(state *WorkspaceState) engineWork {
	return engineWork{Engines: EnginesOf(state).View, Toasts: ToastsOf(state), Effects: []WorkspaceEffect{}}
}

// engineAccessDone is the answer a piece of work comes to, naming only what it changed. `result` may be nil.
func engineAccessDone(state *WorkspaceState, work engineWork, result contracts.WorkspaceCommandResult) SliceTransition {
	engines := work.Engines
	changed := engines.EngineStatus != state.EngineStatus ||
		engines.EngineChecking != state.EngineChecking ||
		!sameEngine(engines.EngineUpdating, state.EngineUpdating) ||
		!jsonx.Same(engines.EngineUpdateQueue, state.EngineUpdateQueue) ||
		engines.EngineUpdatesOffered != state.EngineUpdatesOffered ||
		string(engines.AgentSettingsReload) != string(state.AgentSettingsReload)
	transition := SliceTransition{Edits: []OwnerEdit{work.Toasts}, Effects: work.Effects, Error: work.Error, Result: result}
	if changed {
		transition.Patch = func(next *WorkspaceState) { writeEngines(next, engines) }
	}
	return transition
}

var noError = &SliceError{Message: nil, Page: nil}

// toasted puts a toast up alongside the engine work already decided. `toast.ID` is ignored.
func toasted(work engineWork, toast *domain.Toast) engineWork {
	toasts, effects := ToastIn(work.Toasts, toast, Now())
	work.Toasts = toasts
	work.Effects = slices.Concat(work.Effects, toastDismissals(effects))
	return work
}

// updateOffer is the card that offers an engine's update when the launch first hears about the
// engines; nil for none.
func updateOffer(work engineWork, engine domain.AgentEngine) *domain.Toast {
	readiness := enginesReadiness(work.Engines, engine)
	update := domain.AgentEngineEngineUpdate(&readiness)
	if update == nil || slices.Contains(work.Toasts.View.DismissedToasts, EngineUpdateKey(engine, update.Target)) {
		return nil
	}
	label := domain.EngineLabel(string(engine))
	have := ""
	if update.Version != nil && *update.Version != "" {
		have = "You have " + *update.Version + "."
	}
	needed := have
	if readiness.Required != nil && *readiness.Required != "" {
		needed = "This app needs " + *readiness.Required + " or newer. " + have
	}
	message := strings.TrimSpace(needed + " Update it the way you installed it.")
	if update.Command != nil {
		message = strings.TrimSpace(needed)
	}
	toast := &domain.Toast{
		Tone:       domain.ToastToneUpdate,
		Title:      label + " " + update.Target + " is available",
		Message:    &message,
		Subject:    new(string(engine)),
		Persistent: new(true),
		Remember:   new(EngineUpdateKey(engine, update.Target)),
	}
	if update.Command != nil {
		toast.Action = &domain.ToastAction{Label: "Upgrade", Command: domain.ToastActionCommand{Engine: engine}}
	}
	return toast
}

// startUpdate starts an engine's update, saying so in place of the card that offered it.
func startUpdate(work engineWork, engine domain.AgentEngine) engineWork {
	label := domain.EngineLabel(string(engine))
	readiness := enginesReadiness(work.Engines, engine)
	update := domain.AgentEngineEngineUpdate(&readiness)
	target := "undefined"
	if update != nil {
		target = update.Target
	}
	message := "Moving to " + target + "."
	if update != nil && update.Version != nil && *update.Version != "" {
		message = "Moving from " + *update.Version + " to " + update.Target + "."
	}
	work.Engines.EngineUpdating = &engine
	work.Error = noError
	work.Effects = slices.Concat(work.Effects, []WorkspaceEffect{WorkspaceEffectEngineUpdate{Engine: engine}})
	return toasted(work, &domain.Toast{Tone: domain.ToastToneProgress, Title: "Updating " + label, Message: &message, Subject: new(string(engine)), Persistent: new(true)})
}

// nextUpdate starts the next engine waiting its turn, since package managers lock. One brought up to
// date meanwhile is passed over.
func nextUpdate(work engineWork) engineWork {
	engines := work.Engines
	queue := []domain.AgentEngine{}
	for _, engine := range engines.EngineUpdateQueue {
		readiness := enginesReadiness(engines, engine)
		if update := domain.AgentEngineEngineUpdate(&readiness); update != nil && update.Command != nil {
			queue = append(queue, engine)
		}
	}
	next := work
	if len(queue) > 0 {
		next.Engines.EngineUpdateQueue = queue[1:]
		return startUpdate(next, queue[0])
	}
	next.Engines.EngineUpdateQueue = queue
	if len(queue) == len(engines.EngineUpdateQueue) {
		return work
	}
	return next
}

// updateResult is how an update went: the version it reached, or that the engine is still behind.
func updateResult(engines EnginesState, engine domain.AgentEngine) *domain.Toast {
	label := domain.EngineLabel(string(engine))
	readiness := enginesReadiness(engines, engine)
	if update := domain.AgentEngineEngineUpdate(&readiness); update != nil {
		run := ""
		if update.Command != nil {
			run = " Run `" + *update.Command + "` in your terminal."
		}
		version := "an older version"
		if update.Version != nil {
			version = *update.Version
		}
		return &domain.Toast{Tone: domain.ToastToneError, Title: "Couldn't update " + label, Message: new(label + " is still on " + version + "." + run), Subject: new(string(engine)), Persistent: new(true)}
	}
	toast := &domain.Toast{Tone: domain.ToastToneSuccess, Title: label + " updated", Subject: new(string(engine))}
	if readiness.Version != nil && *readiness.Version != "" {
		toast.Message = new("Now on " + *readiness.Version + ".")
	}
	return toast
}

func ReduceEngine(state *WorkspaceState, input EngineInput) SliceTransition {
	work := engineAccessWorkOf(state)
	engines := work.Engines
	switch input := input.(type) {
	case contracts.EngineCommandEngineReloadSettings:
		if engines.AgentSettingsReload == EnginesStateAgentSettingsReloadReloading || engines.AgentSettingsReload == EnginesStateAgentSettingsReloadPending {
			return SliceTransition{}
		}
		transition := SliceTransition{
			Patch:   func(next *WorkspaceState) { next.AgentSettingsReload = WorkspaceStateAgentSettingsReloadReloading },
			Effects: []WorkspaceEffect{WorkspaceEffectEngineReloadSettings{}},
		}
		if engines.AgentSettingsReload == EnginesStateAgentSettingsReloadFailed && state.ActionErrorPage != nil && *state.ActionErrorPage == domain.SettingsSectionEngines {
			transition.Error = noError
		}
		return transition
	case contracts.AgentSettingsReloadEvent:
		status := WorkspaceStateAgentSettingsReload(input.Status)
		transition := SliceTransition{Patch: func(next *WorkspaceState) { next.AgentSettingsReload = status }}
		if input.Status == contracts.AgentSettingsReloadEventStatusFailed {
			message := "Could not reload agent settings."
			if input.Message != nil {
				message = *input.Message
			}
			transition.Error = &SliceError{Message: &message, Page: new(domain.SettingsSectionEngines)}
		}
		return transition
	case EngineEventEngineStatus:
		next := engineStatusRead(state, work, input.Status)
		next.Engines.EngineChecking = false
		// Only the launch's first answer offers updates; later ones are the user's own checks, answered in Settings.
		if engines.EngineUpdatesOffered {
			return engineAccessDone(state, next, nil)
		}
		next.Engines.EngineUpdatesOffered = true
		for _, engine := range domain.AgentEngines {
			if offer := updateOffer(next, engine); offer != nil {
				next = toasted(next, offer)
			}
		}
		return engineAccessDone(state, next, nil)
	case EngineEventEngineUpdated:
		next := engineStatusRead(state, work, input.Status)
		next.Engines.EngineUpdating = nil
		return engineAccessDone(state, nextUpdate(toasted(next, updateResult(next.Engines, input.Engine))), nil)
	case EngineEventEngineFailed:
		return SliceTransition{Patch: func(next *WorkspaceState) { next.EngineChecking = false }, Refusal: &input.Message}
	case EngineEventEngineUpdateFailed:
		failed := work
		failed.Engines.EngineUpdating = nil
		shown := toasted(failed, &domain.Toast{Tone: domain.ToastToneError, Title: "Couldn't update " + domain.EngineLabel(string(input.Engine)), Message: new(input.Message), Subject: new(string(input.Engine)), Persistent: new(true)})
		return engineAccessDone(state, nextUpdate(shown), contracts.WorkspaceCommandResultFalse{Message: input.Message})
	case contracts.EngineCommandEngineRead:
		if engines.EngineChecking {
			return SliceTransition{}
		}
		// Asked on the way up, asked again whenever something is wrong, since only the user can fix that.
		if engines.EngineStatus == nil {
			return SliceTransition{
				Patch: func(next *WorkspaceState) {
					next.EngineStatus = &domain.EngineStatus{}
					next.EngineChecking = true
				},
				Effects: []WorkspaceEffect{WorkspaceEffectEngineRead{}},
			}
		}
		if (input.Refresh == nil || !*input.Refresh) && !domain.EngineNeedsAttention(engines.EngineStatus) {
			return SliceTransition{}
		}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.EngineChecking = true }, Effects: []WorkspaceEffect{WorkspaceEffectEngineRead{Refresh: new(true)}}}
	case contracts.EngineCommandEngineUpdate:
		// Only an install the app can upgrade is, and only once; one asked for while another runs waits its turn.
		engine := input.Engine
		readiness := enginesReadiness(engines, engine)
		if update := domain.AgentEngineEngineUpdate(&readiness); update == nil || update.Command == nil || sameEngine(engines.EngineUpdating, &engine) || slices.Contains(engines.EngineUpdateQueue, engine) {
			return SliceTransition{}
		}
		if engines.EngineUpdating == nil {
			return engineAccessDone(state, startUpdate(work, engine), nil)
		}
		queued := work
		queued.Engines.EngineUpdateQueue = append(slices.Clip(engines.EngineUpdateQueue), engine)
		return engineAccessDone(state, toasted(queued, &domain.Toast{
			Tone:       domain.ToastToneProgress,
			Title:      "Updating " + domain.EngineLabel(string(engine)),
			Message:    new("Waiting for " + domain.EngineLabel(string(*engines.EngineUpdating)) + " to finish."),
			Subject:    new(string(engine)),
			Persistent: new(true),
		}), nil)
	case contracts.EngineCommandEngineSignIn:
		// Only an engine that asked to be signed in to is; a ready one has nothing to open.
		if EngineAccessOf(state, input.Engine) != domain.EngineAccessSignedOut {
			return SliceTransition{}
		}
		return SliceTransition{ClearsError: true, Effects: []WorkspaceEffect{WorkspaceEffectEngineSignIn{Engine: input.Engine}}}
	}
	return SliceTransition{}
}

// engineStatusRead is the work with a fresh answer about the engines merged in. The error under the
// composer named an engine, so an answer that clears the engine clears it too.
func engineStatusRead(state *WorkspaceState, work engineWork, status domain.EngineStatus) engineWork {
	merged := domain.EngineStatus{}
	if work.Engines.EngineStatus != nil {
		merged = *work.Engines.EngineStatus
	}
	if status.Claude != nil {
		merged.Claude = status.Claude
	}
	if status.Codex != nil {
		merged.Codex = status.Codex
	}
	if status.Opencode != nil {
		merged.Opencode = status.Opencode
	}
	work.Engines.EngineStatus = &merged
	if state.ActionErrorPage != nil && *state.ActionErrorPage == domain.SettingsSectionEngines && nothingBlocked(merged) {
		work.Error = noError
	}
	return work
}

func enginesReadiness(engines EnginesState, engine domain.AgentEngine) domain.EngineReadiness {
	if readiness := engines.EngineStatus.Get(engine); readiness != nil {
		return *readiness
	}
	return domain.EngineReadiness{Access: domain.EngineAccessReady}
}

func sameEngine(a, b *domain.AgentEngine) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func writeEngines(state *WorkspaceState, engines EnginesState) {
	state.EngineStatus = engines.EngineStatus
	state.EngineChecking = engines.EngineChecking
	state.EngineUpdating = engines.EngineUpdating
	state.EngineUpdateQueue = engines.EngineUpdateQueue
	state.EngineUpdatesOffered = engines.EngineUpdatesOffered
	state.AgentSettingsReload = WorkspaceStateAgentSettingsReload(engines.AgentSettingsReload)
}

// toastDismissals is the toasts' own effects as workspace effects.
func toastDismissals(effects []*ToastEffect) []WorkspaceEffect {
	out := make([]WorkspaceEffect, 0, len(effects))
	for _, effect := range effects {
		out = append(out, WorkspaceEffectScheduleToastDismissal{ID: effect.ID, At: effect.At, FollowUp: effect.FollowUp})
	}
	return out
}
