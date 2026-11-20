package host

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// TerminalHost runs the terminal panel's shells. Their output goes straight to the view; only what they did lands here.
var TerminalHost = HostModule{
	Effects: EffectHandlers{
		"terminal.start": reporting(func(ctx context.Context, effect application.WorkspaceEffectTerminalStart, desktop RuntimeDesktop) error {
			return desktop.StartTerminal(ctx, effect.TerminalID, contracts.TerminalStartOptions{Cwd: effect.Cwd})
		}),
		"terminal.write": reporting(func(ctx context.Context, effect application.WorkspaceEffectTerminalWrite, desktop RuntimeDesktop) error {
			return desktop.WriteTerminal(ctx, effect.TerminalID, effect.Data)
		}),
		"terminal.resize": reporting(func(ctx context.Context, effect application.WorkspaceEffectTerminalResize, desktop RuntimeDesktop) error {
			return desktop.ResizeTerminal(ctx, effect.TerminalID, effect.Cols, effect.Rows)
		}),
		// The view outlives the panel, so the shell going is the only thing that takes it away.
		"terminal.close": handle(func(ctx context.Context, effect application.WorkspaceEffectTerminalClose, host EffectHost) error {
			if host.Surface != nil {
				host.Surface(effect)
			}
			return ReportFailure(ctx, host, host.Desktop.CloseTerminal(ctx, effect.TerminalID))
		}),
	},
	Subscribe: func(host SubscriptionHost, _ RunReports) func() {
		return host.Desktop.OnTerminalEvent(func(update domain.TerminalUpdate) {
			host.send(application.TerminalEvent{Update: update})
		})
	},
}
