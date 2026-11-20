package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// cmpPorted runs `body`, skipping the test while it reaches a function another group has yet to port.
func cmpPorted(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if text := fmt.Sprint(recovered); strings.Contains(text, "unported: ") {
				t.Skipf("blocked: %s", text)
			}
			panic(recovered)
		}
	}()
	body()
}

// cmpTask is a thread as the TS fixtures' task() makes one.
func cmpTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title = id, id
	thread.Engine = domain.AgentEngineClaude
	thread.ExecutionPolicy = domain.ExecutionPolicyConfirm
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt, thread.RankedAt = 1, 1
	for _, edit := range edits {
		edit(thread)
	}
	return thread
}

func cmpWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := *EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(&state)
	}
	return &state
}

func cmpLink(id string, edits ...func(*domain.ComputerLink)) domain.ComputerLink {
	link := domain.ComputerLink{ID: id, Name: id, Host: id + ".tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1}
	for _, edit := range edits {
		edit(&link)
	}
	return link
}

func cmpPaired(id string, remote *WorkspaceState, edits ...func(*domain.ComputerLink)) *PairedComputer {
	return &PairedComputer{ComputerLink: cmpLink(id, edits...), State: remote}
}

func cmpWithComputers(state *WorkspaceState, paired []*PairedComputer, edits ...func(*ComputersState)) *WorkspaceState {
	next := *state
	next.Computers.Name, next.Computers.Paired = "This Mac", paired
	for _, edit := range edits {
		edit(&next.Computers)
	}
	return &next
}

func cmpOnScreen(id string) func(*ComputersState) {
	return func(computers *ComputersState) { computers.Active = &id }
}

func cmpStatus(status domain.ComputerStatus, problem ...string) func(*domain.ComputerLink) {
	return func(link *domain.ComputerLink) {
		link.Status = status
		if len(problem) > 0 {
			link.Error = &problem[0]
		}
	}
}

// A paired computer's workspace: one project, and one thread in it.
func cmpRemoteState() *WorkspaceState {
	return cmpWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "remote-project", Root: "/linux/app", WorkspaceID: new("ws-remote")}}
		state.Threads = []*domain.Thread{cmpTask("remote-thread", func(thread *domain.Thread) {
			thread.ProjectID, thread.Title = new("remote-project"), "Remote work"
		})}
		state.CurrentID = new("remote-thread")
		state.Theme = "catppuccin-latte"
	})
}

// A command decoded from its JSON, as the window would send it.
func cmpCommand(t *testing.T, text string) contracts.AppCommand {
	t.Helper()
	var command contracts.AppCommand
	if err := jsonx.Unmarshal([]byte(text), &command); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}
	return command
}

func cmpJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(data)
}

func cmpSameJSON(t *testing.T, got, want any, label string) {
	t.Helper()
	if a, b := cmpJSON(t, got), cmpJSON(t, want); a != b {
		t.Errorf("%s:\n got %s\nwant %s", label, a, b)
	}
}

func TestRoutesStayHereWithNoComputerPaired(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWorkspace(func(state *WorkspaceState) { state.Threads = []*domain.Thread{cmpTask("local")} })
		for _, id := range []string{"local", "remote-thread"} {
			cmpSameJSON(t, RouteInput(state, contracts.TaskCommandTaskSelect{TaskID: id}), local, id)
		}
	})
}

func TestSelectingAPairedComputersThreadPutsItOnScreen(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) { state.Threads = []*domain.Thread{cmpTask("local")} }), []*PairedComputer{cmpPaired("linux", cmpRemoteState())})
		route, ok := RouteInput(state, contracts.TaskCommandTaskSelect{TaskID: "remote-thread"}).(InputRouteComputer)
		if !ok || route.Computer.ID != "linux" || route.Select == nil || !*route.Select {
			t.Fatalf("route = %#v", route)
		}
		cmpSameJSON(t, route.Inputs, []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: state.Focused}, contracts.TaskCommandTaskSelect{TaskID: "remote-thread"}}, "inputs")
	})
}

