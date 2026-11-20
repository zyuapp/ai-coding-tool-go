package store

import (
	"database/sql"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Round-trips a copy of a real tasks.v3.sqlite named by ACT_STORE_FIXTURE. The fixture is copied
// before it is opened, so the file named is never written.

func fixtureCopy(t *testing.T) string {
	source := os.Getenv("ACT_STORE_FIXTURE")
	if source == "" {
		t.Skip("ACT_STORE_FIXTURE names a copy of tasks.v3.sqlite")
	}
	file := filepath.Join(t.TempDir(), TaskDatabaseFile)
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(source + suffix)
		if err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if err := os.WriteFile(file+suffix, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return file
}

const tableDump = `
	SELECT 'tasks', id, '', '', data || '|' || message_count || '|' || attachment_count FROM tasks
	UNION ALL SELECT 'messages', task_id, id, position, data || '|' || quote(message_at) || '|' || audible || '|' || has_attachment FROM messages WHERE task_id IN (SELECT id FROM tasks)
	UNION ALL SELECT 'subagents', task_id, id, position, data FROM subagents WHERE task_id IN (SELECT id FROM tasks)
	UNION ALL SELECT 'subagent_activity', task_id, subagent_id || '/' || id, position, data FROM subagent_activity WHERE task_id IN (SELECT id FROM tasks)
	UNION ALL SELECT 'projects', id, '', '', data FROM projects
	UNION ALL SELECT 'worktrees', id, '', '', data FROM worktrees
	UNION ALL SELECT 'automations', id, task_id, '', data FROM automations
	UNION ALL SELECT 'settings', key, '', '', value FROM settings WHERE key = 'lastFolder'
	ORDER BY 1, 2, 3`

// dump reads every stored row a thread still owns, so two stores can be compared.
func dump(t *testing.T, file string) []string {
	t.Helper()
	db := raw(t, file)
	defer db.Close()
	rows, err := db.Query(tableDump)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var table, key, sub, position, data sql.NullString
		if err := rows.Scan(&table, &key, &sub, &position, &data); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s %s", table.String, key.String, sub.String, position.String, data.String))
	}
	return lines
}

func TestRealDatabaseRoundTrips(t *testing.T) {
	file := fixtureCopy(t)
	before := dump(t, file)
	service, err := OpenTaskDatabaseService(file, OpenOptions{CutOverCodexThreads: true})
	if err != nil {
		t.Fatal(err)
	}
	full, err := service.Load().Wait()
	if err != nil || full == nil {
		t.Fatalf("load %v %v", full, err)
	}
	summaries, err := service.LoadSummaries().Wait()
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Tasks) != len(summaries.Tasks) {
		t.Fatalf("%d threads, %d summaries", len(full.Tasks), len(summaries.Tasks))
	}
	t.Logf("%d threads, %d projects, %d worktrees, %d unloaded", len(full.Tasks), len(full.Projects), len(full.Worktrees), len(summaries.UnloadedTaskIDs))

	rewritten := filepath.Join(filepath.Dir(file), "rewritten.sqlite")
	target, err := OpenTaskDatabaseService(rewritten, OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	delta := rowDelta{Projects: encodeRows(t, full.Projects), Worktrees: encodeRows(t, full.Worktrees), LastFolder: optionalString{Present: true, Value: full.LastFolder}}
	for i, value := range encodeRows(t, full.Tasks) {
		task, _, err := parseObject(value)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := stringValue(task.get("id"))
		typedMessages, err := service.LoadThreadMessages(id).Wait()
		if err != nil {
			t.Fatal(err)
		}
		stored := encodeRows(t, typedMessages)
		loaded, _ := arrayItems(task.get("messages"))
		if string(joinArray(stored)) != string(joinArray(loaded)) {
			t.Fatalf("thread %s reads differently on its own", id)
		}
		if count := summaries.Tasks[i].HistorySummary; count != nil {
			if int(count.MessageCount) != len(stored) {
				t.Fatalf("thread %s counts %v messages, holds %d", id, count.MessageCount, len(stored))
			}
		} else if len(stored) != 0 {
			t.Fatalf("thread %s has messages and no summary", id)
		}
		change := rowChange{Task: task.without("messages").without("subagents").encode()}
		for index, message := range stored {
			change.Messages = append(change.Messages, rowMessage{Index: float64(index), Message: message})
		}
		subagents, _ := arrayItems(task.get("subagents"))
		for index, value := range subagents {
			subagent, _, _ := parseObject(value)
			subagentID, _ := stringValue(subagent.get("id"))
			change.Subagents = append(change.Subagents, rowSubagent{Index: float64(index), Subagent: subagent.without("activity").encode()})
			items, err := service.SubagentActivity(id, subagentID).Wait()
			if err != nil {
				t.Fatal(err)
			}
			for position, item := range encodeRows(t, items) {
				change.Activity = append(change.Activity, rowActivity{SubagentID: subagentID, Index: float64(position), Item: item})
			}
		}
		delta.Tasks = append(delta.Tasks, change)
	}
	if _, err := target.Persist(typedDelta(t, delta)).Wait(); err != nil {
		t.Fatal(err)
	}
	automations, err := service.ListAutomations().Wait()
	if err != nil {
		t.Fatal(err)
	}
	for _, automation := range automations {
		if _, err := target.SaveAutomation(automation).Wait(); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := target.LoadSummaries().Wait()
	if err != nil {
		t.Fatal(err)
	}
	if string(joinArray(encodeRows(t, reloaded.Tasks))) != string(joinArray(encodeRows(t, summaries.Tasks))) {
		t.Fatal("the rewritten store loads different summaries")
	}
	if string(joinArray(encodeRows(t, reloaded.Projects))) != string(joinArray(encodeRows(t, summaries.Projects))) {
		t.Fatal("the rewritten store orders projects differently")
	}
	rewrittenFull, err := target.Load().Wait()
	if err != nil {
		t.Fatal(err)
	}
	if string(joinArray(encodeRows(t, rewrittenFull.Tasks))) != string(joinArray(encodeRows(t, full.Tasks))) {
		t.Fatal("the rewritten store loads different threads")
	}
	originalPaths, _ := service.AttachmentPaths().Wait()
	copiedPaths, _ := target.AttachmentPaths().Wait()
	slices.Sort(originalPaths)
	slices.Sort(copiedPaths)
	if !slices.Equal(originalPaths, copiedPaths) {
		t.Fatal("the rewritten store names different attachments")
	}
	for _, closing := range []*Pending[struct{}]{target.Close(), service.Close()} {
		if _, err := closing.Wait(); err != nil {
			t.Fatal(err)
		}
	}

	if after := dump(t, file); !slices.Equal(before, after) {
		t.Fatal("opening and reading the store changed a row")
	}
	written := dump(t, rewritten)
	if len(before) != len(written) {
		t.Fatalf("%d rows stored, %d rewritten", len(before), len(written))
	}
	for i := range before {
		if !sameRow(t, before[i], written[i]) {
			t.Fatalf("first difference:\n%s\n%s", before[i], written[i])
		}
	}
	t.Logf("%d rows alike", len(written))
}

// sameRow is whether two dumped rows hold the same keys and the same columns after their JSON. The
// JSON itself is compared through what each store loads, since reading migrates older records.
func sameRow(t *testing.T, a, b string) bool {
	t.Helper()
	left, right := strings.SplitN(a, " ", 5), strings.SplitN(b, " ", 5)
	if len(left) != 5 || len(right) != 5 || !slices.Equal(left[:4], right[:4]) {
		return a == b
	}
	rest := func(data string) string {
		decoder := jsontext.NewDecoder(strings.NewReader(data))
		if _, err := decoder.ReadValue(); err != nil {
			t.Fatal(err)
		}
		return data[decoder.InputOffset():]
	}
	return rest(left[4]) == rest(right[4])
}
