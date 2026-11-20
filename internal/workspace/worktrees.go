package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const WorktreeIncludeFile = ".worktreeinclude"

// WorktreeRegistry is the part of the workspace registry the worktree service needs.
type WorktreeRegistry interface {
	RegisterWorktree(root string) (WorkspaceResolution, error)
	ForgetWorktree(root string) error
}

type WorktreeServiceOptions struct {
	WorktreesRoot string
	// LegacyRoots are roots the app used before, still its own: listed and manually removable, but never created in again.
	LegacyRoots []string
	Workspaces  WorktreeRegistry
}

// WorktreeService makes, lists and removes the checkouts the app owns.
type WorktreeService struct {
	git           *Git
	worktreesRoot string
	// ownedRoots is every root the app owns checkouts in, newest first. Only the first one is created in.
	ownedRoots []string
	workspaces WorktreeRegistry
}

func NewWorktreeService(git *Git, options WorktreeServiceOptions) *WorktreeService {
	owned := []string{}
	for _, root := range append([]string{options.WorktreesRoot}, options.LegacyRoots...) {
		if !slices.Contains(owned, root) {
			owned = append(owned, root)
		}
	}
	return &WorktreeService{git: git, worktreesRoot: options.WorktreesRoot, ownedRoots: owned, workspaces: options.Workspaces}
}

// Create makes a worktree detached at whatever the project checkout has checked out right now (or
// at request.Branch), carrying across the gitignored files `.worktreeinclude` names. No branch is created.
func (s *WorktreeService) Create(ctx context.Context, request CreateWorktreeRequest) (CreatedWorktree, error) {
	var repository, baseCommit string
	var repositoryErr, commitErr error
	var wg sync.WaitGroup
	wg.Go(func() { repository, repositoryErr = s.git.RepositoryRoot(ctx, request.ProjectRoot) })
	wg.Go(func() {
		branch := ""
		if request.Branch != nil {
			branch = *request.Branch
		}
		baseCommit, commitErr = s.git.HeadCommit(ctx, request.ProjectRoot, branch)
	})
	wg.Wait()
	if repositoryErr != nil {
		return CreatedWorktree{}, repositoryErr
	}
	if commitErr != nil {
		return CreatedWorktree{}, commitErr
	}
	id := randomHex(4)
	root := filepath.Join(s.worktreesRoot, worktreeDirectoryName(repository, id))
	if err := os.MkdirAll(s.worktreesRoot, 0o777); err != nil {
		return CreatedWorktree{}, nodeFSError("mkdir", s.worktreesRoot, err)
	}
	if err := s.git.AddWorktree(ctx, repository, root, baseCommit); err != nil {
		return CreatedWorktree{}, err
	}
	if err := s.copyIncludedFiles(ctx, repository, root); err != nil {
		return CreatedWorktree{}, err
	}
	if request.CarryChanges {
		if err := s.carryChanges(ctx, repository, root); err != nil {
			return CreatedWorktree{}, err
		}
	}
	registration, err := s.workspaces.RegisterWorktree(root)
	if err != nil {
		return CreatedWorktree{}, err
	}
	at := float64(time.Now().UnixMilli())
	return CreatedWorktree{ID: id, Root: registration.Workspace.Root, WorkspaceID: registration.Workspace.ID, BaseCommit: baseCommit, CreatedAt: at, LastUsedAt: at}, nil
}

// List reads every directory the app owns without changing it, including roots used by older builds.
func (s *WorktreeService) List(ctx context.Context) ([]ManagedWorktree, error) {
	var roots []string
	for _, base := range s.ownedRoots {
		entries, err := os.ReadDir(base)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, nodeFSError("scandir", base, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				roots = append(roots, filepath.Join(base, entry.Name()))
			}
		}
	}
	worktrees := make([]ManagedWorktree, len(roots))
	var cursor int
	var cursorMu sync.Mutex
	next := func() int {
		cursorMu.Lock()
		defer cursorMu.Unlock()
		index := cursor
		cursor++
		return index
	}
	var wg sync.WaitGroup
	for range min(4, len(roots)) {
		wg.Go(func() {
			for index := next(); index < len(roots); index = next() {
				worktrees[index] = s.read(ctx, roots[index])
			}
		})
	}
	wg.Wait()
	unique := map[string]ManagedWorktree{}
	for _, worktree := range worktrees {
		unique[worktree.Root] = worktree
	}
	listed := make([]ManagedWorktree, 0, len(unique))
	for _, worktree := range unique {
		listed = append(listed, worktree)
	}
	collator := newCollator()
	slices.SortStableFunc(listed, func(left, right ManagedWorktree) int { return collator.CompareString(left.Root, right.Root) })
	return listed, nil
}

