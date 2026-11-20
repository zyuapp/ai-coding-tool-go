package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const stopsAt = 1_800_000_000_000

func stopsLead(edits ...func(*domain.Thread)) *domain.Thread {
	return runsTask("lead", append([]func(*domain.Thread){func(thread *domain.Thread) {
		thread.Role = new(domain.ThreadRoleCoordinator)
		thread.Title = "Lead"
	}}, edits...)...)
}

func stopsFar(edits ...func(*domain.Thread)) *domain.Thread {
	return runsTask("far", append([]func(*domain.Thread){func(thread *domain.Thread) {
		thread.Title = "Far"
		thread.RemoteLead = &domain.HeldLead{RemoteLead: domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: "lead", Title: "Lead", Computer: "This Mac"}}}
	}}, edits...)...)
}

func stopsStudio(state *WorkspaceState) *PairedComputer {
	return &PairedComputer{ComputerLink: domain.ComputerLink{ID: "studio", Name: "studio", Host: "studio.tail.ts.net", Status: "connected", PairedAt: 1, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()}, State: state}
}

func stopsPaired(computers ...*PairedComputer) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.Computers.Paired = computers
	}
}

func TestOutstandingStopsStopAllCovers(t *testing.T) {
	runsPorted(t, func() {
		state := runsWorkspace(runsThreads(stopsLead(), runsTask("one"), runsTask("two")), runsRunning(runsActive("one", "run-1")), func(state *WorkspaceState) {
			state.QuietStops = []string{"run-gone"}
			state.QuietSessions = []*QuietSession{{TaskID: "two", At: stopsAt - 60*60_000}}
		})
		far := stopsFar()
		stopped, effects := WithStopAll(state, "lead", StopAllCover{
			Cancelled: []*StopAllCoverCancelled{{RunID: "run-1"}},
			Hushed:    []string{"one"},
			Elsewhere: []*RemoteWork{{Thread: *far, Computer: *stopsStudio(nil), RunID: new("run-far"), ProcessIDs: []string{"wf"}}},
			Landing:   []*StopAllCoverLanding{{ID: "p1", Computer: "studio"}},
		}, stopsAt)
		if !slices.Equal(stopped.QuietStops, []string{"run-1"}) {
			t.Fatalf("quietStops = %v", stopped.QuietStops)
		}
		if len(stopped.QuietSessions) != 1 || *stopped.QuietSessions[0] != (QuietSession{TaskID: "one", At: stopsAt}) {
			t.Fatalf("quietSessions = %+v", stopped.QuietSessions)
		}
		lead := runsThreadIn(stopped.Threads, "lead")
		stop := lead.RemoteStops["far"]
		if len(lead.RemoteStops) != 1 || stop.Computer != "studio" || *stop.RunID != "run-far" || stop.Answer == nil || stop.Answer.At != stopsAt || stop.Answer.RunID != nil {
			t.Fatalf("remoteStops = %+v", lead.RemoteStops)
		}
		if len(lead.CaughtStarts) != 1 || *lead.CaughtStarts["p1"] != (domain.CaughtStart{Computer: "studio"}) {
			t.Fatalf("caughtStarts = %+v", lead.CaughtStarts)
		}
		forward, ok := effects[0].(WorkspaceEffectComputerForward)
		if len(effects) != 1 || !ok || forward.ID != "studio" || len(forward.Inputs) != 2 {
			t.Fatalf("effects = %+v", effects)
		}
		cancel, isCancel := forward.Inputs[0].(contracts.RunControlCommandRunCancel)
		stopProcess, isStop := forward.Inputs[1].(contracts.RunControlCommandRunStopProcess)
		if !isCancel || *cancel.TaskID != "far" || !isStop || *stopProcess.TaskID != "far" || stopProcess.ProcessID != "wf" {
			t.Fatalf("inputs = %+v", forward.Inputs)
		}
	})
}

