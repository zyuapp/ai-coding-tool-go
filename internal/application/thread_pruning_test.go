package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func TestDeletedThreadsLeaveNoSessionData(t *testing.T) {
	corePorted(t, func() {
		gone, payload := "archived-a", "held data"
		state := coreWorkspace(func(state *WorkspaceState) {
			state.Threads = []*domain.Thread{coreTask("kept"), coreTask(gone, thArchived(5))}
			state.CurrentID = new("kept")
			state.History, state.HistoryIndex = []string{gone, "kept"}, 1
			state.Prompts = map[string]string{gone: payload}
			state.Annotations = map[string][]*domain.Annotation{gone: {}}
			state.Pastes = map[string][]*domain.PastedText{gone: {{ID: "paste-1", Text: payload}}}
			state.Images = map[string][]*domain.StagedImage{gone: {{ID: "image-1", Path: "/tmp/image.png", Label: "Screenshot"}}}
			state.PendingRuns = map[string]*PendingRun{"pending": {ID: "pending", RunID: "run-pending", Origin: PendingRunOriginComposer, TaskID: new(gone), Text: payload, Prompt: payload, Attachments: []string{}}}
			state.QueuedMessages = map[string][]*QueuedMessage{gone: {{HeldMessage: domain.HeldMessage{ID: "queued", Text: payload, Prompt: payload, Attachments: []string{}}}}}
			state.LastRunIDs = map[string]string{gone: "run-old"}
			state.ActiveRuns = map[string]ActiveRun{gone: coreActive(gone, "run-active")}
			state.RunStatuses = map[string]ThreadRunStatus{gone: ThreadRunStatusStopped}
			state.StreamingTails = map[string]*StreamingTail{gone: {MessageID: "message", Text: payload}}
			state.ReadingPoints = map[string]contracts.ReadingPoint{gone: {Anchor: "message", Depth: 1}}
		})
		cleared := Reduce(state, contracts.TaskCommandTaskClearArchive{}).State
		threadsSame(t, cleared.History, []string{"kept"}, "history")
		if cleared.HistoryIndex != 0 {
			t.Fatalf("history index %v", cleared.HistoryIndex)
		}
		_, prompt := cleared.Prompts[gone]
		_, annotations := cleared.Annotations[gone]
		_, pastes := cleared.Pastes[gone]
		_, images := cleared.Images[gone]
		_, queued := cleared.QueuedMessages[gone]
		_, lastRun := cleared.LastRunIDs[gone]
		_, active := cleared.ActiveRuns[gone]
		_, status := cleared.RunStatuses[gone]
		_, tail := cleared.StreamingTails[gone]
		_, point := cleared.ReadingPoints[gone]
		if prompt || annotations || pastes || images || queued || lastRun || active || status || tail || point || len(cleared.PendingRuns) != 0 {
			t.Fatalf("left behind: %s", coreJSON(t, cleared))
		}
	})
}

func TestPruningAMissingTaskKeepsUnrelatedState(t *testing.T) {
	corePorted(t, func() {
		state := coreWorkspace(func(state *WorkspaceState) {
			state.Prompts = map[string]string{"kept": "draft"}
			state.ReadingPoints = map[string]contracts.ReadingPoint{"kept": nil}
		})
		pruned := Afterward(state, SliceTransition(Pruned(state, jsonx.SetOf("missing"))))
		if !jsonx.SameMap(pruned.Prompts, state.Prompts) || !jsonx.SameMap(pruned.ReadingPoints, state.ReadingPoints) {
			t.Fatal("unrelated drafts were copied")
		}
		if step := Pruned(state, jsonx.Set[string]{}); len(step.Edits) != 0 {
			t.Fatal("nothing removed still edited")
		}
	})
}
