package application

import (
	"maps"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var PullRequestInputs = []string{"pull-request.read", "pull-request.read-members", "pull-request.read-member", "pull-request.answered"}

// A checkout's read recorded among the member reads, and the effect that asks it (TS: MemberRead).
type pullRequestMemberRead struct {
	MemberPullRequests map[string]*domain.PullRequestRead
	Effect             WorkspaceEffect
}

// The pull requests slice. `input` is one of the PullRequestInputs. Its thread edits patch only `pullRequest`.
func ReducePullRequests(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.PullRequestCommandPullRequestRead:
		threadID := input.TaskID
		if threadID == nil {
			threadID = state.CurrentID
		}
		var workspaceID *string
		if input.TaskID != nil && *input.TaskID != "" {
			workspaceID = threadWorkspaceOf(state, threadByID(state.Threads, *input.TaskID))
		} else {
			workspaceID = CurrentWorkspaceID(state)
		}
		// Nothing to ask about leaves nothing to draw, so the answer the last checkout gave goes too.
		if workspaceID == nil || *workspaceID == "" {
			if state.PullRequest == nil {
				return SliceTransition{}
			}
			return SliceTransition{Patch: func(next *WorkspaceState) { next.PullRequest = nil }}
		}
		id := *workspaceID
		branch := BranchOf(state.Environments[id])
		held := state.PullRequest
		// Only another checkout or another branch can have a different answer, so only those blank the row.
		kept := domain.NoPullRequest
		read := 1.0
		if held != nil {
			if held.WorkspaceID == id && sameOptionalText(held.Branch, branch) {
				kept = held.Answer
			}
			read = held.Read + 1
		}
		pullRequest := &domain.PullRequestRead{WorkspaceID: id, Branch: branch, Read: read, Answer: kept}
		if threadID != nil && *threadID != "" {
			pullRequest.ThreadIDs = []string{*threadID}
		}
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.PullRequest = pullRequest },
			Effects: []WorkspaceEffect{WorkspaceEffectReadPullRequest{WorkspaceID: id, Branch: branch, Read: read}},
		}

	case contracts.PullRequestCommandPullRequestReadMembers:
		var lead *domain.Thread
		if id := input.TaskID; id != nil {
			lead = threadByID(state.Threads, *id)
		} else if state.CurrentID != nil {
			lead = threadByID(state.Threads, *state.CurrentID)
		}
		leads := lead != nil && domain.IsCoordinator(lead)
		// Only checkouts an answer could still move a thread in are asked about; the answer is recorded on every thread in them.
		ours := newCheckoutThreads()
		if leads {
			ours = MemberCheckouts(state, lead.ID, AwaitsPullRequest)
		}
		everyone := everyMemberCheckout(state)
		// Merged, never replaced, so another coordinator's read still in flight keeps its place. Only a
		// checkout no coordinator's thread works in any more is let go, and stops being drawn.
		memberPullRequests := state.MemberPullRequests
		changed := false
		for workspaceID := range state.MemberPullRequests {
			if !everyone.Has(workspaceID) {
				memberPullRequests = maps.Clone(state.MemberPullRequests)
				maps.DeleteFunc(memberPullRequests, func(workspaceID string, _ *domain.PullRequestRead) bool { return !everyone.Has(workspaceID) })
				changed = true
				break
			}
		}
		effects := []WorkspaceEffect{}
		for _, workspaceID := range ours.IDs {
			read := checkoutRead(state, memberPullRequests, workspaceID, everyone.Threads[workspaceID])
			memberPullRequests = read.MemberPullRequests
			changed = true
			effects = append(effects, read.Effect)
		}
		if leads {
			effects = append(effects, remoteReads(state, lead.ID)...)
		}
		transition := SliceTransition{Effects: effects}
		if changed {
			transition.Patch = func(next *WorkspaceState) { next.MemberPullRequests = memberPullRequests }
		}
		return transition

	// One thread's checkout, asked for by the coordinator it works under on another computer, which reads the answer off this computer's state.
	case contracts.PullRequestCommandPullRequestReadMember:
		thread := threadByID(state.Threads, input.TaskID)
		if thread == nil || thread.ArchivedAt != nil || !domain.WorksUnderCoordinator(state.Threads, thread) || !AwaitsPullRequest(thread) {
			return SliceTransition{}
		}
		read, ok := memberRead(state, thread)
		if !ok {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.MemberPullRequests = read.MemberPullRequests },
			Effects: []WorkspaceEffect{read.Effect},
		}

	case PullRequestEvent:
		// Answers to asks older than the latest are dropped, whichever order they arrive in.
		members := input.Members != nil && *input.Members
		held := state.PullRequest
		answersHeld := !members && held != nil && held.Read == input.Read && held.WorkspaceID == input.WorkspaceID && sameOptionalText(held.Branch, input.Branch)
		member := state.MemberPullRequests[input.WorkspaceID]
		answersMember := members && member != nil && member.Read == input.Read && sameOptionalText(member.Branch, input.Branch)
		if !answersHeld && !answersMember {
			return SliceTransition{}
		}
		current := answersHeld && !domain.SamePullRequest(held.Answer, input.Answer)
		// A checkout's first answer is news even when it says none, since none is now GitHub's word.
		changedMember := answersMember && (member.Answered == nil || !*member.Answered || !domain.SamePullRequest(member.Answer, input.Answer))
		var threadIDs []string
		if answersHeld {
			threadIDs = held.ThreadIDs
		} else if member != nil {
			threadIDs = member.ThreadIDs
		}
		edits := []OwnerEdit{}
		if threads, ok := pullRequestsLinked(state, threadIDs, input.Answer); ok {
			edits = append(edits, threads)
		}
		if !current && !changedMember {
			return SliceTransition{Edits: edits}
		}
		var pullRequest *domain.PullRequestRead
		if current {
			answered := *held
			answered.Answer = input.Answer
			pullRequest = &answered
		}
		var memberPullRequests map[string]*domain.PullRequestRead
		if changedMember {
			answered := *member
			answered.Answer, answered.Answered = input.Answer, new(true)
			memberPullRequests = maps.Clone(state.MemberPullRequests)
			memberPullRequests[input.WorkspaceID] = &answered
		}
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				if current {
					next.PullRequest = pullRequest
				}
				if changedMember {
					next.MemberPullRequests = memberPullRequests
				}
			},
			Edits: edits,
		}
	}
	return SliceTransition{}
}

