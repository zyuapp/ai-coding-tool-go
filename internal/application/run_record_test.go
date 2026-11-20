package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func runRecordStart(id string, taskID string, origin PendingRunOrigin) *PendingRun {
	pending := &PendingRun{ID: id, RunID: "run-" + id, Origin: origin, Attachments: []string{}}
	if taskID != "" {
		pending.TaskID = new(taskID)
	}
	return pending
}

func TestRunRecordThreadBusy(t *testing.T) {
	state := &WorkspaceState{
		Threads:     []*domain.Thread{runsTask("one"), runsTask("two"), runsTask("three")},
		ActiveRuns:  map[string]ActiveRun{"one": runsActive("one", "run-1")},
		PendingRuns: map[string]*PendingRun{"p1": runRecordStart("p1", "two", "composer"), "p2": runRecordStart("p2", "", "composer")},
	}
	if !ThreadBusy(state, "one") || !ThreadBusy(state, "two") || ThreadBusy(state, "three") {
		t.Fatal("busy is a run going or a start resolving")
	}
}

func TestRunRecordStartsGroupedOncePerSet(t *testing.T) {
	state := &WorkspaceState{PendingRuns: map[string]*PendingRun{
		"p1": runRecordStart("p1", "two", "composer"),
		"p2": runRecordStart("p2", "", "composer"),
		"p3": runRecordStart("p3", "two", "automation"),
	}}
	var ids []string
	for _, pending := range StartsOf(state, "two") {
		ids = append(ids, pending.ID)
	}
	if !slices.Equal(ids, []string{"p1", "p3"}) || len(StartsOf(state, "one")) != 0 {
		t.Fatalf("starts = %v", ids)
	}
	if grouped := StartingThreads(state); len(grouped) != 1 || grouped["two"] == nil {
		t.Fatalf("grouped = %v", grouped)
	}
	first := StartingThreads(state)
	if again := StartingThreads(&WorkspaceState{PendingRuns: state.PendingRuns}); &again["two"][0] != &first["two"][0] {
		t.Fatal("the same starts were grouped twice")
	}
}

func TestRunRecordUsersTurn(t *testing.T) {
	state := &WorkspaceState{
		ActiveRuns: map[string]ActiveRun{
			"mine": runsActive("mine", "run-1"),
			"tick": runsActive("tick", "run-2", func(run *ActiveRunRunning) { run.Origin = "automation" }),
		},
		PendingRuns: map[string]*PendingRun{"p1": runRecordStart("p1", "sending", "composer"), "p2": runRecordStart("p2", "ticking", "automation")},
	}
	for taskID, want := range map[string]bool{"mine": true, "sending": true, "tick": false, "ticking": false, "idle": false} {
		if UsersTurn(state, taskID) != want {
			t.Errorf("UsersTurn(%s) != %v", taskID, want)
		}
	}
}

func TestRunRecordThreadToBegin(t *testing.T) {
	state := &WorkspaceState{
		Threads:    []*domain.Thread{runsTask("free"), runsTask("filed", func(thread *domain.Thread) { thread.ArchivedAt = new(5.0) }), runsTask("going")},
		ActiveRuns: map[string]ActiveRun{"going": runsActive("going", "run-1")},
	}
	if thread := ThreadToBegin(state, new("free")); thread == nil || thread.ID != "free" {
		t.Fatal("a free thread may begin")
	}
	for _, id := range []*string{new("filed"), new("going"), new("gone"), nil} {
		if ThreadToBegin(state, id) != nil {
			t.Fatalf("%v may not begin", id)
		}
	}
}

func TestRunRecordRunsInWorkspace(t *testing.T) {
	runsPorted(t, func() {
		project := &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}
		state := runsWorkspace(func(state *WorkspaceState) {
			state.Projects = []*domain.Project{project}
			state.Threads = []*domain.Thread{runsTask("one", func(thread *domain.Thread) { thread.ProjectID = new("project-a") }), runsTask("two")}
			state.ActiveRuns = map[string]ActiveRun{"one": runsActive("one", "run-1")}
		})
		if !RunsInWorkspace(state, project.WorkspaceID) || RunsInWorkspace(state, new("elsewhere")) || RunsInWorkspace(state, nil) {
			t.Fatal("a checkout holds a run while a thread in it has one going")
		}
		idle := *state
		idle.ActiveRuns = map[string]ActiveRun{}
		if RunsInWorkspace(&idle, project.WorkspaceID) {
			t.Fatal("no run, no hold")
		}
	})
}
