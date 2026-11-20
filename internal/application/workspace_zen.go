package application

// ZenAllowed: a draft or an empty thread has no conversation to focus on, so only a thread with
// messages may enter zen mode.
func ZenAllowed(state *WorkspaceState) bool {
	shown := state
	if computer := SelectedComputer(state); computer != nil && computer.State != nil {
		shown = computer.State
	}
	if shown.CurrentID == nil || *shown.CurrentID == "" {
		return false
	}
	for _, item := range shown.Threads {
		if item.ID == *shown.CurrentID {
			return len(item.Messages) > 0
		}
	}
	return false
}

func shownThread(state *WorkspaceState) string {
	remote := ""
	if computer := SelectedComputer(state); computer != nil && computer.State != nil && computer.State.CurrentID != nil {
		remote = *computer.State.CurrentID
	}
	return deref(state.Computers.Active) + "\x00" + deref(state.CurrentID) + "\x00" + remote
}

func dockOpen(state *WorkspaceState) bool {
	if computer := SelectedComputer(state); computer != nil && computer.State != nil {
		_, dock := FrontDock(computer.State)
		return dock.Open
	}
	_, dock := FrontDock(state)
	return dock.Open
}

// SettledZen: zen mode belongs to the thread it was entered on, so moving to another thread ends it.
// Opening anything it hides ends it too.
func SettledZen(before, after *WorkspaceState) *WorkspaceState {
	if !after.Zen || !before.Zen {
		return after
	}
	left := shownThread(before) != shownThread(after)
	revealed := (!before.SidebarOpen && after.SidebarOpen) ||
		(!before.SessionPanelOpen && after.SessionPanelOpen) ||
		(!dockOpen(before) && dockOpen(after))
	if left || revealed || !ZenAllowed(after) {
		next := *after
		next.Zen = false
		return &next
	}
	return after
}
