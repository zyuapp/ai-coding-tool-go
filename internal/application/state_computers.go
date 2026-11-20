package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// How many coordinators gone from here are remembered for threads elsewhere that may still work under them.
const MaxDiscardedLeads = 50

var NoComputers = ComputersState{Found: []*domain.DiscoveredComputer{}, Paired: []*PairedComputer{}, Filter: "all", DiscardedLeads: []string{}}

func InitialComputersState() ComputersSliceState {
	return ComputersSliceState{Computers: NoComputers}
}

// The paired computer whose thread is on screen, while its line is up. One whose line is down shows nothing and takes nothing.
func ActiveComputer(state *WorkspaceState) *PairedComputer {
	computer := pairedNamed(state.Computers.Paired, state.Computers.Active)
	if computer == nil || computer.Status != domain.ComputerStatusConnected {
		return nil
	}
	return computer
}

// findPaired is the paired computer `id` names, or nil.
func pairedNamed(paired []*PairedComputer, id *string) *PairedComputer {
	if id == nil {
		return nil
	}
	for _, computer := range paired {
		if computer.ID == *id {
			return computer
		}
	}
	return nil
}

// `id` while it names a paired computer, else nil.
func PairedID(computers *ComputersState, id *string) *string {
	if pairedNamed(computers.Paired, id) == nil {
		return nil
	}
	return id
}

// The selected host may stay on screen offline while its terminal waits to reconnect.
func SelectedComputer(state *WorkspaceState) *PairedComputer {
	computer := pairedNamed(state.Computers.Paired, state.Computers.Active)
	if computer == nil {
		return nil
	}
	if computer.Status == domain.ComputerStatusConnected || terminalInFront(computer.State) {
		return computer
	}
	return nil
}

// Whether a state's dock in front is open on one of its terminals.
func terminalInFront(state *WorkspaceState) bool {
	if state == nil {
		return false
	}
	_, dock := FrontDock(state)
	return dock.Open && slices.ContainsFunc(dock.Terminals, func(terminal *domain.TerminalSession) bool { return terminal.ID == dock.Tab })
}
