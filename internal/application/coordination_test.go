package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var coordIdle = RosterActivity{Working: jsonx.Set[string]{}, Blocked: jsonx.Set[string]{}, Ranked: jsonx.Set[string]{}}

// A coordinator here with `threads` beside it, and threads under it on `computers`.
func coordinating(threads []*domain.Thread, computers ...*PairedComputer) *WorkspaceState {
	return coordWorkspace(coordHolding(append([]*domain.Thread{coordLead("lead", coordTitled("Lead"))}, threads...)...), coordPairedWith(computers...))
}

func rosterIDs(members []*RosterMember) []string {
	return coordIDs(members, func(member *RosterMember) string { return member.Thread.ID })
}

func outcome(value domain.ThreadOutcome) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.Outcome = new(value) }
}

func TestRosterHoldsThreadsHereFirstAndOnlyALiveCoordinatorHasOne(t *testing.T) {
	coordPorted(t, func() {
		studio := coordWorkspace(coordHolding(coordTask("far", coordFarLead), coordTask("other")))
		state := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead")), coordTask("alone")}, coordComputer("studio", studio))
		roster := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, nil)
		if got := rosterIDs(roster.Members); !slices.Equal(got, []string{"near", "far"}) {
			t.Errorf("members: %v", got)
		}
		if got := coordThreadIDs(roster.Threads); !slices.Equal(got, []string{"near", "far"}) {
			t.Errorf("threads: %v", got)
		}
		if roster.Members[0].Computer != nil || roster.Members[1].Computer.ID != "studio" {
			t.Error("each member names the computer that holds it")
		}
		if len(CoordinationCoordinatorRoster(state, state.Threads[2], coordIdle, nil).Members) != 0 {
			t.Error("a thread that coordinates nothing has no roster")
		}
		filed := *state
		lead := *state.Threads[0]
		lead.Role = nil
		filed.Threads = append([]*domain.Thread{&lead}, state.Threads[1:]...)
		if len(CoordinationCoordinatorRoster(&filed, &lead, coordIdle, nil).Members) != 0 {
			t.Error("a thread that is no longer a coordinator has no roster")
		}
	})
}

func TestRosterReadsEachThreadAsTheComputerHoldingItLastSaid(t *testing.T) {
	coordPorted(t, func() {
		studio := coordWorkspace(coordHolding(coordTask("far", coordFarLead), coordTask("asking", coordFarLead)), coordRunning(
			coordRun("far", "run-far"),
			coordRun("asking", "run-asking", func(run *ActiveRunRunning) { run.Status = ActiveRunRunningStatusAwaitingApproval }),
		))
		state := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead"))}, coordComputer("studio", studio))
		type standing struct {
			id                       string
			working, blocked, ranked bool
		}
		read := func(roster *CoordinatorRoster) []standing {
			return slices.Collect(func(yield func(standing) bool) {
				for _, member := range roster.Members {
					if !yield(standing{member.Thread.ID, member.Working, member.Blocked, member.Ranked}) {
						return
					}
				}
			})
		}
		idle := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, nil)
		if got := read(idle); !slices.Equal(got, []standing{{"near", false, false, false}, {"far", true, false, true}, {"asking", false, true, true}}) {
			t.Errorf("one waiting on an approval is not doing work: %v", got)
		}
		if !idle.Running || !idle.Blocked {
			t.Error("the roster runs and is blocked as its members are")
		}
		here := CoordinationCoordinatorRoster(state, state.Threads[0], RosterActivity{Working: jsonx.SetOf("near", "far"), Blocked: jsonx.SetOf("near"), Ranked: jsonx.SetOf("near")}, nil)
		got := read(here)
		if got[0] != (standing{"near", false, true, true}) || !got[1].working || got[1].blocked || got[2].working || !got[2].blocked {
			t.Errorf("this computer's sets read only its own threads: %v", got)
		}
	})
}

