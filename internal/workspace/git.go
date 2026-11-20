package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	gitTimeout = 30 * time.Second
	// Checkout-scale work — populating or removing a worktree, committing a whole tree — takes as long as the tree is big.
	gitLongTimeout = 600 * time.Second
	// A worktree checkout can carry a large binary patch, so the pipe has to be bigger than the default.
	gitMaxBuffer = 64 * 1024 * 1024
)

// GitError is every failed git invocation: no shell, one error shape, a bounded timeout.
type GitError struct {
	Args     []string
	Message  string
	ExitCode *int
}

func (e *GitError) Error() string { return e.Message }

// Git runs git and gh for the app, and keeps the short-lived answers polls ask for again and again.
type Git struct {
	command Command

	mu              sync.Mutex
	comparisonBases map[string]*cachedComparisonBase
	untrackedCounts map[string]countedFile
	diffBases       map[string]*future[string]
	diffCommits     map[string]*future[[]string]
	prAnswers       map[string]cachedAnswer
	prAsking        map[string]*future[PullRequestAnswer]
}

// NewGit runs every child through command; nil uses DefaultCommand.
func NewGit(command Command) *Git {
	if command == nil {
		command = DefaultCommand
	}
	return &Git{
		command:         command,
		comparisonBases: map[string]*cachedComparisonBase{},
		untrackedCounts: map[string]countedFile{},
		diffBases:       map[string]*future[string]{},
		diffCommits:     map[string]*future[[]string]{},
		prAnswers:       map[string]cachedAnswer{},
		prAsking:        map[string]*future[PullRequestAnswer]{},
	}
}

// Run is `git(cwd, args, input, timeout)`. A nil input leaves stdin closed; zero timeout is 30 seconds.
func (g *Git) Run(ctx context.Context, cwd string, args []string, input *string, timeout time.Duration) (string, error) {
	return g.runGit(ctx, cwd, args, input, timeout, false)
}

func (g *Git) runGit(ctx context.Context, cwd string, args []string, input *string, timeout time.Duration, raw bool) (string, error) {
	if timeout == 0 {
		timeout = gitTimeout
	}
	out, err := run(ctx, g.command, "git", args, runOptions{dir: cwd, input: input, timeout: timeout, maxBuffer: gitMaxBuffer, raw: raw})
	if err == nil {
		return out, nil
	}
	var failure *ProcessError
	if !errors.As(err, &failure) {
		return "", &GitError{Args: args, Message: err.Error()}
	}
	if failure.NotFound {
		return "", &GitError{Args: args, Message: "Git is not installed or is not on the PATH."}
	}
	message := strings.TrimSpace(failure.Stderr)
	if message == "" {
		message = failure.Error()
	}
	if message == "" {
		message = "git " + args[0] + " failed"
	}
	return "", &GitError{Args: args, Message: message, ExitCode: failure.ExitCode}
}

func (g *Git) git(ctx context.Context, cwd string, args ...string) (string, error) {
	return g.Run(ctx, cwd, args, nil, 0)
}

func (g *Git) tryGit(ctx context.Context, cwd string, timeout time.Duration, args ...string) (string, bool) {
	out, err := g.Run(ctx, cwd, args, nil, timeout)
	return out, err == nil
}

// refArgument refuses a caller-supplied name git would read as an option instead of a ref.
func refArgument(name string) (string, error) {
	if strings.HasPrefix(name, "-") {
		return "", &GitError{Args: []string{name}, Message: "Invalid ref name: " + name}
	}
	return name, nil
}

// HeadCommit resolves ref, "HEAD" when empty, to its full hash.
func (g *Git) HeadCommit(ctx context.Context, root, ref string) (string, error) {
	if ref == "" {
		ref = "HEAD"
	}
	name, err := refArgument(ref)
	if err != nil {
		return "", err
	}
	out, err := g.git(ctx, root, "rev-parse", name)
	return strings.TrimSpace(out), err
}

func (g *Git) ShortCommit(ctx context.Context, root, commit string) (string, error) {
	out, err := g.git(ctx, root, "rev-parse", "--short", commit)
	return strings.TrimSpace(out), err
}

// IsDetached is true while the checkout has no branch, which is how a worktree starts and often stays.
func (g *Git) IsDetached(ctx context.Context, root string) bool {
	_, ok := g.tryGit(ctx, root, 0, "symbolic-ref", "--quiet", "HEAD")
	return !ok
}