func TestPanelsAndLocalPathsAreRefusedForAThreadElsewhere(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", cmpRemoteState())}, cmpOnScreen("linux"))
		cmpSameJSON(t, RouteInput(state, cmpCommand(t, `{"type":"browser.new-tab"}`)), InputRouteRefuse{Message: PanelElsewhere}, "browser")
		cmpSameJSON(t, RouteInput(state, contracts.FileCommandFileOpen{Path: "src/app.ts"}), InputRouteRefuse{Message: FilesElsewhere}, "file")
		cmpSameJSON(t, RouteInput(state, contracts.ExternalAppCommandAppOpenFolder{AppID: "cursor"}), InputRouteRefuse{Message: FilesElsewhere}, "folder")
		send := contracts.TaskCommandTaskSend{Attachments: []*domain.RunAttachment{{Path: "/tmp/shot.png"}}}
		cmpSameJSON(t, RouteInput(state, send), InputRouteRefuse{Message: AttachmentsElsewhere}, "attachments")
		route, ok := RouteInput(state, cmpCommand(t, `{"type":"terminal.open"}`)).(InputRouteComputer)
		if !ok || route.Computer.ID != "linux" {
			t.Errorf("terminal.open = %#v", route)
		}
	})
}

func TestTheWindowsOwnAffairsStayHere(t *testing.T) {
	cmpPorted(t, func() {
		remote := cmpRemoteState()
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{cmpPaired("linux", remote)}, cmpOnScreen("linux"))
		inputs := []WorkspaceInput{
			cmpCommand(t, `{"type":"view.set-theme","theme":"x"}`),
			cmpCommand(t, `{"type":"view.set-sidebar-open","open":false}`),
			cmpCommand(t, `{"type":"engine.read"}`),
			contracts.ComputerCommandComputersFilter{Filter: "this"},
			ComputerEventComputerState{ID: "linux", State: *remote},
		}
		for _, input := range inputs {
			cmpSameJSON(t, RouteInput(state, input), local, InputType(input))
		}
		if _, ok := RouteInput(state, cmpCommand(t, `{"type":"run.cancel"}`)).(InputRouteComputer); !ok {
			t.Error("a run control about the thread on screen goes to its computer")
		}
		cmpSameJSON(t, RouteInput(state, contracts.TaskCommandTaskNew{}), local, "a new thread with no folder named starts here")
		inProject, ok := RouteInput(state, contracts.TaskCommandTaskNew{ProjectID: new("remote-project")}).(InputRouteComputer)
		if !ok || inProject.Select == nil || !*inProject.Select {
			t.Error("a new thread in a folder on the other computer starts there")
		}
	})
}

func TestEachCommandGoesWhereItsPlacementSays(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) { state.Threads = []*domain.Thread{cmpTask("local")} }), []*PairedComputer{cmpPaired("linux", cmpRemoteState())}, cmpOnScreen("linux"))
		linux := state.Computers.Paired[0]
		for _, definition := range contracts.WorkspaceCommandDefinitions() {
			var input contracts.AppCommand
			if err := jsonx.Unmarshal([]byte(`{"type":"`+definition.Type+`"}`), &input); err != nil {
				continue
			}
			switch contracts.WorkspaceViewInputCommandPlacement(definition.Type) {
			case contracts.CommandPlacementLocal:
				cmpSameJSON(t, RouteInput(state, input), local, definition.Type)
			case contracts.CommandPlacementThread:
				cmpSameJSON(t, RouteInput(state, input), InputRouteComputer{Computer: *linux, Inputs: []WorkspaceInput{input}}, definition.Type)
				if definition.Type != "task.clear-archive" && strings.Contains(","+strings.Join(definition.Fields, ",")+",", ",taskId,") {
					var named contracts.AppCommand
					if err := jsonx.Unmarshal([]byte(`{"type":"`+definition.Type+`","taskId":"local"}`), &named); err == nil {
						cmpSameJSON(t, RouteInput(state, named), local, definition.Type+" naming a thread here")
					}
				}
			case contracts.CommandPlacementPanel:
				cmpSameJSON(t, RouteInput(state, input), InputRouteRefuse{Message: PanelElsewhere}, definition.Type)
			}
		}
		project := cmpCommand(t, `{"type":"project.edit","projectId":"remote-project","name":"Renamed"}`)
		cmpSameJSON(t, RouteInput(state, project), InputRouteComputer{Computer: *linux, Inputs: []WorkspaceInput{project}}, "project")
		home := *state
		home.Computers.Active = nil
		cmpSameJSON(t, RouteInput(&home, project), InputRouteComputer{Computer: *linux, Inputs: []WorkspaceInput{project}}, "a project goes to its holder whatever is on screen")
		for _, typ := range []string{"task.select", "worktree.open-thread", "view.jump-choose"} {
			want := InputRouteComputer{Computer: *linux, Inputs: []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: home.Focused}, contracts.TaskCommandTaskSelect{TaskID: "remote-thread"}}, Select: new(true)}
			cmpSameJSON(t, RouteInput(&home, cmpCommand(t, `{"type":"`+typ+`","taskId":"remote-thread"}`)), want, typ)
			cmpSameJSON(t, RouteInput(&home, cmpCommand(t, `{"type":"`+typ+`","taskId":"local"}`)), local, typ)
		}
		cmpSameJSON(t, RouteInput(state, cmpCommand(t, `{"type":"terminal.input","terminalId":"gone","data":"x"}`)), InputRouteRefuse{Message: "That terminal is no longer available."}, "terminal")
	})
}

