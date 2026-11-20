package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// Schedules as one input leaves them. Only the functions here make one.
type automationsChange = AutomationsEdit

func AutomationsOf(state *WorkspaceState) AutomationsEdit {
	return AutomationsEdit{View: AutomationsState{Automations: state.Automations, AutomationEdits: state.AutomationEdits}}
}

// AutomationsForgottenIn: threads gone for good have no schedule left listed.
func AutomationsForgottenIn(edit AutomationsEdit, removed jsonx.Set[string]) AutomationsEdit {
	edit.View.Automations = WithoutMatching(edit.View.Automations, func(automation *domain.AutomationView) bool { return removed.Has(automation.TaskID) })
	return edit
}
