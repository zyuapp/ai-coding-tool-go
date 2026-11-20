package application

import (
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What a coordinator hears of one of its threads. Wherever the thread is, what it did comes in as
// member facts, from its own events here or from what a paired computer says it holds, and is heard
// here the same way: the notes its coordinator wakes to and what the user is told.

// What a coordinator hears of `member`'s facts, in order. Progress is not news: only a thread that is
// blocked, done or failed leaves a note, and only one that cannot go on is the user's to hear about
// at once. A decision is the user's to hear about at once, under the coordinator.
func MemberFactsHeardFacts(state *WorkspaceState, lead, member *domain.Thread, facts []MemberFact, at float64) HeardFacts {
	heard := HeardFacts{Notes: []*domain.CoordinationNote{}, Effects: []WorkspaceEffect{}}
	for _, fact := range facts {
		switch fact := fact.(type) {
		case MemberFactArrived:
			heard.Notes = append(heard.Notes, CoordinationNote(member.ID, fmt.Sprintf("\"%s\" started on %s.", member.Title, fact.Computer), at, CoordinationNoteOptions{Quiet: true}))
		case MemberFactRaised:
			heard.Notes = append(heard.Notes, DecisionNote(member, &fact.Decision, at, fact.RunID))
			heard.Effects = append(heard.Effects, Announced(state, lead, DecisionHeadline(&fact.Decision), NoticeReasonDecision)...)
		case MemberFactReported:
			if fact.Report.State == domain.CoordinationStateWorking {
				continue
			}
			heard.Notes = append(heard.Notes, ReportNote(member, &fact.Report, at, fact.RunID))
			if fact.Report.State != domain.CoordinationStateDone {
				heard.Effects = append(heard.Effects, Announced(state, member, ReportHeadline(&fact.Report), NoticeReasonBlocked)...)
			}
		case MemberFactFound:
			heard.Effects = append(heard.Effects, Announced(state, member, fact.Headline, NoticeReasonFinding)...)
		case MemberFactWaiting:
			heard.Effects = append(heard.Effects, Announced(state, member, WaitingHeadline(fact.On), NoticeReasonWaiting)...)
		case MemberFactEnded:
			heard.Notes = append(heard.Notes, TurnNote(member, fact.Status, at, fact.Quiet))
			if fact.Tell == nil {
				continue
			}
			if headline, ok := SettledHeadline(fact.Tell.Surfacing, fact.Tell.Failure); ok {
				reason := NoticeReasonFinished
				if fact.Tell.Surfacing != nil && *fact.Tell.Surfacing == RunSurfacingFailed {
					reason = NoticeReasonFailed
				}
				heard.Effects = append(heard.Effects, Announced(state, member, headline, reason)...)
			}
		}
	}
	return heard
}

// Its coordinator here hearing `member`'s facts: each note kept for its next wake, and what the user is told.
func WithFactsHeard(state *WorkspaceState, lead, member *domain.Thread, facts []MemberFact, at float64) WorkspaceTransition {
	heard := MemberFactsHeardFacts(state, lead, member, facts, at)
	next := state
	if len(heard.Notes) > 0 {
		next = Afterward(state, SliceTransition{Edits: []OwnerEdit{NotedIn(ThreadsOf(state), lead.ID, heard.Notes...)}})
	}
	return WorkspaceTransition{State: *next, Effects: heard.Effects}
}

// WithFactsHeard on the threads an input is changing, as `state` reads with them in.
func FactsHeardIn(state *WorkspaceState, threads ThreadsEdit, lead, member *domain.Thread, facts []MemberFact, at float64) (ThreadsEdit, []WorkspaceEffect) {
	heard := MemberFactsHeardFacts(Afterward(state, SliceTransition{Edits: []OwnerEdit{threads}}), lead, member, facts, at)
	if len(heard.Notes) == 0 {
		return threads, heard.Effects
	}
	return EditThread(threads, lead.ID, func(item *domain.Thread) ThreadPatch {
		told := item
		for _, note := range heard.Notes {
			told = domain.WithCoordinationNote(told, note)
		}
		return func(next *domain.Thread) { next.CoordinationNotes = told.CoordinationNotes }
	}), heard.Effects
}
