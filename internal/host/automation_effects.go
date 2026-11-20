package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// AutomationHost carries scheduled runs: changing them, and the ticks that fire them.
var AutomationHost = HostModule{
	Effects: EffectHandlers{
		"automation.save": reporting(func(ctx context.Context, effect application.WorkspaceEffectAutomationSave, desktop RuntimeDesktop) error {
			_, err := desktop.SaveAutomation(ctx, effect.Draft)
			return err
		}),
		"automation.update": reporting(func(ctx context.Context, effect application.WorkspaceEffectAutomationUpdate, desktop RuntimeDesktop) error {
			_, err := desktop.UpdateAutomation(ctx, effect.TaskID, effect.Patch)
			return err
		}),
		"automation.delete": reporting(func(ctx context.Context, effect application.WorkspaceEffectAutomationDelete, desktop RuntimeDesktop) error {
			_, err := desktop.DeleteAutomation(ctx, effect.TaskID)
			return err
		}),
		"automation.run-now": handle(func(ctx context.Context, effect application.WorkspaceEffectAutomationRunNow, host EffectHost) error {
			status, err := host.Desktop.RunAutomationNow(ctx, effect.TaskID)
			if err == nil && (status == RuntimeDesktopRunAutomationNowResultBusy || status == RuntimeDesktopRunAutomationNowResultSkipped) {
				err = host.Dispatch(ctx, contracts.ActionFailedEvent{Message: application.WorkspaceErrors["busyAutomation"]})
			}
			return ReportFailure(ctx, host, err)
		}),
		"automation.ack": told(func(effect application.WorkspaceEffectAutomationAck, desktop RuntimeDesktop) {
			desktop.AcknowledgeAutomation(effect.Ack)
		}),
	},
	// A tick folds in what runs have reported first, since the run it starts may land in one of their threads.
	Subscribe: func(host SubscriptionHost, runs RunReports) func() {
		desktop := host.Desktop
		go func() {
			automations, err := desktop.ListAutomations(background)
			if err != nil {
				host.send(failed(err))
				return
			}
			host.send(application.AutomationEventAutomationsChanged{Automations: automations})
		}()
		stopWatching := desktop.OnAutomationsChanged(func(automations []*domain.AutomationView) {
			host.send(application.AutomationEventAutomationsChanged{Automations: automations})
		})
		stopFiring := desktop.OnAutomationFire(func(fire contracts.AutomationFire) {
			runs.FlushAll()
			host.send(application.AutomationEventAutomationFired{Fire: fire})
		})
		return func() {
			stopWatching()
			stopFiring()
		}
	},
}
