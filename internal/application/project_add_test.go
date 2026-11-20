package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// awProjects runs the project slices an input belongs to.
func awProjects(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	if slices.Contains(ProjectAddInputs, InputType(input)) {
		return awApply(state, ReduceProjectAdd, input)
	}
	return awApply(state, ReduceProjectCommands, input)
}

func awPaired(status domain.ComputerStatus) *PairedComputer {
	return &PairedComputer{ComputerLink: domain.ComputerLink{ID: "linux", Name: "Linux", Host: "linux", PairedAt: 1, Status: status}, State: EmptyWorkspaceState(nil)}
}

// A workspace narrowed to a connected paired computer named linux.
func awFilteredToLinux() *WorkspaceState {
	return awWorkspace(func(state *WorkspaceState) {
		state.Computers.Name, state.Computers.Filter, state.Computers.Active = "Mac", "linux", new("linux")
		state.Computers.Paired = []*PairedComputer{awPaired(domain.ComputerStatusConnected)}
	})
}

func TestProjectOpenUsesThePickerUnlessAConnectedComputerNeedsAChoice(t *testing.T) {
	for _, filter := range []string{"all", "this"} {
		for _, status := range []domain.ComputerStatus{"", domain.ComputerStatusOffline, domain.ComputerStatusConnecting, domain.ComputerStatusConnected} {
			state := awWorkspace(func(state *WorkspaceState) {
				state.Computers.Filter = filter
				if status != "" {
					state.Computers.Paired = []*PairedComputer{awPaired(status)}
				}
			})
			opened := awProjects(state, contracts.ProjectCommandProjectOpen{})
			if status == domain.ComputerStatusConnected {
				if len(opened.Effects) != 0 || opened.State.ProjectAdd == nil || opened.State.ProjectAdd.ComputerID != "this" {
					t.Fatalf("%s/%s: want the dialog on this computer, got %#v", filter, status, opened.Effects)
				}
				continue
			}
			if len(opened.Effects) != 1 || opened.State.ProjectAdd != nil {
				t.Fatalf("%s/%s: want the picker, got %#v", filter, status, opened.Effects)
			}
			if _, ok := opened.Effects[0].(WorkspaceEffectPickProject); !ok {
				t.Fatalf("%s/%s: want pick-project, got %#v", filter, status, opened.Effects[0])
			}
		}
	}
}

func TestProjectOpenNamingAPairedComputerOpensTheDialogEvenWhenItIsDown(t *testing.T) {
	for _, status := range []domain.ComputerStatus{domain.ComputerStatusOffline, domain.ComputerStatusConnecting, domain.ComputerStatusConnected} {
		state := awFilteredToLinux()
		state.Computers.Paired = []*PairedComputer{awPaired(status)}
		opened := awProjects(state, contracts.ProjectCommandProjectOpen{})
		if len(opened.Effects) != 0 || opened.State.ProjectAdd == nil || opened.State.ProjectAdd.ComputerID != "linux" {
			t.Fatalf("%s: got %#v", status, opened.State.ProjectAdd)
		}
	}
}

