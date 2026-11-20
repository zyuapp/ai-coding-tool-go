package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// DictationInputs are the input types ReduceDictation takes.
var DictationInputs = []string{
	"dictation.start", "dictation.hands-free", "dictation.stop", "dictation.cancel", "dictation.download", "dictation.cancel-download",
	"dictation.model", "dictation.started", "dictation.transcribed", "dictation.failed",
}

// withDictation is `dictation` with `patch` made to a copy of it.
func withDictation(dictation domain.DictationState, patch func(next *domain.DictationState)) domain.DictationState {
	next := dictation
	patch(&next)
	return next
}

// noticed is what the hold has to say, on the line under the main composer that its own sends use.
// Nil clears it.
func noticed(composer ComposerEdit, message *string) ComposerEdit {
	held := NoAttachmentSend
	if send := composer.View.AttachmentSends[MainComposer]; send != nil {
		held = send
	}
	if sameOptionalString(held.Error, message) {
		return composer
	}
	next := *held
	next.Error = message
	return WithAttachmentSendIn(composer, MainComposer, &next)
}

// dictationCurrent is the recording `id` names, or nil once it is over.
func dictationCurrent(state *WorkspaceState, id string) *domain.DictationSession {
	if session := state.Dictation.Session; session != nil && session.ID == id {
		return session
	}
	return nil
}

// ReduceDictation takes an input whose type is one of DictationInputs.
func ReduceDictation(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	dictation := state.Dictation
	patched := func(change func(next *domain.DictationState)) OwnPatch {
		next := withDictation(dictation, change)
		return func(state *WorkspaceState) { state.Dictation = next }
	}
	switch input := input.(type) {
	case contracts.DictationCommandDictationStart:
		if refusal, refused := domain.DictationRefusal(&dictation); refused {
			return SliceTransition{Refusal: &refusal}
		}
		if dictation.Session != nil {
			return SliceTransition{}
		}
		session := &domain.DictationSession{ID: NewID(), Key: PromptKey(state), Phase: domain.DictationPhaseStarting, HandsFree: false, Send: false}
		return SliceTransition{
			Patch:   patched(func(next *domain.DictationState) { next.Session = session }),
			Effects: []WorkspaceEffect{WorkspaceEffectDictationStart{ID: session.ID}},
		}

	case contracts.DictationCommandDictationHandsFree:
		session := dictation.Session
		if session == nil || session.Phase == domain.DictationPhaseTranscribing || session.HandsFree {
			return SliceTransition{}
		}
		freed := *session
		freed.HandsFree = true
		patch := patched(func(next *domain.DictationState) { next.Session = &freed })
		if session.Key == PromptKey(state) {
			return SliceTransition{Patch: patch, Edits: []OwnerEdit{FocusComposerIn(ComposerOf(state))}}
		}
		return SliceTransition{Patch: patch}

	case contracts.DictationCommandDictationStop:
		session := dictation.Session
		if session == nil || session.Phase == domain.DictationPhaseTranscribing {
			return SliceTransition{}
		}
		stopped := *session
		stopped.Phase = domain.DictationPhaseTranscribing
		stopped.Send = input.Send != nil && *input.Send
		return SliceTransition{
			Patch:   patched(func(next *domain.DictationState) { next.Session = &stopped }),
			Effects: []WorkspaceEffect{WorkspaceEffectDictationStop{ID: session.ID}},
		}

	case contracts.DictationCommandDictationCancel:
		session := dictation.Session
		if session == nil {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch:   patched(func(next *domain.DictationState) { next.Session = nil }),
			Effects: []WorkspaceEffect{WorkspaceEffectDictationCancel{ID: session.ID}},
		}

	case contracts.DictationCommandDictationDownload:
		if dictation.Model != domain.DictationModelMissing && dictation.Model != domain.DictationModelFailed {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch: patched(func(next *domain.DictationState) {
				next.Model, next.Message, next.Progress = domain.DictationModelDownloading, nil, new(0.0)
			}),
			Effects: []WorkspaceEffect{WorkspaceEffectDictationDownload{}},
		}

	case contracts.DictationCommandDictationCancelDownload:
		if dictation.Model != domain.DictationModelDownloading {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch: patched(func(next *domain.DictationState) {
				next.Model, next.Message, next.Progress = domain.DictationModelMissing, nil, nil
			}),
			Effects: []WorkspaceEffect{WorkspaceEffectDictationCancelDownload{}},
		}

	case DictationEventDictationModel:
		var progress *float64
		if input.Model == domain.DictationModelDownloading {
			progress = input.Progress
		}
		return SliceTransition{Patch: patched(func(next *domain.DictationState) {
			next.Model, next.Message, next.Progress = input.Model, input.Message, progress
		})}

	case DictationEventDictationStarted:
		session := dictationCurrent(state, input.ID)
		if session == nil || session.Phase != domain.DictationPhaseStarting {
			return SliceTransition{}
		}
		recording := *session
		recording.Phase = domain.DictationPhaseRecording
		return SliceTransition{Patch: patched(func(next *domain.DictationState) { next.Session = &recording })}

	case DictationEventDictationFailed:
		if dictationCurrent(state, input.ID) == nil {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch: patched(func(next *domain.DictationState) { next.Session = nil }),
			Edits: []OwnerEdit{noticed(ComposerOf(state), new(input.Message))},
		}

	// The words join the draft the recording began on, to be read over and sent like typing. A draft
	// still on screen takes the caret, so Enter sends it, or is sent when the recording was stopped to send.
	case DictationEventDictationTranscribed:
		session := dictationCurrent(state, input.ID)
		if session == nil {
			return SliceTransition{}
		}
		ended := withDictation(dictation, func(next *domain.DictationState) { next.Session = nil })
		endedPatch := func(state *WorkspaceState) { state.Dictation = ended }
		if runsJSTrim(input.Text) == "" {
			return SliceTransition{Patch: endedPatch, Edits: []OwnerEdit{noticed(ComposerOf(state), new(domain.DictationHeardNothing))}}
		}
		written := noticed(WithPromptIn(ComposerOf(state), session.Key, domain.WithSpokenWords(state.Prompts[session.Key], input.Text)), nil)
		if session.Key != PromptKey(state) {
			return SliceTransition{Patch: endedPatch, Edits: []OwnerEdit{written}}
		}
		if session.Send {
			sent := withDictation(ended, func(next *domain.DictationState) { next.Sends = ended.Sends + 1 })
			return SliceTransition{Patch: func(state *WorkspaceState) { state.Dictation = sent }, Edits: []OwnerEdit{written}}
		}
		return SliceTransition{Patch: endedPatch, Edits: []OwnerEdit{FocusComposerIn(written)}}
	}
	return SliceTransition{}
}
