package domain

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// IsPullRequestLink checks the shape of a JSON-decoded value (map[string]any, float64, …).
func IsPullRequestLink(value any) bool {
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	number, ok := featuresNumber(record["number"])
	url, isURL := record["url"].(string)
	if !ok || !featuresInteger(number) || number <= 0 || !isURL || url == "" {
		return false
	}
	oneOf := func(key string, known []string) bool {
		field, present := record[key]
		if !present {
			return true
		}
		text, ok := field.(string)
		return ok && slices.Contains(known, text)
	}
	title, hasTitle := record["title"]
	if _, isText := title.(string); hasTitle && !isText {
		return false
	}
	conflicts, hasConflicts := record["conflicts"]
	return oneOf("state", states) && oneOf("checks", checks) && oneOf("review", reviews) && (!hasConflicts || conflicts == true)
}

// LinkOf is what a thread keeps of the pull request it was found on.
func LinkOf(ref *PullRequestRef) *PullRequestLink {
	state, title := ref.State, ref.Title
	link := &PullRequestLink{Number: ref.Number, URL: ref.URL, State: &state, Title: &title}
	if ref.Checks != nil && *ref.Checks != "" {
		link.Checks = ref.Checks
	}
	if ref.Review != nil && *ref.Review != "" {
		link.Review = ref.Review
	}
	if ref.Conflicts != nil && *ref.Conflicts {
		link.Conflicts = ref.Conflicts
	}
	return link
}

// LinkMatches reports whether a thread's link (nil when it has none) already says everything the pull request found for it does.
func LinkMatches(link *PullRequestLink, pullRequest *PullRequestRef) bool {
	return link != nil && link.Number == pullRequest.Number && link.URL == pullRequest.URL &&
		link.State != nil && *link.State == pullRequest.State &&
		link.Title != nil && *link.Title == pullRequest.Title &&
		sameNeeds(&link.PullRequestNeeds, &pullRequest.PullRequestNeeds)
}

var numberQuery = regexp.MustCompile(`^#?(\d+)$`)

var link = regexp.MustCompile(`(?i)^(?:https?://)?([^/\s]+/[^/\s]+/[^/\s]+)/pull/(\d+)(?:[/?#]\S*)?$`)

// PullRequestPullRequestQuery is what a search names when it names a pull request: `12`, `#12`, or a link to one; nil otherwise.
func PullRequestPullRequestQuery(query string) *PullRequestQuery {
	wanted := featuresJSTrim(query)
	found := link.FindStringSubmatch(wanted)
	digits := ""
	if found != nil {
		digits = found[2]
	} else if numbered := numberQuery.FindStringSubmatch(wanted); numbered != nil {
		digits = numbered[1]
	}
	number, err := strconv.ParseFloat(digits, 64)
	if err != nil || !featuresSafeInteger(number) || number <= 0 {
		return nil
	}
	result := &PullRequestQuery{Number: number}
	if found != nil {
		repository := strings.ToLower(found[1])
		result.Repository = &repository
	}
	return result
}

// LinksPullRequest reports whether a thread's pull request is the one a search names.
func LinksPullRequest(pullRequestLink *PullRequestLink, query *PullRequestQuery) bool {
	if pullRequestLink.Number != query.Number {
		return false
	}
	if query.Repository == nil {
		return true
	}
	found := link.FindStringSubmatch(pullRequestLink.URL)
	return found != nil && strings.ToLower(found[1]) == *query.Repository
}

var states = []string{"draft", "open", "merged", "closed"}
var checks = []string{"pending", "passing", "failing"}
var reviews = []string{"approved", "changes-requested", "required"}

// PullRequestFromList reads JSON-decoded `gh pr list --json …` output; nil when there is none.
func PullRequestFromList(value any) *PullRequestRef {
	found := firstRecord(value)
	if found == nil {
		return nil
	}
	state := lowercase(found["state"])
	if found["isDraft"] == true && state == "open" {
		state = "draft"
	}
	ref := pullRequest(found["number"], found["title"], found["url"], state)
	if ref == nil {
		return nil
	}
	ref.PullRequestNeeds = needsOf(found)
	return ref
}

// PullRequestDetails reads JSON-decoded `gh pr view --json mergeable,reviewDecision,statusCheckRollup` output.
func PullRequestDetails(value any) PullRequestNeeds {
	if record, ok := value.(map[string]any); ok {
		return needsOf(record)
	}
	return PullRequestNeeds{}
}

func needsOf(found map[string]any) PullRequestNeeds {
	var needs PullRequestNeeds
	needs.Checks = ChecksOf(found["statusCheckRollup"])
	decision, _ := found["reviewDecision"].(string)
	if review, ok := reviewDecisions[decision]; ok {
		needs.Review = &review
	}
	if found["mergeable"] == "CONFLICTING" {
		conflicts := true
		needs.Conflicts = &conflicts
	}
	return needs
}

var reviewDecisions = map[string]PullRequestReview{
	"APPROVED":          PullRequestReviewApproved,
	"CHANGES_REQUESTED": PullRequestReviewChangesRequested,
	"REVIEW_REQUIRED":   PullRequestReviewRequired,
}

var failedRuns = []any{"FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE"}
var passedRuns = []any{"SUCCESS", "NEUTRAL", "SKIPPED"}

