package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func changesAvailable(t *testing.T, result ChangedFilesResult) ChangedFilesAvailable {
	t.Helper()
	available, ok := result.(ChangedFilesAvailable)
	if !ok {
		t.Fatalf("expected available, got %#v", result)
	}
	return available
}

func TestChangedFilesDistinguishesResolutionFailures(t *testing.T) {
	git := NewGit(nil)
	unknown := git.ChangedFiles(bg, "missing", resolverFunc(func(id string) (WorkspaceResolution, error) {
		return WorkspaceResolution{}, &UnknownWorkspaceError{WorkspaceID: id}
	}))
	if got := encode(t, unknown); got != `{"status":"unknown","workspaceId":"missing"}` {
		t.Fatal(got)
	}
	broken := git.ChangedFiles(bg, "broken", resolverFunc(func(string) (WorkspaceResolution, error) {
		return WorkspaceResolution{}, errors.New("registry unavailable")
	}))
	if got := encode(t, broken); got != `{"status":"error","message":"registry unavailable"}` {
		t.Fatal(got)
	}
	gone := git.ChangedFiles(bg, "gone", resolverFunc(func(string) (WorkspaceResolution, error) {
		return WorkspaceResolution{Status: ResolutionUnavailable, Workspace: WorkspaceRecord{ID: "gone", Kind: WorkspaceProject, Root: "/tmp/gone"}, Reason: ReasonMissing}, nil
	}))
	if got := encode(t, gone); got != `{"status":"unavailable","reason":"missing"}` {
		t.Fatal(got)
	}
}

func TestNumstatTotalsIgnoreBinaryFilesAndRenamedPathRecords(t *testing.T) {
	output := strings.Join([]string{"12\t3\tsrc/a.ts", "-\t-\timage.png", "4\t1\t", "old.ts", "new.ts", ""}, "\x00")
	additions, deletions := SummarizeNumstat(output)
	if additions != 16 || deletions != 4 {
		t.Fatal(additions, deletions)
	}
}

func TestChangedFilesReportsExactSummaryWithSafeUntrackedCounts(t *testing.T) {
	root := repository(t, "one\n")
	outside := tempDir(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(root, "no-final-newline.txt"), "one\ntwo")
	writeFile(t, filepath.Join(root, "final-newline.txt"), "one\ntwo\n")
	writeFile(t, filepath.Join(root, "binary.bin"), "\x00\x01\x02")
	writeFile(t, filepath.Join(root, "large.txt"), strings.Repeat("A", 5_000_001))
	writeFile(t, filepath.Join(outside, "secret.txt"), "must\nnot\ncount\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "outside-link.txt")); err != nil {
		t.Fatal(err)
	}

	result := changesAvailable(t, NewGit(nil).ChangedFiles(bg, "fixture", fixture(root)))
	if result.Branch == nil || *result.Branch != "main" || result.Baseline != nil {
		t.Fatalf("%#v", result)
	}
	if result.Additions != 5 || result.Deletions != 0 {
		t.Fatal(result.Additions, result.Deletions)
	}
	files := slices.Sorted(slices.Values(result.Files))
	want := []string{" M tracked.txt", "?? binary.bin", "?? final-newline.txt", "?? large.txt", "?? no-final-newline.txt", "?? outside-link.txt"}
	if !reflect.DeepEqual(files, want) {
		t.Fatal(files)
	}
}

func TestUntrackedLineCountingKeepsEveryFileBeyondOneWorkerPool(t *testing.T) {
	root := repository(t, "one\n")
	var files []string
	for index := range 24 {
		file := fmt.Sprintf("fresh-%02d.txt", index)
		files = append(files, "?? "+file)
		writeFile(t, filepath.Join(root, file), strings.Repeat("line\n", index+1))
	}
	result := changesAvailable(t, NewGit(nil).ChangedFiles(bg, "fixture", fixture(root)))
	if result.Additions != 300 || result.Deletions != 0 || !reflect.DeepEqual(result.Files, files) {
		t.Fatalf("%v %v %v", result.Additions, result.Deletions, result.Files)
	}
}

