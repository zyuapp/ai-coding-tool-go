package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var awPullRequest = domain.PullRequestRef{Number: 7, Title: "Poll me", URL: "https://github.com/o/r/pull/7", State: domain.PullRequestStateOpen}

func awFound(edit func(*domain.PullRequestRef)) domain.PullRequestAnswer {
	ref := awPullRequest
	if edit != nil {
		edit(&ref)
	}
	return domain.PullRequestAnswerFound{PullRequest: ref}
}

var awDelivers = &domain.ThreadBrief{Intent: "Ship it", DoneWhen: "It merges", Delivers: domain.DeliveryPullRequest}

func awAvailable(branch *string) contracts.ChangedFilesResult {
	return contracts.ChangedFilesResultAvailable{Files: []string{}, Branch: branch}
}

// A project whose checkout Git has already answered for, which is what names the branch.
func awProjected(edits ...func(*WorkspaceState)) *WorkspaceState {
	return awWorkspace(append([]func(*WorkspaceState){func(state *WorkspaceState) {
		state.Projects = []*domain.Project{{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}}
		state.DraftProjectID = new("project-a")
	}}, edits...)...)
}

func awCheckedOut() *WorkspaceState {
	return awProjected(func(state *WorkspaceState) {
		state.Threads = []*domain.Thread{awTask("task-a", awInProject("project-a")), awTask("task-b", awInProject("project-a"))}
		state.CurrentID = new("task-a")
		state.Environments = map[string]contracts.ChangedFilesResult{"workspace-a": awAvailable(new("pr-poll"))}
	})
}

func awReadOf(t *testing.T, transition WorkspaceTransition) WorkspaceEffectReadPullRequest {
	t.Helper()
	for _, effect := range transition.Effects {
		if read, ok := effect.(WorkspaceEffectReadPullRequest); ok {
			return read
		}
	}
	t.Fatalf("no read-pull-request in %#v", transition.Effects)
	return WorkspaceEffectReadPullRequest{}
}

func awAnswered(state *WorkspaceState, read WorkspaceEffectReadPullRequest, answer domain.PullRequestAnswer) WorkspaceTransition {
	return awApply(state, ReducePullRequests, PullRequestEvent{WorkspaceID: read.WorkspaceID, Branch: read.Branch, Read: read.Read, Members: read.Members, Answer: answer})
}

func TestPullRequestReadAnswersOnlyTheLatestAskOnItsBranch(t *testing.T) {
	awPorted(t, func() {
		first := awApply(awCheckedOut(), ReducePullRequests, contracts.PullRequestCommandPullRequestRead{})
		overtaken := awReadOf(t, first)
		if overtaken.WorkspaceID != "workspace-a" || awText(overtaken.Branch) != "pr-poll" || overtaken.Read != 1 || overtaken.Members != nil {
			t.Fatalf("read: %#v", overtaken)
		}
		second := awApply(&first.State, ReducePullRequests, contracts.PullRequestCommandPullRequestRead{})
		latest := awReadOf(t, second)
		late := awAnswered(&second.State, overtaken, awFound(nil))
		if _, ok := late.State.PullRequest.Answer.(domain.PullRequestAnswerNone); !ok {
			t.Fatalf("an overtaken ask spoke for the row")
		}
		elsewhere := latest
		elsewhere.Branch = new("other")
		if moved := awAnswered(&late.State, elsewhere, awFound(nil)); moved.State.PullRequest != late.State.PullRequest {
			t.Fatalf("an answer for another branch landed")
		}
		answered := awAnswered(&late.State, latest, awFound(nil))
		if !domain.SamePullRequest(answered.State.PullRequest.Answer, awFound(nil)) {
			t.Fatalf("answer: %#v", answered.State.PullRequest)
		}
		if link := answered.State.Threads[0].PullRequest; link == nil || link.Number != 7 || answered.State.Threads[1].PullRequest != nil {
			t.Fatalf("the thread read records the pull request")
		}

		again := awApply(&answered.State, ReducePullRequests, contracts.PullRequestCommandPullRequestRead{})
		if !domain.SamePullRequest(again.State.PullRequest.Answer, awFound(nil)) {
			t.Fatalf("asking again about the same branch blanked the row")
		}
		same := awAnswered(&again.State, awReadOf(t, again), awFound(nil))
		if same.State.PullRequest != again.State.PullRequest || !reflect.DeepEqual(same.State.Threads, again.State.Threads) {
			t.Fatalf("an unchanged answer rewrote the row")
		}
		moved := again.State
		moved.Environments = map[string]contracts.ChangedFilesResult{"workspace-a": awAvailable(new("other"))}
		if blanked := awApply(&moved, ReducePullRequests, contracts.PullRequestCommandPullRequestRead{}); !domain.SamePullRequest(blanked.State.PullRequest.Answer, domain.NoPullRequest) {
			t.Fatalf("another branch keeps the old answer")
		}

		closed := answered.State
		closed.Projects, closed.Threads, closed.CurrentID, closed.DraftProjectID = nil, nil, nil, nil
		if gone := awApply(&closed, ReducePullRequests, contracts.PullRequestCommandPullRequestRead{}); len(gone.Effects) != 0 || gone.State.PullRequest != nil {
			t.Fatalf("no checkout leaves nothing to ask")
		}
	})
}

