package application

import (
	"fmt"
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The images a composer sends: what has to be on disk first, and what stopped them getting there.

const movedDraftError = "The images were not sent because you left the new thread. Send them again from there."

var AttachmentInputs = []string{"attachments.send", "attachments.saved", "attachments.failed", "attachments.notice"}

// The composers' sends with the one of `taskID`'s composer (nil: the main composer) replaced.
func withSend(state *WorkspaceState, taskID *string, send *AttachmentSendState) map[string]*AttachmentSendState {
	sends := maps.Clone(state.AttachmentSends)
	if sends == nil {
		sends = map[string]*AttachmentSendState{}
	}
	key := MainComposer
	if taskID != nil {
		key = *taskID
	}
	sends[key] = send
	return sends
}

// A marked image leaves as a new file, so its draft follows that file until the run that carries it
// starts. Answers the composers' images.
func withWrittenImages(state *WorkspaceState, input AttachmentEventAttachmentsSaved) map[string][]*domain.StagedImage {
	var key string
	switch draftKey := AttachmentSendFor(state.AttachmentSends, nil).DraftKey; {
	case input.TaskID != nil:
		key = *input.TaskID
	case draftKey != nil:
		key = *draftKey
	default:
		key = PromptKey(state)
	}
	held := ImagesFor(state, key)
	images := make([]*domain.StagedImage, len(held))
	for index, image := range held {
		if at := slices.Index(input.IDs, image.ID); at >= 0 && at < len(input.Attachments) {
			moved := *image
			moved.Path = input.Attachments[at].Path
			image = &moved
		}
		images[index] = image
	}
	return withDrafts(state.Images, key, images)
}

// The images' send, for the composer of `taskID` (nil: the main composer). Words that came with
// them, from a phone, go with them; otherwise the composer's own are sent. A false steer and an
// empty text are left out, as TS leaves out falsy ones.
func composerAttachmentsSent(state *WorkspaceState, taskID *string, attachments []*domain.RunAttachment, steer bool, text string) SliceTransition {
	send := contracts.TaskCommandTaskSend{TaskID: taskID, Attachments: attachments}
	if send.Attachments == nil {
		send.Attachments = []*domain.RunAttachment{}
	}
	if steer {
		send.Steer = &steer
	}
	if text != "" {
		send.Text = &text
	}
	return SliceTransition(ReduceSending(state, send, CoordinationSend{}, SendSaid{}))
}

// `input` is one of the AttachmentInputs.
func ReduceComposerAttachments(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	failed := func(message string) contracts.WorkspaceCommandResult {
		return contracts.WorkspaceCommandResultFalse{Message: message}
	}
	sendsPatch := func(sends map[string]*AttachmentSendState) OwnPatch {
		return func(next *WorkspaceState) { next.AttachmentSends = sends }
	}
	switch input := input.(type) {
	case contracts.TaskCommandAttachmentsSend:
		send := AttachmentSendFor(state.AttachmentSends, input.TaskID)
		// One send carries the whole strip, so a second cannot start until the first has written and started.
		key := PromptKey(state)
		if input.TaskID != nil {
			key = *input.TaskID
		}
		if send.Busy {
			return SliceTransition{}
		}
		for _, pending := range state.PendingRuns {
			if pending.DraftKey != nil && *pending.DraftKey == key {
				return SliceTransition{}
			}
		}
		// Pasting and grabbing fill the same strip from different sides, so the total is checked once here.
		if len(input.Attachments) > domain.MaxAttachments {
			message := fmt.Sprintf("You can attach up to %d images.", domain.MaxAttachments)
			refused := *send
			refused.Error = &message
			return SliceTransition{Patch: sendsPatch(withSend(state, input.TaskID, &refused)), Result: failed(message)}
		}
		steer := input.Steer != nil && *input.Steer
		text := ""
		if input.Text != nil {
			text = *input.Text
		}
		if len(input.Attachments) == 0 {
			return composerAttachmentsSent(state, input.TaskID, []*domain.RunAttachment{}, steer, text)
		}
		busy := &AttachmentSendState{Busy: true, Error: nil, Sent: send.Sent}
		if busy.Sent == nil {
			busy.Sent = []string{}
		}
		if input.TaskID == nil && input.Text == nil {
			busy.DraftKey = &key
		}
		effect := WorkspaceEffectSendAttachments{TaskID: input.TaskID, Attachments: input.Attachments}
		if steer {
			effect.Steer = &steer
		}
		if text != "" {
			effect.Text = &text
		}
		return SliceTransition{Patch: sendsPatch(withSend(state, input.TaskID, busy)), Effects: []WorkspaceEffect{effect}}

	case AttachmentEventAttachmentsSaved:
		send := AttachmentSendFor(state.AttachmentSends, input.TaskID)
		// The main composer moved on while they were written, so they go to the thread they left from.
		var left *string
		if send.DraftKey != nil && *send.DraftKey != PromptKey(state) {
			left = send.DraftKey
		}
		if left != nil && threadsFind(state.Threads, *left) == nil {
			message := movedDraftError
			return SliceTransition{
				Patch:  sendsPatch(withSend(state, input.TaskID, &AttachmentSendState{Busy: false, Error: &message, Sent: []string{}})),
				Result: failed(message),
			}
		}
		images := withWrittenImages(state, input)
		sentIDs := input.IDs
		if sentIDs == nil {
			sentIDs = []string{}
		}
		sends := withSend(state, input.TaskID, &AttachmentSendState{Busy: false, Error: nil, Sent: sentIDs})
		written := func(next *WorkspaceState) {
			next.Images = images
			next.AttachmentSends = sends
		}
		taskID := input.TaskID
		if left != nil {
			taskID = left
		}
		steer := input.Steer != nil && *input.Steer
		text := ""
		if input.Text != nil {
			text = *input.Text
		}
		transition := composerAttachmentsSent(Afterward(state, SliceTransition{Patch: written}), taskID, input.Attachments, steer, text)
		transition.Patch = written
		return transition

	case AttachmentEventAttachmentsFailed:
		send := AttachmentSendFor(state.AttachmentSends, input.TaskID)
		message := input.Message
		return SliceTransition{
			Patch:  sendsPatch(withSend(state, input.TaskID, &AttachmentSendState{Busy: false, Error: &message, Sent: send.Sent})),
			Result: failed(message),
		}

	case contracts.TaskCommandAttachmentsNotice:
		send := AttachmentSendFor(state.AttachmentSends, input.TaskID)
		if threadsSameString(send.Error, input.Message) {
			return SliceTransition{}
		}
		noticed := *send
		noticed.Error = input.Message
		return SliceTransition{Patch: sendsPatch(withSend(state, input.TaskID, &noticed))}
	}
	return SliceTransition{}
}