func withOrigin(t *testing.T, root string) {
	t.Helper()
	origin := tempDir(t)
	gitT(t, origin, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", origin)
	gitT(t, root, "push", "origin", "main")
}

func TestChangedFilesCountsCommittedWorkAgainstOriginDefaultBranch(t *testing.T) {
	root := repository(t, "one\n")
	withOrigin(t, root)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	gitT(t, root, "commit", "-am", "committed by the thread")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")
	writeFile(t, filepath.Join(root, "untracked.txt"), "four\n")

	result := changesAvailable(t, NewGit(nil).ChangedFiles(bg, "fixture", fixture(root)))
	if result.Baseline == nil || *result.Baseline != "origin/main" || result.Additions != 3 || result.Deletions != 0 {
		t.Fatalf("%#v", result)
	}
}

func TestComparisonBasesAreCachedPerRepository(t *testing.T) {
	root := repository(t, "one\n")
	other := repository(t, "one\n")
	withOrigin(t, root)
	git := NewGit(nil)

	first := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root)))
	gitT(t, root, "update-ref", "-d", "refs/remotes/origin/main")
	cached := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root)))
	unrelated := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(other)))

	if first.Baseline == nil || *first.Baseline != "origin/main" {
		t.Fatal("first", first.Baseline)
	}
	if cached.Baseline == nil || *cached.Baseline != "origin/main" {
		t.Fatal("the cached commit outlives a ref disappearing between polls")
	}
	if unrelated.Baseline != nil {
		t.Fatal("another root does not inherit the first repository's base")
	}
}

func TestCachedComparisonBaseFollowsNewHead(t *testing.T) {
	root := repository(t, "one\n")
	withOrigin(t, root)
	git := NewGit(nil)
	initial := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root)))
	if initial.Baseline == nil || *initial.Baseline != "origin/main" {
		t.Fatal(initial.Baseline)
	}
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\n")
	gitT(t, root, "commit", "-am", "advance main")
	gitT(t, root, "push", "origin", "main")
	gitT(t, root, "checkout", "-b", "feature")
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\ntwo\nthree\n")
	gitT(t, root, "commit", "-am", "feature work")

	result := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root)))
	if *result.Branch != "feature" || *result.Baseline != "origin/main" || result.Additions != 1 {
		t.Fatalf("the new branch is compared with its own merge base: %#v", result)
	}
}

func TestChangedFilesReportsDetachedHeadAndNonGitFailures(t *testing.T) {
	root := repository(t, "one\n")
	nonGit := tempDir(t)
	short := strings.TrimSpace(gitOut(t, root, "rev-parse", "--short", "HEAD"))
	gitT(t, root, "checkout", "--detach")
	git := NewGit(nil)

	detached := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root)))
	if *detached.Branch != "detached@"+short {
		t.Fatal(*detached.Branch)
	}
	failed, ok := git.ChangedFiles(bg, "fixture", fixture(nonGit)).(Failure)
	if !ok || !regexp.MustCompile(`(?i)not a git repository`).MatchString(failed.Message) {
		t.Fatalf("%#v", failed)
	}
	if !strings.HasPrefix(failed.Message, "Command failed: git ") {
		t.Fatalf("worded as Node words it: %q", failed.Message)
	}
}

func TestUntrackedFileIsOnlyReadAgainOnceItMoves(t *testing.T) {
	root := repository(t, "one\n")
	file := filepath.Join(root, "notes.txt")
	written := time.UnixMilli(1_760_000_000_000)
	writeFile(t, file, "one\ntwo\n")
	if err := os.Chtimes(file, written, written); err != nil {
		t.Fatal(err)
	}
	git := NewGit(nil)
	if first := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root))); first.Additions != 2 {
		t.Fatal(first.Additions)
	}
	writeFile(t, file, strings.Repeat("\n", 8))
	if err := os.Chtimes(file, written, written); err != nil {
		t.Fatal(err)
	}
	if unchanged := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root))); unchanged.Additions != 2 {
		t.Fatal("a file that has not moved is not opened again", unchanged.Additions)
	}
	if err := os.Chtimes(file, written, written.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if moved := changesAvailable(t, git.ChangedFiles(bg, "fixture", fixture(root))); moved.Additions != 8 {
		t.Fatal("a file that moved is counted again", moved.Additions)
	}
}