func TestACommandForAComputerThatIsAwayIsRefusedWithWhy(t *testing.T) {
	cmpPorted(t, func() {
		problem := "The other computer runs a different version of AI Coding Tool. Update both."
		away := cmpPaired("linux", cmpRemoteState(), cmpStatus(domain.ComputerStatusOffline, problem))
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) { state.Threads = []*domain.Thread{cmpTask("local")} }), []*PairedComputer{away})
		why := InputRouteRefuse{Message: "linux is offline. " + problem}
		for _, input := range []contracts.AppCommand{
			contracts.TaskCommandTaskSelect{TaskID: "remote-thread"},
			contracts.TaskCommandTaskNew{ProjectID: new("remote-project")},
			cmpCommand(t, `{"type":"run.cancel","taskId":"remote-thread"}`),
			contracts.TaskCommandTaskSend{TaskID: new("remote-thread"), Attachments: []*domain.RunAttachment{}},
			cmpCommand(t, `{"type":"project.remove","projectId":"remote-project"}`),
		} {
			cmpSameJSON(t, RouteInput(state, input), why, InputType(input))
		}
		nameless := cmpWithComputers(state, []*PairedComputer{cmpPaired("linux", cmpRemoteState(), cmpStatus(domain.ComputerStatusConnecting))})
		cmpSameJSON(t, RouteInput(nameless, contracts.TaskCommandTaskSelect{TaskID: "remote-thread"}), InputRouteRefuse{Message: "linux is reconnecting."}, "connecting")
	})
}

func TestAComputerOnScreenThatIsNotConnectedCountsAsNone(t *testing.T) {
	cmpPorted(t, func() {
		state := cmpWithComputers(cmpWorkspace(func(state *WorkspaceState) {
			state.Threads, state.CurrentID = []*domain.Thread{cmpTask("local")}, new("local")
		}), []*PairedComputer{cmpPaired("linux", cmpRemoteState(), cmpStatus(domain.ComputerStatusOffline))}, cmpOnScreen("linux"))
		cmpSameJSON(t, RouteInput(state, contracts.TaskCommandTaskSend{Attachments: []*domain.RunAttachment{}, Text: new("hello")}), local, "send")
		cmpSameJSON(t, RouteInput(state, contracts.ViewCommandViewSetFocused{Focused: false}), local, "focus")
	})
}

func TestComputerCommandAvailableHidesOnlyMissingCapabilities(t *testing.T) {
	cmpPorted(t, func() {
		connected := cmpPaired("linux", cmpRemoteState(), func(link *domain.ComputerLink) {
			link.Capabilities = contracts.ComputerCapabilitiesComputerCapabilities()
		})
		state := cmpWithComputers(cmpWorkspace(), []*PairedComputer{connected}, cmpOnScreen("linux"))
		files := *state
		files.Files = map[string][]*domain.AttachedFile{"remote-thread": {{ID: "file", Name: "local", Path: "/local/file"}}}
		send := contracts.TaskCommandAttachmentsSend{Attachments: []*domain.OutgoingAttachment{}}
		if !ComputerCommandAvailable(&files, send) {
			t.Error("a send carrying local files is available, and refuses when sent")
		}
		cmpSameJSON(t, RouteInput(&files, send), InputRouteRefuse{Message: AttachmentsElsewhere}, "files")
		offlineLink := *connected
		offlineLink.Status = domain.ComputerStatusOffline
		offline := cmpWithComputers(state, []*PairedComputer{&offlineLink})
		archive := cmpCommand(t, `{"type":"task.archive","taskId":"remote-thread"}`)
		if !ComputerCommandAvailable(offline, archive) {
			t.Error("an offline computer's command stays available")
		}
		cmpSameJSON(t, RouteInput(offline, archive), InputRouteRefuse{Message: "linux is offline."}, "offline")
		limitedLink := *connected
		limitedLink.Capabilities = []string{}
		if ComputerCommandAvailable(cmpWithComputers(state, []*PairedComputer{&limitedLink}), archive) {
			t.Error("a computer that does not take the command hides it")
		}
	})
}