// Asks again about the pull request of the checkout a thread under a coordinator works in, on behalf
// of every such thread in it. A thread's branch rarely moves, so the answer stands until the next one
// replaces it. False when the thread has no checkout.
func MemberPullRequestRead(state *WorkspaceState, thread *domain.Thread) (*WorkspaceState, WorkspaceEffect, bool) {
	read, ok := memberRead(state, thread)
	if !ok {
		return nil, nil, false
	}
	next := *state
	next.MemberPullRequests = read.MemberPullRequests
	return &next, read.Effect, true
}

func memberRead(state *WorkspaceState, thread *domain.Thread) (pullRequestMemberRead, bool) {
	workspaceID := threadWorkspaceOf(state, thread)
	if workspaceID == nil || *workspaceID == "" {
		return pullRequestMemberRead{}, false
	}
	threads, ok := everyMemberCheckout(state).Threads[*workspaceID]
	if !ok {
		threads = []*domain.Thread{thread}
	}
	return checkoutRead(state, state.MemberPullRequests, *workspaceID, threads), true
}

func checkoutRead(state *WorkspaceState, reads map[string]*domain.PullRequestRead, workspaceID string, threads []*domain.Thread) pullRequestMemberRead {
	held := reads[workspaceID]
	branch := BranchOf(state.Environments[workspaceID])
	// What GitHub said of another branch says nothing of this one, so the checkout waits for its answer again.
	var kept *domain.PullRequestRead
	if held != nil && sameOptionalText(held.Branch, branch) {
		kept = held
	}
	count := 1.0
	if held != nil {
		count = held.Read + 1
	}
	read := &domain.PullRequestRead{WorkspaceID: workspaceID, Branch: branch, Read: count, Answer: domain.NoPullRequest, ThreadIDs: make([]string, len(threads))}
	for index, thread := range threads {
		read.ThreadIDs[index] = thread.ID
	}
	if kept != nil {
		if kept.Answer != nil {
			read.Answer = kept.Answer
		}
		if kept.Answered != nil && *kept.Answered {
			read.Answered = new(true)
		}
	}
	recorded := maps.Clone(reads)
	if recorded == nil {
		recorded = map[string]*domain.PullRequestRead{}
	}
	recorded[workspaceID] = read
	return pullRequestMemberRead{
		MemberPullRequests: recorded,
		Effect:             WorkspaceEffectReadPullRequest{WorkspaceID: workspaceID, Branch: branch, Read: count, Members: new(true)},
	}
}

