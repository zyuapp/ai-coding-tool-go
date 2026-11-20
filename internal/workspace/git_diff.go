package workspace

import (
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	diffTimeout = 10 * time.Second
	// PatchLimit is the size past which a patch is not worth drawing, and the file is offered to an editor instead.
	PatchLimit = 2_000_000
	// How much unchanged code surrounds each hunk. Three is what Git and every review tool default to.
	diffContext = 3
	// How long a resolved base is reused for. Long enough for one review's patches, short enough to move.
	diffBaseTTL = 30 * time.Second
	// Git's own empty tree, which is what a repository with no commits has to be compared against.
	emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
)

// spacing is what a comparison that ignores whitespace passes to Git. `-w` drops a line whose only
// change is spacing, and a file whose only changes were those lines.
func spacing(ignoreWhitespace bool) []string {
	if ignoreWhitespace {
		return []string{"-w"}
	}
	return nil
}

// runDiff runs git with `--literal-pathspecs`, so a filename holding `*`, `?` or `[` is a path and not a glob.
func (g *Git) runDiff(ctx context.Context, root string, args ...string) (string, error) {
	return run(ctx, g.command, "git", append([]string{"--literal-pathspecs"}, args...), runOptions{dir: root, timeout: diffTimeout, maxBuffer: PatchLimit})
}

// runAllowingDifferences reads `git diff`'s exit 1, which `--no-index` makes the normal case, as its answer.
func (g *Git) runAllowingDifferences(ctx context.Context, root string, args ...string) (string, error) {
	out, err := g.runDiff(ctx, root, args...)
	var failure *ProcessError
	if errors.As(err, &failure) && failure.ExitCode != nil && *failure.ExitCode == 1 {
		return failure.Stdout, nil
	}
	return out, err
}

