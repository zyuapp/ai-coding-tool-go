package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/mattn/go-sqlite3"
)

// TaskDatabaseFile is the profile's thread store.
const TaskDatabaseFile = "tasks.v3.sqlite"

const driverName = "sqlite3_aicodingtool"

var registerDriver = sync.OnceFunc(func() {
	sql.Register(driverName, &sqlite3.SQLiteDriver{
		ConnectHook: func(connection *sqlite3.SQLiteConn) error {
			return connection.RegisterFunc("js_lower", jsLowerSQL, true)
		},
	})
})

func jsLowerSQL(value any) any {
	if text, ok := value.(string); ok {
		return JSLower(text)
	}
	return nil
}

const schema = `
      PRAGMA journal_mode = WAL;
      PRAGMA synchronous = NORMAL;
      PRAGMA foreign_keys = ON;
      CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY, position INTEGER NOT NULL, data TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, data TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS worktrees (id TEXT PRIMARY KEY, data TEXT NOT NULL);
      CREATE TABLE IF NOT EXISTS messages (
        task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
        id TEXT NOT NULL,
        position INTEGER NOT NULL,
        data TEXT NOT NULL,
        PRIMARY KEY (task_id, id)
      );
      CREATE INDEX IF NOT EXISTS messages_task_position ON messages(task_id, position);
      CREATE TABLE IF NOT EXISTS subagents (
        task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
        id TEXT NOT NULL,
        position INTEGER NOT NULL,
        data TEXT NOT NULL,
        PRIMARY KEY (task_id, id)
      );
      CREATE INDEX IF NOT EXISTS subagents_task_position ON subagents(task_id, position);
      CREATE TABLE IF NOT EXISTS subagent_activity (
        task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
        subagent_id TEXT NOT NULL,
        id TEXT NOT NULL,
        position INTEGER NOT NULL,
        data TEXT NOT NULL,
        PRIMARY KEY (task_id, subagent_id, id)
      );
      CREATE INDEX IF NOT EXISTS subagent_activity_position ON subagent_activity(task_id, subagent_id, position);
      CREATE TABLE IF NOT EXISTS automations (id TEXT PRIMARY KEY, task_id TEXT NOT NULL UNIQUE, data TEXT NOT NULL);
    `

const historyColumns = `
          ALTER TABLE messages ADD COLUMN message_at REAL;
          ALTER TABLE messages ADD COLUMN audible INTEGER NOT NULL DEFAULT 1;
          ALTER TABLE messages ADD COLUMN has_attachment INTEGER NOT NULL DEFAULT 0;
          ALTER TABLE tasks ADD COLUMN message_count INTEGER NOT NULL DEFAULT 0;
          ALTER TABLE tasks ADD COLUMN attachment_count INTEGER NOT NULL DEFAULT 0;
          UPDATE messages SET
            message_at = CASE WHEN json_valid(data) THEN json_extract(data, '$.at') END,
            audible = CASE WHEN json_valid(data) THEN CASE WHEN coalesce(json_extract(data, '$.withdrawn'), json_extract(data, '$.quiet'), 0) = 1 THEN 0 ELSE 1 END ELSE 1 END,
            has_attachment = CASE WHEN json_valid(data) THEN CASE WHEN json_array_length(data, '$.attachments') > 0 THEN 1 ELSE 0 END ELSE 0 END;
          UPDATE tasks SET
            message_count = (SELECT count(*) FROM messages WHERE task_id = tasks.id),
            attachment_count = (SELECT coalesce(sum(has_attachment), 0) FROM messages WHERE task_id = tasks.id);
        `

const historyTriggers = `
        CREATE INDEX IF NOT EXISTS messages_task_audible_position ON messages(task_id, audible, position);
        CREATE TRIGGER IF NOT EXISTS messages_summary_insert AFTER INSERT ON messages BEGIN
          UPDATE tasks SET message_count = message_count + 1, attachment_count = attachment_count + NEW.has_attachment WHERE id = NEW.task_id;
        END;
        CREATE TRIGGER IF NOT EXISTS messages_summary_update AFTER UPDATE OF has_attachment ON messages BEGIN
          UPDATE tasks SET attachment_count = attachment_count + NEW.has_attachment - OLD.has_attachment WHERE id = NEW.task_id;
        END;
        CREATE TRIGGER IF NOT EXISTS messages_summary_delete AFTER DELETE ON messages BEGIN
          UPDATE tasks SET message_count = message_count - 1, attachment_count = attachment_count - OLD.has_attachment WHERE id = OLD.task_id;
        END;
      `

