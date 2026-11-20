package application

import (
	"math"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var ProjectAddNoProjectAdd = ProjectAddWorkspaceState{ProjectAdd: nil, ProjectAddSequence: 0}

var ProjectAddInputs = []string{
	"project.open", "project.add", "project.added", "project.add-finished", "project.directories", "project.path-picked",
	"view.add-project-close", "view.add-project-device", "view.add-project-path", "view.add-project-pick", "view.add-project-submit",
	"view.add-project-key", "view.add-project-accept",
}

func projectAddPatch(add *ProjectAddState) OwnPatch {
	return func(next *WorkspaceState) { next.ProjectAdd = add }
}

// TS defaults `query` to true.
func projectAddChanged(state *WorkspaceState, add ProjectAddState, query bool) SliceTransition {
	request := state.ProjectAddSequence + 1
	next := add
	next.Request, next.Suggestions, next.Selected, next.Error = request, []string{}, -1, nil
	transition := SliceTransition{Patch: func(state *WorkspaceState) {
		state.ProjectAddSequence = request
		state.ProjectAdd = &next
	}}
	if query && next.Root != "" {
		transition.Effects = []WorkspaceEffect{WorkspaceEffectProjectAddDirectories{Prefix: next.Root, ComputerID: next.ComputerID, Request: request}}
	}
	return transition
}

func submit(state *WorkspaceState) SliceTransition {
	add := state.ProjectAdd
	if add == nil || add.Saving || projectsTrim(add.Root) == "" {
		return SliceTransition{}
	}
	root := projectsTrim(add.Root)
	var start *bool
	if add.Start {
		start = new(true)
	}
	route := RouteInput(state, contracts.ProjectCommandProjectAdd{Root: root, ComputerID: &add.ComputerID, Start: start})
	if refused, ok := route.(InputRouteRefuse); ok {
		next := *add
		next.Error = &refused.Message
		return SliceTransition{Patch: projectAddPatch(&next)}
	}
	request := state.ProjectAddSequence + 1
	next := *add
	next.Request, next.Saving, next.Error, next.Suggestions, next.Selected = request, true, nil, []string{}, -1
	var effect WorkspaceEffect = WorkspaceEffectAddProject{Root: root, Request: &request, Start: start}
	if computer, ok := route.(InputRouteComputer); ok {
		effect = WorkspaceEffectComputerForward{ID: computer.Computer.ID, Inputs: computer.Inputs, ProjectAddRequest: &request}
	}
	return SliceTransition{
		Patch: func(state *WorkspaceState) {
			state.ProjectAddSequence = request
			state.ProjectAdd = &next
		},
		Effects: []WorkspaceEffect{effect},
	}
}

// A folder opened to start in lands on a fresh draft there, listed in the sidebar only once a thread starts.
func startIn(state *WorkspaceState, workspace domain.WorkspaceRecord) Step {
	return AndThen(state, ProjectOpened(state, workspace, true), func(opened *WorkspaceState) Step {
		return NewThread(opened, opened.DraftProjectID, nil)
	})
}

// A paired computer that opened a folder to start in is put on screen, where its draft now waits.
func showComputer(state *WorkspaceState, id string) Step {
	left := state.Computers.Active
	step := Step{Edits: []OwnerEdit{ShownIn(ComputersOf(state), id)}}
	if left != nil && *left != "" && *left != id {
		step.Effects = []WorkspaceEffect{WorkspaceEffectComputerForward{ID: *left, Inputs: []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: false}}}}
	}
	return step
}

// withPatch is `{ patch, ...step }`.
func withPatch(patch OwnPatch, step Step) SliceTransition {
	transition := SliceTransition(step)
	transition.Patch = patch
	return transition
}

func closedProjectAdd(next *WorkspaceState) { next.ProjectAdd = nil }

func sameRequest(add *ProjectAddState, request *float64) bool {
	return add != nil && request != nil && add.Request == *request
}

