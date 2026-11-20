package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// ReportFailure dispatches what went wrong with work whose only answer is its failure. The work
// has already run: err is what it returned.
func ReportFailure(ctx context.Context, host EffectHost, err error) error {
	if err == nil {
		return nil
	}
	return host.Dispatch(ctx, contracts.ActionFailedEvent{Message: errorMessage(err)})
}

// errorMessage is contracts.ErrorMessage for a Go error.
func errorMessage(err error) string { return contracts.ErrorMessage(err) }

// handle adapts a handler of one effect member to an EffectHandler.
func handle[E application.WorkspaceEffect](run func(ctx context.Context, effect E, host EffectHost) error) EffectHandler {
	return func(ctx context.Context, effect application.WorkspaceEffect, host EffectHost) error {
		return run(ctx, effect.(E), host)
	}
}

// reporting is a handler whose only answer is what went wrong with the work it does.
func reporting[E application.WorkspaceEffect](work func(ctx context.Context, effect E, desktop RuntimeDesktop) error) EffectHandler {
	return handle(func(ctx context.Context, effect E, host EffectHost) error {
		return ReportFailure(ctx, host, work(ctx, effect, host.Desktop))
	})
}

// told is a handler that tells the desktop something and waits for nothing.
func told[E application.WorkspaceEffect](tell func(effect E, desktop RuntimeDesktop)) EffectHandler {
	return handle(func(_ context.Context, effect E, host EffectHost) error {
		tell(effect, host.Desktop)
		return nil
	})
}