// checkOf reads one entry of gh's `statusCheckRollup`; false when it says nothing.
func checkOf(entry any) (PullRequestChecks, bool) {
	check, ok := entry.(map[string]any)
	if !ok {
		return "", false
	}
	typename, hasTypename := check["__typename"]
	_, hasStatus := check["status"]
	if typename == "StatusContext" || !hasTypename && !hasStatus {
		switch check["state"] {
		case "FAILURE", "ERROR":
			return PullRequestChecksFailing, true
		case "PENDING", "EXPECTED":
			return PullRequestChecksPending, true
		case "SUCCESS":
			return PullRequestChecksPassing, true
		}
		return "", false
	}
	if check["status"] != "COMPLETED" {
		return PullRequestChecksPending, true
	}
	conclusion, _ := check["conclusion"].(string)
	if slices.Contains(failedRuns, any(conclusion)) {
		return PullRequestChecksFailing, true
	}
	if slices.Contains(passedRuns, any(conclusion)) {
		return PullRequestChecksPassing, true
	}
	return "", false
}

// ChecksOf takes every check together; nil is no answer.
func ChecksOf(rollup any) *PullRequestChecks {
	entries, ok := rollup.([]any)
	if !ok {
		return nil
	}
	seen := map[PullRequestChecks]bool{}
	for _, entry := range entries {
		if check, ok := checkOf(entry); ok {
			seen[check] = true
		}
	}
	for _, check := range []PullRequestChecks{PullRequestChecksFailing, PullRequestChecksPending, PullRequestChecksPassing} {
		if seen[check] {
			return &check
		}
	}
	return nil
}

// PullRequestFromCommit reads JSON-decoded `gh api repos/{owner}/{repo}/commits/{sha}/pulls` output; nil when there is none.
func PullRequestFromCommit(value any) *PullRequestRef {
	found := firstRecord(value)
	if found == nil {
		return nil
	}
	var state string
	switch {
	case featuresTruthy(found["merged_at"]):
		state = "merged"
	case found["draft"] == true:
		state = "draft"
	default:
		state = lowercase(found["state"])
	}
	return pullRequest(found["number"], found["title"], found["html_url"], state)
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

// pullRequest is nil unless every field a row draws is there and is itself.
func pullRequest(number, title, url any, state string) *PullRequestRef {
	value, ok := featuresNumber(number)
	if !ok || !featuresInteger(value) || value <= 0 {
		return nil
	}
	name, isTitle := title.(string)
	address, isURL := url.(string)
	if !isTitle || !isURL || address == "" || !slices.Contains(states, state) {
		return nil
	}
	return &PullRequestRef{Number: value, Title: name, URL: address, State: PullRequestState(state)}
}

// PullRequestPollMs is how often an unsettled pull request is asked about again.
const PullRequestPollMs = 60_000

// NoPullRequest is the answer a checkout that has not been asked about yet stands at.
var NoPullRequest PullRequestAnswer = PullRequestAnswerNone{}

var settled = []PullRequestState{PullRequestStateMerged, PullRequestStateClosed}

// PullRequestSettled is true once nothing local or remote will move the pull request again.
func PullRequestSettled(answer PullRequestAnswer) bool {
	found, ok := featuresFoundPullRequest(answer)
	return ok && slices.Contains(settled, found.PullRequest.State)
}

// SamePullRequest reports whether two answers say the same thing.
func SamePullRequest(left, right PullRequestAnswer) bool {
	leftFound, leftOK := featuresFoundPullRequest(left)
	rightFound, rightOK := featuresFoundPullRequest(right)
	if featuresAnswerStatus(left) != featuresAnswerStatus(right) {
		return false
	}
	if !leftOK || !rightOK {
		return true
	}
	a, b := &leftFound.PullRequest, &rightFound.PullRequest
	return a.Number == b.Number && a.Title == b.Title && a.URL == b.URL && a.State == b.State && sameNeeds(&a.PullRequestNeeds, &b.PullRequestNeeds)
}

func sameNeeds(left, right *PullRequestNeeds) bool {
	return featuresSamePointee(left.Checks, right.Checks) && featuresSamePointee(left.Review, right.Review) && featuresSamePointee(left.Conflicts, right.Conflicts)
}

// featuresFoundPullRequest is the found answer, by value or pointer.
func featuresFoundPullRequest(answer PullRequestAnswer) (*PullRequestAnswerFound, bool) {
	switch answer := answer.(type) {
	case PullRequestAnswerFound:
		return &answer, true
	case *PullRequestAnswerFound:
		return answer, answer != nil
	}
	return nil, false
}

func featuresAnswerStatus(answer PullRequestAnswer) string {
	switch answer.(type) {
	case PullRequestAnswerFound, *PullRequestAnswerFound:
		return "found"
	case PullRequestAnswerNone, *PullRequestAnswerNone:
		return "none"
	case PullRequestAnswerGhMissing, *PullRequestAnswerGhMissing:
		return "gh-missing"
	}
	return ""
}

// featuresTruthy is JavaScript's truthiness for a JSON-decoded value.
func featuresTruthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case float64:
		return value != 0 && !math.IsNaN(value)
	}
	return true
}