func TestProjectAddRepliesCannotOverwriteANewerDeviceOrPath(t *testing.T) {
	state := &[]WorkspaceTransition{awProjects(awFilteredToLinux(), contracts.ProjectCommandProjectOpen{})}[0].State
	typed := awProjects(state, contracts.ProjectCommandViewAddProjectPath{Root: "/wo"})
	state = &typed.State
	request := state.ProjectAdd.Request
	query, ok := typed.Effects[0].(WorkspaceEffectProjectAddDirectories)
	if !ok || query.Prefix != "/wo" || query.ComputerID != "linux" || query.Request != request {
		t.Fatalf("query: %#v", typed.Effects)
	}
	step := func(input WorkspaceInput) {
		next := awProjects(state, input)
		state = &next.State
	}
	step(ProjectAddEventProjectDirectories{Request: request, Directories: []string{"/work/", "/world/"}})
	step(contracts.ProjectCommandViewAddProjectKey{Key: contracts.ProjectCommandViewAddProjectKeyKeyArrowUp})
	if state.ProjectAdd.Selected != 1 {
		t.Fatalf("ArrowUp from nothing selects the last, got %v", state.ProjectAdd.Selected)
	}
	step(contracts.ProjectCommandViewAddProjectKey{Key: contracts.ProjectCommandViewAddProjectKeyKeyEnter})
	if state.ProjectAdd.Root != "/world/" || len(state.ProjectAdd.Suggestions) != 0 {
		t.Fatalf("Enter accepts the selection, got %#v", state.ProjectAdd)
	}
	step(contracts.ProjectCommandViewAddProjectDevice{ComputerID: "this"})
	if state.ProjectAdd.Root != "" {
		t.Fatalf("a new device clears the path")
	}
	before := state.ProjectAdd
	step(ProjectAddEventProjectDirectories{Request: request, Directories: []string{"/wrong/"}})
	step(ProjectAddEventProjectPathPicked{Request: request, Root: new("/wrong")})
	if state.ProjectAdd != before {
		t.Fatalf("stale replies changed the dialog")
	}
	newRequest := state.ProjectAdd.Request
	step(ProjectAddEventProjectPathPicked{Request: newRequest, Root: new("/chosen")})
	if state.ProjectAdd.Root != "/chosen" || len(state.Projects) != 0 {
		t.Fatalf("picked: %#v", state.ProjectAdd)
	}
	step(contracts.ProjectCommandViewAddProjectClose{})
	step(contracts.ProjectCommandProjectOpen{})
	reopened := state.ProjectAdd
	step(ProjectAddEventProjectAddFinished{Request: &newRequest})
	if state.ProjectAdd != reopened {
		t.Fatalf("an old request's finish closed the new dialog")
	}
}

func TestProjectAddFailureReturnsToTheSubmittingDialog(t *testing.T) {
	state := awWorkspace()
	for _, input := range []WorkspaceInput{contracts.ProjectCommandProjectOpen{Start: new(true)}, contracts.ProjectCommandViewAddProjectPath{Root: "/app"}} {
		state = &[]WorkspaceTransition{awProjects(state, input)}[0].State
	}
	queryRequest := state.ProjectAdd.Request
	submitted := awProjects(state, contracts.ProjectCommandViewAddProjectSubmit{})
	add, ok := submitted.Effects[0].(WorkspaceEffectAddProject)
	if !ok || add.Root != "/app" || add.Request == nil || *add.Request != queryRequest+1 || add.Start == nil || !*add.Start {
		t.Fatalf("submit: %#v", submitted.Effects)
	}
	failed := awProjects(&submitted.State, ProjectAddEventProjectAddFinished{Request: add.Request, Error: new("There is no folder at /app.")})
	if failed.State.ProjectAdd == nil || awText(failed.State.ProjectAdd.Error) != "There is no folder at /app." || failed.State.ProjectAdd.Saving {
		t.Fatalf("failure: %#v", failed.State.ProjectAdd)
	}
	if result, ok := failed.Result.(contracts.WorkspaceCommandResultFalse); !ok || result.Message != "There is no folder at /app." {
		t.Fatalf("result: %#v", failed.Result)
	}
	late := awProjects(&failed.State, ProjectAddEventProjectDirectories{Request: queryRequest, Directories: []string{}})
	if awText(late.State.ProjectAdd.Error) != "There is no folder at /app." {
		t.Fatalf("late autocomplete erased the submission error")
	}
}

