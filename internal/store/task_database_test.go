package store

import (
	"database/sql"
	"encoding/json/jsontext"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const summaryTask = `{"id":"summary-thread","title":"Stored history","engine":"codex","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":100,"updatedAt":100}`

func openTest(t *testing.T, file string, options OpenOptions) *TaskDatabase {
	t.Helper()
	database, err := OpenTaskDatabase(file, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func raw(t *testing.T, file string) *sql.DB {
	t.Helper()
	registerDriver()
	db, err := sql.Open(driverName, file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func withMember[V ~string | ~[]byte](t *testing.T, value V, name string, member string) jsontext.Value {
	t.Helper()
	record, _, err := parseObject([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return record.with(name, jsontext.Value(member)).encode()
}

func messages(values ...string) []rowMessage {
	indexed := make([]rowMessage, len(values))
	for i, value := range values {
		indexed[i] = rowMessage{Index: float64(i), Message: jsontext.Value(value)}
	}
	return indexed
}

func field(t *testing.T, value jsontext.Value, name string) string {
	t.Helper()
	record, _, err := parseObject(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(record.get(name))
}

func persist(t *testing.T, database *TaskDatabase, delta rowDelta) {
	t.Helper()
	if err := database.Persist(delta); err != nil {
		t.Fatal(err)
	}
}

// loadedRows is a loaded store with each record encoded, as a row holds it.
type loadedRows struct {
	Tasks, Projects, Worktrees []jsontext.Value
	LastFolder                 *string
	UnloadedTaskIDs            []string
}

func load(t *testing.T, database *TaskDatabase, summariesOnly bool) *loadedRows {
	t.Helper()
	loaded, err := database.Load(summariesOnly)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		return nil
	}
	return &loadedRows{Tasks: encodeRows(t, loaded.Tasks), Projects: encodeRows(t, loaded.Projects), Worktrees: encodeRows(t, loaded.Worktrees), LastFolder: loaded.LastFolder, UnloadedTaskIDs: loaded.UnloadedTaskIDs}
}

func encodeRows[T any](t *testing.T, values []*T) []jsontext.Value {
	t.Helper()
	rows := make([]jsontext.Value, len(values))
	for i, value := range values {
		data, err := jsonx.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		rows[i] = data
	}
	return rows
}

func ids(t *testing.T, records []jsontext.Value) []string {
	t.Helper()
	var values []string
	for _, record := range records {
		id, _ := stringValue(jsontext.Value(field(t, record, "id")))
		values = append(values, id)
	}
	return values
}

func TestCodexCutoverKeepsClaudeDataAcrossRestarts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	claude := withMember(t, withMember(t, withMember(t, withMember(t, summaryTask, "id", `"claude"`), "engine", `"claude"`), "continuation", `{"provider":"codex","value":"old-shared-id"}`), "continuationStatus", `"available"`)
	message := `{"id":"kept","kind":"user","text":"Claude history","at":10}`
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask)}, {Task: claude, Messages: messages(message)}}})
	if err := database.CutOverCodexThreads(); err != nil {
		t.Fatal(err)
	}
	kept := load(t, database, false).Tasks
	if got := ids(t, kept); !slices.Equal(got, []string{"claude"}) {
		t.Fatalf("kept %v", got)
	}
	if got := field(t, kept[0], "messages"); got != "["+message+"]" {
		t.Fatalf("messages %s", got)
	}
	if got := field(t, kept[0], "continuationStatus"); got != `"none"` {
		t.Fatalf("continuationStatus %s", got)
	}
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: withMember(t, summaryTask, "id", `"private-codex"`)}}})
	database.Close()
	database = openTest(t, file, OpenOptions{CutOverCodexThreads: true})
	got := ids(t, load(t, database, false).Tasks)
	slices.Sort(got)
	if !slices.Equal(got, []string{"claude", "private-codex"}) {
		t.Fatalf("after restart %v", got)
	}
}

