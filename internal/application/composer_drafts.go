package application

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What a composer holds before a send: highlights taken from the transcript, blocks pasted in,
// images grabbed or dropped, and files named by where they sit. Each is drafted per thread and
// cleared by the send that carries it, so this is the one place their shapes and limits live.

// The composers as one input leaves them. Only the functions here make one, so a slice changes a composer through them.
type composerChange = ComposerEdit

func ComposerOf(state *WorkspaceState) ComposerEdit {
	return ComposerEdit{View: ComposerState{
		Prompts: state.Prompts, Annotations: state.Annotations, Pastes: state.Pastes, Images: state.Images, Files: state.Files,
		ComposerFocus: state.ComposerFocus, ReadingPoints: state.ReadingPoints, AnnotationReveal: state.AnnotationReveal,
		AttachmentSends: state.AttachmentSends,
	}}
}

// Threads gone for good take what was drafted in them, and where their reader was, with them.
func DraftsForgottenIn(edit ComposerEdit, removed jsonx.Set[string]) ComposerEdit {
	view := edit.View
	view.Prompts = WithoutThreadKeys(view.Prompts, removed)
	view.Annotations = WithoutThreadKeys(view.Annotations, removed)
	view.Pastes = WithoutThreadKeys(view.Pastes, removed)
	view.Images = WithoutThreadKeys(view.Images, removed)
	view.ReadingPoints = WithoutThreadKeys(view.ReadingPoints, removed)
	return ComposerEdit{View: view}
}

// What a paired computer has taken is let go of here; what was typed after it went stays.
func SentElsewhereIn(edit ComposerEdit, draft *SentDraft) ComposerEdit {
	key := draft.Key
	next := edit.View
	kept := TextAfterSend(next.Prompts[key], draft.Prompt)
	next.Prompts = composerPrompts(next.Prompts, key, kept)
	var annotations []*domain.Annotation
	for _, annotation := range next.Annotations[key] {
		if !slices.ContainsFunc(draft.Annotations, func(sent *domain.Annotation) bool { return sent.ID == annotation.ID && sent.Note == annotation.Note }) {
			annotations = append(annotations, annotation)
		}
	}
	var pastes []*domain.PastedText
	for _, paste := range next.Pastes[key] {
		if !slices.ContainsFunc(draft.Pastes, func(sent *domain.PastedText) bool { return sent.ID == paste.ID }) {
			pastes = append(pastes, paste)
		}
	}
	next.Annotations = withDrafts(next.Annotations, key, annotations)
	next.Pastes = withDrafts(next.Pastes, key, pastes)
	if draft.Attachments != nil {
		composer, ids := draft.Attachments.Key, draft.Attachments.IDs
		var images []*domain.StagedImage
		for _, image := range next.Images[key] {
			if !slices.Contains(ids, image.ID) {
				images = append(images, image)
			}
		}
		next.Images = withDrafts(next.Images, key, images)
		sends := maps.Clone(next.AttachmentSends)
		if sends == nil {
			sends = map[string]*AttachmentSendState{}
		}
		sent := ids
		if sent == nil {
			sent = []string{}
		}
		sends[composer] = &AttachmentSendState{Busy: false, Error: nil, Sent: sent}
		next.AttachmentSends = sends
	}
	return ComposerEdit{View: next}
}

func WithPromptIn(edit ComposerEdit, key, prompt string) ComposerEdit {
	view := edit.View
	view.Prompts = composerPrompts(view.Prompts, key, prompt)
	return ComposerEdit{View: view}
}

// The prompts with the composer under key holding prompt; an empty prompt clears it. Always a new map.
func composerPrompts(prompts map[string]string, key, prompt string) map[string]string {
	next := maps.Clone(prompts)
	if next == nil {
		next = map[string]string{}
	}
	if prompt != "" {
		next[key] = prompt
	} else {
		delete(next, key)
	}
	return next
}

func FocusComposerIn(edit ComposerEdit) ComposerEdit {
	view := edit.View
	view.ComposerFocus++
	return ComposerEdit{View: view}
}

