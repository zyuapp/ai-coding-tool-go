package application

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// A coordinator's suggestions, and the threads it asked for on other computers: filed, started or
// allowed, turned down, and settled.

var ProposalInputs = []string{"coordination.proposed", "proposal.dismiss", "proposal.start", "proposal.settled"}

const askerGone = "The thread that asked for this no longer works under this coordinator."

// The proposals slice. `input` is one of the ProposalInputs.
func ReduceProposals(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.TaskCommandProposalStart, WorkspaceEventProposalSettled:
		return SliceTransition(proposalStarts(state, input))

	// Suggestions wait for the user, as many as there is room for. Nothing starts until the user starts
	// it. A thread under the coordinator asking to start one on another computer is put to the user at
	// once, as its decisions are, since no reply of the coordinator's says so; what the coordinator
	// asks for itself, its reply says.
	case WorkspaceEventCoordinationProposed:
		lead := coordThread(state.Threads, input.TaskID)
		if lead == nil || !domain.IsCoordinator(lead) {
			return SliceTransition{}
		}
		proposed := domain.WithProposals(lead, input.Proposals)
		if proposed == lead {
			return SliceTransition{}
		}
		filed := EditThread(ThreadsOf(state), lead.ID, func(*domain.Thread) ThreadPatch {
			return func(next *domain.Thread) { next.Proposals = proposed.Proposals }
		})
		held := jsonx.Set[string]{}
		for _, proposal := range lead.Proposals {
			held[proposal.ID] = struct{}{}
		}
		after := Afterward(state, SliceTransition{Edits: []OwnerEdit{filed}})
		effects := []WorkspaceEffect{}
		for _, proposal := range proposed.Proposals {
			if held.Has(proposal.ID) || proposal.From == nil || proposal.Place.Computer == nil {
				continue
			}
			effects = append(effects, Announced(after, lead, RequestHeadline(proposal.Title, proposal.Place.Computer.Name), NoticeReasonRequest)...)
		}
		return SliceTransition{Edits: []OwnerEdit{filed}, Effects: effects}

	// What the user turned down is gone, and its coordinator hears so with its next wake. One already
	// starting is past turning down; dismissing them all means the suggestions here, each thread asked
	// for on another computer being allowed or denied on its own. One elsewhere that was allowed and
	// carried there may have started with its answer lost, so it is given up on rather than denied;
	// one refused here before it ever went is denied, as it never left.
	case contracts.TaskCommandProposalDismiss:
		lead := coordThread(state.Threads, input.TaskID)
		if lead == nil || !domain.IsCoordinator(lead) {
			return SliceTransition{}
		}
		declined := targetedProposals(state, lead, input.ProposalID, input.ProposalIDs)
		if len(declined) == 0 {
			return SliceTransition{}
		}
		titles := make([]string, len(declined))
		ids := jsonx.Set[string]{}
		for index, proposal := range declined {
			titles[index] = `"` + proposal.Title + `"`
			ids[proposal.ID] = struct{}{}
		}
		var text string
		switch denied := declined[0].Place.Computer; {
		case denied != nil && coordTrue(declined[0].Carried):
			text = fmt.Sprintf("The user gave up on starting %s on %s. If it started there after all, you will hear from it.", titles[0], denied.Name)
		case denied != nil:
			text = fmt.Sprintf("The user did not allow %s to run on %s.", titles[0], denied.Name)
		case len(titles) == 1:
			text = "The user declined the suggested thread " + titles[0] + "."
		default:
			text = "The user declined the suggested threads " + strings.Join(titles[:len(titles)-1], ", ") + " and " + titles[len(titles)-1] + "."
		}
		note := CoordinationNote(lead.ID, text, Now(), CoordinationNoteOptions{Quiet: true})
		return SliceTransition{Edits: []OwnerEdit{EditThread(ThreadsOf(state), lead.ID, func(item *domain.Thread) ThreadPatch {
			told := domain.WithCoordinationNote(domain.WithoutProposals(item, ids), note)
			return func(next *domain.Thread) {
				next.Proposals = told.Proposals
				next.CoordinationNotes = told.CoordinationNotes
			}
		})}}
	}
	return SliceTransition{}
}