const (
	saveTaskSQL     = "INSERT INTO tasks (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data"
	saveMessageSQL  = "INSERT INTO messages (task_id, id, position, data, message_at, audible, has_attachment) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_id, id) DO UPDATE SET position = excluded.position, data = excluded.data, message_at = excluded.message_at, audible = excluded.audible, has_attachment = excluded.has_attachment"
	saveSubagentSQL = "INSERT INTO subagents (task_id, id, position, data) VALUES (?, ?, ?, ?) ON CONFLICT(task_id, id) DO UPDATE SET position = excluded.position, data = excluded.data"
	saveActivitySQL = "INSERT INTO subagent_activity (task_id, subagent_id, id, position, data) VALUES (?, ?, ?, ?, ?) ON CONFLICT(task_id, subagent_id, id) DO UPDATE SET position = excluded.position, data = excluded.data"
	searchSQL       = `
      SELECT DISTINCT task_id FROM messages
      WHERE task_id IN (SELECT value FROM json_each(?))
        AND instr(js_lower(CASE WHEN json_valid(data) THEN json_extract(data, '$.text') END), ?) > 0
    `
	historySummariesSQL = `
        SELECT id, message_count, attachment_count,
          (SELECT message_at FROM messages WHERE task_id = tasks.id ORDER BY position LIMIT 1) AS first_at,
          (SELECT message_at FROM messages WHERE task_id = tasks.id AND audible = 1 ORDER BY position DESC LIMIT 1) AS last_at
        FROM tasks WHERE message_count > 0
      `
	privateCodexHomeMarker = "privateCodexHome.v2"
)

// ErrThreadGone answers a read of a thread the store no longer holds.
var ErrThreadGone = errors.New("Thread no longer exists.")

// OpenOptions is how a profile's store is opened.
type OpenOptions struct {
	// WorktreesRoots are every root the app's checkouts live under; no project folder may be
	// recorded inside one.
	WorktreesRoots []string
	// CutOverCodexThreads runs the one-time cutover to Codex's private home, as the worker did when
	// PRIVATE_CODEX_HOME_ENV was set.
	CutOverCodexThreads bool
	Domain              Domain
	// JSON encodes deltas and automations handed over as typed values.
	JSON []json.Options
}

// TaskDatabase is the SQLite thread store, schema-compatible with the TypeScript build's
// tasks.v3.sqlite. It is not safe for concurrent use; TaskDatabaseService runs it on one goroutine.
type TaskDatabase struct {
	db             *sql.DB
	conn           *sql.Conn
	worktreesRoots []string
	domain         Domain
	jsonOptions    []json.Options
	saveTask       *sql.Stmt
	saveMessage    *sql.Stmt
	saveSubagent   *sql.Stmt
	saveActivity   *sql.Stmt
	searchMessages *sql.Stmt
	closed         bool
}

var background = context.Background()

