package domain

import (
	"maps"
	"testing"
)

func pullRequestListed(t *testing.T, fields string) any {
	t.Helper()
	listed := featuresDecode(t, `{"number":12,"title":"Name the two families","url":"https://github.com/o/r/pull/12","state":"OPEN"}`).(map[string]any)
	maps.Copy(listed, featuresDecode(t, fields).(map[string]any))
	return []any{listed}
}

func pullRequestFromAPI(t *testing.T, fields string) any {
	t.Helper()
	found := featuresDecode(t, `{"number":12,"title":"Name the two families","html_url":"https://github.com/o/r/pull/12","state":"open","draft":false,"merged_at":null}`).(map[string]any)
	for key, value := range featuresDecode(t, fields).(map[string]any) {
		if value == "<undefined>" {
			delete(found, key)
		} else {
			found[key] = value
		}
	}
	return []any{found}
}

func TestPullRequestFromListFirstFound(t *testing.T) {
	featuresExpectJSON(t, PullRequestFromList(pullRequestListed(t, `{}`)), `{"number":12,"title":"Name the two families","url":"https://github.com/o/r/pull/12","state":"open"}`)
}

func TestPullRequestDraftOnlyWhileOpen(t *testing.T) {
	if PullRequestFromList(pullRequestListed(t, `{"isDraft":true}`)).State != "draft" {
		t.Fatal("draft")
	}
	if PullRequestFromList(pullRequestListed(t, `{"isDraft":true,"state":"MERGED"}`)).State != "merged" {
		t.Fatal("merged")
	}
}

func TestPullRequestFromCommitReadsMergedOffDate(t *testing.T) {
	if PullRequestFromCommit(pullRequestFromAPI(t, `{"state":"closed","merged_at":"2026-08-20T00:00:00Z"}`)).State != "merged" ||
		PullRequestFromCommit(pullRequestFromAPI(t, `{"state":"closed"}`)).State != "closed" ||
		PullRequestFromCommit(pullRequestFromAPI(t, `{"draft":true}`)).State != "draft" {
		t.Fatal("REST reads merged off the date rather than the state")
	}
}

func TestPullRequestNothingToShow(t *testing.T) {
	for _, value := range []any{nil, []any{}, map[string]any{}, "", []any{nil}, []any{map[string]any{}}} {
		if PullRequestFromList(value) != nil || PullRequestFromCommit(value) != nil {
			t.Fatalf("%v is no pull request", value)
		}
	}
}

func TestPullRequestMissingFieldIsNone(t *testing.T) {
	if PullRequestFromList(pullRequestListed(t, `{"number":0}`)) != nil ||
		PullRequestFromList(pullRequestListed(t, `{"url":""}`)) != nil ||
		PullRequestFromList(pullRequestListed(t, `{"state":"QUEUED"}`)) != nil ||
		PullRequestFromCommit(pullRequestFromAPI(t, `{"html_url":"<undefined>"}`)) != nil {
		t.Fatal("a record missing a field a row draws is not a pull request")
	}
}

func TestPullRequestQueryByNumberOrLink(t *testing.T) {
	featuresExpectJSON(t, PullRequestPullRequestQuery("12"), `{"number":12,"repository":null}`)
	featuresExpectJSON(t, PullRequestPullRequestQuery(" #12 "), `{"number":12,"repository":null}`)
	featuresExpectJSON(t, PullRequestPullRequestQuery("https://github.com/Acme/App/pull/12#discussion_r1"), `{"number":12,"repository":"github.com/acme/app"}`)
	featuresExpectJSON(t, PullRequestPullRequestQuery("github.com/acme/app/pull/12/files"), `{"number":12,"repository":"github.com/acme/app"}`)
	for _, query := range []string{"", "#", "0", "#12a", "panel 12", "https://github.com/acme/app/issues/12", "99999999999999999999"} {
		if PullRequestPullRequestQuery(query) != nil {
			t.Fatalf("%q names no pull request", query)
		}
	}
}

func TestLinksPullRequestOwnRepositoryOnly(t *testing.T) {
	link := &PullRequestLink{Number: 12, URL: "https://github.com/acme/app/pull/12"}
	if !LinksPullRequest(link, PullRequestPullRequestQuery("12")) ||
		LinksPullRequest(link, PullRequestPullRequestQuery("13")) ||
		LinksPullRequest(link, PullRequestPullRequestQuery("https://github.com/acme/other/pull/12")) {
		t.Fatal("a link names only the pull request in its own repository")
	}
}

func TestPullRequestNeeds(t *testing.T) {
	featuresExpectJSON(t, PullRequestFromList(pullRequestListed(t, `{"mergeable":"CONFLICTING","reviewDecision":"CHANGES_REQUESTED","statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"FAILURE"}]}`)),
		`{"number":12,"title":"Name the two families","url":"https://github.com/o/r/pull/12","state":"open","checks":"failing","review":"changes-requested","conflicts":true}`)
	featuresExpectJSON(t, PullRequestDetails(featuresDecode(t, `{"mergeable":"MERGEABLE","reviewDecision":"APPROVED","statusCheckRollup":[{"__typename":"CheckRun","name":"ci","status":"COMPLETED","conclusion":"SUCCESS"}]}`)), `{"checks":"passing","review":"approved"}`)
	featuresExpectJSON(t, PullRequestDetails(featuresDecode(t, `{"mergeable":"UNKNOWN","reviewDecision":"REVIEW_REQUIRED","statusCheckRollup":[]}`)), `{"review":"required"}`)
	featuresExpectJSON(t, PullRequestDetails(featuresDecode(t, `{"reviewDecision":"","statusCheckRollup":null}`)), `{}`)
	featuresExpectJSON(t, PullRequestDetails(nil), `{}`)
}