func (g *Git) hasCommits(ctx context.Context, root string) bool {
	_, err := g.runDiff(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// resolveBase is where a comparison starts. Branch comparisons measure from where the two sides last
// agreed, so a base that has moved ahead does not report its own commits as the thread's work.
func (g *Git) resolveBase(ctx context.Context, root string, r DiffRange) string {
	if r.Kind == RangeUncommitted {
		if g.hasCommits(ctx, root) {
			return "HEAD"
		}
		return emptyTree
	}
	head := "HEAD"
	if r.Compare != nil {
		head = *r.Compare
	}
	if r.Base == "HEAD" && head == "HEAD" && !g.hasCommits(ctx, root) {
		return emptyTree
	}
	// A base the checkout does not have is compared against directly, and Git says so if it cannot.
	if out, err := g.runDiff(ctx, root, "merge-base", r.Base, head); err == nil {
		if mergeBase := strings.TrimSpace(out); mergeBase != "" {
			return mergeBase
		}
	}
	return r.Base
}

var commitHash = regexp.MustCompile(`(?i)^[a-f\d]{7,40}$`)

func (g *Git) commitRevisions(ctx context.Context, root, hash string) ([]string, error) {
	if !commitHash.MatchString(hash) {
		return nil, errors.New("Invalid commit hash.")
	}
	out, err := g.runDiff(ctx, root, "rev-parse", "--verify", hash+"^{commit}")
	if err != nil {
		return nil, errors.New("Commit " + hash + " is not available in this repository.")
	}
	commit := strings.TrimSpace(out)
	out, err = g.runDiff(ctx, root, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return nil, err
	}
	parents := strings.Fields(strings.TrimSpace(out))
	// A merge is shown against its first parent, and a root commit against the empty tree.
	if len(parents) > 1 {
		return []string{parents[1], commit}, nil
	}
	return []string{emptyTree, commit}, nil
}

// revisions are what a `git diff` for this range takes, with the working tree left as the absent side.
// The summary and every patch of one review share one resolution, kept briefly.
func (g *Git) revisions(ctx context.Context, root string, r DiffRange) ([]string, error) {
	detached := context.WithoutCancel(ctx)
	if r.Kind == RangeCommit {
		key := root + "\x00" + r.Commit
		g.mu.Lock()
		resolved := g.diffCommits[key]
		if resolved == nil {
			resolved = startFuture(func() ([]string, error) { return g.commitRevisions(detached, root, r.Commit) })
			g.diffCommits[key] = resolved
			g.expire(diffBaseTTL, func() {
				if g.diffCommits[key] == resolved {
					delete(g.diffCommits, key)
				}
			})
			go func() {
				if _, err := resolved.wait(); err != nil {
					g.mu.Lock()
					if g.diffCommits[key] == resolved {
						delete(g.diffCommits, key)
					}
					g.mu.Unlock()
				}
			}()
		}
		g.mu.Unlock()
		return resolved.wait()
	}
	key := root + "\x00" + rangeKey(r)
	g.mu.Lock()
	base := g.diffBases[key]
	if base == nil {
		base = startFuture(func() (string, error) { return g.resolveBase(detached, root, r), nil })
		g.diffBases[key] = base
		// A base is only as current as the refs behind it, so it is not remembered for long.
		g.expire(diffBaseTTL, func() {
			if g.diffBases[key] == base {
				delete(g.diffBases, key)
			}
		})
	}
	g.mu.Unlock()
	resolved, _ := base.wait()
	if r.Kind != RangeUncommitted && r.Compare != nil {
		return []string{resolved, *r.Compare}, nil
	}
	return []string{resolved}, nil
}

func (g *Git) expire(after time.Duration, drop func()) {
	time.AfterFunc(after, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		drop()
	})
}

// untrackedFiles are files Git is not tracking. They have no diff of their own, so they are listed
// here and patched with `--no-index` when one is opened.
func (g *Git) untrackedFiles(ctx context.Context, root string) ([]string, error) {
	out, err := g.runDiff(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	return splitNul(out), err
}

func (g *Git) untrackedSummary(ctx context.Context, root, file string) *DiffFileSummary {
	candidate := absolute(root, file)
	if escapes(root, candidate) {
		return nil
	}
	metadata, err := os.Lstat(candidate)
	if err != nil || (!metadata.Mode().IsRegular() && metadata.Mode()&os.ModeSymlink == 0) {
		return nil
	}
	out, err := g.runAllowingDifferences(ctx, root, "diff", "--numstat", "-z", "--no-index", "--", "/dev/null", file)
	if err != nil {
		return nil
	}
	fields := strings.Split(strings.Split(out, "\x00")[0], "\t")
	var added, deleted string
	if len(fields) > 1 {
		added, deleted = fields[0], fields[1]
	} else {
		added = fields[0]
	}
	binary := added == "-" || deleted == "-"
	summary := &DiffFileSummary{Path: file, Status: FileUntracked, Binary: binary}
	if !binary {
		summary.Additions = jsNumber(added)
	}
	return summary
}

// jsNumber is `Number(text) || 0` for the counts Git writes.
func jsNumber(text string) float64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || value != value {
		return 0
	}
	return value
}

// measureUntracked works through untracked files in batches, since each one is a Git process of its own.
func (g *Git) measureUntracked(ctx context.Context, root string, files []string) []DiffFileSummary {
	measured := []DiffFileSummary{}
	for cursor := 0; cursor < len(files); cursor += untrackedConcurrency {
		batch := files[cursor:min(cursor+untrackedConcurrency, len(files))]
		results := make([]*DiffFileSummary, len(batch))
		var wg sync.WaitGroup
		for index, file := range batch {
			wg.Go(func() { results[index] = g.untrackedSummary(ctx, root, file) })
		}
		wg.Wait()
		for _, result := range results {
			if result != nil {
				measured = append(measured, *result)
			}
		}
	}
	return measured
}

var diffStatuses = map[byte]DiffFileStatus{'A': FileAdded, 'D': FileDeleted, 'R': FileRenamed, 'C': FileAdded, 'M': FileModified, 'T': FileModified}

// ReadNumstat reads `--numstat -z`. A rename writes both of its paths, so records are read with a
// cursor rather than split into fixed groups.
func ReadNumstat(output string, statuses map[string]DiffFileStatus) []DiffFileSummary {
	fields := strings.Split(output, "\x00")
	files := []DiffFileSummary{}
	for index := 0; index < len(fields); index++ {
		record := fields[index]
		if record == "" {
			continue
		}
		parts := strings.Split(record, "\t")
		if len(parts) < 2 {
			continue
		}
		added, deleted := parts[0], parts[1]
		// Only the counts are tab-delimited; whatever follows is one path, tabs and all.
		filePath := strings.Join(parts[2:], "\t")
		previousPath := ""
		if filePath == "" {
			if index+1 < len(fields) {
				previousPath = fields[index+1]
			}
			if index+2 < len(fields) {
				filePath = fields[index+2]
			}
			index += 2
			if filePath == "" {
				continue
			}
		}
		binary := added == "-" || deleted == "-"
		file := DiffFileSummary{Path: filePath, Binary: binary}
		if previousPath != "" {
			file.PreviousPath = &previousPath
		}
		if status, ok := statuses[filePath]; ok {
			file.Status = status
		} else if previousPath != "" {
			file.Status = FileRenamed
		} else {
			file.Status = FileModified
		}
		if !binary {
			file.Additions = jsNumber(added)
			file.Deletions = jsNumber(deleted)
		}
		files = append(files, file)
	}
	return files
}

// readNameStatus says what happened to each file, which `--numstat` does not and `--name-status` does.
func readNameStatus(output string) map[string]DiffFileStatus {
	fields := strings.Split(output, "\x00")
	statuses := map[string]DiffFileStatus{}
	for index := 0; index < len(fields); index++ {
		code := fields[index]
		if code == "" {
			continue
		}
		status, ok := diffStatuses[code[0]]
		if !ok {
			continue
		}
		// A rename writes its old path then its new one; the new one is what the list is keyed by.
		renamed := code[0] == 'R' || code[0] == 'C'
		target := ""
		step := 1
		if renamed {
			step = 2
		}
		if index+step < len(fields) {
			target = fields[index+step]
		}
		index += step
		if target != "" {
			statuses[target] = status
		}
	}
	return statuses
}

// newCollator orders strings as JavaScript's `localeCompare` does under the root locale.
func newCollator() *collate.Collator {
	return collate.New(language.Und)
}

// DiffSummary lists the files a comparison touches, with their counts.
func (g *Git) DiffSummary(ctx context.Context, workspaceID string, r DiffRange, workspaces Resolver, ignoreWhitespace bool) DiffSummaryResult {
	resolved, err := workspaces.Resolve(workspaceID)
	if err != nil {
		var unknown *UnknownWorkspaceError
		if errors.As(err, &unknown) {
			return WorkspaceUnknown{WorkspaceID: workspaceID}
		}
		return Failure{Message: err.Error()}
	}
	if resolved.Status == ResolutionUnavailable {
		return WorkspaceUnavailable{Reason: resolved.Reason}
	}

	root := resolved.Workspace.Root
	revs, err := g.revisions(ctx, root, r)
	if err != nil {
		return Failure{Message: err.Error()}
	}
	space := spacing(ignoreWhitespace)
	diffArgs := func(format string) []string {
		args := append([]string{"diff", format, "-z", "--relative", "--find-renames"}, space...)
		return append(append(args, revs...), "--")
	}
	var numstat, nameStatus string
	var untracked []string
	var numstatErr, nameStatusErr, untrackedErr error
	var wg sync.WaitGroup
	wg.Go(func() { numstat, numstatErr = g.runDiff(ctx, root, diffArgs("--numstat")...) })
	wg.Go(func() { nameStatus, nameStatusErr = g.runDiff(ctx, root, diffArgs("--name-status")...) })
	// Only a comparison that ends at the working tree can have files Git has never seen.
	if r.Kind == RangeUncommitted || (r.Kind == RangeBranches && r.Compare == nil) {
		wg.Go(func() { untracked, untrackedErr = g.untrackedFiles(ctx, root) })
	}
	wg.Wait()
	for _, err := range []error{numstatErr, nameStatusErr, untrackedErr} {
		if err != nil {
			return Failure{Message: err.Error()}
		}
	}
	files := ReadNumstat(numstat, readNameStatus(nameStatus))
	files = append(files, g.measureUntracked(ctx, root, untracked)...)
	collator := newCollator()
	slices.SortStableFunc(files, func(a, b DiffFileSummary) int { return collator.CompareString(a.Path, b.Path) })
	var additions, deletions float64
	for _, file := range files {
		additions += file.Additions
		deletions += file.Deletions
	}
	return DiffSummaryAvailable{Range: r, IgnoreWhitespace: ignoreWhitespace, Files: files, Additions: additions, Deletions: deletions}
}

// DiffPatch is one file's patch, read only once that file is drawn. A rename needs both of its paths.
func (g *Git) DiffPatch(ctx context.Context, workspaceID string, r DiffRange, filePath string, workspaces Resolver, previousPath *string, ignoreWhitespace bool) DiffPatchResult {
	resolved, err := workspaces.Resolve(workspaceID)
	if err != nil {
		return Failure{Message: err.Error()}
	}
	if resolved.Status != ResolutionAvailable {
		reason := string(resolved.Reason)
		if resolved.Status != ResolutionUnavailable {
			reason = "unavailable"
		}
		return Failure{Message: "Workspace is " + reason + "."}
	}

	root := resolved.Workspace.Root
	if escapes(root, absolute(root, filePath)) {
		return Failure{Message: "Path is outside the workspace."}
	}
	patch, err := g.patch(ctx, root, r, filePath, previousPath, ignoreWhitespace)
	if err != nil {
		var failure *ProcessError
		if errors.As(err, &failure) && failure.TooLarge {
			return DiffPatchTooLarge{Limit: PatchLimit}
		}
		return Failure{Message: err.Error()}
	}
	return DiffPatchAvailable{Patch: patch}
}

func (g *Git) patch(ctx context.Context, root string, r DiffRange, filePath string, previousPath *string, ignoreWhitespace bool) (string, error) {
	revs, err := g.revisions(ctx, root, r)
	if err != nil {
		return "", err
	}
	// Both sides of a rename, or Git sees only the new path and calls the whole file an addition.
	paths := []string{filePath}
	if previousPath != nil && *previousPath != "" && *previousPath != filePath {
		paths = []string{*previousPath, filePath}
	}
	space := spacing(ignoreWhitespace)
	unified := "-U" + strconv.Itoa(diffContext)
	args := append([]string{"diff", unified, "--relative", "--find-renames"}, space...)
	args = append(append(append(args, revs...), "--"), paths...)
	patch, err := g.runDiff(ctx, root, args...)
	if err != nil {
		return "", err
	}
	// Nothing tracked answers for a file Git has never seen, so it is diffed against emptiness.
	if strings.TrimSpace(patch) != "" {
		return patch, nil
	}
	if r.Kind == RangeCommit || (r.Kind == RangeBranches && r.Compare != nil) {
		return "", nil
	}
	fresh := append(append([]string{"diff", unified}, space...), "--no-index", "--", "/dev/null", filePath)
	return g.runAllowingDifferences(ctx, root, fresh...)
}
