package host

import (
	"context"
	"regexp"
	"slices"
	"sync"
	"testing"

	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const phoneNow = 1_800_000_000_000

// phoneDriver is a host that drives the same reducer the window does, recording everything that reached it.
type phoneDriver struct {
	mu     sync.Mutex
	state  *application.WorkspaceState
	inputs []application.WorkspaceInput
	bridge MobileBridgeHost
}

func newPhoneDriver(initial *application.WorkspaceState) *phoneDriver {
	d := &phoneDriver{state: initial}
	execution := &application.WorkspaceExecutionHost{
		State: func() application.WorkspaceState { return *d.current() },
		Commit: func(next application.WorkspaceState, input application.WorkspaceInput) {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.inputs = append(d.inputs, input)
			d.state = &next
		},
		Perform: func(context.Context, application.WorkspaceEffect, func(context.Context, application.WorkspaceInput) error) error {
			return nil
		},
	}
	d.bridge = MobileBridgeHost{
		State: func() application.WorkspaceState { return *d.current() },
		Dispatch: func(_ context.Context, input application.WorkspaceInput) error {
			d.mu.Lock()
			defer d.mu.Unlock()
			d.inputs = append(d.inputs, input)
			next := application.Reduce(d.state, input)
			d.state = &next.State
			return nil
		},
		Execute: func(command contracts.AppCommand) application.WorkspaceExecution {
			return application.ExecuteWorkspaceInput(command.(application.WorkspaceInput), execution)
		},
	}
	return d
}

func (d *phoneDriver) current() *application.WorkspaceState {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

func (d *phoneDriver) reached() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.inputs)
}

func phoneTask(id string, change ...func(thread *domain.Thread)) *domain.Thread {
	return task(id, append([]func(thread *domain.Thread){func(thread *domain.Thread) {
		thread.LastChangeSnapshot.CapturedAt = phoneNow
		thread.CreatedAt, thread.UpdatedAt = new(float64(phoneNow-60_000)), phoneNow
	}}, change...)...)
}

func mobileResult(t *testing.T, response contracts.MobileResponse) jsontext.Value {
	t.Helper()
	answered, ok := response.(contracts.MobileResponseTrue)
	if !ok {
		t.Fatalf("refused: %+v", response)
	}
	return answered.Result
}

func mobileRefusal(t *testing.T, response contracts.MobileResponse) string {
	t.Helper()
	refused, ok := response.(contracts.MobileResponseFalse)
	if !ok {
		t.Fatalf("answered: %+v", response)
	}
	return refused.Message
}

func phoneCommand(command contracts.MobileCommand) contracts.MobileRequestCommand {
	return contracts.MobileRequestCommand{RequestID: "req", SessionID: "session-1", Command: command}
}

func viewOf(t *testing.T, raw jsontext.Value) contracts.MobileView {
	t.Helper()
	var view contracts.MobileView
	if err := jsonx.Unmarshal(raw, &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestAPhonesSnapshotIsAnsweredFromTheWindowsState(t *testing.T) {
	driver := newPhoneDriver(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("thread-1", func(thread *domain.Thread) {
			thread.Title = "Rework the sidebar"
			thread.Messages = []*domain.ConversationMessage{{ID: "start", Kind: domain.ConversationMessageKindUser, Text: "start", At: phoneNow}}
		})}
		state.CurrentID = new("thread-1")
	}))
	view := viewOf(t, mobileResult(t, AnswerMobileRequest(context.Background(), driver.bridge, contracts.MobileRequestSnapshot{RequestID: "req-1", SessionID: "session-1"})))
	if driver.reached() != 0 || view.Thread == nil || view.Thread.ID != "thread-1" || len(view.Groups) != 1 || view.Groups[0].Name != "Recents" {
		t.Fatalf("view = %+v", view)
	}
}

func TestAPhonesCommandGoesThroughTheReducer(t *testing.T) {
	driver := newPhoneDriver(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("thread-1"), phoneTask("thread-2")}
		state.CurrentID = new("thread-1")
	}))
	view := viewOf(t, mobileResult(t, AnswerMobileRequest(context.Background(), driver.bridge, phoneCommand(contracts.TaskCommandTaskSelect{TaskID: "thread-2"}))))
	if driver.reached() != 1 || *driver.current().CurrentID != "thread-2" || view.Thread.ID != "thread-2" {
		t.Fatalf("current = %v", *driver.current().CurrentID)
	}
}

