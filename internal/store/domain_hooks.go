package store

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func decodeValues(values []jsontext.Value) ([]any, error) {
	decoded := make([]any, len(values))
	for i, value := range values {
		if err := jsonx.Unmarshal(value, &decoded[i]); err != nil {
			return nil, err
		}
	}
	return decoded, nil
}

func decodeEach[T any](values []jsontext.Value) ([]*T, error) {
	decoded := make([]*T, len(values))
	for i, value := range values {
		decoded[i] = new(T)
		if err := jsonx.Unmarshal(value, decoded[i]); err != nil {
			return nil, err
		}
	}
	return decoded, nil
}

// validateThreadStore is validateThreadStoreData over the rows as read: the readable store, how many
// threads it hid, or the errors that make the whole store unreadable.
func validateThreadStore(values decodedThreadStore) (*domain.ThreadStoreData, int, []string) {
	var decoded domain.DecodedThreadStore
	tasks, err := decodeValues(values.Tasks)
	if err != nil {
		return nil, 0, []string{err.Error()}
	}
	projects, err := decodeValues(values.Projects)
	if err != nil {
		return nil, 0, []string{err.Error()}
	}
	decoded.Tasks, decoded.Projects = tasks, projects
	if values.Worktrees != nil {
		worktrees, err := decodeValues(values.Worktrees)
		if err != nil {
			return nil, 0, []string{err.Error()}
		}
		decoded.Worktrees = worktrees
	}
	if len(values.LastFolder) > 0 {
		if err := jsonx.Unmarshal(values.LastFolder, &decoded.LastFolder); err != nil {
			return nil, 0, []string{err.Error()}
		}
	}
	result := domain.ValidateThreadStoreData(decoded)
	if !result.Ok {
		return nil, 0, result.Errors
	}
	return &result.Data, result.HiddenTasks, nil
}

// parseStoredMessages is parseStoredConversationMessages over the rows as read.
func parseStoredMessages(values []jsontext.Value) ([]*domain.ConversationMessage, error) {
	return domain.ParseStoredConversationRows(values)
}
