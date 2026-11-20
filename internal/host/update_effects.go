package host

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// UpdateHost carries the app's own updates, and the build and update this launch reports on the way up.
var UpdateHost = HostModule{
	Effects: EffectHandlers{
		"app.check-for-updates": told(func(effect application.WorkspaceEffectAppCheckForUpdates, desktop RuntimeDesktop) {
			desktop.CheckForUpdates(effect.Quiet)
		}),
		"app.download-update": told(func(effect application.WorkspaceEffectAppDownloadUpdate, desktop RuntimeDesktop) {
			desktop.DownloadUpdate(effect.Unattended)
		}),
		"app.install-update": told(func(effect application.WorkspaceEffectAppInstallUpdate, desktop RuntimeDesktop) {
			desktop.InstallUpdate(effect.Unattended)
		}),
	},
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		desktop := host.Desktop
		go func() {
			if update, err := desktop.AppUpdate(background); err == nil {
				host.send(application.AppUpdateEventAppUpdateChanged{Update: update})
			}
		}()
		go func() {
			if build, err := desktop.AppBuild(background); err == nil {
				host.send(application.AppUpdateEventAppBuild{Build: build})
			}
		}()
		go func() {
			if version, err := desktop.LaunchedUpdate(background); err == nil && version != nil && *version != "" {
				host.send(application.AppUpdateEventAppLaunched{Version: *version})
			}
		}()
		return desktop.OnAppUpdate(func(update domain.AppUpdate) {
			host.send(application.AppUpdateEventAppUpdateChanged{Update: update})
		})
	},
}
