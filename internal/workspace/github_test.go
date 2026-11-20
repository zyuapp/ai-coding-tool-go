package workspace

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// commandOn finds programs only in bin, so `gh` is missing however the machine is set up.
func commandOn(bin string) Command {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, filepath.Join(bin, name), args...)
		cmd.Env = append(os.Environ(), "PATH="+bin)
		return cmd
	}
}

func binWithoutGh(t *testing.T) string {
	t.Helper()
	bin := tempDir(t)
	found, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(found, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	return bin
}

func pullRequestRepository(t *testing.T, remote string) string {
	t.Helper()
	root := repository(t, "one\n")
	if remote != "" {
		gitT(t, root, "remote", "add", "origin", remote)
	}
	return root
}

func TestCheckoutOnGitHubSaysGhIsMissing(t *testing.T) {
	root := pullRequestRepository(t, "https://github.com/o/r.git")
	answer := NewGit(commandOn(binWithoutGh(t))).PullRequestFor(bg, root)
	if got := encode(t, answer); got != `{"status":"gh-missing"}` {
		t.Fatal(got)
	}
}

func TestCheckoutElsewhereLosesNothingWithoutGh(t *testing.T) {
	for _, remote := range []string{"git@gitlab.com:o/r.git", ""} {
		root := pullRequestRepository(t, remote)
		answer := NewGit(commandOn(binWithoutGh(t))).PullRequestFor(bg, root)
		if got := encode(t, answer); got != `{"status":"none"}` {
			t.Fatal(remote, got)
		}
	}
}

// binWithGh answers the commit lookup with a pull request in state, and the details view with what
// it still needs, writing down each thing it was asked.
func binWithGh(t *testing.T, state string, mergedAt *string) (string, func() []string) {
	t.Helper()
	bin := binWithoutGh(t)
	log := filepath.Join(bin, "asked.log")
	found, _ := json.Marshal([]map[string]any{{"number": 5, "title": "Ship it", "html_url": "https://github.com/o/r/pull/5", "state": state, "draft": false, "merged_at": mergedAt}})
	details := `{"mergeable":"CONFLICTING","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED","conclusion":"FAILURE"}]}`
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nif [ \"$1\" = api ]; then echo '" + string(found) + "'; else echo '" + details + "'; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, func() []string {
		data, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
}

func TestWorktreeOpenPullRequestIsAskedWhatItNeedsAndMergedIsNot(t *testing.T) {
	root := pullRequestRepository(t, "https://github.com/o/r.git")
	gitT(t, root, "checkout", "--detach")
	bin, asked := binWithGh(t, "open", nil)

	answer := NewGit(commandOn(bin)).PullRequestFor(bg, root)
	want := `{"status":"found","pullRequest":{"number":5,"title":"Ship it","url":"https://github.com/o/r/pull/5","state":"open","checks":"failing","review":"approved","conflicts":true}}`
	if got := encode(t, answer); got != want {
		t.Fatal(got)
	}
	commit := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	if got := asked(); !slices.Equal(got, []string{"api repos/{owner}/{repo}/commits/" + commit + "/pulls", "pr view 5 --json mergeable,reviewDecision,statusCheckRollup"}) {
		t.Fatal(got)
	}

	merged := pullRequestRepository(t, "https://github.com/o/r.git")
	gitT(t, merged, "checkout", "--detach")
	doneBin, doneAsked := binWithGh(t, "closed", ptr("2026-10-01T00:00:00Z"))
	answer = NewGit(commandOn(doneBin)).PullRequestFor(bg, merged)
	if found, ok := answer.(PullRequestFound); !ok || found.PullRequest.State != PullRequestMerged {
		t.Fatalf("%#v", answer)
	}
	for _, line := range doneAsked() {
		if strings.HasPrefix(line, "pr view") {
			t.Fatal("a merged pull request needs nothing more")
		}
	}
}

func TestBranchIsAskedByNameAndAnswersAreRemembered(t *testing.T) {
	root := pullRequestRepository(t, "https://github.com/o/r.git")
	bin := binWithoutGh(t)
	log := filepath.Join(bin, "asked.log")
	list := `[{"number":7,"title":"Draft","url":"https://github.com/o/r/pull/7","state":"OPEN","isDraft":true,"mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED","statusCheckRollup":[{"__typename":"StatusContext","state":"PENDING"},{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}]`
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\necho '" + list + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	git := NewGit(commandOn(bin))
	want := `{"status":"found","pullRequest":{"number":7,"title":"Draft","url":"https://github.com/o/r/pull/7","state":"draft","checks":"pending","review":"required"}}`
	for range 2 {
		if got := encode(t, git.PullRequestFor(bg, root)); got != want {
			t.Fatal(got)
		}
	}
	data, _ := os.ReadFile(log)
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 1 || !strings.HasPrefix(lines[0], "pr list --head main --state all --limit 1 --json ") {
		t.Fatal(lines)
	}
}

func TestChecksTakenTogether(t *testing.T) {
	cases := []struct {
		rollup any
		want   PullRequestChecks
	}{
		{nil, ""},
		{[]any{}, ""},
		{[]any{map[string]any{"state": "SUCCESS"}}, ChecksPassing},
		{[]any{map[string]any{"__typename": nil, "status": "QUEUED"}}, ChecksPending},
		{[]any{map[string]any{"status": "COMPLETED", "conclusion": "NEUTRAL"}, map[string]any{"__typename": "StatusContext", "state": "ERROR"}}, ChecksFailing},
		{[]any{map[string]any{"status": "COMPLETED", "conclusion": "STALE"}}, ""},
	}
	for _, c := range cases {
		if got := checksOf(c.rollup); got != c.want {
			t.Fatalf("%v: %q", c.rollup, got)
		}
	}
}