func TestPullRequestReadMembersAsksEachCheckoutOnce(t *testing.T) {
	awPorted(t, func() {
		state := awProjected(func(state *WorkspaceState) {
			state.Projects = []*domain.Project{{ID: "a", Root: "/a", WorkspaceID: new("workspace-a")}, {ID: "b", Root: "/b", WorkspaceID: new("workspace-b")}}
			lead := func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator) }
			member := func(thread *domain.Thread) { thread.ParentID, thread.Brief = new("lead"), awDelivers }
			state.Threads = []*domain.Thread{
				awTask("lead", awInProject("a"), lead), awTask("one", awInProject("b"), member), awTask("two", awInProject("b"), member), awTask("elsewhere", awInProject("a")),
			}
			state.CurrentID = new("lead")
		})
		asked := awApply(state, ReducePullRequests, contracts.PullRequestCommandPullRequestReadMembers{})
		reads := []string{}
		for _, effect := range asked.Effects {
			if read, ok := effect.(WorkspaceEffectReadPullRequest); ok {
				reads = append(reads, read.WorkspaceID)
			}
		}
		if !reflect.DeepEqual(reads, []string{"workspace-b"}) {
			t.Fatalf("reads: %v", reads)
		}
		read := awReadOf(t, asked)
		if !reflect.DeepEqual(asked.State.MemberPullRequests["workspace-b"].ThreadIDs, []string{"one", "two"}) {
			t.Fatalf("thread ids: %v", asked.State.MemberPullRequests["workspace-b"].ThreadIDs)
		}
		plain := read
		plain.Members = nil
		if ignored := awAnswered(&asked.State, plain, awFound(nil)); !reflect.DeepEqual(ignored.State.Threads, asked.State.Threads) {
			t.Fatalf("an ordinary answer landed on a member read")
		}
		answered := awAnswered(&asked.State, read, awFound(nil))
		view := PullRequestViewMemberPullRequestsView(&answered.State, answered.State.Threads[0])
		if len(view.Found) != 1 || view.Found[0].PullRequest.Number != 7 || len(view.Found[0].Threads) != 2 || view.Settled {
			t.Fatalf("view: %#v", view)
		}
		if answered.State.PullRequest != nil {
			t.Fatalf("the checkout in front keeps its own answer")
		}
	})
}

func TestPullRequestMemberResolvesWhenItsOwnPullRequestMerges(t *testing.T) {
	awPorted(t, func() {
		worktree := &domain.Worktree{ID: "wt1", ProjectID: "project-a", Root: "/worktrees/repo-wt1", WorkspaceID: "worktree-wt1", BaseCommit: "abcdef1234", CreatedAt: 2, LastUsedAt: 2}
		delivering := func(edits ...func(*domain.Thread)) *WorkspaceState {
			return awProjected(func(state *WorkspaceState) {
				state.Worktrees = []*domain.Worktree{worktree}
				one := awTask("one", append([]func(*domain.Thread){awInProject("project-a"), awInWorktree("wt1"), func(thread *domain.Thread) {
					thread.ParentID, thread.Brief = new("lead"), awDelivers
				}}, edits...)...)
				state.Threads = []*domain.Thread{awTask("lead", awInProject("project-a"), func(thread *domain.Thread) { thread.Role = new(domain.ThreadRoleCoordinator) }), one}
				state.CurrentID = new("lead")
				state.Environments = map[string]contracts.ChangedFilesResult{"worktree-wt1": awAvailable(nil)}
			})
		}
		memberAnswer := func(state *WorkspaceState, answer domain.PullRequestAnswer) *WorkspaceState {
			asked := awApply(state, ReducePullRequests, contracts.PullRequestCommandPullRequestReadMembers{})
			return &[]WorkspaceTransition{awAnswered(&asked.State, awReadOf(t, asked), answer)}[0].State
		}
		merged := func(ref *domain.PullRequestRef) { ref.State = domain.PullRequestStateMerged }
		open := memberAnswer(delivering(), awFound(nil))
		if open.Threads[1].ResolvedAt != nil {
			t.Fatalf("an open pull request resolves nothing")
		}
		done := memberAnswer(open, awFound(merged))
		if done.Threads[1].ResolvedAt == nil {
			t.Fatalf("a merge resolves its member")
		}
		failing := memberAnswer(delivering(), awFound(func(ref *domain.PullRequestRef) {
			ref.Checks, ref.Review, ref.Conflicts = new(domain.PullRequestChecksFailing), new(domain.PullRequestReviewChangesRequested), new(true)
		}))
		if link := failing.Threads[1].PullRequest; link == nil || link.Checks == nil || *link.Checks != domain.PullRequestChecksFailing || link.Conflicts == nil {
			t.Fatalf("needs: %#v", link)
		}
		failed := memberAnswer(delivering(func(thread *domain.Thread) {
			thread.Report = &domain.CoordinationReport{State: domain.CoordinationStateFailed, Summary: "Post-merge migration failed", At: 5}
		}), awFound(merged))
		if failed.Threads[1].ResolvedAt != nil {
			t.Fatalf("a member left stuck is not resolved")
		}
		archived := delivering(func(thread *domain.Thread) { thread.ArchivedAt = new(1.0) })
		if asked := awApply(archived, ReducePullRequests, contracts.PullRequestCommandPullRequestReadMembers{}); len(asked.Effects) != 0 {
			t.Fatalf("an archived member is not asked about")
		}
	})
}
