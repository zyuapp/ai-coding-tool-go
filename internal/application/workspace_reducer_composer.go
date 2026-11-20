package application

import (
	"maps"
	"math"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The composer draft: its text, and everything staged alongside it.

var ComposerInputs = []string{
	"annotation.add", "annotation.note", "annotation.remove", "annotation.recall", "annotation.reveal", "paste.add", "paste.remove", "paste.recall", "image.add",
	"image.remove", "image.recall", "file.attach", "file.detach", "file.recall", "view.set-prompt", "view.reading-point",
}

// The composers as `next` holds them, named only when they changed.
func composerChanged(state, next *WorkspaceState) SliceTransition {
	composer := ComposerOf(next)
	if !differs(composer, state) {
		return SliceTransition{}
	}
	return SliceTransition{Patch: func(target *WorkspaceState) { writeView(target, composer.View) }}
}

// Where the caret lands once a draft changes. A fresh annotation is written to be sent, so it hands
// the caret to the composer that will carry it. A comment on the diff keeps it, because the review
// is worked down a row at a time and each comment would otherwise cost the reviewer their place.
func focusAfter(state, drafted *WorkspaceState, input ComposerDraftCommand, key string) SliceTransition {
	add, ok := input.(contracts.AnnotationCommandAnnotationAdd)
	if !ok {
		return composerChanged(state, drafted)
	}
	if _, onDiff := add.Anchor.(domain.AnnotationAnchorDiff); onDiff {
		return composerChanged(state, drafted)
	}
	if holder, held := TabHolderOf(drafted, key); held && holder != "" {
		transition := composerChanged(state, drafted)
		transition.Edits = []OwnerEdit{FocusedTabIn(DocksOf(drafted), holder, key)}
		return transition
	}
	return composerChanged(state, FocusComposer(drafted))
}

// Every change to one composer's drafts. `key` is the composer: a thread's id, or the draft's.
func ComposerDraft(state *WorkspaceState, input ComposerDraftCommand, key string) SliceTransition {
	drafted, refusal := DraftedComposer(state, input, key)
	if refusal != "" {
		transition := composerChanged(state, drafted)
		transition.Refusal = &refusal
		return transition
	}
	if drafted == state {
		return SliceTransition{}
	}
	return focusAfter(state, drafted, input, key)
}

// `input` is one of the ComposerInputs.
func ReduceComposer(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	keyOf := func(taskID *string) string {
		if taskID != nil {
			return *taskID
		}
		return PromptKey(state)
	}
	switch input := input.(type) {
	case contracts.AnnotationCommandAnnotationAdd:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.AnnotationCommandAnnotationNote:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.AnnotationCommandAnnotationRemove:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.AnnotationCommandAnnotationRecall:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.PasteCommandPasteAdd:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.PasteCommandPasteRemove:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.PasteCommandPasteRecall:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.ImageCommandImageAdd:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.ImageCommandImageRemove:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.ImageCommandImageRecall:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.FileCommandFileAttach:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.FileCommandFileDetach:
		return ComposerDraft(state, input, keyOf(input.TaskID))
	case contracts.FileCommandFileRecall:
		return ComposerDraft(state, input, keyOf(input.TaskID))

	case contracts.AnnotationCommandAnnotationReveal:
		return composerChanged(state, RevealAnnotation(state, keyOf(input.TaskID), input.AnnotationID))

	case contracts.ViewCommandViewSetPrompt:
		prompts := composerPrompts(state.Prompts, keyOf(input.TaskID), input.Prompt)
		return SliceTransition{Patch: func(next *WorkspaceState) { next.Prompts = prompts }}

	// A place whose row the thread no longer has is dropped when it is next read, not here.
	case contracts.ViewCommandViewReadingPoint:
		point := input.Point
		wellFormed := point == nil || point.Anchor != "" && !math.IsInf(point.Depth, 0) && !math.IsNaN(point.Depth)
		if !wellFormed || threadsFind(state.Threads, input.TaskID) == nil {
			return SliceTransition{}
		}
		if SameReadingPoint(state.ReadingPoints[input.TaskID], point) {
			return SliceTransition{}
		}
		points := maps.Clone(state.ReadingPoints)
		if points == nil {
			points = map[string]contracts.ReadingPoint{}
		}
		points[input.TaskID] = point
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ReadingPoints = points }}
	}
	return SliceTransition{}
}
