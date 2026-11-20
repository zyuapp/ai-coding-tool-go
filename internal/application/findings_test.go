package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func findingsFire() contracts.AutomationFire {
	return contracts.AutomationFire{AutomationID: "automation-1", TaskID: "task-a", RunID: "run-1", Prompt: "Poll", RunNumber: 5}
}

func TestTickRefusalNamesWhoTurnedItAway(t *testing.T) {
	corePorted(t, func() {
		fire := findingsFire()
		idle := coreWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{coreTask("task-a")} })
		waiting := idle.Threads[0]
		busy := func(origin ActiveRunRunningOrigin) *WorkspaceState {
			run := coreActive("task-a", "other").(ActiveRunRunning)
			run.Origin = origin
			next := *idle
			next.ActiveRuns = map[string]ActiveRun{"task-a": run}
			return &next
		}
		sending := *idle
		sending.PendingRuns = map[string]*PendingRun{"pending-1": {ID: "pending-1", RunID: "run-2", Origin: PendingRunOriginComposer, TaskID: new("task-a"), Text: "Hi", Prompt: "Hi", Attachments: []string{}}}
		filed := coreTask("task-a", thArchived(5))
		inProject := coreTask("task-a", thProject("project-1"))
		for _, item := range []struct {
			state   *WorkspaceState
			thread  *domain.Thread
			project *domain.Project
			want    TickRefusal
		}{
			{idle, waiting, nil, ""},
			{idle, nil, nil, TickRefusalNoThread},
			{idle, filed, nil, TickRefusalArchived},
			{busy(ActiveRunRunningOriginAutomation), waiting, nil, TickRefusalBusyAgent},
			{busy(ActiveRunRunningOriginComposer), waiting, nil, TickRefusalBusyUser},
			{&sending, waiting, nil, TickRefusalBusyUser},
			{busy(ActiveRunRunningOriginComposer), filed, nil, TickRefusalBusyUser},
			{idle, inProject, nil, TickRefusalNoWorkspace},
			{idle, inProject, &domain.Project{ID: "project-1", Root: "/work/api", WorkspaceID: new("workspace-1")}, ""},
			{idle, coreTask("task-a", func(t *domain.Thread) {
				t.ResourceStop = &domain.ResourceStop{Cleanup: domain.ResourceStopCleanupComplete}
			}), nil, TickRefusalBusyUser},
		} {
			got, refused := WhyTickCannotRun(item.state, fire, item.thread, item.project)
			if got != item.want || refused != (item.want != "") {
				t.Errorf("want %q, got %q", item.want, got)
			}
		}
	})
}

func TestScheduleTurnedAwayThreeTimesSaysSo(t *testing.T) {
	corePorted(t, func() {
		declined := func(before float64, threads []*domain.Thread, refusal TickRefusal) Step {
			state := coreWorkspace(func(s *WorkspaceState) {
				s.Threads = threads
				s.Notifications = true
				s.Automations = []*domain.AutomationView{threadsAutomation("task-a", func(a *domain.AutomationView) {
					a.ID, a.CreatedAt, a.LastRunAt, a.ConsecutiveDeclines = "automation-1", 100, new(900.0), new(before)
				})}
			})
			return DeclinedTick(state, findingsFire(), threads[0], refusal)
		}
		kinds := func(effects []WorkspaceEffect) []string {
			var names []string
			for _, effect := range effects {
				switch effect.(type) {
				case WorkspaceEffectAutomationAck:
					names = append(names, "automation.ack")
				case WorkspaceEffectAnnounceThread:
					names = append(names, "announce-thread")
				default:
					names = append(names, "other")
				}
			}
			return names
		}
		fresh := []*domain.Thread{coreTask("task-a")}
		for _, before := range []float64{0, 1} {
			if early := declined(before, fresh, TickRefusalBusyAgent); len(early.Edits) != 0 || len(early.Effects) != 1 {
				t.Fatalf("%v declines said something", before)
			}
		}
		third := declined(2, fresh, TickRefusalBusyAgent)
		threadsSame(t, kinds(third.Effects), []string{"automation.ack", "announce-thread"}, "third")
		if third.Effects[0].(WorkspaceEffectAutomationAck).Ack.Started {
			t.Fatal("the tick started")
		}
		after := Afterward(coreWorkspace(func(s *WorkspaceState) { s.Threads = fresh }), SliceTransition(third))
		findings := after.Threads[0].Findings
		if len(findings) != 1 || !strings.HasPrefix(findings[0].Headline, "This automation has not been able to run since ") || *findings[0].Key != "declined:automation-1" {
			t.Fatalf("finding: %s", coreJSON(t, findings))
		}
		if third.Effects[1].(WorkspaceEffectAnnounceThread).Notice.Headline != findings[0].Headline {
			t.Fatal("the notice says something else")
		}
		if chatting := declined(2, fresh, TickRefusalBusyUser); len(chatting.Effects) != 1 || len(chatting.Edits) != 0 {
			t.Fatal("a thread its user works in is not a broken schedule")
		}
		if fourth := declined(3, after.Threads, TickRefusalBusyAgent); len(fourth.Effects) != 1 || len(fourth.Edits) != 0 {
			t.Fatal("said again")
		}
	})
}
