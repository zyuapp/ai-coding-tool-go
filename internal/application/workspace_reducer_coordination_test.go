package application

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The coordination slice's answer joined into `state`.
func coordinated(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	return Applied(state, ReduceCoordination(state, input))
}

func refusalOf(transition WorkspaceTransition) string {
	if refused, ok := transition.Result.(contracts.WorkspaceCommandResultFalse); ok {
		return refused.Message
	}
	return ""
}

func TestAThreadMovesUnderACoordinatorAndBackOut(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(coordLead("lead"), coordTask("worker"), coordLead("other")))
		joined := coordinated(state, contracts.TaskCommandTaskSetCoordinator{TaskID: "worker", CoordinatorID: new("lead")})
		if parent := joined.State.Threads[1].ParentID; parent == nil || *parent != "lead" {
			t.Errorf("joined: %v", parent)
		}
		left := coordinated(&joined.State, contracts.TaskCommandTaskSetCoordinator{TaskID: "worker"})
		if left.State.Threads[1].ParentID != nil {
			t.Error("left")
		}
		if refusalOf(coordinated(state, contracts.TaskCommandTaskSetCoordinator{TaskID: "other", CoordinatorID: new("lead")})) != joinRefused {
			t.Error("a coordinator never works under another")
		}
		if refusalOf(coordinated(state, contracts.TaskCommandTaskSetCoordinator{TaskID: "worker", CoordinatorID: new("worker")})) != joinRefused {
			t.Error("only a coordinator takes threads")
		}
		resolved := coordWorkspace(coordHolding(coordLead("lead"), coordTask("worker", coordUnder("lead"), func(thread *domain.Thread) { thread.ResolvedAt = new(3.0) }), coordLead("other")))
		if moved := coordinated(resolved, contracts.TaskCommandTaskSetCoordinator{TaskID: "worker"}); moved.State.Threads[1].ResolvedAt != nil {
			t.Error("a thread moved out from under its coordinator leaves its resolution behind")
		}
		if other := coordinated(resolved, contracts.TaskCommandTaskSetCoordinator{TaskID: "worker", CoordinatorID: new("other")}); other.State.Threads[1].ResolvedAt != nil {
			t.Error("and so does one moved under another")
		}
	})
}

func TestProgressIsNotNewsButADoneThreadLeavesItsCoordinatorANote(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(coordLead("lead"), coordTask("worker", coordUnder("lead"))))
		working := coordinated(state, WorkspaceEventCoordinationReported{TaskID: "worker", State: domain.CoordinationStateWorking, Summary: "Profiling"})
		if report := working.State.Threads[1].Report; report == nil || report.State != domain.CoordinationStateWorking {
			t.Error("the report is kept")
		}
		if working.State.Threads[0].CoordinationNotes != nil {
			t.Error("progress is not news")
		}
		done := coordinated(state, WorkspaceEventCoordinationReported{TaskID: "worker", State: domain.CoordinationStateDone, Summary: "PR #42"})
		coordMatch(t, done.State.Threads[0].CoordinationNotes[0].Text, `reported done: PR #42`, "done")
		if _, ok := done.Result.(contracts.WorkspaceCommandResultTrue).Answer.(contracts.CoordinationAnswerTrue); !ok {
			t.Error("the report is recorded")
		}
		alone := coordinated(coordWorkspace(coordHolding(coordTask("alone"))), WorkspaceEventCoordinationReported{TaskID: "alone", State: domain.CoordinationStateDone, Summary: "x"})
		if declined, ok := alone.Result.(contracts.WorkspaceCommandResultTrue).Answer.(contracts.CoordinationAnswerFalse); !ok || declined.Reason != NoCoordinator {
			t.Errorf("a thread under no coordinator is told why: %+v", alone.Result)
		}
	})
}

