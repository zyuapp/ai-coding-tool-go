package store

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// Stand-ins for the contracts and domain types the store reads and writes (contracts/task-store.ts,
// domain/thread-storage.ts). Each record is the JSON the TypeScript build stores, carried as its
// bytes so a row read back is written back unchanged.

// TaskStoreDelta is contracts TaskStoreDelta. Nil Projects or Worktrees leave those rows alone;
// a non-nil empty list clears them.
type TaskStoreDelta struct {
	Tasks        []TaskChange     `json:"tasks"`
	RemovedTasks []string         `json:"removedTasks,omitzero"`
	Projects     []jsontext.Value `json:"projects,omitzero"`
	Worktrees    []jsontext.Value `json:"worktrees,omitzero"`
	LastFolder   OptionalString   `json:"lastFolder,omitzero"`
}

// TaskChange is one thread's writes in a TaskStoreDelta. Task is a PersistedTask.
type TaskChange struct {
	Task      jsontext.Value    `json:"task"`
	Messages  []IndexedMessage  `json:"messages"`
	Subagents []IndexedSubagent `json:"subagents,omitzero"`
	Activity  []IndexedActivity `json:"activity,omitzero"`
}

// IndexedMessage is a ConversationMessage at its place in the thread.
type IndexedMessage struct {
	Index   float64        `json:"index"`
	Message jsontext.Value `json:"message"`
}

// IndexedSubagent is a PersistedSubagent at its place in the thread.
type IndexedSubagent struct {
	Index    float64        `json:"index"`
	Subagent jsontext.Value `json:"subagent"`
}

// IndexedActivity is a SubagentActivity item at its place in its subagent's log.
type IndexedActivity struct {
	SubagentID string         `json:"subagentId"`
	Index      float64        `json:"index"`
	Item       jsontext.Value `json:"item"`
}

// OptionalString is `x?: string | null`: absent, null, or a string.
type OptionalString struct {
	Present bool
	Value   *string
}

// SomeString is a present OptionalString; nil is null.
func SomeString(value *string) OptionalString { return OptionalString{Present: true, Value: value} }

func (o OptionalString) IsZero() bool { return !o.Present }

func (o OptionalString) MarshalJSONTo(encoder *jsontext.Encoder) error {
	if o.Value == nil {
		return encoder.WriteToken(jsontext.Null)
	}
	return encoder.WriteToken(jsontext.String(*o.Value))
}

func (o *OptionalString) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
	o.Present = true
	o.Value = nil
	if decoder.PeekKind() == 'n' {
		_, err := decoder.ReadToken()
		return err
	}
	var value string
	if err := json.UnmarshalDecode(decoder, &value); err != nil {
		return err
	}
	o.Value = &value
	return nil
}

// ThreadStoreData is domain ThreadStoreData; each task carries its messages and subagents.
type ThreadStoreData struct {
	Version    float64          `json:"version"`
	Tasks      []jsontext.Value `json:"tasks"`
	Projects   []jsontext.Value `json:"projects"`
	Worktrees  []jsontext.Value `json:"worktrees"`
	LastFolder *string          `json:"lastFolder"`
}

// LoadedTaskStore is contracts LoadedTaskStore.
type LoadedTaskStore struct {
	ThreadStoreData   `json:",inline"`
	HiddenTasks       float64  `json:"hiddenTasks"`
	UnloadedTaskIDs   []string `json:"unloadedTaskIds,omitzero"`
	ResumeAfterUpdate []string `json:"resumeAfterUpdate,omitzero"`
}

// HistorySummary is a thread's historySummary: what the list needs of a conversation left on disk.
type HistorySummary struct {
	MessageCount    float64  `json:"messageCount"`
	AttachmentCount float64  `json:"attachmentCount"`
	FirstMessageAt  *float64 `json:"firstMessageAt,omitzero"`
	LastAudibleAt   *float64 `json:"lastAudibleAt,omitzero"`
}

// DecodedThreadStore is what validation receives: the stored values as read, unchecked.
// Worktrees and LastFolder hold JSON (LastFolder may be any value the setting held).
type DecodedThreadStore struct {
	Tasks      []jsontext.Value
	Projects   []jsontext.Value
	Worktrees  []jsontext.Value
	LastFolder jsontext.Value
}

// Domain is the domain layer's reading of stored threads (domain/thread-storage.ts). Either may
// be nil, in which case stored values pass through as read.
type Domain struct {
	// ValidateThreadStore is validateThreadStoreData: the readable store, how many threads it hid,
	// or the errors that make the whole store unreadable.
	ValidateThreadStore func(DecodedThreadStore) (ThreadStoreData, int, []string)
	// ParseStoredMessages is parseStoredConversationMessages.
	ParseStoredMessages func([]jsontext.Value) ([]jsontext.Value, error)
}