// CurrentBranch is the branch this checkout holds, or nil while it is detached.
func (g *Git) CurrentBranch(ctx context.Context, root string) *string {
	out, _ := g.tryGit(ctx, root, 0, "symbolic-ref", "--quiet", "--short", "HEAD")
	return nonEmpty(out)
}

// OriginURL is where this checkout pushes, or nil when it has no origin. A worktree reports its repository's.
func (g *Git) OriginURL(ctx context.Context, root string) *string {
	out, _ := g.tryGit(ctx, root, 0, "config", "--get", "remote.origin.url")
	return nonEmpty(out)
}

func nonEmpty(out string) *string {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func (g *Git) IsDirty(ctx context.Context, root string) (bool, error) {
	out, err := g.git(ctx, root, "status", "--porcelain", "-z")
	return len(out) > 0, err
}

func (g *Git) RepositoryRoot(ctx context.Context, root string) (string, error) {
	out, err := g.git(ctx, root, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

func (g *Git) AddWorktree(ctx context.Context, repository, worktree, commit string) error {
	_, err := g.Run(ctx, repository, []string{"worktree", "add", "--detach", worktree, commit}, nil, gitLongTimeout)
	return err
}

func (g *Git) RemoveWorktree(ctx context.Context, repository, worktree string) {
	g.tryGit(ctx, repository, gitLongTimeout, "worktree", "remove", "--force", worktree)
	g.tryGit(ctx, repository, 0, "worktree", "prune")
}

// ListBranches lists branches newest first, with the one the checkout is on. Local ones are what a
// thread can start from or move onto; remote ones are only compared against. A detached head
// reports no current branch.
func (g *Git) ListBranches(ctx context.Context, root string) (Branches, error) {
	var local, remote, current string
	var localErr, remoteErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		local, localErr = g.git(ctx, root, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/heads")
	})
	wg.Go(func() {
		remote, remoteErr = g.git(ctx, root, "for-each-ref", "--sort=-committerdate", "--format=%(refname:short)", "refs/remotes")
	})
	wg.Go(func() { current, _ = g.tryGit(ctx, root, 0, "symbolic-ref", "--quiet", "--short", "HEAD") })
	wg.Wait()
	if localErr != nil {
		return Branches{}, localErr
	}
	if remoteErr != nil {
		return Branches{}, remoteErr
	}
	names := func(output string) []string {
		list := []string{}
		for _, line := range strings.Split(output, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				list = append(list, line)
			}
		}
		return list
	}
	remotes := []string{}
	for _, name := range names(remote) {
		// `origin/HEAD` is a pointer at another of these rather than a branch of its own.
		if !strings.HasSuffix(name, "/HEAD") {
			remotes = append(remotes, name)
		}
	}
	return Branches{Branches: names(local), Remotes: remotes, Current: nonEmpty(current)}, nil
}

// KnownCommits keeps the hashes that name a commit the checkout's repository holds, read in one go.
func (g *Git) KnownCommits(ctx context.Context, root string, hashes []string) ([]string, error) {
	var input strings.Builder
	for _, hash := range hashes {
		input.WriteString(hash + "^{commit}\n")
	}
	text := input.String()
	out, err := g.Run(ctx, root, []string{"cat-file", "--batch-check=%(objecttype)"}, &text, 0)
	if err != nil {
		return nil, err
	}
	answers := strings.Split(out, "\n")
	known := []string{}
	for index, hash := range hashes {
		if index < len(answers) && answers[index] == "commit" {
			known = append(known, hash)
		}
	}
	return known, nil
}

// CreateBranch makes branch at the checkout's HEAD. Git refuses a name the repository already has.
func (g *Git) CreateBranch(ctx context.Context, root, branch string) error {
	name, err := refArgument(branch)
	if err != nil {
		return err
	}
	_, err = g.git(ctx, root, "branch", name)
	return err
}

// CheckoutBranch moves the checkout onto branch. Never forced: Git refuses when the switch would
// overwrite uncommitted work, and that refusal is the answer.
func (g *Git) CheckoutBranch(ctx context.Context, root, branch string) error {
	name, err := refArgument(branch)
	if err != nil {
		return err
	}
	_, err = g.git(ctx, root, "checkout", name)
	return err
}

func (g *Git) UpdateRef(ctx context.Context, root, ref, commit string) error {
	_, err := g.git(ctx, root, "update-ref", ref, commit)
	return err
}

// SnapshotCommit commits everything in the checkout with hooks skipped, so a hook cannot reject the
// snapshot of a worktree being let go. Returns nil when there was nothing to commit.
func (g *Git) SnapshotCommit(ctx context.Context, root, message string) (*string, error) {
	dirty, err := g.IsDirty(ctx, root)
	if err != nil || !dirty {
		return nil, err
	}
	if _, err := g.Run(ctx, root, []string{"add", "-A"}, nil, gitLongTimeout); err != nil {
		return nil, err
	}
	// A machine with no git identity still gets its snapshot; the app signs it instead.
	var args []string
	if ident, _ := g.tryGit(ctx, root, 0, "var", "GIT_AUTHOR_IDENT"); ident == "" {
		args = append(args, "-c", "user.name=AI Coding Tool", "-c", "user.email=aic@localhost")
	}
	args = append(args, "commit", "--no-verify", "-m", message)
	if _, err := g.Run(ctx, root, args, nil, gitLongTimeout); err != nil {
		return nil, err
	}
	head, err := g.HeadCommit(ctx, root, "")
	if err != nil {
		return nil, err
	}
	return &head, nil
}

// UnreachableHead is the HEAD no branch, remote or tag holds, if removing this checkout would orphan it.
func (g *Git) UnreachableHead(ctx context.Context, root string) (*string, error) {
	out, err := g.git(ctx, root, "rev-list", "-n", "1", "HEAD", "--not", "--branches", "--remotes", "--tags")
	if err != nil {
		return nil, err
	}
	return nonEmpty(out), nil
}

func splitNul(output string) []string {
	parts := []string{}
	for _, part := range strings.Split(output, "\x00") {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

// IgnoredPaths lists ignored files present in the checkout. `--directory` collapses `node_modules/` into one entry.
func (g *Git) IgnoredPaths(ctx context.Context, root string) ([]string, error) {
	out, err := g.git(ctx, root, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	return splitNul(out), err
}

func (g *Git) UntrackedPaths(ctx context.Context, root string) ([]string, error) {
	out, err := g.git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	return splitNul(out), err
}

// MatchIgnorePatterns keeps the candidates the patterns in patternFile match, decided by Git's own
// ignore matcher in an empty scratch repository, so `.worktreeinclude` follows `.gitignore` syntax exactly.
func (g *Git) MatchIgnorePatterns(ctx context.Context, patternFile string, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return []string{}, nil
	}
	scratch, err := os.MkdirTemp(os.TempDir(), "aic-match-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	if _, err := g.git(ctx, scratch, "init", "-q", "."); err != nil {
		return nil, err
	}
	input := strings.Join(candidates, "\x00") + "\x00"
	out, err := g.Run(ctx, scratch, []string{"-c", "core.excludesFile=" + patternFile, "check-ignore", "--no-index", "--stdin", "-z"}, &input, 0)
	if err != nil {
		// `check-ignore` exits 1 when no candidate matches, which is an answer rather than a failure.
		var failure *GitError
		if errors.As(err, &failure) && failure.ExitCode != nil && *failure.ExitCode == 1 {
			return []string{}, nil
		}
		return nil, err
	}
	return splitNul(out), nil
}

// TrackedDiff is the uncommitted work in a checkout, as a patch that applies onto the same commit elsewhere.
func (g *Git) TrackedDiff(ctx context.Context, root string) (string, error) {
	return g.runGit(ctx, root, []string{"diff", "--binary", "HEAD", "--"}, nil, gitLongTimeout, true)
}

func (g *Git) ApplyPatch(ctx context.Context, root, patch string) error {
	_, err := g.Run(ctx, root, []string{"apply", "--whitespace=nowarn", "-"}, &patch, gitLongTimeout)
	return err
}

// absolute is Node's `path.resolve` for one or more segments.
func absolute(parts ...string) string {
	joined := ""
	for _, part := range parts {
		if filepath.IsAbs(part) {
			joined = part
		} else if part != "" {
			joined = filepath.Join(joined, part)
		}
	}
	resolved, err := filepath.Abs(joined)
	if err != nil {
		return filepath.Clean(joined)
	}
	return resolved
}
