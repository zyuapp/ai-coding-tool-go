package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func persistenceSnapshot(text string) *PersistenceState {
	thread := &domain.Thread{}
	thread.ID, thread.Title, thread.Engine, thread.ExecutionPolicy = "task-1", "Task", "claude", "confirm"
	thread.Messages = []*domain.ConversationMessage{{ID: "message-1", Kind: domain.ConversationMessageKindAssistant, Text: text, At: 1}}
	thread.ContinuationStatus = "none"
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.RankedAt, thread.UpdatedAt = 1, 1
	return &PersistenceState{Threads: []*domain.Thread{thread}, Subagents: map[string][]*domain.Subagent{}, SideChats: []*application.SideChat{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{}}
}

func withThreads(state *PersistenceState, threads ...*domain.Thread) *PersistenceState {
	next := *state
	next.Threads = threads
	return &next
}

func withTitle(thread *domain.Thread, title string) *domain.Thread {
	next := *thread
	next.Title = title
	return &next
}

func setPending(queue *PersistenceQueue, state *PersistenceState) {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	queue.Pending = state
}

func queueFields(queue *PersistenceQueue) (persisted, pending *PersistenceState, inFlight bool) {
	persistenceMu.Lock()
	defer persistenceMu.Unlock()
	return queue.Persisted, queue.Pending, queue.InFlight != nil
}

// blocker is a write that waits until released, then answers with err.
type blocker struct {
	release chan struct{}
	err     error
}

func newBlocker() *blocker { return &blocker{release: make(chan struct{})} }

func (b *blocker) wait() error {
	<-b.release
	return b.err
}

func (b *blocker) resolve(err error) {
	b.err = err
	close(b.release)
}

// recorder collects the deltas a persist function was handed.
type recorder struct {
	mu     sync.Mutex
	deltas []contracts.TaskStoreDelta
	write  chan struct{}
}

func newRecorder() *recorder { return &recorder{write: make(chan struct{}, 16)} }

func (r *recorder) add(delta contracts.TaskStoreDelta) int {
	r.mu.Lock()
	r.deltas = append(r.deltas, delta)
	count := len(r.deltas)
	r.mu.Unlock()
	r.write <- struct{}{}
	return count
}

func (r *recorder) all() []contracts.TaskStoreDelta {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contracts.TaskStoreDelta(nil), r.deltas...)
}

func drained(queue *PersistenceQueue, persist func(ctx context.Context, delta contracts.TaskStoreDelta) error) chan error {
	done := make(chan error, 1)
	go func() { done <- DrainLatestPersistence(context.Background(), queue, persist) }()
	return done
}

func waitFor1(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func result(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
		return nil
	}
}

func firstText(delta contracts.TaskStoreDelta) string {
	return delta.Tasks[0].Messages[0].Message.Text
}

func TestSlowPersistenceKeepsOnlyTheLatestSnapshotQueued(t *testing.T) {
	queue := &PersistenceQueue{Pending: persistenceSnapshot("one")}
	first := newBlocker()
	writes := newRecorder()
	draining := drained(queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		if writes.add(delta) == 1 {
			return first.wait()
		}
		return nil
	})
	waitFor1(t, writes.write)
	setPending(queue, persistenceSnapshot("one two"))
	setPending(queue, persistenceSnapshot("one two three"))
	flushing := drained(queue, func(context.Context, contracts.TaskStoreDelta) error {
		t.Error("a second drain must not start")
		return nil
	})
	time.Sleep(20 * time.Millisecond)
	select {
	case <-flushing:
		t.Fatal("the flush settled before the active write")
	default:
	}
	first.resolve(nil)
	if result(t, draining) != nil || result(t, flushing) != nil {
		t.Fatal("drains failed")
	}
	deltas := writes.all()
	if len(deltas) != 2 || firstText(deltas[1]) != "one two three" {
		t.Fatalf("writes = %d", len(deltas))
	}
}

