package workspace

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// Room for one round trip to GitHub. Nothing waits on this, so a slower network simply has no answer.
	githubTimeout = 5 * time.Second
	// How long one answer stands in for the next asker. Threads sharing a checkout ask about the same
	// branch, and a panel remounts whenever it is reopened. Well under the poll interval, so a
	// deliberate refresh still reaches GitHub.
	pullRequestCacheTTL = 10 * time.Second
)

// commandOutput is what a command said, and whether it said nothing because it is not installed at all.
type commandOutput struct {
	text    *string
	missing bool
}

func (g *Git) runTool(ctx context.Context, program string, args []string, cwd string) commandOutput {
	out, err := run(ctx, g.command, program, args, runOptions{dir: cwd, timeout: githubTimeout})
	if err != nil {
		var failure *ProcessError
		return commandOutput{missing: errors.As(err, &failure) && failure.NotFound}
	}
	return commandOutput{text: nonEmpty(out)}
}

type jsonOutput struct {
	value   any
	missing bool
}

func (g *Git) readJSON(ctx context.Context, args []string, cwd string) jsonOutput {
	output := g.runTool(ctx, "gh", args, cwd)
	if output.text == nil {
		return jsonOutput{missing: output.missing}
	}
	var value any
	if err := json.Unmarshal([]byte(*output.text), &value, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)); err != nil {
		return jsonOutput{}
	}
	return jsonOutput{value: value}
}

// onGitHub: only a checkout that has somewhere on GitHub to look loses anything when `gh` is not installed.
func (g *Git) onGitHub(ctx context.Context, root string) bool {
	remotes := g.runTool(ctx, "git", []string{"remote", "-v"}, root).text
	return remotes != nil && strings.Contains(*remotes, "github.com")
}

func (g *Git) answerFrom(ctx context.Context, root string, found jsonOutput, parse func(any) *PullRequestRef) PullRequestAnswer {
	if found.missing {
		if g.onGitHub(ctx, root) {
			return PullRequestGhMissing{}
		}
		return PullRequestNone{}
	}
	if pullRequest := parse(found.value); pullRequest != nil {
		return PullRequestFound{PullRequest: *pullRequest}
	}
	return PullRequestNone{}
}

type cachedAnswer struct {
	at     time.Time
	answer PullRequestAnswer
}

// remembered is keyed by what was asked, never by the checkout alone, so moving to another branch
// never reads the old answer. Askers of the same key share one ask in flight.
func (g *Git) remembered(key string, ask func() PullRequestAnswer) PullRequestAnswer {
	g.mu.Lock()
	now := time.Now()
	for known, cached := range g.prAnswers {
		if now.Sub(cached.at) >= pullRequestCacheTTL {
			delete(g.prAnswers, known)
		}
	}
	if cached, ok := g.prAnswers[key]; ok {
		g.mu.Unlock()
		return cached.answer
	}
	pending := g.prAsking[key]
	if pending == nil {
		pending = startFuture(func() (PullRequestAnswer, error) {
			answer := ask()
			g.mu.Lock()
			delete(g.prAsking, key)
			g.mu.Unlock()
			return answer, nil
		})
		g.prAsking[key] = pending
	}
	g.mu.Unlock()
	answer, _ := pending.wait()
	g.mu.Lock()
	g.prAnswers[key] = cachedAnswer{at: time.Now(), answer: answer}
	g.mu.Unlock()
	return answer
}

// PullRequestFor is the pull request the checkout's work belongs to, and what it still needs before
// it can merge. A branch is asked about by name, so unpushed commits still find it; a detached
// worktree is asked about by commit, so only a pushed one is recognised, and a still-moving pull
// request found that way is asked about once more by number for its checks, review and conflicts.
//
// None covers every way there might not be an answer. A `gh` that is not installed is the one
// exception: a checkout on GitHub is told that rather than told it has no pull request.
func (g *Git) PullRequestFor(ctx context.Context, root string) PullRequestAnswer {
	ctx = context.WithoutCancel(ctx)
	if branch := g.runTool(ctx, "git", []string{"symbolic-ref", "--quiet", "--short", "HEAD"}, root).text; branch != nil {
		const fields = "number,title,url,state,isDraft,mergeable,reviewDecision,statusCheckRollup"
		return g.remembered(root+"\x00"+*branch, func() PullRequestAnswer {
			found := g.readJSON(ctx, []string{"pr", "list", "--head", *branch, "--state", "all", "--limit", "1", "--json", fields}, root)
			return g.answerFrom(ctx, root, found, pullRequestFromList)
		})
	}
	commit := g.runTool(ctx, "git", []string{"rev-parse", "HEAD"}, root).text
	if commit == nil {
		return PullRequestNone{}
	}
	return g.remembered(root+"\x00"+*commit, func() PullRequestAnswer {
		answer := g.answerFrom(ctx, root, g.readJSON(ctx, []string{"api", "repos/{owner}/{repo}/commits/" + *commit + "/pulls"}, root), pullRequestFromCommit)
		found, ok := answer.(PullRequestFound)
		// A merged or closed pull request needs nothing more, so nothing more is asked of it.
		if !ok || (found.PullRequest.State != PullRequestOpen && found.PullRequest.State != PullRequestDraft) {
			return answer
		}
		number := strconv.FormatFloat(found.PullRequest.Number, 'f', -1, 64)
		details := g.readJSON(ctx, []string{"pr", "view", number, "--json", "mergeable,reviewDecision,statusCheckRollup"}, root)
		found.PullRequest.PullRequestNeeds = pullRequestDetails(details.value)
		return found
	})
}

