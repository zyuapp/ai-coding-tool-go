package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// DictationEffects drive the microphone and speech model, each answer carried back under the hold it belongs to.
var DictationEffects = EffectHandlers{
	"dictation.start": handle(func(ctx context.Context, effect application.WorkspaceEffectDictationStart, host EffectHost) error {
		err := host.Desktop.StartDictation(ctx, effect.ID)
		if err == nil {
			err = host.Dispatch(ctx, application.DictationEventDictationStarted{ID: effect.ID})
		}
		if err != nil {
			return host.Dispatch(ctx, application.DictationEventDictationFailed{ID: effect.ID, Message: errorMessage(err)})
		}
		return nil
	}),
	"dictation.stop": handle(func(ctx context.Context, effect application.WorkspaceEffectDictationStop, host EffectHost) error {
		text, err := host.Desktop.StopDictation(ctx, effect.ID)
		if err == nil {
			err = host.Dispatch(ctx, application.DictationEventDictationTranscribed{ID: effect.ID, Text: text})
		}
		if err != nil {
			return host.Dispatch(ctx, application.DictationEventDictationFailed{ID: effect.ID, Message: errorMessage(err)})
		}
		return nil
	}),
	"dictation.cancel": handle(func(ctx context.Context, effect application.WorkspaceEffectDictationCancel, host EffectHost) error {
		_ = host.Desktop.CancelDictation(ctx, effect.ID)
		return nil
	}),
	"dictation.cancel-download": reporting(func(ctx context.Context, _ application.WorkspaceEffectDictationCancelDownload, desktop RuntimeDesktop) error {
		return desktop.CancelDictationDownload(ctx)
	}),
	"dictation.download": handle(func(ctx context.Context, _ application.WorkspaceEffectDictationDownload, host EffectHost) error {
		if err := host.Desktop.DownloadDictationModel(ctx); err != nil {
			return host.Dispatch(ctx, application.DictationEventDictationModel{Model: "failed", Message: new(errorMessage(err))})
		}
		return nil
	}),
}

var DictationHost = HostModule{
	Effects: DictationEffects,
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		return host.Desktop.OnDictationModel(func(status contracts.DictationModelStatus) {
			event := application.DictationEventDictationModel{Model: status.Model, Progress: status.Progress}
			if status.Message != nil && *status.Message != "" {
				event.Message = status.Message
			}
			host.send(event)
		})
	},
}
