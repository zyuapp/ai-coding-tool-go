package application

import (
	"reflect"
	"slices"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func panelsPopulated(t *testing.T) *WorkspaceState {
	t.Helper()
	state := panelsWorkspace(panelsOn(
		panelsTask("current", func(thread *domain.Thread) { thread.WorktreeID = new("wt") }),
		panelsTask("archived", func(thread *domain.Thread) { thread.ArchivedAt = new(10.0) }),
	))
	return panelsPatched(t, state, `{
		"activeRuns":{"current":{"taskId":"current","runId":"run","sequence":0,"status":"running","origin":"composer","quiet":false,"notified":false,"acknowledged":false,"reportedIssues":[],"messagesBefore":0,"before":{"updatedAt":1}}},
		"projects":[{"id":"project","root":"/repo"}],
		"worktrees":[{"id":"wt","projectId":"project","root":"/wt","workspaceId":"workspace","baseCommit":"abc","createdAt":1,"lastUsedAt":1}],
		"managedWorktrees":[{"id":"wt","root":"/wt","repository":"/repo","branch":"feature","status":{"changedFiles":null,"comparison":null}}],
		"automations":[{"id":"automation-current","taskId":"current","prompt":"Poll","schedule":"0 * * * *","paused":false,"createdAt":1,"updatedAt":1,"runCount":0,"nextRunAt":null}]
	}`)
}

func panelsSameView(field string, a, b *OwnWorkspaceView) bool {
	return sameValue(reflect.ValueOf(a).Elem().FieldByName(field), reflect.ValueOf(b).Elem().FieldByName(field))
}

func panelsPaired(t *testing.T, own *WorkspaceState, remote *WorkspaceState, active bool) *WorkspaceState {
	t.Helper()
	next := *own
	next.Computers.Paired = []*PairedComputer{{ComputerLink: domain.ComputerLink{ID: "linux", Name: "linux", Host: "linux.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1}, State: remote}}
	next.Computers.Active = nil
	if active {
		next.Computers.Active = new("linux")
	}
	return &next
}

func TestComposerAndStreamingUpdatesReuseWorkspaceLists(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsPopulated(t)
		before := DeriveView(state)
		typing := DeriveView(panelsPatched(t, state, `{"prompts":{"current":"typing"}}`))
		running := RunRecord(state.ActiveRuns["current"])
		running.Sequence = 1
		streamed := *state
		streamed.ActiveRuns = map[string]ActiveRun{"current": *running}
		streaming := DeriveView(panelsPatched(t, &streamed, `{"streamingTails":{"current":{"messageId":"message","text":"stream"}}}`))
		for _, view := range []*OwnWorkspaceView{typing, streaming} {
			for _, field := range []string{"OrderedThreads", "ArchivedThreads", "ActivityThreads", "RunningThreadIDs", "BlockedThreadIDs", "WorktreeGroups", "ManagedWorktrees", "WorktreeSettings", "Schedules", "SideChatAttention"} {
				if !panelsSameView(field, view, before) {
					t.Errorf("%s is rebuilt by a change it reads nothing of", field)
				}
			}
		}
		if typing.Prompt != "typing" || streaming.StreamingTail == nil || streaming.StreamingTail.Text != "stream" {
			t.Error("the change itself shows")
		}
	})
}

func TestTypingOverAPairedComputersThreadReusesBothComputersLists(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsPatched(t, panelsWorkspace(panelsOn(
			panelsTask("remote", func(thread *domain.Thread) {
				thread.ProjectID = new("remote-project")
				thread.WorktreeID = new("remote-wt")
			}),
			panelsTask("other", func(thread *domain.Thread) { thread.ProjectID = new("remote-project") }),
		)), `{
			"activeRuns":{"remote":{"taskId":"remote","runId":"run","sequence":0,"status":"running","origin":"composer","quiet":false,"notified":false,"acknowledged":false,"reportedIssues":[],"messagesBefore":0,"before":{"updatedAt":1}}},
			"projects":[{"id":"remote-project","root":"/remote"}],
			"worktrees":[{"id":"remote-wt","projectId":"remote-project","root":"/remote-wt","workspaceId":"remote-workspace","baseCommit":"abc","createdAt":1,"lastUsedAt":1}]
		}`)
		state := panelsPaired(t, panelsPopulated(t), remote, true)
		before := DeriveView(state)
		typing := DeriveView(panelsPatched(t, state, `{"prompts":{"remote":"x"}}`))
		if typing.Prompt != "x" {
			t.Error("the draft typed here shows")
		}
		for _, field := range []string{"OrderedThreads", "WorktreeGroups", "RunningThreadIDs", "Coordination", "CoordinatorsByComputer"} {
			if !panelsSameView(field, typing, before) {
				t.Errorf("%s is rebuilt by typing", field)
			}
		}
	})
}