// `input` is a contracts.TaskCommandProposalStart or a WorkspaceEventProposalSettled.
func proposalStarts(state *WorkspaceState, input WorkspaceInput) Step {
	switch input := input.(type) {
	// The user starts a suggestion, or every one here not already starting, as its coordinator would
	// have started it then, or allows one on another computer in the place they picked. One that
	// cannot start keeps its row and says why.
	case contracts.TaskCommandProposalStart:
		lead := coordThread(state.Threads, input.TaskID)
		if lead == nil || !domain.IsCoordinator(lead) {
			return Step{}
		}
		waiting := targetedProposals(state, lead, input.ProposalID, input.ProposalIDs)
		// Start all is every start in turn; a refusal among them is the one said back.
		step := Step{}
		for _, proposal := range waiting {
			var started Step
			joined := AndThen(state, step, func(next *WorkspaceState) Step {
				if proposal.Place.Computer != nil {
					started = startElsewhere(next, lead.ID, proposal.ID, input.Place)
				} else {
					started = startProposal(next, lead.ID, proposal.ID)
				}
				return started
			})
			result := step.Result
			if _, refused := started.Result.(contracts.WorkspaceCommandResultFalse); refused {
				result = started.Result
			}
			joined.Result = result
			step = joined
		}
		return step

	case WorkspaceEventProposalSettled:
		return proposalSettled(state, input)
	}
	return Step{}
}

// A thread a coordinator here allowed on another computer starting there, or failing to, which is a
// thread on that computer now that tells the coordinator so itself once it shows up there. A start
// whose answer was lost settles all the same once its thread shows up there; one Stop all caught on
// its way has nothing left to say, and is let go only once that computer said it started nothing,
// since one whose answer was lost may still land, and is stopped then. Any other waits to be allowed again.
func proposalSettled(state *WorkspaceState, input WorkspaceEventProposalSettled) Step {
	lead := coordThread(state.Threads, input.TaskID)
	if input.ThreadID != nil {
		var place string
		found := false
		if lead != nil {
			for _, item := range lead.Proposals {
				if item.ID == input.ProposalID && item.Place.Computer != nil {
					place, found = item.Place.Computer.ID, true
					break
				}
			}
		}
		if !found {
			place, found = CaughtStartComputer(lead, input.ProposalID)
		}
		var computer *PairedComputer
		if found {
			computer = coordPaired(state, place)
		}
		return landedStep(state, input.TaskID, input.ProposalID, *input.ThreadID, nil, computer)
	}
	if lead == nil || !CatchesStart(lead, input.ProposalID) {
		message := "The thread did not start."
		if input.Message != nil {
			message = *input.Message
		}
		return ProposalFailure(state, input.TaskID, input.ProposalID, &message)
	}
	var edits []OwnerEdit
	if coordTrue(input.Refused) {
		edits = append(edits, EditThread(ThreadsOf(state), lead.ID, func(item *domain.Thread) ThreadPatch {
			caught := WithoutCaughtStart(item, input.ProposalID).CaughtStarts
			return func(next *domain.Thread) { next.CaughtStarts = caught }
		}))
	}
	return Step{Edits: append(edits, ProposalSettledIn(RunsOf(state), input.ProposalID))}
}

// coordPaired is the paired computer with `id`, or nil.
func coordPaired(state *WorkspaceState, id string) *PairedComputer {
	for _, computer := range state.Computers.Paired {
		if computer.ID == id {
			return computer
		}
	}
	return nil
}

// A thread a coordinator here allowed on `computer` having landed there as `threadID`, as that
// computer answered or as the thread itself shows up there, whichever is first heard: its card is
// gone, and a start Stop all caught on its way is stopped as it lands. `runEndedAt` is that thread's,
// nil when it has not ended a turn: one first seen past a turn of its own is past stopping, so a run
// going there now is someone's since, and is left alone. Hearing it again changes nothing.
// `computer` may be nil.
func ProposalLanded(state *WorkspaceState, leadID, proposalID, threadID string, runEndedAt *float64, computer *PairedComputer) WorkspaceTransition {
	return Applied(state, SliceTransition(landedStep(state, leadID, proposalID, threadID, runEndedAt, computer)))
}

