package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// readCli dispatches where the terminal command stands, after reading it or after changing it.
func readCli(ctx context.Context, ask func(ctx context.Context) (*domain.CliStatus, error), host EffectHost) error {
	status, err := ask(ctx)
	if err == nil {
		err = host.Dispatch(ctx, application.CliEventCliReadStatus{Status: *status})
	}
	if err != nil {
		return host.Dispatch(ctx, application.CliEventCliFailed{Message: contracts.ReportedMessage(err)})
	}
	return nil
}

// readComputerUse dispatches what the platform allows, whether it was asked outright or after the
// user was sent to grant one (enabling).
func readComputerUse(ctx context.Context, ask func(ctx context.Context) (*domain.ComputerUsePermissions, error), host EffectHost, enabling bool) error {
	var marked *bool
	if enabling {
		marked = new(true)
	}
	permissions, err := ask(ctx)
	if err == nil {
		err = host.Dispatch(ctx, application.ComputerUseEventComputerUsePermissions{Permissions: *permissions, Enabling: marked})
	}
	if err != nil {
		return host.Dispatch(ctx, application.ComputerUseEventComputerUseFailed{Message: contracts.ReportedMessage(err), Enabling: marked})
	}
	return nil
}

// SystemEffects tell the window itself, its schedules and its settings, none of which belongs to a thread.
var SystemEffects = EffectHandlers{
	"schedule-idle-compaction": handle(func(_ context.Context, effect application.WorkspaceEffectScheduleIdleCompaction, host EffectHost) error {
		host.ScheduleIdleCompaction(effect.At)
		return nil
	}),
	"schedule-limit-reset": handle(func(_ context.Context, effect application.WorkspaceEffectScheduleLimitReset, host EffectHost) error {
		host.ScheduleLimitReset(effect.At)
		return nil
	}),
	"schedule-snooze-expiry": handle(func(_ context.Context, effect application.WorkspaceEffectScheduleSnoozeExpiry, host EffectHost) error {
		host.ScheduleSnoozeExpiry(effect.At)
		return nil
	}),
	"schedule-toast-dismissal": handle(func(_ context.Context, effect application.WorkspaceEffectScheduleToastDismissal, host EffectHost) error {
		host.ScheduleToastDismissal(effect.ID, effect.At)
		return nil
	}),
	"focus-window": told(func(_ application.WorkspaceEffectFocusWindow, desktop RuntimeDesktop) { desktop.FocusWindow() }),
	"close-window": told(func(_ application.WorkspaceEffectCloseWindow, desktop RuntimeDesktop) { desktop.CloseWindow() }),
	"apply-shortcuts": told(func(effect application.WorkspaceEffectApplyShortcuts, desktop RuntimeDesktop) {
		desktop.SetShortcuts(effect.Overrides)
	}),
	"apply-capture-options": told(func(effect application.WorkspaceEffectApplyCaptureOptions, desktop RuntimeDesktop) {
		desktop.SetCaptureOptions(effect.Options)
	}),
	"capture-shortcut": told(func(effect application.WorkspaceEffectCaptureShortcut, desktop RuntimeDesktop) {
		desktop.SetShortcutCapture(effect.Capturing)
	}),
	"announce-thread": told(func(effect application.WorkspaceEffectAnnounceThread, desktop RuntimeDesktop) {
		desktop.AnnounceThread(effect.Notice)
	}),
	"computer-use.read": handle(func(ctx context.Context, _ application.WorkspaceEffectComputerUseRead, host EffectHost) error {
		return readComputerUse(ctx, host.Desktop.ComputerUsePermissions, host, false)
	}),
	"computer-use.enable": handle(func(ctx context.Context, effect application.WorkspaceEffectComputerUseEnable, host EffectHost) error {
		return readComputerUse(ctx, func(ctx context.Context) (*domain.ComputerUsePermissions, error) {
			return host.Desktop.EnableComputerUse(ctx, effect.Permission)
		}, host, true)
	}),
	"computer-use.restart": told(func(_ application.WorkspaceEffectComputerUseRestart, desktop RuntimeDesktop) {
		desktop.RestartForComputerUse()
	}),
	"cli.read": handle(func(ctx context.Context, _ application.WorkspaceEffectCliRead, host EffectHost) error {
		return readCli(ctx, host.Desktop.CliStatus, host)
	}),
	"cli.install": handle(func(ctx context.Context, _ application.WorkspaceEffectCliInstall, host EffectHost) error {
		return readCli(ctx, host.Desktop.InstallCli, host)
	}),
	"cli.uninstall": handle(func(ctx context.Context, _ application.WorkspaceEffectCliUninstall, host EffectHost) error {
		return readCli(ctx, host.Desktop.UninstallCli, host)
	}),
}

var SystemHost = HostModule{Effects: SystemEffects}
