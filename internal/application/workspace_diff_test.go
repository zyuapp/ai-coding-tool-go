package application

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func awFile(path string, additions, deletions float64) *domain.DiffFileSummary {
	return &domain.DiffFileSummary{Path: path, Status: "modified", Additions: additions, Deletions: deletions}
}

func awSummary(ignoreWhitespace bool, files ...*domain.DiffFileSummary) contracts.DiffSummaryResultAvailable {
	result := contracts.DiffSummaryResultAvailable{Range: domain.DefaultBranchRange, IgnoreWhitespace: ignoreWhitespace, Files: files}
	for _, file := range files {
		result.Additions += file.Additions
		result.Deletions += file.Deletions
	}
	return result
}

// The draft dock's review, already showing `files` from workspace-a.
func awReviewing(files ...*domain.DiffFileSummary) *WorkspaceState {
	return awWorkspace(func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}}
		state.DraftProjectID = new("project-a")
		review := *EmptyDiff
		review.WorkspaceID, review.Result = new("workspace-a"), awSummary(true, files...)
		state.Diffs = map[string]*DiffState{"draft": &review}
	})
}

func awLoaded(state *WorkspaceState, result contracts.DiffSummaryResult) *WorkspaceState {
	loaded := awApply(state, ReduceDiffs, DiffEventDiffLoaded{Owner: "draft", WorkspaceID: "workspace-a", Range: domain.DefaultBranchRange, Result: result})
	return &loaded.State
}

func TestRetainedViewsUntickAFileThatChangedUnlessOnlyRecounted(t *testing.T) {
	before := awSummary(true, awFile("a.ts", 1, 1), awFile("b.ts", 1, 0))
	viewed := map[string]string{"a.ts": "modified:1:1", "b.ts": "modified:1:0", "gone.ts": "modified:1:0"}
	rewritten := awSummary(true, awFile("a.ts", 4, 1), awFile("b.ts", 1, 0))
	if got := RetainedViews(viewed, rewritten, before); !reflect.DeepEqual(got, map[string]string{"b.ts": "modified:1:0"}) {
		t.Fatalf("rewrite: %v", got)
	}
	recounted := awSummary(false, awFile("a.ts", 4, 4), awFile("b.ts", 1, 0))
	if !Recounted(before, recounted) || Recounted(nil, recounted) || Recounted(before, rewritten) {
		t.Fatalf("recounted")
	}
	if got := RetainedViews(viewed, recounted, before); !reflect.DeepEqual(got, map[string]string{"a.ts": "modified:4:4", "b.ts": "modified:1:0"}) {
		t.Fatalf("a recount restamps ticks: %v", got)
	}
	failed := contracts.DiffSummaryResultUnavailable{Reason: "missing"}
	if got := RetainedViews(viewed, failed, before); !reflect.DeepEqual(got, viewed) {
		t.Fatalf("an unavailable list keeps the ticks")
	}
}

func TestFoldedOnLoad(t *testing.T) {
	review := *EmptyDiff
	review.Result = awSummary(true, awFile("a.ts", 1, 0), awFile("b.ts", 1, 0), awFile("big.ts", domain.DrawnFileLimit+1, 0))
	review.Collapsed = []string{"a.ts"}
	files := []*domain.DiffFileSummary{awFile("a.ts", 1, 0), awFile("b.ts", 1, 0), awFile("big.ts", domain.DrawnFileLimit+1, 0)}
	if got := FoldedOnLoad(&review, files, awSummary(true, files...)); !reflect.DeepEqual(got, []string{"a.ts"}) {
		t.Fatalf("a decided file keeps the user's decision, even one opened by hand: %v", got)
	}
	changed := []*domain.DiffFileSummary{awFile("a.ts", 9, 0), awFile("b.ts", 500_000, 0)}
	if got := FoldedOnLoad(&review, changed, awSummary(true, changed...)); !reflect.DeepEqual(got, []string{"b.ts"}) {
		t.Fatalf("a changed file folds only when too large: %v", got)
	}
	recount := []*domain.DiffFileSummary{awFile("a.ts", 4, 4), awFile("b.ts", 4, 4)}
	if got := FoldedOnLoad(&review, recount, awSummary(false, recount...)); !reflect.DeepEqual(got, []string{"a.ts"}) {
		t.Fatalf("a recount keeps the folds: %v", got)
	}
	budget := []*domain.DiffFileSummary{}
	for index := range 12 {
		budget = append(budget, awFile(string(rune('a'+index))+".ts", domain.DrawnFileLimit, 0))
	}
	fresh := *EmptyDiff
	if got := FoldedOnLoad(&fresh, budget, awSummary(true, budget...)); len(budget)-len(got) != domain.DrawnLineBudget/domain.DrawnFileLimit {
		t.Fatalf("budget: %d folded", len(got))
	}
}

