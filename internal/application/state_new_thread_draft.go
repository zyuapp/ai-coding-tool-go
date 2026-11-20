package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialNewThreadDraftState() NewThreadDraftState {
	return NewThreadDraftState{
		DraftProjectID:  nil,
		DraftPolicy:     domain.ExecutionPolicyConfirm,
		DraftBranch:     nil,
		DraftWorktree:   false,
		DraftWorktreeID: nil,
		DraftRole:       nil,
		DraftEngine:     domain.DefaultEngine,
		DraftModel:      domain.DefaultModel,
		DraftEffort:     domain.DefaultEffort,
		DraftFastMode:   false,
	}
}

func DraftOf(state *WorkspaceState) domain.AgentDraft {
	return domain.AgentDraft{Engine: state.DraftEngine, Model: state.DraftModel, Effort: state.DraftEffort, Policy: state.DraftPolicy, FastMode: state.DraftFastMode}
}
