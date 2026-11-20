package host

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func storeWithCold() *contracts.LoadedTaskStore {
	store := loadedStore()
	selected := store.Tasks[0]
	selected.HistorySummary = nil
	cold := *selected
	cold.ID, cold.Title, cold.UpdatedAt = "cold", "cold", 1
	cold.HistorySummary = &domain.StoredThreadHistorySummary{MessageCount: 1}
	store.Tasks = append(store.Tasks, &cold)
	return store
}

// slowHistory is a history read held until released, which says when it starts.
type slowHistory struct {
	once    sync.Once
	loading chan struct{}
	loaded  chan struct{}
	mu      sync.Mutex
	loads   []string
}

func newSlowHistory() *slowHistory {
	return &slowHistory{loading: make(chan struct{}), loaded: make(chan struct{})}
}

func (h *slowHistory) load(taskID string) ([]*domain.ConversationMessage, error) {
	h.mu.Lock()
	h.loads = append(h.loads, taskID)
	h.mu.Unlock()
	h.once.Do(func() { close(h.loading) })
	<-h.loaded
	return storedMessages, nil
}

func startedRuntime(t *testing.T, desktop *runtimeDesktop) *Runtime {
	t.Helper()
	desktop.loadTaskStore = func() (*contracts.LoadedTaskStore, error) { return storeWithCold(), nil }
	runtime := newTestRuntime(desktop)
	within(t, "start", runtime.Start())
	return runtime
}

func runStarted(taskID, runID string) contracts.AgentEvent {
	return contracts.RunEventRunStarted{RunEventBase: contracts.RunEventBase{TaskID: taskID, RunID: runID, Sequence: 1}, AgentInitiated: new(true)}
}

func completes(t *testing.T, execution application.WorkspaceExecution) <-chan contracts.WorkspaceCommandResult {
	t.Helper()
	done := make(chan contracts.WorkspaceCommandResult, 1)
	go func() {
		result, err := execution.Completed(context.Background())
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	return done
}

func settledWithin(t *testing.T, what string, done <-chan contracts.WorkspaceCommandResult) contracts.WorkspaceCommandResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

func TestTheRuntimeServesAPairedTranscriptFromDiskWithoutChangingSelection(t *testing.T) {
	history := newSlowHistory()
	close(history.loaded)
	runtime := startedRuntime(t, &runtimeDesktop{loadThreadMessages: history.load})
	defer runtime.Dispose()
	selectedID := *runtime.GetState().CurrentID
	response, err := runtime.QueryThreads(context.Background(), contracts.ComputerThreadQueryThreadRead{ThreadID: "cold", Limit: new(1.0)})
	if err != nil {
		t.Fatal(err)
	}
	transcript := response.(*contracts.ThreadTranscript)
	if transcript.Thread.ID != "cold" || len(transcript.Messages) != 1 || transcript.Messages[0].Text != "persisted text" || !slices.Equal(history.loads, []string{"cold"}) {
		t.Fatalf("transcript = %+v, loads = %v", transcript, history.loads)
	}
	if *runtime.GetState().CurrentID != selectedID {
		t.Fatal("reading moved the selection")
	}
}

func TestAThreadsHistoryReadHoldsOnlyInputsForThatThread(t *testing.T) {
	history := newSlowHistory()
	runtime := startedRuntime(t, &runtimeDesktop{loadThreadMessages: history.load})
	defer runtime.Dispose()
	defer func() {
		select {
		case <-history.loaded:
		default:
			close(history.loaded)
		}
	}()
	if err := runtime.Dispatch(context.Background(), application.WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{runStarted("selected", "selected-run")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Execute(contracts.TaskCommandTaskSelect{TaskID: "cold"}).Accepted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *runtime.GetState().CurrentID != "cold" || findThread(runtime.GetState().Threads, "cold").HistorySummary == nil {
		t.Fatal("selection did not land before the transcript")
	}
	within(t, "the history read", history.loading)
	first := completes(t, runtime.Execute(application.WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{
		runStarted("cold", "cold-run"),
		contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "cold", RunID: "cold-run", Sequence: 2}, MessageID: "first", Text: "First"},
	}}))
	second := completes(t, runtime.Execute(application.WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{
		contracts.RunEventAssistantDelta{RunEventBase: contracts.RunEventBase{TaskID: "cold", RunID: "cold-run", Sequence: 3}, MessageID: "second", Text: "Second"},
	}}))
	settledWithin(t, "cancel", completes(t, runtime.Execute(contracts.RunControlCommandRunCancel{TaskID: new("selected")})))
	settledWithin(t, "settings", completes(t, runtime.Execute(contracts.ViewCommandViewSetSettingsOpen{Open: true})))
	if !runtime.GetState().SettingsOpen {
		t.Fatal("settings did not open")
	}
	if runtime.GetState().ActiveRuns["cold"] != nil {
		t.Fatal("the cold thread's events did not wait for its transcript")
	}
	close(history.loaded)
	settledWithin(t, "first", first)
	settledWithin(t, "second", second)
	var texts []string
	for _, message := range findThread(runtime.GetState().Threads, "cold").Messages {
		texts = append(texts, message.Text)
	}
	if !slices.Equal(texts, []string{"persisted text", "First", "Second"}) {
		t.Fatalf("messages = %v", texts)
	}
}

func TestABatchSpanningALoadingThreadHoldsOnlyItsEvents(t *testing.T) {
	history := newSlowHistory()
	runtime := startedRuntime(t, &runtimeDesktop{loadThreadMessages: history.load})
	defer runtime.Dispose()
	batch := completes(t, runtime.Execute(application.WorkspaceEventAgentEvents{Events: []contracts.AgentEvent{runStarted("cold", "cold-run"), runStarted("selected", "selected-run")}}))
	within(t, "the history read", history.loading)
	eventually(t, "the loaded thread's event", func() bool {
		record := application.RunRecord(runtime.GetState().ActiveRuns["selected"])
		return record != nil && record.RunID == "selected-run"
	})
	if runtime.GetState().ActiveRuns["cold"] != nil {
		t.Fatal("the loading thread's event did not wait")
	}
	settledWithin(t, "cancel", completes(t, runtime.Execute(contracts.RunControlCommandRunCancel{TaskID: new("selected")})))
	close(history.loaded)
	settledWithin(t, "batch", batch)
	if record := application.RunRecord(runtime.GetState().ActiveRuns["cold"]); record == nil || record.RunID != "cold-run" {
		t.Fatal("the cold thread's event never applied")
	}
}

func TestInputsQueuedBehindAHistoryReadReportTheClosedRuntime(t *testing.T) {
	history := newSlowHistory()
	runtime := startedRuntime(t, &runtimeDesktop{loadThreadMessages: history.load})
	send := completes(t, runtime.Execute(contracts.TaskCommandTaskSend{TaskID: new("cold"), Text: new("hello")}))
	rename := completes(t, runtime.Execute(contracts.TaskCommandTaskRename{TaskID: "cold", Title: "Renamed"}))
	within(t, "the history read", history.loading)
	runtime.Dispose()
	close(history.loaded)
	for _, done := range []<-chan contracts.WorkspaceCommandResult{send, rename} {
		if refused, ok := settledWithin(t, "the refusal", done).(contracts.WorkspaceCommandResultFalse); !ok || refused.Message != closed {
			t.Fatalf("result = %+v", refused)
		}
	}
	if len(runtime.GetState().PendingRuns) != 0 {
		t.Fatal("a run started after the runtime closed")
	}
}
