package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeRegistry records what the service registers and forgets.
type fakeRegistry struct {
	mu       sync.Mutex
	sequence int
	records  map[string]WorkspaceRecord
}

func newFakeRegistry() *fakeRegistry { return &fakeRegistry{records: map[string]WorkspaceRecord{}} }

func (r *fakeRegistry) RegisterWorktree(root string) (WorkspaceResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	record := WorkspaceRecord{ID: "workspace-" + strconv.Itoa(r.sequence), Kind: WorkspaceWorktree, Root: root}
	r.records[root] = record
	return WorkspaceResolution{Status: ResolutionAvailable, Workspace: record}, nil
}

func (r *fakeRegistry) ForgetWorktree(root string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, root)
	return nil
}

func (r *fakeRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.records)
}

type testWorktrees struct {
	*WorktreeService
	root     string
	registry *fakeRegistry
}

func newTestWorktrees(t *testing.T) testWorktrees {
	root := tempDir(t)
	registry := newFakeRegistry()
	return testWorktrees{NewWorktreeService(NewGit(nil), WorktreeServiceOptions{WorktreesRoot: root, Workspaces: registry}), root, registry}
}

func worktreeRepository(t *testing.T) string {
	root := tempDir(t)
	initRepository(t, root)
	writeFile(t, filepath.Join(root, "tracked.txt"), "one\n")
	writeFile(t, filepath.Join(root, ".gitignore"), "secrets/\n.env\n*.log\n")
	gitT(t, root, "add", "-A")
	gitT(t, root, "commit", "-m", "initial")
	return root
}

func listWorktrees(t *testing.T, root string) []string {
	var list []string
	for _, line := range strings.Split(gitOut(t, root, "worktree", "list", "--porcelain"), "\n") {
		if after, ok := strings.CutPrefix(line, "worktree "); ok {
			list = append(list, after)
		}
	}
	return list
}

func create(t *testing.T, worktrees testWorktrees, root string, carry bool) CreatedWorktree {
	t.Helper()
	created, err := worktrees.Create(bg, CreateWorktreeRequest{ProjectRoot: root, CarryChanges: carry})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func release(t *testing.T, worktrees testWorktrees, request ReleaseWorktreeRequest) WorktreeSnapshot {
	t.Helper()
	snapshot, err := worktrees.Release(bg, request)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestWorktreeStartsDetachedAtWhatTheProjectHasCheckedOut(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	head := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)

	if worktree.BaseCommit != head || !worktrees.git.IsDetached(bg, worktree.Root) || !exists(filepath.Join(worktree.Root, "tracked.txt")) || worktree.WorkspaceID == "" {
		t.Fatalf("%#v", worktree)
	}
	if !regexp.MustCompile(`/[a-z0-9-]+-[0-9a-f]{8}$`).MatchString(worktree.Root) || filepath.Dir(worktree.Root) != worktrees.root {
		t.Fatal(worktree.Root)
	}
	if got := listWorktrees(t, root); !slices.Equal(got, []string{root, worktree.Root}) {
		t.Fatal(got)
	}
}

func TestWorktreeFollowsTheBranchTheProjectIsOn(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	gitT(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(root, "tracked.txt"), "two\n")
	gitT(t, root, "commit", "-qam", "feature work")
	featureHead := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	worktree := create(t, newTestWorktrees(t), root, false)
	if worktree.BaseCommit != featureHead || readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "two\n" {
		t.Fatal(worktree)
	}
}

func TestWorktreeFromANamedBranch(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	gitT(t, root, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(root, "tracked.txt"), "two\n")
	gitT(t, root, "commit", "-qam", "feature work")
	gitT(t, root, "checkout", "-q", "main")
	worktrees := newTestWorktrees(t)
	worktree, err := worktrees.Create(bg, CreateWorktreeRequest{ProjectRoot: root, Branch: ptr("feature")})
	if err != nil || readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "two\n" {
		t.Fatal(err)
	}
	if _, err := worktrees.Create(bg, CreateWorktreeRequest{ProjectRoot: root, Branch: ptr("--orphan")}); err == nil || err.Error() != "Invalid ref name: --orphan" {
		t.Fatal(err)
	}
}

func TestWorktreeIncludeCopiesTheIgnoredFilesItNames(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	writeFile(t, filepath.Join(root, ".env"), "TOKEN=abc\n")
	writeFile(t, filepath.Join(root, "debug.log"), "noise\n")
	writeFile(t, filepath.Join(root, "secrets", "key.pem"), "private\n")
	writeFile(t, filepath.Join(root, ".worktreeinclude"), ".env\nsecrets/\n")
	worktree := create(t, newTestWorktrees(t), root, false)

	if readFile(t, filepath.Join(worktree.Root, ".env")) != "TOKEN=abc\n" || readFile(t, filepath.Join(worktree.Root, "secrets", "key.pem")) != "private\n" {
		t.Fatal("included files not copied")
	}
	if exists(filepath.Join(worktree.Root, "debug.log")) {
		t.Fatal("an ignored file the patterns do not name stays behind")
	}
}

func TestWorktreeIncludeNeverDuplicatesATrackedFile(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	writeFile(t, filepath.Join(root, ".worktreeinclude"), "tracked.txt\n")
	worktree := create(t, newTestWorktrees(t), root, false)
	if readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "one\n" {
		t.Fatal("the checkout's own copy is untouched")
	}
}

func TestWorktreeIncludeHonoursNegation(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	writeFile(t, filepath.Join(root, ".env"), "TOKEN=abc\n")
	writeFile(t, filepath.Join(root, "keep.log"), "keep\n")
	writeFile(t, filepath.Join(root, "drop.log"), "drop\n")
	writeFile(t, filepath.Join(root, ".worktreeinclude"), "*.log\n!drop.log\n")
	worktree := create(t, newTestWorktrees(t), root, false)
	if !exists(filepath.Join(worktree.Root, "keep.log")) || exists(filepath.Join(worktree.Root, "drop.log")) || exists(filepath.Join(worktree.Root, ".env")) {
		t.Fatal("only the named patterns are copied")
	}
}

func TestMovingThreadCarriesUncommittedWork(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "edited\n")
	writeFile(t, filepath.Join(root, "fresh.txt"), "new file\n")
	worktree := create(t, newTestWorktrees(t), root, true)
	if readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "edited\n" || readFile(t, filepath.Join(worktree.Root, "fresh.txt")) != "new file\n" {
		t.Fatal("work not carried")
	}
	if readFile(t, filepath.Join(root, "tracked.txt")) != "edited\n" || !exists(filepath.Join(root, "fresh.txt")) {
		t.Fatal("the project checkout keeps its own copy")
	}
}