func (s *WorktreeService) read(ctx context.Context, root string) ManagedWorktree {
	canonical := canonicalPath(root)
	repository := s.parentRepository(ctx, canonical)
	worktree := ManagedWorktree{ID: worktreeIDFromDirectoryName(filepath.Base(canonical)), Root: canonical, Repository: repository}
	if repository == nil {
		return worktree
	}
	var wg sync.WaitGroup
	wg.Go(func() { worktree.Branch = s.git.CurrentBranch(ctx, canonical) })
	wg.Go(func() { worktree.Status = s.git.ReadWorktreeStatus(ctx, canonical) })
	wg.Wait()
	return worktree
}

// OwnedPath resolves a caller-supplied path only after proving it sits under a root the app owns.
func (s *WorktreeService) OwnedPath(root string) (string, error) {
	if err := s.assertOwned(root); err != nil {
		return "", err
	}
	return canonicalPath(root), nil
}

// Release commits everything left in the worktree so a thread never loses work by walking away from
// it, then gives the directory back. A still-detached worktree has no branch holding its commits, so
// a ref under `refs/aicodingtool` keeps them reachable. The snapshot lands before the directory goes.
func (s *WorktreeService) Release(ctx context.Context, request ReleaseWorktreeRequest) (WorktreeSnapshot, error) {
	if err := s.assertOwned(request.Root); err != nil {
		return WorktreeSnapshot{}, err
	}
	if request.MissingOnly != nil && *request.MissingOnly {
		if _, err := os.Lstat(request.Root); err == nil {
			return WorktreeSnapshot{}, errors.New("This folder exists again. Refresh the worktree list before deleting it.")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return WorktreeSnapshot{}, nodeFSError("lstat", request.Root, err)
		}
		if err := s.workspaces.ForgetWorktree(canonicalPath(request.Root)); err != nil {
			return WorktreeSnapshot{}, err
		}
		return WorktreeSnapshot{}, nil
	}
	snapshot, err := s.snapshot(ctx, request)
	if err != nil {
		return WorktreeSnapshot{}, err
	}
	if err := s.Delete(ctx, request.Root); err != nil {
		return WorktreeSnapshot{}, err
	}
	return snapshot, nil
}

// Delete throws the directory away along with anything uncommitted in it. Branches are never
// touched. Git cannot remove a checkout whose repository is gone, so the directory itself always goes.
func (s *WorktreeService) Delete(ctx context.Context, root string) error {
	if err := s.assertOwned(root); err != nil {
		return err
	}
	canonical := canonicalPath(root)
	if repository := s.parentRepository(ctx, root); repository != nil {
		s.git.RemoveWorktree(ctx, *repository, root)
	}
	if err := os.RemoveAll(root); err != nil {
		return nodeFSError("rm", root, err)
	}
	return s.workspaces.ForgetWorktree(canonical)
}

// snapshot: a directory already gone, or whose repository is, has nothing to commit and nowhere to
// keep it. A detached checkout keeps its ref even with nothing left to commit, for the commits the
// thread made itself.
func (s *WorktreeService) snapshot(ctx context.Context, request ReleaseWorktreeRequest) (WorktreeSnapshot, error) {
	if info, err := os.Stat(request.Root); err != nil || !info.IsDir() {
		return WorktreeSnapshot{}, nil
	}
	if s.parentRepository(ctx, request.Root) == nil {
		return WorktreeSnapshot{}, nil
	}
	var ref *string
	if s.git.IsDetached(ctx, request.Root) {
		name := worktreeRef(request.WorktreeID)
		ref = &name
	}
	commit, err := s.git.SnapshotCommit(ctx, request.Root, snapshotMessage(request.Title, request.TaskID, request.Release, ref))
	if err != nil {
		return WorktreeSnapshot{}, err
	}
	kept := commit
	if kept == nil && ref != nil {
		if kept, err = s.git.UnreachableHead(ctx, request.Root); err != nil {
			return WorktreeSnapshot{}, err
		}
	}
	if kept == nil {
		return WorktreeSnapshot{}, nil
	}
	if ref != nil {
		if err := s.git.UpdateRef(ctx, request.Root, *ref, *kept); err != nil {
			return WorktreeSnapshot{}, err
		}
	}
	snapshot := WorktreeSnapshot{Commit: commit, Ref: ref}
	if commit != nil {
		short, err := s.git.ShortCommit(ctx, request.Root, *commit)
		if err != nil {
			return WorktreeSnapshot{}, err
		}
		snapshot.ShortCommit = &short
	}
	return snapshot, nil
}

// assertOwned refuses any path outside a root the app owns.
func (s *WorktreeService) assertOwned(root string) error {
	var bases []string
	for _, owned := range s.ownedRoots {
		bases = append(bases, canonicalPath(owned))
	}
	for _, owned := range s.ownedRoots {
		bases = append(bases, absolute(owned))
	}
	candidates := []string{canonicalPath(root), absolute(root)}
	for _, base := range bases {
		for _, candidate := range candidates {
			if strings.HasPrefix(candidate, base+string(filepath.Separator)) {
				return nil
			}
		}
	}
	return errors.New("Not an AI Coding Tool worktree: " + root)
}

// canonicalPath is resolved through symlinks while the path exists, so the registry, git and the disk agree on it.
func canonicalPath(target string) string {
	if resolved, err := realpath(target); err == nil {
		return resolved
	}
	return absolute(target)
}

