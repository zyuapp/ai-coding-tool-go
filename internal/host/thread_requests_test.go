package host

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	noCoordinator       = "This thread works under no coordinator, so there is no one to report to and nothing was recorded. Say it in your reply instead."
	outsideCoordination = "Only a coordinator and the threads working under it can raise decisions, so nothing was recorded. Ask the user in your reply instead."
	raisedDecision      = "Raised. The user sees it now, and their answer arrives in this thread as a message. If you cannot go on without it, end your turn."
)

var decisionsFull = fmt.Sprintf("This thread already has %d decisions waiting on the user, so this one was not raised. Wait for their answers, or ask in your reply instead.", domain.MaxOpenDecisions)

// window is the window as the tools reach it. Inputs wait behind hold while it is shut.
type window struct {
	mu    sync.Mutex
	state *application.WorkspaceState
	open  chan struct{}
	host  threadRequestHost
}

func newWindow(initial *application.WorkspaceState) *window {
	w := &window{state: initial, open: make(chan struct{})}
	close(w.open)
	execution := &application.WorkspaceExecutionHost{
		State: func() application.WorkspaceState { return *w.current() },
		Commit: func(next application.WorkspaceState, _ application.WorkspaceInput) {
			w.set(&next)
		},
		Perform: func(context.Context, application.WorkspaceEffect, func(context.Context, application.WorkspaceInput) error) error {
			return nil
		},
	}
	later := func(input application.WorkspaceInput) application.WorkspaceExecution {
		w.mu.Lock()
		open := w.open
		w.mu.Unlock()
		made := newFuture[application.WorkspaceExecution]()
		go func() {
			<-open
			made.settle(application.ExecuteWorkspaceInput(input, execution), nil)
		}()
		return application.WorkspaceExecution{
			Accepted: func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
				execution, _ := made.wait(ctx)
				return execution.Accepted(ctx)
			},
			Completed: func(ctx context.Context) (contracts.WorkspaceCommandResult, error) {
				execution, _ := made.wait(ctx)
				return execution.Completed(ctx)
			},
		}
	}
	w.host = threadRequestHost{
		state: w.current,
		dispatch: func(ctx context.Context, input application.WorkspaceInput) error {
			_, err := later(input).Completed(ctx)
			return err
		},
		execute: later,
		waiters: &ThreadWaiters{},
	}
	return w
}

func (w *window) current() *application.WorkspaceState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

func (w *window) set(next *application.WorkspaceState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state = next
}

func (w *window) hold() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.open = make(chan struct{})
}

func (w *window) release() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.open)
}

func ask(host threadRequestHost, request contracts.ThreadRequest) contracts.ThreadResponse {
	return AnswerThreadRequest(context.Background(), host, request, nil)
}

func answerOf(t *testing.T, response contracts.ThreadResponse) string {
	t.Helper()
	switch response := response.(type) {
	case contracts.ThreadResponseTrue:
		return string(response.Result)
	case contracts.ThreadResponseFalse:
		t.Fatalf("refused: %s", response.Message)
	}
	return ""
}

func refusalOf(t *testing.T, response contracts.ThreadResponse) string {
	t.Helper()
	refused, ok := response.(contracts.ThreadResponseFalse)
	if !ok {
		t.Fatalf("answered: %v", response)
	}
	return refused.Message
}

func finding(recorded bool, note string) string {
	raw, _ := jsonx.Marshal(contracts.FindingResult{Recorded: recorded, Note: note})
	return string(raw)
}

func note(text string) string {
	raw, _ := jsonx.Marshal(noteResult{text})
	return string(raw)
}

func team() *application.WorkspaceState {
	return workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{
			task("lead", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator) }),
			task("worker", func(thread *domain.Thread) { thread.ParentID = new("lead") }),
			task("alone"),
			task("far", func(thread *domain.Thread) {
				thread.RemoteLead = &domain.HeldLead{RemoteLead: domain.RemoteLead{ThreadID: "lead-there", Title: "Lead", Computer: "Laptop"}}
			}),
		}
	})
}

