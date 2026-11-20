package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func threadsEdit(t *testing.T, state *WorkspaceState, input WorkspaceInput, revision *float64) *OptimisticEdit {
	t.Helper()
	pending := OptimisticEditsOptimisticEdit(state, input)
	if pending == nil {
		t.Fatalf("no edit for %T", input)
	}
	pending.Revision = revision
	return pending
}

func TestOptimisticEditNamesItsField(t *testing.T) {
	state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("thread")}, "thread"))
	typed := threadsEdit(t, state, contracts.ViewCommandViewSetPrompt{Prompt: "hello"}, nil)
	coreEqualJSON(t, typed.Input, `{"type":"view.set-prompt","taskId":"thread","prompt":"hello"}`, "input")
	other := coreWorkspace(func(s *WorkspaceState) { s.CurrentID = new("other") })
	if typed.Key != threadsEdit(t, other, contracts.ViewCommandViewSetPrompt{TaskID: new("thread"), Prompt: "later"}, nil).Key {
		t.Fatal("same field, different key")
	}
	if typed.Key != `["view.set-prompt","thread"]` {
		t.Fatalf("key %s", typed.Key)
	}
	if typed.Key == threadsEdit(t, state, contracts.TaskCommandTaskRename{TaskID: "thread", Title: "Renamed"}, nil).Key {
		t.Fatal("different fields share a key")
	}
	if OptimisticEditsOptimisticEdit(state, contracts.TaskCommandTaskNew{}) != nil || OptimisticEditsOptimisticEdit(state, contracts.ViewCommandViewFindQuery{Query: "no find bar"}) != nil {
		t.Fatal("held an edit with no local text")
	}
}

func TestPendingEditsReplayOldestFirst(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("thread")}, "thread"))
		view := ApplyOptimisticEdits(state, []*OptimisticEdit{
			threadsEdit(t, state, contracts.ViewCommandViewSetPrompt{Prompt: "draft"}, nil),
			threadsEdit(t, state, contracts.TaskCommandTaskRename{TaskID: "thread", Title: "Renamed"}, nil),
		}, 0)
		if view.State.Prompts["thread"] != "draft" || view.State.Threads[0].Title != "Renamed" || len(view.Edits) != 2 {
			t.Fatalf("replayed: %s", coreJSON(t, view.Edits))
		}
		if untouched := ApplyOptimisticEdits(state, nil, 0); !jsonx.SameSlice(untouched.State.Threads, state.Threads) || !jsonx.SameMap(untouched.State.Prompts, state.Prompts) {
			t.Fatal("nothing pending redrew the state")
		}
	})
}

func TestEditHeldUntilItsRevisionArrives(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(s *WorkspaceState) { s.Prompts = map[string]string{"draft:": "Saved"} })
		older := threadsEdit(t, state, contracts.ViewCommandViewSetPrompt{Prompt: "Older"}, new(7.0))
		newer := threadsEdit(t, state, contracts.ViewCommandViewSetPrompt{TaskID: new("draft:other"), Prompt: "Newer"}, new(5.0))
		unacknowledged := threadsEdit(t, state, contracts.TaskCommandTaskRename{TaskID: "thread", Title: "Renamed"}, nil)
		pending := ApplyOptimisticEdits(state, []*OptimisticEdit{older, newer, unacknowledged}, 4)
		if pending.State.Prompts["draft:"] != "Older" || len(pending.Edits) != 3 {
			t.Fatal("pending")
		}
		partly := ApplyOptimisticEdits(state, []*OptimisticEdit{older, newer, unacknowledged}, 6)
		if len(partly.Edits) != 2 || partly.Edits[0] != older || partly.Edits[1] != unacknowledged || partly.State.Prompts["draft:"] != "Older" {
			t.Fatal("partly")
		}
		caught := *state
		caught.Prompts = map[string]string{"draft:": "Older"}
		settled := ApplyOptimisticEdits(&caught, partly.Edits, 7)
		if len(settled.Edits) != 1 || settled.Edits[0] != unacknowledged || settled.State.Prompts["draft:"] != "Older" {
			t.Fatal("settled")
		}
	})
}

func TestEditAlreadyDrawnIsNotReplayed(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("thread", func(t *domain.Thread) { t.Title = "Renamed"; t.TitleByUser = new(true) })}, "thread"), func(s *WorkspaceState) {
			s.Prompts = map[string]string{"thread": "typed"}
			s.Jump = &JumpState{Query: "jump"}
			s.Annotations = map[string][]*domain.Annotation{"thread": {{ID: "annotation", Quote: "quoted", Note: "noted"}}}
			s.WorktreeMenuSearch = WorktreeMenuSearch{"threads": "worktree", "destinations": ""}
			s.Find = &FindState{Target: domain.FindTargetThread{TaskID: new("thread")}, Query: "needle"}
		})
		for _, input := range []WorkspaceInput{
			contracts.ViewCommandViewSetPrompt{Prompt: "typed"},
			contracts.TaskCommandTaskRename{TaskID: "thread", Title: "Renamed"},
			contracts.AnnotationCommandAnnotationNote{AnnotationID: "annotation", Note: "noted"},
			contracts.WorktreeCommandWorktreeMenuSearch{List: "threads", Query: "worktree"},
			contracts.ViewCommandViewFindQuery{Query: "needle"},
			contracts.ViewCommandViewJumpQuery{Query: "jump"},
		} {
			pending := threadsEdit(t, state, input, nil)
			if !alreadyDrawn(state, pending.Input) {
				t.Errorf("%T redrew a state that already reads the same", input)
			}
		}
		if alreadyDrawn(state, threadsEdit(t, state, contracts.ViewCommandViewSetPrompt{Prompt: "typing on"}, nil).Input) {
			t.Fatal("a new prompt counted as drawn")
		}
	})
}

func TestSearchEditHeldButNotDrawnElsewhere(t *testing.T) {
	corePorted(t, func() {
		searching := coreWorkspace(threadsOn([]*domain.Thread{coreTask("thread")}, "thread"), func(s *WorkspaceState) {
			s.Find = &FindState{Target: domain.FindTargetThread{TaskID: new("thread")}}
		})
		typed := threadsEdit(t, searching, contracts.ViewCommandViewFindQuery{Query: "needle"}, nil)
		if got := ApplyOptimisticEdits(searching, []*OptimisticEdit{typed}, 0); got.State.Find.Query != "needle" {
			t.Fatalf("drawn: %q", got.State.Find.Query)
		}
		moved := *searching
		moved.Find = &FindState{Target: domain.FindTargetTerminal{TerminalID: "shell"}, Query: "elsewhere"}
		invalidated := ApplyOptimisticEdits(&moved, []*OptimisticEdit{typed}, 0)
		if invalidated.State.Find.Query != "elsewhere" || len(invalidated.Edits) != 1 {
			t.Fatal("drawn over another target")
		}
		closed := *searching
		closed.Find = nil
		if got := ApplyOptimisticEdits(&closed, []*OptimisticEdit{typed}, 0); got.State.Find != nil || len(got.Edits) != 1 {
			t.Fatal("closed bar")
		}
	})
}