func TestSummariesCountHistoryWhileMessagesLoadOnDemand(t *testing.T) {
	database := openTest(t, filepath.Join(t.TempDir(), "tasks.sqlite"), OpenOptions{})
	stored := []string{
		`{"id":"first","kind":"user","text":"Hello","at":10}`,
		`{"id":"answer","kind":"assistant","text":"Answer","at":20,"attachments":["picture.png"]}`,
		`{"id":"quiet","kind":"assistant","text":"Nothing changed","at":30,"withdrawn":true}`,
	}
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask), Messages: messages(stored...)}}})
	summary := load(t, database, true)
	if !slices.Equal(summary.UnloadedTaskIDs, []string{"summary-thread"}) {
		t.Fatalf("unloaded %v", summary.UnloadedTaskIDs)
	}
	if got := field(t, summary.Tasks[0], "messages"); got != "[]" {
		t.Fatalf("messages %s", got)
	}
	if got := field(t, summary.Tasks[0], "historySummary"); got != `{"messageCount":3,"attachmentCount":1,"firstMessageAt":10,"lastAudibleAt":20}` {
		t.Fatalf("summary %s", got)
	}
	read := func() []string {
		values, err := database.LoadThreadMessages("summary-thread")
		if err != nil {
			t.Fatal(err)
		}
		var texts []string
		for _, value := range encodeRows(t, values) {
			texts = append(texts, string(value))
		}
		return texts
	}
	if got := read(); !sameJSON(t, "["+strings.Join(got, ",")+"]", "["+strings.Join(stored, ",")+"]") {
		t.Fatalf("messages %v", got)
	}
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: withMember(t, summaryTask, "title", `"Renamed before opening"`)}}})
	if got := read(); !sameJSON(t, "["+strings.Join(got, ",")+"]", "["+strings.Join(stored, ",")+"]") {
		t.Fatalf("a summary-only rename keeps all stored messages: %v", got)
	}
	withdrawn := `{"id":"answer","kind":"assistant","text":"Answer","at":20,"attachments":[],"withdrawn":true}`
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask), Messages: []rowMessage{{Index: 1, Message: jsontext.Value(withdrawn)}}}}})
	if got := field(t, load(t, database, true).Tasks[0], "historySummary"); got != `{"messageCount":3,"attachmentCount":0,"firstMessageAt":10,"lastAudibleAt":10}` {
		t.Fatalf("updating a message changes summary counts without duplicating it: %s", got)
	}
	persist(t, database, rowDelta{RemovedTasks: []string{"summary-thread"}})
	if got := load(t, database, true); got != nil {
		t.Fatalf("loaded %v", got)
	}
	if _, err := database.LoadThreadMessages("summary-thread"); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("err %v", err)
	}
}

func TestSearchMatchesStoredTextCaseInsensitivelyAndSkipsUnreadableRows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	message := func(id, text string) string {
		return `{"id":"` + id + `","kind":"assistant","text":` + string(jsString(text)) + `,"at":1}`
	}
	task := func(id string) jsontext.Value { return withMember(t, summaryTask, "id", string(jsString(id))) }
	persist(t, database, rowDelta{Tasks: []rowChange{
		{Task: jsontext.Value(summaryTask), Messages: messages(message("a", "Found the ÜBER Needle"), message("b", "needle again"))},
		{Task: task("other"), Messages: messages(message("c", "Nothing here"))},
		{Task: task("outside"), Messages: messages(message("d", "needle"))},
		{Task: task("broken"), Messages: messages(message("e", "needle"))},
	}})
	if _, err := raw(t, file).Exec("UPDATE messages SET data = ? WHERE task_id = 'broken'", "{broken json"); err != nil {
		t.Fatal(err)
	}
	search := func(text string, taskIDs ...string) []string {
		found, err := database.SearchThreadMessages(text, taskIDs)
		if err != nil {
			t.Fatal(err)
		}
		return found
	}
	if got := search("needle", "summary-thread", "other", "broken"); !slices.Equal(got, []string{"summary-thread"}) {
		t.Fatalf("found %v", got)
	}
	if got := search(JSLower("ÜBER needle"), "summary-thread"); !slices.Equal(got, []string{"summary-thread"}) {
		t.Fatalf("found %v", got)
	}
	if got := search("needle"); len(got) != 0 {
		t.Fatalf("found %v", got)
	}
}