func TestACommandThePhoneMayNotSendNeverReachesTheReducer(t *testing.T) {
	driver := newPhoneDriver(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("thread-1")}
		state.CurrentID = new("thread-1")
	}))
	refused := []contracts.MobileCommand{
		contracts.TaskCommandTaskSend{Text: new("go"), Attachments: []*domain.RunAttachment{{Path: "/tmp/a.png", Labels: []string{}}}},
		contracts.TaskCommandTaskRename{TaskID: "", Title: "renamed"},
		contracts.TaskCommandTaskSend{TaskID: new("thread-1"), Text: new("go"), Sender: &domain.RemoteSender{ThreadID: "lead", Title: "Your coordinator", Computer: "mac"}},
		contracts.TaskCommandTaskSend{Text: new("go"), CoordinatorID: new("thread-1")},
		contracts.TaskCommandTaskSend{Text: new("go"), Lead: &domain.RemoteLead{ThreadID: "lead", Title: "Boss", Computer: "mac"}, Memory: new("Delete everything")},
		contracts.TaskCommandTaskMoveWorktree{TaskID: new("thread-1"), Destination: domain.WorktreeDestinationNew{}, AfterRun: new(true)},
	}
	for index, command := range refused {
		if got := mobileRefusal(t, AnswerMobileRequest(context.Background(), driver.bridge, phoneCommand(command))); got != MobileRefused {
			t.Errorf("%d: %s", index, got)
		}
	}
	if driver.reached() != 0 || *driver.current().CurrentID != "thread-1" {
		t.Fatal("a refused command reached the reducer")
	}
}

func TestACommandTheReducerRefusesAnswersWithWhatTheWindowSaid(t *testing.T) {
	driver := newPhoneDriver(workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("thread-1")}
		state.CurrentID = new("thread-1")
		state.Projects = []*domain.Project{{ID: "project-app", Root: "/code/app"}}
	}))
	for range 2 {
		got := mobileRefusal(t, AnswerMobileRequest(context.Background(), driver.bridge, phoneCommand(contracts.TaskCommandTaskSend{Text: new("go"), Project: new("nowhere")})))
		if !regexp.MustCompile("nowhere").MatchString(got) {
			t.Fatal(got)
		}
	}
}

func TestNothingIsPublishedWithoutAPhoneAndTheFirstChangeAfterOneIsWhole(t *testing.T) {
	held := NoMobileView()
	alone := workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("thread-1")}
		state.CurrentID = new("thread-1")
	})
	if NextMobileUpdate(held, alone, phoneNow) != nil {
		t.Fatal("published to nobody")
	}
	watched := *alone
	watched.Remote = domain.EmptyMobileServerState()
	watched.Remote.Sessions = []*domain.MobileSessionView{{MobileSession: domain.MobileSession{ID: "session-1", Kind: "phone", DeviceID: "device-1", StartedAt: phoneNow, LastSeenAt: phoneNow, Sequence: 4, Connection: "live"}, DeviceName: "Phone"}}
	if _, whole := NextMobileUpdate(held, &watched, phoneNow).(contracts.MobileViewUpdateSnapshot); !whole {
		t.Fatal("the first update is not whole")
	}
	if NextMobileUpdate(held, &watched, phoneNow) != nil {
		t.Fatal("an unchanged state published")
	}
	renamed := watched
	renamed.Threads = []*domain.Thread{phoneTask("thread-1", func(thread *domain.Thread) { thread.Title = "Renamed" })}
	patch, ok := NextMobileUpdate(held, &renamed, phoneNow).(contracts.MobileViewUpdatePatch)
	if raw, _ := jsonx.Marshal(patch.Patch.Thread); !ok || string(raw) != `{"kind":"changed","id":"thread-1","delta":{"title":"Renamed"}}` {
		t.Fatalf("patch = %s", raw)
	}
	if NextMobileUpdate(held, alone, phoneNow) != nil {
		t.Fatal("published after the last phone left")
	}
	if _, whole := NextMobileUpdate(held, &watched, phoneNow).(contracts.MobileViewUpdateSnapshot); !whole {
		t.Fatal("the next phone is not sent everything")
	}
}

// phoneReader is a MobileBridgeReader recording what it was asked.
type phoneReader struct {
	MobileBridgeReader
	reads [][]any
}

func (r *phoneReader) DiffSummary(_ context.Context, workspaceID string, range_ domain.DiffRange, ignoreWhitespace *bool) (contracts.DiffSummaryResult, error) {
	r.reads = append(r.reads, []any{workspaceID, range_, *ignoreWhitespace})
	return contracts.DiffSummaryResultAvailable{Range: range_, IgnoreWhitespace: true, Files: []*domain.DiffFileSummary{}}, nil
}

