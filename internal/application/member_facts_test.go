package application

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const factsAt = 1_800_000_000_000

var factsDecision = domain.Decision{ID: "d1", Question: "Keep the old API?", Options: []*domain.DecisionOption{}, RaisedAt: 20}

func factsLead() *domain.Thread { return coordLead("lead", coordTitled("Lead")) }

func factsWorker(edits ...func(*domain.Thread)) *domain.Thread {
	return coordTask("worker", append([]func(*domain.Thread){coordUnder("lead"), coordTitled("Worker")}, edits...)...)
}

// noteShape is a note as the tests read it: whose, what, and how it wakes.
func noteShape(note *domain.CoordinationNote) string {
	return fmt.Sprintf("%s|%s|urgent=%v|quiet=%v|run=%s", note.ThreadID, note.Text, coordTrue(note.Urgent), coordTrue(note.Quiet), coordValueOr(note.RunID, ""))
}

func notesOf(state *WorkspaceState) []string {
	return coordIDs(coordThreadOf(state, "lead").CoordinationNotes, noteShape)
}

func noticesOf(effects []WorkspaceEffect) []string {
	var notices []string
	for _, effect := range effects {
		if announced, ok := effect.(WorkspaceEffectAnnounceThread); ok {
			notices = append(notices, announced.Notice.Title+" | "+announced.Notice.Headline)
		}
	}
	return notices
}

func TestProgressIsNoNewsAndAThreadThatCannotGoOnIsTheUsersAtOnce(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(factsLead(), factsWorker()), func(state *WorkspaceState) { state.Notifications = true })
		heard := func(fact MemberFact) HeardFacts {
			return MemberFactsHeardFacts(state, state.Threads[0], state.Threads[1], []MemberFact{fact}, factsAt)
		}
		if working := heard(MemberFactReported{Report: domain.CoordinationReport{State: domain.CoordinationStateWorking, Summary: "Halfway", At: factsAt}}); len(working.Notes)+len(working.Effects) != 0 {
			t.Error("progress is no news")
		}
		done := heard(MemberFactReported{Report: domain.CoordinationReport{State: domain.CoordinationStateDone, Summary: "Shipped", At: factsAt}, RunID: new("run-1")})
		if got := coordIDs(done.Notes, noteShape); !reflect.DeepEqual(got, []string{"worker|\"Worker\" reported done: Shipped|urgent=false|quiet=false|run=run-1"}) || len(done.Effects) != 0 {
			t.Errorf("a thread done is only a note: %v %v", got, done.Effects)
		}
		blocked := heard(MemberFactReported{Report: domain.CoordinationReport{State: domain.CoordinationStateBlocked, Summary: "No access", At: factsAt}})
		if !coordTrue(blocked.Notes[0].Urgent) {
			t.Error("a blocked thread's note cannot wait")
		}
		if got := noticesOf(blocked.Effects); !reflect.DeepEqual(got, []string{"Lead · Worker | Blocked: No access"}) {
			t.Errorf("notices: %v", got)
		}
	})
}

func TestADecisionIsNotedForTheCoordinatorAndPutToTheUserUnderIt(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(factsLead(), factsWorker()), func(state *WorkspaceState) { state.Notifications = true })
		heard := WithFactsHeard(state, state.Threads[0], state.Threads[1], []MemberFact{MemberFactRaised{Decision: factsDecision, RunID: new("run-1")}}, factsAt)
		if got := notesOf(&heard.State); !reflect.DeepEqual(got, []string{"worker|\"Worker\" asked the user to decide: Keep the old API?|urgent=false|quiet=false|run=run-1"}) {
			t.Errorf("notes: %v", got)
		}
		if got := noticesOf(heard.Effects); !reflect.DeepEqual(got, []string{"Lead | Needs you: Keep the old API?"}) {
			t.Errorf("notices: %v", got)
		}
	})
}

func TestATurnsEndIsToldToTheUserOnlyWhenItsOwnComputerKeepsItToItself(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(factsLead(), factsWorker()), func(state *WorkspaceState) { state.Notifications = true })
		ended := func(fact MemberFactEnded) HeardFacts {
			return MemberFactsHeardFacts(state, state.Threads[0], state.Threads[1], []MemberFact{fact}, factsAt)
		}
		if got := ended(MemberFactEnded{Status: TurnStatusSucceeded}).Effects; len(got) != 0 {
			t.Errorf("a thread here tells it itself: %v", got)
		}
		failed := ended(MemberFactEnded{Status: TurnStatusFailed, Tell: &MemberFactEndedTell{Surfacing: new(RunSurfacingFailed), Failure: new("Out of disk")}})
		if got := noticesOf(failed.Effects); !reflect.DeepEqual(got, []string{"Lead · Worker | Out of disk"}) {
			t.Errorf("a failure there: %v", got)
		}
		if got := ended(MemberFactEnded{Status: TurnStatusSucceeded, Quiet: true, Tell: &MemberFactEndedTell{}}).Effects; len(got) != 0 {
			t.Errorf("a quiet finish: %v", got)
		}
		stopped := ended(MemberFactEnded{Status: TurnStatusCancelled, Quiet: true})
		if got := coordIDs(stopped.Notes, noteShape); !reflect.DeepEqual(got, []string{"worker|\"Worker\" was stopped.|urgent=false|quiet=true|run="}) {
			t.Errorf("stopped: %v", got)
		}
		arrived := MemberFactsHeardFacts(state, state.Threads[0], state.Threads[1], []MemberFact{MemberFactArrived{Computer: "studio"}}, factsAt)
		if got := coordIDs(arrived.Notes, noteShape); !reflect.DeepEqual(got, []string{"worker|\"Worker\" started on studio.|urgent=false|quiet=true|run="}) {
			t.Errorf("arrived: %v", got)
		}
	})
}

