package store

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

const workerTask = `{"id":"task","title":"Worker storage","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":1,"updatedAt":%d}`

func TestServiceCloseDrainsQueuedWritesInOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	service, err := OpenTaskDatabaseService(file, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var writes []*Pending[struct{}]
	for index := range 40 {
		writes = append(writes, service.Persist(TaskStoreDelta{Tasks: []TaskChange{{
			Task:     jsontext.Value(fmt.Sprintf(workerTask, index)),
			Messages: []IndexedMessage{{Index: float64(index), Message: jsontext.Value(fmt.Sprintf(`{"id":"message-%d","kind":"assistant","text":"Reply %d","at":%d}`, index, index, index))}},
		}}}))
	}
	closed := service.Close()
	if service.Close() != closed {
		t.Fatal("a second close shares the first")
	}
	if _, err := service.Persist(TaskStoreDelta{}).Wait(); !errors.Is(err, ErrClosing) {
		t.Fatalf("a write after close: %v", err)
	}
	for _, write := range append(writes, closed) {
		if _, err := write.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenTaskDatabaseService(file, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { reopened.Close().Wait() }()
	summaries, err := reopened.LoadSummaries().Wait()
	if err != nil {
		t.Fatal(err)
	}
	if got := field(t, summaries.Tasks[0], "historySummary"); !strings.HasPrefix(got, `{"messageCount":40,`) {
		t.Fatalf("summary %s", got)
	}
	messages, err := reopened.LoadThreadMessages("task").Wait()
	if err != nil || len(messages) != 40 || field(t, messages[39], "text") != `"Reply 39"` {
		t.Fatalf("messages %d %v", len(messages), err)
	}
	full, _ := reopened.Load().Wait()
	if got := field(t, full.Tasks[0], "updatedAt"); got != "39" {
		t.Fatalf("updatedAt %s", got)
	}
}

func TestServiceReturnsWriteFailuresAndKeepsServing(t *testing.T) {
	service, err := OpenTaskDatabaseService(filepath.Join(t.TempDir(), "tasks.sqlite"), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { service.Close().Wait() }()
	automation := `{"id":"a","taskId":"task","prompt":"Poll","schedule":"0 * * * *","paused":false,"runCount":0,"createdAt":1,"updatedAt":1}`
	if _, err := service.SaveAutomation(jsontext.Value(automation)).Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveAutomation(jsontext.Value(strings.Replace(automation, `"id":"a"`, `"id":"duplicate"`, 1))).Wait(); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("duplicate: %v", err)
	}
	listed, err := service.ListAutomations().Wait()
	if err != nil || len(listed) != 1 || string(listed[0]) != automation {
		t.Fatalf("listed %s %v", joinArray(listed), err)
	}
	service.DeleteAutomation("a")
	service.Flush().Wait()
	if listed, _ := service.ListAutomations().Wait(); len(listed) != 0 {
		t.Fatalf("listed %s", joinArray(listed))
	}
}
