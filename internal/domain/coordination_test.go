package domain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func coordThread(id string, change func(thread *Thread)) *Thread {
	thread := &Thread{}
	thread.ID, thread.Title, thread.Engine, thread.ExecutionPolicy = id, id, AgentEngineClaude, ExecutionPolicyConfirm
	thread.Messages, thread.ContinuationStatus, thread.RankedAt, thread.UpdatedAt = []*ConversationMessage{}, ContinuationStatusNone, 1, 1
	thread.LastChangeSnapshot = ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	if change != nil {
		change(thread)
	}
	return thread
}

func decoded(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := jsonx.Unmarshal([]byte(text), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDeliveryLabelAndThreadModelOfANewerBuild(t *testing.T) {
	if DeliveryLabel("pull-request") != "Pull request" || DeliveryLabel("release") != "release" {
		t.Fatal("delivery labels")
	}
	lead := coordThread("lead", func(thread *Thread) { thread.Role = new(ThreadRoleCoordinator) })
	kept := *lead
	kept.Model = new("opus")
	kept.ThreadModel = &ThreadModelChoice{Model: "opus-next", Effort: new(AgentEffort("galaxy"))}
	jsonEqual(t, ThreadModelOf(BuiltInModels, &kept), `{"model":"opus-next","effort":"galaxy"}`, "kept as that build chose it")
	unknown := *lead
	unknown.Model = new("opus-next")
	if choice := ThreadModelOf(BuiltInModels, &unknown); choice == nil || choice.Model != "opus-next" {
		t.Fatal("own unknown model")
	}
	codex := coordThread("lead", func(thread *Thread) {
		thread.Engine, thread.Effort = AgentEngineCodex, new(AgentEffortUltra)
	})
	jsonEqual(t, ThreadModelOf(BuiltInModels, codex), `{"engine":"codex","model":"gpt-6.2-sol","effort":"ultra"}`, "runs on what it runs on")
	haiku := coordThread("lead", func(thread *Thread) {
		thread.ThreadModel = &ThreadModelChoice{Model: "haiku", Effort: new(AgentEffortMax)}
	})
	jsonEqual(t, ThreadModelOf(BuiltInModels, haiku), `{"engine":"claude","model":"haiku"}`, "a model without efforts takes none")
	retired := coordThread("lead", func(thread *Thread) {
		thread.ThreadModel = &ThreadModelChoice{Model: "sonnet", Effort: new(AgentEffortUltra)}
	})
	jsonEqual(t, ThreadModelOf(BuiltInModels, retired), `{"engine":"claude","model":"sonnet","effort":"max"}`, "an effort moves to the nearest")
	became := AsCoordinator(BuiltInModels, codex)
	if *became.Role != ThreadRoleCoordinator || became.ThreadModel == nil || codex.Role != nil {
		t.Fatal("asCoordinator")
	}
}

func TestCoordinationMembership(t *testing.T) {
	lead := coordThread("lead", func(thread *Thread) { thread.Role = new(ThreadRoleCoordinator) })
	member := coordThread("member", func(thread *Thread) { thread.ParentID = new("lead") })
	archived := coordThread("archived", func(thread *Thread) { thread.ParentID, thread.ArchivedAt = new("lead"), new(1.0) })
	remote := coordThread("remote", func(thread *Thread) {
		thread.RemoteLead = &HeldLead{RemoteLead: RemoteLead{RemoteSender: RemoteSender{ThreadID: "lead", Title: "Lead", Computer: "Laptop"}}}
	})
	away := coordThread("away", func(thread *Thread) {
		thread.RemoteLead = &HeldLead{RemoteLead: RemoteLead{RemoteSender: RemoteSender{ThreadID: "lead", Title: "Lead", Computer: "Laptop"}}, Away: new(true)}
	})
	loner := coordThread("loner", nil)
	threads := []*Thread{lead, member, archived, remote, away, loner}
	if CoordinatorOf(threads, member) != lead || CoordinatorOf(threads, loner) != nil || CoordinatorOf(threads, lead) != nil {
		t.Fatal("coordinatorOf")
	}
	if RemoteLeadOf(remote) == nil || RemoteLeadOf(away) != nil || AwayLeadOf(away) == nil || AwayLeadOf(remote) != nil {
		t.Fatal("remote leads")
	}
	if ids := handleIDsOf(CoordinatedThreads(threads, "lead")); !reflect.DeepEqual(ids, []string{"member", "remote"}) {
		t.Fatal(ids)
	}
	if got := CoordinatedThreadIds(threads).Sorted(); !reflect.DeepEqual(got, []string{"archived", "member", "remote"}) {
		t.Fatal(got)
	}
	if ids := handleIDsOf(ThreadsByCoordinator(threads)["lead"]); !reflect.DeepEqual(ids, []string{"member", "archived", "remote"}) {
		t.Fatal(ids)
	}
	alone := jsonx.SetOf("loner")
	if !jsonx.SameMap(WithCoordinated(threads, alone), alone) {
		t.Fatal("no coordinator named keeps the set")
	}
	if got := WithCoordinated(threads, jsonx.SetOf("lead")).Sorted(); !reflect.DeepEqual(got, []string{"archived", "lead", "member", "remote"}) {
		t.Fatal(got)
	}
	if !CanJoinCoordinator(loner, lead) || CanJoinCoordinator(lead, lead) || !WorksUnderCoordinator(threads, remote) || WorksUnderCoordinator(threads, away) {
		t.Fatal("joins")
	}
}

func handleIDsOf(threads []*Thread) []string {
	ids := []string{}
	for _, thread := range threads {
		ids = append(ids, thread.ID)
	}
	return ids
}

func TestDecisionsAndWaitingOnUser(t *testing.T) {
	lead := coordThread("lead", func(thread *Thread) { thread.Role = new(ThreadRoleCoordinator) })
	for i := range 25 {
		lead = WithDecision(lead, &Decision{ID: fmt.Sprint("d", i), Question: "Q", Options: []*DecisionOption{}, RaisedAt: float64(30 - i)})
		if i < 15 {
			lead = WithAnswer(lead, fmt.Sprint("d", i), "Yes", 1)
		}
	}
	if len(lead.Decisions) != 20 || lead.Decisions[0].ID != "d5" || len(OpenDecisions(lead)) != 10 || !DecisionsFull(lead) {
		t.Fatalf("answered decisions are history: %d %s", len(lead.Decisions), lead.Decisions[0].ID)
	}
	reopened := WithoutAnswer(lead, "d5")
	if reopened.Decisions[0].Answer != nil || lead.Decisions[0].Answer == nil || WithoutAnswer(lead, "d24") != lead {
		t.Fatal("withoutAnswer")
	}
	blocked := coordThread("blocked", func(thread *Thread) {
		thread.Outcome = new(ThreadOutcomeFinished)
		thread.Report = &CoordinationReport{State: CoordinationStateBlocked, Summary: "Needs a key", At: 2}
	})
	failed := coordThread("failed", func(thread *Thread) {
		thread.Outcome, thread.RunEndedAs = new(ThreadOutcomeFailed), new(ThreadOutcomeFailed)
	})
	resolved := coordThread("resolved", func(thread *Thread) {
		thread.Outcome, thread.RunEndedAs, thread.ResolvedAt = new(ThreadOutcomeFailed), new(ThreadOutcomeFailed), new(3.0)
	})
	lead.Proposals = []*ThreadProposal{{ID: "p1"}, {ID: "p2", Place: ProposalPlace{Computer: &ProposalPlaceComputer{ID: "s", Name: "Studio"}}}, {ID: "p3"}}
	waiting := CoordinationWaitingOnUser(lead, []*Thread{blocked, failed, resolved}, []string{"p3"})
	jsonEqual(t, waiting, `{"decisions":10,"suggestions":1,"requests":["Studio"],"blocked":1,"failed":1}`, "waiting")
	if !IsWaitingOnUser(waiting) || IsWaitingOnUser(WaitingOnUser{Requests: []string{}}) {
		t.Fatal("isWaitingOnUser")
	}
	verdict := CoordinationStuckVerdict(blocked)
	if verdict.State != StuckVerdictStateBlocked || *verdict.Summary != "Needs a key" || CoordinationStuckVerdict(failed).Summary != nil {
		t.Fatal("verdicts")
	}
	if ReopenedIfStuck(resolved).ResolvedAt != nil || resolved.ResolvedAt == nil || ReopenedIfStuck(failed) != failed {
		t.Fatal("reopened")
	}
	decisions := CoordinationDecisions(lead, nil)
	if len(decisions) != 10 || decisions[0].Decision.ID != "d24" {
		t.Fatal("oldest first")
	}
}

func TestMemoryChanges(t *testing.T) {
	lead := coordThread("lead", nil)
	lead, id, err := WithMemoryChange(lead, MemoryChangeRemember{Text: "  Propose first "}, 5, true)
	if err != nil || id != "n1" {
		t.Fatal(id, err)
	}
	jsonEqual(t, lead.Memory, `{"notes":[{"id":"n1","text":"Propose first","at":5}],"revision":1,"heard":1}`, "remembered")
	lead, _, _ = WithMemoryChange(lead, MemoryChangePurpose{Purpose: new("Ship it")}, 6, false)
	jsonEqual(t, lead.Memory, `{"purpose":"Ship it","notes":[{"id":"n1","text":"Propose first","at":5}],"revision":2,"heard":1}`, "purpose")
	same, _, _ := WithMemoryChange(lead, MemoryChangePurpose{Purpose: new(" Ship it ")}, 7, false)
	if same != lead {
		t.Fatal("an unchanged purpose hands the thread back")
	}
	lead, _, _ = WithMemoryChange(lead, MemoryChangePurpose{}, 7, true)
	jsonEqual(t, lead.Memory, `{"purposeCleared":3,"notes":[{"id":"n1","text":"Propose first","at":5}],"revision":3,"heard":1}`, "cleared, still unread")
	lead = WithMemoryHeard(lead)
	lead, id, _ = WithMemoryChange(lead, MemoryChangeEdit{NoteID: "n1", Text: "Propose threads first"}, 8, true)
	jsonEqual(t, lead.Memory, `{"purposeCleared":3,"notes":[{"id":"n1","text":"Propose threads first","at":8}],"revision":4,"heard":4}`, "edited")
	if _, _, err := WithMemoryChange(lead, MemoryChangeForget{NoteID: "n9"}, 9, false); err == nil || err.Error() != "No note has the ID n9." {
		t.Fatal(err)
	}
	if _, _, err := WithMemoryChange(lead, MemoryChangeRemember{Text: "  "}, 9, false); err == nil || err.Error() != "A note needs some text." {
		t.Fatal(err)
	}
	if _, _, err := WithMemoryChange(lead, MemoryChangePurpose{Purpose: new(strings.Repeat("x", 201))}, 9, false); err == nil || err.Error() != "A purpose can be at most 200 characters." {
		t.Fatal(err)
	}
	lead, id, _ = WithMemoryChange(lead, MemoryChangeForget{NoteID: "n1"}, 9, false)
	jsonEqual(t, lead.Memory, `{"purposeCleared":3,"notes":[],"revision":5,"heard":4}`, "forgotten")
	if id != "n1" || WithMemoryUnheard(lead).Memory.Heard != nil || lead.Memory.Heard == nil {
		t.Fatal("unheard")
	}
	full := coordThread("full", nil)
	for range MaxMemoryNotes {
		full, _, _ = WithMemoryChange(full, MemoryChangeRemember{Text: "x"}, 1, false)
	}
	if _, _, err := WithMemoryChange(full, MemoryChangeRemember{Text: "x"}, 1, false); err == nil || err.Error() != "Memory holds at most 30 notes. Forget one first." {
		t.Fatal(err)
	}
}

func TestProposalsAndNotes(t *testing.T) {
	lead := coordThread("lead", nil)
	proposals := []*ThreadProposal{}
	for i := range 14 {
		proposals = append(proposals, &ThreadProposal{ID: fmt.Sprint("p", i)})
	}
	lead = WithProposals(lead, proposals)
	if len(lead.Proposals) != MaxProposals || WithProposals(lead, proposals) != lead {
		t.Fatal("room for twelve")
	}
	errored := WithProposalError(lead, "p1", new("Lost"))
	if *errored.Proposals[1].Error != "Lost" || lead.Proposals[1].Error != nil || WithProposalError(errored, "p1", new("Lost")) != errored || WithProposalError(errored, "p1", nil).Proposals[1].Error != nil {
		t.Fatal("errors")
	}
	carried := WithProposalCarried(lead, "p2")
	if !*carried.Proposals[2].Carried || WithProposalCarried(carried, "p2") != carried {
		t.Fatal("carried")
	}
	fewer := WithoutProposals(lead, jsonx.SetOf("p0", "p1"))
	if len(fewer.Proposals) != 10 || WithoutProposals(lead, jsonx.SetOf("none")) != lead {
		t.Fatal("withoutProposals")
	}
	all := jsonx.Set[string]{}
	for _, proposal := range lead.Proposals {
		all[proposal.ID] = struct{}{}
	}
	if WithoutProposals(lead, all).Proposals != nil {
		t.Fatal("none left")
	}

	notes := coordThread("lead", nil)
	for i := range 45 {
		notes = WithCoordinationNote(notes, &CoordinationNote{ID: fmt.Sprint("busy", i), ThreadID: "busy", Urgent: new(i == 0)})
	}
	for i := range 6 {
		notes = WithCoordinationNote(notes, &CoordinationNote{ID: fmt.Sprint("quiet", i), ThreadID: "quiet"})
	}
	if len(notes.CoordinationNotes) != 50 || notes.CoordinationNotes[0].ID != "busy0" || notes.CoordinationNotes[1].ID != "busy2" {
		t.Fatalf("the busiest thread gives up its oldest note that can wait: %s %s", notes.CoordinationNotes[0].ID, notes.CoordinationNotes[1].ID)
	}
	heard := WithoutCoordinationNotes(notes, jsonx.SetOf("busy0"))
	if len(heard.CoordinationNotes) != 49 || WithoutCoordinationNotes(notes, CoordinatedThreadIds(nil)).CoordinationNotes == nil {
		t.Fatal("withoutCoordinationNotes")
	}
}

func TestRemoteHeardKeepsWhatIsStillLed(t *testing.T) {
	lead := coordThread("lead", nil)
	led := jsonx.SetOf("t0")
	for i := range 105 {
		lead = WithRemoteHeard(lead, fmt.Sprint("t", i), float64(i), led)
	}
	if len(lead.RemoteHeard) != 100 {
		t.Fatal(len(lead.RemoteHeard))
	}
	for _, id := range []string{"t0", "t6", "t104"} {
		if _, ok := lead.RemoteHeard[id]; !ok {
			t.Fatalf("%s should be kept", id)
		}
	}
	for _, id := range []string{"t1", "t5"} {
		if _, ok := lead.RemoteHeard[id]; ok {
			t.Fatalf("%s should be dropped", id)
		}
	}
	if WithRemoteHeard(lead, "t104", 104, led) != lead {
		t.Fatal("nothing new")
	}
}

func TestCoordinationValidators(t *testing.T) {
	valid := map[string]func(any) bool{
		`{"intent":"x","doneWhen":"y","delivers":"report"}`:                                                         IsThreadBrief,
		`{"model":"opus","engine":"claude","effort":"max"}`:                                                         IsThreadModelChoice,
		`{"notes":[{"id":"n1","text":"","at":1}],"revision":3,"purposeCleared":2}`:                                  IsCoordinatorMemory,
		`{"kind":"purpose","purpose":null}`:                                                                         IsMemoryChange,
		`{"kind":"edit","noteId":"n1","text":"t"}`:                                                                  IsMemoryChange,
		`{"title":"T","prompt":"P","worktreeId":"w","computer":"Studio"}`:                                           IsProposalRequest,
		`{"id":"p","title":"","prompt":"","place":{"computer":{"id":"s","name":""}},"proposedAt":1,"carried":true}`: IsThreadProposal,
		`{"threadId":"t","title":"","computer":"c","away":true,"backAt":3}`:                                         IsHeldLead,
		`{"a":1}`: IsRemoteHeard,
		`{"a":{"computer":"c","answer":{"at":1}}}`:                                         IsRemoteStops,
		`{"a":{"computer":"c","threadId":"t"}}`:                                            IsCaughtStarts,
		`{"a":{"computer":"c","title":"t"}}`:                                               IsRemoteWorking,
		`{"state":"done","summary":"ok","at":1}`:                                           IsCoordinationReport,
		`{"question":"Q","options":[{"label":"A"},{"label":"B","recommended":true}]}`:      IsDecisionRequest,
		`{"id":"d","question":"Q","options":[{"label":"A"},{"label":" a "}],"raisedAt":1}`: IsDecision,
		`{"id":"n","threadId":"t","text":"","at":1,"urgent":true}`:                         IsCoordinationNote,
	}
	for text, check := range valid {
		if !check(decoded(t, text)) {
			t.Fatalf("should pass: %s", text)
		}
	}
	invalid := map[string]func(any) bool{
		`{"intent":" ","doneWhen":"y","delivers":"report"}`:          IsThreadBrief,
		`{"model":"opus","engine":null}`:                             IsThreadModelChoice,
		`{"notes":[],"revision":-1}`:                                 IsCoordinatorMemory,
		`{"kind":"purpose"}`:                                         IsMemoryChange,
		`{"title":"T","prompt":" "}`:                                 IsProposalRequest,
		`{"title":"T","prompt":"P","project":null}`:                  IsProposalRequest,
		`{"threadId":"t","title":"","computer":"c","away":false}`:    IsHeldLead,
		`{"a":"late"}`:                                               IsRemoteHeard,
		`{"question":"Q","options":[{"label":"A"},{"label":" a "}]}`: IsDecisionRequest,
		`{"id":"n","threadId":"t","text":"","at":1,"runId":""}`:      IsCoordinationNote,
		`[]`: IsRemoteWorking,
	}
	for text, check := range invalid {
		if check(decoded(t, text)) {
			t.Fatalf("should fail: %s", text)
		}
	}
	if !DistinctLabels([]*DecisionOption{{Label: "A"}, {Label: "B"}}) || DistinctLabels([]*DecisionOption{{Label: "A"}, {Label: "a "}}) {
		t.Fatal("distinctLabels")
	}
}
