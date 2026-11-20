package automation

import (
	"context"
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// DesktopHost is what the automation port is served from: the scheduler wherever the runtime runs,
// the desktop's "automation:changed" and "automation:fire" events, and the run host's acknowledgements.
type DesktopHost struct {
	Scheduler            func() *Scheduler
	OnAutomationsChanged func(listener func(automations []*domain.AutomationView)) func()
	OnAutomationFire     func(listener func(fire contracts.AutomationFire)) func()
	// AcknowledgeAutomation is the run host's acknowledgeAutomation(runId, started).
	AcknowledgeAutomation func(runID string, started bool)
}

type desktop struct{ host DesktopHost }

// NewDesktop serves the automation port: scheduled runs, kept by the scheduler wherever the runtime runs.
func NewDesktop(host DesktopHost) contracts.AutomationPortRuntime { return desktop{host} }

func (d desktop) ListAutomations(context.Context) ([]*domain.AutomationView, error) {
	return d.host.Scheduler().List(), nil
}

func (d desktop) SaveAutomation(ctx context.Context, draft domain.AutomationDraft) (*domain.AutomationView, error) {
	return d.host.Scheduler().Save(ctx, draft, nil)
}

func (d desktop) UpdateAutomation(ctx context.Context, taskID string, patch domain.AutomationPatch) (*domain.AutomationView, error) {
	return d.host.Scheduler().Update(ctx, taskID, patch, nil)
}

func (d desktop) DeleteAutomation(ctx context.Context, taskID string) (bool, error) {
	return d.host.Scheduler().Remove(ctx, taskID)
}

func (d desktop) RunAutomationNow(ctx context.Context, taskID string) (contracts.AutomationPortRuntimeRunAutomationNowResult, error) {
	return d.host.Scheduler().RunNow(ctx, taskID)
}

func (d desktop) OnAutomationsChanged(listener func(automations []*domain.AutomationView)) func() {
	return d.host.OnAutomationsChanged(listener)
}

func (d desktop) OnAutomationFire(listener func(fire contracts.AutomationFire)) func() {
	return d.host.OnAutomationFire(listener)
}

func (d desktop) AcknowledgeAutomation(ack contracts.AutomationAck) {
	d.host.AcknowledgeAutomation(ack.RunID, ack.Started)
}

// RecordStore is the task database's automation table, which hands records back as raw JSON.
type RecordStore interface {
	ListAutomations() ([]jsontext.Value, error)
	SaveAutomation(automation any) error
	DeleteAutomation(id string) error
}

// DatabaseStore adapts the task database to the scheduler's store.
func DatabaseStore(database RecordStore) Store { return databaseStore{database} }

type databaseStore struct{ database RecordStore }

func (s databaseStore) ListAutomations(context.Context) ([]*domain.Automation, error) {
	records, err := s.database.ListAutomations()
	if err != nil {
		return nil, err
	}
	automations := make([]*domain.Automation, 0, len(records))
	for _, record := range records {
		var automation domain.Automation
		if err := jsonx.Unmarshal(record, &automation); err != nil {
			continue
		}
		automations = append(automations, &automation)
	}
	return automations, nil
}

func (s databaseStore) SaveAutomation(_ context.Context, automation *domain.Automation) error {
	return s.database.SaveAutomation(automation)
}

func (s databaseStore) DeleteAutomation(_ context.Context, id string) error {
	return s.database.DeleteAutomation(id)
}