func TestOutstandingStopsStopIntent(t *testing.T) {
	runsPorted(t, func() {
		state := runsWorkspace(func(state *WorkspaceState) { state.QuietStops = []string{"run-1"} })
		intent := func(state *WorkspaceState, runID string, cancelling bool) string {
			run := runsActive("a", runID, func(run *ActiveRunRunning) {
				if cancelling {
					run.Cancelling = new(true)
				}
			})
			if stop := StopIntentOf(state, run); stop != nil {
				return string(*stop)
			}
			return ""
		}
		if intent(state, "run-1", false) != "stop-all" || intent(state, "run-1", true) != "stop-all" || intent(state, "run-2", true) != "user" || intent(state, "run-2", false) != "" {
			t.Fatal("stop intent")
		}
		if intent(WithRunAnswered(state, "run-1"), "run-1", false) != "" {
			t.Fatal("an answered stop still holds the run")
		}
	})
}

func TestOutstandingStopsTurnBegun(t *testing.T) {
	runsPorted(t, func() {
		state := runsWorkspace(func(state *WorkspaceState) { state.QuietSessions = []*QuietSession{{TaskID: "one", At: stopsAt}} })
		answering, answers := WithTurnBegun(state, "one", true, stopsAt+60_000)
		if !answers || len(answering.QuietSessions) != 0 {
			t.Fatal("the agent's own turn in the window answers the stop")
		}
		if spent, answers := WithTurnBegun(state, "one", false, stopsAt); answers || len(spent.QuietSessions) != 0 {
			t.Fatal("a run someone sent spends the stop without answering it")
		}
		if _, answers := WithTurnBegun(state, "one", true, stopsAt+3*60_000); answers {
			t.Fatal("a turn long after the stop answered it")
		}
		if untouched, answers := WithTurnBegun(state, "two", true, stopsAt); untouched != state || answers {
			t.Fatal("another thread's turn touched the stop")
		}
	})
}

func TestOutstandingStopsRemoteTurn(t *testing.T) {
	runsPorted(t, func() {
		stopping := stopsLead(func(thread *domain.Thread) {
			thread.RemoteStops = map[string]*domain.RemoteStop{"far": {Computer: "studio", RunID: new("run-far")}}
		})
		going := OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "studio", runsActive("far", "run-far"), false, stopsAt)
		if going.Spent || going.Held(*stopping).RemoteStops["far"] != stopping.RemoteStops["far"] {
			t.Fatal("a run the stop is waiting on spent it")
		}
		ended := OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "studio", nil, true, stopsAt)
		if !ended.StopAll || ended.Answered || !ended.Spent || ended.Held(*stopping).RemoteStops != nil {
			t.Fatalf("ended = %+v", ended)
		}
		if OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "attic", nil, true, stopsAt).StopAll {
			t.Fatal("a stop was heard from a computer it was not sent to")
		}
		carriedOn := OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "studio", runsActive("far", "run-later"), false, stopsAt)
		if carriedOn.Held(*stopping).RemoteStops != nil {
			t.Fatal("a run of its own first is still the stop's")
		}
	})
}

func TestOutstandingStopsRemoteAnswer(t *testing.T) {
	runsPorted(t, func() {
		stopping := stopsLead(func(thread *domain.Thread) {
			thread.RemoteStops = map[string]*domain.RemoteStop{"far": {Computer: "studio", Answer: &domain.RemoteStopAnswer{At: stopsAt}}}
		})
		agents := runsActive("far", "run-a", func(run *ActiveRunRunning) { run.AgentInitiated = new(true) })
		held := OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "studio", agents, false, stopsAt+60_000).Held(*stopping)
		if answer := held.RemoteStops["far"].Answer; answer == nil || answer.At != stopsAt || *answer.RunID != "run-a" {
			t.Fatalf("held = %+v", held.RemoteStops)
		}
		answered := OutstandingStopsRemoteTurnStop(&held, stopsFar(), "studio", nil, true, stopsAt+90_000)
		if !answered.StopAll || !answered.Answered {
			t.Fatal("the agent's answer did not end quietly")
		}
		late := OutstandingStopsRemoteTurnStop(stopping, stopsFar(), "studio", agents, false, stopsAt+3*60_000)
		if late.Held(*stopping).RemoteStops != nil {
			t.Fatal("a late turn answered the stop")
		}
	})
}