func TestLeavingAPairedComputersThreadLetsGoOfItsLists(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsPatched(t, panelsWorkspace(panelsOn(panelsTask("remote", func(thread *domain.Thread) { thread.ProjectID = new("remote-project") }))), `{"projects":[{"id":"remote-project","root":"/remote"}]}`)
		paired := panelsPaired(t, panelsPopulated(t), remote, true)
		DeriveView(paired)
		shown := ShownViewCollections.Of(remote).Lists
		if again := ShownViewCollections.Of(remote).Lists; !sameValue(reflect.ValueOf(again), reflect.ValueOf(shown)) {
			t.Fatal("the shown lists are held")
		}
		DeriveView(panelsPaired(t, panelsPopulated(t), remote, false))
		if again := ShownViewCollections.Of(remote).Lists; sameValue(reflect.ValueOf(again), reflect.ValueOf(shown)) {
			t.Error("leaving lets go of them")
		}
	})
}

func TestRunApprovalAndCheckoutDeletionInvalidateBusyAndBlocked(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsPopulated(t)
		before := DeriveView(state)
		awaiting := RunRecord(state.ActiveRuns["current"])
		awaiting.Status = ActiveRunRunningStatusAwaitingApproval
		asking := *state
		asking.ActiveRuns = map[string]ActiveRun{"current": *awaiting}
		approval := DeriveView(&asking)
		if panelsSameView("ActivityThreads", approval, before) || !slices.Equal(approval.BlockedThreadIDs.Sorted(), []string{"current"}) || len(approval.ActivityThreads[ActivitySectionsKeyPriority]) != 1 || approval.ActivityThreads[ActivitySectionsKeyPriority][0].ID != "current" {
			t.Error("an approval moves the thread to Priority")
		}
		idle := *state
		idle.ActiveRuns = map[string]ActiveRun{}
		if view := DeriveView(&idle); len(view.RunningThreadIDs) != 0 {
			t.Error("an idle thread is not running")
		}
		deleting := DeriveView(panelsPatched(t, &idle, `{"deletingWorktrees":["/wt"]}`))
		if !slices.Equal(deleting.RunningThreadIDs.Sorted(), []string{"current"}) || !deleting.ManagedWorktrees[0].Deleting {
			t.Error("a checkout being deleted keeps its thread working")
		}
	})
}

func TestTheCheckoutsByIDHoldStill(t *testing.T) {
	panelsPorted(t, func() {
		remote := panelsPatched(t, panelsWorkspace(panelsOn(panelsTask("remote", func(thread *domain.Thread) {
			thread.ProjectID = new("remote-project")
			thread.WorktreeID = new("remote-wt")
		}))), `{
			"currentId":null,
			"projects":[{"id":"remote-project","root":"/remote"}],
			"worktrees":[{"id":"remote-wt","projectId":"remote-project","root":"/remote-wt","workspaceId":"remote-workspace","baseCommit":"abc","createdAt":1,"lastUsedAt":1}]
		}`)
		state := panelsPaired(t, panelsPopulated(t), remote, false)
		before := DeriveView(state)
		if len(before.WorktreesByID) != 2 || before.WorktreesByID["wt"] == nil || before.WorktreesByID["remote-wt"] == nil {
			t.Fatal("every computer's checkouts are listed")
		}
		messaged := *state.Threads[0]
		messaged.Messages = []*domain.ConversationMessage{{ID: "new", Kind: domain.ConversationMessageKindAssistant, Text: "new", At: 100}}
		moved := *state
		moved.Threads = []*domain.Thread{&messaged, state.Threads[1]}
		movedView := DeriveView(&moved)
		if panelsSameView("WorktreeGroups", movedView, before) || !jsonx.Same(movedView.WorktreesByID, before.WorktreesByID) {
			t.Error("a thread that moves leaves the checkouts alone")
		}
		resent := *remote
		resent.Worktrees = make([]*domain.Worktree, len(remote.Worktrees))
		for index, worktree := range remote.Worktrees {
			copied := *worktree
			resent.Worktrees[index] = &copied
		}
		if again := DeriveView(panelsPaired(t, panelsPopulated(t), &resent, false)); !jsonx.Same(again.WorktreesByID, before.WorktreesByID) {
			t.Error("the same checkouts sent again are the same")
		}
		renamedWorktree := *state.Worktrees[0]
		renamedWorktree.Name = new("dark-mode")
		renamed := *state
		renamed.Worktrees = []*domain.Worktree{&renamedWorktree}
		if view := DeriveView(&renamed); jsonx.Same(view.WorktreesByID, before.WorktreesByID) || deref(view.WorktreesByID["wt"].Name) != "dark-mode" {
			t.Error("a renamed checkout is new")
		}
	})
}

