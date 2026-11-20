package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func summaryAvailable(t *testing.T, result DiffSummaryResult) DiffSummaryAvailable {
	t.Helper()
	available, ok := result.(DiffSummaryAvailable)
	if !ok {
		t.Fatalf("expected available, got %#v", result)
	}
	return available
}

func patchAvailable(t *testing.T, result DiffPatchResult) string {
	t.Helper()
	available, ok := result.(DiffPatchAvailable)
	if !ok {
		t.Fatalf("expected available, got %#v", result)
	}
	return available.Patch
}

// patchLines keeps the hunk lines of one kind, markers removed: '+' added, '-' deleted, ' ' context.
func patchLines(patch string, kind byte) []string {
	var lines []string
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if inHunk && line != "" && line[0] == kind {
			lines = append(lines, line[1:])
		}
	}
	return lines
}

func paths(files []DiffFileSummary) []string {
	list := []string{}
	for _, file := range files {
		list = append(list, file.Path)
	}
	return list
}

var uncommitted = DiffRange{Kind: RangeUncommitted}

func TestCommitComparisonIncludesRootCommitAndIgnoresLaterWork(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	git := NewGit(nil)
	commit := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	r := DiffRange{Kind: RangeCommit, Commit: commit[:7]}
	writeFile(t, filepath.Join(root, "tracked.txt"), "later edits\n")
	writeFile(t, filepath.Join(root, "untracked.txt"), "untracked\n")

	summary := summaryAvailable(t, git.DiffSummary(bg, "fixture", r, fixture(root), false))
	if len(summary.Files) != 1 || summary.Files[0].Path != "tracked.txt" || summary.Files[0].Additions != 2 {
		t.Fatalf("%#v", summary.Files)
	}
	patch := patchAvailable(t, git.DiffPatch(bg, "fixture", r, "tracked.txt", fixture(root), nil, false))
	if !strings.Contains(patch, "+one\n+two") || strings.Contains(patch, "later edits") {
		t.Fatal(patch)
	}
	missing, ok := git.DiffSummary(bg, "fixture", DiffRange{Kind: RangeCommit, Commit: "0000000"}, fixture(root), false).(Failure)
	if !ok || !strings.Contains(missing.Message, "not available in this repository") {
		t.Fatalf("%#v", missing)
	}
}

func TestMergeCommitShowsWhatItIntroducedAgainstFirstParent(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	gitT(t, root, "checkout", "-b", "side")
	writeFile(t, filepath.Join(root, "side.txt"), "side\n")
	gitT(t, root, "add", "side.txt")
	gitT(t, root, "commit", "-m", "side")
	gitT(t, root, "checkout", "main")
	writeFile(t, filepath.Join(root, "main.txt"), "main\n")
	gitT(t, root, "add", "main.txt")
	gitT(t, root, "commit", "-m", "main")
	gitT(t, root, "merge", "--no-ff", "side", "-m", "merge")
	commit := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))

	summary := summaryAvailable(t, NewGit(nil).DiffSummary(bg, "fixture", DiffRange{Kind: RangeCommit, Commit: commit}, fixture(root), false))
	if !reflect.DeepEqual(paths(summary.Files), []string{"side.txt"}) {
		t.Fatal(paths(summary.Files))
	}
}

func TestSummaryDistinguishesUnknownWorkspaces(t *testing.T) {
	result := NewGit(nil).DiffSummary(bg, "missing", uncommitted, resolverFunc(func(id string) (WorkspaceResolution, error) {
		return WorkspaceResolution{}, &UnknownWorkspaceError{WorkspaceID: id}
	}), false)
	if got := encode(t, result); got != `{"status":"unknown","workspaceId":"missing"}` {
		t.Fatal(got)
	}
}

func TestSummaryListsUncommittedTrackedAndUntracked(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(root, "fresh.txt"), "new\n")

	result := summaryAvailable(t, NewGit(nil).DiffSummary(bg, "fixture", uncommitted, fixture(root), false))
	var got [][3]any
	for _, file := range result.Files {
		got = append(got, [3]any{file.Path, file.Status, file.Additions})
	}
	want := [][3]any{{"fresh.txt", FileUntracked, 1.0}, {"tracked.txt", FileModified, 1.0}}
	if !reflect.DeepEqual(got, want) || result.Additions != 2 {
		t.Fatal(got, result.Additions)
	}
	encoded := encode(t, result)
	if encoded != `{"status":"available","range":{"kind":"uncommitted"},"ignoreWhitespace":false,"files":[{"path":"fresh.txt","status":"untracked","additions":1,"deletions":0,"binary":false},{"path":"tracked.txt","status":"modified","additions":1,"deletions":0,"binary":false}],"additions":2,"deletions":0}` {
		t.Fatal(encoded)
	}
}