func TestAThreadRaisesOnlySoManyDecisionsAtOnce(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(coordLead("lead"), coordTask("worker", coordUnder("lead"))), func(state *WorkspaceState) { state.Notifications = true })
		for index := range domain.MaxOpenDecisions {
			state = coordStateOf(coordinated(state, WorkspaceEventCoordinationDecisionRaised{TaskID: "worker", Request: domain.DecisionRequest{Question: fmt.Sprintf("Q%d?", index), Options: []*domain.DecisionOption{}}}))
		}
		past := coordinated(state, WorkspaceEventCoordinationDecisionRaised{TaskID: "worker", Request: domain.DecisionRequest{Question: "One more?", Options: []*domain.DecisionOption{}}})
		if !jsonx.SameSlice(past.State.Threads, state.Threads) || len(past.Effects) != 0 {
			t.Error("one past them neither waits nor is announced")
		}
		if declined, ok := past.Result.(contracts.WorkspaceCommandResultTrue).Answer.(contracts.CoordinationAnswerFalse); !ok || declined.Reason != DecisionsFull {
			t.Errorf("its tool call is told why: %+v", past.Result)
		}
		stray := coordinated(coordWorkspace(coordHolding(coordTask("alone"))), WorkspaceEventCoordinationDecisionRaised{TaskID: "alone", Request: domain.DecisionRequest{Question: "Q?", Options: []*domain.DecisionOption{}}})
		if stray.State.Threads[0].Decisions != nil {
			t.Error("a thread outside any coordinator has nowhere to show a decision")
		}
	})
}

func TestAFloodingThreadCrowdsOutItsOwnNotesNeverAnotherThreadsNews(t *testing.T) {
	coordPorted(t, func() {
		member := func(id string) *domain.Thread {
			return coordTask(id, coordUnder("lead"), coordTitled(strings.ToUpper(id[:1])+id[1:]))
		}
		state := coordWorkspace(coordHolding(coordLead("lead"), member("one"), member("two")))
		flooded := coordStateOf(coordinated(state, WorkspaceEventCoordinationReported{TaskID: "two", State: domain.CoordinationStateDone, Summary: "Shipped PR #9"}))
		for index := range 60 {
			flooded = coordStateOf(coordinated(flooded, WorkspaceEventCoordinationReported{TaskID: "one", State: domain.CoordinationStateBlocked, Summary: fmt.Sprintf("Stuck %d", index)}))
		}
		notes := flooded.Threads[0].CoordinationNotes
		if len(notes) != 50 {
			t.Fatalf("notes: %d", len(notes))
		}
		var two, one []*domain.CoordinationNote
		for _, note := range notes {
			if note.ThreadID == "two" {
				two = append(two, note)
			} else {
				one = append(one, note)
			}
		}
		if len(two) != 1 {
			t.Error("another thread's news is kept")
		}
		coordMatch(t, notes[len(notes)-1].Text, `Stuck 59`, "the newest of the flood is kept")
		coordMatch(t, one[0].Text, `Stuck 11$`, "its oldest given up")
	})
}

func TestACoordinatorRemembersAndForgetsWhatItIsTold(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(coordLead("lead")))
		remember := func(state *WorkspaceState, change domain.MemoryChange) *WorkspaceState {
			return coordStateOf(coordinated(state, WorkspaceEventCoordinationRemembered{TaskID: "lead", Change: change}))
		}
		remembered := remember(state, domain.MemoryChangeRemember{Text: "  Propose threads before starting them  "})
		memory := remembered.Threads[0].Memory
		if memory == nil || len(memory.Notes) != 1 || memory.Notes[0].ID != "n1" || memory.Notes[0].Text != "Propose threads before starting them" || coordValueOr(memory.Heard, 0) != 1 {
			t.Fatalf("remembered: %+v", memory)
		}
		purposed := remember(remembered, domain.MemoryChangePurpose{Purpose: new("Ship the v2 reader")})
		forgot := remember(purposed, domain.MemoryChangeForget{NoteID: "n1"})
		if memory := forgot.Threads[0].Memory; coordValueOr(memory.Purpose, "") != "Ship the v2 reader" || len(memory.Notes) != 0 || memory.Revision != 3 || coordValueOr(memory.Heard, 0) != 3 {
			t.Errorf("forgot: %+v", memory)
		}
		if again := remember(forgot, domain.MemoryChangeRemember{Text: "Run at most two at a time"}); again.Threads[0].Memory.Notes[0].ID != "n4" {
			t.Error("a note's ID is never reused")
		}
		notes := make([]*domain.MemoryNote, 30)
		for index := range notes {
			notes[index] = &domain.MemoryNote{ID: fmt.Sprintf("n%d", index+1), Text: fmt.Sprintf("Note %d", index), At: 1}
		}
		full := coordWorkspace(coordHolding(coordLead("lead", func(thread *domain.Thread) {
			thread.Memory = &domain.CoordinatorMemory{Notes: notes, Revision: 30, Heard: new(30.0)}
		})))
		refused := coordinated(full, WorkspaceEventCoordinationRemembered{TaskID: "lead", Change: domain.MemoryChangeRemember{Text: "One more"}})
		if !jsonx.SameSlice(refused.State.Threads, full.Threads) || refused.State.ActionError != nil {
			t.Error("the coordinator's tool hears the refusal, not the user")
		}
		if declined, ok := refused.Result.(contracts.WorkspaceCommandResultTrue).Answer.(contracts.CoordinationAnswerFalse); !ok || declined.Reason != "Memory holds at most 30 notes. Forget one first." {
			t.Errorf("refusal: %+v", refused.Result)
		}
		solo := coordWorkspace(coordHolding(coordTask("solo")))
		for _, input := range []WorkspaceInput{
			contracts.TaskCommandMemoryAdd{TaskID: "solo", Text: "Remember this"},
			contracts.TaskCommandMemorySetPurpose{TaskID: "solo", Purpose: new("Ship it")},
			WorkspaceEventCoordinationRemembered{TaskID: "solo", Change: domain.MemoryChangeRemember{Text: "Remember this"}},
		} {
			next := coordinated(solo, input)
			if !jsonx.SameSlice(next.State.Threads, solo.Threads) || refusalOf(next) != "" {
				t.Errorf("a non-coordinator keeps no memory: %T", input)
			}
		}
	})
}

