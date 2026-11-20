package application

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func InitialComposerState() ComposerState {
	return ComposerState{
		Prompts:          map[string]string{},
		Annotations:      map[string][]*domain.Annotation{},
		Pastes:           map[string][]*domain.PastedText{},
		Images:           map[string][]*domain.StagedImage{},
		Files:            map[string][]*domain.AttachedFile{},
		ComposerFocus:    0,
		ReadingPoints:    map[string]contracts.ReadingPoint{},
		AnnotationReveal: nil,
		AttachmentSends:  map[string]*AttachmentSendState{},
	}
}

const NewThreadDraft = "draft:"

// WithPrompt is state with the composer under key holding prompt; an empty prompt clears it.
func WithPrompt(state *WorkspaceState, key, prompt string) *WorkspaceState {
	next := *state
	prompts := maps.Clone(state.Prompts)
	if prompts == nil {
		prompts = map[string]string{}
	}
	if prompt != "" {
		prompts[key] = prompt
	} else {
		delete(prompts, key)
	}
	next.Prompts = prompts
	return &next
}

func SameReadingPoint(a, b contracts.ReadingPoint) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Anchor == b.Anchor && a.Depth == b.Depth
}
