package application

import (
	"encoding/json/jsontext"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const mobNow = 1_800_000_000_000

func mobMessage(text string, at float64, kind domain.ConversationMessageKind) *domain.ConversationMessage {
	return &domain.ConversationMessage{ID: text + "-" + string(mobJSON(at)), Kind: kind, Text: text, At: at}
}

func mobJSON(value any) []byte {
	data, _ := jsonx.Marshal(value)
	return data
}

// mobTask is a thread as the mobile tests' task() makes one: ranked by when it last spoke.
func mobTask(id string, edits ...func(*domain.Thread)) *domain.Thread {
	thread := cmpTask(id, func(thread *domain.Thread) {
		thread.LastChangeSnapshot.CapturedAt = mobNow
		thread.CreatedAt, thread.UpdatedAt, thread.RankedAt = new(float64(mobNow-60_000)), mobNow, 0
	})
	for _, edit := range edits {
		edit(thread)
	}
	if thread.RankedAt == 0 {
		thread.RankedAt = domain.ThreadLastOutputAt(thread)
	}
	return thread
}

func mobInProject(id string) func(*domain.Thread) {
	return func(thread *domain.Thread) { thread.ProjectID = &id }
}

func mobWorkspace(threads []*domain.Thread, edits ...func(*WorkspaceState)) *WorkspaceState {
	return cmpWorkspace(append([]func(*WorkspaceState){func(state *WorkspaceState) {
		state.Threads = threads
		state.Projects = []*domain.Project{{ID: "project-app", Root: "/code/app", Name: new("App")}, {ID: "project-site", Root: "/code/site"}}
	}}, edits...)...)
}

func mobCurrent(id string) func(*WorkspaceState) {
	return func(state *WorkspaceState) { state.CurrentID = &id }
}

func TestTheMobileListIsGroupedByProjectAndProjectlessThreadsComeLast(t *testing.T) {
	cmpPorted(t, func() {
		state := mobWorkspace([]*domain.Thread{
			mobTask("in-app", mobInProject("project-app")),
			mobTask("in-site", mobInProject("project-site"), func(thread *domain.Thread) { thread.UpdatedAt = mobNow - 1_000 }),
			mobTask("loose", func(thread *domain.Thread) { thread.UpdatedAt = mobNow - 2_000 }),
			mobTask("gone", mobInProject("project-app"), func(thread *domain.Thread) { thread.ArchivedAt = new(float64(mobNow)) }),
		})
		view := ProjectMobileView(state, mobNow)
		var groups []string
		for _, group := range view.Groups {
			id := "null"
			if group.ProjectID != nil {
				id = *group.ProjectID
			}
			groups = append(groups, id+":"+group.Name)
		}
		if !slices.Equal(groups, []string{"project-app:App", "project-site:site", "null:Recents"}) {
			t.Errorf("groups = %v", groups)
		}
		if len(view.Groups[0].Threads) != 1 || view.Groups[0].Threads[0].ID != "in-app" {
			t.Error("archived threads are not listed")
		}
		if view.Thread != nil {
			t.Error("nothing is open")
		}
		empty := ProjectMobileView(mobWorkspace(nil), mobNow)
		if len(empty.Groups) != 3 || len(empty.Groups[0].Threads) != 0 {
			t.Error("a project with nothing in it is still a group")
		}
	})
}

func TestAThreadWithNoTitleIsNamedByTheView(t *testing.T) {
	cmpPorted(t, func() {
		state := mobWorkspace([]*domain.Thread{mobTask("blank", mobInProject("project-app"), func(thread *domain.Thread) { thread.Title = "" })}, mobCurrent("blank"))
		view := ProjectMobileView(state, mobNow)
		if view.Groups[0].Threads[0].Title != "Untitled thread" || view.Thread == nil || view.Thread.Title != "Untitled thread" {
			t.Error("untitled")
		}
	})
}

func TestWhatWentWrongTravelsAsPartOfTheView(t *testing.T) {
	cmpPorted(t, func() {
		state := mobWorkspace([]*domain.Thread{mobTask("thread-1")}, mobCurrent("thread-1"), func(state *WorkspaceState) { state.ActionError = new("That worktree is busy.") })
		failed := ProjectMobileView(state, mobNow)
		if failed.Error == nil || *failed.Error != "That worktree is busy." {
			t.Fatal("error")
		}
		clear := *state
		clear.ActionError = nil
		cmpSameJSON(t, DiffMobileView(failed, ProjectMobileView(&clear, mobNow)), &contracts.MobilePatch{Error: jsonx.Null[string]()}, "diff")
		if got := ApplyMobilePatch(failed, &contracts.MobilePatch{Error: jsonx.Null[string]()}); got.Error != nil {
			t.Error("null clears it")
		}
		if got := ApplyMobilePatch(failed, &contracts.MobilePatch{Groups: []*contracts.MobileProjectGroup{}}); got.Error == nil {
			t.Error("a patch that says nothing about it leaves it alone")
		}
	})
}

func TestAViewThatHasNotMovedIsNoPatch(t *testing.T) {
	cmpPorted(t, func() {
		state := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"))}, mobCurrent("in-app"))
		if patch := DiffMobileView(ProjectMobileView(state, mobNow), ProjectMobileView(state, mobNow)); patch != nil {
			t.Errorf("patch = %s", mobJSON(patch))
		}
	})
}

func TestAMessageArrivingCostsAnAppend(t *testing.T) {
	cmpPorted(t, func() {
		first := mobMessage("do it", mobNow-1_000, domain.ConversationMessageKindUser)
		before := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"), func(thread *domain.Thread) { thread.Messages = []*domain.ConversationMessage{first} })}, mobCurrent("in-app"))
		after := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"), func(thread *domain.Thread) {
			thread.Messages = []*domain.ConversationMessage{first, mobMessage("on it", mobNow, domain.ConversationMessageKindAssistant)}
		})}, mobCurrent("in-app"))
		patch := DiffMobileView(ProjectMobileView(before, mobNow), ProjectMobileView(after, mobNow))
		if patch == nil {
			t.Fatal("no patch")
		}
		if patch.Groups != nil {
			t.Error("a row that only moved in time, by less than the list draws, is not sent again")
		}
		changed, ok := patch.Thread.(contracts.MobileThreadPatchChanged)
		if !ok || changed.Delta.Spliced == nil || len(changed.Delta.Spliced.Messages) != 1 || changed.Delta.Spliced.Messages[0].Text != "on it" || changed.Delta.Messages != nil {
			t.Fatalf("thread = %s", mobJSON(patch.Thread))
		}
		cmpSameJSON(t, ApplyMobilePatch(ProjectMobileView(before, mobNow), patch).Thread, ProjectMobileView(after, mobNow).Thread, "applied")
	})
}