func TestStartupBackfillPreservesAWorktreeCreatedBeforeTheStoreLoaded(t *testing.T) {
	current := persistenceSnapshot("working")
	current.Worktrees = []*domain.Worktree{{ID: "wt1", ProjectID: "project-1", Root: "/worktrees/repo-wt1", WorkspaceID: "workspace-wt1", BaseCommit: "abcdef1", CreatedAt: 1, LastUsedAt: 1}}
	delta := PersistenceDelta(PersistedStoreState(&domain.ThreadStoreData{Tasks: []*domain.StoredThread{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{}}), current)
	if !jsonx.SameSlice(delta.Worktrees, current.Worktrees) || len(delta.Tasks) == 0 || delta.Tasks[0].Task.ID != "task-1" {
		t.Fatalf("delta = %+v", delta)
	}
}

func TestAFailedWriteKeepsItsSnapshotPending(t *testing.T) {
	before, after := persistenceSnapshot("before"), persistenceSnapshot("after")
	queue := &PersistenceQueue{Persisted: before, Pending: after}
	err := DrainLatestPersistence(context.Background(), queue, func(context.Context, contracts.TaskStoreDelta) error { return errors.New("disk full") })
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	persisted, pending, inFlight := queueFields(queue)
	if persisted != before || pending != after || inFlight {
		t.Fatal("the failed write moved the queue")
	}
	var writes []contracts.TaskStoreDelta
	if err := DrainLatestPersistence(context.Background(), queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		writes = append(writes, delta)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	persisted, pending, _ = queueFields(queue)
	if persisted != after || pending != nil || firstText(writes[0]) != "after" {
		t.Fatal("the retry did not write the pending snapshot")
	}
}

func TestAFailedInFlightWriteRetainsANewerSnapshotAndRejectsEveryCaller(t *testing.T) {
	before, latest := persistenceSnapshot("before"), persistenceSnapshot("latest")
	first := newBlocker()
	started := make(chan struct{}, 1)
	queue := &PersistenceQueue{Persisted: before, Pending: persistenceSnapshot("middle")}
	draining := drained(queue, func(context.Context, contracts.TaskStoreDelta) error {
		started <- struct{}{}
		return first.wait()
	})
	waitFor1(t, started)
	setPending(queue, latest)
	flushing := drained(queue, func(context.Context, contracts.TaskStoreDelta) error {
		t.Error("concurrent writer")
		return nil
	})
	time.Sleep(20 * time.Millisecond)
	first.resolve(errors.New("disk full"))
	for _, done := range []chan error{draining, flushing} {
		if err := result(t, done); err == nil || err.Error() != "disk full" {
			t.Fatalf("err = %v", err)
		}
	}
	persisted, pending, _ := queueFields(queue)
	if persisted != before || pending != latest {
		t.Fatal("the failure moved the queue")
	}
	if err := DrainLatestPersistence(context.Background(), queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		if firstText(delta) != "latest" {
			t.Errorf("wrote %q", firstText(delta))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persisted, _, _ := queueFields(queue); persisted != latest {
		t.Fatal("latest was not persisted")
	}
}

func TestStartupPersistenceIncludesUpdatesArrivingDuringItsBackfill(t *testing.T) {
	initial, latest := persistenceSnapshot("initial"), persistenceSnapshot("latest")
	latest.Worktrees = []*domain.Worktree{{ID: "wt", ProjectID: "project", Root: "/worktrees/wt", WorkspaceID: "workspace", BaseCommit: "abc", CreatedAt: 1, LastUsedAt: 1}}
	queue := &PersistenceQueue{Persisted: PersistedStoreState(&domain.ThreadStoreData{}), Pending: initial}
	first := newBlocker()
	writes := newRecorder()
	draining := drained(queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		if writes.add(delta) == 1 {
			return first.wait()
		}
		return nil
	})
	waitFor1(t, writes.write)
	setPending(queue, latest)
	first.resolve(nil)
	if err := result(t, draining); err != nil {
		t.Fatal(err)
	}
	deltas := writes.all()
	if len(deltas) != 2 || firstText(deltas[1]) != "latest" || !jsonx.SameSlice(deltas[1].Worktrees, latest.Worktrees) {
		t.Fatalf("writes = %+v", deltas)
	}
	if persisted, _, _ := queueFields(queue); persisted != latest {
		t.Fatal("latest was not persisted")
	}
}

func TestMetadataEditsDoNotWriteHistory(t *testing.T) {
	before := persistenceSnapshot("old")
	metadata := withThreads(before, withTitle(before.Threads[0], "Renamed"))
	delta := PersistenceDelta(before, metadata)
	if delta.Tasks[0].Task.Title != "Renamed" || len(delta.Tasks[0].Messages) != 0 {
		t.Fatalf("delta = %+v", delta.Tasks[0])
	}
	same := *before
	if HasPersistenceChanges(before, &same) {
		t.Fatal("an unchanged snapshot has changes")
	}
	if delta := PersistenceDelta(before, &same); len(delta.Tasks) != 0 || HasPersistenceDelta(delta) {
		t.Fatalf("delta = %+v", delta)
	}
}

func TestMessageEditsBeforeTheLastArePersisted(t *testing.T) {
	before := persistenceSnapshot("first")
	thread := before.Threads[0]
	thread.Messages = append(thread.Messages, &domain.ConversationMessage{ID: "message-2", Kind: domain.ConversationMessageKindAssistant, Text: "last", At: 2})
	withdrawn := *thread.Messages[0]
	withdrawn.Withdrawn = new(true)
	edited := *thread
	edited.Messages = []*domain.ConversationMessage{&withdrawn, thread.Messages[1]}
	delta := PersistenceDelta(before, withThreads(before, &edited))
	if len(delta.Tasks[0].Messages) != 1 || delta.Tasks[0].Messages[0].Index != 0 || delta.Tasks[0].Messages[0].Message.Withdrawn == nil {
		t.Fatalf("messages = %+v", delta.Tasks[0].Messages)
	}
}

func TestSideChatsStayEphemeral(t *testing.T) {
	before := persistenceSnapshot("before")
	side := *persistenceSnapshot("private").Threads[0]
	side.ID = "side"
	next := withThreads(before, before.Threads[0], &side)
	next.SideChats = []*application.SideChat{{ID: "side", SourceThreadID: "task-1"}}
	empty := func(delta contracts.TaskStoreDelta) bool {
		return len(delta.Tasks) == 0 && delta.RemovedTasks == nil && delta.Projects == nil && delta.Worktrees == nil && delta.LastFolder.IsAbsent()
	}
	if !empty(PersistenceDelta(before, next)) {
		t.Fatal("an added side chat was written")
	}
	changed := withThreads(next, before.Threads[0], withTitle(&side, "Changed"))
	if !empty(PersistenceDelta(next, changed)) {
		t.Fatal("a changed side chat was written")
	}
	if !empty(PersistenceDelta(changed, before)) {
		t.Fatal("a removed side chat was written")
	}
}

func TestAnAppendedBlockWritesOnlyWhatChanged(t *testing.T) {
	before := persistenceSnapshot("old")
	messages := make([]*domain.ConversationMessage, 20_000)
	for index := range messages {
		messages[index] = &domain.ConversationMessage{ID: string(rune('a' + index%26)), Kind: domain.ConversationMessageKindAssistant, Text: "old", At: float64(index)}
	}
	before.Threads[0].Messages = messages
	appended := domain.AppendMessages(messages, []*domain.ConversationMessage{{ID: "new", Kind: domain.ConversationMessageKindAssistant, Text: "new", At: 20_000}})
	last := *appended[len(appended)-1]
	last.Text = "new block"
	replaced := domain.ReplaceLastMessage(appended, &last)
	thread := *before.Threads[0]
	thread.Messages = replaced
	next := withThreads(before, &thread)
	delta := PersistenceDelta(before, next)
	if len(delta.Tasks[0].Messages) != 1 || delta.Tasks[0].Messages[0].Index != 20_000 || delta.Tasks[0].Messages[0].Message.Text != "new block" {
		t.Fatalf("messages = %+v", delta.Tasks[0].Messages)
	}
	withdrawnThread := thread
	withdrawnThread.Messages = domain.WithdrawMessages(replaced, 19_999)
	var indexes []float64
	for _, message := range PersistenceDelta(before, withThreads(next, &withdrawnThread)).Tasks[0].Messages {
		indexes = append(indexes, message.Index)
	}
	if len(indexes) != 2 || indexes[0] != 19_999 || indexes[1] != 20_000 {
		t.Fatalf("indexes = %v", indexes)
	}
}

func unloadedSnapshot() (*PersistenceState, []*domain.ConversationMessage) {
	before := persistenceSnapshot("disk message")
	messages := before.Threads[0].Messages
	thread := *before.Threads[0]
	thread.Messages = []*domain.ConversationMessage{}
	thread.HistorySummary = &domain.StoredThreadHistorySummary{MessageCount: 1}
	return withThreads(before, &thread), messages
}

func TestHydrationSurvivesAnActiveWriteAndANewerQueuedSnapshot(t *testing.T) {
	before, messages := unloadedSnapshot()
	first := withThreads(before, withTitle(before.Threads[0], "First title"))
	queue := &PersistenceQueue{Persisted: before, Pending: first}
	blocked := newBlocker()
	writes := newRecorder()
	drain := drained(queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		if writes.add(delta) == 1 {
			return blocked.wait()
		}
		return nil
	})
	waitFor1(t, writes.write)
	setPending(queue, withThreads(first, withTitle(first.Threads[0], "Latest title")))
	AdoptPersistedMessages(queue, "task-1", messages)
	blocked.resolve(nil)
	if err := result(t, drain); err != nil {
		t.Fatal(err)
	}
	persisted, _, _ := queueFields(queue)
	if !jsonx.SameSlice(persisted.Threads[0].Messages, messages) || persisted.Threads[0].Title != "Latest title" || persisted.Threads[0].HistorySummary != nil {
		t.Fatalf("persisted = %+v", persisted.Threads[0])
	}
	for _, delta := range writes.all() {
		if len(delta.Tasks[0].Messages) != 0 {
			t.Fatal("hydrated history was written again")
		}
	}
}

func TestAFailedWriteRetainsTheHydratedHistory(t *testing.T) {
	before, messages := unloadedSnapshot()
	queue := &PersistenceQueue{Persisted: before, Pending: withThreads(before, withTitle(before.Threads[0], "Rename"))}
	blocked := newBlocker()
	started := make(chan struct{}, 1)
	drain := drained(queue, func(context.Context, contracts.TaskStoreDelta) error {
		started <- struct{}{}
		return blocked.wait()
	})
	waitFor1(t, started)
	AdoptPersistedMessages(queue, "task-1", messages)
	blocked.resolve(errors.New("disk full"))
	if err := result(t, drain); err == nil {
		t.Fatal("the write did not fail")
	}
	_, pending, _ := queueFields(queue)
	if !jsonx.SameSlice(pending.Threads[0].Messages, messages) {
		t.Fatal("the retained snapshot lost the hydrated history")
	}
	if err := DrainLatestPersistence(context.Background(), queue, func(_ context.Context, delta contracts.TaskStoreDelta) error {
		if len(delta.Tasks[0].Messages) != 0 {
			t.Error("hydrated history was written again")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if persisted, _, _ := queueFields(queue); persisted.Threads[0].Title != "Rename" {
		t.Fatal("the rename was not persisted")
	}
}