func TestSummaryNamesWhatACommitAddedAndTookAway(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	gitT(t, root, "checkout", "-b", "topic")
	writeFile(t, filepath.Join(root, "added.txt"), "a\n")
	os.Remove(filepath.Join(root, "tracked.txt"))
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "shuffle")

	result := summaryAvailable(t, NewGit(nil).DiffSummary(bg, "fixture", DiffRange{Kind: RangeBranches, Base: "main", Compare: ptr("topic")}, fixture(root), false))
	var got [][2]any
	for _, file := range result.Files {
		got = append(got, [2]any{file.Path, file.Status})
	}
	if !reflect.DeepEqual(got, [][2]any{{"added.txt", FileAdded}, {"tracked.txt", FileDeleted}}) {
		t.Fatal(got)
	}
}

func TestBranchComparisonMeasuresFromWhereTheSidesLastAgreed(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	gitT(t, root, "checkout", "-b", "topic")
	writeFile(t, filepath.Join(root, "mine.txt"), "mine\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "mine")
	gitT(t, root, "checkout", "main")
	writeFile(t, filepath.Join(root, "theirs.txt"), "theirs\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "theirs")

	result := summaryAvailable(t, NewGit(nil).DiffSummary(bg, "fixture", DiffRange{Kind: RangeBranches, Base: "main", Compare: ptr("topic")}, fixture(root), false))
	if !reflect.DeepEqual(paths(result.Files), []string{"mine.txt"}) {
		t.Fatal(paths(result.Files))
	}
}

func TestRepositoryWithNoCommitsListsWhatItHolds(t *testing.T) {
	for _, r := range []DiffRange{uncommitted, {Kind: RangeBranches, Base: "HEAD"}} {
		t.Run(rangeKey(r), func(t *testing.T) {
			root := tempDir(t)
			initRepository(t, root)
			writeFile(t, filepath.Join(root, "first.txt"), "one\ntwo\n")
			git := NewGit(nil)

			result := summaryAvailable(t, git.DiffSummary(bg, "fixture", r, fixture(root), false))
			if len(result.Files) != 1 || result.Files[0].Path != "first.txt" || result.Files[0].Additions != 2 {
				t.Fatalf("%#v", result.Files)
			}
			patch := patchAvailable(t, git.DiffPatch(bg, "fixture", r, "first.txt", fixture(root), nil, false))
			if !reflect.DeepEqual(patchLines(patch, '+'), []string{"one", "two"}) {
				t.Fatal(patch)
			}
		})
	}
}

func TestFileWhoseNameHoldsATabIsStillOneFile(t *testing.T) {
	files := ReadNumstat("2\t1\ttabbed\tname.txt\x00", map[string]DiffFileStatus{})
	if len(files) != 1 || files[0].Path != "tabbed\tname.txt" || files[0].Additions != 2 || files[0].Deletions != 1 {
		t.Fatalf("%#v", files)
	}
}

func TestNumstatReadsRenamesWithBothPaths(t *testing.T) {
	files := ReadNumstat("3\t1\t\x00old/name.ts\x00new/name.ts\x00-\t-\timage.png\x00", readNameStatus("R090\x00old/name.ts\x00new/name.ts\x00A\x00image.png\x00"))
	if len(files) != 2 || files[0].Path != "new/name.ts" || *files[0].PreviousPath != "old/name.ts" || files[0].Status != FileRenamed {
		t.Fatalf("%#v", files)
	}
	if files[1].Status != FileAdded || !files[1].Binary || files[1].Additions != 0 {
		t.Fatalf("%#v", files[1])
	}
}

func TestPatchIsReadForOneFileAndUntrackedIsDiffedAgainstEmptiness(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\nTWO\n")
	writeFile(t, filepath.Join(root, "fresh.txt"), "brand\nnew\n")
	git := NewGit(nil)

	tracked := patchAvailable(t, git.DiffPatch(bg, "fixture", uncommitted, "tracked.txt", fixture(root), nil, false))
	if !reflect.DeepEqual(patchLines(tracked, ' '), []string{"one"}) || !reflect.DeepEqual(patchLines(tracked, '-'), []string{"two"}) || !reflect.DeepEqual(patchLines(tracked, '+'), []string{"TWO"}) {
		t.Fatal(tracked)
	}
	fresh := patchAvailable(t, git.DiffPatch(bg, "fixture", uncommitted, "fresh.txt", fixture(root), nil, false))
	if !reflect.DeepEqual(patchLines(fresh, '+'), []string{"brand", "new"}) {
		t.Fatal(fresh)
	}
}

func TestPathIsAPathNotAGlob(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\nTWO\n")
	patch := patchAvailable(t, NewGit(nil).DiffPatch(bg, "fixture", uncommitted, "tr*.txt", fixture(root), nil, false))
	if strings.TrimSpace(patch) != "" {
		t.Fatal("nothing is named by that path", patch)
	}
}

func TestRenamePatchShowsWhatChanged(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	gitT(t, root, "mv", "tracked.txt", "moved.txt")
	writeFile(t, filepath.Join(root, "moved.txt"), "one\ntwo\nthree\n")

	patch := patchAvailable(t, NewGit(nil).DiffPatch(bg, "fixture", uncommitted, "moved.txt", fixture(root), ptr("tracked.txt"), false))
	if !reflect.DeepEqual(patchLines(patch, '+'), []string{"three"}) || len(patchLines(patch, '-')) != 0 {
		t.Fatal("the old path is not re-added", patch)
	}
}

func TestSummaryIgnoringWhitespaceDropsFilesWhoseLinesOnlyMoved(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "spaced.txt"), "one\ntwo\n")
	gitT(t, root, "add", "spaced.txt")
	gitT(t, root, "commit", "-m", "spaced")
	writeFile(t, filepath.Join(root, "spaced.txt"), "  one\ntwo  \n")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n  two\nthree\n")
	git := NewGit(nil)

	counted := summaryAvailable(t, git.DiffSummary(bg, "fixture", uncommitted, fixture(root), false))
	if !reflect.DeepEqual(paths(counted.Files), []string{"spaced.txt", "tracked.txt"}) || counted.IgnoreWhitespace {
		t.Fatal(paths(counted.Files))
	}
	ignored := summaryAvailable(t, git.DiffSummary(bg, "fixture", uncommitted, fixture(root), true))
	if len(ignored.Files) != 1 || ignored.Files[0].Path != "tracked.txt" || ignored.Files[0].Additions != 1 || ignored.Files[0].Deletions != 0 || !ignored.IgnoreWhitespace {
		t.Fatalf("%#v", ignored)
	}
}

