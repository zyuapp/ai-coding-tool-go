package host

import (
	"context"
	"errors"
	"sync"
	"testing"

	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// runtimeDesktop is the desktop a runtime test starts against: the store, histories and writes it
// answers from funcs, paired with no other computer.
type runtimeDesktop struct {
	RuntimeDesktop
	loadTaskStore      func() (*contracts.LoadedTaskStore, error)
	loadThreadMessages func(taskID string) ([]*domain.ConversationMessage, error)
	mu                 sync.Mutex
	persisted          []contracts.TaskStoreDelta
	badges             []float64
}

func (d *runtimeDesktop) LoadTaskStore(context.Context) (*contracts.LoadedTaskStore, error) {
	return d.loadTaskStore()
}

func (d *runtimeDesktop) LoadThreadMessages(_ context.Context, taskID string) ([]*domain.ConversationMessage, error) {
	return d.loadThreadMessages(taskID)
}

func (d *runtimeDesktop) SearchThreadMessages(context.Context, string, []string) ([]string, error) {
	return []string{}, nil
}

func (d *runtimeDesktop) PersistTaskStore(_ context.Context, delta contracts.TaskStoreDelta) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.persisted = append(d.persisted, delta)
	return nil
}

func (d *runtimeDesktop) SetBadgeCount(count float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.badges = append(d.badges, count)
}

func (d *runtimeDesktop) PublishMobileView(contracts.MobileViewUpdate) {}

var storedMessages = []*domain.ConversationMessage{{ID: "message", Kind: domain.ConversationMessageKindUser, Text: "persisted text", At: 1}}

func loadedStore() *contracts.LoadedTaskStore {
	return &contracts.LoadedTaskStore{ThreadStoreData: domain.ThreadStoreData{
		Tasks: []*domain.StoredThread{{
			ID: "selected", Title: "selected", Engine: "claude", ExecutionPolicy: "confirm", Messages: []*domain.ConversationMessage{},
			ContinuationStatus: "none", LastChangeSnapshot: domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}, UpdatedAt: 2,
			HistorySummary: &domain.StoredThreadHistorySummary{MessageCount: 1},
		}},
		Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{},
	}}
}

func newTestRuntime(desktop *runtimeDesktop) *Runtime {
	if desktop.loadTaskStore == nil {
		desktop.loadTaskStore = func() (*contracts.LoadedTaskStore, error) { return loadedStore(), nil }
	}
	if desktop.loadThreadMessages == nil {
		desktop.loadThreadMessages = func(string) ([]*domain.ConversationMessage, error) { return storedMessages, nil }
	}
	runtime := CreateWorkspaceRuntime(WorkspaceRuntimeHost{Desktop: desktop, Storage: newMemoryStorage(nil)})
	runtime.subscribe = func(SubscriptionHost) (func(), func()) { return func() {}, func() {} }
	runtime.perform = func(context.Context, application.WorkspaceEffect, EffectHost) error { return nil }
	return runtime
}

func within(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func selected(runtime *Runtime) *domain.Thread {
	return findThread(runtime.GetState().Threads, "selected")
}

func TestLoadedSettlesWithTheStoreInBeforeTheCurrentHistoryLoads(t *testing.T) {
	history := make(chan struct{})
	runtime := newTestRuntime(&runtimeDesktop{loadThreadMessages: func(string) ([]*domain.ConversationMessage, error) {
		<-history
		return storedMessages, nil
	}})
	defer runtime.Dispose()
	starting := runtime.Start()
	within(t, "loaded", runtime.Loaded())
	if current := runtime.GetState().CurrentID; current == nil || *current != "selected" {
		t.Fatalf("current = %v", current)
	}
	if len(selected(runtime).Messages) != 0 {
		t.Fatal("the history loaded before loaded settled")
	}
	close(history)
	within(t, "start", starting)
	if !jsonx.SameSlice(selected(runtime).Messages, storedMessages) {
		t.Fatalf("messages = %v", selected(runtime).Messages)
	}
}

func TestLoadedSettlesWhenTheStoreCannotBeRead(t *testing.T) {
	runtime := newTestRuntime(&runtimeDesktop{loadTaskStore: func() (*contracts.LoadedTaskStore, error) { return nil, errors.New("unreadable") }})
	defer runtime.Dispose()
	within(t, "start", runtime.Start())
	within(t, "loaded", runtime.Loaded())
	eventually(t, "the storage error", func() bool {
		message := runtime.GetState().StorageError
		return message != nil && *message == "unreadable"
	})
}

func TestLoadedSettlesWhenDisposedDuringTheLoadOrBeforeStarting(t *testing.T) {
	load := make(chan struct{})
	during := newTestRuntime(&runtimeDesktop{loadTaskStore: func() (*contracts.LoadedTaskStore, error) {
		<-load
		return loadedStore(), nil
	}})
	starting := during.Start()
	during.Dispose()
	within(t, "loaded", during.Loaded())
	close(load)
	within(t, "start", starting)

	unstarted := newTestRuntime(&runtimeDesktop{})
	unstarted.Dispose()
	within(t, "loaded", unstarted.Loaded())
}

func TestAStartedRuntimePersistsWhatChangesAndFlushesIt(t *testing.T) {
	desktop := &runtimeDesktop{}
	runtime := newTestRuntime(desktop)
	defer runtime.Dispose()
	within(t, "start", runtime.Start())
	if err := runtime.Dispatch(context.Background(), contracts.TaskCommandTaskRename{TaskID: "selected", Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if selected(runtime).Title != "Renamed" {
		t.Fatalf("title = %q", selected(runtime).Title)
	}
	if err := runtime.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	desktop.mu.Lock()
	defer desktop.mu.Unlock()
	last := desktop.persisted[len(desktop.persisted)-1]
	if len(last.Tasks) != 1 || last.Tasks[0].Task.Title != "Renamed" {
		t.Fatalf("persisted = %+v", desktop.persisted)
	}
}

func TestInputsRunInArrivalOrderAndAcceptanceWaitsForTheReducer(t *testing.T) {
	runtime := newTestRuntime(&runtimeDesktop{})
	defer runtime.Dispose()
	within(t, "start", runtime.Start())
	var executions []application.WorkspaceExecution
	for _, title := range []string{"one", "two", "three"} {
		executions = append(executions, runtime.Execute(contracts.TaskCommandTaskRename{TaskID: "selected", Title: title}))
	}
	for _, execution := range executions {
		if _, err := execution.Completed(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if selected(runtime).Title != "three" {
		t.Fatalf("title = %q", selected(runtime).Title)
	}
	accepted, err := runtime.Execute(contracts.TaskCommandTaskRename{TaskID: "selected", Title: "four"}).Accepted(context.Background())
	if err != nil || !isOK(accepted) || selected(runtime).Title != "four" {
		t.Fatalf("accepted = %v %v, title = %q", accepted, err, selected(runtime).Title)
	}
}
