package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// SurfaceEffects open files and folders out of the window, and save the images shown in it.
var SurfaceEffects = EffectHandlers{
	"image.download": reporting(func(ctx context.Context, effect application.WorkspaceEffectImageDownload, desktop RuntimeDesktop) error {
		return desktop.DownloadImage(ctx, effect.Source)
	}),
	"file.open": reporting(func(ctx context.Context, effect application.WorkspaceEffectFileOpen, desktop RuntimeDesktop) error {
		return desktop.OpenFile(ctx, effect.Roots, effect.Path, effect.Line)
	}),
	// An application scan that fails leaves the menu with nothing to offer rather than an error.
	"app.list": handle(func(ctx context.Context, _ application.WorkspaceEffectAppList, host EffectHost) error {
		apps, err := host.Desktop.ListApps(ctx)
		if err != nil || apps == nil {
			apps = []*contracts.InstalledApp{}
		}
		return host.Dispatch(ctx, application.DesktopEvent{Apps: apps})
	}),
	"app.open-folder": reporting(func(ctx context.Context, effect application.WorkspaceEffectAppOpenFolder, desktop RuntimeDesktop) error {
		return desktop.OpenFolderInApp(ctx, effect.AppID, effect.Root)
	}),
	"app.open-source-licenses": reporting(func(ctx context.Context, _ application.WorkspaceEffectAppOpenSourceLicenses, desktop RuntimeDesktop) error {
		return desktop.OpenSourceLicenses(ctx)
	}),
}

var SurfaceHost = HostModule{Effects: SurfaceEffects}