func TestATranscriptThatDidNotSimplyGrowIsReplacedWhole(t *testing.T) {
	cmpPorted(t, func() {
		with := func(text string) *WorkspaceState {
			return mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"), func(thread *domain.Thread) {
				thread.Messages = []*domain.ConversationMessage{mobMessage(text, mobNow-1_000, domain.ConversationMessageKindUser)}
			})}, mobCurrent("in-app"))
		}
		patch := DiffMobileView(ProjectMobileView(with("do it"), mobNow), ProjectMobileView(with("do that instead"), mobNow))
		changed, ok := patch.Thread.(contracts.MobileThreadPatchChanged)
		if !ok || changed.Delta.Spliced != nil || len(changed.Delta.Messages) != 1 || changed.Delta.Messages[0].Text != "do that instead" {
			t.Fatalf("thread = %s", mobJSON(patch.Thread))
		}
	})
}

func TestOpeningChangingAndClosingAreThreeDifferentPatches(t *testing.T) {
	cmpPorted(t, func() {
		none := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"))})
		open := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"))}, mobCurrent("in-app"))
		other := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app")), mobTask("in-site", mobInProject("project-site"))}, mobCurrent("in-site"))
		if _, ok := DiffMobileView(ProjectMobileView(none, mobNow), ProjectMobileView(open, mobNow)).Thread.(contracts.MobileThreadPatchOpened); !ok {
			t.Error("opened")
		}
		closed := DiffMobileView(ProjectMobileView(open, mobNow), ProjectMobileView(none, mobNow))
		if _, ok := closed.Thread.(contracts.MobileThreadPatchClosed); !ok || ApplyMobilePatch(ProjectMobileView(open, mobNow), closed).Thread != nil {
			t.Error("closed")
		}
		moved := DiffMobileView(ProjectMobileView(open, mobNow), ProjectMobileView(other, mobNow))
		if _, ok := moved.Thread.(contracts.MobileThreadPatchOpened); !ok || moved.Groups == nil {
			t.Error("another thread is one the phone has never seen, and the list grew")
		}
	})
}

