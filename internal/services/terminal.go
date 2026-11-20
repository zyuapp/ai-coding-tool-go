package services

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/terminal"
)

// Terminals is the terminal port over the PTY host (terminal-host.ts's terminalDesktop). Updates go
// to the runtime as "terminal:event"; output goes straight to whatever view Data names.
type Terminals struct {
	Host   *terminal.Host
	events *Events

	data Topic[contracts.TerminalDataEvent]
}

var _ contracts.TerminalPortRuntime = (*Terminals)(nil)

func newTerminals(events *Events, env func() []string) *Terminals {
	terminals := &Terminals{events: events}
	terminals.Host = terminal.NewHost(terminalSink{terminals}, terminal.Options{Env: env})
	return terminals
}

type terminalSink struct{ terminals *Terminals }

func (s terminalSink) TerminalData(event terminal.DataEvent) { s.terminals.data.Emit(event) }

func (s terminalSink) TerminalUpdated(update terminal.Update) {
	s.terminals.events.TerminalEvent.Emit(update)
}

// OnTerminalData is every terminal's output, coalesced, for the window's own view.
func (t *Terminals) OnTerminalData(listener func(event contracts.TerminalDataEvent)) func() {
	return t.data.On(listener)
}

func (t *Terminals) StartTerminal(_ context.Context, terminalID string, options contracts.TerminalStartOptions) error {
	t.Host.Start(terminalID, options.Cwd)
	return nil
}

func (t *Terminals) WriteTerminal(_ context.Context, terminalID string, data string) error {
	return t.Host.Write(terminalID, data)
}

func (t *Terminals) ResizeTerminal(_ context.Context, terminalID string, cols float64, rows float64) error {
	return t.Host.Resize(terminalID, cols, rows)
}

func (t *Terminals) CloseTerminal(_ context.Context, terminalID string) error {
	t.Host.Close(terminalID)
	return nil
}

func (t *Terminals) ReadTerminal(_ context.Context, terminalID string, options contracts.TerminalReadOptions) (*contracts.TerminalText, error) {
	return t.Host.ReadTerminal(terminalID, options), nil
}

func (t *Terminals) OnTerminalEvent(listener func(update domain.TerminalUpdate)) func() {
	return t.events.TerminalEvent.On(listener)
}

// TerminalSnapshot is the screen a view restores before it takes live output.
func (t *Terminals) TerminalSnapshot(terminalID string) *contracts.TerminalScreenSnapshot {
	return t.Host.Snapshot(terminalID)
}

// ReadTerminalOutput is a remote viewer's read: computers.TerminalReader.
func (t *Terminals) ReadTerminalOutput(ctx context.Context, terminalID string, after *float64) (*contracts.TerminalOutputRead, error) {
	return t.Host.ReadOutput(ctx, terminalID, after)
}
