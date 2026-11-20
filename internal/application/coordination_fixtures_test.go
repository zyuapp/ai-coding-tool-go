package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// coordPorted runs `body`, skipping the test while it reaches a function another group has yet to port.
func coordPorted(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if text := fmt.Sprint(recovered); strings.Contains(text, "unported: ") {
				t.Skipf("blocked: %s", text)
			}
			panic(recovered)
		}
	}()
	body()
}

// coordTask is a thread as the TS fixtures' task() makes one.
func coordTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title = id, id
	thread.Engine = domain.AgentEngine("claude")
	thread.ExecutionPolicy = domain.ExecutionPolicyConfirm
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = domain.ContinuationStatusNone
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt = 1
	for _, edit := range edits {
		edit(thread)
	}
	if thread.RankedAt == 0 {
		thread.RankedAt = domain.ThreadLastOutputAt(thread)
	}
	return thread
}

func coordLead(id string, edits ...func(*domain.Thread)) *domain.Thread {
	return coordTask(id, append([]func(*domain.Thread){func(thread *domain.Thread) {
		thread.Role = new(domain.ThreadRoleCoordinator)
	}}, edits...)...)
}

func coordUnder(leadID string) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.ParentID = new(leadID) }
}

func coordTitled(title string) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.Title = title }
}

// coordWorkspace is the empty workspace with `edits` made to it.
func coordWorkspace(edits ...func(*WorkspaceState)) *WorkspaceState {
	state := *EmptyWorkspaceState(nil)
	for _, edit := range edits {
		edit(&state)
	}
	return &state
}

func coordHolding(threads ...*domain.Thread) func(*WorkspaceState) {
	return func(state *WorkspaceState) { state.Threads = threads }
}

func coordRunning(runs ...ActiveRun) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.ActiveRuns = map[string]ActiveRun{}
		for _, run := range runs {
			state.ActiveRuns[RunRecord(run).TaskID] = run
		}
	}
}

// coordRun is a run going on `taskID`, as the TS fixtures' activeRun() makes one.
func coordRun(taskID, runID string, edits ...func(*ActiveRunRunning)) ActiveRun {
	run := ActiveRunRunning{
		TaskID: taskID, RunID: runID, Status: ActiveRunRunningStatusRunning, Origin: ActiveRunRunningOriginComposer,
		ReportedIssues: []string{}, Before: ThreadMark{UpdatedAt: 1},
	}
	for _, edit := range edits {
		edit(&run)
	}
	return run
}

// coordComputer is a paired computer whose line is up, holding `state`.
func coordComputer(id string, state *WorkspaceState) *PairedComputer {
	return &PairedComputer{
		ComputerLink: domain.ComputerLink{ID: id, Name: id, Host: id + ".tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1, Capabilities: contracts.ComputerCapabilitiesComputerCapabilities()},
		State:        state,
	}
}

func coordPairedWith(computers ...*PairedComputer) func(*WorkspaceState) {
	return func(state *WorkspaceState) {
		state.Computers.Name = "This Mac"
		state.Computers.Paired = computers
	}
}

// coordFarLead names the coordinator "lead" on "This Mac" as a thread on a paired computer holds it.
func coordFarLead(thread *domain.Thread) {
	thread.RemoteLead = &domain.HeldLead{RemoteLead: domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: "lead", Title: "Lead", Computer: "This Mac"}}}
}

func coordIDs[T any](items []T, id func(T) string) []string {
	out := make([]string, len(items))
	for index, item := range items {
		out[index] = id(item)
	}
	return out
}

func coordThreadIDs(threads []*domain.Thread) []string {
	return coordIDs(threads, func(thread *domain.Thread) string { return thread.ID })
}

func coordThreadOf(state *WorkspaceState, id string) *domain.Thread {
	return coordThread(state.Threads, id)
}

var coordNone = jsonx.Set[string]{}
