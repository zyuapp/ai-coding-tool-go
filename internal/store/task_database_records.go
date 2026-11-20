package store

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// The rows a contracts.TaskStoreDelta writes, each record the JSON a row stores. Nil Projects or
// Worktrees leave those rows alone; a non-nil empty list clears them.
type rowDelta struct {
	Tasks        []rowChange      `json:"tasks"`
	RemovedTasks []string         `json:"removedTasks,omitzero"`
	Projects     []jsontext.Value `json:"projects,omitzero"`
	Worktrees    []jsontext.Value `json:"worktrees,omitzero"`
	LastFolder   optionalString   `json:"lastFolder,omitzero"`
}

// rowChange is one thread's writes in a rowDelta. Task is a PersistedTask.
type rowChange struct {
	Task      jsontext.Value `json:"task"`
	Messages  []rowMessage   `json:"messages"`
	Subagents []rowSubagent  `json:"subagents,omitzero"`
	Activity  []rowActivity  `json:"activity,omitzero"`
}

// rowMessage is a ConversationMessage at its place in the thread.
type rowMessage struct {
	Index   float64        `json:"index"`
	Message jsontext.Value `json:"message"`
}

// rowSubagent is a PersistedSubagent at its place in the thread.
type rowSubagent struct {
	Index    float64        `json:"index"`
	Subagent jsontext.Value `json:"subagent"`
}

// rowActivity is a SubagentActivity item at its place in its subagent's log.
type rowActivity struct {
	SubagentID string         `json:"subagentId"`
	Index      float64        `json:"index"`
	Item       jsontext.Value `json:"item"`
}

// optionalString is `x?: string | null`: absent, null, or a string.
type optionalString struct {
	Present bool
	Value   *string
}

func (o optionalString) IsZero() bool { return !o.Present }

func (o optionalString) MarshalJSONTo(encoder *jsontext.Encoder) error {
	if o.Value == nil {
		return encoder.WriteToken(jsontext.Null)
	}
	return encoder.WriteToken(jsontext.String(*o.Value))
}

func (o *optionalString) UnmarshalJSONFrom(decoder *jsontext.Decoder) error {
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

// decodedThreadStore is what validation receives: the stored values as read, unchecked.
// Worktrees and LastFolder hold JSON (LastFolder may be any value the setting held).
type decodedThreadStore struct {
	Tasks      []jsontext.Value
	Projects   []jsontext.Value
	Worktrees  []jsontext.Value
	LastFolder jsontext.Value
}
