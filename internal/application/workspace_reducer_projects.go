package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var ProjectInputs = []string{
	"project.opened", "project.edit", "project.registered", "project.register-failed", "view.edit-project",
	"project.remove",
}

// The project folders slice. `input` is one of the ProjectInputs.
func ReduceProjectCommands(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case ProjectInput:
		return ReduceProjects(state, input)

	case contracts.ProjectCommandProjectRemove:
		// Projects are removed from Settings, so the refusal is shown on that page.
		refused := func(message string) SliceTransition {
			return SliceTransition{Error: &SliceError{Message: &message, Page: new(domain.SettingsSectionProjects)}, Refusal: &message}
		}
		inProject := func(thread *domain.Thread) bool {
			return thread.ProjectID != nil && *thread.ProjectID == input.ProjectID
		}
		if slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool {
			return inProject(thread) && state.ActiveRuns[thread.ID] != nil
		}) {
			return refused(RunningProjectError)
		}
		if slices.ContainsFunc(state.Worktrees, func(worktree *domain.Worktree) bool { return worktree.ProjectID == input.ProjectID }) {
			return refused(ProjectWorktreesError)
		}
		leaving := []string{}
		for _, thread := range state.Threads {
			if inProject(thread) {
				leaving = append(leaving, thread.ID)
			}
		}
		effects := RetireAutomations(state, leaving)
		var root *string
		if project := findProject(state.Projects, input.ProjectID); project != nil {
			root = &project.Root
		}
		onScreen := false
		if state.CurrentID != nil {
			if current := threadByID(state.Threads, *state.CurrentID); current != nil {
				onScreen = inProject(current)
			}
		}
		projects := slices.DeleteFunc(slices.Clone(state.Projects), func(item *domain.Project) bool { return item.ID == input.ProjectID })
		projectEdit := state.ProjectEdit
		if projectEdit != nil && projectEdit.ProjectID == input.ProjectID {
			projectEdit = nil
		}
		return SliceTransition{
			Patch: func(next *WorkspaceState) { next.Projects, next.ProjectEdit = projects, projectEdit },
			Edits: []OwnerEdit{
				ProjectRemovedIn(ThreadsOf(state), input.ProjectID, Now),
				ProjectLeftIn(NavigationOf(state), input.ProjectID, root, onScreen),
			},
			Effects:    effects,
			ClosesMenu: true,
			Error:      &SliceError{Message: nil, Page: nil},
		}
	}
	return SliceTransition{}
}
