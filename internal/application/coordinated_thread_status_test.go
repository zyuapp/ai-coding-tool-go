package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var coordBrief = domain.ThreadBrief{Intent: "fix the flaky login test", DoneWhen: "the suite passes 50 runs", Delivers: domain.DeliveryPullRequest}

// What a thread delivering a pull request is offered next, with its pull request standing as `edit` leaves it.
func offeredSteps(edit func(link *domain.PullRequestLink), status CoordinatedThreadStatus, checkout domain.PullRequestAnswer) []domain.NextStep {
	member := coordTask("worker", coordUnder("lead"), func(thread *domain.Thread) {
		thread.Brief = new(coordBrief)
		if edit != nil {
			link := &domain.PullRequestLink{Number: 212, URL: "https://github.com/o/r/pull/212", Title: new("Fix it"), State: new(domain.PullRequestStateOpen)}
			edit(link)
			thread.PullRequest = link
		}
	})
	return NextSteps(member, status, checkout)
}

func TestNextStepsOfferWhatAThreadsOwnPullRequestNeeds(t *testing.T) {
	steps := func(names ...domain.NextStep) []domain.NextStep { return append([]domain.NextStep{}, names...) }
	cases := []struct {
		name     string
		edit     func(*domain.PullRequestLink)
		status   CoordinatedThreadStatus
		checkout domain.PullRequestAnswer
		want     []domain.NextStep
	}{
		{"most pressing first", func(link *domain.PullRequestLink) {
			link.Conflicts, link.Checks, link.Review = new(true), new(domain.PullRequestChecksFailing), new(domain.PullRequestReviewChangesRequested)
		}, CoordinatedThreadStatusReview, nil, steps(domain.NextStepResolveConflicts, domain.NextStepFixCi, domain.NextStepAddressComments)},
		{"failing checks", func(link *domain.PullRequestLink) { link.Checks = new(domain.PullRequestChecksFailing) }, CoordinatedThreadStatusReview, nil, steps(domain.NextStepFixCi)},
		{"changes requested", func(link *domain.PullRequestLink) {
			link.Review, link.Checks = new(domain.PullRequestReviewChangesRequested), new(domain.PullRequestChecksPassing)
		}, CoordinatedThreadStatusReview, nil, steps(domain.NextStepAddressComments)},
		{"approved and passing can merge", func(link *domain.PullRequestLink) {
			link.Review, link.Checks = new(domain.PullRequestReviewApproved), new(domain.PullRequestChecksPassing)
		}, CoordinatedThreadStatusReview, nil, steps(domain.NextStepMerge)},
		{"no review asked can merge", func(*domain.PullRequestLink) {}, CoordinatedThreadStatusReview, nil, steps(domain.NextStepMerge)},
		{"waiting on a review cannot", func(link *domain.PullRequestLink) {
			link.Review, link.Checks = new(domain.PullRequestReviewRequired), new(domain.PullRequestChecksPassing)
		}, CoordinatedThreadStatusReview, nil, steps()},
		{"checks still going", func(link *domain.PullRequestLink) { link.Checks = new(domain.PullRequestChecksPending) }, CoordinatedThreadStatusReview, nil, steps()},
		{"conflicts", func(link *domain.PullRequestLink) { link.Conflicts = new(true) }, CoordinatedThreadStatusReview, nil, steps(domain.NextStepResolveConflicts)},
		{"a draft is fixed but never merged", func(link *domain.PullRequestLink) {
			link.State, link.Checks = new(domain.PullRequestStateDraft), new(domain.PullRequestChecksFailing)
		}, CoordinatedThreadStatusDone, nil, steps(domain.NextStepFixCi)},
		{"a clean draft", func(link *domain.PullRequestLink) { link.State = new(domain.PullRequestStateDraft) }, CoordinatedThreadStatusDone, nil, steps()},
		{"merged needs nothing more", func(link *domain.PullRequestLink) { link.State = new(domain.PullRequestStateMerged) }, CoordinatedThreadStatusDone, nil, steps()},
		{"a stuck thread can still be told", func(link *domain.PullRequestLink) { link.Checks = new(domain.PullRequestChecksFailing) }, CoordinatedThreadStatusBlocked, nil, steps(domain.NextStepFixCi)},
		{"no pull request, GitHub not asked", nil, CoordinatedThreadStatusDone, nil, steps()},
		{"GitHub said it has none", nil, CoordinatedThreadStatusDone, domain.PullRequestAnswerNone{}, steps(domain.NextStepCreatePullRequest)},
		{"nothing can be asked without gh", nil, CoordinatedThreadStatusDone, domain.PullRequestAnswerGhMissing{}, steps()},
		{"working", nil, CoordinatedThreadStatusWorking, domain.PullRequestAnswerNone{}, steps()},
	}
	for _, status := range []CoordinatedThreadStatus{CoordinatedThreadStatusWorking, CoordinatedThreadStatusApproval, CoordinatedThreadStatusAsking, CoordinatedThreadStatusResolved} {
		cases = append(cases, struct {
			name     string
			edit     func(*domain.PullRequestLink)
			status   CoordinatedThreadStatus
			checkout domain.PullRequestAnswer
			want     []domain.NextStep
		}{"nothing while " + string(status), func(link *domain.PullRequestLink) { link.Checks = new(domain.PullRequestChecksFailing) }, status, nil, steps()})
	}
	for _, item := range cases {
		if got := offeredSteps(item.edit, item.status, item.checkout); !slices.Equal(got, item.want) {
			t.Errorf("%s: got %v, want %v", item.name, got, item.want)
		}
	}
	reporting := coordTask("worker", coordUnder("lead"), func(thread *domain.Thread) {
		thread.Brief = &domain.ThreadBrief{Intent: coordBrief.Intent, DoneWhen: coordBrief.DoneWhen, Delivers: domain.DeliveryReport}
	})
	if got := NextSteps(reporting, CoordinatedThreadStatusDone, domain.PullRequestAnswerNone{}); len(got) != 0 {
		t.Errorf("a thread that delivers a report is never asked for a pull request: %v", got)
	}
}