func TestExistingDatabasesGainHistoryMetadata(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	previous := raw(t, file)
	for _, statement := range []string{
		"CREATE TABLE tasks (id TEXT PRIMARY KEY, data TEXT NOT NULL)",
		"CREATE TABLE messages (task_id TEXT NOT NULL, id TEXT NOT NULL, position INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY (task_id, id))",
	} {
		if _, err := previous.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := previous.Exec("INSERT INTO tasks VALUES (?, ?)", "summary-thread", summaryTask); err != nil {
		t.Fatal(err)
	}
	if _, err := previous.Exec("INSERT INTO messages VALUES (?, ?, ?, ?)", "summary-thread", "legacy", 0, `{"id":"legacy","kind":"assistant","text":"Quiet reply","at":5,"quiet":true}`); err != nil {
		t.Fatal(err)
	}
	previous.Close()
	for range 2 {
		database := openTest(t, file, OpenOptions{})
		if got := field(t, load(t, database, true).Tasks[0], "historySummary"); got != `{"messageCount":1,"attachmentCount":0,"firstMessageAt":5}` {
			t.Fatalf("summary %s", got)
		}
		database.Close()
	}
}

func TestUnreadableBodiesFailHydrationWithoutBlockingSummaries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask), Messages: messages(`{"id":"broken","kind":"user","text":"Saved","at":1}`)}}})
	if _, err := raw(t, file).Exec("UPDATE messages SET data = ?", "{broken json"); err != nil {
		t.Fatal(err)
	}
	if got := load(t, database, true).UnloadedTaskIDs; !slices.Equal(got, []string{"summary-thread"}) {
		t.Fatalf("unloaded %v", got)
	}
	_, err := database.LoadThreadMessages("summary-thread")
	if _, ok := err.(*SyntaxError); !ok {
		t.Fatalf("err %v", err)
	}
}

func TestMessagesAreUpdatedInPlace(t *testing.T) {
	database := openTest(t, filepath.Join(t.TempDir(), "tasks.sqlite"), OpenOptions{})
	task := `{"id":"task-1","title":"Render markdown","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":2,"updatedAt":2}`
	folder := "/work"
	persist(t, database, rowDelta{
		Projects:   []jsontext.Value{jsontext.Value(`{"id":"project-1","root":"/work"}`)},
		LastFolder: optionalString{Present: true, Value: &folder},
		Tasks:      []rowChange{{Task: jsontext.Value(task), Messages: messages(`{"id":"user-1","kind":"user","text":"Hello","at":1}`, `{"id":"assistant-1","kind":"assistant","text":"First","at":2}`)}},
	})
	persist(t, database, rowDelta{Tasks: []rowChange{{
		Task:     withMember(t, task, "updatedAt", "3"),
		Messages: []rowMessage{{Index: 1, Message: jsontext.Value(`{"id":"assistant-1","kind":"assistant","text":"First\nSecond","at":2}`)}},
	}}})
	loaded := load(t, database, false)
	if loaded.LastFolder == nil || *loaded.LastFolder != "/work" {
		t.Fatalf("lastFolder %v", loaded.LastFolder)
	}
	if got := string(joinArray(loaded.Projects)); got != `[{"id":"project-1","root":"/work"}]` {
		t.Fatalf("projects %s", got)
	}
	if got := field(t, loaded.Tasks[0], "messages"); got != `[{"id":"user-1","kind":"user","text":"Hello","at":1},{"id":"assistant-1","kind":"assistant","text":"First\nSecond","at":2}]` {
		t.Fatalf("messages %s", got)
	}
	if got := field(t, loaded.Tasks[0], "updatedAt"); got != "3" {
		t.Fatalf("updatedAt %s", got)
	}
}