// What a coordinator's threads on paired computers need read: each computer that can take it is asked
// to read the checkout of each such thread there that an answer could still move, and the answer is
// recorded on the thread there, which is where this computer reads it. Like a read of a plan's limits,
// one the line drops is asked again on the next poll.
func remoteReads(state *WorkspaceState, leadID string) []WorkspaceEffect {
	var order []string
	asks := map[string][]WorkspaceInput{}
	for _, member := range CoordinationMembers(state, leadID) {
		if member.Computer == nil || member.Computer.Status != domain.ComputerStatusConnected || !AwaitsPullRequest(&member.Thread) {
			continue
		}
		read := contracts.PullRequestCommandPullRequestReadMember{TaskID: member.Thread.ID}
		if !contracts.SupportsComputerCommand(member.Computer.Capabilities, read) {
			continue
		}
		id := member.Computer.ID
		if _, ok := asks[id]; !ok {
			order = append(order, id)
		}
		asks[id] = append(asks[id], read)
	}
	effects := make([]WorkspaceEffect, len(order))
	for index, id := range order {
		effects[index] = WorkspaceEffectComputerForward{ID: id, Inputs: asks[id]}
	}
	return effects
}

// Every checkout a thread under any live coordinator works in, each with those threads, and every
// one a thread under a coordinator on another computer works in here.
func everyMemberCheckout(state *WorkspaceState) *CheckoutThreads {
	checkouts := newCheckoutThreads()
	for _, lead := range state.Threads {
		if !domain.IsCoordinator(lead) {
			continue
		}
		members := MemberCheckouts(state, lead.ID, nil)
		for _, workspaceID := range members.IDs {
			checkouts.add(workspaceID, members.Threads[workspaceID]...)
		}
	}
	for _, thread := range RemotelyLedThreads(state.Threads) {
		if workspaceID := threadWorkspaceOf(state, thread); workspaceID != nil && *workspaceID != "" {
			checkouts.add(*workspaceID, thread)
		}
	}
	return checkouts
}

// Records a found pull request, and how it stands, on the threads it answers for, so a search can
// find them by it and a coordinator sees what each still needs. A thread under a coordinator whose
// own pull request has just merged has nothing left to do, so it resolves, unless its turn left it
// stuck, which still needs the user. One the user reopened after that pull request merged was told
// nothing new and stays open, though a different pull request of its own merging is news.
// False when no thread changes.
func pullRequestsLinked(state *WorkspaceState, threadIDs []string, answer domain.PullRequestAnswer) (ThreadsEdit, bool) {
	found, ok := answer.(domain.PullRequestAnswerFound)
	if !ok || len(threadIDs) == 0 {
		return ThreadsEdit{}, false
	}
	pullRequest := found.PullRequest
	resolving := jsonx.Set[string]{}
	recorded := EditThreads(ThreadsOf(state), func(thread *domain.Thread) ThreadPatch {
		if !slices.Contains(threadIDs, thread.ID) || domain.LinkMatches(thread.PullRequest, &pullRequest) {
			return nil
		}
		link := domain.LinkOf(&pullRequest)
		after := *thread
		after.PullRequest = link
		own := CoordinatedThreadStatusOwnPullRequest(&after)
		merged := !mergedAlready(thread.PullRequest, pullRequest) && own != nil && own.State == domain.PullRequestStateMerged
		if merged && after.ResolvedAt == nil && after.ArchivedAt == nil && domain.CoordinationStuckVerdict(&after) == nil && domain.WorksUnderCoordinator(state.Threads, &after) {
			resolving[thread.ID] = struct{}{}
		}
		return func(next *domain.Thread) { next.PullRequest = link }
	})
	if jsonx.Same(recorded.View.Threads, state.Threads) {
		return ThreadsEdit{}, false
	}
	if len(resolving) > 0 {
		return ResolvedIn(recorded, resolving, Now()), true
	}
	return recorded, true
}

// Whether a thread's link already said the pull request found for it had merged. `link` may be nil.
func mergedAlready(link *domain.PullRequestLink, pullRequest domain.PullRequestRef) bool {
	return link != nil && link.State != nil && *link.State == domain.PullRequestStateMerged && link.Number == pullRequest.Number && link.URL == pullRequest.URL
}