func TestOnlyWhatMovedTravelsAndAPatchPutsItBack(t *testing.T) {
	cmpPorted(t, func() {
		before := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"))}, mobCurrent("in-app"))
		after := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"), func(thread *domain.Thread) { thread.Model = new("sonnet") })}, mobCurrent("in-app"))
		patch := DiffMobileView(ProjectMobileView(before, mobNow), ProjectMobileView(after, mobNow))
		changed, ok := patch.Thread.(contracts.MobileThreadPatchChanged)
		if !ok {
			t.Fatalf("patch = %s", mobJSON(patch))
		}
		cmpSameJSON(t, changed.Delta, contracts.MobileThreadDelta{Settings: &contracts.MobileThreadSettings{FastMode: new(false), Engine: "claude", Model: new("sonnet"), Effort: "high", Policy: "confirm"}}, "delta")
		cmpSameJSON(t, ApplyMobilePatch(ProjectMobileView(before, mobNow), patch), ProjectMobileView(after, mobNow), "applied")
	})
}

func TestAPatchForAThreadThePhoneNoLongerHoldsLeavesItAlone(t *testing.T) {
	cmpPorted(t, func() {
		open := mobWorkspace([]*domain.Thread{mobTask("in-app", mobInProject("project-app"))}, mobCurrent("in-app"))
		patch := &contracts.MobilePatch{Thread: contracts.MobileThreadPatchChanged{ID: "somebody-else", Delta: contracts.MobileThreadDelta{Title: new("stray")}}}
		cmpSameJSON(t, ApplyMobilePatch(ProjectMobileView(open, mobNow), patch), ProjectMobileView(open, mobNow), "open")
		cmpSameJSON(t, ApplyMobilePatch(EmptyMobileView(), patch), EmptyMobileView(), "empty")
	})
}

func TestAMessageSaysHowManyImagesItCarriedAndAToolCallWhatItDid(t *testing.T) {
	cmpPorted(t, func() {
		sent := &domain.ConversationMessage{ID: "m1", Kind: domain.ConversationMessageKindUser, Text: "Look", Attachments: []string{"/a.jpg", "/b.jpg"}, At: mobNow}
		call := &domain.ConversationMessage{ID: "c1", Kind: domain.ConversationMessageKindTool, Text: "Bash", Detail: new(`{"command":"npm test"}`), At: mobNow}
		state := mobWorkspace([]*domain.Thread{mobTask("t1", func(thread *domain.Thread) {
			thread.Messages = []*domain.ConversationMessage{sent, mobMessage("plain", mobNow+1, domain.ConversationMessageKindUser), call}
		})}, mobCurrent("t1"))
		messages := ProjectMobileView(state, mobNow).Thread.Messages
		if messages[0].Images == nil || *messages[0].Images != 2 || messages[1].Images != nil {
			t.Error("images")
		}
		cmpSameJSON(t, messages[2].Call, &contracts.MobileToolCall{Family: "shell", Label: "Bash", Argument: "npm test", Sigil: new("$")}, "call")
		if again := ProjectMobileView(state, mobNow+1).Thread.Messages[2].Call; again != messages[2].Call {
			t.Error("the same stored call is described once")
		}
	})
}