// ClearedDraft: a composer that has just sent, with nothing of what it carried left.
func ClearedDraftIn(edit ComposerEdit, key string) ComposerEdit {
	return ComposerEdit{View: ClearedDraft(edit.View, key)}
}

// A composer that has just sent: text, annotations, pastes, images, and attached files all go.
func ClearedDraft(state ComposerState, key string) ComposerState {
	state.Prompts = composerPrompts(state.Prompts, key, "")
	state.Annotations = withDrafts[*domain.Annotation](state.Annotations, key, nil)
	state.Pastes = withDrafts[*domain.PastedText](state.Pastes, key, nil)
	state.Images = withDrafts[*domain.StagedImage](state.Images, key, nil)
	state.Files = withDrafts[*domain.AttachedFile](state.Files, key, nil)
	return state
}

// Messages put back in a thread's composer, ahead of whatever is drafted there, with everything
// they carry. Their images come back as images the app already holds.
func HandedBackIn(edit ComposerEdit, key string, messages []*domain.HeldMessage) ComposerEdit {
	state := edit.View
	texts := make([]string, 0, len(messages)+1)
	var annotations []*domain.Annotation
	var pastes []*domain.PastedText
	var files []*domain.AttachedFile
	var images []string
	for _, message := range messages {
		texts = append(texts, message.Text)
		annotations = append(annotations, message.Annotations...)
		pastes = append(pastes, message.Pastes...)
		files = append(files, message.Files...)
		images = append(images, message.Attachments...)
	}
	texts = append(texts, state.Prompts[key])
	annotations = append(annotations, state.Annotations[key]...)
	pastes = append(pastes, state.Pastes[key]...)
	files = append(files, state.Files[key]...)
	handed := state
	handed.Prompts = composerPrompts(state.Prompts, key, threadsJoinNonEmpty(texts, "\n\n"))
	handed.Annotations = withDrafts(state.Annotations, key, annotations)
	handed.Pastes = withDrafts(state.Pastes, key, pastes)
	handed.Files = withDrafts(state.Files, key, files)
	if len(images) > 0 {
		for _, image := range state.Images[key] {
			images = append(images, image.Path)
		}
		staged := make([]*domain.StagedImage, len(images))
		for index, path := range images {
			staged[index] = stagedImage(path)
		}
		handed.Images = withDrafts(state.Images, key, staged)
	}
	return ComposerEdit{View: handed}
}

func WithAttachmentSendIn(edit ComposerEdit, key string, send *AttachmentSendState) ComposerEdit {
	view := edit.View
	sends := maps.Clone(view.AttachmentSends)
	if sends == nil {
		sends = map[string]*AttachmentSendState{}
	}
	sends[key] = send
	view.AttachmentSends = sends
	return ComposerEdit{View: view}
}

var tooManyImagesError = fmt.Sprintf("You can attach up to %d images.", domain.MaxAttachments)

var tooManyFilesError = fmt.Sprintf("You can attach up to %d files.", domain.MaxAttachedFiles)

// A composer's drafts of one kind; nil when nothing is drafted (TS shares one empty array, NO_DRAFTS).
func draftsFor[T any](held map[string][]T, key string) []T {
	return held[key]
}

// One composer's drafts of one kind, with the whole entry dropped once nothing is left in it.
func withDrafts[T any](held map[string][]T, key string, drafted []T) map[string][]T {
	next := maps.Clone(held)
	if next == nil {
		next = map[string][]T{}
	}
	if len(drafted) > 0 {
		next[key] = drafted
	} else {
		delete(next, key)
	}
	return next
}

func AnnotationsFor(state *WorkspaceState, key string) []*domain.Annotation {
	return draftsFor(state.Annotations, key)
}

func WithAnnotations(state *WorkspaceState, key string, annotations []*domain.Annotation) *WorkspaceState {
	next := *state
	next.Annotations = withDrafts(state.Annotations, key, annotations)
	return &next
}

// The reveal meant for the transcript a composer drafts into, so no other transcript scrolls to it.
func AnnotationRevealFor(state *WorkspaceState, key string) *AnnotationReveal {
	if state.AnnotationReveal != nil && state.AnnotationReveal.TaskID == key {
		return state.AnnotationReveal
	}
	return nil
}