func TestRosterDismissFilesAwayThreadsWithAVerdictNoneWaitingOnAnApproval(t *testing.T) {
	coordPorted(t, func() {
		studio := coordWorkspace(coordHolding(coordTask("far", coordFarLead, outcome(domain.ThreadOutcomeFinished)), coordTask("asking", coordFarLead, outcome(domain.ThreadOutcomeFailed))),
			coordRunning(coordRun("asking", "run-asking", func(run *ActiveRunRunning) { run.Status = ActiveRunRunningStatusAwaitingApproval })))
		state := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead"), outcome(domain.ThreadOutcomeFinished))}, coordComputer("studio", studio))
		roster := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, nil)
		if got := rosterIDs(roster.Filed); !slices.Equal(got, []string{"near", "far"}) || !roster.Dismissable {
			t.Errorf("filed %v, dismissable %v", got, roster.Dismissable)
		}
		near := CoordinationCoordinatorRoster(state, state.Threads[0], RosterActivity{Working: coordNone, Blocked: jsonx.SetOf("near"), Ranked: coordNone}, nil)
		if got := rosterIDs(near.Filed); !slices.Equal(got, []string{"far"}) || !near.Dismissable {
			t.Errorf("the one on the paired computer still has a verdict to file away: %v", got)
		}
		quiet := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead"))}, coordComputer("studio", coordWorkspace(coordHolding(coordTask("far", coordFarLead)))))
		if CoordinationCoordinatorRoster(quiet, quiet.Threads[0], coordIdle, nil).Dismissable {
			t.Error("nothing to file away")
		}
		if CoordinationCoordinatorRoster(state, state.Threads[0], RosterActivity{Working: coordNone, Blocked: jsonx.SetOf("lead"), Ranked: coordNone}, nil).Dismissable {
			t.Error("a row waiting on an approval has nothing to dismiss until it is answered")
		}
	})
}

func TestRosterCountsOnlyThePairedComputersNamed(t *testing.T) {
	coordPorted(t, func() {
		studio := coordWorkspace(coordHolding(coordTask("far", coordFarLead, outcome(domain.ThreadOutcomeFinished))))
		laptop := coordWorkspace(coordHolding(coordTask("lap", coordFarLead)), coordRunning(coordRun("lap", "run-lap")))
		computers := []*PairedComputer{coordComputer("studio", studio), coordComputer("laptop", laptop)}
		state := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead"))}, computers...)
		if got := rosterIDs(CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, nil).Members); !slices.Equal(got, []string{"near", "far", "lap"}) {
			t.Errorf("every computer: %v", got)
		}
		shownOn := computers[:1]
		shown := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, &shownOn)
		if got := rosterIDs(shown.Members); !slices.Equal(got, []string{"near", "far"}) || shown.Running || !shown.Dismissable {
			t.Errorf("studio only: %v running %v dismissable %v", got, shown.Running, shown.Dismissable)
		}
		otherOn := computers[1:]
		other := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, &otherOn)
		if got := rosterIDs(other.Members); !slices.Equal(got, []string{"near", "lap"}) || !other.Running || other.Dismissable {
			t.Errorf("laptop only: %v running %v dismissable %v", got, other.Running, other.Dismissable)
		}
	})
}

func TestRosterCountsWhatWaitsOnTheUserWhereverEachThreadIs(t *testing.T) {
	coordPorted(t, func() {
		decided := func(id string) func(*domain.Thread) {
			return func(thread *domain.Thread) {
				thread.Decisions = []*domain.Decision{{ID: id, Question: "Which?", Options: []*domain.DecisionOption{{Label: "A"}, {Label: "B"}}, RaisedAt: 1}}
			}
		}
		studio := coordWorkspace(coordHolding(coordTask("far", coordFarLead, decided("d-far"))))
		state := coordinating([]*domain.Thread{coordTask("near", coordUnder("lead"), decided("d-near"))}, coordComputer("studio", studio))
		if got := CoordinationCoordinatorRoster(state, state.Threads[0], coordIdle, nil).Waiting.Decisions; got != 2 {
			t.Errorf("decisions: %v", got)
		}
		withOwn := *state
		lead := coordLead("lead", coordTitled("Lead"), decided("d-lead"))
		withOwn.Threads = append([]*domain.Thread{lead}, state.Threads[1:]...)
		if got := CoordinationCoordinatorRoster(&withOwn, lead, coordIdle, nil).Waiting.Decisions; got != 3 {
			t.Errorf("with its own: %v", got)
		}
	})
}

