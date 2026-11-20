package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestFindViewCountsAThreadAndWaitsOnOtherViews(t *testing.T) {
	user := domain.ConversationMessageKindUser
	thread := coreTask("task-a", func(t *domain.Thread) {
		t.Messages = []*domain.ConversationMessage{{ID: "m1", Kind: user, Text: "a needle", At: 1}, {ID: "m2", Kind: user, Text: "needle again", At: 2}}
	})
	state := coreWorkspace(threadsOn([]*domain.Thread{thread}, "task-a"), func(s *WorkspaceState) {
		s.Find = &FindState{Target: domain.FindTargetThread{TaskID: new("task-a")}, Query: "needle", Index: 5}
	})
	view := WorkspaceFindFindView(state, thread)
	if view.Matches != 2 || view.Index != 1 || view.Hit == nil || view.Counting {
		t.Fatalf("thread find: %s", coreJSON(t, view))
	}
	review := *state
	review.Find = &FindState{Target: domain.FindTargetReview{Owner: "task-a"}, Query: "needle", Index: 4}
	if counting := WorkspaceFindFindView(&review, thread); !counting.Counting || counting.Matches != 0 || counting.Index != 0 {
		t.Fatalf("nothing reported: %s", coreJSON(t, counting))
	}
	review.FindResults = &domain.FindResults{Matches: 3}
	if counted := WorkspaceFindFindView(&review, thread); counted.Counting || counted.Matches != 3 || counted.Index != 2 {
		t.Fatalf("counted: %s", coreJSON(t, counted))
	}
	page := *state
	page.Find = &FindState{Target: domain.FindTargetBrowser{TabID: "tab"}, Query: " needle ", Index: 0}
	page.FindResults = &domain.FindResults{Matches: 9, Index: new(4.0)}
	if searched := WorkspaceFindFindView(&page, thread); searched.Index != 4 || searched.Matches != 9 {
		t.Fatalf("page: %s", coreJSON(t, searched))
	}
	coreEqualJSON(t, SearchEffects(page.Find, true, false), `[{"type":"find-in-page","tabId":"tab","query":"needle","forward":false,"findNext":true}]`, "search")
	coreEqualJSON(t, StopSearchEffects(page.Find), `[{"type":"stop-find-in-page","tabId":"tab"}]`, "stop")
	if len(SearchEffects(review.Find, false, true)) != 0 || len(StopSearchEffects(nil)) != 0 {
		t.Fatal("a review is searched where it is drawn")
	}
	closed := *state
	closed.Find = nil
	if WorkspaceFindFindView(&closed, thread) != nil {
		t.Fatal("closed bar")
	}
}

func TestFindGoesWithTheViewItSearches(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(threadsOn([]*domain.Thread{coreTask("task-a"), coreTask("task-b")}, "task-a"), func(s *WorkspaceState) {
			s.Find = &FindState{Target: domain.FindTargetThread{TaskID: new("task-a")}, Query: "needle"}
			s.FindResults = &domain.FindResults{Matches: 1}
		})
		if PrunedFind(state) != state {
			t.Fatal("the thread on screen keeps its find")
		}
		moved := *state
		moved.CurrentID = new("task-b")
		pruned := PrunedFind(&moved)
		if pruned.Find != nil || pruned.FindResults != nil {
			t.Fatal("find outlived its thread")
		}
		stale := *state
		stale.KeyboardTab = new("gone-tab")
		if kept := PrunedFind(&stale); kept.KeyboardTab != nil || kept.Find == nil {
			t.Fatal("a keyboard tab the dock no longer holds")
		}
		target := FindTargetFor(state, domain.ShortcutSurfaceAny)
		coreEqualJSON(t, target, `{"kind":"thread","taskId":"task-a"}`, "thread target")
	})
}

func TestJumpViewOffersThreadsThenSettings(t *testing.T) {
	titled := func(title string, at float64) func(*domain.Thread) {
		return func(t *domain.Thread) { t.Title, t.RankedAt = title, at }
	}
	state := coreWorkspace(func(s *WorkspaceState) {
		s.Threads = []*domain.Thread{
			coreTask("task-a", titled("Dock the browser panel", 3)),
			coreTask("task-b", titled("Panel find", 2)),
			coreTask("task-c", titled("Ship the review", 1)),
			coreTask("task-d", titled("Archived panel", 9), thArchived(1)),
		}
		s.Jump = &JumpState{Query: ""}
	})
	titles := func(view *JumpView) []string {
		var named []string
		for _, row := range view.Options {
			switch row := row.(type) {
			case ThreadJumpRow:
				named = append(named, row.Title)
			case SettingJumpRow:
				named = append(named, row.Title)
			}
		}
		return named
	}
	threadsSame(t, titles(WorkspaceJumpJumpView(state, nil)), []string{"Dock the browser panel", "Panel find", "Ship the review"}, "recent first")
	typed := *state
	typed.Jump = &JumpState{Query: "panel", Index: 7}
	view := WorkspaceJumpJumpView(&typed, nil)
	got := titles(view)
	if len(got) < 2 || got[0] != "Panel find" || got[1] != "Dock the browser panel" || view.Index != float64(len(view.Options)-1) {
		t.Fatalf("narrowed: %v index %v", got, view.Index)
	}
	closed := *state
	closed.Jump = nil
	if WorkspaceJumpJumpView(&closed, nil) != nil {
		t.Fatal("closed panel")
	}
}