// The add-project dialog. `input` is one of the ProjectAddInputs.
func ReduceProjectAdd(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	add := state.ProjectAdd
	switch input := input.(type) {
	case contracts.ProjectCommandProjectOpen:
		start := input.Start != nil && *input.Start
		filter := state.Computers.Filter
		connected := slices.ContainsFunc(state.Computers.Paired, func(computer *PairedComputer) bool {
			return computer.Status == domain.ComputerStatusConnected
		})
		if !start && (filter == "all" || filter == "this") && !connected {
			return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectPickProject{}}}
		}
		computerID := filter
		if filter == "all" {
			computerID = "this"
		}
		return projectAddChanged(state, ProjectAddState{Request: 0, ComputerID: computerID, Root: "", Suggestions: []string{}, Selected: -1, Saving: false, Error: nil, Start: start}, false)
	case contracts.ProjectCommandProjectAdd:
		var start *bool
		if input.Start != nil && *input.Start {
			start = new(true)
		}
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAddProject{Root: input.Root, Start: start}}}
	case ProjectAddEventProjectAdded:
		if input.Start != nil && *input.Start {
			if input.Request == nil {
				return SliceTransition(startIn(state, input.Workspace))
			}
			if sameRequest(add, input.Request) {
				return withPatch(closedProjectAdd, startIn(state, input.Workspace))
			}
			return SliceTransition{}
		}
		// A folder this window's dialog added lands on a draft there, as one picked from the folder panel does.
		if sameRequest(add, input.Request) {
			return withPatch(closedProjectAdd, ProjectOpened(state, input.Workspace, false))
		}
		// Registering a folder on a served host must not move its own window away from a thread.
		opened, _ := OpenedIn(ProjectsOf(state), input.Workspace, false)
		return SliceTransition{Edits: []OwnerEdit{opened, FolderOpenedIn(NavigationOf(state), input.Workspace.Root)}, ClearsError: true}
	case ProjectAddEventProjectAddFinished:
		failed := input.Error != nil && *input.Error != ""
		if input.Request == nil {
			if failed {
				return SliceTransition{Refusal: input.Error}
			}
			return SliceTransition{}
		}
		if !sameRequest(add, input.Request) {
			if failed {
				return SliceTransition{Result: contracts.WorkspaceCommandResultFalse{Message: *input.Error}}
			}
			return SliceTransition{Result: contracts.WorkspaceCommandResultTrue{}}
		}
		if failed {
			next := *add
			next.Saving, next.Error = false, input.Error
			return SliceTransition{Patch: projectAddPatch(&next), Result: contracts.WorkspaceCommandResultFalse{Message: *input.Error}}
		}
		var shown Step
		if add.Start && add.ComputerID != "this" {
			shown = showComputer(state, add.ComputerID)
		}
		transition := withPatch(closedProjectAdd, shown)
		transition.Result = contracts.WorkspaceCommandResultTrue{}
		return transition
	case contracts.ProjectCommandViewAddProjectClose:
		return SliceTransition{Patch: closedProjectAdd}
	case contracts.ProjectCommandViewAddProjectDevice:
		if add == nil || add.Saving {
			return SliceTransition{}
		}
		if input.ComputerID != "this" && !slices.ContainsFunc(state.Computers.Paired, func(computer *PairedComputer) bool {
			return computer.ID == input.ComputerID && computer.Status == domain.ComputerStatusConnected
		}) {
			return SliceTransition{}
		}
		next := *add
		next.ComputerID, next.Root = input.ComputerID, ""
		return projectAddChanged(state, next, false)
	case contracts.ProjectCommandViewAddProjectPath:
		if add == nil || add.Saving {
			return SliceTransition{}
		}
		next := *add
		next.Root = input.Root
		return projectAddChanged(state, next, true)
	case contracts.ProjectCommandViewAddProjectPick:
		if add == nil || add.Saving || add.ComputerID != "this" {
			return SliceTransition{}
		}
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectProjectAddPick{Request: add.Request}}}
	case ProjectAddEventProjectPathPicked:
		if add == nil || add.Request != input.Request || add.Saving {
			return SliceTransition{}
		}
		if input.Error != nil && *input.Error != "" {
			next := *add
			next.Error = input.Error
			return SliceTransition{Patch: projectAddPatch(&next)}
		}
		if input.Root == nil {
			return SliceTransition{}
		}
		next := *add
		next.Root = *input.Root
		return projectAddChanged(state, next, false)
	case ProjectAddEventProjectDirectories:
		if add == nil || add.Request != input.Request || add.Saving {
			return SliceTransition{}
		}
		next := *add
		next.Suggestions, next.Selected, next.Error = input.Directories, -1, input.Error
		return SliceTransition{Patch: projectAddPatch(&next)}
	case contracts.ProjectCommandViewAddProjectSubmit:
		return submit(state)
	case contracts.ProjectCommandViewAddProjectAccept:
		if add == nil || add.Saving {
			return SliceTransition{}
		}
		index := int(input.Index)
		if float64(index) != input.Index || index < 0 || index >= len(add.Suggestions) {
			return SliceTransition{}
		}
		next := *add
		next.Root = add.Suggestions[index]
		return projectAddChanged(state, next, false)
	case contracts.ProjectCommandViewAddProjectKey:
		if add == nil {
			return SliceTransition{}
		}
		if input.Key == contracts.ProjectCommandViewAddProjectKeyKeyEscape {
			if len(add.Suggestions) > 0 {
				return projectAddChanged(state, *add, false)
			}
			return SliceTransition{Patch: closedProjectAdd}
		}
		if add.Saving {
			return SliceTransition{}
		}
		down := input.Key == contracts.ProjectCommandViewAddProjectKeyKeyArrowDown
		if down || input.Key == contracts.ProjectCommandViewAddProjectKeyKeyArrowUp {
			length := float64(len(add.Suggestions))
			if length == 0 {
				return SliceTransition{}
			}
			var selected float64
			switch {
			case add.Selected < 0 && down:
				selected = 0
			case add.Selected < 0:
				selected = length - 1
			case down:
				selected = math.Mod(add.Selected+1, length)
			default:
				selected = math.Mod(add.Selected+length-1, length)
			}
			next := *add
			next.Selected = selected
			return SliceTransition{Patch: projectAddPatch(&next)}
		}
		// A path typed out in full is submitted rather than completed to itself.
		enter := input.Key == contracts.ProjectCommandViewAddProjectKeyKeyEnter
		typed := enter && add.Selected < 0 && slices.ContainsFunc(add.Suggestions, func(root string) bool { return domain.SameRoot(root, add.Root) })
		if len(add.Suggestions) > 0 && !typed {
			return ReduceProjectAdd(state, contracts.ProjectCommandViewAddProjectAccept{Index: max(0, add.Selected)})
		}
		if enter {
			return submit(state)
		}
		return SliceTransition{}
	}
	return SliceTransition{}
}
