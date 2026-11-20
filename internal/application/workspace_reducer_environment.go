package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"maps"
	"slices"
)

// gitChange is the TypeScript class behind GitEdit: what Git and the forge last said, as one input leaves it.
type gitChange = GitEdit

func GitOf(state *WorkspaceState) GitEdit {
	return GitEdit{View: OwnFields[GitState](state)}
}

// EnvironmentReadIn is what Git just said about a checkout, kept with the answers about the checkouts
// live (its projects and worktrees) still has, as RetainedEnvironments keeps them.
func EnvironmentReadIn(edit GitEdit, live *WorkspaceState, workspaceID string, result contracts.ChangedFilesResult) GitEdit {
	retained := *live
	retained.Environments = edit.View.Environments
	edit.View.Environments = RetainedEnvironments(&retained, workspaceID, result)
	return edit
}

// EnvironmentForgottenIn is a checkout that is gone, its answer forgotten with it.
func EnvironmentForgottenIn(edit GitEdit, workspaceID string) GitEdit {
	environments := WithoutEnvironment(edit.View.Environments, workspaceID)
	if jsonx.Same(environments, edit.View.Environments) {
		return edit
	}
	edit.View.Environments = environments
	return edit
}

// SameChangedFiles compares two answers; a nil left is TypeScript's null.
func SameChangedFiles(left, right contracts.ChangedFilesResult) bool {
	switch left := left.(type) {
	case contracts.ChangedFilesResultAvailable:
		right, ok := right.(contracts.ChangedFilesResultAvailable)
		return ok && samePointee(left.Branch, right.Branch) && samePointee(left.Baseline, right.Baseline) &&
			left.Additions == right.Additions && left.Deletions == right.Deletions && SameStrings(left.Files, right.Files)
	case contracts.DiffSummaryResultUnavailable:
		right, ok := right.(contracts.DiffSummaryResultUnavailable)
		return ok && left.Reason == right.Reason
	case contracts.DiffSummaryResultUnknown:
		right, ok := right.(contracts.DiffSummaryResultUnknown)
		return ok && left.WorkspaceID == right.WorkspaceID
	case contracts.BranchesResultError:
		right, ok := right.(contracts.BranchesResultError)
		return ok && left.Message == right.Message
	}
	return false
}

// CurrentWorkspaceID is the checkout the thread in front works in: what Git is read from.
func CurrentWorkspaceID(state *WorkspaceState) *string {
	if current := findThread(state.Threads, state.CurrentID); current != nil {
		if id, ok := ThreadWorkspaceID(state, current); ok {
			return &id
		}
		return nil
	}
	if state.DraftProjectID == nil || *state.DraftProjectID == "" {
		return nil
	}
	for _, project := range state.Projects {
		if project.ID == *state.DraftProjectID {
			return project.WorkspaceID
		}
	}
	return nil
}

// SubjectWorkspaceID is the checkout a dock's review reads: the thread tab it is about, else the thread in front.
func SubjectWorkspaceID(state *WorkspaceState, owner string) *string {
	subject := DockSubject(state, owner, DiffPanel)
	if subject == nil || *subject == "" {
		return CurrentWorkspaceID(state)
	}
	if id, ok := ThreadWorkspaceID(state, findThread(state.Threads, subject)); ok {
		return &id
	}
	return nil
}

// RefreshThreadEnvironment asks Git about one thread's checkout, naming the run its answer follows.
func RefreshThreadEnvironment(state *WorkspaceState, taskID string) []WorkspaceEffect {
	workspaceID, ok := ThreadWorkspaceID(state, findThread(state.Threads, &taskID))
	if !ok || workspaceID == "" {
		return []WorkspaceEffect{}
	}
	effect := WorkspaceEffectRefreshEnvironment{WorkspaceID: workspaceID, TaskID: &taskID}
	if runID := state.LastRunIDs[taskID]; runID != "" {
		effect.RunID = &runID
	}
	return []WorkspaceEffect{effect}
}

// RefreshEnvironment asks Git about the checkout the thread in front works in, and those of the threads open as its tabs.
func RefreshEnvironment(state *WorkspaceState) []WorkspaceEffect {
	tabs := []WorkspaceEffect{}
	for _, taskID := range DockFor(state, DockOwner(state, nil)).ThreadTabs {
		tabs = append(tabs, RefreshThreadEnvironment(state, taskID)...)
	}
	if current := findThread(state.Threads, state.CurrentID); current != nil {
		return slices.Concat(RefreshThreadEnvironment(state, current.ID), tabs)
	}
	if workspaceID := CurrentWorkspaceID(state); workspaceID != nil && *workspaceID != "" {
		return slices.Concat([]WorkspaceEffect{WorkspaceEffectRefreshEnvironment{WorkspaceID: *workspaceID}}, tabs)
	}
	return tabs
}

// EnvironmentFor is what Git last said about a checkout, or nil while none of it has been read yet.
func EnvironmentFor(state *WorkspaceState, workspaceID *string) contracts.ChangedFilesResult {
	if workspaceID == nil || *workspaceID == "" {
		return nil
	}
	return state.Environments[*workspaceID]
}

// WithoutEnvironment forgets one checkout's answer; the map is returned as is when it holds none.
func WithoutEnvironment(environments map[string]contracts.ChangedFilesResult, workspaceID string) map[string]contracts.ChangedFilesResult {
	if _, held := environments[workspaceID]; !held {
		return environments
	}
	rest := maps.Clone(environments)
	delete(rest, workspaceID)
	return rest
}

// RetainedEnvironments is the answers worth keeping: one per checkout state's projects and worktrees
// still have, plus the one just read, from state's environments.
func RetainedEnvironments(state *WorkspaceState, workspaceID string, result contracts.ChangedFilesResult) map[string]contracts.ChangedFilesResult {
	live := map[string]bool{workspaceID: true}
	for _, project := range state.Projects {
		if project.WorkspaceID != nil {
			live[*project.WorkspaceID] = true
		}
	}
	for _, worktree := range state.Worktrees {
		live[worktree.WorkspaceID] = true
	}
	kept := map[string]contracts.ChangedFilesResult{workspaceID: result}
	for id, answer := range state.Environments {
		if id != workspaceID && live[id] {
			kept[id] = answer
		}
	}
	return kept
}