// Asks the transcript that drafted an annotation to show its passage. One with no passage there is left alone.
func RevealAnnotation(state *WorkspaceState, key, annotationID string) *WorkspaceState {
	index := slices.IndexFunc(AnnotationsFor(state, key), func(item *domain.Annotation) bool { return item.ID == annotationID })
	if index < 0 {
		return state
	}
	anchor, ok := AnnotationsFor(state, key)[index].Anchor.(domain.AnnotationAnchorMessage)
	if !ok {
		return state
	}
	count := 1.0
	if state.AnnotationReveal != nil {
		count = state.AnnotationReveal.Count + 1
	}
	next := *state
	next.AnnotationReveal = &AnnotationReveal{TaskID: key, AnnotationID: annotationID, MessageID: anchor.MessageID, Count: count}
	return &next
}

func PastesFor(state *WorkspaceState, key string) []*domain.PastedText {
	return draftsFor(state.Pastes, key)
}

func WithPastes(state *WorkspaceState, key string, pastes []*domain.PastedText) *WorkspaceState {
	next := *state
	next.Pastes = withDrafts(state.Pastes, key, pastes)
	return &next
}

func ImagesFor(state *WorkspaceState, key string) []*domain.StagedImage {
	return draftsFor(state.Images, key)
}

func WithImages(state *WorkspaceState, key string, images []*domain.StagedImage) *WorkspaceState {
	next := *state
	next.Images = withDrafts(state.Images, key, images)
	return &next
}

func FilesFor(state *WorkspaceState, key string) []*domain.AttachedFile {
	return draftsFor(state.Files, key)
}

func WithFiles(state *WorkspaceState, key string, files []*domain.AttachedFile) *WorkspaceState {
	next := *state
	next.Files = withDrafts(state.Files, key, files)
	return &next
}

// What a composer keeps once `sent` has gone: whatever was typed after it and whatever was handed
// back ahead of it meanwhile, or all of it if it was rewritten.
func TextAfterSend(typed, sent string) string {
	if typed == sent {
		return ""
	}
	if strings.HasPrefix(typed, sent) {
		return threadsTrimStart(typed[len(sent):])
	}
	at := strings.LastIndex(typed, "\n\n"+sent)
	if at < 0 {
		return typed
	}
	return threadsJoinNonEmpty([]string{typed[:at], threadsTrimStart(typed[at+2+len(sent):])}, "\n\n")
}

// The caret goes back to the composer, which is where anything arriving in a draft is spoken about.
func FocusComposer(state *WorkspaceState) *WorkspaceState {
	next := *state
	next.ComposerFocus++
	return &next
}

// An image the app already holds, put back in a composer. What it is of is lost with the send.
func stagedImage(path string) *domain.StagedImage {
	return &domain.StagedImage{ID: NewID(), Path: path, Label: ""}
}

// Files dropped or pasted at once, less the ones this composer already holds and any repeat.
func freshFiles(held []*domain.AttachedFile, arriving []*domain.AttachedFileDraft) []*domain.AttachedFileDraft {
	var fresh []*domain.AttachedFileDraft
	for index, file := range arriving {
		if file.Path == "" || file.Name == "" {
			continue
		}
		if slices.ContainsFunc(held, func(item *domain.AttachedFile) bool { return item.Path == file.Path }) {
			continue
		}
		if slices.IndexFunc(arriving, func(item *domain.AttachedFileDraft) bool { return item.Path == file.Path }) != index {
			continue
		}
		fresh = append(fresh, file)
	}
	return fresh
}