func TestAThreadThatIsWorkingOrWaitingOnTheUserCannotBeResolved(t *testing.T) {
	coordPorted(t, func() {
		finished := func(id string, edits ...func(*domain.Thread)) *domain.Thread {
			return coordTask(id, append([]func(*domain.Thread){coordUnder("lead"), outcome(domain.ThreadOutcomeFinished)}, edits...)...)
		}
		state := coordWorkspace(coordHolding(coordLead("lead"), finished("busy"), finished("approval"), finished("asking", func(thread *domain.Thread) {
			thread.Decisions = []*domain.Decision{{ID: "d1", Question: "Keep it?", Options: []*domain.DecisionOption{}, RaisedAt: 1}}
		}), finished("worker")), coordRunning(coordRun("busy", "run-b"), coordRun("approval", "run-a", func(run *ActiveRunRunning) { run.Status = ActiveRunRunningStatusAwaitingApproval })))
		for _, taskID := range []string{"busy", "approval", "asking"} {
			refused := coordinated(state, contracts.TaskCommandTaskResolve{TaskID: taskID})
			if refusalOf(refused) != "Finish or answer what this thread is waiting on first." || !jsonx.SameSlice(refused.State.Threads, state.Threads) {
				t.Errorf("%s: %q", taskID, refusalOf(refused))
			}
		}
		resolved := coordinated(state, contracts.TaskCommandTaskResolve{TaskID: "worker"})
		worker := coordThreadOf(&resolved.State, "worker")
		if worker.ResolvedAt == nil || worker.Outcome != nil {
			t.Error("its last run is filed away with it")
		}
		if again := coordinated(&resolved.State, contracts.TaskCommandTaskResolve{TaskID: "worker"}); !jsonx.SameSlice(again.State.Threads, resolved.State.Threads) {
			t.Error("resolving twice changes nothing")
		}
		if reopened := coordinated(&resolved.State, contracts.TaskCommandTaskReopen{TaskID: "worker"}); coordThreadOf(&reopened.State, "worker").ResolvedAt != nil {
			t.Error("reopened")
		}
	})
}

func TestAThreadEndingItsTurnWakesAFreeCoordinatorAndWaitsForABusyOne(t *testing.T) {
	coordPorted(t, func() {
		worker := coordTask("worker", coordUnder("lead"), coordTitled("Fix login"))
		statuses := func(ids ...string) func(*WorkspaceState) {
			return func(state *WorkspaceState) {
				state.RunStatuses = map[string]ThreadRunStatus{}
				for _, id := range ids {
					state.RunStatuses[id] = ThreadRunStatusRunning
				}
			}
		}
		ended := func(taskID, runID string, status domain.RunStatus) WorkspaceInput {
			return coordRunEvent(contracts.RunEventRunStatus{RunEventBase: contracts.RunEventBase{TaskID: taskID, RunID: runID, Sequence: 1}, Status: status})
		}
		wakeOf := func(transition WorkspaceTransition) *WorkspaceEffectResolveRunWorkspace {
			for _, effect := range transition.Effects {
				if wake, ok := effect.(WorkspaceEffectResolveRunWorkspace); ok {
					return &wake
				}
			}
			return nil
		}
		free := coordWorkspace(coordHolding(coordLead("lead"), worker), coordRunning(coordRun("worker", "run-w")), statuses("worker"))
		woke := Reduce(free, ended("worker", "run-w", domain.RunStatusSucceeded))
		wake := wakeOf(woke)
		if wake == nil {
			t.Fatal("a free coordinator is woken")
		}
		pending := woke.State.PendingRuns[wake.PendingID]
		if pending == nil || coordValueOr(pending.TaskID, "") != "lead" {
			t.Fatalf("the wake is the coordinator's: %+v", pending)
		}
		if _, ok := pending.MessageOrigin.(domain.MessageOriginCoordination); !ok {
			t.Error("the wake comes from coordination")
		}
		coordMatch(t, pending.Text, `"Fix login" ended its turn`, "the news")

		busyLead := coordWorkspace(coordHolding(coordLead("lead"), worker), coordRunning(coordRun("worker", "run-w"), coordRun("lead", "run-l")), statuses("worker", "lead"))
		waiting := Reduce(busyLead, ended("worker", "run-w", domain.RunStatusSucceeded))
		if wakeOf(waiting) != nil || len(waiting.State.Threads[0].CoordinationNotes) != 1 {
			t.Error("a busy coordinator keeps the news")
		}
		if wakeOf(Reduce(&waiting.State, ended("lead", "run-l", domain.RunStatusSucceeded))) == nil {
			t.Error("the coordinator hears it once it comes free")
		}
		if wakeOf(Reduce(&waiting.State, ended("lead", "run-l", domain.RunStatusCancelled))) != nil {
			t.Error("a coordinator the user stopped is not woken straight back up")
		}
	})
}