func TestTranscriptDeltaSplicesAtItsEnd(t *testing.T) {
	m := func(text string) *contracts.MobileMessage { return &contracts.MobileMessage{Kind: "user", Text: text} }
	if spliced, messages := transcriptDelta(nil, nil); spliced != nil || messages != nil {
		t.Error("nothing moved")
	}
	spliced, messages := transcriptDelta([]*contracts.MobileMessage{m("a"), m("b")}, []*contracts.MobileMessage{m("b"), m("c")})
	if messages != nil || spliced == nil || spliced.Dropped != 1 || spliced.Kept != 1 || len(spliced.Messages) != 1 || spliced.Messages[0].Text != "c" {
		t.Errorf("slid = %s %v", mobJSON(spliced), messages)
	}
	spliced, _ = transcriptDelta([]*contracts.MobileMessage{m("a"), m("b")}, []*contracts.MobileMessage{m("a"), m("b2")})
	if spliced == nil || spliced.Dropped != 0 || spliced.Kept != 1 || spliced.Messages[0].Text != "b2" {
		t.Errorf("the newest grew = %s", mobJSON(spliced))
	}
	if spliced, messages := transcriptDelta([]*contracts.MobileMessage{m("a"), m("b"), m("c")}, []*contracts.MobileMessage{m("a"), m("x"), m("c")}); spliced != nil || len(messages) != 3 {
		t.Error("a change in the middle sends the whole transcript")
	}
	if spliced, messages := transcriptDelta([]*contracts.MobileMessage{m("a")}, []*contracts.MobileMessage{m("a")}); spliced != nil || messages != nil {
		t.Error("the same transcript")
	}
}

func TestAStreamingTailTravelsAsWhatItGrewBy(t *testing.T) {
	view := func(tail *string) *contracts.MobileView {
		empty := EmptyMobileView()
		empty.Thread = &contracts.MobileThreadView{ID: "t", Messages: []*contracts.MobileMessage{}, StreamingTail: tail}
		return empty
	}
	patch := DiffMobileView(view(new("héllo")), view(new("héllo wörld")))
	changed := patch.Thread.(contracts.MobileThreadPatchChanged)
	if changed.Delta.Tail == nil || changed.Delta.Tail.From != 5 || changed.Delta.Tail.Text != " wörld" {
		t.Fatalf("tail = %s", mobJSON(changed.Delta))
	}
	applied := ApplyMobilePatch(view(new("héllo")), patch)
	if applied == nil || *applied.Thread.StreamingTail != "héllo wörld" {
		t.Error("a tail that grew is put back")
	}
	if ApplyMobilePatch(view(new("oth")), patch) != nil {
		t.Error("a tail of another length does not fit")
	}
	reset := DiffMobileView(view(new("abc")), view(nil)).Thread.(contracts.MobileThreadPatchChanged)
	if !reset.Delta.StreamingTail.IsNull() {
		t.Error("a tail that went travels as null")
	}
	if got := ApplyMobilePatch(view(new("abc")), &contracts.MobilePatch{Thread: reset}); got.Thread.StreamingTail != nil {
		t.Error("and is put back as none")
	}
}

func TestRowsThatOnlyDriftInTimeAreTheSame(t *testing.T) {
	row := func(at float64) []*contracts.MobileThreadEntry {
		return []*contracts.MobileThreadEntry{{ID: "a", Title: "a", Status: "idle", LastActivityAt: at}}
	}
	if !sameRows(row(0), row(activityDriftMs-1)) {
		t.Error("within the drift")
	}
	if sameRows(row(0), row(activityDriftMs)) {
		t.Error("past the drift")
	}
}

func TestAnApprovalsInputReadsAsJSONStringifyWritesIt(t *testing.T) {
	input := map[string]jsontext.Value{"command": jsontext.Value(`"rm -rf build"`), "options": jsontext.Value(`{"a":[1,2],"b":{}}`)}
	want := "{\n  \"command\": \"rm -rf build\",\n  \"options\": {\n    \"a\": [\n      1,\n      2\n    ],\n    \"b\": {}\n  }\n}"
	if got := approvalDetail(input); got != want {
		t.Errorf("got %q", got)
	}
}