func TestCoordinatorStandsForItsThreadsInTheActivityLists(t *testing.T) {
	coordPorted(t, func() {
		lead := coordLead("lead")
		asking := coordTask("asking", coordUnder("lead"), func(thread *domain.Thread) {
			thread.Decisions = []*domain.Decision{{ID: "d1", Question: "Q?", Options: []*domain.DecisionOption{}, RaisedAt: 1}}
		})
		busy := coordTask("busy", coordUnder("lead"))
		sections := CoordinationSections([]*domain.Thread{lead, asking, busy}, jsonx.SetOf("busy"), coordNone, nil)
		if got := coordThreadIDs(sections[ActivitySectionsKeyPriority]); !slices.Equal(got, []string{"lead"}) {
			t.Errorf("priority: %v", got)
		}
		if got := coordThreadIDs(slices.Concat(sections[ActivitySectionsKeyRunning], sections[ActivitySectionsKeyThreads])); len(got) != 0 {
			t.Errorf("its threads are reached through it, in no list of their own: %v", got)
		}
		working := CoordinationSections([]*domain.Thread{lead, busy}, jsonx.SetOf("busy"), coordNone, nil)
		if got := coordThreadIDs(working[ActivitySectionsKeyRunning]); !slices.Equal(got, []string{"lead"}) {
			t.Errorf("running: %v", got)
		}
		orphan := CoordinationSections([]*domain.Thread{coordTask("lead"), busy}, jsonx.SetOf("busy"), coordNone, nil)
		if got := coordThreadIDs(orphan[ActivitySectionsKeyRunning]); !slices.Equal(got, []string{"busy"}) {
			t.Errorf("a thread whose coordinator stepped down stands on its own: %v", got)
		}
		suggested := &domain.ThreadProposal{ID: "p1", Title: "Docs", Prompt: "Write them", Place: domain.ProposalPlace{Worktree: new(false)}, ProposedAt: 1}
		requested := &domain.ThreadProposal{ID: "p2", Title: "GPU tests", Prompt: "Run them", Place: domain.ProposalPlace{Worktree: new(false), Computer: &domain.ProposalPlaceComputer{ID: "studio", Name: "Studio"}}, From: new("busy"), ProposedAt: 2}
		for _, proposal := range []*domain.ThreadProposal{suggested, requested} {
			proposing := coordLead("lead", func(thread *domain.Thread) { thread.Proposals = []*domain.ThreadProposal{proposal} })
			for _, threads := range [][]*domain.Thread{{proposing, busy}, {proposing}} {
				if got := coordThreadIDs(CoordinationSections(threads, jsonx.SetOf("busy"), coordNone, nil)[ActivitySectionsKeyPriority]); !slices.Equal(got, []string{"lead"}) {
					t.Errorf("%s waits on the user as a decision does: %v", proposal.Title, got)
				}
			}
		}
	})
}

func TestWaitingLineSaysWhatWaitsOnTheUser(t *testing.T) {
	cases := []struct {
		waiting domain.WaitingOnUser
		owedBy  string
		want    string
	}{
		{domain.WaitingOnUser{Decisions: 1, Requests: []string{}}, "", "A decision is waiting on you"},
		{domain.WaitingOnUser{Decisions: 2, Suggestions: 1, Requests: []string{"Studio"}}, "the user", "2 decisions, a request to run a thread on Studio and a suggested thread are waiting on the user"},
		{domain.WaitingOnUser{Requests: []string{"Studio", "Laptop"}, Failed: 1}, "", "2 requests to run threads on other computers and a failed thread are waiting on you"},
	}
	for _, item := range cases {
		if got := WaitingLine(item.waiting, item.owedBy); got == nil || *got != item.want {
			t.Errorf("got %q, want %q", coordValueOr(got, ""), item.want)
		}
	}
	if WaitingLine(domain.WaitingOnUser{Requests: []string{}}, "") != nil {
		t.Error("nothing waits")
	}
}
