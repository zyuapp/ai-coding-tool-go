package application

import (
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func runQueueOwn(id, text string) *QueuedMessage {
	return &QueuedMessage{HeldMessage: domain.HeldMessage{ID: id, Text: text, Prompt: text, Attachments: []string{}, UsersOwn: new(true)}}
}

func runQueueState(edits ...func(*domain.Thread)) *WorkspaceState {
	return runsWorkspace(func(state *WorkspaceState) {
		state.Threads = []*domain.Thread{runsTask("one", edits...)}
		state.QueuedMessages = map[string][]*QueuedMessage{"one": {runQueueOwn("a", "first"), runQueueOwn("b", "second")}}
	})
}

func runQueueDrain(state *WorkspaceState, intent QueueIntent) WorkspaceTransition {
	return Applied(state, SliceTransition(Drained(state, "one", intent)))
}

func runQueuePending(state WorkspaceState) []*PendingRun {
	var pending []*PendingRun
	for _, run := range state.PendingRuns {
		pending = append(pending, run)
	}
	return pending
}

func TestRunQueueDeliverStartsNextMessageAlone(t *testing.T) {
	runsPorted(t, func() {
		drained := runQueueDrain(runQueueState(), QueueIntentDeliver)
		pending := runQueuePending(drained.State)
		if len(pending) != 1 || pending[0].Text != "first" || len(pending[0].QueuedIDs) != 1 || pending[0].QueuedIDs[0] != "a" || pending[0].Warming != nil {
			t.Fatalf("pending = %+v", pending)
		}
		if _, ok := drained.Effects[0].(WorkspaceEffectResolveRunWorkspace); !ok {
			t.Fatalf("effects = %+v", drained.Effects)
		}
		if again := Drained(&drained.State, "one", QueueIntentDeliver); len(again.Edits) != 0 {
			t.Fatal("a queue a starting run is taking from started another")
		}
	})
}

func TestRunQueueResumeWarmsUp(t *testing.T) {
	runsPorted(t, func() {
		pending := runQueuePending(runQueueDrain(runQueueState(), QueueIntentResume).State)
		if len(pending) != 1 || pending[0].Text != "first" || !runsIsTrue(pending[0].Warming) {
			t.Fatalf("pending = %+v", pending)
		}
	})
}

func TestRunQueueHandBack(t *testing.T) {
	runsPorted(t, func() {
		drained := runQueueDrain(runQueueState(), QueueIntentHandBack)
		if len(drained.State.PendingRuns) != 0 || len(drained.State.QueuedMessages["one"]) != 0 {
			t.Fatal("a handed-back queue started or kept something")
		}
		if prompt := drained.State.Prompts["one"]; prompt != "first\n\nsecond" {
			t.Fatalf("prompt = %q", prompt)
		}
	})
}

func TestRunQueueHeldWhileLimitPaused(t *testing.T) {
	runsPorted(t, func() {
		state := runQueueState(func(thread *domain.Thread) {
			thread.LimitPause = &domain.LimitPause{UsageLimit: domain.UsageLimit{ResetsAt: 2, Window: "session"}, PausedAt: 1}
		})
		step := Drained(state, "one", QueueIntentDeliver)
		if len(step.Edits) != 0 || len(step.Effects) != 0 {
			t.Fatal("a paused thread's queue was drained")
		}
	})
}

func TestRunQueueSettledTurnSaysWhatBecomesOfQueue(t *testing.T) {
	for _, item := range []struct {
		end  TurnEnd
		want SettlementQueue
	}{
		{TurnEnd{Status: TurnStatusSucceeded}, SettlementQueueDeliver},
		{TurnEnd{Status: TurnStatusFailed}, SettlementQueueDeliver},
		{TurnEnd{Status: TurnStatusCancelled, Stop: new(StopIntentUser)}, SettlementQueueHandBack},
		{TurnEnd{Status: TurnStatusFailed, Limit: &domain.UsageLimit{ResetsAt: 2, Window: "session"}, Stop: new(StopIntentUser)}, SettlementQueueHandBack},
	} {
		if got := SettleTurn(item.end).Queue; got != item.want {
			t.Errorf("SettleTurn(%+v).Queue = %s, want %s", item.end, got, item.want)
		}
	}
}

func TestRunQueueEditableQueued(t *testing.T) {
	theirs := &QueuedMessage{HeldMessage: domain.HeldMessage{ID: "c", Text: "from a lead", Prompt: "from a lead", Attachments: []string{}, Origin: domain.MessageOriginThread{ThreadID: new("lead"), Title: "Lead"}}}
	steered := runQueueOwn("s", "steered")
	steered.Steering = new(true)
	queued := []*QueuedMessage{runQueueOwn("a", "first"), steered, theirs}
	if message, refusal := EditableQueued(queued, "a"); message != queued[0] || refusal != "" {
		t.Fatal("the user's own message is theirs to rewrite")
	}
	for id, want := range map[string]string{"s": QueuedSent, "gone": QueuedSent, "c": QueuedNotYours} {
		if message, refusal := EditableQueued(queued, id); message != nil || refusal != want {
			t.Errorf("EditableQueued(%s) = %v, %q", id, message, refusal)
		}
	}
}

func TestRunQueueResolveWorkspaceEffect(t *testing.T) {
	project := &domain.Project{ID: "p", Root: "/repo", WorkspaceID: new("ws")}
	thread := runsTask("one")
	worktree := &domain.Worktree{ID: "wt", Root: "/wt", WorkspaceID: "wt-ws"}
	if effect := ResolveWorkspaceEffect("x", thread, project, worktree, true, &DraftBranch{Name: "b", Create: true}); effect.Workspace == nil || effect.Workspace.Kind != domain.WorkspaceKindWorktree || effect.CreateWorktree != nil || effect.CreateBranch != nil {
		t.Fatalf("a worktree is where the run happens: %+v", effect)
	}
	effect := ResolveWorkspaceEffect("x", nil, project, nil, true, &DraftBranch{Name: "b", Create: true})
	if effect.CreateWorktree == nil || effect.CreateWorktree.CarryChanges || *effect.CreateWorktree.Branch != "b" || effect.CreateBranch == nil || effect.Workspace != nil || effect.Checkout != nil {
		t.Fatalf("a new worktree from a branch: %+v", effect)
	}
	effect = ResolveWorkspaceEffect("x", thread, project, nil, false, &DraftBranch{Name: "b"})
	if effect.Workspace == nil || effect.Workspace.ID != "ws" || effect.Checkout == nil || effect.CreateBranch != nil || effect.Picker {
		t.Fatalf("a branch without a checkout moves the project: %+v", effect)
	}
	if effect := ResolveWorkspaceEffect("x", nil, &domain.Project{ID: "p", Root: "/repo"}, nil, false, nil); !effect.Picker || effect.Workspace != nil || *effect.Root != "/repo" {
		t.Fatalf("a project with no workspace asks the picker: %+v", effect)
	}
}