// The parsers below port domain/pull-request.ts.

var (
	pullRequestStates = []string{"draft", "open", "merged", "closed"}
	reviewDecisions   = map[string]PullRequestReview{"APPROVED": ReviewApproved, "CHANGES_REQUESTED": ReviewChangesRequested, "REVIEW_REQUIRED": ReviewRequired}
	failedRuns        = []string{"FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE"}
	passedRuns        = []string{"SUCCESS", "NEUTRAL", "SKIPPED"}
)

// pullRequestFromList reads `gh pr list --json …`, an array of at most one that keeps draft beside the state.
func pullRequestFromList(value any) *PullRequestRef {
	found := firstRecord(value)
	if found == nil {
		return nil
	}
	state := lowercase(found["state"])
	if found["isDraft"] == true && state == "open" {
		state = "draft"
	}
	ref := pullRequestOf(found["number"], found["title"], found["url"], state)
	if ref != nil {
		ref.PullRequestNeeds = needsOf(found)
	}
	return ref
}

// pullRequestDetails reads `gh pr view --json mergeable,reviewDecision,statusCheckRollup`.
func pullRequestDetails(value any) PullRequestNeeds {
	if record, ok := value.(map[string]any); ok {
		return needsOf(record)
	}
	return PullRequestNeeds{}
}

func needsOf(found map[string]any) PullRequestNeeds {
	decision, _ := found["reviewDecision"].(string)
	return PullRequestNeeds{
		Checks:    checksOf(found["statusCheckRollup"]),
		Review:    reviewDecisions[decision],
		Conflicts: found["mergeable"] == "CONFLICTING",
	}
}

// checkOf reads one entry of `statusCheckRollup`: a check run, or a commit status saying the same in other words.
func checkOf(entry any) PullRequestChecks {
	check, ok := entry.(map[string]any)
	if !ok {
		if _, isArray := entry.([]any); !isArray {
			return ""
		}
		check = map[string]any{}
	}
	typename, hasTypename := check["__typename"]
	status, hasStatus := check["status"]
	if typename == "StatusContext" || (!hasTypename && !hasStatus) {
		switch check["state"] {
		case "FAILURE", "ERROR":
			return ChecksFailing
		case "PENDING", "EXPECTED":
			return ChecksPending
		case "SUCCESS":
			return ChecksPassing
		}
		return ""
	}
	if status != "COMPLETED" {
		return ChecksPending
	}
	conclusion, _ := check["conclusion"].(string)
	if slices.Contains(failedRuns, conclusion) {
		return ChecksFailing
	}
	if slices.Contains(passedRuns, conclusion) {
		return ChecksPassing
	}
	return ""
}

// checksOf takes every check together: one failing fails them all, else one still going keeps them pending.
func checksOf(rollup any) PullRequestChecks {
	entries, ok := rollup.([]any)
	if !ok {
		return ""
	}
	seen := map[PullRequestChecks]bool{}
	for _, entry := range entries {
		seen[checkOf(entry)] = true
	}
	switch {
	case seen[ChecksFailing]:
		return ChecksFailing
	case seen[ChecksPending]:
		return ChecksPending
	case seen[ChecksPassing]:
		return ChecksPassing
	}
	return ""
}

// pullRequestFromCommit reads `gh api repos/{owner}/{repo}/commits/{sha}/pulls`, which is REST: it
// knows only open and closed, and leaves merged to be read off the date it happened.
func pullRequestFromCommit(value any) *PullRequestRef {
	found := firstRecord(value)
	if found == nil {
		return nil
	}
	state := lowercase(found["state"])
	if truthy(found["merged_at"]) {
		state = "merged"
	} else if found["draft"] == true {
		state = "draft"
	}
	return pullRequestOf(found["number"], found["title"], found["html_url"], state)
}

func firstRecord(value any) map[string]any {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	record, _ := list[0].(map[string]any)
	return record
}

func lowercase(value any) string {
	text, _ := value.(string)
	return strings.ToLower(text)
}

func truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0 && !math.IsNaN(v)
	}
	return true
}

// pullRequestOf: every field a row draws has to be there and be itself, or there is no pull request to show.
func pullRequestOf(number, title, url any, state string) *PullRequestRef {
	n, ok := number.(float64)
	if !ok || n != math.Trunc(n) || math.IsInf(n, 0) || n <= 0 {
		return nil
	}
	titleText, titleOK := title.(string)
	urlText, urlOK := url.(string)
	if !titleOK || !urlOK || urlText == "" || !slices.Contains(pullRequestStates, state) {
		return nil
	}
	return &PullRequestRef{Number: n, Title: titleText, URL: urlText, State: PullRequestState(state)}
}