func report(taskID string, state domain.CoordinationState, summary string) contracts.ThreadRequestReport {
	return contracts.ThreadRequestReport{RequestID: "r1", TaskID: taskID, State: state, Summary: summary}
}

func TestAReportIsAnsweredWithWhatTheWindowRecorded(t *testing.T) {
	desk := newWindow(team())
	host := desk.host
	cases := []struct {
		request contracts.ThreadRequestReport
		want    string
	}{
		{report("alone", domain.CoordinationStateDone, "Done"), finding(false, noCoordinator)},
		{report("worker", domain.CoordinationStateWorking, "Halfway"), finding(true, "Noted. Progress is not passed on; report again when you are blocked, done, or failed.")},
		{report("worker", domain.CoordinationStateBlocked, "Needs a token"), finding(true, "Reported. Your coordinator hears it when this turn ends.")},
		{report("far", domain.CoordinationStateDone, "Shipped"), finding(true, "Reported. Your coordinator hears it when this turn ends.")},
	}
	for _, c := range cases {
		if got := answerOf(t, ask(host, c.request)); got != c.want {
			t.Errorf("%s: %s", c.request.TaskID, got)
		}
	}
	state := host.state()
	if state.Threads[2].Report != nil || state.Threads[3].Report == nil || state.Threads[3].Report.Summary != "Shipped" {
		t.Fatal("reports landed on the wrong threads")
	}
	if state.ActionError != nil {
		t.Fatal("a refusal reached the user")
	}
}

func outOfCoordination(desk *window) {
	state := *desk.current()
	threads := make([]*domain.Thread, len(state.Threads))
	copy(threads, state.Threads)
	threads[1] = task("worker")
	state.Threads = threads
	desk.set(&state)
}

func TestAReportOutOfCoordinationOnceTheWindowTakesItRecordsNothing(t *testing.T) {
	desk := newWindow(team())
	desk.hold()
	asked := make(chan contracts.ThreadResponse, 1)
	go func() { asked <- ask(desk.host, report("worker", domain.CoordinationStateDone, "PR #42")) }()
	outOfCoordination(desk)
	desk.release()
	if got := answerOf(t, <-asked); got != finding(false, noCoordinator) {
		t.Fatalf("answer = %s", got)
	}
	if desk.current().Threads[1].Report != nil {
		t.Fatal("a report was recorded")
	}
}

func decision(taskID string) contracts.ThreadRequestDecision {
	return contracts.ThreadRequestDecision{RequestID: "r1", TaskID: taskID, Request: domain.DecisionRequest{Question: "Ship it?", Options: []*domain.DecisionOption{}}}
}

func TestADecisionIsAnsweredWithWhatTheWindowRaised(t *testing.T) {
	waiting := make([]*domain.Decision, domain.MaxOpenDecisions)
	for index := range waiting {
		waiting[index] = &domain.Decision{ID: fmt.Sprintf("d%d", index), Question: fmt.Sprintf("Q%d?", index), Options: []*domain.DecisionOption{}, RaisedAt: float64(index)}
	}
	state := team()
	state.Threads = append(state.Threads, task("full", func(thread *domain.Thread) {
		thread.ParentID, thread.Decisions = new("lead"), waiting
	}))
	host := newWindow(state).host
	cases := map[string]string{
		"alone": finding(false, outsideCoordination), "ghost": finding(false, outsideCoordination), "full": finding(false, decisionsFull),
		"worker": finding(true, raisedDecision), "lead": finding(true, raisedDecision), "far": finding(true, raisedDecision),
	}
	for _, taskID := range []string{"alone", "ghost", "full", "worker", "lead", "far"} {
		if got := answerOf(t, ask(host, decision(taskID))); got != cases[taskID] {
			t.Errorf("%s: %s", taskID, got)
		}
	}
	if len(findThread(host.state().Threads, "full").Decisions) != domain.MaxOpenDecisions || host.state().ActionError != nil {
		t.Fatal("a full thread took another decision, or a refusal reached the user")
	}
}

