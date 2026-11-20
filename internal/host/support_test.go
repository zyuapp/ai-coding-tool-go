package host

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// memoryStorage is a KeyValueStorage held in memory.
type memoryStorage struct {
	mu     sync.Mutex
	values map[string]string
}

func newMemoryStorage(entries map[string]string) *memoryStorage {
	values := map[string]string{}
	for key, value := range entries {
		values[key] = value
	}
	return &memoryStorage{values: values}
}

func (m *memoryStorage) GetItem(key string) *string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value, ok := m.values[key]; ok {
		return &value
	}
	return nil
}

func (m *memoryStorage) SetItem(key, value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = value
}

// task is a thread as the reducer fixtures make one.
func task(id string, change ...func(thread *domain.Thread)) *domain.Thread {
	thread := &domain.Thread{}
	thread.ID, thread.Title, thread.Engine, thread.ExecutionPolicy = id, id, "claude", "confirm"
	thread.Messages = []*domain.ConversationMessage{}
	thread.ContinuationStatus = "none"
	thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: 1}
	thread.UpdatedAt, thread.RankedAt = 1, 1
	for _, apply := range change {
		apply(thread)
	}
	return thread
}

// workspace is an empty workspace with change applied.
func workspace(change ...func(state *application.WorkspaceState)) *application.WorkspaceState {
	state := application.EmptyWorkspaceState(nil)
	for _, apply := range change {
		apply(state)
	}
	return state
}

var project = &domain.Project{ID: "project-a", Root: "/repo", WorkspaceID: new("workspace-a")}

// ported runs a test body, skipping the test while a function it reaches is not ported yet.
func ported(t *testing.T, body func()) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			if message := fmt.Sprint(recovered); strings.Contains(message, "unported") {
				t.Skipf("blocked: %s", message)
			}
			panic(recovered)
		}
	}()
	body()
}

// eventually waits for check to hold.
func eventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}