func TestANextStepReachesTheThreadAsTheUsersOwnMessage(t *testing.T) {
	coordPorted(t, func() {
		failing := coordTask("worker", coordUnder("lead"), coordTitled("Fix login"), func(thread *domain.Thread) {
			thread.Brief = new(coordBrief)
			thread.PullRequest = &domain.PullRequestLink{Number: 212, URL: "https://github.com/o/r/pull/212", Title: new("Fix it"), State: new(domain.PullRequestStateOpen), PullRequestNeeds: domain.PullRequestNeeds{Checks: new(domain.PullRequestChecksFailing)}}
		})
		state := coordWorkspace(coordHolding(coordLead("lead"), failing))
		stepping := coordinated(state, contracts.TaskCommandCoordinationNextStep{TaskID: "worker", Step: domain.NextStepFixCi})
		var sent *PendingRun
		for _, pending := range stepping.State.PendingRuns {
			sent = pending
		}
		if sent == nil || sent.Text != "The pull request's checks are failing. Find out why, fix them, and push." {
			t.Fatalf("sent: %+v", sent)
		}
		if refusalOf(coordinated(state, contracts.TaskCommandCoordinationNextStep{TaskID: "worker", Step: domain.NextStepMerge})) != stepGone {
			t.Error("failing checks cannot be merged")
		}
		solo := failing
		solo.ParentID = nil
		if refusalOf(coordinated(coordWorkspace(coordHolding(solo)), contracts.TaskCommandCoordinationNextStep{TaskID: "worker", Step: domain.NextStepFixCi})) != stepGone {
			t.Error("only a thread under a coordinator takes a next step")
		}
	})
}

func TestADeclinedSuggestionIsHeardQuietly(t *testing.T) {
	proposal := func(id, title string) *domain.ThreadProposal {
		return &domain.ThreadProposal{ID: id, Title: title, Prompt: "Do " + id, Brief: new(coordBrief), Place: domain.ProposalPlace{Worktree: new(false)}, ProposedAt: 1}
	}
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(coordLead("lead", func(thread *domain.Thread) {
			thread.Proposals = []*domain.ThreadProposal{proposal("p1", "Docs"), proposal("p2", "Tests"), proposal("p3", "Bench")}
		})))
		proposed := func(state *WorkspaceState, input WorkspaceInput) *WorkspaceState {
			return coordStateOf(Applied(state, ReduceProposals(state, input)))
		}
		one := proposed(state, contracts.TaskCommandProposalDismiss{TaskID: "lead", ProposalID: new("p1")})
		lead := coordThreadOf(one, "lead")
		if got := coordIDs(lead.Proposals, func(item *domain.ThreadProposal) string { return item.ID }); strings.Join(got, ",") != "p2,p3" {
			t.Errorf("left: %v", got)
		}
		note := lead.CoordinationNotes[len(lead.CoordinationNotes)-1]
		if note.Text != `The user declined the suggested thread "Docs".` || !coordTrue(note.Quiet) {
			t.Errorf("note: %+v", note)
		}
		rest := coordThreadOf(proposed(one, contracts.TaskCommandProposalDismiss{TaskID: "lead"}), "lead")
		if len(rest.Proposals) != 0 || rest.CoordinationNotes[len(rest.CoordinationNotes)-1].Text != `The user declined the suggested threads "Tests" and "Bench".` {
			t.Errorf("dismissing all says so once: %+v", rest.CoordinationNotes)
		}
		named := coordThreadOf(proposed(state, contracts.TaskCommandProposalDismiss{TaskID: "lead", ProposalIDs: []string{"p1", "p2"}}), "lead")
		if len(named.Proposals) != 1 || named.Proposals[0].ID != "p3" {
			t.Error("one it did not name stays")
		}
	})
}

