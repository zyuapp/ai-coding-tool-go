package application

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// A thread the coordinator "lead" here started on a paired computer.
func farWork(edits ...func(*domain.Thread)) *domain.Thread {
	return coordTask("far", append([]func(*domain.Thread){coordTitled("Far work"), coordFarLead, func(thread *domain.Thread) { thread.CreatedAt = new(10.0) }}, edits...)...)
}

// The coordinator here with "studio" holding `holder`, its line as `status`.
func farCoordinating(holder *WorkspaceState, status domain.ComputerStatus) *WorkspaceState {
	studio := coordComputer("studio", holder)
	studio.Status = status
	return coordWorkspace(coordHolding(coordLead("lead", coordTitled("Lead"))), coordPairedWith(studio))
}

// `state` with studio saying `holder`, its line as `status`.
func farSaid(state *WorkspaceState, holder *WorkspaceState, status domain.ComputerStatus) *WorkspaceState {
	next := *state
	studio := *state.Computers.Paired[0]
	studio.State, studio.Status = holder, status
	next.Computers.Paired = []*PairedComputer{&studio}
	return &next
}

func farNotes(state *WorkspaceState) []string {
	return coordIDs(coordThreadOf(state, "lead").CoordinationNotes, func(note *domain.CoordinationNote) string { return note.Text })
}

func TestAPairedComputersThreadIsAMemberOnlyWhileItsCoordinatorIsLiveAndHere(t *testing.T) {
	coordPorted(t, func() {
		state := farCoordinating(coordWorkspace(coordHolding(farWork(), coordTask("other"))), domain.ComputerStatusConnected)
		members := CoordinationMembers(state, "lead")
		if len(members) != 1 || members[0].Thread.ID != "far" || members[0].Computer.ID != "studio" {
			t.Errorf("members: %+v", members)
		}
		filed := *state
		lead := *state.Threads[0]
		lead.Role = nil
		filed.Threads = []*domain.Thread{&lead}
		if len(CoordinationMembers(&filed, "lead")) != 0 {
			t.Error("only a live coordinator here has threads there")
		}
		away := farCoordinating(coordWorkspace(coordHolding(farWork(func(thread *domain.Thread) { thread.RemoteLead.Away = new(true) }))), domain.ComputerStatusConnected)
		if len(CoordinationMembers(away, "lead")) != 0 {
			t.Error("one whose coordinator is away is left out")
		}
	})
}

func TestARemoteMembersNewsReachesItsCoordinatorOnceEachEvenAcrossADroppedLink(t *testing.T) {
	coordPorted(t, func() {
		clock := Now
		Now = func() float64 { return 1_000 }
		defer func() { Now = clock }()
		state := farCoordinating(coordWorkspace(), domain.ComputerStatusConnected)
		next := CoordinationRemoteRemoteNews(state, farSaid(state, coordWorkspace(coordHolding(farWork()), coordRunning(coordRun("far", "run-far"))), domain.ComputerStatusConnected))
		heard := &next.State
		if got := farNotes(heard); !slices.Equal(got, []string{"\"Far work\" started on studio."}) {
			t.Errorf("arrived: %v", got)
		}
		if watermark := coordThreadOf(heard, "lead").RemoteHeard["far"]; watermark != 10 {
			t.Errorf("heard up to its start: %v", watermark)
		}
		dropped := farSaid(heard, heard.Computers.Paired[0].State, domain.ComputerStatusOffline)
		dropped = &CoordinationRemoteRemoteNews(heard, dropped).State
		decision := &domain.Decision{ID: "d1", Question: "Keep the old API?", Options: []*domain.DecisionOption{}, RaisedAt: 20}
		finished := coordWorkspace(coordHolding(farWork(func(thread *domain.Thread) {
			thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateDone, Summary: "Tests pass", At: 30}
			thread.Decisions = []*domain.Decision{decision}
			thread.RunEndedAt, thread.Outcome, thread.RunEndedAs = new(40.0), new(domain.ThreadOutcomeFinished), new(domain.ThreadOutcomeFinished)
		})))
		caught := CoordinationRemoteRemoteNews(dropped, farSaid(dropped, finished, domain.ComputerStatusConnected))
		if got := farNotes(&caught.State)[1:]; !slices.Equal(got, []string{
			"\"Far work\" asked the user to decide: Keep the old API?",
			"\"Far work\" reported done: Tests pass",
			"\"Far work\" ended its turn.",
		}) {
			t.Errorf("what happened while the line was down is caught up once it is back: %v", got)
		}
		if watermark := coordThreadOf(&caught.State, "lead").RemoteHeard["far"]; watermark != 40 {
			t.Errorf("watermark: %v", watermark)
		}
		if !slices.ContainsFunc(caught.Effects, func(effect WorkspaceEffect) bool {
			announced, ok := effect.(WorkspaceEffectAnnounceThread)
			return ok && announced.Notice.Headline == "Needs you: Keep the old API?"
		}) {
			t.Error("the decision is put to the user")
		}
		if !caught.Leads.Has("lead") {
			t.Error("its coordinator is looked at again")
		}
		again := CoordinationRemoteRemoteNews(&caught.State, farSaid(&caught.State, coordWorkspace(coordHolding(finished.Threads...)), domain.ComputerStatusConnected))
		if !slices.Equal(farNotes(&again.State), farNotes(&caught.State)) {
			t.Error("each is heard once")
		}
	})
}