func TestNewThreadWorktreeStartsClean(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	writeFile(t, filepath.Join(root, "tracked.txt"), "edited\n")
	worktree := create(t, newTestWorktrees(t), root, false)
	if readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "one\n" {
		t.Fatal("dirty")
	}
}

func TestMovingThreadCarriesUntrackedSymlinksAsSymlinks(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	if err := os.Symlink("tracked.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	worktree := create(t, newTestWorktrees(t), root, true)
	if target, err := os.Readlink(filepath.Join(worktree.Root, "link.txt")); err != nil || target != "tracked.txt" {
		t.Fatal(target, err)
	}
}

func TestWorktreesOfOneProjectStayIndependent(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	first := create(t, worktrees, root, false)
	second := create(t, worktrees, root, false)
	writeFile(t, filepath.Join(first.Root, "tracked.txt"), "first\n")
	if first.Root == second.Root || readFile(t, filepath.Join(second.Root, "tracked.txt")) != "one\n" || readFile(t, filepath.Join(root, "tracked.txt")) != "one\n" {
		t.Fatal("not independent")
	}
}

func TestBranchNamesReadAsOptionsAreRefused(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	git := NewGit(nil)
	if err := git.CreateBranch(bg, root, "--force"); err == nil || !strings.Contains(err.Error(), "Invalid ref name") {
		t.Fatal(err)
	}
	if err := git.CheckoutBranch(bg, root, "-b"); err == nil || !strings.Contains(err.Error(), "Invalid ref name") {
		t.Fatal(err)
	}
}

func TestReleasingADetachedWorktreeCommitsItsWorkAndKeepsItByRef(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "worktree work\n")
	writeFile(t, filepath.Join(worktree.Root, "added.txt"), "also new\n")

	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-1"), Title: "Rename dock tabs", Release: ReleaseReturnedToLocal})
	if snapshot.Commit == nil || snapshot.ShortCommit == nil || snapshot.Ref == nil || *snapshot.Ref != "refs/aicodingtool/"+worktree.ID {
		t.Fatalf("%#v", snapshot)
	}
	message := gitOut(t, root, "log", "-1", "--format=%B", *snapshot.Commit)
	if !strings.HasPrefix(message, `aicodingtool: snapshot "Rename dock tabs"`) || !strings.Contains(message, "Thread thread-1 · returned to local") || !strings.Contains(message, "Ref refs/aicodingtool/"+worktree.ID) {
		t.Fatal(message)
	}
	if exists(worktree.Root) || !slices.Equal(listWorktrees(t, root), []string{root}) || worktrees.registry.size() != 0 {
		t.Fatal("a released worktree leaves no directory, registration or workspace record behind")
	}
	if preserved := strings.TrimSpace(gitOut(t, root, "show", "--format=%H", "-s", *snapshot.Ref)); preserved != *snapshot.Commit {
		t.Fatal(preserved)
	}
	files := gitOut(t, root, "show", "--name-only", "--format=", *snapshot.Commit)
	if !strings.Contains(files, "tracked.txt") || !strings.Contains(files, "added.txt") {
		t.Fatal(files)
	}
}

