package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialAutomationsState() AutomationsState {
	return AutomationsState{Automations: []*domain.AutomationView{}, AutomationEdits: map[string]*domain.AutomationEdit{}}
}
