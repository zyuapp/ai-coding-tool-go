package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialDocksState() DocksState {
	return DocksState{OverviewFolds: map[string]*domain.OverviewFolds{}, DockFocus: nil, Docks: map[string]*ThreadDock{}, Diffs: map[string]*DiffState{}, KeyboardTab: nil}
}