func TestSubagentActivityKeepsRowsOfItsOwn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	task := `{"id":"task-1","title":"Delegate","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":2,"updatedAt":2}`
	subagent := `{"id":"agent-1","description":"Explore","status":"working","startedAt":1,"model":"claude-sonnet","effort":"high","prompt":"  Exact\nprompt  "}`
	persist(t, database, rowDelta{Tasks: []rowChange{{
		Task:      jsontext.Value(task),
		Subagents: []rowSubagent{{Index: 0, Subagent: jsontext.Value(subagent)}},
		Activity:  []rowActivity{{SubagentID: "agent-1", Index: 0, Item: jsontext.Value(`{"id":"activity-1","kind":"text","text":"Reading","at":1}`)}},
	}}})
	completed := withMember(t, withMember(t, subagent, "status", `"completed"`), "finishedAt", "4")
	persist(t, database, rowDelta{Tasks: []rowChange{{
		Task:      withMember(t, task, "updatedAt", "3"),
		Subagents: []rowSubagent{{Index: 0, Subagent: completed}},
		Activity:  []rowActivity{{SubagentID: "agent-1", Index: 1, Item: jsontext.Value(`{"id":"activity-2","kind":"tool","title":"Grep","text":"hits","at":2}`)}},
	}}})
	loaded := load(t, database, false)
	if got := field(t, loaded.Tasks[0], "subagents"); !sameJSON(t, got, `[{"id":"agent-1","description":"Explore","status":"completed","startedAt":1,"model":"claude-sonnet","effort":"high","prompt":"  Exact\nprompt  ","finishedAt":4,"activity":[]}]`) {
		t.Fatalf("subagents %s", got)
	}
	activity, err := database.SubagentActivity("task-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(t, encodeRows(t, activity)); !slices.Equal(got, []string{"activity-1", "activity-2"}) {
		t.Fatalf("activity %v", got)
	}
	var data string
	if err := raw(t, file).QueryRow("SELECT data FROM tasks WHERE id = 'task-1'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(data, "subagents") {
		t.Fatalf("task row %s", data)
	}
}

func TestEmbeddedSubagentsAreLiftedIntoRows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	legacy := openTest(t, file, OpenOptions{})
	activity := `[{"id":"activity-1","kind":"text","text":"Reading","at":1}]`
	task := `{"id":"task-1","title":"Delegate","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":2,"updatedAt":2,"subagents":[{"id":"agent-1","description":"Explore","status":"completed","startedAt":1,"finishedAt":2,"activity":` + activity + `}]}`
	persist(t, legacy, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(task)}}})
	legacy.Close()

	database := openTest(t, file, OpenOptions{})
	if got := field(t, load(t, database, false).Tasks[0], "subagents"); got != `[{"id":"agent-1","description":"Explore","status":"completed","startedAt":1,"finishedAt":2,"activity":[]}]` {
		t.Fatalf("subagents %s", got)
	}
	items, err := database.SubagentActivity("task-1", "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := string(joinArray(encodeRows(t, items))); got != activity {
		t.Fatalf("activity %s", got)
	}
	var data string
	if err := raw(t, file).QueryRow("SELECT data FROM tasks WHERE id = 'task-1'").Scan(&data); err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(task, `,"subagents":[{"id":"agent-1","description":"Explore","status":"completed","startedAt":1,"finishedAt":2,"activity":`+activity+`}]`, "", 1); data != want {
		t.Fatalf("task row %s", data)
	}
}

func TestAutomationsKeepOneRowPerTaskAndDropUnreadableRows(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	automation := `{"id":"automation-1","taskId":"task-1","prompt":"check whether the PR is approved","schedule":"* * * * *","paused":false,"createdAt":1,"updatedAt":2,"runCount":0}`
	save := func(database *TaskDatabase, value jsontext.Value) {
		if err := database.SaveAutomation(value); err != nil {
			t.Fatal(err)
		}
	}
	list := func(database *TaskDatabase) []jsontext.Value {
		automations, err := database.ListAutomations()
		if err != nil {
			t.Fatal(err)
		}
		return automations
	}
	save(database, jsontext.Value(automation))
	save(database, withMember(t, withMember(t, withMember(t, automation, "runCount", "7"), "lastStatus", `"succeeded"`), "lastRunAt", "8"))
	save(database, withMember(t, withMember(t, automation, "id", `"automation-2"`), "taskId", `"task-2"`))
	stored := list(database)
	if len(stored) != 2 || field(t, stored[0], "runCount") != "7" || field(t, stored[1], "taskId") != `"task-2"` {
		t.Fatalf("stored %s", joinArray(stored))
	}
	if err := database.SaveAutomation(withMember(t, automation, "id", `"duplicate"`)); err == nil || !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("a second automation for one thread is refused: %v", err)
	}
	if err := database.DeleteAutomation("automation-2"); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, list(database)); !slices.Equal(got, []string{"automation-1"}) {
		t.Fatalf("after delete %v", got)
	}
	database.Close()

	reopened := openTest(t, file, OpenOptions{})
	save(reopened, withMember(t, withMember(t, withMember(t, automation, "id", `"automation-3"`), "taskId", `"task-3"`), "schedule", "42"))
	if got := ids(t, list(reopened)); !slices.Equal(got, []string{"automation-1"}) {
		t.Fatalf("a row with the wrong shape never reaches the scheduler: %v", got)
	}
	if _, err := raw(t, file).Exec("INSERT INTO automations (id, task_id, data) VALUES (?, ?, ?)", "automation-4", "task-4", "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := ids(t, list(reopened)); !slices.Equal(got, []string{"automation-1"}) {
		t.Fatalf("unparseable JSON is skipped rather than thrown: %v", got)
	}
}

