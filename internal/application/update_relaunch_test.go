package application

import (
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var panelsProject = &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}

func panelsQuitMidRun() *WorkspaceState {
	ids := []string{"working", "approving", "asking", "other"}
	threads := make([]*domain.Thread, len(ids))
	for index, id := range ids {
		threads[index] = panelsTask(id, func(thread *domain.Thread) {
			thread.ProjectID = new(panelsProject.ID)
			thread.RunOpen = new(true)
			thread.Continuation = &domain.Continuation{Provider: "claude", Value: "session-" + id}
			thread.ContinuationStatus = domain.ContinuationStatusAvailable
		})
	}
	run := func(taskID string, edit func(*ActiveRunRunning)) ActiveRun {
		record := ActiveRunRunning{TaskID: taskID, RunID: "run-" + taskID, Status: ActiveRunRunningStatusRunning, Origin: ActiveRunRunningOriginComposer, ReportedIssues: []string{}, Before: ThreadMark{UpdatedAt: 1}}
		if edit != nil {
			edit(&record)
		}
		return record
	}
	return panelsWorkspace(func(s *WorkspaceState) {
		s.Projects = []*domain.Project{panelsProject}
		s.Threads = threads
		s.ActiveRuns = map[string]ActiveRun{
			"working":   run("working", nil),
			"approving": run("approving", func(record *ActiveRunRunning) { record.Status = ActiveRunRunningStatusAwaitingApproval }),
			"asking":    run("asking", func(record *ActiveRunRunning) { record.Questions = []*domain.PendingQuestion{{}} }),
			"other":     run("other", nil),
		}
	})
}

func panelsRelaunched(t *testing.T, before *WorkspaceState, resuming []string) WorkspaceTransition {
	t.Helper()
	stored := make([]*domain.StoredThread, len(before.Threads))
	for index, thread := range before.Threads {
		stored[index] = &domain.StoredThread{}
		encoded, _ := jsonxMarshal(thread)
		if err := jsonxUnmarshal(encoded, stored[index]); err != nil {
			t.Fatal(err)
		}
	}
	serialized := domain.SerializeThreadStore(&domain.ThreadStoreData{Tasks: stored, Projects: []*domain.Project{panelsProject}, Worktrees: []*domain.Worktree{}})
	parsed, ok := domain.ParseThreadStore(serialized).(domain.ThreadStoreParseResultTrue)
	if !ok {
		t.Fatal("the store reads back")
	}
	current := Reduce(panelsWorkspace(), WorkspaceEventStoreLoaded{Data: parsed.Data, Resuming: resuming})
	for _, effect := range current.Effects {
		if resolve, ok := effect.(WorkspaceEffectResolveRunWorkspace); ok {
			next := Reduce(&current.State, panelsIn(t, `{"type":"run.resolved","pendingId":"`+resolve.PendingID+`","workspace":{"id":"workspace-a","kind":"project","root":"/repo"}}`))
			current = next
		}
	}
	return current
}

func TestAnUpdateCarriesOnTheTurnsItCutOff(t *testing.T) {
	panelsPorted(t, func() {
		before := panelsQuitMidRun()
		resuming := ResumableAfterUpdate(before)
		slices.Sort(resuming)
		if !slices.Equal(resuming, []string{"other", "working"}) {
			t.Fatalf("a turn waiting on the user stays stopped: %v", resuming)
		}
		after := panelsRelaunched(t, before, []string{"working"})
		working := panelsThread(&after.State, "working")
		if last := working.Messages[len(working.Messages)-1]; last.Text != "App updated. Resuming." {
			t.Errorf("the thread says it resumed: %q", last.Text)
		}
		if working.RunEndedAs != nil {
			t.Error("the cut-off turn did not end")
		}
		if run, ok := after.State.ActiveRuns["working"].(ActiveRunRunning); ok && run.Warming != nil {
			t.Error("nothing waits on it to warm up")
		}
		start := panelsEffectOf(t, after, "start-run")["command"].(map[string]any)
		if !strings.Contains(start["prompt"].(string), "restarted to install an update") || start["continuation"].(map[string]any)["value"] != "session-working" {
			t.Errorf("the run carries on in its session: %v", start)
		}
		other := panelsThread(&after.State, "other")
		if other.RunEndedAs == nil || *other.RunEndedAs != domain.ThreadOutcomeStopped || after.State.ActiveRuns["other"] != nil {
			t.Error("a thread the update did not name ends as it always has")
		}
	})
}

func TestAThreadWhoseTurnEndedBeforeTheQuitIsNotStartedAgain(t *testing.T) {
	panelsPorted(t, func() {
		before := panelsQuitMidRun()
		threads := slices.Clone(before.Threads)
		for index, thread := range threads {
			if thread.ID == "working" {
				ended := *thread
				ended.RunOpen = nil
				threads[index] = &ended
			}
		}
		ended := *before
		ended.Threads = threads
		if after := panelsRelaunched(t, &ended, []string{"working"}); panelsHasEffect(t, after, `{"type":"start-run"}`) {
			t.Error("a thread whose turn ended before the quit is not started again")
		}
	})
}
