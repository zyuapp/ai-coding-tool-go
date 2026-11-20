package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestAWatchThatIsRemovedLandsItsThreadInPriority(t *testing.T) {
	panelsPorted(t, func() {
		watched := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{panelsTask("task-a"), panelsTask("task-b")}
		}), `{"automations":[`+panelsWatch(`,"endsWhen":"the PR is approved."`)+`]}`)
		ended, _ := panelsReduce(t, watched, `{"type":"automations.changed","automations":[]}`)
		thread := ended.Threads[0]
		if deref(thread.Outcome) != domain.ThreadOutcomeFinished || thread.OutcomeUnread == nil || !*thread.OutcomeUnread {
			t.Error("a watch that ended out of sight finished unread")
		}
		if priority, _, _ := panelsSections(ended); !slices.Equal(priority, []string{"task-a"}) {
			t.Errorf("its thread lands in Priority: %v", priority)
		}
		onScreen := *watched
		onScreen.CurrentID = new("task-a")
		seen, _ := panelsReduce(t, &onScreen, `{"type":"automations.changed","automations":[]}`)
		if deref(seen.Threads[0].Outcome) != domain.ThreadOutcomeFinished || seen.Threads[0].OutcomeUnread != nil {
			t.Error("one that ended on screen is already read")
		}
	})
}

func TestAWatchThatEndsAfterAFailedTickReadsAsFinished(t *testing.T) {
	panelsPorted(t, func() {
		watched := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{panelsTask("task-a", func(thread *domain.Thread) {
				thread.Outcome = new(domain.ThreadOutcomeFailed)
				thread.RunEndedAs = new(domain.ThreadOutcomeFailed)
			})}
		}), `{"automations":[`+panelsWatch(`,"endsWhen":"the PR is approved."`)+`]}`)
		ended, _ := panelsReduce(t, watched, `{"type":"automations.changed","automations":[]}`)
		if deref(ended.Threads[0].RunEndedAs) != domain.ThreadOutcomeFinished {
			t.Error("the watch's ending is its verdict")
		}
	})
}

func TestAWatchUnderACoordinatorThatEndsIsHeardFinished(t *testing.T) {
	panelsPorted(t, func() {
		watched := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{
				panelsTask("lead", func(thread *domain.Thread) {
					thread.Role = new(domain.ThreadRoleCoordinator)
					thread.Title = "Lead"
				}),
				panelsTask("task-a", func(thread *domain.Thread) {
					thread.Title = "Babysit"
					thread.ParentID = new("lead")
				}),
			}
		}), `{"automations":[`+panelsWatch(`,"endsWhen":"the PR is approved."`)+`]}`)
		ended, _ := panelsReduce(t, watched, `{"type":"automations.changed","automations":[]}`)
		lead := panelsThread(ended, "lead")
		if len(lead.CoordinationNotes) != 1 || lead.CoordinationNotes[0].Text != `"Babysit" finished watching.` {
			t.Errorf("its coordinator hears it finished: %+v", lead.CoordinationNotes)
		}
	})
}

func TestOnlyALiveWatchLeavingAnnouncesAnything(t *testing.T) {
	panelsPorted(t, func() {
		for _, before := range []string{panelsWatch(``), panelsWatch(`,"endsWhen":"the PR is approved.","paused":true`)} {
			state := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{panelsTask("task-a")} }), `{"automations":[`+before+`]}`)
			if ended, _ := panelsReduce(t, state, `{"type":"automations.changed","automations":[]}`); ended.Threads[0].Outcome != nil {
				t.Error("a routine or a paused watch leaving says nothing")
			}
		}
		live := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) { s.Threads = []*domain.Thread{panelsTask("task-a")} }), `{"automations":[`+panelsWatch(`,"endsWhen":"the PR is approved."`)+`]}`)
		if paused, _ := panelsReduce(t, live, `{"type":"automations.changed","automations":[`+panelsWatch(`,"endsWhen":"the PR is approved.","paused":true`)+`]}`); paused.Threads[0].Outcome != nil {
			t.Error("pausing a watch is not finishing it")
		}
	})
}
