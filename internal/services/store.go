package services

import (
	"context"
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
)

// TaskDatabase is the thread store as the runtime's desktop reads and writes it, in the generated
// types; the store itself keeps each record's JSON as it was written.
type TaskDatabase struct {
	Store *store.TaskDatabaseService
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

func (d TaskDatabase) LoadSummaries(ctx context.Context) (*contracts.LoadedTaskStore, error) {
	loaded, err := d.Store.LoadSummaries().WaitContext(ctx)
	if err != nil || loaded == nil {
		return nil, err
	}
	return convert[*contracts.LoadedTaskStore](loaded)
}

func (d TaskDatabase) LoadThreadMessages(ctx context.Context, taskID string) ([]*domain.ConversationMessage, error) {
	messages, err := d.Store.LoadThreadMessages(taskID).WaitContext(ctx)
	if err != nil {
		return nil, err
	}
	return decodeEach[domain.ConversationMessage](messages)
}

func (d TaskDatabase) SearchThreadMessages(ctx context.Context, search string, taskIDs []string) ([]string, error) {
	return d.Store.SearchThreadMessages(store.JSLower(search), taskIDs).WaitContext(ctx)
}

func (d TaskDatabase) Persist(ctx context.Context, delta contracts.TaskStoreDelta) error {
	_, err := d.Store.Persist(delta).WaitContext(ctx)
	return err
}

func (d TaskDatabase) SubagentActivity(ctx context.Context, taskID, subagentID string) ([]*domain.SubagentActivity, error) {
	activity, err := d.Store.SubagentActivity(taskID, subagentID).WaitContext(ctx)
	if err != nil {
		return nil, err
	}
	return decodeEach[domain.SubagentActivity](activity)
}

// AttachmentPaths names every image a stored message still carries.
func (d TaskDatabase) AttachmentPaths(ctx context.Context) ([]string, error) {
	return d.Store.AttachmentPaths().WaitContext(ctx)
}

// Automations is the store's automation rows, as the scheduler's store reads them.
type Automations struct {
	Store *store.TaskDatabaseService
}

func (a Automations) ListAutomations() ([]jsontext.Value, error) {
	return a.Store.ListAutomations().Wait()
}

func (a Automations) SaveAutomation(automation any) error {
	_, err := a.Store.SaveAutomation(automation).Wait()
	return err
}

func (a Automations) DeleteAutomation(id string) error {
	_, err := a.Store.DeleteAutomation(id).Wait()
	return err
}