func landedStep(state *WorkspaceState, leadID, proposalID, threadID string, runEndedAt *float64, computer *PairedComputer) Step {
	lead := coordThread(state.Threads, leadID)
	carded := lead != nil && slices.ContainsFunc(lead.Proposals, func(item *domain.ThreadProposal) bool { return item.ID == proposalID })
	caught := CatchesStart(lead, proposalID)
	if !carded && !caught && !slices.Contains(state.StartingProposals, proposalID) {
		return Step{}
	}
	uncarded := ThreadsOf(state)
	if carded {
		uncarded = EditThread(uncarded, leadID, func(item *domain.Thread) ThreadPatch {
			proposals := domain.WithoutProposals(item, jsonx.SetOf(proposalID)).Proposals
			return func(next *domain.Thread) { next.Proposals = proposals }
		})
	}
	started := ProposalSettledIn(RunsOf(state), proposalID)
	if !caught {
		return Step{Edits: []OwnerEdit{uncarded, started}}
	}
	if runEndedAt != nil {
		return Step{Edits: []OwnerEdit{EditThread(uncarded, leadID, func(item *domain.Thread) ThreadPatch {
			caughtStarts := WithoutCaughtStart(item, proposalID).CaughtStarts
			return func(next *domain.Thread) { next.CaughtStarts = caughtStarts }
		}), started}}
	}
	cancel := contracts.RunControlCommandRunCancel{TaskID: &threadID}
	effects := []WorkspaceEffect{}
	if computer != nil && contracts.SupportsComputerCommand(computer.Capabilities, cancel) {
		effects = append(effects, WorkspaceEffectComputerForward{ID: computer.ID, Inputs: []WorkspaceInput{cancel}})
	}
	return Step{Edits: []OwnerEdit{EditThread(uncarded, leadID, func(item *domain.Thread) ThreadPatch {
		caughtStarts := WithStartLanded(item, proposalID, threadID).CaughtStarts
		return func(next *domain.Thread) { next.CaughtStarts = caughtStarts }
	}), started}, Effects: effects}
}

// refusedStart is a suggestion's start failing with `message`, said on its row and answered to the input.
func refusedStart(state *WorkspaceState, leadID, proposalID, message string) Step {
	step := ProposalFailure(state, leadID, proposalID, &message)
	step.Result = contracts.WorkspaceCommandResultFalse{Message: message}
	return step
}

// proposalOf is the suggestion `proposalID` of the thread `leadID`, with that thread, or nils.
func proposalOf(state *WorkspaceState, leadID, proposalID string) (*domain.Thread, *domain.ThreadProposal) {
	lead := coordThread(state.Threads, leadID)
	if lead == nil {
		return nil, nil
	}
	for _, item := range lead.Proposals {
		if item.ID == proposalID {
			return lead, item
		}
	}
	return lead, nil
}

// One suggestion starting by the same rules as every start, under the coordinator that suggested it.
// A refusal, from those rules or from the send itself, is said on the suggestion's row rather than
// in the banner, and leaves it there to start again.
func startProposal(state *WorkspaceState, leadID, proposalID string) Step {
	return AndThen(state, ProposalFailure(state, leadID, proposalID, nil), func(retried *WorkspaceState) Step {
		lead, proposal := proposalOf(retried, leadID, proposalID)
		if proposal == nil {
			return Step{}
		}
		send, err := ProposalSend(proposal)
		if err != nil {
			return refusedStart(retried, leadID, proposalID, err.Error())
		}
		start := ThreadStartsNewThreadSend(retried, lead, *send, jsonx.Null[*PairedComputer]())
		if start.Error != nil {
			return refusedStart(retried, leadID, proposalID, *start.Error)
		}
		if start.Elsewhere != nil {
			return refusedStart(retried, leadID, proposalID, "Its project is no longer on this computer.")
		}
		sent := ReduceSending(retried, *start.Command, CoordinationSend{ProposalID: &proposalID}, SendSaid{})
		if Failed(sent) {
			// What the send refused is said on the row, so the banner keeps what it showed.
			var message string
			if sent.Refusal != nil {
				message = *sent.Refusal
			} else if refused, ok := sent.Result.(contracts.WorkspaceCommandResultFalse); ok {
				message = refused.Message
			}
			kept := sent
			kept.Refusal, kept.Error, kept.ClearsError = nil, nil, false
			return AndThen(retried, kept, func(next *WorkspaceState) Step {
				return refusedStart(next, leadID, proposalID, message)
			})
		}
		// Only a send that set a run on its way has anything left to finish starting.
		going := false
		for _, pending := range Afterward(retried, SliceTransition(sent)).PendingRuns {
			if pending.Coordination != nil && pending.Coordination.ProposalID != nil && *pending.Coordination.ProposalID == proposalID {
				going = true
				break
			}
		}
		if !going {
			return sent
		}
		return AndThen(retried, sent, func(next *WorkspaceState) Step {
			return Step{Edits: []OwnerEdit{ProposalStartingIn(RunsOf(next), proposalID)}}
		})
	})
}