// parentRepository: a worktree's `.git` is a file pointing into the checkout it was linked from.
func (s *WorktreeService) parentRepository(ctx context.Context, root string) *string {
	common, err := s.git.git(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil
	}
	repository := filepath.Dir(absolute(root, strings.TrimSpace(common)))
	return &repository
}

// copyIncludedFiles copies the files `.worktreeinclude` names out of the project checkout. Only paths
// Git already ignores are eligible, so a pattern can never duplicate a tracked file into the worktree.
func (s *WorktreeService) copyIncludedFiles(ctx context.Context, repository, worktree string) error {
	patterns := filepath.Join(repository, WorktreeIncludeFile)
	if _, err := os.Stat(patterns); err != nil {
		return nil
	}
	ignored, err := s.git.IgnoredPaths(ctx, repository)
	if err != nil {
		return err
	}
	included, err := s.git.MatchIgnorePatterns(ctx, patterns, ignored)
	if err != nil {
		return err
	}
	for _, relative := range included {
		from := filepath.Join(repository, relative)
		to := filepath.Join(worktree, relative)
		if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
			return nodeFSError("mkdir", filepath.Dir(to), err)
		}
		// The includes are conveniences, not the thread's work; one that will not copy is not fatal.
		if err := copyPath(from, to, copyOptions{recursive: true, preserveTimestamps: true}); err != nil {
			log.Printf("Could not copy %s into the worktree: %v", relative, err)
		}
	}
	return nil
}

// carryChanges copies uncommitted work across: tracked edits as a patch, untracked files and symlinks verbatim.
func (s *WorktreeService) carryChanges(ctx context.Context, repository, worktree string) error {
	patch, err := s.git.TrackedDiff(ctx, repository)
	if err != nil {
		return err
	}
	if strings.TrimSpace(patch) != "" {
		if err := s.git.ApplyPatch(ctx, worktree, patch); err != nil {
			return err
		}
	}
	untracked, err := s.git.UntrackedPaths(ctx, repository)
	if err != nil {
		return err
	}
	for _, relative := range untracked {
		from := filepath.Join(repository, relative)
		to := filepath.Join(worktree, relative)
		// A path deleted while this runs is gone from the checkout too, so there is nothing to carry.
		info, err := os.Lstat(from)
		if err != nil || (!info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
			return nodeFSError("mkdir", filepath.Dir(to), err)
		}
		if err := copyPath(from, to, copyOptions{preserveTimestamps: true, verbatimSymlinks: true}); err != nil {
			return err
		}
	}
	return nil
}

func randomHex(size int) string {
	b := make([]byte, size)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// The helpers below port domain/worktree.ts.

const snapshotRefNamespace = "refs/aicodingtool"

func worktreeRef(worktreeID string) string {
	return snapshotRefNamespace + "/" + worktreeID
}

var releaseReasons = map[WorktreeRelease]string{
	ReleaseReturnedToLocal: "returned to local",
	ReleaseDeleted:         "deleted manually",
}

// snapshotMessage is the snapshot a worktree is force-committed to before it lets go of its thread,
// prefixed so `git log --grep=aicodingtool` finds them all.
func snapshotMessage(title string, taskID *string, release WorktreeRelease, ref *string) string {
	thread := "No thread"
	if taskID != nil && *taskID != "" {
		thread = "Thread " + *taskID
	}
	lines := []string{`aicodingtool: snapshot "` + title + `"`, "", thread + " · " + releaseReasons[release]}
	if ref != nil && *ref != "" {
		lines = append(lines, "Ref "+*ref)
	}
	return strings.Join(lines, "\n")
}

const slugLimit = 24

var (
	slugInvalid        = regexp.MustCompile(`[^a-z0-9]+`)
	slugEdges          = regexp.MustCompile(`^-|-$`)
	directoryWorktree  = regexp.MustCompile(`-([0-9a-f]{8})$`)
	directoryNameNoise = regexp.MustCompile(`[^A-Za-z0-9-]+`)
)

// worktreeDirectoryName is the project's name, so `git worktree list` stays readable, plus the worktree's id.
func worktreeDirectoryName(projectRoot, worktreeID string) string {
	name := "project"
	for _, part := range strings.Split(projectRoot, "/") {
		if part != "" {
			name = part
		}
	}
	slug := slugEdges.ReplaceAllString(slugInvalid.ReplaceAllString(strings.ToLower(name), "-"), "")
	if len(slug) > slugLimit {
		slug = slug[:slugLimit]
	}
	if slug == "" {
		slug = "project"
	}
	return slug + "-" + worktreeID
}

// worktreeIDFromDirectoryName is the id inside a directory the app made. A name from anywhere else stands in for itself.
func worktreeIDFromDirectoryName(name string) string {
	if match := directoryWorktree.FindStringSubmatch(name); match != nil {
		return match[1]
	}
	return directoryNameNoise.ReplaceAllString(name, "-")
}
