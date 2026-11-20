package workspace

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const worktreeStatusTimeout = 5 * time.Second

var (
	renameOrCopy  = regexp.MustCompile(`[RC]`)
	refNamespaces = regexp.MustCompile(`^refs/(heads|remotes)/`)
)

// CountChangedFiles counts `status --porcelain=v1 -z` records. A rename occupies two records but is one changed file.
func CountChangedFiles(output string) int {
	records := strings.Split(output, "\x00")
	count := 0
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		count++
		if renameOrCopy.MatchString(record[:min(2, len(record))]) {
			index++
		}
	}
	return count
}

func (g *Git) worktreeComparison(ctx context.Context, root string) *WorktreeComparison {
	var candidates []string
	if remoteHead, err := g.Run(ctx, root, []string{"symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"}, nil, worktreeStatusTimeout); err == nil {
		candidates = append(candidates, strings.TrimSpace(remoteHead))
	}
	candidates = append(candidates, "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master")
	seen := map[string]bool{}
	for _, ref := range candidates {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		commit, err := g.Run(ctx, root, []string{"rev-parse", "--verify", ref + "^{commit}"}, nil, worktreeStatusTimeout)
		if err != nil {
			continue
		}
		output, err := g.Run(ctx, root, []string{"rev-list", "--count", "HEAD", "--not", strings.TrimSpace(commit), "--"}, nil, worktreeStatusTimeout)
		if err != nil || !digitsOnly.MatchString(strings.TrimSpace(output)) {
			return nil
		}
		ahead, _ := strconv.ParseFloat(strings.TrimSpace(output), 64)
		return &WorktreeComparison{Branch: refNamespaces.ReplaceAllString(ref, ""), Ahead: ahead}
	}
	return nil
}

// ReadWorktreeStatus counts the checkout's changed files and how far it is ahead of a locally known
// default branch. Nothing is fetched.
func (g *Git) ReadWorktreeStatus(ctx context.Context, root string) WorktreeStatus {
	var status WorktreeStatus
	var wg sync.WaitGroup
	wg.Go(func() {
		output, err := g.Run(ctx, root, []string{"--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all"}, nil, worktreeStatusTimeout)
		if err == nil {
			count := float64(CountChangedFiles(output))
			status.ChangedFiles = &count
		}
	})
	wg.Go(func() { status.Comparison = g.worktreeComparison(ctx, root) })
	wg.Wait()
	return status
}