// A thread asked for on a paired computer, allowed by the user in the place `chosen` (nil for the one
// asked for): the start is carried to that computer as the coordinator would have started it here,
// with its name, brief, the coordinator's memory as it stands, and the coordinator and suggestion it
// works under, signed by the thread that asked. It is allowed once: nothing here lets the next one
// through, and that computer starts a suggestion only once however often it is asked. A computer
// whose line is down, or whose build cannot take it, is refused, as is a project it no longer holds.
func startElsewhere(state *WorkspaceState, leadID, proposalID string, chosen *contracts.TaskCommandProposalStartPlace) Step {
	return AndThen(state, ProposalFailure(state, leadID, proposalID, nil), func(retried *WorkspaceState) Step {
		return allowedElsewhere(retried, leadID, proposalID, chosen)
	})
}

func allowedElsewhere(retried *WorkspaceState, leadID, proposalID string, chosen *contracts.TaskCommandProposalStartPlace) Step {
	lead, proposal := proposalOf(retried, leadID, proposalID)
	if proposal == nil || proposal.Place.Computer == nil {
		return Step{}
	}
	computer := coordPaired(retried, proposal.Place.Computer.ID)
	if refusal := LedStartRefusal(computer); refusal != nil {
		return refusedStart(retried, leadID, proposalID, *refusal)
	}
	if computer == nil {
		return refusedStart(retried, leadID, proposalID, "That computer is no longer paired.")
	}
	project := ""
	if chosen != nil {
		project = chosen.Project
	} else if proposal.Place.Project != nil {
		project = *proposal.Place.Project
	}
	if project == "" || computer.State == nil || !slices.ContainsFunc(computer.State.Projects, func(item *domain.Project) bool { return item.ID == project }) {
		return refusedStart(retried, leadID, proposalID, "That folder is no longer on "+computer.Name+".")
	}
	worktree := coordTrue(proposal.Place.Worktree)
	if chosen != nil {
		worktree = chosen.Worktree
	}
	place := proposal.Place
	place.Project = &project
	place.Worktree = &worktree
	// A thread under the coordinator that asked starts it as itself, so it runs as that thread's own start would have.
	var asker *domain.Thread
	if proposal.From != nil {
		for _, item := range retried.Threads {
			if item.ID == *proposal.From && domain.CoordinatorOf(retried.Threads, item) == lead {
				asker = item
				break
			}
		}
		if asker == nil {
			return refusedStart(retried, leadID, proposalID, askerGone)
		}
	}
	caller := lead
	if asker != nil {
		caller = asker
	}
	placed := *proposal
	placed.Place = place
	asked, err := ProposalSend(&placed)
	if err != nil {
		return refusedStart(retried, leadID, proposalID, err.Error())
	}
	start := ThreadStartsNewThreadSend(retried, caller, *asked, jsonx.Some(computer))
	if start.Error != nil {
		return refusedStart(retried, leadID, proposalID, *start.Error)
	}
	command := start.Command
	name := retried.Computers.Name
	if name == "" {
		name = "another computer"
	}
	send := contracts.TaskCommandTaskSend{
		Text:     &proposal.Prompt,
		Project:  &project,
		Worktree: &worktree,
		Model:    coordNonEmpty(command.Model),
		Engine:   coordNonEmpty(command.Engine),
		Effort:   coordNonEmpty(command.Effort),
		Role:     coordNonEmpty(command.Role),
		Brief:    command.Brief,
		Title:    coordNonEmpty(command.Title),
		Lead:     &domain.RemoteLead{RemoteSender: domain.RemoteSender{ThreadID: lead.ID, Title: lead.Title, Computer: name}, ProposalID: &proposalID},
		Sender:   &domain.RemoteSender{ThreadID: caller.ID, Title: caller.Title, Computer: name},
	}
	if memory := MemoryPrompt(lead.Memory); memory != "" {
		send.Memory = &memory
	}
	send = SendFor(computer, send)
	if !contracts.SupportsComputerCommand(computer.Capabilities, send) {
		return refusedStart(retried, leadID, proposalID, "Update AI Coding Tool on "+computer.Name+" to run threads there.")
	}
	// Allowed again, it is the user's to start, whatever an earlier Stop all caught of it.
	carried := EditThread(ThreadsOf(retried), leadID, func(item *domain.Thread) ThreadPatch {
		allowed := domain.WithProposalCarried(WithoutCaughtStart(item, proposalID), proposalID)
		return func(next *domain.Thread) {
			next.Proposals = allowed.Proposals
			next.CaughtStarts = allowed.CaughtStarts
		}
	})
	return Step{
		Edits:   []OwnerEdit{carried, ProposalStartingIn(RunsOf(retried), proposalID)},
		Effects: []WorkspaceEffect{WorkspaceEffectComputerForward{ID: computer.ID, Inputs: []WorkspaceInput{send}, Proposal: &ComputerEffectComputerForwardProposal{TaskID: leadID, ProposalID: proposalID}}},
	}
}

