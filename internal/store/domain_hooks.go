package store

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// DomainReading is the domain layer's reading of stored threads (validateThreadStoreData and
// parseStoredConversationMessages), over the raw JSON the store keeps.
func DomainReading() Domain {
	return Domain{ValidateThreadStore: validateThreadStore, ParseStoredMessages: parseStoredMessages}
}

func decodeValues(values []jsontext.Value) ([]any, error) {
	decoded := make([]any, len(values))
	for i, value := range values {
		if err := jsonx.Unmarshal(value, &decoded[i]); err != nil {
			return nil, err
		}
	}
	return decoded, nil
}

func encodeValues[T any](values []*T) ([]jsontext.Value, error) {
	encoded := make([]jsontext.Value, len(values))
	for i, value := range values {
		data, err := jsonx.Marshal(value)
		if err != nil {
			return nil, err
		}
		encoded[i] = data
	}
	return encoded, nil
}

func validateThreadStore(values DecodedThreadStore) (ThreadStoreData, int, []string) {
	var decoded domain.DecodedThreadStore
	tasks, err := decodeValues(values.Tasks)
	if err != nil {
		return ThreadStoreData{}, 0, []string{err.Error()}
	}
	projects, err := decodeValues(values.Projects)
	if err != nil {
		return ThreadStoreData{}, 0, []string{err.Error()}
	}
	decoded.Tasks, decoded.Projects = tasks, projects
	if values.Worktrees != nil {
		worktrees, err := decodeValues(values.Worktrees)
		if err != nil {
			return ThreadStoreData{}, 0, []string{err.Error()}
		}
		decoded.Worktrees = worktrees
	}
	if len(values.LastFolder) > 0 {
		if err := jsonx.Unmarshal(values.LastFolder, &decoded.LastFolder); err != nil {
			return ThreadStoreData{}, 0, []string{err.Error()}
		}
	}
	result := domain.ValidateThreadStoreData(decoded)
	if !result.Ok {
		return ThreadStoreData{}, 0, result.Errors
	}
	data := ThreadStoreData{Version: 2, LastFolder: result.Data.LastFolder}
	if data.Tasks, err = encodeValues(result.Data.Tasks); err != nil {
		return ThreadStoreData{}, 0, []string{err.Error()}
	}
	if data.Projects, err = encodeValues(result.Data.Projects); err != nil {
		return ThreadStoreData{}, 0, []string{err.Error()}
	}
	if data.Worktrees, err = encodeValues(result.Data.Worktrees); err != nil {
		return ThreadStoreData{}, 0, []string{err.Error()}
	}
	return data, result.HiddenTasks, nil
}

func parseStoredMessages(values []jsontext.Value) ([]jsontext.Value, error) {
	decoded, err := decodeValues(values)
	if err != nil {
		return nil, err
	}
	messages, err := domain.ParseStoredConversationMessages(decoded)
	if err != nil {
		return nil, err
	}
	return encodeValues(messages)
}
