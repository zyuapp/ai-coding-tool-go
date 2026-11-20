package application

import (
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What a run starting under a coordinator, or a coordinator's own, adds to the start every run makes.

// A thread `located` in the checkout its run starts in, as coordination leaves it, and what the run is
// sent. `existing` is nil for a new thread. A new thread a coordinator starts works under it from its
// first run, and so does one a coordinator on another computer starts here. One whose coordinator was
// archived or lost its role on the way still names it, as the threads already under it do, so it is
// back under it with them. A new coordinator's threads run on what it runs on itself until the user
// picks otherwise. A coordinator's run hears everything its threads said since the last one and reads
// its memory whenever its session has yet to. A message is more work for a thread, so one that was
// resolved is open again.
func CoordinatedStartCoordinatedStart(state *WorkspaceState, pending *PendingRun, located, existing *domain.Thread) CoordinatedStart {
	coordination := pending.Coordination
	if coordination == nil {
		coordination = &CoordinationSend{}
	}
	startsCoordinator := pending.Role != nil && *pending.Role == domain.ThreadRoleCoordinator
	var lead *domain.Thread
	var remoteLead *domain.RemoteLead
	if existing == nil && !startsCoordinator {
		if coordination.CoordinatorID != nil {
			lead = coordThread(state.Threads, *coordination.CoordinatorID)
		}
		remoteLead = coordination.Lead
	}
	opened := located
	if existing == nil && startsCoordinator {
		catalog := CatalogOf(state)
		opened = domain.AsCoordinator(&catalog, located)
	}
	joined := opened
	if lead != nil {
		copied := *opened
		copied.ParentID = &lead.ID
		joined = &copied
	} else if remoteLead != nil {
		copied := *opened
		copied.RemoteLead = &domain.HeldLead{RemoteLead: *remoteLead}
		joined = &copied
	}
	coordinating := domain.IsCoordinator(joined)
	told := joined
	if coordinating && joined.CoordinationNotes != nil {
		heard := jsonx.Set[string]{}
		for _, note := range joined.CoordinationNotes {
			heard[note.ID] = struct{}{}
		}
		told = domain.WithoutCoordinationNotes(joined, heard)
	}
	if coordinating {
		told = domain.WithMemoryHeard(told)
	}
	thread := *told
	thread.ResolvedAt = nil
	// A thread starting under a coordinator reads the brief it was handed and the memory it was started with.
	standing := BriefPrompt(coordination.Brief, "")
	if joined != opened {
		standing = BriefPrompt(coordination.Brief, coordValueOr(coordination.Memory, ""))
	}
	sent := pending.Prompt
	if standing != "" {
		sent = fmt.Sprintf("%s\n\n%s", pending.Prompt, standing)
	}
	if !coordinating {
		return CoordinatedStart{Thread: thread, Prompt: sent}
	}
	// Any coordinator session that starts over history reads the seed, so one whose last session failed
	// before naming itself, or was dropped, still knows what came before and where read_thread finds it.
	seeded := thread.Continuation == nil && (len(joined.Messages) > 0 || joined.HistorySummary != nil)
	return CoordinatedStart{Thread: thread, Prompt: CoordinatorPrompt(state, joined, sent, CoordinationContextOptions{Carried: coordination.Notes, Fresh: seeded})}
}

// A suggestion the user started is `thread` now, which its coordinator hears without being woken by
// it: the threads and startingProposals as that leaves them.
func WithSuggestionStarted(state *WorkspaceState, pending *PendingRun, existing *domain.Thread, threads []*domain.Thread, thread *domain.Thread) ([]*domain.Thread, []string) {
	if existing != nil || pending.Coordination == nil || pending.Coordination.ProposalID == nil || *pending.Coordination.ProposalID == "" {
		return threads, state.StartingProposals
	}
	proposalID := *pending.Coordination.ProposalID
	leadID := pending.Coordination.CoordinatorID
	heard := make([]*domain.Thread, len(threads))
	for index, item := range threads {
		if leadID != nil && item.ID == *leadID {
			heard[index] = suggestionHeard(item, proposalID, thread)
		} else {
			heard[index] = item
		}
	}
	return heard, WithoutStarting(state, proposalID).StartingProposals
}

func suggestionHeard(lead *domain.Thread, proposalID string, thread *domain.Thread) *domain.Thread {
	var proposal *domain.ThreadProposal
	for _, item := range lead.Proposals {
		if item.ID == proposalID {
			proposal = item
			break
		}
	}
	settledLead := domain.WithoutProposals(lead, jsonx.SetOf(proposalID))
	if proposal == nil || !domain.IsCoordinator(lead) {
		return settledLead
	}
	return domain.WithCoordinationNote(settledLead, CoordinationNote(thread.ID, fmt.Sprintf("The user started your suggested thread \"%s\" [%s].", proposal.Title, thread.ID), Now(), CoordinationNoteOptions{Quiet: true}))
}
