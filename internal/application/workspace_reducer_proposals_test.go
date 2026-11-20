package application

import (
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func suggestion(id string, edits ...func(*domain.ThreadProposal)) *domain.ThreadProposal {
	proposal := &domain.ThreadProposal{ID: id, Title: "Suggestion " + id, Prompt: "Do " + id, Brief: new(coordBrief), Place: domain.ProposalPlace{Worktree: new(false)}, ProposedAt: 1}
	for _, edit := range edits {
		edit(proposal)
	}
	return proposal
}

func suggesting(proposals ...*domain.ThreadProposal) *domain.Thread {
	return coordLead("lead", func(thread *domain.Thread) {
		thread.ThreadModel = &domain.ThreadModelChoice{Model: "opus", Effort: new(domain.AgentEffort("high"))}
		thread.Proposals = proposals
	})
}

func resolvingOf(effects []WorkspaceEffect) int {
	count := 0
	for _, effect := range effects {
		if _, ok := effect.(WorkspaceEffectResolveRunWorkspace); ok {
			count++
		}
	}
	return count
}

func proposalNamed(state *WorkspaceState, id string) *domain.ThreadProposal {
	for _, proposal := range coordThreadOf(state, "lead").Proposals {
		if proposal.ID == id {
			return proposal
		}
	}
	return nil
}

func TestStartAllStartsEverySuggestionHereAndOneThatCannotStartKeepsItsError(t *testing.T) {
	coordPorted(t, func() {
		project := &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}
		gone := suggestion("p3", func(proposal *domain.ThreadProposal) { proposal.Place.Project = new("project-gone") })
		state := coordWorkspace(coordHolding(suggesting(suggestion("p1"), suggestion("p2"), gone)), func(state *WorkspaceState) {
			state.Projects = []*domain.Project{project}
			state.CurrentID = new("lead")
		})
		start := func(state *WorkspaceState, input contracts.TaskCommandProposalStart) WorkspaceTransition {
			return Applied(state, ReduceProposals(state, input))
		}
		all := start(state, contracts.TaskCommandProposalStart{TaskID: "lead"})
		if resolvingOf(all.Effects) != 2 || !slices.Equal(all.State.StartingProposals, []string{"p1", "p2"}) {
			t.Fatalf("starting: %v", all.State.StartingProposals)
		}
		failed := proposalNamed(&all.State, "p3")
		if failed.Error == nil {
			t.Fatal("one that cannot start keeps its error")
		}
		coordMatch(t, *failed.Error, `No project matches "project-gone"`, "error")
		if all.State.ActionError != nil {
			t.Error("the refusal is said on its row, not in the banner")
		}
		if _, refused := all.Result.(contracts.WorkspaceCommandResultFalse); !refused {
			t.Error("Start all answers with the refusal")
		}
		if again := start(&all.State, contracts.TaskCommandProposalStart{TaskID: "lead"}); resolvingOf(again.Effects) != 0 {
			t.Error("suggestions already starting are not started again")
		}
		fixed := all.State
		fixed.Projects = []*domain.Project{project, {ID: "project-gone", Root: "/gone", WorkspaceID: new("workspace-gone")}}
		retried := start(&fixed, contracts.TaskCommandProposalStart{TaskID: "lead", ProposalID: new("p3")})
		if proposalNamed(&retried.State, "p3").Error != nil || !slices.Equal(retried.State.StartingProposals, []string{"p1", "p2", "p3"}) {
			t.Errorf("a new attempt clears the error: %v", retried.State.StartingProposals)
		}
	})
}

func TestProposalFailureSaysWhyOnItsRowAndClearsWhenStartedAgain(t *testing.T) {
	coordPorted(t, func() {
		state := coordWorkspace(coordHolding(suggesting(suggestion("p1"))), func(state *WorkspaceState) { state.StartingProposals = []string{"p1"} })
		failed := Afterward(state, SliceTransition(ProposalFailure(state, "lead", "p1", new("The folder is gone."))))
		if got := proposalNamed(failed, "p1").Error; got == nil || *got != "The folder is gone." || len(failed.StartingProposals) != 0 {
			t.Errorf("failed: %v %v", got, failed.StartingProposals)
		}
		cleared := Afterward(failed, SliceTransition(ProposalFailure(failed, "lead", "p1", nil)))
		if proposalNamed(cleared, "p1").Error != nil {
			t.Error("a new start clears it")
		}
		if same := Afterward(cleared, SliceTransition(ProposalFailure(cleared, "lead", "p1", nil))); same != cleared {
			t.Error("nothing to clear changes nothing")
		}
	})
}