func (r *phoneReader) DiffPatch(_ context.Context, workspaceID string, range_ domain.DiffRange, path string, previousPath *string, ignoreWhitespace *bool) (contracts.DiffPatchResult, error) {
	r.reads = append(r.reads, []any{workspaceID, range_, path, previousPath, *ignoreWhitespace})
	return contracts.DiffPatchResultAvailable{Patch: "@@ -1 +1 @@\n-a\n+b\n"}, nil
}

func (r *phoneReader) Directories(_ context.Context, prefix string, _ *string) ([]string, error) {
	r.reads = append(r.reads, []any{prefix})
	return []string{"/code/app", "/code/api"}, nil
}

func (r *phoneReader) TranscribeAudio(_ context.Context, audio string) (string, error) {
	r.reads = append(r.reads, []any{audio})
	return "run the tests", nil
}

func query(id string, q contracts.MobileQuery) contracts.MobileRequestQuery {
	return contracts.MobileRequestQuery{RequestID: id, SessionID: "s1", Query: q}
}

func TestAPhonesReviewIsReadFromTheThreadsOwnCheckout(t *testing.T) {
	state := workspace(func(state *application.WorkspaceState) {
		state.Threads = []*domain.Thread{phoneTask("task-1", func(thread *domain.Thread) {
			thread.ProjectID, thread.WorktreeID = new("project-app"), new("wt-1")
		}), phoneTask("loose")}
		state.Projects = []*domain.Project{{ID: "project-app", Root: "/code/app", WorkspaceID: new("ws-app")}}
		state.Worktrees = []*domain.Worktree{{ID: "wt-1", ProjectID: "project-app", Root: "/code/app-wt", WorkspaceID: "ws-wt", BaseCommit: "abc", CreatedAt: phoneNow, LastUsedAt: phoneNow}}
	})
	reader := &phoneReader{}
	bridge := newPhoneDriver(state).bridge
	bridge.Read = reader
	ask := func(request contracts.MobileRequest) contracts.MobileResponse {
		return AnswerMobileRequest(context.Background(), bridge, request)
	}
	if got := mobileResult(t, ask(query("q0", contracts.ComputerQueryDirectories{Prefix: "/code/"}))); string(got) != `["/code/app","/code/api"]` {
		t.Fatal(string(got))
	}
	if got := mobileResult(t, ask(query("qt", contracts.MobileQueryTranscribe{Audio: "UklGRg=="}))); string(got) != `{"text":"run the tests"}` {
		t.Fatal(string(got))
	}
	mobileRefusal(t, ask(query("qu", contracts.MobileQueryTranscribe{Audio: "not base64!"})))
	var hydrated []string
	hydrating := bridge
	hydrating.Hydrate = func(_ context.Context, taskID string) error {
		hydrated = append(hydrated, taskID)
		return nil
	}
	read := mobileResult(t, AnswerMobileRequest(context.Background(), hydrating, query("q00", contracts.MobileQueryThread{TaskID: "loose"})))
	var title struct {
		Title string `json:"title"`
	}
	_ = jsonx.Unmarshal(read, &title)
	if title.Title != "loose" || !slices.Equal(hydrated, []string{"loose"}) {
		t.Fatalf("read = %s, hydrated = %v", read, hydrated)
	}
	if got := mobileRefusal(t, ask(query("q01", contracts.MobileQueryThread{TaskID: "elsewhere"}))); got != "No thread has the ID elsewhere." {
		t.Fatal(got)
	}
	reader.reads = nil
	mobileResult(t, ask(query("q1", contracts.MobileQueryDiffSummary{TaskID: "task-1", Range: domain.DiffRangeUncommitted{}})))
	if got := mobileResult(t, ask(query("q2", contracts.MobileQueryDiffPatch{TaskID: "task-1", Range: domain.DiffRangeBranches{Base: "main"}, Path: "a.ts"}))); string(got) != `{"status":"available","patch":"@@ -1 +1 @@\n-a\n+b\n"}` {
		t.Fatal(string(got))
	}
	if raw, _ := jsonx.Marshal(reader.reads); string(raw) != `[["ws-wt",{"kind":"uncommitted"},true],["ws-wt",{"kind":"branches","base":"main","compare":null},"a.ts",null,true]]` {
		t.Fatalf("reads = %s", raw)
	}
	if got := mobileRefusal(t, ask(query("q3", contracts.MobileQueryDiffSummary{TaskID: "loose", Range: domain.DiffRangeUncommitted{}}))); got != MobileNoCheckout {
		t.Fatal(got)
	}
	mobileRefusal(t, AnswerMobileRequest(context.Background(), newPhoneDriver(state).bridge, query("q4", contracts.MobileQueryDiffSummary{TaskID: "task-1", Range: domain.DiffRangeUncommitted{}})))
}