func TestArchivingTheCoordinatorLetsItsRemoteMembersStandOnTheirOwn(t *testing.T) {
	coordPorted(t, func() {
		studio := coordComputer("studio", coordWorkspace(coordHolding(farWork())))
		linux := coordComputer("linux", coordWorkspace(coordHolding(farWork(func(thread *domain.Thread) { thread.ID = "away" }))))
		linux.Status = domain.ComputerStatusOffline
		state := coordWorkspace(coordHolding(coordLead("lead", coordTitled("Lead"))), coordPairedWith(studio, linux))
		lead := state.Threads[0]
		archivedLead := *lead
		archivedLead.ArchivedAt = new(5.0)
		archived := *state
		archived.Threads = []*domain.Thread{&archivedLead}
		changes := []*ThreadChange{{ID: "lead", Before: lead, After: &archivedLead}}
		forwards := Released(state, &archived, changes)
		want := []WorkspaceEffect{WorkspaceEffectComputerForward{ID: "studio", Inputs: []WorkspaceInput{contracts.TaskCommandTaskSetLeadAway{TaskID: "far", Away: true}}, Silent: new(true)}}
		if !reflect.DeepEqual(forwards, want) {
			t.Errorf("only a line that is up carries it: %#v", forwards)
		}
		back := archived
		linuxBack := *linux
		linuxBack.Status = domain.ComputerStatusConnected
		back.Computers.Paired = []*PairedComputer{studio, &linuxBack}
		told := Released(&archived, &back, nil)
		want = []WorkspaceEffect{WorkspaceEffectComputerForward{ID: "linux", Inputs: []WorkspaceInput{contracts.TaskCommandTaskSetLeadAway{TaskID: "away", Away: true}}, Silent: new(true)}}
		if !reflect.DeepEqual(told, want) {
			t.Errorf("a computer that was offline is told when it reconnects: %#v", told)
		}
		older := slices.DeleteFunc(slices.Clone(studio.Capabilities), func(capability string) bool { return strings.HasPrefix(capability, "command:task.set-lead-away") })
		oldStudio := *studio
		oldStudio.Capabilities = older
		oldState, oldArchived := *state, archived
		oldState.Computers.Paired = []*PairedComputer{&oldStudio}
		oldArchived.Computers.Paired = oldState.Computers.Paired
		let := Released(&oldState, &oldArchived, changes)
		want = []WorkspaceEffect{WorkspaceEffectComputerForward{ID: "studio", Inputs: []WorkspaceInput{contracts.TaskCommandTaskSetCoordinator{TaskID: "far"}}, Silent: new(true)}}
		if !reflect.DeepEqual(let, want) {
			t.Errorf("a computer from before a coordinator could be away lets its thread go for good: %#v", let)
		}
	})
}