// coordNonEmpty is `value` when it holds something other than "", else nil, as a truthiness check reads it.
func coordNonEmpty[T ~string](value *T) *T {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

// A suggestion's start failing with `message`, or, with nil, a new start clearing that. Either way it
// is no longer starting, which only the thread it becomes ends otherwise.
func ProposalFailure(state *WorkspaceState, leadID, proposalID string, message *string) Step {
	lead := coordThread(state.Threads, leadID)
	settledIn := ProposalSettledIn(RunsOf(state), proposalID)
	if lead == nil {
		return Step{Edits: []OwnerEdit{settledIn}}
	}
	marked := domain.WithProposalError(lead, proposalID, message)
	if marked == lead {
		return Step{Edits: []OwnerEdit{settledIn}}
	}
	return Step{Edits: []OwnerEdit{EditThread(ThreadsOf(state), leadID, func(*domain.Thread) ThreadPatch {
		return func(next *domain.Thread) { next.Proposals = marked.Proposals }
	}), settledIn}}
}

// A suggestion no longer starting, whether it started, failed, or was stopped.
func WithoutStarting(state *WorkspaceState, proposalID string) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{ProposalSettledIn(RunsOf(state), proposalID)}})
}

// The suggestions a start or a dismissal is for: the one named, else those of `proposalIDs` here,
// as Start all and Dismiss all name the ones on screen, else every one here, as each thread asked
// for on another computer is allowed or denied on its own. One already starting is past either.
func targetedProposals(state *WorkspaceState, lead *domain.Thread, proposalID *string, proposalIDs []string) []*domain.ThreadProposal {
	var named jsonx.Set[string]
	if proposalIDs != nil {
		named = jsonx.SetOf(proposalIDs...)
	}
	targeted := []*domain.ThreadProposal{}
	for _, proposal := range lead.Proposals {
		var wanted bool
		if proposalID != nil {
			wanted = proposal.ID == *proposalID
		} else {
			wanted = proposal.Place.Computer == nil && (named == nil || named.Has(proposal.ID))
		}
		if wanted && !slices.Contains(state.StartingProposals, proposal.ID) {
			targeted = append(targeted, proposal)
		}
	}
	return targeted
}