func TestADecisionOutOfCoordinationOnceTheWindowTakesItIsToldSo(t *testing.T) {
	desk := newWindow(team())
	desk.hold()
	asked := make(chan contracts.ThreadResponse, 1)
	go func() { asked <- ask(desk.host, decision("worker")) }()
	outOfCoordination(desk)
	desk.release()
	if got := answerOf(t, <-asked); got != finding(false, outsideCoordination) {
		t.Fatalf("answer = %s", got)
	}
	if desk.current().Threads[1].Decisions != nil {
		t.Fatal("a decision was raised")
	}
}

func memory(taskID string, change domain.MemoryChange) contracts.ThreadRequestMemory {
	return contracts.ThreadRequestMemory{RequestID: "r1", TaskID: taskID, Change: change.(contracts.ThreadRequestMemoryChange)}
}

func TestAMemoryChangeIsAnsweredWithWhatTheWindowSaved(t *testing.T) {
	notes := []*domain.MemoryNote{{ID: "n1", Text: "Use Sonnet for docs", At: 1}}
	host := newWindow(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{
			task("lead", func(thread *domain.Thread) {
				thread.Role = new(domain.ThreadRoleCoordinator)
				thread.Memory = &domain.CoordinatorMemory{Notes: notes, Revision: 2, Heard: new(2.0)}
			}),
			task("solo"),
		}
	})).host
	remember := domain.MemoryChangeRemember{Text: "Remember this"}
	if got := refusalOf(t, ask(host, memory("solo", remember))); got != "Only a coordinator keeps a memory." {
		t.Fatal(got)
	}
	if got := refusalOf(t, ask(host, memory("ghost", remember))); got != "Only a coordinator keeps a memory." {
		t.Fatal(got)
	}
	steps := []struct {
		change  domain.MemoryChange
		answer  string
		refusal string
	}{
		{domain.MemoryChangeRemember{Text: "  Keep updates short  "}, note("Saved as [n3]. " + readFromNow), ""},
		{domain.MemoryChangeRemember{Text: "   "}, "", "A note needs some text."},
		{domain.MemoryChangePurpose{Purpose: new("Ship the v2 reader")}, note("Purpose set. " + readFromNow), ""},
		{domain.MemoryChangePurpose{Purpose: new("Ship the v2 reader")}, note("Purpose set. " + readFromNow), ""},
		{domain.MemoryChangePurpose{Purpose: new("")}, note("Purpose cleared."), ""},
		{domain.MemoryChangeForget{NoteID: "n1"}, note("Forgot [n1]."), ""},
		{domain.MemoryChangeForget{NoteID: "n1"}, "", "No note has the ID n1."},
	}
	for index, step := range steps {
		response := ask(host, memory("lead", step.change))
		if step.refusal != "" {
			if got := refusalOf(t, response); got != step.refusal {
				t.Errorf("step %d: %s", index, got)
			}
		} else if got := answerOf(t, response); got != step.answer {
			t.Errorf("step %d: %s", index, got)
		}
	}
	lead := host.state().Threads[0]
	if len(lead.Memory.Notes) != 1 || lead.Memory.Notes[0].ID != "n3" || lead.Memory.Notes[0].Text != "Keep updates short" || host.state().ActionError != nil {
		t.Fatalf("notes = %+v", lead.Memory.Notes)
	}
}

func TestAFullMemoryRefusesOneMoreNote(t *testing.T) {
	notes := make([]*domain.MemoryNote, 30)
	for index := range notes {
		notes[index] = &domain.MemoryNote{ID: fmt.Sprintf("n%d", index+1), Text: fmt.Sprintf("Note %d", index), At: 1}
	}
	host := newWindow(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{task("lead", func(thread *domain.Thread) {
			thread.Role = new(domain.ThreadRoleCoordinator)
			thread.Memory = &domain.CoordinatorMemory{Notes: notes, Revision: 30, Heard: new(30.0)}
		})}
	})).host
	if got := refusalOf(t, ask(host, memory("lead", domain.MemoryChangeRemember{Text: "One more"}))); got != "Memory holds at most 30 notes. Forget one first." {
		t.Fatal(got)
	}
	if len(host.state().Threads[0].Memory.Notes) != 30 || host.state().ActionError != nil {
		t.Fatal("the full memory took a note")
	}
}