func TestFoldersInsideWorktreeRootsAreRefusedAndTheRestLands(t *testing.T) {
	directory := t.TempDir()
	worktreesRoot := filepath.Join(directory, "worktrees")
	legacyRoot := filepath.Join(directory, "legacy-worktrees")
	database := openTest(t, filepath.Join(directory, "tasks.sqlite"), OpenOptions{WorktreesRoots: []string{worktreesRoot, legacyRoot}})
	task := `{"id":"task-1","title":"Worktree work","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"rankedAt":2,"updatedAt":2}`
	work := "/work"
	persist(t, database, rowDelta{Projects: []jsontext.Value{jsontext.Value(`{"id":"project-1","root":"/work"}`)}, LastFolder: optionalString{Present: true, Value: &work}, Tasks: []rowChange{{Task: jsontext.Value(task)}}})
	inside := filepath.Join(legacyRoot, "work-9aefd881")
	persist(t, database, rowDelta{
		Projects:   []jsontext.Value{withMember(t, `{"id":"project-1"}`, "root", string(jsString(filepath.Join(worktreesRoot, "work-9aefd881"))))},
		LastFolder: optionalString{Present: true, Value: &inside},
		Tasks:      []rowChange{{Task: withMember(t, task, "updatedAt", "3"), Messages: messages(`{"id":"user-1","kind":"user","text":"Still saved","at":1}`)}},
	})
	loaded := load(t, database, false)
	if got := string(joinArray(loaded.Projects)); got != `[{"id":"project-1","root":"/work"}]` {
		t.Fatalf("the folder on disk outlives a bad write: %s", got)
	}
	if *loaded.LastFolder != "/work" {
		t.Fatalf("the root the app used before is refused just the same: %s", *loaded.LastFolder)
	}
	if got := field(t, loaded.Tasks[0], "updatedAt"); got != "3" {
		t.Fatalf("the transcript still saves: %s", got)
	}

	sibling := worktreesRoot + "-mine"
	persist(t, database, rowDelta{Projects: []jsontext.Value{withMember(t, `{"id":"project-1"}`, "root", string(jsString(sibling)))}})
	if got := field(t, load(t, database, false).Projects[0], "root"); got != string(jsString(sibling)) {
		t.Fatalf("a folder that merely starts with the root's name is allowed: %s", got)
	}
}

func TestEmbeddedCheckoutIsLiftedIntoItsOwnRecord(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	checkout := `"id":"wt1","root":"/worktrees/repo-wt1","workspaceId":"workspace-wt1","baseCommit":"abcdef1","createdAt":1,"lastUsedAt":2`
	stored := `{"id":"task-1","title":"Older thread","projectId":"project-1","engine":"claude","executionPolicy":"confirm","continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"updatedAt":2,"worktree":{` + checkout + `,"enteredAt":3}}`
	before := raw(t, file)
	if _, err := before.Exec("CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, data TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := before.Exec("INSERT INTO tasks (id, data) VALUES (?, ?)", "task-1", stored); err != nil {
		t.Fatal(err)
	}
	before.Close()

	database := openTest(t, file, OpenOptions{})
	persist(t, database, rowDelta{Projects: []jsontext.Value{jsontext.Value(`{"id":"project-1","root":"/repo"}`)}})
	loaded := load(t, database, false)
	if got := string(joinArray(loaded.Worktrees)); !sameJSON(t, got, `[{`+checkout+`,"projectId":"project-1"}]`) {
		t.Fatalf("the checkout is filed under the project its thread was in: %s", got)
	}
	task, _, _ := parseObject(loaded.Tasks[0])
	if string(task.get("worktreeId")) != `"wt1"` || string(task.get("worktreeEnteredAt")) != "3" || task.has("worktree") {
		t.Fatalf("task %s", loaded.Tasks[0])
	}
}