// OpenTaskDatabase opens or creates the store at file, bringing an older one up to the current
// shape. The folder must already exist.
func OpenTaskDatabase(file string, options OpenOptions) (*TaskDatabase, error) {
	registerDriver()
	db, err := sql.Open(driverName, "file:"+(&url.URL{Path: file}).EscapedPath())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(background)
	if err != nil {
		db.Close()
		return nil, err
	}
	database := &TaskDatabase{db: db, conn: conn, worktreesRoots: options.WorktreesRoots, domain: options.Domain, jsonOptions: options.JSON}
	if err := database.open(options); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func (d *TaskDatabase) open(options OpenOptions) error {
	if _, err := d.conn.ExecContext(background, schema); err != nil {
		return err
	}
	if err := d.prepareHistoryMetadata(); err != nil {
		return err
	}
	for _, statement := range []struct {
		target **sql.Stmt
		sql    string
	}{
		{&d.saveTask, saveTaskSQL},
		{&d.saveMessage, saveMessageSQL},
		{&d.saveSubagent, saveSubagentSQL},
		{&d.saveActivity, saveActivitySQL},
		{&d.searchMessages, searchSQL},
	} {
		prepared, err := d.conn.PrepareContext(background, statement.sql)
		if err != nil {
			return err
		}
		*statement.target = prepared
	}
	if err := d.liftEmbeddedSubagents(); err != nil {
		return err
	}
	if err := d.liftEmbeddedWorktrees(); err != nil {
		return err
	}
	if options.CutOverCodexThreads {
		return d.CutOverCodexThreads()
	}
	return nil
}

// transaction runs work inside BEGIN IMMEDIATE, rolling back when it fails.
func (d *TaskDatabase) transaction(work func() error) error {
	if _, err := d.conn.ExecContext(background, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := work(); err != nil {
		if _, rollback := d.conn.ExecContext(background, "ROLLBACK"); rollback != nil {
			return errors.Join(err, rollback)
		}
		return err
	}
	if _, err := d.conn.ExecContext(background, "COMMIT"); err != nil {
		d.conn.ExecContext(background, "ROLLBACK")
		return err
	}
	return nil
}

func (d *TaskDatabase) exec(query string, args ...any) error {
	_, err := d.conn.ExecContext(background, query, args...)
	return err
}

// Counts and indexed timestamps let the thread list load without reading conversation bodies.
func (d *TaskDatabase) prepareHistoryMetadata() error {
	rows, err := d.conn.QueryContext(background, "SELECT name FROM pragma_table_info('messages')")
	if err != nil {
		return err
	}
	migrated := false
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		migrated = migrated || name == "message_at"
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return d.transaction(func() error {
		if !migrated {
			if err := d.exec(historyColumns); err != nil {
				return err
			}
		}
		return d.exec(historyTriggers)
	})
}

type taskRow struct {
	id   string
	task object
}

// taskRows reads every task record, failing as JSON.parse would on one that is not JSON.
func (d *TaskDatabase) taskRows() ([]taskRow, error) {
	rows, err := d.conn.QueryContext(background, "SELECT id, data FROM tasks")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []taskRow
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		task, ok, err := parseObject(data)
		if err != nil {
			return nil, err
		}
		// Spreading null throws; spreading any other value that is not an object gives {}.
		if !ok && string(data) != "null" {
			task = object{}
		}
		tasks = append(tasks, taskRow{id: id, task: task})
	}
	return tasks, rows.Err()
}

// Tasks written before subagents had rows of their own still carry them inside the task record.
func (d *TaskDatabase) liftEmbeddedSubagents() error {
	tasks, err := d.taskRows()
	if err != nil {
		return err
	}
	stale := slices.DeleteFunc(tasks, func(row taskRow) bool { return !truthy(row.task.get("subagents")) })
	if len(stale) == 0 {
		return nil
	}
	return d.transaction(func() error {
		for _, row := range stale {
			subagents, ok := arrayItems(row.task.get("subagents"))
			if !ok {
				return fmt.Errorf("Task %s has unreadable subagents.", row.id)
			}
			for index, subagent := range subagents {
				if err := d.writeSubagent(row.id, index, subagent); err != nil {
					return err
				}
			}
			if err := d.exec("UPDATE tasks SET data = ? WHERE id = ?", string(row.task.without("subagents").encode()), row.id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *TaskDatabase) writeSubagent(taskID string, index int, value jsontext.Value) error {
	subagent, isObject, err := parseObject(value)
	if err != nil {
		return err
	}
	if !isObject {
		subagent = object{}
	}
	id, err := bindable(subagent.get("id"))
	if err != nil {
		return err
	}
	if _, err := d.saveSubagent.ExecContext(background, taskID, id, index, string(subagent.without("activity").encode())); err != nil {
		return err
	}
	activity := subagent.get("activity")
	if activity == nil {
		return nil
	}
	items, ok := arrayItems(activity)
	if !ok {
		return fmt.Errorf("Subagent %v has unreadable activity.", id)
	}
	for position, item := range items {
		itemID, err := memberBinding(item, "id")
		if err != nil {
			return err
		}
		if _, err := d.saveActivity.ExecContext(background, taskID, id, itemID, position, string(item)); err != nil {
			return err
		}
	}
	return nil
}

// Tasks written while a checkout belonged to exactly one thread carry it inside themselves. Giving
// each checkout a row of its own is what lets a second thread claim it; the fork the thread had
// already made stays with the thread. A checkout on a thread with no project remains on disk for
// the manual worktree manager.
func (d *TaskDatabase) liftEmbeddedWorktrees() error {
	tasks, err := d.taskRows()
	if err != nil {
		return err
	}
	stale := slices.DeleteFunc(tasks, func(row taskRow) bool { return !truthy(row.task.get("worktree")) })
	if len(stale) == 0 {
		return nil
	}
	return d.transaction(func() error {
		for _, row := range stale {
			rest := row.task.without("worktree")
			record, isObject, err := parseObject(row.task.get("worktree"))
			if err != nil || !isObject {
				record = object{}
			}
			enteredAt := record.get("enteredAt")
			record = record.without("enteredAt")
			if projectID := rest.get("projectId"); projectID != nil {
				record = record.with("projectId", projectID)
			} else {
				record = record.without("projectId")
			}
			if !isWorktree(record) {
				if err := d.exec("UPDATE tasks SET data = ? WHERE id = ?", string(rest.encode()), row.id); err != nil {
					return err
				}
				continue
			}
			worktreeID := record.get("id")
			id, _ := stringValue(worktreeID)
			if err := d.exec("INSERT INTO worktrees (id, data) VALUES (?, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data", id, string(record.encode())); err != nil {
				return err
			}
			lifted := rest.with("worktreeId", worktreeID)
			if _, isNumber := numberValue(enteredAt); isNumber {
				lifted = lifted.with("worktreeEnteredAt", enteredAt)
			}
			if err := d.exec("UPDATE tasks SET data = ? WHERE id = ?", string(lifted.encode()), row.id); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListAutomations reads every automation, skipping a row that is unreadable: automations are read
// while the app boots, so one bad row must not take the window with it.
func (d *TaskDatabase) ListAutomations() ([]jsontext.Value, error) {
	rows, err := d.conn.QueryContext(background, "SELECT data FROM automations ORDER BY task_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	automations := []jsontext.Value{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		record, ok, err := parseObject(data)
		if err == nil && ok && isAutomation(record) {
			automations = append(automations, data)
		}
	}
	return automations, rows.Err()
}

// SaveAutomation writes an Automation, given as its JSON or as a value that encodes to it.
func (d *TaskDatabase) SaveAutomation(automation any) error {
	data, err := d.encode(automation)
	if err != nil {
		return err
	}
	record, _, err := parseObject(data)
	if err != nil {
		return err
	}
	id, err := bindable(record.get("id"))
	if err != nil {
		return err
	}
	taskID, err := bindable(record.get("taskId"))
	if err != nil {
		return err
	}
	return d.exec("INSERT INTO automations (id, task_id, data) VALUES (?, ?, ?) ON CONFLICT(id) DO UPDATE SET task_id = excluded.task_id, data = excluded.data", id, taskID, string(data))
}

func (d *TaskDatabase) DeleteAutomation(id string) error {
	return d.exec("DELETE FROM automations WHERE id = ?", id)
}

// CutOverCodexThreads is one clean cutover: it discards app records tied to the old Codex home,
// never Codex's original sessions.
func (d *TaskDatabase) CutOverCodexThreads() error {
	var done int
	err := d.conn.QueryRowContext(background, "SELECT 1 FROM settings WHERE key = ?", privateCodexHomeMarker).Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return d.transaction(func() error {
		old := "SELECT id FROM tasks WHERE json_valid(data) AND json_extract(data, '$.engine') = 'codex'"
		for _, table := range []string{"automations", "messages", "subagent_activity", "subagents"} {
			if err := d.exec("DELETE FROM " + table + " WHERE task_id IN (" + old + ")"); err != nil {
				return err
			}
		}
		if err := d.exec("DELETE FROM tasks WHERE id IN (" + old + ")"); err != nil {
			return err
		}
		// A thread currently using Claude can still carry an older Codex continuation.
		if err := d.exec("UPDATE tasks SET data = json_set(json_remove(data, '$.continuation'), '$.continuationStatus', 'none') WHERE json_valid(data) AND json_extract(data, '$.continuation.provider') = 'codex'"); err != nil {
			return err
		}
		return d.exec("INSERT INTO settings (key, value) VALUES (?, '1')", privateCodexHomeMarker)
	})
}

// Close closes the store, checkpointing its write-ahead log. Closing twice is harmless.
func (d *TaskDatabase) Close() error {
	if d.closed {
		return nil
	}
	var errs []error
	for _, statement := range []*sql.Stmt{d.saveTask, d.saveMessage, d.saveSubagent, d.saveActivity, d.searchMessages} {
		if statement != nil {
			errs = append(errs, statement.Close())
		}
	}
	errs = append(errs, d.conn.Close(), d.db.Close())
	if err := errors.Join(errs...); err != nil {
		return err
	}
	d.closed = true
	return nil
}

// Load reads the stored workspace, or nil when nothing was ever stored. With summariesOnly every
// conversation stays on disk and each thread that has one carries a historySummary instead.
func (d *TaskDatabase) Load(summariesOnly bool) (*LoadedTaskStore, error) {
	taskRecords, err := d.taskRows()
	if err != nil {
		return nil, err
	}
	projects, err := d.jsonRows("SELECT data FROM projects ORDER BY position")
	if err != nil {
		return nil, err
	}
	var lastFolderRow []byte
	err = d.conn.QueryRowContext(background, "SELECT value FROM settings WHERE key = 'lastFolder'").Scan(&lastFolderRow)
	hasLastFolder := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if len(taskRecords) == 0 && len(projects) == 0 && !hasLastFolder {
		return nil, nil
	}

	messages := map[string][]jsontext.Value{}
	if !summariesOnly {
		if err := d.eachRow("SELECT task_id, data FROM messages ORDER BY task_id, position", func(taskID string, data []byte) error {
			if _, err := parseJSON(data); err != nil {
				return err
			}
			messages[taskID] = append(messages[taskID], data)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	subagents := map[string][]jsontext.Value{}
	if err := d.eachRow("SELECT task_id, data FROM subagents ORDER BY task_id, position", func(taskID string, data []byte) error {
		record, isObject, err := parseObject(data)
		if err != nil {
			return err
		}
		if !isObject {
			record = object{}
		}
		subagents[taskID] = append(subagents[taskID], record.with("activity", jsontext.Value("[]")).encode())
		return nil
	}); err != nil {
		return nil, err
	}

	type loadedTask struct {
		record    jsontext.Value
		updatedAt float64
	}
	tasks := make([]loadedTask, 0, len(taskRecords))
	for _, row := range taskRecords {
		if row.task == nil {
			return nil, fmt.Errorf("TypeError: Cannot read properties of the stored task %q", row.id)
		}
		id, _ := stringValue(row.task.get("id"))
		task := row.task.with("messages", joinArray(messages[id]))
		if held, ok := subagents[id]; ok {
			task = task.with("subagents", joinArray(held))
		}
		updatedAt, ok := numberValue(row.task.get("updatedAt"))
		if !ok {
			updatedAt = math.NaN()
		}
		tasks = append(tasks, loadedTask{record: task.encode(), updatedAt: updatedAt})
	}
	slices.SortStableFunc(tasks, func(left, right loadedTask) int {
		if math.IsNaN(left.updatedAt) || math.IsNaN(right.updatedAt) {
			return 0
		}
		return cmp.Compare(right.updatedAt, left.updatedAt)
	})
	worktrees, err := d.jsonRows("SELECT data FROM worktrees")
	if err != nil {
		return nil, err
	}
	lastFolder := jsontext.Value("null")
	if hasLastFolder {
		if lastFolder, err = parseJSON(lastFolderRow); err != nil {
			return nil, err
		}
	}
	decoded := DecodedThreadStore{Projects: projects, Worktrees: worktrees, LastFolder: lastFolder}
	for _, task := range tasks {
		decoded.Tasks = append(decoded.Tasks, task.record)
	}
	validate := d.domain.ValidateThreadStore
	if validate == nil {
		validate = passThroughThreadStore
	}
	data, hidden, errs := validate(decoded)
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, " "))
	}
	result := &LoadedTaskStore{ThreadStoreData: data, HiddenTasks: float64(hidden)}
	if summariesOnly {
		summaries, err := d.historySummaries()
		if err != nil {
			return nil, err
		}
		result.UnloadedTaskIDs = []string{}
		result.Tasks = slices.Clone(result.Tasks)
		for i, value := range result.Tasks {
			task, _, err := parseObject(value)
			if err != nil {
				return nil, err
			}
			id, _ := stringValue(task.get("id"))
			summary, ok := summaries[id]
			if !ok {
				continue
			}
			result.UnloadedTaskIDs = append(result.UnloadedTaskIDs, id)
			encoded, err := json.Marshal(summary)
			if err != nil {
				return nil, err
			}
			result.Tasks[i] = task.with("historySummary", encoded).encode()
		}
	}
	return result, nil
}

// passThroughThreadStore stands in for validateThreadStoreData when no domain is given: it
// checks only the shape of the lists and the last folder.
func passThroughThreadStore(values DecodedThreadStore) (ThreadStoreData, int, []string) {
	data := ThreadStoreData{Version: 2, Tasks: values.Tasks, Projects: values.Projects, Worktrees: values.Worktrees}
	if string(values.LastFolder) != "null" {
		folder, ok := stringValue(values.LastFolder)
		if !ok {
			return ThreadStoreData{}, 0, []string{"v2 lastFolder must be a string or null"}
		}
		data.LastFolder = &folder
	}
	return data, 0, nil
}

func (d *TaskDatabase) eachRow(query string, each func(key string, data []byte) error, args ...any) error {
	rows, err := d.conn.QueryContext(background, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return err
		}
		if err := each(key, data); err != nil {
			return err
		}
	}
	return rows.Err()
}

// jsonRows reads one JSON column, failing as JSON.parse would on a row that is not JSON.
func (d *TaskDatabase) jsonRows(query string, args ...any) ([]jsontext.Value, error) {
	rows, err := d.conn.QueryContext(background, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []jsontext.Value{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if _, err := parseJSON(data); err != nil {
			return nil, err
		}
		values = append(values, data)
	}
	return values, rows.Err()
}

func (d *TaskDatabase) historySummaries() (map[string]HistorySummary, error) {
	rows, err := d.conn.QueryContext(background, historySummariesSQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := map[string]HistorySummary{}
	for rows.Next() {
		var id string
		var messageCount, attachmentCount float64
		var firstAt, lastAt any
		if err := rows.Scan(&id, &messageCount, &attachmentCount, &firstAt, &lastAt); err != nil {
			return nil, err
		}
		summaries[id] = HistorySummary{
			MessageCount:    messageCount,
			AttachmentCount: attachmentCount,
			FirstMessageAt:  sqlNumberValue(firstAt),
			LastAudibleAt:   sqlNumberValue(lastAt),
		}
	}
	return summaries, rows.Err()
}

func sqlNumberValue(value any) *float64 {
	switch number := value.(type) {
	case float64:
		return &number
	case int64:
		converted := float64(number)
		return &converted
	}
	return nil
}

// AttachmentPaths names every image a stored message still carries, as the absolute paths the
// messages hold.
func (d *TaskDatabase) AttachmentPaths() ([]string, error) {
	rows, err := d.conn.QueryContext(background, "SELECT DISTINCT value FROM messages, json_each(json_extract(messages.data, '$.attachments')) WHERE messages.has_attachment = 1 AND type = 'text'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// LoadThreadMessages reads a thread's whole conversation, oldest first.
func (d *TaskDatabase) LoadThreadMessages(taskID string) ([]jsontext.Value, error) {
	var exists int
	if err := d.conn.QueryRowContext(background, "SELECT 1 FROM tasks WHERE id = ?", taskID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrThreadGone
		}
		return nil, err
	}
	messages, err := d.jsonRows("SELECT data FROM messages WHERE task_id = ? ORDER BY position", taskID)
	if err != nil {
		return nil, err
	}
	if d.domain.ParseStoredMessages != nil {
		return d.domain.ParseStoredMessages(messages)
	}
	return messages, nil
}

// SearchThreadMessages names which of these threads have a stored message whose text contains
// search, which the caller has already lowercased with JSLower.
func (d *TaskDatabase) SearchThreadMessages(search string, taskIDs []string) ([]string, error) {
	rows, err := d.searchMessages.QueryContext(background, string(jsStrings(taskIDs)), search)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found = append(found, id)
	}
	return found, rows.Err()
}

// SubagentActivity reads a subagent's activity, only when someone opens it: a session's logs never
// all fit in the window.
func (d *TaskDatabase) SubagentActivity(taskID, subagentID string) ([]jsontext.Value, error) {
	return d.jsonRows("SELECT data FROM subagent_activity WHERE task_id = ? AND subagent_id = ? ORDER BY position", taskID, subagentID)
}

// ProjectRootsAreOwn is whether no project folder lies inside any root the app has made
// checkouts under.
func ProjectRootsAreOwn(roots []string, worktreesRoots []string) bool {
	owned := make([]string, len(worktreesRoots))
	for i, root := range worktreesRoots {
		owned[i] = resolvePath(root) + string(filepath.Separator)
	}
	for _, root := range roots {
		resolved := resolvePath(root)
		for _, prefix := range owned {
			if strings.HasPrefix(resolved, prefix) {
				return false
			}
		}
	}
	return true
}

// resolvePath is Node's path.resolve for one path.
func resolvePath(path string) string {
	if resolved, err := filepath.Abs(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

// Persist writes a delta in one transaction. delta is a TaskStoreDelta, or a value that encodes to
// one. A project row and the last folder are where the user's own directories are recorded, so a
// delta that would move either into the app's worktrees is dropped instead of written. The rest of
// the delta still lands: losing transcripts is a worse answer than keeping the folder on disk.
func (d *TaskDatabase) Persist(value any) error {
	delta, err := d.delta(value)
	if err != nil {
		return err
	}
	var projectRoots []string
	for _, project := range delta.Projects {
		root, ok := stringValue(memberOf(project, "root"))
		if !ok {
			return errors.New(`The "paths[0]" argument must be of type string.`)
		}
		projectRoots = append(projectRoots, root)
	}
	writesProjects := delta.Projects == nil || ProjectRootsAreOwn(projectRoots, d.worktreesRoots)
	writesLastFolder := !delta.LastFolder.Present || delta.LastFolder.Value == nil || *delta.LastFolder.Value == "" ||
		ProjectRootsAreOwn([]string{*delta.LastFolder.Value}, d.worktreesRoots)
	if !writesProjects || !writesLastFolder {
		lastFolder := "undefined"
		if delta.LastFolder.Present {
			lastFolder = "null"
			if delta.LastFolder.Value != nil {
				lastFolder = *delta.LastFolder.Value
			}
		}
		log.Printf("Refused to record a folder inside the app's worktrees directory. projects: %q, lastFolder: %s", projectRoots, lastFolder)
	}
	return d.transaction(func() error {
		if delta.Projects != nil && writesProjects {
			if err := d.exec("DELETE FROM projects"); err != nil {
				return err
			}
			for index, project := range delta.Projects {
				id, err := memberBinding(project, "id")
				if err != nil {
					return err
				}
				if err := d.exec("INSERT INTO projects (id, position, data) VALUES (?, ?, ?)", id, index, string(project)); err != nil {
					return err
				}
			}
		}
		if delta.Worktrees != nil {
			if err := d.exec("DELETE FROM worktrees"); err != nil {
				return err
			}
			for _, worktree := range delta.Worktrees {
				id, err := memberBinding(worktree, "id")
				if err != nil {
					return err
				}
				if err := d.exec("INSERT INTO worktrees (id, data) VALUES (?, ?)", id, string(worktree)); err != nil {
					return err
				}
			}
		}
		if delta.LastFolder.Present && writesLastFolder {
			folder := "null"
			if delta.LastFolder.Value != nil {
				folder = string(jsString(*delta.LastFolder.Value))
			}
			if err := d.exec("INSERT INTO settings (key, value) VALUES ('lastFolder', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", folder); err != nil {
				return err
			}
		}
		for _, id := range delta.RemovedTasks {
			for _, drop := range []string{
				"DELETE FROM messages WHERE task_id = ?",
				"DELETE FROM subagent_activity WHERE task_id = ?",
				"DELETE FROM subagents WHERE task_id = ?",
				"DELETE FROM tasks WHERE id = ?",
			} {
				if err := d.exec(drop, id); err != nil {
					return err
				}
			}
		}
		for _, change := range delta.Tasks {
			if err := d.persistChange(change); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *TaskDatabase) persistChange(change TaskChange) error {
	taskID, err := memberBinding(change.Task, "id")
	if err != nil {
		return err
	}
	if _, err := d.saveTask.ExecContext(background, taskID, string(change.Task)); err != nil {
		return err
	}
	for _, indexed := range change.Messages {
		message, _, err := parseObject(indexed.Message)
		if err != nil {
			return err
		}
		id, err := bindable(message.get("id"))
		if err != nil {
			return err
		}
		at, err := bindable(message.get("at"))
		if err != nil {
			return err
		}
		withdrawn := message.get("withdrawn")
		if withdrawn == nil || string(withdrawn) == "null" {
			withdrawn = message.get("quiet")
		}
		audible := 1
		if truthy(withdrawn) {
			audible = 0
		}
		hasAttachment := 0
		if attachments := message.get("attachments"); attachments != nil && attachmentsLength(attachments) > 0 {
			hasAttachment = 1
		}
		if _, err := d.saveMessage.ExecContext(background, taskID, id, sqlNumber(indexed.Index), string(indexed.Message), at, audible, hasAttachment); err != nil {
			return err
		}
	}
	for _, indexed := range change.Subagents {
		id, err := memberBinding(indexed.Subagent, "id")
		if err != nil {
			return err
		}
		if _, err := d.saveSubagent.ExecContext(background, taskID, id, sqlNumber(indexed.Index), string(indexed.Subagent)); err != nil {
			return err
		}
	}
	for _, indexed := range change.Activity {
		id, err := memberBinding(indexed.Item, "id")
		if err != nil {
			return err
		}
		if _, err := d.saveActivity.ExecContext(background, taskID, indexed.SubagentID, id, sqlNumber(indexed.Index), string(indexed.Item)); err != nil {
			return err
		}
	}
	return nil
}

// attachmentsLength is `attachments.length` for an array or a string.
func attachmentsLength(value jsontext.Value) int {
	if items, ok := arrayItems(value); ok {
		return len(items)
	}
	if text, ok := stringValue(value); ok {
		return utf16Length(text)
	}
	return 0
}

func memberOf(value jsontext.Value, name string) jsontext.Value {
	record, ok, err := parseObject(value)
	if err != nil || !ok {
		return nil
	}
	return record.get(name)
}

func memberBinding(value jsontext.Value, name string) (any, error) {
	record, _, err := parseObject(value)
	if err != nil {
		return nil, err
	}
	return bindable(record.get(name))
}

// bindable is the SQLite parameter node:sqlite binds for a JSON value: absent and null bind NULL.
func bindable(value jsontext.Value) (any, error) {
	if value == nil || string(value) == "null" {
		return nil, nil
	}
	if text, ok := stringValue(value); ok {
		return text, nil
	}
	if number, ok := numberValue(value); ok {
		return sqlNumber(number), nil
	}
	return nil, fmt.Errorf("Provided value cannot be bound to SQLite parameter: %s", value)
}

// sqlNumber binds a whole number as an integer and any other as a real, as node:sqlite's column
// affinities store them.
func sqlNumber(number float64) any {
	if number == math.Trunc(number) && math.Abs(number) < 1<<63 {
		return int64(number)
	}
	return number
}

func (d *TaskDatabase) encode(value any) (jsontext.Value, error) {
	switch typed := value.(type) {
	case jsontext.Value:
		return typed, nil
	case []byte:
		return typed, nil
	}
	return json.Marshal(value, d.jsonOptions...)
}

func (d *TaskDatabase) delta(value any) (*TaskStoreDelta, error) {
	switch typed := value.(type) {
	case *TaskStoreDelta:
		return typed, nil
	case TaskStoreDelta:
		return &typed, nil
	}
	data, err := d.encode(value)
	if err != nil {
		return nil, err
	}
	delta := &TaskStoreDelta{}
	if err := json.Unmarshal(data, delta); err != nil {
		return nil, err
	}
	return delta, nil
}