var (
	factsReply  = &domain.ConversationMessage{ID: "reply", Kind: domain.ConversationMessageKindAssistant, Text: "I need the staging key.", At: 25}
	factsReport = domain.CoordinationReport{State: domain.CoordinationStateBlocked, Summary: "No staging access", At: 20}
)

func coordRunEvent(event contracts.RunEvent) WorkspaceInput {
	return WorkspaceEventRunEvent{Event: event}
}

// One turn of a thread under the lead, heard from its own events here.
func factsHeardHere() (*WorkspaceState, []WorkspaceEffect) {
	state := coordWorkspace(coordHolding(factsLead(), factsWorker()), coordRunning(coordRun("worker", "run-1")), func(state *WorkspaceState) {
		state.Notifications = true
		state.RunStatuses = map[string]ThreadRunStatus{"worker": ThreadRunStatusRunning}
	})
	var effects []WorkspaceEffect
	for _, input := range []WorkspaceInput{
		WorkspaceEventCoordinationDecisionRaised{TaskID: "worker", DecisionID: &factsDecision.ID, Request: domain.DecisionRequest{Question: factsDecision.Question, Options: []*domain.DecisionOption{}}},
		WorkspaceEventCoordinationReported{TaskID: "worker", State: factsReport.State, Summary: factsReport.Summary},
		coordRunEvent(contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "worker", RunID: "run-1", Sequence: 1}, MessageID: factsReply.ID, Text: factsReply.Text}),
		coordRunEvent(contracts.RunEventRunStatus{RunEventBase: contracts.RunEventBase{TaskID: "worker", RunID: "run-1", Sequence: 2}, Status: domain.RunStatusSucceeded}),
	} {
		next := Reduce(state, input)
		effects = append(effects, next.Effects...)
		state = &next.State
	}
	return state, effects
}

// The same turn, heard from what a paired computer holding the thread says.
func factsHeardFromPairedComputer() (*WorkspaceState, []WorkspaceEffect) {
	far := func(edits ...func(*domain.Thread)) *domain.Thread {
		return coordTask("worker", append([]func(*domain.Thread){coordTitled("Worker"), coordFarLead, func(thread *domain.Thread) { thread.CreatedAt = new(10.0) }}, edits...)...)
	}
	raised := func(thread *domain.Thread) {
		thread.Decisions = []*domain.Decision{new(factsDecision)}
		thread.Report = new(factsReport)
	}
	state := coordWorkspace(coordHolding(factsLead()), coordPairedWith(coordComputer("studio", nil)), func(state *WorkspaceState) { state.Notifications = true })
	var effects []WorkspaceEffect
	for _, holder := range []*WorkspaceState{
		coordWorkspace(coordHolding(far()), coordRunning(coordRun("worker", "run-1"))),
		coordWorkspace(coordHolding(far(raised)), coordRunning(coordRun("worker", "run-1"))),
		coordWorkspace(coordHolding(far(raised, func(thread *domain.Thread) {
			thread.Messages = []*domain.ConversationMessage{factsReply}
			thread.RunEndedAt, thread.RunEndedAs = new(30.0), new(domain.ThreadOutcomeFinished)
		}))),
	} {
		next := Reduce(state, ComputerEventComputerState{ID: "studio", State: *holder})
		effects = append(effects, next.Effects...)
		state = &next.State
	}
	return state, effects
}

func TestACoordinatorHearsTheSameOfAThreadHereAndOfOneOnAPairedComputer(t *testing.T) {
	coordPorted(t, func() {
		here, hereEffects := factsHeardHere()
		there, thereEffects := factsHeardFromPairedComputer()
		heard := notesOf(there)
		if len(heard) == 0 || heard[0] != "worker|\"Worker\" started on studio.|urgent=false|quiet=true|run=" {
			t.Fatalf("arrived first: %v", heard)
		}
		if !reflect.DeepEqual(heard[1:], notesOf(here)) {
			t.Errorf("there %v\nhere %v", heard[1:], notesOf(here))
		}
		texts := coordIDs(coordThreadOf(here, "lead").CoordinationNotes, func(note *domain.CoordinationNote) string { return note.Text })
		if strings.Join(texts, "\n") != strings.Join([]string{
			"\"Worker\" asked the user to decide: Keep the old API?",
			"\"Worker\" reported blocked: No staging access",
			"\"Worker\" ended its turn. It last said: I need the staging key.",
		}, "\n") {
			t.Errorf("notes here: %v", texts)
		}
		if !reflect.DeepEqual(noticesOf(thereEffects), noticesOf(hereEffects)) {
			t.Errorf("notices there %v, here %v", noticesOf(thereEffects), noticesOf(hereEffects))
		}
		if want := []string{"Lead | Needs you: Keep the old API?", "Lead · Worker | Blocked: No staging access", "Lead · Worker | The run finished."}; !reflect.DeepEqual(noticesOf(hereEffects), want) {
			t.Errorf("notices here: %v", noticesOf(hereEffects))
		}
	})
}