// Every change to one composer's drafts, or why the change is refused. `key` is the composer: a
// thread's id, or the draft's. A refusal answers a non-empty error beside the state it leaves.
func DraftedComposer(state *WorkspaceState, input ComposerDraftCommand, key string) (*WorkspaceState, string) {
	switch input := input.(type) {
	case contracts.AnnotationCommandAnnotationAdd:
		quote := ClampQuote(input.Quote)
		if quote == "" {
			return state, ""
		}
		note := ""
		if input.Note != nil {
			note = *input.Note
		}
		annotation := &domain.Annotation{ID: NewID(), Quote: quote, Note: note, Anchor: input.Anchor}
		return WithAnnotations(state, key, append(slices.Clip(AnnotationsFor(state, key)), annotation)), ""

	case contracts.AnnotationCommandAnnotationNote:
		held := AnnotationsFor(state, key)
		annotations := make([]*domain.Annotation, len(held))
		for index, item := range held {
			if item.ID == input.AnnotationID {
				noted := *item
				noted.Note = input.Note
				item = &noted
			}
			annotations[index] = item
		}
		return WithAnnotations(state, key, annotations), ""

	case contracts.AnnotationCommandAnnotationRemove:
		return WithAnnotations(state, key, slices.DeleteFunc(slices.Clone(AnnotationsFor(state, key)), func(item *domain.Annotation) bool { return item.ID == input.AnnotationID })), ""

	case contracts.AnnotationCommandAnnotationRecall:
		return WithAnnotations(state, key, input.Annotations), ""

	case contracts.PasteCommandPasteAdd:
		if input.Text == "" {
			return state, ""
		}
		return WithPastes(state, key, append(slices.Clip(PastesFor(state, key)), &domain.PastedText{ID: NewID(), Text: input.Text})), ""

	case contracts.PasteCommandPasteRemove:
		return WithPastes(state, key, slices.DeleteFunc(slices.Clone(PastesFor(state, key)), func(item *domain.PastedText) bool { return item.ID == input.PasteID })), ""

	case contracts.PasteCommandPasteRecall:
		return WithPastes(state, key, input.Pastes), ""

	case contracts.ImageCommandImageAdd:
		held := ImagesFor(state, key)
		if input.Path == "" {
			return state, ""
		}
		// The same file dropped twice is the same one image, however many copies of it the app holds.
		if input.Source != nil && *input.Source != "" && slices.ContainsFunc(held, func(image *domain.StagedImage) bool {
			return image.Source != nil && *image.Source == *input.Source
		}) {
			return state, ""
		}
		if len(held) >= domain.MaxAttachments {
			return state, tooManyImagesError
		}
		image := &domain.StagedImage{ID: NewID(), Path: input.Path, Label: input.Label}
		if input.Source != nil && *input.Source != "" {
			image.Source = input.Source
		}
		// An image only ever arrives to be captioned, so the caret goes where the caption is typed.
		return FocusComposer(WithImages(state, key, append(slices.Clip(held), image))), ""

	case contracts.ImageCommandImageRemove:
		return WithImages(state, key, slices.DeleteFunc(slices.Clone(ImagesFor(state, key)), func(item *domain.StagedImage) bool { return item.ID == input.ImageID })), ""

	case contracts.ImageCommandImageRecall:
		images := make([]*domain.StagedImage, len(input.Paths))
		for index, path := range input.Paths {
			images[index] = stagedImage(path)
		}
		return WithImages(state, key, images), ""

	case contracts.FileCommandFileAttach:
		held := FilesFor(state, key)
		fresh := freshFiles(held, input.Files)
		if len(fresh) == 0 {
			return state, ""
		}
		room := domain.MaxAttachedFiles - len(held)
		if room <= 0 {
			return state, tooManyFilesError
		}
		files := slices.Clip(held)
		for _, file := range fresh[:min(room, len(fresh))] {
			files = append(files, &domain.AttachedFile{ID: NewID(), Path: file.Path, Name: file.Name, Folder: file.Folder})
		}
		attached := WithFiles(state, key, files)
		if len(fresh) > room {
			return attached, tooManyFilesError
		}
		// A file only ever arrives to be asked about, so the caret goes where the question is typed.
		return FocusComposer(attached), ""

	case contracts.FileCommandFileDetach:
		return WithFiles(state, key, slices.DeleteFunc(slices.Clone(FilesFor(state, key)), func(item *domain.AttachedFile) bool { return item.ID == input.FileID })), ""

	case contracts.FileCommandFileRecall:
		return WithFiles(state, key, input.Files), ""
	}
	return state, ""
}