func TestOutstandingStopsCaughtStart(t *testing.T) {
	runsPorted(t, func() {
		catching := stopsLead(func(thread *domain.Thread) {
			thread.CaughtStarts = map[string]*domain.CaughtStart{"p1": {Computer: "studio"}}
		})
		if !CatchesStart(catching, "p1") {
			t.Fatal("not caught")
		}
		if computer, ok := CaughtStartComputer(catching, "p1"); !ok || computer != "studio" {
			t.Fatal("caught on the wrong computer")
		}
		landed := WithStartLanded(catching, "p1", "far")
		if CatchesStart(landed, "p1") {
			t.Fatal("a landed start is still caught")
		}
		if computer, ok := CaughtStartComputer(landed, "p1"); !ok || computer != "studio" {
			t.Fatal("a landed start forgot its computer")
		}
		formerLead := *catching
		formerLead.Role = nil
		if WithStartLanded(&formerLead, "p1", "far").CaughtStarts != nil {
			t.Fatal("one no longer a coordinator kept the start")
		}
		member := stopsFar(func(thread *domain.Thread) { thread.RemoteLead.ProposalID = new("p1") })
		ended := OutstandingStopsRemoteTurnStop(landed, member, "studio", nil, true, stopsAt)
		if !ended.StopAll || ended.Held(*landed).CaughtStarts != nil {
			t.Fatal("a caught start's end did not settle it")
		}
		if WithoutCaughtStart(catching, "p1").CaughtStarts != nil || CatchesStart(nil, "p1") {
			t.Fatal("caught start bookkeeping")
		}
	})
}

func TestOutstandingStopsKeptUntilComputerSays(t *testing.T) {
	runsPorted(t, func() {
		holding := stopsLead(func(thread *domain.Thread) {
			thread.RemoteStops = map[string]*domain.RemoteStop{"far": {Computer: "studio", RunID: new("run-far")}}
			thread.CaughtStarts = map[string]*domain.CaughtStart{"p1": {Computer: "studio", ThreadID: new("landed")}}
		})
		silent := runsWorkspace(runsThreads(holding), stopsPaired(stopsStudio(nil)))
		if WithStopsKept(silent) != silent {
			t.Fatal("a computer yet to answer lost the stops")
		}
		landedUnder := runsTask("landed", func(thread *domain.Thread) {
			thread.RemoteLead = &domain.HeldLead{RemoteLead: domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: "lead", Title: "Lead", Computer: "This Mac"}}}
		})
		answered := runsWorkspace(runsThreads(holding), stopsPaired(stopsStudio(runsWorkspace(runsThreads(stopsFar(), landedUnder)))))
		if WithStopsKept(answered) != answered {
			t.Fatal("both still under it there, yet let go")
		}
		movedOut := runsWorkspace(runsThreads(holding), stopsPaired(stopsStudio(runsWorkspace(runsThreads(runsTask("far"), runsTask("landed"))))))
		if lead := runsThreadIn(WithStopsKept(movedOut).Threads, "lead"); lead.RemoteStops != nil || lead.CaughtStarts != nil {
			t.Fatal("threads moved out kept their stops")
		}
		unpaired := runsWorkspace(runsThreads(holding))
		if runsThreadIn(WithStopsKept(unpaired).Threads, "lead").RemoteStops != nil {
			t.Fatal("an unpaired computer's stop was kept")
		}
	})
}

func TestOutstandingStopsLapsedCoordinator(t *testing.T) {
	runsPorted(t, func() {
		before := stopsLead(func(thread *domain.Thread) {
			thread.RemoteStops = map[string]*domain.RemoteStop{"far": {Computer: "studio"}}
			thread.CaughtStarts = map[string]*domain.CaughtStart{"p1": {Computer: "studio"}}
		})
		after := *before
		after.Role = nil
		state := WithoutLapsedStops(runsWorkspace(runsThreads(&after)), []*ThreadChange{{ID: "lead", Before: before, After: &after}})
		lead := runsThreadIn(state.Threads, "lead")
		if lead.RemoteStops != nil || len(lead.CaughtStarts) != 1 {
			t.Fatalf("lead = %+v", lead.CoordinationThread)
		}
	})
}
