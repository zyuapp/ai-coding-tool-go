package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// surfaced carries an effect to the window's own views; a host without a window drops it.
func surfaced[E interface {
	application.WorkspaceEffect
	application.WorkspaceSurfaceEffect
}]() EffectHandler {
	return handle(func(_ context.Context, effect E, host EffectHost) error {
		if host.Surface != nil {
			host.Surface(effect)
		}
		return nil
	})
}

// FindHost finds in what holds its own text: a page counts its matches in main, a shell's view counts its own.
var FindHost = HostModule{
	Effects: EffectHandlers{
		"find-in-page": reporting(func(ctx context.Context, effect application.WorkspaceEffectFindInPage, desktop RuntimeDesktop) error {
			return desktop.FindInPage(ctx, effect.TabID, effect.Query, effect.Forward, effect.FindNext)
		}),
		"stop-find-in-page": reporting(func(ctx context.Context, effect application.WorkspaceEffectStopFindInPage, desktop RuntimeDesktop) error {
			return desktop.StopFindInPage(ctx, effect.TabID)
		}),
		"find-in-terminal":      surfaced[application.WorkspaceEffectFindInTerminal](),
		"stop-find-in-terminal": surfaced[application.WorkspaceEffectStopFindInTerminal](),
	},
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		return host.Desktop.OnBrowserFind(func(event contracts.BrowserFindEvent) {
			host.send(contracts.FindResultsEvent{
				Target:  domain.FindTargetBrowser{TabID: event.TabID},
				Results: domain.FindResults{Matches: event.Matches, Index: event.Index},
			})
		})
	},
}