func panelsWatch(edits string) string {
	return `{"id":"automation-1","taskId":"task-a","prompt":"Babysit the PR","schedule":"*/5 * * * *","paused":false,"createdAt":1,"updatedAt":1,"runCount":3,"nextRunAt":null` + edits + `}`
}

func panelsSections(state *WorkspaceState) (priority, running, threads []string) {
	sections := WorkspaceViewCollections.Of(state).Lists.ActivityThreads
	ids := func(list []*domain.Thread) []string {
		out := []string{}
		for _, thread := range list {
			out = append(out, thread.ID)
		}
		return out
	}
	return ids(sections[ActivitySectionsKeyPriority]), ids(sections[ActivitySectionsKeyRunning]), ids(sections[ActivitySectionsKeyThreads])
}

func TestAWatchHoldsItsThreadInRunningBetweenTicks(t *testing.T) {
	panelsPorted(t, func() {
		state := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) {
			s.Threads = []*domain.Thread{panelsTask("task-a", func(thread *domain.Thread) {
				thread.Findings = []*domain.AutomationFinding{{ID: "finding-1", Headline: "CI failed", At: 2}}
				thread.Outcome = new(domain.ThreadOutcomeFinished)
			})}
		}), `{"automations":[`+panelsWatch(`,"endsWhen":"the PR is approved."`)+`]}`)
		priority, running, threads := panelsSections(state)
		if len(priority) != 0 || !slices.Equal(running, []string{"task-a"}) || len(threads) != 0 {
			t.Errorf("a watch holds its thread in Running: %v %v %v", priority, running, threads)
		}
		if WorkspaceViewCollections.Of(state).EveryBusy.Has("task-a") {
			t.Error("between ticks the row draws no spinner")
		}
	})
}

func TestARoutineOrAPausedWatchRanksByWhatTheThreadCarries(t *testing.T) {
	panelsPorted(t, func() {
		threads := []*domain.Thread{panelsTask("task-a", func(thread *domain.Thread) {
			thread.Findings = []*domain.AutomationFinding{{ID: "finding-1", Headline: "CI failed", At: 2}}
		}), panelsTask("task-b")}
		for _, automations := range []string{
			`[` + panelsWatch(``) + `,` + panelsWatch(`,"id":"automation-2","taskId":"task-b"`) + `]`,
			`[` + panelsWatch(`,"endsWhen":"the PR is approved.","paused":true`) + `,` + panelsWatch(`,"id":"automation-2","taskId":"task-b","endsWhen":"the PR is approved.","paused":true`) + `]`,
		} {
			state := panelsPatched(t, panelsWorkspace(func(s *WorkspaceState) { s.Threads = threads }), `{"automations":`+automations+`}`)
			priority, running, rest := panelsSections(state)
			if !slices.Equal(priority, []string{"task-a"}) || len(running) != 0 || !slices.Equal(rest, []string{"task-b"}) {
				t.Errorf("ranked by what the thread carries: %v %v %v", priority, running, rest)
			}
		}
	})
}
