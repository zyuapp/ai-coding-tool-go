package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// TerminalInputs are the input types ReduceTerminal takes.
var TerminalInputs = []string{"terminal.open", "terminal.select", "terminal.close", "terminal.restart", "terminal.input", "terminal.resize", "terminal.updated"}

// ReduceTerminal takes an input whose type is one of TerminalInputs. Shells own no state of their
// own: they live in docks, so it never patches.
func ReduceTerminal(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.TerminalCommandTerminalOpen:
		owner := DockOwner(state, nil)
		cwd := input.Cwd
		if cwd == nil {
			cwd = CurrentFolder(state)
		}
		if cwd == nil || *cwd == "" {
			return SliceTransition{Refusal: new(TerminalFolderError)}
		}
		terminal := &domain.TerminalSession{ID: NewID(), Title: domain.TerminalTitle(*cwd), Cwd: *cwd, TaskID: state.CurrentID, Status: domain.TerminalSessionStatusRunning}
		terminals := append(slices.Clip(DockFor(state, owner).Terminals), terminal)
		opened := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Open = true
			dock.Tab = terminal.ID
			dock.Terminals = terminals
			dock.TerminalID = new(terminal.ID)
		})
		focused, effects := FocusDockTabIn(opened, owner, terminal.ID)
		return SliceTransition{ClearsError: true, Edits: []OwnerEdit{focused}, Effects: slices.Concat([]WorkspaceEffect{WorkspaceEffectTerminalStart{TerminalID: terminal.ID, Cwd: *cwd}}, effects)}

	// A shell that is asked for is a shell to type in, so it takes the keyboard as a side chat does.
	case contracts.TerminalCommandTerminalSelect:
		owner, ok := OwnerOfTerminal(state, input.TerminalID)
		if !ok {
			return SliceTransition{}
		}
		terminalID := input.TerminalID
		shown := EditDock(ShowDockTabIn(DocksOf(state), owner, terminalID), owner, func(dock *ThreadDock) { dock.TerminalID = new(terminalID) })
		focused, effects := FocusDockTabIn(shown, owner, terminalID)
		return SliceTransition{Edits: []OwnerEdit{focused}, Effects: effects}

	case contracts.TerminalCommandTerminalClose:
		owner, ok := OwnerOfTerminal(state, input.TerminalID)
		if !ok {
			return SliceTransition{}
		}
		dock := DockFor(state, owner)
		closing := input.TerminalID
		index := slices.IndexFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == closing })
		tab := dock.Tab
		if dock.Tab == closing {
			tab = DockTabAfterClosing(state, owner, closing)
		}
		terminals := slices.DeleteFunc(slices.Clone(dock.Terminals), func(terminal *domain.TerminalSession) bool { return terminal.ID == closing })
		var next *string
		if dock.TerminalID != nil && *dock.TerminalID == closing {
			if index-1 >= 0 && index-1 < len(terminals) {
				next = new(terminals[index-1].ID)
			} else if index >= 0 && index < len(terminals) {
				next = new(terminals[index].ID)
			}
		} else if dock.TerminalID != nil && slices.ContainsFunc(terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == *dock.TerminalID }) {
			next = new(*dock.TerminalID)
		}
		return SliceTransition{
			Edits: []OwnerEdit{EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
				dock.Terminals = terminals
				dock.TerminalID = next
				dock.Tab = tab
			})},
			Effects: []WorkspaceEffect{WorkspaceEffectTerminalClose{TerminalID: closing}},
		}

	case contracts.TerminalCommandTerminalRestart:
		owner, ok := OwnerOfTerminal(state, input.TerminalID)
		if !ok {
			return SliceTransition{}
		}
		dock := DockFor(state, owner)
		index := slices.IndexFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == input.TerminalID })
		old := dock.Terminals[index]
		terminal := &domain.TerminalSession{ID: NewID(), Title: domain.TerminalTitle(old.Cwd), Cwd: old.Cwd, TaskID: old.TaskID, Status: domain.TerminalSessionStatusRunning}
		terminals := slices.Clone(dock.Terminals)
		terminals[index] = terminal
		replaced := EditDock(DocksOf(state), owner, func(dock *ThreadDock) {
			dock.Terminals = terminals
			dock.TerminalID = new(terminal.ID)
		})
		focused, effects := FocusDockTabIn(ShowDockTabIn(replaced, owner, terminal.ID), owner, terminal.ID)
		return SliceTransition{
			ClearsError: true,
			Edits:       []OwnerEdit{focused},
			Effects:     slices.Concat([]WorkspaceEffect{WorkspaceEffectTerminalClose{TerminalID: old.ID}, WorkspaceEffectTerminalStart{TerminalID: terminal.ID, Cwd: old.Cwd}}, effects),
		}

	// Keystrokes and the size the shell believes it has. Neither is state, so only the effect happens.
	case contracts.TerminalCommandTerminalInput:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectTerminalWrite{TerminalID: input.TerminalID, Data: input.Data}}}

	case contracts.TerminalCommandTerminalResize:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectTerminalResize{TerminalID: input.TerminalID, Cols: input.Cols, Rows: input.Rows}}}

	case TerminalEvent:
		update := input.Update
		owner, ok := OwnerOfTerminal(state, update.TerminalID)
		if !ok {
			return SliceTransition{}
		}
		held := DockFor(state, owner).Terminals
		terminals := make([]*domain.TerminalSession, len(held))
		for index, terminal := range held {
			if terminal.ID != update.TerminalID {
				terminals[index] = terminal
				continue
			}
			patched := *terminal
			if update.Title != nil {
				patched.Title = *update.Title
			}
			if update.Status != nil {
				patched.Status = domain.TerminalSessionStatus(*update.Status)
			}
			if update.ExitCode != nil {
				patched.ExitCode = update.ExitCode
			}
			if update.Error != nil {
				patched.Error = update.Error
			}
			terminals[index] = &patched
		}
		return SliceTransition{Edits: []OwnerEdit{EditDock(DocksOf(state), owner, func(dock *ThreadDock) { dock.Terminals = terminals })}}
	}
	return SliceTransition{}
}