func TestAThreadAskingToRunOneOnAnotherComputerIsPutToTheUserAtOnce(t *testing.T) {
	coordPorted(t, func() {
		studio := &domain.ProposalPlaceComputer{ID: "studio", Name: "Studio"}
		asked := &domain.ThreadProposal{ID: "p1", Title: "GPU tests", Prompt: "Do p1", Place: domain.ProposalPlace{Computer: studio, Project: new("app"), Worktree: new(false)}, From: new("worker"), ProposedAt: 1}
		state := coordWorkspace(coordHolding(coordLead("lead", coordTitled("Plan")), coordTask("worker", coordUnder("lead"))), func(state *WorkspaceState) { state.Notifications = true })
		filed := Applied(state, ReduceProposals(state, WorkspaceEventCoordinationProposed{TaskID: "lead", Proposals: []*domain.ThreadProposal{asked}}))
		if got := noticesOf(filed.Effects); strings.Join(got, ";") != `Plan | Needs you: Run "GPU tests" on Studio?` {
			t.Errorf("on the coordinator, whose card holds it: %v", got)
		}
		again := Applied(&filed.State, ReduceProposals(&filed.State, WorkspaceEventCoordinationProposed{TaskID: "lead", Proposals: []*domain.ThreadProposal{{ID: "p2", Title: "Other", Prompt: "Do p2", ProposedAt: 1}}}))
		if len(noticesOf(again.Effects)) != 0 {
			t.Error("one already waiting is not said again")
		}
	})
}

func coordStateOf(transition WorkspaceTransition) *WorkspaceState { return &transition.State }

func TestTheAgentProcessIsToldWhenAThreadsRoleChanges(t *testing.T) {
	coordPorted(t, func() {
		roles := func(transition WorkspaceTransition) []contracts.ThreadRoleCommand {
			var commands []contracts.ThreadRoleCommand
			for _, effect := range transition.Effects {
				if sent, ok := effect.(WorkspaceEffectSendRunCommand); ok {
					if command, ok := sent.Command.(contracts.ThreadRoleCommand); ok {
						commands = append(commands, command)
					}
				}
			}
			return commands
		}
		// The threads as `edit` leaves them, reconciled as every input is.
		changed := func(state *WorkspaceState, id string, edit func(*domain.Thread)) WorkspaceTransition {
			next := *state
			next.Threads = make([]*domain.Thread, len(state.Threads))
			for index, thread := range state.Threads {
				next.Threads[index] = thread
				if thread.ID == id {
					copied := *thread
					edit(&copied)
					next.Threads[index] = &copied
				}
			}
			return ReconcileCoordination(state, WorkspaceTransition{State: next, Effects: []WorkspaceEffect{}}, contracts.TaskCommandTaskRename{TaskID: id})
		}
		state := coordWorkspace(coordHolding(coordTask("lead"), coordTask("worker", coordUnder("lead"))))
		made := changed(state, "lead", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator) })
		coordinator, member := domain.CoordinationRoleCoordinator, domain.CoordinationRoleMember
		if got := roles(made); len(got) != 2 || got[0].TaskID != "lead" || *got[0].CoordinationRole != coordinator || got[1].TaskID != "worker" || *got[1].CoordinationRole != member {
			t.Errorf("made: %+v", got)
		}
		reviewer := changed(&made.State, "lead", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleReviewer) })
		if got := roles(reviewer); len(got) != 2 || got[0].CoordinationRole != nil || got[1].CoordinationRole != nil {
			t.Errorf("unmade: %+v", got)
		}
		if got := roles(changed(&made.State, "lead", func(thread *domain.Thread) { thread.Title = "Renamed" })); len(got) != 0 {
			t.Errorf("nothing that leaves the role alone says anything: %+v", got)
		}
		if got := roles(changed(state, "worker", func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleReviewer) })); len(got) != 0 {
			t.Errorf("a role that changes no tools says nothing: %+v", got)
		}
	})
}
