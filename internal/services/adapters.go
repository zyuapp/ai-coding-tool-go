package services

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/zyuapp/ai-coding-tool-go/internal/automation"
	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
)

// Schedules is the scheduler with the run host it dispatches to and the port it is served as.
type Schedules struct {
	*automation.Scheduler
	port contracts.AutomationPortRuntime
}

func newSchedules(events *Events, store Automations, runs agent.RunBridge) *Schedules {
	dispatch := func(ctx context.Context, scheduled *domain.Automation, tick domain.TickKind) (domain.AutomationRunStatus, error) {
		return runs.DispatchAutomation(ctx, scheduled, tick), nil
	}
	held := &Schedules{}
	held.Scheduler = automation.NewScheduler(automation.DatabaseStore(store), dispatch, automation.Options{
		OnChange: func(automations []*domain.AutomationView) { events.AutomationChanged.Emit(automations) },
	})
	held.port = automation.NewDesktop(automation.DesktopHost{
		Scheduler:             func() *automation.Scheduler { return held.Scheduler },
		OnAutomationsChanged:  events.OnAutomationsChanged,
		OnAutomationFire:      events.OnAutomationFire,
		AcknowledgeAutomation: runs.AcknowledgeAutomation,
	})
	return held
}

// Desktop is the automation port as the runtime's desktop serves it.
func (a *Schedules) Desktop() computers.AutomationDesktop { return automationDesktop{a.port} }

type automationDesktop struct {
	contracts.AutomationPortRuntime
}

func (d automationDesktop) RunAutomationNow(ctx context.Context, taskID string) (host.RuntimeDesktopRunAutomationNowResult, error) {
	result, err := d.AutomationPortRuntime.RunAutomationNow(ctx, taskID)
	return host.RuntimeDesktopRunAutomationNowResult(result), err
}

// engineServices answers for each engine outside a run.
type engineServices struct {
	services map[domain.AgentEngine]agent.EngineServices
}

func (e engineServices) of(engine domain.AgentEngine) (agent.EngineServices, error) {
	if services := e.services[engine]; services != nil {
		return services, nil
	}
	return nil, errors.New(domain.UnopenableEngine(string(engine)))
}

func (e engineServices) PlanUsage(ctx context.Context, engine domain.AgentEngine) (domain.PlanUsage, error) {
	services, err := e.of(engine)
	if err != nil {
		return nil, err
	}
	return services.PlanUsage(ctx)
}

func (e engineServices) SuggestTitle(ctx context.Context, engine domain.AgentEngine, text string, images []string, model *domain.AgentModel) (*string, error) {
	services, err := e.of(engine)
	if err != nil {
		return nil, err
	}
	return services.SuggestTitle(ctx, text, images, model)
}

func (e engineServices) SubagentMetadata(ctx context.Context, engine domain.AgentEngine, subagentID string, sessionID *string) (*domain.SubagentMetadata, error) {
	services, err := e.of(engine)
	if err != nil {
		return nil, err
	}
	metadata, ok := services.(agent.SubagentMetadataService)
	if !ok {
		return nil, nil
	}
	answer, err := metadata.SubagentMetadata(ctx, subagentID, sessionID)
	if err != nil {
		return nil, err
	}
	return &answer, nil
}

type engineAccess struct{ host *agent.EngineAccessHost }

func (a engineAccess) Read(ctx context.Context, refresh bool) (*domain.EngineStatus, error) {
	return a.host.Read(ctx, refresh)
}

func (a engineAccess) SignIn(ctx context.Context, engine domain.AgentEngine, openURL func(ctx context.Context, url string) error) (*domain.EngineStatus, error) {
	return a.host.SignIn(ctx, engine, openURL)
}

func (a engineAccess) Update(ctx context.Context, engine domain.AgentEngine) (*domain.EngineStatus, error) {
	return a.host.Update(ctx, engine)
}

func sweptLine(files int, bytes int64) string {
	return fmt.Sprintf("Removed %d unused attachment file(s), %.0f MB.", files, math.Round(float64(bytes)/1024/1024))
}
