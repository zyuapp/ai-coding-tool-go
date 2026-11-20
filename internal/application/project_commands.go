package application

import (
	"slices"
	"strings"
	"unicode"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The sidebar's folders as one input leaves them. Only the functions here make one.
func projectsChange(view ProjectsState) ProjectsEdit {
	return ProjectsEdit{View: view}
}

func ProjectsOf(state *WorkspaceState) ProjectsEdit {
	return projectsChange(ProjectsState{Projects: state.Projects, ProjectEdit: state.ProjectEdit})
}

// A folder just opened, listed under the project it already is or a new one, and that project's id.
// A project that was moved no longer goes by the id its folder makes, so its folder finds it. Opening
// a folder outright lists it; opening it only to start a thread leaves a listed one listed.
func OpenedIn(edit ProjectsEdit, workspace domain.WorkspaceRecord, unlisted bool) (ProjectsEdit, string) {
	listed := edit.View.Projects
	legacy := domain.LegacyProjectID(workspace.Root)
	index := slices.IndexFunc(listed, func(project *domain.Project) bool {
		return project.ID == legacy || domain.SameRoot(project.Root, workspace.Root)
	})
	var projects []*domain.Project
	id := legacy
	if index >= 0 {
		id = listed[index].ID
		projects = make([]*domain.Project, len(listed))
		for i, project := range listed {
			if project.ID != id {
				projects[i] = project
				continue
			}
			opened := *project
			opened.Root, opened.WorkspaceID = workspace.Root, &workspace.ID
			if !(unlisted && project.Unlisted != nil && *project.Unlisted) {
				opened.Unlisted = nil
			} else {
				opened.Unlisted = new(true)
			}
			projects[i] = &opened
		}
	} else {
		created := &domain.Project{ID: id, Root: workspace.Root, WorkspaceID: &workspace.ID, SortIndex: new(NextProjectSortIndex(listed))}
		if unlisted {
			created.Unlisted = new(true)
		}
		projects = append([]*domain.Project{created}, listed...)
	}
	view := edit.View
	view.Projects = projects
	return projectsChange(view), id
}

// The folder editor, closed.
func EditorClosedIn(edit ProjectsEdit) ProjectsEdit {
	next := edit.View
	next.ProjectEdit = nil
	return projectsChange(next)
}

// A project with no workspace of its own, adopting the one just opened for its folder.
func AdoptedIn(edit ProjectsEdit, projectID string, workspaceID string) ProjectsEdit {
	projects := make([]*domain.Project, len(edit.View.Projects))
	for i, item := range edit.View.Projects {
		if item.ID != projectID {
			projects[i] = item
			continue
		}
		adopted := *item
		adopted.WorkspaceID = &workspaceID
		projects[i] = &adopted
	}
	view := edit.View
	view.Projects = projects
	return projectsChange(view)
}

// A folder opened, with the window on a fresh draft there.
func ProjectOpened(state *WorkspaceState, workspace domain.WorkspaceRecord, unlisted bool) Step {
	projects, id := OpenedIn(ProjectsOf(state), workspace, unlisted)
	return Step{Edits: []OwnerEdit{projects, ProjectOpenedIn(NavigationOf(state), id, workspace.Root)}, ClearsError: true}
}

func findProject(projects []*domain.Project, id string) *domain.Project {
	if index := slices.IndexFunc(projects, func(item *domain.Project) bool { return item.ID == id }); index >= 0 {
		return projects[index]
	}
	return nil
}

// `input` is a ProjectInput: project.opened, project.edit, project.registered,
// project.register-failed or view.edit-project.
func ReduceProjects(state *WorkspaceState, input ProjectInput) SliceTransition {
	switch input := input.(type) {
	case ProjectEventProjectOpened:
		return SliceTransition(ProjectOpened(state, input.Workspace, input.Unlisted != nil && *input.Unlisted))

	case contracts.ProjectCommandProjectEdit:
		project := findProject(state.Projects, input.ProjectID)
		if project == nil {
			return SliceTransition{}
		}
		root := ""
		if input.Root != nil {
			root = projectsTrim(*input.Root)
		}
		// Only a folder that differs from the one the project already has is worth opening again.
		if root == "" || domain.SameRoot(root, project.Root) {
			projects := NameProject(state.Projects, project.ID, input.Name)
			return SliceTransition{
				Patch:       func(next *WorkspaceState) { next.Projects, next.ProjectEdit = projects, nil },
				ClosesMenu:  true,
				ClearsError: true,
			}
		}
		edit := &ProjectEdit{ProjectID: project.ID, Name: input.Name, Saving: true, Error: nil}
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.ProjectEdit = edit },
			Effects: []WorkspaceEffect{WorkspaceEffectRegisterProject{ProjectID: project.ID, Root: root}},
		}

	case ProjectEventProjectRegistered:
		project := findProject(state.Projects, input.ProjectID)
		if project == nil {
			return SliceTransition{Patch: func(next *WorkspaceState) { next.ProjectEdit = nil }}
		}
		moved := make([]*domain.Project, len(state.Projects))
		for i, item := range state.Projects {
			if item.ID != project.ID {
				moved[i] = item
				continue
			}
			next := *item
			next.Root, next.WorkspaceID = input.Workspace.Root, &input.Workspace.ID
			moved[i] = &next
		}
		// A name typed beside the folder lands with it, so a directory that cannot be opened keeps both.
		var pendingName jsonx.Opt[string]
		if state.ProjectEdit != nil && state.ProjectEdit.ProjectID == project.ID {
			pendingName = state.ProjectEdit.Name
		}
		projects := NameProject(moved, project.ID, pendingName)
		transition := SliceTransition{
			Patch:       func(next *WorkspaceState) { next.Projects, next.ProjectEdit = projects, nil },
			ClosesMenu:  true,
			ClearsError: true,
		}
		if state.LastFolder != nil && *state.LastFolder != "" && domain.SameRoot(*state.LastFolder, project.Root) {
			transition.Edits = []OwnerEdit{FolderMovedIn(NavigationOf(state), input.Workspace.Root)}
		}
		return transition

	case ProjectEventProjectRegisterFailed:
		if state.ProjectEdit == nil || state.ProjectEdit.ProjectID != input.ProjectID {
			return SliceTransition{Refusal: &input.Message}
		}
		edit := *state.ProjectEdit
		edit.Saving, edit.Error = false, &input.Message
		return SliceTransition{
			Patch:  func(next *WorkspaceState) { next.ProjectEdit = &edit },
			Result: contracts.WorkspaceCommandResultFalse{Message: input.Message},
		}

	case contracts.ViewCommandViewEditProject:
		if input.ProjectID == nil || *input.ProjectID == "" {
			return SliceTransition{Patch: func(next *WorkspaceState) { next.ProjectEdit = nil }}
		}
		if findProject(state.Projects, *input.ProjectID) == nil {
			return SliceTransition{}
		}
		edit := &ProjectEdit{ProjectID: *input.ProjectID, Saving: false, Error: nil}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ProjectEdit = edit }, ClosesMenu: true}
	}
	return SliceTransition{}
}