func TestPatchIgnoringWhitespaceKeepsOnlyRewrittenLines(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "tracked.txt"), "  one\ntwo!\n")
	git := NewGit(nil)

	counted := patchAvailable(t, git.DiffPatch(bg, "fixture", uncommitted, "tracked.txt", fixture(root), nil, false))
	if !reflect.DeepEqual(patchLines(counted, '+'), []string{"  one", "two!"}) {
		t.Fatal(counted)
	}
	ignored := patchAvailable(t, git.DiffPatch(bg, "fixture", uncommitted, "tracked.txt", fixture(root), nil, true))
	if !reflect.DeepEqual(patchLines(ignored, '+'), []string{"two!"}) {
		t.Fatal(ignored)
	}
}

func TestPatchOutsideTheWorkspaceIsRefused(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	result := NewGit(nil).DiffPatch(bg, "fixture", uncommitted, "../escape.txt", fixture(root), nil, false)
	if got := encode(t, result); got != `{"status":"error","message":"Path is outside the workspace."}` {
		t.Fatal(got)
	}
}

func TestPatchPastTheCeilingIsTooLarge(t *testing.T) {
	root := repository(t, "one\ntwo\n")
	writeFile(t, filepath.Join(root, "tracked.txt"), strings.Repeat("a line long enough to add up quickly\n", 80_000))
	result := NewGit(nil).DiffPatch(bg, "fixture", uncommitted, "tracked.txt", fixture(root), nil, false)
	if got := encode(t, result); got != `{"status":"too-large","limit":2000000}` {
		t.Fatal(got)
	}
}

func TestDiffRangeEncodesAsTheRendererNamesIt(t *testing.T) {
	cases := map[string]DiffRange{
		`{"kind":"uncommitted"}`:                           uncommitted,
		`{"kind":"commit","commit":"abc1234"}`:             {Kind: RangeCommit, Commit: "abc1234"},
		`{"kind":"branches","base":"main","compare":null}`: {Kind: RangeBranches, Base: "main"},
		`{"kind":"branches","base":"main","compare":"x"}`:  {Kind: RangeBranches, Base: "main", Compare: ptr("x")},
	}
	for want, r := range cases {
		if got := encode(t, r); got != want {
			t.Fatal(got, want)
		}
	}
	if !regexp.MustCompile(`^branches:main:$`).MatchString(rangeKey(DiffRange{Kind: RangeBranches, Base: "main"})) {
		t.Fatal(rangeKey(DiffRange{Kind: RangeBranches, Base: "main"}))
	}
}