func TestReleasingACleanWorktreeCommitsNothing(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-2"), Title: "Nothing to save", Release: ReleaseReturnedToLocal})
	if got := encode(t, snapshot); got != `{"commit":null,"shortCommit":null,"ref":null}` {
		t.Fatal(got)
	}
}

func TestReleasingACleanWorktreeKeepsDetachedCommits(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "committed detached\n")
	gitT(t, worktree.Root, "commit", "-qam", "detached work")
	head := strings.TrimSpace(gitOut(t, worktree.Root, "rev-parse", "HEAD"))

	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-2b"), Title: "Committed but detached", Release: ReleaseReturnedToLocal})
	if snapshot.Commit != nil || snapshot.Ref == nil || *snapshot.Ref != "refs/aicodingtool/"+worktree.ID {
		t.Fatalf("%#v", snapshot)
	}
	if preserved := strings.TrimSpace(gitOut(t, root, "rev-parse", *snapshot.Ref)); preserved != head {
		t.Fatal("the thread's own commits stay reachable after the directory goes")
	}
}

func TestBranchTheThreadMadeHoldsTheSnapshot(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	gitT(t, worktree.Root, "checkout", "-q", "-b", "thread-work")
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "on a branch\n")
	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-3"), Title: "Branched work", Release: ReleaseReturnedToLocal})
	if snapshot.Commit == nil || snapshot.Ref != nil {
		t.Fatalf("%#v", snapshot)
	}
	if branchHead := strings.TrimSpace(gitOut(t, root, "rev-parse", "thread-work")); branchHead != *snapshot.Commit {
		t.Fatal(branchHead)
	}
}

func TestSnapshotSurvivesAHookThatRejectsCommits(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	hooks := filepath.Join(root, ".git", "hooks")
	mkdir(t, hooks)
	if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "must not be lost\n")
	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-4"), Title: "Hook says no", Release: ReleaseReturnedToLocal})
	if snapshot.Commit == nil {
		t.Fatal("a hook must not be able to discard the work")
	}
}

func TestReleasingAWorktreeWhoseDirectoryIsGoneTidiesUp(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	os.RemoveAll(worktree.Root)
	snapshot := release(t, worktrees, ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, TaskID: ptr("thread-5"), Title: "Removed from underneath", Release: ReleaseReturnedToLocal})
	if !reflect.DeepEqual(snapshot, WorktreeSnapshot{}) || worktrees.registry.size() != 0 {
		t.Fatalf("%#v", snapshot)
	}
}

func TestDeletingAWorktreeTakesUncommittedWorkAndLeavesBranches(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	gitT(t, worktree.Root, "checkout", "-q", "-b", "kept-branch")
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "committed on the branch\n")
	gitT(t, worktree.Root, "commit", "-qam", "branch work")
	writeFile(t, filepath.Join(worktree.Root, "tracked.txt"), "never committed\n")

	if err := worktrees.Delete(bg, worktree.Root); err != nil {
		t.Fatal(err)
	}
	if exists(worktree.Root) || !slices.Equal(listWorktrees(t, root), []string{root}) {
		t.Fatal("not removed")
	}
	if kept := gitOut(t, root, "show", "kept-branch:tracked.txt"); kept != "committed on the branch\n" {
		t.Fatal(kept)
	}
}

