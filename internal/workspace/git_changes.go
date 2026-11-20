package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Long enough for the two-second environment poll to reuse, short enough to follow fetched refs.
	changesBaseTTL = 30 * time.Second
	// File reads are independent, but an untracked directory must not open everything at once.
	untrackedConcurrency = 8
	// Bounds the memory. A count only saves work, so throwing the lot away costs one slower scan.
	untrackedCacheLimit = 4_096
	changesTimeout      = 5 * time.Second
)

var digitsOnly = regexp.MustCompile(`^\d+$`)

// SummarizeNumstat totals `git diff --numstat -z`, skipping binary files and rename path records.
func SummarizeNumstat(output string) (additions, deletions int) {
	for _, record := range strings.Split(output, "\x00") {
		fields := strings.Split(record, "\t")
		if len(fields) < 2 || !digitsOnly.MatchString(fields[0]) || !digitsOnly.MatchString(fields[1]) {
			continue
		}
		added, _ := strconv.Atoi(fields[0])
		deleted, _ := strconv.Atoi(fields[1])
		additions += added
		deletions += deleted
	}
	return additions, deletions
}

// countedFile is what one untracked file counted, against the size and modification time it counted,
// so a file that has not moved is not opened again on the next poll.
type countedFile struct {
	size       int64
	modifiedAt int64
	lines      int
}

func countLines(contents []byte) int {
	if bytes.IndexByte(contents, 0) >= 0 {
		return 0
	}
	count := bytes.Count(contents, []byte{'\n'})
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		count++
	}
	return count
}

// escapes is the `relative.startsWith("..") || path.isAbsolute(relative)` check.
func escapes(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err != nil || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative)
}

func (g *Git) untrackedFileLines(root, file string) int {
	candidate := absolute(root, file)
	if escapes(root, candidate) {
		return 0
	}
	// The file may disappear while Git and the filesystem are being sampled.
	metadata, err := os.Lstat(candidate)
	if err != nil || !metadata.Mode().IsRegular() || metadata.Size() > 5_000_000 {
		return 0
	}
	modifiedAt := metadata.ModTime().UnixNano()
	g.mu.Lock()
	counted, ok := g.untrackedCounts[candidate]
	g.mu.Unlock()
	if ok && counted.size == metadata.Size() && counted.modifiedAt == modifiedAt {
		return counted.lines
	}
	contents, err := os.ReadFile(candidate)
	if err != nil {
		return 0
	}
	lines := countLines(contents)
	g.mu.Lock()
	if len(g.untrackedCounts) >= untrackedCacheLimit {
		clear(g.untrackedCounts)
	}
	g.untrackedCounts[candidate] = countedFile{size: metadata.Size(), modifiedAt: modifiedAt, lines: lines}
	g.mu.Unlock()
	return lines
}

func (g *Git) untrackedLines(root string, files []string) int {
	counts := make([]int, len(files))
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
	for range min(untrackedConcurrency, len(files)) {
		wg.Go(func() {
			for index := next(); index < len(files); index = next() {
				counts[index] = g.untrackedFileLines(root, files[index])
			}
		})
	}
	wg.Wait()
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func (g *Git) readGit(ctx context.Context, root string, args ...string) *string {
	out, err := run(ctx, g.command, "git", args, runOptions{dir: root, timeout: changesTimeout})
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(out)
	return &trimmed
}

// comparisonBase is where the counts are measured from: the commit at which HEAD left the origin
// default branch, so work a thread has already committed keeps counting. Nil when there is no origin
// to compare against. Nothing is fetched, so the baseline is as current as the last fetch.
type comparisonBase struct {
	ref    string
	commit string
}

func (g *Git) resolveComparisonBase(ctx context.Context, root string) *comparisonBase {
	var candidates []string
	if remoteHead := g.readGit(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); remoteHead != nil {
		candidates = append(candidates, strings.TrimPrefix(*remoteHead, "refs/remotes/"))
	}
	candidates = append(candidates, "origin/main", "origin/master")
	seen := map[string]bool{}
	for _, ref := range candidates {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		if commit := g.readGit(ctx, root, "merge-base", ref, "HEAD"); commit != nil && *commit != "" {
			return &comparisonBase{ref: ref, commit: *commit}
		}
	}
	return nil
}

type cachedComparisonBase struct {
	head string
	base *future[*comparisonBase]
}

// comparisonBaseFor shares one repository's in-flight and settled reads while HEAD stays put.
func (g *Git) comparisonBaseFor(ctx context.Context, root, head string, resolving *future[*comparisonBase]) *comparisonBase {
	key := absolute(root)
	g.mu.Lock()
	cached := g.comparisonBases[key]
	if cached != nil && cached.head == head {
		g.mu.Unlock()
		base, _ := cached.base.wait()
		return base
	}
	if resolving == nil {
		resolving = g.startComparisonBase(ctx, root)
	}
	entry := &cachedComparisonBase{head: head, base: resolving}
	g.comparisonBases[key] = entry
	g.mu.Unlock()
	time.AfterFunc(changesBaseTTL, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.comparisonBases[key] == entry {
			delete(g.comparisonBases, key)
		}
	})
	base, _ := entry.base.wait()
	return base
}