func TestCapabilitySnapshotHoldsWhileOnlyStateStreams(t *testing.T) {
	snapshot := CreateComputerCapabilitySnapshot()
	capabilities := []string{"command:task.select"}
	computer := &PairedComputer{ComputerLink: cmpLink("linux", func(link *domain.ComputerLink) { link.Capabilities = capabilities })}
	active := "linux"
	computers := &ComputersState{Paired: []*PairedComputer{computer}, Active: &active}
	first := snapshot(computers)
	streamed := *computers
	streamedComputer := *computer
	streamedComputer.State = &WorkspaceState{ComposerFocus: 1}
	streamed.Paired = []*PairedComputer{&streamedComputer}
	if snapshot(&streamed) != first {
		t.Error("streaming state keeps the snapshot")
	}
	offline := *computer
	offline.Status = domain.ComputerStatusOffline
	if snapshot(&ComputersState{Paired: []*PairedComputer{&offline}, Active: &active}) == first {
		t.Error("a line that drops changes it")
	}
	refreshed := *computer
	refreshed.Capabilities = []string{}
	if snapshot(&ComputersState{Paired: []*PairedComputer{&refreshed}, Active: &active}) == first {
		t.Error("a new capability list changes it")
	}
}

func TestShownComputersFollowTheFilter(t *testing.T) {
	a, b := cmpPaired("a", nil), cmpPaired("b", nil)
	computers := &ComputersState{Paired: []*PairedComputer{a, b}, Filter: "all"}
	if got := ShownComputers(computers); len(got) != 2 {
		t.Errorf("all = %d", len(got))
	}
	computers.Filter = "this"
	if got := ShownComputers(computers); len(got) != 0 {
		t.Errorf("this = %d", len(got))
	}
	computers.Filter = "b"
	if got := ShownComputers(computers); len(got) != 1 || got[0] != b {
		t.Errorf("b = %v", got)
	}
}

func TestProjectOnComputerMatchesByNameThenPathThenFolder(t *testing.T) {
	remote := &WorkspaceState{Projects: []*domain.Project{{ID: "p1", Root: "/linux/app"}, {ID: "p2", Root: "/linux/other"}}}
	computer := cmpPaired("linux", remote)
	if _, err := ProjectOnComputer(computer, nil, nil); err == nil || err.Error() != "Name the project on linux to start the thread in." {
		t.Errorf("unnamed = %v", err)
	}
	if project, err := ProjectOnComputer(computer, nil, &domain.Project{ID: "mine", Root: "/mac/app"}); err != nil || project.ID != "p1" {
		t.Errorf("by folder = %v, %v", project, err)
	}
	if _, err := ProjectOnComputer(computer, nil, &domain.Project{ID: "mine", Root: "/mac/none"}); err == nil || err.Error() != "No project on linux matches none. Name the project to start the thread in." {
		t.Errorf("none = %v", err)
	}
}

func TestLedStartRefusal(t *testing.T) {
	if got := LedStartRefusal(nil); got == nil || *got != "That computer is no longer paired." {
		t.Errorf("nil = %v", got)
	}
	old := cmpPaired("linux", nil, func(link *domain.ComputerLink) { link.Capabilities = []string{"command:task.send:lead"} })
	if got := LedStartRefusal(old); got == nil || *got != "Update AI Coding Tool on linux to run threads there." {
		t.Errorf("old = %v", got)
	}
	none := cmpPaired("linux", nil)
	if got := LedStartRefusal(none); got == nil || *got != "Update AI Coding Tool on linux to run threads there." {
		t.Errorf("none = %v", got)
	}
	able := cmpPaired("linux", nil, func(link *domain.ComputerLink) { link.Capabilities = ledSend })
	if got := LedStartRefusal(able); got != nil {
		t.Errorf("able = %v", *got)
	}
}

func TestLeadsDiscardedKeepTheNewest(t *testing.T) {
	edit := ComputersEdit{View: ComputersSliceState{Computers: NoComputers}}
	var ids []string
	for index := range MaxDiscardedLeads + 5 {
		ids = append(ids, fmt.Sprint(index))
	}
	leads := LeadsDiscardedIn(edit, ids).View.Computers.DiscardedLeads
	if len(leads) != MaxDiscardedLeads || leads[0] != "5" || leads[len(leads)-1] != fmt.Sprint(MaxDiscardedLeads+4) {
		t.Errorf("leads = %v", leads)
	}
}
