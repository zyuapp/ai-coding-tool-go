package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// readEngines dispatches where every engine stands: asked outright, or answered by an engine's own
// sign-in once it is over. An update answers apart, naming its engine.
func readEngines(ctx context.Context, ask func(ctx context.Context) (*domain.EngineStatus, error), host EffectHost) error {
	status, err := ask(ctx)
	if err == nil {
		err = host.Dispatch(ctx, application.EngineEventEngineStatus{Status: *status})
	}
	if err != nil {
		return host.Dispatch(ctx, application.EngineEventEngineFailed{Message: errorMessage(err)})
	}
	return nil
}

// EngineEffects carry the engines a run can take, their settings, and what their plans have left.
var EngineEffects = EffectHandlers{
	"engine.reload-settings": told(func(_ application.WorkspaceEffectEngineReloadSettings, desktop RuntimeDesktop) {
		desktop.Send(contracts.ReloadAgentSettingsCommand{})
	}),
	"read-plan-usage": handle(func(ctx context.Context, effect application.WorkspaceEffectReadPlanUsage, host EffectHost) error {
		usage, err := host.Desktop.PlanUsage(ctx, effect.Engine)
		if err != nil {
			usage = domain.ComputerUseRunConfigUnavailable{Message: contracts.ReportedMessage(err)}
		}
		return host.Dispatch(ctx, application.PlanUsageEvent{Engine: effect.Engine, Read: effect.Read, Usage: usage})
	}),
	"engine.read": handle(func(ctx context.Context, effect application.WorkspaceEffectEngineRead, host EffectHost) error {
		return readEngines(ctx, func(ctx context.Context) (*domain.EngineStatus, error) {
			return host.Desktop.EngineStatus(ctx, effect.Refresh)
		}, host)
	}),
	"engine.sign-in": handle(func(ctx context.Context, effect application.WorkspaceEffectEngineSignIn, host EffectHost) error {
		return readEngines(ctx, func(ctx context.Context) (*domain.EngineStatus, error) {
			return host.Desktop.SignInEngine(ctx, effect.Engine)
		}, host)
	}),
	"engine.update": handle(func(ctx context.Context, effect application.WorkspaceEffectEngineUpdate, host EffectHost) error {
		status, err := host.Desktop.UpdateEngine(ctx, effect.Engine)
		if err == nil {
			err = host.Dispatch(ctx, application.EngineEventEngineUpdated{Engine: effect.Engine, Status: *status})
		}
		if err != nil {
			return host.Dispatch(ctx, application.EngineEventEngineUpdateFailed{Engine: effect.Engine, Message: errorMessage(err)})
		}
		return nil
	}),
}

var EngineHost = HostModule{
	Effects: EngineEffects,
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		host.send(contracts.EngineCommandEngineRead{})
		return nil
	},
}