func (g *Git) startComparisonBase(ctx context.Context, root string) *future[*comparisonBase] {
	detached := context.WithoutCancel(ctx)
	return startFuture(func() (*comparisonBase, error) { return g.resolveComparisonBase(detached, root), nil })
}

func (g *Git) branchName(ctx context.Context, root string) (*string, error) {
	out, err := run(ctx, g.command, "git", []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, runOptions{dir: root, timeout: changesTimeout})
	if err == nil {
		return nonEmpty(out), nil
	}
	out, err = run(ctx, g.command, "git", []string{"rev-parse", "--short", "HEAD"}, runOptions{dir: root, timeout: changesTimeout})
	if err != nil {
		return nil, err
	}
	if short := strings.TrimSpace(out); short != "" {
		detached := "detached@" + short
		return &detached, nil
	}
	return nil, nil
}

func (g *Git) headState(ctx context.Context, root string) (string, *string, error) {
	var commit string
	var branch *string
	var commitErr, branchErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		commit, commitErr = run(ctx, g.command, "git", []string{"rev-parse", "--verify", "HEAD"}, runOptions{dir: root, timeout: changesTimeout})
	})
	wg.Go(func() { branch, branchErr = g.branchName(ctx, root) })
	wg.Wait()
	if commitErr != nil {
		return "", nil, commitErr
	}
	if branchErr != nil {
		return "", nil, branchErr
	}
	return strings.TrimSpace(commit), branch, nil
}

// ChangedFiles is the environment poll's answer: the checkout's status records and how many lines
// changed, measured from where HEAD left the origin default branch.
func (g *Git) ChangedFiles(ctx context.Context, workspaceID string, workspaces Resolver) ChangedFilesResult {
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
	g.mu.Lock()
	_, cached := g.comparisonBases[absolute(root)]
	g.mu.Unlock()
	var resolving *future[*comparisonBase]
	if !cached {
		resolving = g.startComparisonBase(ctx, root)
	}
	head, branch, err := g.headState(ctx, root)
	if err != nil {
		return Failure{Message: err.Error()}
	}
	base := g.comparisonBaseFor(ctx, root, head, resolving)
	against := "HEAD"
	var baseline *string
	if base != nil {
		against = base.commit
		baseline = &base.ref
	}
	var statusOutput, numstatOutput string
	var statusErr, numstatErr error
	var wg sync.WaitGroup
	wg.Go(func() {
		statusOutput, statusErr = run(ctx, g.command, "git", []string{"--no-optional-locks", "status", "--porcelain=v1", "-z"}, runOptions{dir: root, timeout: changesTimeout})
	})
	wg.Go(func() {
		numstatOutput, numstatErr = run(ctx, g.command, "git", []string{"diff", "--numstat", "-z", against, "--"}, runOptions{dir: root, timeout: changesTimeout})
	})
	wg.Wait()
	if statusErr != nil {
		return Failure{Message: statusErr.Error()}
	}
	if numstatErr != nil {
		return Failure{Message: numstatErr.Error()}
	}
	records := splitNul(statusOutput)
	var untracked []string
	for _, record := range records {
		if strings.HasPrefix(record, "?? ") {
			untracked = append(untracked, record[3:])
		}
	}
	additions, deletions := SummarizeNumstat(numstatOutput)
	additions += g.untrackedLines(root, untracked)
	return ChangedFilesAvailable{Files: records, Branch: branch, Baseline: baseline, Additions: float64(additions), Deletions: float64(deletions)}
}