func pullRequestRun(status string, conclusion any) any {
	return map[string]any{"__typename": "CheckRun", "name": "ci", "status": status, "conclusion": conclusion}
}

func pullRequestContext(state string) any {
	return map[string]any{"__typename": "StatusContext", "context": "deploy", "state": state}
}

func pullRequestChecks(rollup any) string {
	if checks := ChecksOf(rollup); checks != nil {
		return string(*checks)
	}
	return "undefined"
}

func TestChecksOfTakenTogether(t *testing.T) {
	cases := []struct {
		rollup any
		want   string
	}{
		{[]any{pullRequestRun("COMPLETED", "SUCCESS"), pullRequestContext("SUCCESS"), pullRequestRun("COMPLETED", "SKIPPED"), pullRequestRun("COMPLETED", "NEUTRAL")}, "passing"},
		{[]any{pullRequestRun("COMPLETED", "SUCCESS"), pullRequestRun("IN_PROGRESS", nil), pullRequestContext("SUCCESS")}, "pending"},
		{[]any{pullRequestRun("QUEUED", nil), pullRequestContext("PENDING")}, "pending"},
		{[]any{pullRequestContext("EXPECTED"), pullRequestRun("COMPLETED", "SUCCESS")}, "pending"},
		{[]any{pullRequestRun("COMPLETED", "SUCCESS"), pullRequestContext("ERROR")}, "failing"},
		{[]any{pullRequestContext("FAILURE")}, "failing"},
		{[]any{pullRequestRun("COMPLETED", map[string]any{})}, "undefined"},
		{[]any{}, "undefined"},
		{nil, "undefined"},
	}
	for _, conclusion := range []string{"FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE"} {
		cases = append(cases, struct {
			rollup any
			want   string
		}{[]any{pullRequestRun("IN_PROGRESS", nil), pullRequestRun("COMPLETED", conclusion), pullRequestContext("SUCCESS")}, "failing"})
	}
	for index, check := range cases {
		if got := pullRequestChecks(check.rollup); got != check.want {
			t.Fatalf("case %d = %s, want %s", index, got, check.want)
		}
	}
}

func TestSamePullRequestNoticesNeeds(t *testing.T) {
	pending, passing, approved, conflicts := PullRequestChecksPending, PullRequestChecksPassing, PullRequestReviewApproved, true
	ref := PullRequestRef{Number: 12, Title: "T", URL: "https://github.com/o/r/pull/12", State: "open", PullRequestNeeds: PullRequestNeeds{Checks: &pending}}
	with := func(change func(*PullRequestRef)) PullRequestAnswer {
		next := ref
		change(&next)
		return PullRequestAnswerFound{PullRequest: next}
	}
	found := PullRequestAnswerFound{PullRequest: ref}
	if !SamePullRequest(found, with(func(*PullRequestRef) {})) ||
		SamePullRequest(found, with(func(r *PullRequestRef) { r.Checks = &passing })) ||
		SamePullRequest(found, with(func(r *PullRequestRef) { r.Review = &approved })) ||
		SamePullRequest(found, with(func(r *PullRequestRef) { r.Conflicts = &conflicts })) {
		t.Fatal("a change in what a pull request needs is a change in the answer")
	}
	if !SamePullRequest(PullRequestAnswerNone{}, NoPullRequest) || SamePullRequest(PullRequestAnswerNone{}, PullRequestAnswerGhMissing{}) || SamePullRequest(found, NoPullRequest) {
		t.Fatal("statuses")
	}
	merged := ref
	merged.State = PullRequestStateMerged
	if PullRequestSettled(found) || !PullRequestSettled(PullRequestAnswerFound{PullRequest: merged}) || PullRequestSettled(NoPullRequest) {
		t.Fatal("settled")
	}
}

func TestLinkOfKeepsHowPullRequestStands(t *testing.T) {
	failing, conflicts := PullRequestChecksFailing, true
	link := LinkOf(&PullRequestRef{Number: 12, Title: "T", URL: "https://github.com/o/r/pull/12", State: "open", PullRequestNeeds: PullRequestNeeds{Checks: &failing, Conflicts: &conflicts}})
	featuresExpectJSON(t, link, `{"number":12,"url":"https://github.com/o/r/pull/12","state":"open","title":"T","checks":"failing","conflicts":true}`)
	decoded := func() map[string]any { return featuresDecode(t, featuresJSON(t, link)).(map[string]any) }
	if !IsPullRequestLink(decoded()) || !IsPullRequestLink(featuresDecode(t, `{"number":12,"url":"https://github.com/o/r/pull/12"}`)) {
		t.Fatal("a link and an old one read")
	}
	for key, value := range map[string]any{"checks": "flaky", "state": "queued", "conflicts": false} {
		changed := decoded()
		changed[key] = value
		if IsPullRequestLink(changed) {
			t.Fatalf("%s %v is not a link", key, value)
		}
	}
	if !LinkMatches(link, &PullRequestRef{Number: 12, Title: "T", URL: "https://github.com/o/r/pull/12", State: "open", PullRequestNeeds: PullRequestNeeds{Checks: &failing, Conflicts: &conflicts}}) ||
		LinkMatches(nil, &PullRequestRef{}) || LinkMatches(&PullRequestLink{Number: 12, URL: "https://github.com/o/r/pull/12"}, &PullRequestRef{Number: 12, URL: "https://github.com/o/r/pull/12", State: "open"}) {
		t.Fatal("linkMatches")
	}
}
