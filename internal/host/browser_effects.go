package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// importing reports a failed import step beside the import, not in the workspace error banner.
func importing(ctx context.Context, host EffectHost, work func(ctx context.Context) error) error {
	if err := work(ctx); err != nil {
		return host.Dispatch(ctx, application.BrowserImportEventBrowserImportFailed{Message: errorMessage(err)})
	}
	return nil
}

// BrowserHost carries the browser panel's pages, and the sites copied in from other browsers.
var BrowserHost = HostModule{
	Effects: EffectHandlers{
		"browser.permissions": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserPermissions, desktop RuntimeDesktop) error {
			return desktop.ConfigureBrowserPermissions(ctx, effect.Permissions)
		}),
		"browser.open": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserOpen, desktop RuntimeDesktop) error {
			return desktop.OpenBrowserTab(ctx, effect.TabID, effect.URL, effect.TaskID)
		}),
		"browser.navigate": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserNavigate, desktop RuntimeDesktop) error {
			return desktop.NavigateBrowser(ctx, effect.TabID, effect.URL, effect.TaskID)
		}),
		"browser.history": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserHistory, desktop RuntimeDesktop) error {
			return desktop.BrowserHistory(ctx, effect.TabID, effect.Delta, effect.TaskID)
		}),
		"browser.reload": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserReload, desktop RuntimeDesktop) error {
			return desktop.ReloadBrowser(ctx, effect.TabID, effect.TaskID)
		}),
		"browser.close": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserClose, desktop RuntimeDesktop) error {
			return desktop.CloseBrowserTab(ctx, effect.TabID)
		}),
		"browser.show": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserShow, desktop RuntimeDesktop) error {
			return desktop.ShowBrowserTab(ctx, effect.TabID)
		}),
		"browser.act": reporting(func(ctx context.Context, effect application.WorkspaceEffectBrowserAct, desktop RuntimeDesktop) error {
			_, err := desktop.ActInBrowser(ctx, effect.TabID, effect.Action, effect.TaskID)
			return err
		}),
		"browser.clear-data": reporting(func(ctx context.Context, _ application.WorkspaceEffectBrowserClearData, desktop RuntimeDesktop) error {
			return desktop.ClearBrowserData(ctx)
		}),
		"focus-browser": reporting(func(ctx context.Context, effect application.WorkspaceEffectFocusBrowser, desktop RuntimeDesktop) error {
			return desktop.FocusBrowserTab(ctx, effect.TabID)
		}),
		"browser-import.list-sources": handle(func(ctx context.Context, _ application.WorkspaceEffectBrowserImportListSources, host EffectHost) error {
			return importing(ctx, host, func(ctx context.Context) error {
				sources, err := host.Desktop.ListBrowserImportSources(ctx)
				if err != nil {
					return err
				}
				return host.Dispatch(ctx, application.BrowserImportEventBrowserImportSources{Sources: sources})
			})
		}),
		"browser-import.list-sites": handle(func(ctx context.Context, effect application.WorkspaceEffectBrowserImportListSites, host EffectHost) error {
			return importing(ctx, host, func(ctx context.Context) error {
				sites, err := host.Desktop.ListBrowserImportSites(ctx, effect.SourceID)
				if err != nil {
					return err
				}
				return host.Dispatch(ctx, application.BrowserImportEventBrowserImportSites{SourceID: effect.SourceID, Sites: sites})
			})
		}),
		"browser-import.import": handle(func(ctx context.Context, effect application.WorkspaceEffectBrowserImportImport, host EffectHost) error {
			return importing(ctx, host, func(ctx context.Context) error {
				result, err := host.Desktop.ImportBrowserSites(ctx, effect.SourceID, effect.Sites)
				if err != nil {
					return err
				}
				return host.Dispatch(ctx, application.BrowserImportEventBrowserImportDone{SourceID: effect.SourceID, Sites: effect.Sites, Result: *result})
			})
		}),
	},
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		return host.Desktop.OnBrowserEvent(func(page contracts.BrowserPageEvent) {
			host.send(application.BrowserEvent{Page: page})
		})
	},
}