func TestWithdrawnMessagesAreQuietUnderEitherName(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tasks.sqlite")
	database := openTest(t, file, OpenOptions{})
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask), Messages: messages(
		`{"id":"label","kind":"user","text":"Poll","detail":"Automation run #2","withdrawn":true,"at":10}`,
		`{"id":"reply","kind":"assistant","text":"Nothing new","quiet":true,"at":11}`,
		`{"id":"spoken","kind":"assistant","text":"Something new","withdrawn":false,"quiet":true,"at":12}`,
	)}}})
	rows, err := raw(t, file).Query("SELECT audible FROM messages ORDER BY position")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var audible []int
	for rows.Next() {
		var value int
		rows.Scan(&value)
		audible = append(audible, value)
	}
	if !slices.Equal(audible, []int{0, 0, 1}) {
		t.Fatalf("audible %v", audible)
	}
}

func TestAttachmentPathsNameEveryImageOnce(t *testing.T) {
	database := openTest(t, filepath.Join(t.TempDir(), "tasks.sqlite"), OpenOptions{})
	persist(t, database, rowDelta{Tasks: []rowChange{{Task: jsontext.Value(summaryTask), Messages: messages(
		`{"id":"first","kind":"user","text":"Look","attachments":["/data/attachments/a.png","/data/attachments/b.png"],"at":10}`,
		`{"id":"second","kind":"user","text":"Again","attachments":["/data/attachments/a.png"],"at":11}`,
		`{"id":"third","kind":"assistant","text":"Seen","at":12}`,
	)}}})
	paths, err := database.AttachmentPaths()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	if !slices.Equal(paths, []string{"/data/attachments/a.png", "/data/attachments/b.png"}) {
		t.Fatalf("paths %v", paths)
	}
	persist(t, database, rowDelta{RemovedTasks: []string{"summary-thread"}})
	if paths, _ := database.AttachmentPaths(); len(paths) != 0 {
		t.Fatalf("paths %v", paths)
	}
}

func TestTypedDeltasEncodeOnTheStoresGoroutine(t *testing.T) {
	database := openTest(t, filepath.Join(t.TempDir(), "tasks.sqlite"), OpenOptions{})
	type message struct {
		ID   string  `json:"id"`
		Kind string  `json:"kind"`
		Text string  `json:"text"`
		At   float64 `json:"at"`
	}
	type indexed struct {
		Index   float64 `json:"index"`
		Message message `json:"message"`
	}
	type change struct {
		Task     map[string]any `json:"task"`
		Messages []indexed      `json:"messages"`
	}
	type delta struct {
		Tasks    []change `json:"tasks"`
		Projects []any    `json:"projects"`
	}
	var typedTask map[string]any
	if err := jsonx.Unmarshal(withMember(t, summaryTask, "id", `"typed"`), &typedTask); err != nil {
		t.Fatal(err)
	}
	if err := database.Persist(delta{Tasks: []change{{
		Task:     typedTask,
		Messages: []indexed{{Index: 0, Message: message{ID: "m", Kind: "user", Text: "<b> & é", At: 1}}},
	}}, Projects: []any{}}); err != nil {
		t.Fatal(err)
	}
	loaded := load(t, database, false)
	if got := field(t, loaded.Tasks[0], "messages"); got != `[{"id":"m","kind":"user","text":"<b> & é","at":1}]` {
		t.Fatalf("messages %s", got)
	}
}

func TestOpeningInAMissingFolderFails(t *testing.T) {
	_, err := OpenTaskDatabase(filepath.Join(t.TempDir(), "missing", "tasks.sqlite"), OpenOptions{})
	if err == nil || !strings.Contains(err.Error(), "unable to open database") {
		t.Fatalf("err %v", err)
	}
}

// sameJSON is whether two JSON texts hold the same values, whatever order their keys are in.
func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var left, right any
	if err := jsonx.Unmarshal([]byte(a), &left); err != nil {
		t.Fatal(err)
	}
	if err := jsonx.Unmarshal([]byte(b), &right); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(left, right)
}
