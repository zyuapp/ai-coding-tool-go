package domain

import (
	"unsafe"
	"weak"
)

// historyChange is how a history was made: the history it was made from, held weakly, and the first
// index that differs. A history is a backing array and a length, as one array may back several.
type historyChange struct {
	length         int
	previous       weak.Pointer[*ConversationMessage]
	previousLength int
	from           int
}

// changes records, per history, the history it was made from and the first index that differs.
var changes weakMemo[*ConversationMessage, historyChange]

const maxAncestors = 256

func recordChange(next, before []*ConversationMessage, from int) {
	if len(next) == 0 {
		return
	}
	change := historyChange{length: len(next), previousLength: len(before), from: from}
	if len(before) > 0 {
		change.previous = weak.Make(unsafe.SliceData(before))
	}
	changes.set(unsafe.SliceData(next), change)
}

// AppendMessages adds committed messages while recording the unchanged prefix for incremental persistence.
func AppendMessages(before []*ConversationMessage, added []*ConversationMessage) []*ConversationMessage {
	if len(added) == 0 {
		return before
	}
	next := make([]*ConversationMessage, 0, len(before)+len(added))
	next = append(append(next, before...), added...)
	recordChange(next, before, len(before))
	return next
}

// ReplaceLastMessage replaces the committed tail, keeping every earlier message unchanged.
func ReplaceLastMessage(before []*ConversationMessage, message *ConversationMessage) []*ConversationMessage {
	if len(before) > 0 && before[len(before)-1] == message {
		return before
	}
	kept := before[:max(0, len(before)-1)]
	next := make([]*ConversationMessage, 0, len(kept)+1)
	next = append(append(next, kept...), message)
	recordChange(next, before, max(0, len(before)-1))
	return next
}

// WithdrawMessages withdraws the suffix from index from, keeping earlier history intact.
func WithdrawMessages(before []*ConversationMessage, from int) []*ConversationMessage {
	next := make([]*ConversationMessage, len(before))
	copy(next, before)
	for index := max(0, from); index < len(next); index++ {
		if message := next[index]; message.Withdrawn == nil || !*message.Withdrawn {
			withdrawn := *message
			withdrawn.Withdrawn = new(true)
			next[index] = &withdrawn
		}
	}
	recordChange(next, before, from)
	return next
}

// FirstChangedMessage is the first index of next that may differ from before. A nil before is unknown.
func FirstChangedMessage(before []*ConversationMessage, next []*ConversationMessage) int {
	if before == nil {
		return 0
	}
	if len(before) == len(next) && unsafe.SliceData(before) == unsafe.SliceData(next) {
		return len(next)
	}
	if len(before) == 0 {
		return 0
	}
	current := next
	from := len(next)
	for range maxAncestors {
		if len(current) == 0 {
			return 0
		}
		change, ok := changes.get(unsafe.SliceData(current))
		if !ok || change.length != len(current) {
			return 0
		}
		from = min(from, change.from)
		previous := change.previous.Value()
		if previous == nil {
			return 0
		}
		if previous == unsafe.SliceData(before) && change.previousLength == len(before) {
			return from
		}
		current = unsafe.Slice(previous, change.previousLength)
	}
	return 0
}
