package store

import (
	"slices"
	"testing"
)

type testStart struct {
	taskID, runID, title string
}

func (s *testStart) StartTaskID() string        { return s.taskID }
func (s *testStart) StartRunID() string         { return s.runID }
func (s *testStart) StartPolicy() string        { return "confirm" }
func (s *testStart) SetStartTitle(title string) { s.title = title }

func TestNewStartSupersedesOlderStartsOfItsThread(t *testing.T) {
	ledger := NewRunLedger[*testStart]()
	for _, start := range []*testStart{{"t", "old", ""}, {"other", "side", ""}, {"t", "older", ""}, {"t", "new", ""}} {
		ledger.BeginStart(start)
	}
	var superseded []string
	for _, start := range ledger.SupersedeStarts("t", "new") {
		superseded = append(superseded, start.runID)
	}
	if !slices.Equal(superseded, []string{"old", "older"}) {
		t.Fatalf("superseded %v", superseded)
	}
	if _, ok := ledger.PendingStart("t", "new"); !ok || !ledger.StartsFor("other") {
		t.Fatal("the new start and other threads' starts stay")
	}
}

func TestTerminalEventsCloseTheGate(t *testing.T) {
	state := &RunEventGateState{}
	if !AcceptRunEvent(state, 1, "run.started", "") || !AcceptRunEvent(state, 2, "run.status", "failed") {
		t.Fatal("events in order are accepted")
	}
	if *state != (RunEventGateState{LastSequence: 2, Terminal: true}) {
		t.Fatalf("state %+v", state)
	}
	if AcceptRunEvent(state, 3, "assistant.delta", "") || AcceptRunEvent(state, 2, "run.status", "failed") {
		t.Fatal("late events are ignored")
	}
}

func TestTransportLossFailsOnlyOpenRuns(t *testing.T) {
	states := []*CorrelatedRunState{
		{TaskID: "task-1", RunID: "run-1", RunEventGateState: RunEventGateState{LastSequence: 3}},
		{TaskID: "task-2", RunID: "run-2", RunEventGateState: RunEventGateState{LastSequence: 8, Terminal: true}},
	}
	events := FailedEventsForTransportLoss(slices.Values(states), "Agent process exited with code 1.")
	if !slices.Equal(events, []RunStatusEvent{{Type: "run.status", TaskID: "task-1", RunID: "run-1", Sequence: 4, Status: "failed", Message: "Agent process exited with code 1."}}) {
		t.Fatalf("events %+v", events)
	}
}

func TestAgentInitiatedTurnTakesTheThreadsCeiling(t *testing.T) {
	ledger := NewRunLedger[*testStart]()
	ledger.BeginStart(&testStart{"t", "run-1", ""})
	ledger.Accept(RunEventHeader{Type: "run.started", TaskID: "t", RunID: "run-1", Sequence: 1})
	ledger.Accept(RunEventHeader{Type: "run.status", TaskID: "t", RunID: "run-1", Sequence: 2, Status: "succeeded"})
	if !ledger.Accept(RunEventHeader{Type: "run.started", TaskID: "t", RunID: "run-2", Sequence: 1, AgentInitiated: true}) {
		t.Fatal("an agent-initiated turn opens its own books")
	}
	if session, _ := ledger.Session("t"); session.RunID != "run-2" || session.Policy != "confirm" {
		t.Fatalf("session %+v", session)
	}
	start := &testStart{"t", "run-3", "Old"}
	ledger.BeginStart(start)
	ledger.RetitleStarts("t", "New")
	if taken, _ := ledger.TakeStart("t", "run-3"); taken.title != "New" {
		t.Fatalf("title %q", taken.title)
	}
}
