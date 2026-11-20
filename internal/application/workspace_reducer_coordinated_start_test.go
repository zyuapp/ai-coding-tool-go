package application

import (
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestACoordinatorStartingANewSessionOverItsHistoryIsSeeded(t *testing.T) {
	coordPorted(t, func() {
		message := func(id string, kind domain.ConversationMessageKind, text string, edits ...func(*domain.ConversationMessage)) *domain.ConversationMessage {
			item := &domain.ConversationMessage{ID: id, Kind: kind, Text: text, At: 1}
			for _, edit := range edits {
				edit(item)
			}
			return item
		}
		lead := coordLead("lead", func(thread *domain.Thread) {
			thread.Memory = &domain.CoordinatorMemory{Purpose: new("Ship the v2 reader"), Notes: []*domain.MemoryNote{{ID: "n1", Text: "Propose threads first", At: 1}}, Revision: 1, Heard: new(1.0)}
			thread.Decisions = []*domain.Decision{{ID: "d1", Question: "Tokens or overrides?", Options: []*domain.DecisionOption{{Label: "Tokens"}, {Label: "Overrides"}}, RaisedAt: 1}}
			thread.Proposals = []*domain.ThreadProposal{{ID: "p1", Title: "Write the docs", Prompt: "Write them", Brief: new(coordBrief), ProposedAt: 1}}
			thread.CoordinationNotes = []*domain.CoordinationNote{{ID: "c1", ThreadID: "worker", Text: "\"Fix login\" was stopped.", At: 1, Quiet: new(true)}}
			thread.ResolvedAt = new(2.0)
			thread.Messages = []*domain.ConversationMessage{
				message("m1", domain.ConversationMessageKindUser, "Plan the reader"),
				message("m2", domain.ConversationMessageKindTool, "start_thread"),
				message("m3", domain.ConversationMessageKindAssistant, "I started Fix login."),
				message("m4", domain.ConversationMessageKindUser, "The test is fixed.", func(item *domain.ConversationMessage) {
					item.Origin = domain.MessageOriginThread{ThreadID: new("worker"), Title: "Fix login"}
				}),
				message("m5", domain.ConversationMessageKindUser, "Checking in", func(item *domain.ConversationMessage) { item.Withdrawn = new(true) }),
				message("m6", domain.ConversationMessageKindAssistant, "Report: all green", func(item *domain.ConversationMessage) { item.Artifact = new(true) }),
				message("m7", domain.ConversationMessageKindSystem, "Context auto-compacted at 90,000 tokens."),
			}
		})
		state := coordWorkspace(coordHolding(lead, coordTask("worker", coordUnder("lead"), coordTitled("Fix login"), func(thread *domain.Thread) { thread.Brief = new(coordBrief) })))
		start := CoordinatedStartCoordinatedStart(state, &PendingRun{TaskID: new("lead"), Prompt: "What next?", Coordination: &CoordinationSend{}}, lead, lead)
		seed, sent, found := strings.Cut(start.Prompt, "\n\n---\n")
		if !found || sent != "What next?" {
			t.Fatalf("the message that started the run comes after the seed: %q", start.Prompt)
		}
		want := strings.Join([]string{
			"This coordinator continues in a new session; read_thread with its ID [lead] reaches its full history.",
			"Your purpose: Ship the v2 reader",
			"Your memory:",
			"- [n1] Propose threads first",
			"Updates from your threads you have not seen yet:",
			"- \"Fix login\" was stopped.",
			"Your threads right now:",
			"- \"Fix login\" [worker] · idle",
			"Suggested threads waiting on the user:",
			"- \"Write the docs\" [p1]",
			"Decisions you raised that the user has yet to answer:",
			"- Tokens or overrides?",
			"The last messages of your conversation, oldest first:",
			"The user: Plan the reader",
			"You: I started Fix login.",
			"From Fix login · worker: The test is fixed.",
		}, "\n")
		if seed != want {
			t.Errorf("tool rows, withdrawn messages, artifacts and notices are left out:\n%s", seed)
		}
		if start.Thread.CoordinationNotes != nil || start.Thread.ResolvedAt != nil {
			t.Error("the notes it was seeded with are delivered, and a message opens it again")
		}
		fresh := coordLead("lead")
		opened := CoordinatedStartCoordinatedStart(coordWorkspace(coordHolding(fresh)), &PendingRun{TaskID: new("lead"), Prompt: "Plan the reader"}, fresh, fresh)
		if opened.Prompt != "Plan the reader" {
			t.Errorf("a coordinator with no history yet has nothing to be seeded with: %q", opened.Prompt)
		}
	})
}

func TestANewThreadUnderACoordinatorReadsItsBriefAndMemory(t *testing.T) {
	coordPorted(t, func() {
		lead := coordLead("lead", func(thread *domain.Thread) {
			thread.Memory = &domain.CoordinatorMemory{Purpose: new("Ship it"), Notes: []*domain.MemoryNote{}, Revision: 1}
		})
		state := coordWorkspace(coordHolding(lead))
		send := CoordinationSendOf(state, CoordinationSend{CoordinatorID: new("lead"), Brief: new(coordBrief)})
		located := coordTask("new")
		start := CoordinatedStartCoordinatedStart(state, &PendingRun{Prompt: "Fix it", Coordination: send}, located, nil)
		if coordValueOr(start.Thread.ParentID, "") != "lead" {
			t.Error("a new thread a coordinator starts works under it from its first run")
		}
		want := "Fix it\n\n---\nBrief from the coordinator that started this thread:\nThe user's words: fix the flaky login test\nDone when: the suite passes 50 runs\nDelivers: Pull request\n\nThe coordinator's purpose: Ship it"
		if start.Prompt != want {
			t.Errorf("prompt:\n%s", start.Prompt)
		}
	})
}
