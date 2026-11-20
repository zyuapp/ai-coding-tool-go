package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestRestoredThreadsStopWhatTheAppQuitUnder(t *testing.T) {
	corePorted(t, func() {
		open := coreTask("open", func(t *domain.Thread) {
			t.RunOpen = new(true)
			t.UpdatedAt = 10
			t.Messages = []*domain.ConversationMessage{{ID: "m", Kind: domain.ConversationMessageKindAssistant, Text: "working", At: 40}}
			t.Decisions = []*domain.Decision{{ID: "d", Question: "Ship?", Options: []*domain.DecisionOption{}, RaisedAt: 55}}
		})
		quiet := coreTask("quiet")
		storedOpen := coreStored(t, open)
		storedOpen.Subagents = []*domain.Subagent{
			{ID: "working", Status: domain.SubagentStatusWorking, Stopping: new(true)},
			{ID: "done", Status: domain.SubagentStatusCompleted},
		}
		threads, subagents, cutOff := RestoredThreads([]*domain.StoredThread{storedOpen, coreStored(t, quiet)})
		if len(threads) != 2 || len(cutOff) != 1 || cutOff[0] != threads[0] {
			t.Fatalf("cut off: %s", coreJSON(t, cutOff))
		}
		restored := threads[0]
		if restored.RunOpen != nil || *restored.RunEndedAt != 56 || restored.RankedAt != 56 || *restored.RunEndedAs != domain.ThreadOutcomeStopped {
			t.Fatalf("restored: %s", coreJSON(t, restored))
		}
		held := subagents["open"]
		if len(held) != 2 || held[0].Status != domain.SubagentStatusStopped || held[0].Stopping != nil || held[1].Status != domain.SubagentStatusCompleted {
			t.Fatalf("subagents: %s", coreJSON(t, held))
		}
		if _, ok := subagents["quiet"]; ok {
			t.Fatal("a thread with no subagents holds a list")
		}
		state := coreWorkspace(func(s *WorkspaceState) { s.Threads = threads })
		ended := WithCutOffTurnsEnded(state, cutOff, 60)
		if got := threadsFind(ended.Threads, "open"); got.Outcome == nil || *got.Outcome != domain.ThreadOutcomeStopped {
			t.Fatalf("verdict: %s", coreJSON(t, got))
		}
	})
}

func TestRestoredResourceStopIsCleanedUp(t *testing.T) {
	corePorted(t, func() {
		stopped := coreTask("stopped", func(t *domain.Thread) {
			t.ResourceStop = &domain.ResourceStop{Cleanup: domain.ResourceStopCleanupStopping}
		})
		threads, _, cutOff := RestoredThreads([]*domain.StoredThread{coreStored(t, stopped)})
		if threads[0].ResourceStop.Cleanup != domain.ResourceStopCleanupComplete || len(cutOff) != 1 {
			t.Fatalf("restored: %s", coreJSON(t, threads[0]))
		}
	})
}