func TestWorktreeWhoseRepositoryIsGoneCanStillBeDeleted(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	os.RemoveAll(root)
	if err := worktrees.Delete(bg, worktree.Root); err != nil {
		t.Fatal(err)
	}
	if exists(worktree.Root) || worktrees.registry.size() != 0 {
		t.Fatal("the directory goes even with no git to remove it")
	}
}

func TestReleaseAndDeleteRefuseADirectoryOutsideTheWorktreesRoot(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	if err := worktrees.Delete(bg, root); err == nil || err.Error() != "Not an AI Coding Tool worktree: "+root {
		t.Fatal(err)
	}
	if _, err := worktrees.Release(bg, ReleaseWorktreeRequest{WorktreeID: "x", Root: root, Title: "elsewhere", Release: ReleaseDeleted}); err == nil || !strings.Contains(err.Error(), "Not an AI Coding Tool worktree") {
		t.Fatal(err)
	}
	if !exists(filepath.Join(root, "tracked.txt")) {
		t.Fatal("the refused directory is untouched")
	}
	if _, err := worktrees.OwnedPath(filepath.Join(worktrees.root, "..", filepath.Base(worktrees.root)+"-x", "y")); err == nil {
		t.Fatal("a sibling of the root is not under it")
	}
}

func TestManualListIncludesCurrentAndLegacyWorktrees(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	legacyRoot := tempDir(t)
	registry := newFakeRegistry()
	git := NewGit(nil)
	legacy, err := NewWorktreeService(git, WorktreeServiceOptions{WorktreesRoot: legacyRoot, Workspaces: registry}).Create(bg, CreateWorktreeRequest{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	worktreesRoot := tempDir(t)
	moved := NewWorktreeService(git, WorktreeServiceOptions{WorktreesRoot: worktreesRoot, LegacyRoots: []string{legacyRoot}, Workspaces: registry})
	made, err := moved.Create(bg, CreateWorktreeRequest{ProjectRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := moved.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(made.Root) != worktreesRoot {
		t.Fatal("new checkouts only ever land in the current root")
	}
	var roots []string
	for _, worktree := range listed {
		roots = append(roots, worktree.Root)
		if worktree.Repository == nil || *worktree.Repository != root || worktree.Branch != nil {
			t.Fatalf("%#v", worktree)
		}
	}
	if want := slices.Sorted(slices.Values([]string{legacy.Root, made.Root})); !slices.Equal(roots, want) {
		t.Fatal(roots)
	}
	if !exists(legacy.Root) || !exists(made.Root) {
		t.Fatal("listing never removes a checkout")
	}
	if err := moved.Delete(bg, legacy.Root); err != nil || exists(legacy.Root) {
		t.Fatal("the old checkout remains manually removable", err)
	}
}

func TestBranchesReportTheCurrentBranchAndADetachedCheckout(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	git := NewGit(nil)
	branches, err := git.ListBranches(bg, root)
	if err != nil || branches.Current == nil || *branches.Current != "main" {
		t.Fatal(branches, err)
	}
	gitT(t, root, "checkout", "-q", "--detach")
	detached, _ := git.ListBranches(bg, root)
	if detached.Current != nil || !slices.Contains(detached.Branches, "main") {
		t.Fatalf("%#v", detached)
	}
	if got := encode(t, BranchesAvailable{Branches: detached}); got != `{"status":"available","branches":["main"],"remotes":[],"current":null}` {
		t.Fatal(got)
	}
}

func TestKnownCommitsKeepsTheHashesTheRepositoryHolds(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	head := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD^{tree}"))
	known, err := NewGit(nil).KnownCommits(bg, root, []string{"0000000", head[:8], tree, head})
	if err != nil || !slices.Equal(known, []string{head[:8], head}) {
		t.Fatal(known, err)
	}
}

func TestListingCountsRenamesAndUntrackedAndComparesDetachedCommits(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	gitT(t, worktree.Root, "commit", "--allow-empty", "-m", "detached work")
	gitT(t, worktree.Root, "mv", "tracked.txt", "renamed\nfile.txt")
	writeFile(t, filepath.Join(worktree.Root, "new", "one.txt"), "one")
	writeFile(t, filepath.Join(worktree.Root, "new", "two.txt"), "two")
	writeFile(t, filepath.Join(worktree.Root, ".env"), "ignored")
	before := gitOut(t, worktree.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	listed, err := worktrees.List(bg)
	if err != nil || len(listed) != 1 || listed[0].Branch != nil {
		t.Fatal(listed, err)
	}
	if got := encode(t, listed[0].Status); got != `{"changedFiles":3,"comparison":{"branch":"main","ahead":1}}` {
		t.Fatal(got)
	}
	if after := gitOut(t, worktree.Root, "status", "--porcelain=v1", "-z", "--untracked-files=all"); after != before {
		t.Fatal("listing changed git")
	}
}

func TestListingComparesWithTheRecordedOriginDefaultBranch(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	gitT(t, root, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	gitT(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	gitT(t, worktree.Root, "checkout", "-b", "feature")
	listed, _ := worktrees.List(bg)
	if listed[0].Branch == nil || *listed[0].Branch != "feature" {
		t.Fatal(listed[0].Branch)
	}
	if got := encode(t, listed[0].Status); got != `{"changedFiles":0,"comparison":{"branch":"origin/trunk","ahead":0}}` {
		t.Fatal(got)
	}
}

func TestListingKeepsUnknownComparisonsAndUnavailableRepositoriesDistinct(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	gitT(t, root, "branch", "-m", "trunk")
	worktrees := newTestWorktrees(t)
	create(t, worktrees, root, false)
	orphan := filepath.Join(worktrees.root, "orphan")
	writeFile(t, filepath.Join(orphan, "important.txt"), "preserve")
	listed, err := worktrees.List(bg)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range listed {
		switch {
		case item.Root == orphan:
			if item.Repository != nil || encode(t, item.Status) != `{"changedFiles":null,"comparison":null}` || item.ID != "orphan" {
				t.Fatalf("%#v", item)
			}
		case item.Repository != nil && *item.Repository == root:
			if got := encode(t, item.Status); got != `{"changedFiles":0,"comparison":null}` {
				t.Fatal(got)
			}
		default:
			t.Fatalf("%#v", item)
		}
	}
	if readFile(t, filepath.Join(orphan, "important.txt")) != "preserve" {
		t.Fatal("orphan changed")
	}
}

func TestForgettingRefusesAFolderThatExists(t *testing.T) {
	t.Parallel()
	root := worktreeRepository(t)
	worktrees := newTestWorktrees(t)
	worktree := create(t, worktrees, root, false)
	request := ReleaseWorktreeRequest{WorktreeID: worktree.ID, Root: worktree.Root, Title: "Missing folder", Release: ReleaseDeleted, MissingOnly: ptr(true)}
	if _, err := worktrees.Release(bg, request); err == nil || !strings.Contains(err.Error(), "folder exists again") {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(worktree.Root, "tracked.txt")) != "one\n" || worktrees.registry.size() != 1 {
		t.Fatal("the folder and its registration stay")
	}
	os.RemoveAll(worktree.Root)
	if snapshot := release(t, worktrees, request); !reflect.DeepEqual(snapshot, WorktreeSnapshot{}) || worktrees.registry.size() != 0 {
		t.Fatal(snapshot)
	}
}

func TestARootThatCannotBeListedFailsTheScan(t *testing.T) {
	t.Parallel()
	file := filepath.Join(tempDir(t), "file")
	writeFile(t, file, "not a directory")
	worktrees := NewWorktreeService(NewGit(nil), WorktreeServiceOptions{WorktreesRoot: file, Workspaces: newFakeRegistry()})
	if _, err := worktrees.List(bg); err == nil || !strings.Contains(err.Error(), "ENOTDIR") {
		t.Fatal(err)
	}
}

func TestWorktreeNames(t *testing.T) {
	if got := worktreeDirectoryName("/Users/me/My Project!", "abcd1234"); got != "my-project-abcd1234" {
		t.Fatal(got)
	}
	if got := worktreeDirectoryName("/", "abcd1234"); got != "project-abcd1234" {
		t.Fatal(got)
	}
	if got := worktreeDirectoryName("/a/__", "abcd1234"); got != "project-abcd1234" {
		t.Fatal(got)
	}
	if got := worktreeIDFromDirectoryName("repo-0123abcd"); got != "0123abcd" {
		t.Fatal(got)
	}
	if got := worktreeIDFromDirectoryName("odd name.x"); got != "odd-name-x" {
		t.Fatal(got)
	}
	if got := snapshotMessage("T", nil, ReleaseDeleted, nil); got != "aicodingtool: snapshot \"T\"\n\nNo thread · deleted manually" {
		t.Fatal(got)
	}
}
