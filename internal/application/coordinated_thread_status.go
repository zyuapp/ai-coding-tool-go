package application

import (
	"slices"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The pull request that is a thread's own work, if it has one. A thread whose brief delivers a pull
// request owns the one its checkout is on, and so does a thread with no brief working in a worktree.
// A link read before its state was kept says too little to count.
func CoordinatedThreadStatusOwnPullRequest(member *domain.Thread) *OwnPullRequest {
	link := member.PullRequest
	if link == nil || link.State == nil || *link.State == "" || link.Title == nil {
		return nil
	}
	if !OwnsPullRequest(member) {
		return nil
	}
	return &OwnPullRequest{
		Checks: link.Checks, Review: link.Review, Conflicts: link.Conflicts,
		Number: link.Number, URL: link.URL, State: *link.State, Title: *link.Title,
	}
}

// Whether the pull request a thread's checkout is on would be its own work.
func OwnsPullRequest(member *domain.Thread) bool {
	if member.Brief != nil {
		return member.Brief.Delivers == domain.DeliveryPullRequest
	}
	return member.WorktreeID != nil
}

var checksLine = map[domain.PullRequestChecks]string{
	domain.PullRequestChecksFailing: "Checks failing",
	domain.PullRequestChecksPassing: "Checks passing",
	domain.PullRequestChecksPending: "Checks pending",
}

var stateLine = map[domain.PullRequestState]string{
	domain.PullRequestStateDraft:  "Draft",
	domain.PullRequestStateMerged: "Merged",
	domain.PullRequestStateClosed: "Closed",
}

// A thread's own pull request in a few words: how it stands unless it is simply open, then, while it
// can still move, its conflicts, where its checks stand, and any changes its review asks for.
func PullRequestLine(pullRequest *OwnPullRequest) string {
	parts := []string{"#" + strconv.FormatFloat(pullRequest.Number, 'f', -1, 64)}
	state := pullRequest.State
	if state != domain.PullRequestStateOpen {
		parts = append(parts, stateLine[state])
	}
	if state == domain.PullRequestStateOpen || state == domain.PullRequestStateDraft {
		if pullRequest.Conflicts != nil && *pullRequest.Conflicts {
			parts = append(parts, "Merge conflicts")
		}
		if pullRequest.Checks != nil && *pullRequest.Checks != "" {
			parts = append(parts, checksLine[*pullRequest.Checks])
		}
		if pullRequest.Review != nil && *pullRequest.Review == domain.PullRequestReviewChangesRequested {
			parts = append(parts, "Changes requested")
		}
	}
	return strings.Join(parts, " · ")
}

// What a thread's own open or draft pull request needs fixed before anyone can review or merge it,
// most pressing first.
func pullRequestFixes(pullRequest *OwnPullRequest) []domain.NextStep {
	if pullRequest.State != domain.PullRequestStateOpen && pullRequest.State != domain.PullRequestStateDraft {
		return []domain.NextStep{}
	}
	fixes := []domain.NextStep{}
	if pullRequest.Conflicts != nil && *pullRequest.Conflicts {
		fixes = append(fixes, domain.NextStepResolveConflicts)
	}
	if pullRequest.Checks != nil && *pullRequest.Checks == domain.PullRequestChecksFailing {
		fixes = append(fixes, domain.NextStepFixCi)
	}
	if pullRequest.Review != nil && *pullRequest.Review == domain.PullRequestReviewChangesRequested {
		fixes = append(fixes, domain.NextStepAddressComments)
	}
	return fixes
}

// Statuses the user cannot resolve a thread from: it is working or waiting on them, or already resolved.
var unresolvable = []CoordinatedThreadStatus{
	CoordinatedThreadStatusApproval, CoordinatedThreadStatusAsking, CoordinatedThreadStatusWorking, CoordinatedThreadStatusResolved,
}

// Whether the user can file a thread away under Resolved as it stands now.
func CanResolve(status CoordinatedThreadStatus) bool {
	return !slices.Contains(unresolvable, status)
}

// What GitHub last said of the pull request of the checkout a thread works in, or nil before it has
// said anything.
func CheckoutAnswer(state *WorkspaceState, member *domain.Thread) domain.PullRequestAnswer {
	workspaceID, ok := ThreadWorkspaceID(state, member)
	if !ok {
		return nil
	}
	read := state.MemberPullRequests[workspaceID]
	if read == nil || read.Answered == nil || !*read.Answered {
		return nil
	}
	return read.Answer
}

// What the user can tell a thread under a coordinator to do next about its own pull request, most
// pressing first. `checkout` is nil before GitHub has answered.
func NextSteps(member *domain.Thread, status CoordinatedThreadStatus, checkout domain.PullRequestAnswer) []domain.NextStep {
	if !CanResolve(status) {
		return []domain.NextStep{}
	}
	pullRequest := CoordinatedThreadStatusOwnPullRequest(member)
	if pullRequest == nil {
		if _, none := checkout.(domain.PullRequestAnswerNone); none && member.Brief != nil && member.Brief.Delivers == domain.DeliveryPullRequest {
			return []domain.NextStep{domain.NextStepCreatePullRequest}
		}
		return []domain.NextStep{}
	}
	steps := pullRequestFixes(pullRequest)
	checksPending := pullRequest.Checks != nil && *pullRequest.Checks == domain.PullRequestChecksPending
	reviewClear := pullRequest.Review == nil || *pullRequest.Review == domain.PullRequestReviewApproved
	if pullRequest.State == domain.PullRequestStateOpen && len(steps) == 0 && !checksPending && reviewClear {
		return []domain.NextStep{domain.NextStepMerge}
	}
	return steps
}

// Where a thread under a coordinator stands, most pressing first.
func CoordinatedThreadStatusCoordinatedThreadStatus(thread *domain.Thread, busy, blocked jsonx.Set[string]) CoordinatedThreadStanding {
	standing := func(status CoordinatedThreadStatus, summary *string) CoordinatedThreadStanding {
		return CoordinatedThreadStanding{Thread: *thread, Status: status, Summary: summary}
	}
	var question *string
	if open := domain.OpenDecisions(thread); len(open) > 0 && open[0].Question != "" {
		question = &open[0].Question
	}
	if blocked.Has(thread.ID) {
		return standing(CoordinatedThreadStatusApproval, nil)
	}
	if question != nil {
		return standing(CoordinatedThreadStatusAsking, question)
	}
	if busy.Has(thread.ID) {
		if thread.Report != nil && thread.Report.State == domain.CoordinationStateWorking {
			return standing(CoordinatedThreadStatusWorking, &thread.Report.Summary)
		}
		return standing(CoordinatedThreadStatusWorking, nil)
	}
	if thread.ResolvedAt != nil {
		return standing(CoordinatedThreadStatusResolved, nil)
	}
	if stuck := domain.CoordinationStuckVerdict(thread); stuck != nil {
		return standing(CoordinatedThreadStatus(stuck.State), stuck.Summary)
	}
	var report *domain.CoordinationReport
	if thread.Report != nil && thread.Report.State != domain.CoordinationStateWorking {
		report = thread.Report
	}
	// Read by how its turn ended rather than by its verdict, which the user filing it away takes off.
	ended, _ := domain.LastRunOutcome(thread)
	pullRequest := CoordinatedThreadStatusOwnPullRequest(thread)
	if pullRequest != nil && len(pullRequestFixes(pullRequest)) > 0 {
		line := PullRequestLine(pullRequest)
		return standing(CoordinatedThreadStatusNeedsWork, &line)
	}
	if pullRequest != nil && pullRequest.State == domain.PullRequestStateOpen {
		line := PullRequestLine(pullRequest)
		return standing(CoordinatedThreadStatusReview, &line)
	}
	if report != nil {
		return standing(CoordinatedThreadStatusDone, &report.Summary)
	}
	// A thread that ended its turn without reporting, and left nothing waiting on the user, said where
	// it stands in its reply, which reads as its report: whatever engine it runs on, it is done.
	if ended == domain.ThreadOutcomeFinished {
		return standing(CoordinatedThreadStatusDone, closingLine(thread))
	}
	return standing(CoordinatedThreadStatusIdle, nil)
}

// The first line of what a thread last said that says something, as a report's summary would be.
func closingLine(thread *domain.Thread) *string {
	reply, ok := LastReply(thread, 0)
	if !ok {
		return nil
	}
	if line, ok := ReplyLine(reply, domain.MaxSummary); ok {
		return &line
	}
	return nil
}

// The Overview group each status files a thread under, which the coordinator's roster reads too.
var GroupOf = map[CoordinatedThreadStatus]domain.OverviewGroup{
	CoordinatedThreadStatusApproval:  domain.OverviewGroupNeeds,
	CoordinatedThreadStatusAsking:    domain.OverviewGroupNeeds,
	CoordinatedThreadStatusBlocked:   domain.OverviewGroupNeeds,
	CoordinatedThreadStatusFailed:    domain.OverviewGroupNeeds,
	CoordinatedThreadStatusNeedsWork: domain.OverviewGroupNeeds,
	CoordinatedThreadStatusReview:    domain.OverviewGroupReview,
	CoordinatedThreadStatusWorking:   domain.OverviewGroupWorking,
	CoordinatedThreadStatusDone:      domain.OverviewGroupDone,
	CoordinatedThreadStatusIdle:      domain.OverviewGroupDone,
	CoordinatedThreadStatusResolved:  domain.OverviewGroupResolved,
}

// The last thing the thread said, trimmed, from the message at `from` on (0 for all of them).
func LastReply(thread *domain.Thread, from int) (string, bool) {
	for index := len(thread.Messages) - 1; index >= from; index-- {
		message := thread.Messages[index]
		if message.Kind == domain.ConversationMessageKindAssistant {
			if text := coordinationTrim(message.Text); text != "" {
				return text, true
			}
		}
	}
	return "", false
}