// A project opened only to start a thread joins the sidebar once a thread starts there, and is let
// go once the draft moves elsewhere without one.
func SettledUnlisted(before *WorkspaceState, state *WorkspaceState) *WorkspaceState {
	if jsonx.Same(before.Threads, state.Threads) && jsonx.Same(before.Projects, state.Projects) && sameOptionalText(before.DraftProjectID, state.DraftProjectID) {
		return state
	}
	isUnlisted := func(project *domain.Project) bool { return project.Unlisted != nil && *project.Unlisted }
	if !slices.ContainsFunc(state.Projects, isUnlisted) {
		return state
	}
	listed := map[string]bool{}
	dropped := map[string]bool{}
	for _, project := range state.Projects {
		if !isUnlisted(project) {
			continue
		}
		if slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool { return thread.ProjectID != nil && *thread.ProjectID == project.ID }) {
			listed[project.ID] = true
		}
	}
	for _, project := range state.Projects {
		if isUnlisted(project) && !listed[project.ID] && (state.DraftProjectID == nil || project.ID != *state.DraftProjectID) {
			dropped[project.ID] = true
		}
	}
	if len(listed) == 0 && len(dropped) == 0 {
		return state
	}
	projects := make([]*domain.Project, 0, len(state.Projects))
	for _, project := range state.Projects {
		switch {
		case dropped[project.ID]:
		case !listed[project.ID]:
			projects = append(projects, project)
		default:
			kept := *project
			kept.Unlisted = nil
			projects = append(projects, &kept)
		}
	}
	next := *state
	next.Projects = projects
	if len(dropped) == 0 {
		return &next
	}
	// The last folder comes back listed on launch, so one let go stops being it.
	if state.LastFolder != nil && slices.ContainsFunc(state.Projects, func(project *domain.Project) bool {
		return dropped[project.ID] && domain.SameRoot(project.Root, *state.LastFolder)
	}) {
		next.LastFolder = nil
	}
	return &next
}

// projectsTrim is String.prototype.trim.
func projectsTrim(text string) string {
	return strings.TrimFunc(text, func(r rune) bool { return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r) })
}