func TestDiffMatchesAndWithDiff(t *testing.T) {
	review := *EmptyDiff
	review.WorkspaceID = new("workspace-a")
	if !DiffMatches(&review, "workspace-a", domain.DefaultBranchRange) || DiffMatches(&review, "workspace-b", domain.DefaultBranchRange) ||
		DiffMatches(&review, "workspace-a", domain.DiffRangeBranches{Base: "main"}) || DiffMatches(EmptyDiff, "workspace-a", domain.DefaultBranchRange) {
		t.Fatalf("matches")
	}
	state := awWorkspace()
	next := WithDiff(state, "owner", func(diff *DiffState) { diff.Split = false })
	if DiffFor(next, "owner").Split || !EmptyDiff.Split || len(state.Diffs) != 0 {
		t.Fatalf("withDiff wrote into what it was given")
	}
}

func TestDiffLoadedKeepsFoldsAndTicksAsTheRendererSees(t *testing.T) {
	awPorted(t, func() {
		state := awReviewing(awFile("a.ts", 1, 0), awFile("b.ts", 1, 0))
		ticked := awApply(state, ReduceDiffs, contracts.DiffCommandDiffSetViewed{Path: "a.ts", Viewed: true}).State
		if review := DiffFor(&ticked, "draft"); !reflect.DeepEqual(review.Collapsed, []string{"a.ts"}) || review.Viewed["a.ts"] != "modified:1:0" {
			t.Fatalf("ticking folds: %#v", review)
		}
		rewritten := awLoaded(&ticked, awSummary(true, awFile("a.ts", 9, 0), awFile("b.ts", 1, 0)))
		if review := DiffFor(rewritten, "draft"); len(review.Viewed) != 0 || len(review.Collapsed) != 0 {
			t.Fatalf("a changed file comes back open and unread: %#v", review)
		}
		ghost := awApply(state, ReduceDiffs, contracts.DiffCommandDiffSetViewed{Path: "ghost.ts", Viewed: true})
		if len(DiffFor(&ghost.State, "draft").Viewed) != 0 {
			t.Fatalf("a file the list does not have cannot be ticked")
		}
	})
}

func TestDiffLoadedDropsAListThatAnswersNothing(t *testing.T) {
	state := awReviewing(awFile("a.ts", 1, 0))
	stale := awApply(state, ReduceDiffs, DiffEventDiffLoaded{Owner: "draft", WorkspaceID: "workspace-a", Range: domain.DiffRangeBranches{Base: "main"}, Result: awSummary(true, awFile("stale.ts", 1, 0))})
	if !jsonx.Same(stale.State.Diffs, state.Diffs) {
		t.Fatalf("a list for another comparison landed")
	}
	other := awApply(state, ReduceDiffs, DiffEventDiffLoaded{Owner: "draft", WorkspaceID: "workspace-a", Range: domain.DefaultBranchRange, Result: awSummary(false, awFile("spaced.ts", 1, 0))})
	if !jsonx.Same(other.State.Diffs, state.Diffs) {
		t.Fatalf("a list counted the other way landed")
	}
}

func TestDiffSetRangeAndModeReadOnlyWhatChanged(t *testing.T) {
	state := awReviewing(awFile("a.ts", 1, 0))
	awPorted(t, func() {
		same := awApply(state, ReduceDiffs, contracts.DiffCommandDiffSetRange{Range: domain.DefaultBranchRange})
		if len(same.Effects) != 0 {
			t.Fatalf("the comparison on screen is read again")
		}
		menu := *state
		menu.OpenMenu = new(DiffModeMenu)
		kept := awApply(&menu, ReduceDiffs, contracts.DiffCommandDiffSetMode{Mode: domain.DiffModeBranch})
		if kept.State.OpenMenu != nil || len(kept.Effects) != 0 {
			t.Fatalf("choosing the shown mode closes the menu and reads nothing")
		}
		changed := awApply(state, ReduceDiffs, contracts.DiffCommandDiffSetRange{Range: domain.DiffRangeBranches{Base: "main"}})
		read := slices.IndexFunc(changed.Effects, func(effect WorkspaceEffect) bool { _, ok := effect.(WorkspaceEffectReadDiff); return ok })
		if read < 0 || DiffFor(&changed.State, "draft").Result != nil || !DiffFor(&changed.State, "draft").Loading {
			t.Fatalf("a new comparison reads afresh: %#v", changed.Effects)
		}
	})
}
