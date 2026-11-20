package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Threads by the checkout they work in, keyed by workspace id in the order each id was first met
// (TS: Map<string, Thread[]>).
type CheckoutThreads struct {
	IDs     []string
	Threads map[string][]*domain.Thread
}

func newCheckoutThreads() *CheckoutThreads {
	return &CheckoutThreads{Threads: map[string][]*domain.Thread{}}
}

// Appends `threads` to `workspaceID`'s, adding the id at the end when new.
func (c *CheckoutThreads) add(workspaceID string, threads ...*domain.Thread) {
	if _, ok := c.Threads[workspaceID]; !ok {
		c.IDs = append(c.IDs, workspaceID)
	}
	c.Threads[workspaceID] = append(c.Threads[workspaceID], threads...)
}

func (c *CheckoutThreads) Has(workspaceID string) bool {
	_, ok := c.Threads[workspaceID]
	return ok
}

// The branch a checkout sits on, or nil while Git has not said or could not. `environment` may be nil.
func BranchOf(environment contracts.ChangedFilesResult) *string {
	if available, ok := environment.(contracts.ChangedFilesResultAvailable); ok {
		return available.Branch
	}
	return nil
}

// The answer held for a checkout, kept only while that checkout and branch are the ones on screen.
// `held`, `workspaceID` and `environment` may be nil.
func PullRequestFor(held *domain.PullRequestRead, workspaceID *string, environment contracts.ChangedFilesResult) domain.PullRequestAnswer {
	if held == nil || workspaceID == nil || held.WorkspaceID != *workspaceID || !sameOptionalText(held.Branch, BranchOf(environment)) {
		return domain.NoPullRequest
	}
	return held.Answer
}

// What one thread's panel asks about: changes with the checkout, its branch, or the thread reading it.
// Any argument may be nil.
func ThreadAsking(workspaceID *string, environment contracts.ChangedFilesResult, threadID *string) string {
	branch := BranchOf(environment)
	return strings.Join([]string{valueOr(workspaceID), valueOr(branch), valueOr(threadID)}, "\x00")
}

// Whether reading a thread's checkout again can change how it stands under its coordinator. A thread
// already resolved, or one that would not own the pull request it found, has nothing to learn.
func AwaitsPullRequest(member *domain.Thread) bool {
	return member.ResolvedAt == nil && OwnsPullRequest(member)
}

// The checkouts on this computer a coordinator's threads work in, each with the threads in it, in the
// order the list holds them, kept to the threads `counts` takes when given (nil takes every one). A
// pull request is read on the computer that holds its thread.
func MemberCheckouts(state *WorkspaceState, leadID string, counts func(member *domain.Thread) bool) *CheckoutThreads {
	checkouts := newCheckoutThreads()
	for _, member := range CoordinationMembers(state, leadID) {
		thread := &member.Thread
		if member.Computer != nil || (counts != nil && !counts(thread)) {
			continue
		}
		if workspaceID, ok := ThreadWorkspaceID(state, thread); ok && workspaceID != "" {
			checkouts.add(workspaceID, thread)
		}
	}
	return checkouts
}

var noMemberPullRequests = MemberPullRequestsView{Asking: "", Settled: true, Found: []*MemberPullRequest{}}

// What a coordinator's threads' pull requests are and when to ask about them again. What each one is
// comes from the threads, each of which records its own; when to ask comes from the reads of the
// checkouts they work in, kept to the threads a new answer could still move. `lead` may be nil.
func PullRequestViewMemberPullRequestsView(state *WorkspaceState, lead *domain.Thread) MemberPullRequestsView {
	if lead == nil {
		return noMemberPullRequests
	}
	var urls []string
	found := map[string]*MemberPullRequest{}
	members := CoordinationMembers(state, lead.ID)
	for _, member := range members {
		pullRequest := CoordinatedThreadStatusOwnPullRequest(&member.Thread)
		if pullRequest == nil {
			continue
		}
		known, ok := found[pullRequest.URL]
		if !ok {
			urls = append(urls, pullRequest.URL)
			found[pullRequest.URL] = &MemberPullRequest{PullRequest: *pullRequest, Threads: []*domain.Thread{&member.Thread}}
			continue
		}
		found[pullRequest.URL] = &MemberPullRequest{PullRequest: known.PullRequest, Threads: append(slices.Clip(known.Threads), &member.Thread)}
	}
	checkouts := MemberCheckouts(state, lead.ID, AwaitsPullRequest)
	// A thread on a paired computer is read there; what that computer last answered says whether to ask again.
	var elsewhere []*CoordinationMember
	for _, member := range members {
		if member.Computer != nil && AwaitsPullRequest(&member.Thread) {
			elsewhere = append(elsewhere, member)
		}
	}
	if len(found) == 0 && len(checkouts.IDs) == 0 && len(elsewhere) == 0 {
		return noMemberPullRequests
	}
	asking := []string{}
	settled := true
	for _, workspaceID := range checkouts.IDs {
		asking = append(asking, workspaceID+"\x00"+valueOr(BranchOf(state.Environments[workspaceID])))
		if !domain.PullRequestSettled(answerOf(state.MemberPullRequests[workspaceID])) {
			settled = false
		}
	}
	for _, member := range elsewhere {
		workspaceID, ok := ThreadWorkspaceID(member.Holder, &member.Thread)
		branch := ""
		if ok && workspaceID != "" {
			branch = valueOr(BranchOf(member.Holder.Environments[workspaceID]))
		}
		asking = append(asking, member.Computer.ID+"\x00"+member.Thread.ID+"\x00"+branch)
		if ok && workspaceID != "" && !domain.PullRequestSettled(answerOf(member.Holder.MemberPullRequests[workspaceID])) {
			settled = false
		}
	}
	view := MemberPullRequestsView{Asking: strings.Join(asking, "\n"), Settled: settled, Found: make([]*MemberPullRequest, len(urls))}
	for i, url := range urls {
		view.Found[i] = found[url]
	}
	return view
}

func answerOf(read *domain.PullRequestRead) domain.PullRequestAnswer {
	if read == nil {
		return domain.NoPullRequest
	}
	return read.Answer
}

func valueOr(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}
