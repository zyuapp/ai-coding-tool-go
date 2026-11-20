package services

import (
	"context"
	"encoding/json/jsontext"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
)

// TaskDatabase is the thread store as the runtime's desktop reads and writes it.
type TaskDatabase struct {
	Store *store.TaskDatabaseService
	// early is the first load, started while the login shell is still asked for its search path.
	early *earlyLoad
}

type earlyLoad struct {
	mu      sync.Mutex
	pending *store.Pending[*contracts.LoadedTaskStore]
}

// newTaskDatabase starts reading the store at once, so the runtime's first load does not wait for it.
func newTaskDatabase(database *store.TaskDatabaseService) TaskDatabase {
	return TaskDatabase{Store: database, early: &earlyLoad{pending: database.LoadSummaries()}}
}

func (d TaskDatabase) LoadSummaries(ctx context.Context) (*contracts.LoadedTaskStore, error) {
	pending := d.Store.LoadSummaries
	if d.early != nil {
		d.early.mu.Lock()
		if early := d.early.pending; early != nil {
			d.early.pending = nil
			pending = func() *store.Pending[*contracts.LoadedTaskStore] { return early }
		}
		d.early.mu.Unlock()
	}
	return pending().WaitContext(ctx)
}

func (d TaskDatabase) LoadThreadMessages(ctx context.Context, taskID string) ([]*domain.ConversationMessage, error) {
	return d.Store.LoadThreadMessages(taskID).WaitContext(ctx)
}

func (d TaskDatabase) SearchThreadMessages(ctx context.Context, search string, taskIDs []string) ([]string, error) {
	return d.Store.SearchThreadMessages(store.JSLower(search), taskIDs).WaitContext(ctx)
}

func (d TaskDatabase) Persist(ctx context.Context, delta contracts.TaskStoreDelta) error {
	_, err := d.Store.Persist(delta).WaitContext(ctx)
	return err
}

func (d TaskDatabase) SubagentActivity(ctx context.Context, taskID, subagentID string) ([]*domain.SubagentActivity, error) {
	return d.Store.SubagentActivity(taskID, subagentID).WaitContext(ctx)
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