func TestProjectAddEnterSubmitsAFullPathAndCompletesAPartialOne(t *testing.T) {
	state := awWorkspace()
	step := func(input WorkspaceInput) { state = &[]WorkspaceTransition{awProjects(state, input)}[0].State }
	step(contracts.ProjectCommandProjectOpen{Start: new(true)})
	step(contracts.ProjectCommandViewAddProjectPath{Root: "/app"})
	step(ProjectAddEventProjectDirectories{Request: state.ProjectAdd.Request, Directories: []string{"/app/", "/apple/"}})
	if entered := awProjects(state, contracts.ProjectCommandViewAddProjectKey{Key: "Enter"}); !entered.State.ProjectAdd.Saving {
		t.Fatalf("a typed-out path submits")
	}
	step(contracts.ProjectCommandViewAddProjectPath{Root: "/ap"})
	step(ProjectAddEventProjectDirectories{Request: state.ProjectAdd.Request, Directories: []string{"/app/", "/apple/"}})
	if entered := awProjects(state, contracts.ProjectCommandViewAddProjectKey{Key: "Enter"}); entered.State.ProjectAdd.Root != "/app/" {
		t.Fatalf("a partial path completes, got %q", entered.State.ProjectAdd.Root)
	}
}

func TestProjectAddStartAnsweredAfterTheDialogClosedChangesNothing(t *testing.T) {
	state := awWorkspace()
	step := func(input WorkspaceInput) { state = &[]WorkspaceTransition{awProjects(state, input)}[0].State }
	step(contracts.ProjectCommandProjectOpen{Start: new(true)})
	step(contracts.ProjectCommandViewAddProjectPath{Root: "/app"})
	step(contracts.ProjectCommandViewAddProjectSubmit{})
	request := state.ProjectAdd.Request
	step(contracts.ProjectCommandViewAddProjectClose{})
	state.Computers.Active = new("linux")
	answered := awProjects(state, ProjectAddEventProjectAdded{Workspace: domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}, Request: &request, Start: new(true)})
	if len(answered.State.Projects) != 0 || awText(answered.State.Computers.Active) != "linux" {
		t.Fatalf("a late start moved the window: %#v", answered.State.Projects)
	}
}

func TestProjectAddedForAnotherWindowLeavesThisOneWhereItWas(t *testing.T) {
	awPorted(t, func() {
		state := awWorkspace(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{awTask("open-thread")}
			state.CurrentID, state.DraftProjectID = new("open-thread"), new("kept")
			state.Projects = []*domain.Project{{ID: "kept", Root: "/kept"}}
		})
		added := awProjects(state, ProjectAddEventProjectAdded{Workspace: domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}})
		if awText(added.State.CurrentID) != "open-thread" || awText(added.State.DraftProjectID) != "kept" {
			t.Fatalf("the window moved")
		}
		if !slices.ContainsFunc(added.State.Projects, func(project *domain.Project) bool { return project.Root == "/app" && project.Unlisted == nil }) {
			t.Fatalf("the folder was not listed: %#v", added.State.Projects)
		}
	})
}

func TestProjectAddStartLandsOnADraftInAnUnlistedFolder(t *testing.T) {
	awPorted(t, func() {
		state := awWorkspace()
		step := func(input WorkspaceInput) { state = &[]WorkspaceTransition{awProjects(state, input)}[0].State }
		step(contracts.ProjectCommandProjectOpen{Start: new(true)})
		step(contracts.ProjectCommandViewAddProjectPath{Root: "/app"})
		step(contracts.ProjectCommandViewAddProjectSubmit{})
		request := state.ProjectAdd.Request
		step(ProjectAddEventProjectAdded{Workspace: domain.WorkspaceRecord{ID: "ws", Kind: "project", Root: "/app"}, Request: &request, Start: new(true)})
		if state.ProjectAdd != nil || state.CurrentID != nil || len(state.Projects) != 1 || awText(state.DraftProjectID) != state.Projects[0].ID {
			t.Fatalf("start: %#v", state.Projects)
		}
		if state.Projects[0].Unlisted == nil || !*state.Projects[0].Unlisted {
			t.Fatalf("a folder opened to start in is unlisted")
		}
	})
}
