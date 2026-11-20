package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func panelsPriority(state *WorkspaceState) []string {
	ids := []string{}
	for _, thread := range DeriveView(state).ActivityThreads[ActivitySectionsKeyPriority] {
		ids = append(ids, thread.ID)
	}
	slices.Sort(ids)
	return ids
}

func panelsFinished(id, projectID string) *domain.Thread {
	return panelsTask(id, func(thread *domain.Thread) {
		thread.Outcome = new(domain.ThreadOutcomeFinished)
		if projectID != "" {
			thread.ProjectID = new(projectID)
		}
	})
}

func TestACoordinatorStaysInANarrowedListWhileAThreadUnderItWorksThere(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{
				panelsTask("lead", func(thread *domain.Thread) {
					thread.Role = new(domain.ThreadRoleCoordinator)
					thread.Outcome = new(domain.ThreadOutcomeFinished)
				}),
				panelsTask("member", func(thread *domain.Thread) {
					thread.ParentID = new("lead")
					thread.ProjectID = new("web")
				}),
				panelsFinished("api-done", "api"),
			}
		}), `{"projects":[{"id":"web","root":"/work/web"},{"id":"api","root":"/work/api"}]}`)
		narrowed, transition := panelsReduce(t, state, `{"type":"view.set-activity-project","projectId":"web"}`)
		if len(transition.Effects) != 1 {
			t.Error("the choice is remembered")
		}
		if priority := panelsPriority(narrowed); !slices.Equal(priority, []string{"lead"}) {
			t.Errorf("the coordinator stays: %v", priority)
		}
	})
}

func TestTheActivityListsNarrowToOneProjectAPairedComputersIncluded(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{panelsFinished("remote-done", "remote-project"), panelsFinished("remote-chat", "")}
		}), `{"projects":[{"id":"remote-project","root":"/linux/app"}]}`)
		local := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{panelsFinished("web-done", "web"), panelsFinished("api-done", "api")}
		}), `{"projects":[{"id":"web","root":"/work/web"},{"id":"api","root":"/work/api"}]}`)
		local.Computers.Name = "This Mac"
		local.Computers.Paired = []*PairedComputer{{ComputerLink: domain.ComputerLink{ID: "linux", Name: "linux", Host: "linux.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: panelsCapabilities()}, State: remote}}
		if priority := panelsPriority(local); !slices.Equal(priority, []string{"api-done", "remote-chat", "remote-done", "web-done"}) {
			t.Fatalf("every computer's threads are listed: %v", priority)
		}
		web, _ := panelsReduce(t, local, `{"type":"view.set-activity-project","projectId":"web"}`)
		if deref(DeriveView(web).ActivityProject) != "web" || !slices.Equal(panelsPriority(web), []string{"web-done"}) {
			t.Error("the lists narrow to one project")
		}
		remoteOnly, _ := panelsReduce(t, local, `{"type":"view.set-activity-project","projectId":"remote-project"}`)
		if !slices.Equal(panelsPriority(remoteOnly), []string{"remote-done"}) {
			t.Error("a paired computer's project narrows them too")
		}
		gone, _ := panelsReduce(t, local, `{"type":"view.set-activity-project","projectId":"removed"}`)
		if DeriveView(gone).ActivityProject != nil || !slices.Equal(panelsPriority(gone), panelsPriority(local)) {
			t.Error("a project no longer listed narrows nothing")
		}
	})
}
