package domain

import (
	"crypto/rand"
	"fmt"
	"time"
	"unsafe"
)

// Now is the domain's clock in ms since the epoch. Tests pin it; application's clock should read it.
var Now = func() float64 { return float64(time.Now().UnixMilli()) }

// NewID is the domain's id source: a random version 4 UUID, as crypto.randomUUID makes. Tests pin it;
// application's should read it.
var NewID = func() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

const MaxAttachments = 6

const MaxAttachmentBytes = 25 * 1024 * 1024

const MaxAttachmentEncodedBytes = (MaxAttachmentBytes + 2) / 3 * 4

const MaxAttachedFiles = 10

// CreateConversationMessage stamps a new message with NewID and Now. A nil detail is none; empty lists are left off.
func CreateConversationMessage(kind ConversationMessageKind, text string, detail *string, attachments []string, annotations []*Annotation, pastes []*PastedText, files []*AttachedFile) *ConversationMessage {
	message := &ConversationMessage{ID: NewID(), Kind: kind, Text: text, Detail: detail}
	if len(attachments) > 0 {
		message.Attachments = attachments
	}
	if len(annotations) > 0 {
		message.Annotations = annotations
	}
	if len(pastes) > 0 {
		message.Pastes = pastes
	}
	if len(files) > 0 {
		message.Files = files
	}
	message.At = Now()
	return message
}

// SentMessage is what createSentMessage takes. A nil Origin is the user's own words.
type SentMessage struct {
	Text        string
	Origin      MessageOrigin
	Attachments []string
	Annotations []*Annotation
	Pastes      []*PastedText
	Files       []*AttachedFile
}

// CreateSentMessage is a user message as sent: typed by the user, or by whoever its origin names.
func CreateSentMessage(sent SentMessage) *ConversationMessage {
	origin, detail := OriginFields(sent.Origin)
	message := CreateConversationMessage(ConversationMessageKindUser, sent.Text, detail, sent.Attachments, sent.Annotations, sent.Pastes, sent.Files)
	message.Origin = origin
	return message
}

func CreateFailureMessage(text string) *ConversationMessage {
	message := CreateConversationMessage(ConversationMessageKindSystem, text, nil, nil, nil, nil, nil)
	message.Tone = new("error")
	return message
}

// sentPromptsOf is the prompts of a history of length messages, as one backing array may hold several.
type sentPromptsOf struct {
	length  int
	prompts []*RecalledMessage
}

// sentPromptCache memoizes SentPrompts by the identity of the messages slice.
var sentPromptCache weakMemo[*ConversationMessage, sentPromptsOf]

// orEmpty is list ?? [].
func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// SentPrompts is the prompts the user sent themselves, oldest first. The result is shared: never change it.
func SentPrompts(messages []*ConversationMessage) []*RecalledMessage {
	if len(messages) == 0 {
		return []*RecalledMessage{}
	}
	key := unsafe.SliceData(messages)
	if cached, ok := sentPromptCache.get(key); ok && cached.length == len(messages) {
		return cached.prompts
	}
	prompts := []*RecalledMessage{}
	for _, message := range messages {
		if message.Kind == ConversationMessageKindUser && message.Origin == nil {
			prompts = append(prompts, &RecalledMessage{Text: message.Text, Annotations: orEmpty(message.Annotations), Pastes: orEmpty(message.Pastes), Files: orEmpty(message.Files), Attachments: orEmpty(message.Attachments)})
		}
	}
	sentPromptCache.set(key, sentPromptsOf{length: len(messages), prompts: prompts})
	return prompts
}
