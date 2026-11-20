package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// IsRemoteSender checks a value decoded from JSON.
func IsRemoteSender(value any) bool {
	sender, ok := value.(map[string]any)
	if !ok || sender == nil {
		return false
	}
	threadID, ok := sender["threadId"].(string)
	_, titled := sender["title"].(string)
	_, placed := sender["computer"].(string)
	return ok && threadID != "" && titled && placed
}

const from = "From "

const on = " on "

const separator = " · "

const coordination = "Thread updates"

var messageOriginAutomation = regexp.MustCompile(`^Automation run #(\d+)$`)

// OriginLabel is what a message's origin reads as.
func OriginLabel(origin MessageOrigin) string {
	switch origin := origin.(type) {
	case MessageOriginThread:
		label := from + threadName(origin)
		if origin.ThreadID != nil && *origin.ThreadID != "" {
			label += separator + *origin.ThreadID
		}
		return label
	case MessageOriginAutomation:
		return "Automation run #" + jsNumber(origin.RunNumber)
	case MessageOriginCoordination:
		return coordination
	case MessageOriginLegacy:
		return origin.Label
	}
	return ""
}

// SenderLabel is the short name a folded row gives the sender; false for none. A nil origin is none.
func SenderLabel(origin MessageOrigin) (string, bool) {
	switch origin := origin.(type) {
	case MessageOriginThread:
		return from + threadName(origin), true
	case MessageOriginCoordination:
		return coordination, true
	}
	return "", false
}

func threadName(origin MessageOriginThread) string {
	if origin.Computer != nil && *origin.Computer != "" {
		return origin.Title + on + *origin.Computer
	}
	return origin.Title
}

// OriginFields is the origin a message carries with the label that travels beside it; both nil for a nil origin.
func OriginFields(origin MessageOrigin) (MessageOrigin, *string) {
	if origin == nil {
		return nil, nil
	}
	label := OriginLabel(origin)
	return origin, &label
}

// MessageLabel is the label shown above a message: its origin's when it has one, else detail.
func MessageLabel(origin MessageOrigin, detail *string) (string, bool) {
	if origin != nil {
		return OriginLabel(origin), true
	}
	if detail != nil {
		return *detail, true
	}
	return "", false
}

// IsUsersOwn is whether a queued message, by its origin, detail and answers fields, is the user's own words.
func IsUsersOwn(origin MessageOrigin, detail *string, answers *string) bool {
	return origin == nil && (detail == nil || *detail == "") && answers == nil
}

// LegacyOrigin is where a message an older build labelled came from, read from the label it wrote.
func LegacyOrigin(detail string) MessageOrigin {
	if detail == coordination {
		return MessageOriginCoordination{}
	}
	if run := messageOriginAutomation.FindStringSubmatch(detail); run != nil {
		number, _ := strconv.ParseFloat(run[1], 64)
		return MessageOriginAutomation{RunNumber: number}
	}
	if sender, ok := strings.CutPrefix(detail, from); ok {
		end := strings.LastIndex(sender, separator)
		threadID := ""
		if end >= 0 {
			threadID = sender[end+len(separator):]
		}
		if threadID != "" && !strings.ContainsFunc(threadID, isJSSpace) {
			return MessageOriginThread{ThreadID: &threadID, Title: sender[:end]}
		}
		return MessageOriginThread{Title: sender}
	}
	return MessageOriginLegacy{Label: detail}
}

// WithLegacyOrigin gives a user message stored with only a label the origin that label names; a copy when it changes.
func WithLegacyOrigin(message *ConversationMessage) *ConversationMessage {
	if message.Origin != nil || message.Detail == nil || message.Kind != ConversationMessageKindUser {
		return message
	}
	next := *message
	next.Origin = LegacyOrigin(*message.Detail)
	return &next
}
