package domain

import (
	"maps"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func legacyStore() StorageValues {
	return StorageValues{
		Tasks:      new(`[{"id":"task-1","title":"Fix the app","folder":"/work/ai-coding-tool","mode":"acceptEdits","messages":[{"id":"message-1","kind":"user","text":"Fix it","at":10}],"changedFiles":[],"updatedAt":20}]`),
		Projects:   new(`["/work/ai-coding-tool"]`),
		LastFolder: new("/work/ai-coding-tool"),
	}
}

func migratedStore(t *testing.T) ThreadStoreData {
	t.Helper()
	migrated, ok := MigrateV1ToV2(legacyStore()).(ThreadStoreParseResultTrue)
	if !ok {
		t.Fatal("migration failed")
	}
	return migrated.Data
}

// asRecord is a value as JSON decodes it into any.
func asRecord(t *testing.T, value any) map[string]any {
	t.Helper()
	return decoded(t, string(must(jsonx.Marshal(value)))).(map[string]any)
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// storeOf serializes the migrated store with tasks as given, however malformed.
func storeOf(t *testing.T, data ThreadStoreData, tasks ...map[string]any) StorageValues {
	t.Helper()
	raw := SerializeThreadStore(&data)
	raw.Tasks = new(string(must(jsonx.Marshal(map[string]any{"version": 2, "value": tasks}))))
	return raw
}

func parsedStore(t *testing.T, raw StorageValues) ThreadStoreParseResultTrue {
	t.Helper()
	parsed, ok := ParseThreadStore(raw).(ThreadStoreParseResultTrue)
	if !ok {
		t.Fatalf("store did not parse: %+v", ParseThreadStore(raw))
	}
	return parsed
}

func with(record map[string]any, key string, value any) map[string]any {
	next := maps.Clone(record)
	next[key] = value
	return next
}

func TestLegacyStoreMigrates(t *testing.T) {
	data := migratedStore(t)
	jsonEqual(t, data, `{"version":2,"tasks":[{"id":"task-1","title":"Fix the app","projectId":"legacy-project-%2Fwork%2Fai-coding-tool","executionPolicy":"allow-edits","engine":"claude","messages":[{"id":"message-1","kind":"user","text":"Fix it","at":10}],"continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":20},"updatedAt":20}],"projects":[{"id":"legacy-project-%2Fwork%2Fai-coding-tool","root":"/work/ai-coding-tool"}],"worktrees":[],"lastFolder":"/work/ai-coding-tool"}`, "migrated")
	bad := legacyStore()
	bad.Tasks = new(`[{"id":"","title":1,"folder":"/a","mode":"x","messages":[{"id":1}],"changedFiles":[2],"updatedAt":"now","sessionId":null}]`)
	failed, ok := MigrateV1ToV2(bad).(ThreadStoreParseResultFalse)
	if !ok || failed.PreservedV1 == nil || len(failed.Errors) != 6 || failed.Errors[5] != "tasks[0].messages[0] is invalid" {
		t.Fatalf("%+v", failed)
	}
	empty := parsedStore(t, StorageValues{})
	jsonEqual(t, empty.Data, `{"version":2,"tasks":[],"projects":[],"worktrees":[],"lastFolder":null}`, "empty")
	if empty.SourceVersion != 0 {
		t.Fatal("source version")
	}
	if corrupt, ok := ParseThreadStore(StorageValues{Tasks: new(`{"version":2,"value":[]}`), Projects: new("{")}).(ThreadStoreParseResultFalse); !ok || corrupt.Errors[0] != "projects is not valid JSON" || corrupt.SourceVersion != 2 {
		t.Fatal("corrupt v2")
	}
}

func TestThreadModelSurvivesAReload(t *testing.T) {
	data := migratedStore(t)
	lead := with(asRecord(t, data.Tasks[0]), "role", "coordinator")
	parsed := parsedStore(t, storeOf(t, data, with(lead, "threadModel", map[string]any{"model": "gpt-6.2-sol", "effort": "ultra"})))
	jsonEqual(t, parsed.Data.Tasks[0].ThreadModel, `{"model":"gpt-6.2-sol","effort":"ultra"}`, "kept")
	retired := parsedStore(t, storeOf(t, data, with(lead, "threadModel", map[string]any{"model": "gpt-5.6-luna", "effort": "max"})))
	jsonEqual(t, retired.Data.Tasks[0].ThreadModel, `{"model":"gpt-6-luna","effort":"max"}`, "successor")
	named := parsedStore(t, storeOf(t, data, with(lead, "threadModel", map[string]any{"model": "openai/gpt-9", "effort": "high"})))
	jsonEqual(t, named.Data.Tasks[0].ThreadModel, `{"model":"openai/gpt-9","effort":"high"}`, "an engine's own model")
	for _, broken := range []any{map[string]any{"model": "gpt 0"}, map[string]any{"model": ""}, map[string]any{"model": "opus", "effort": "furious"}, "opus"} {
		loaded := parsedStore(t, storeOf(t, data, with(lead, "threadModel", broken)))
		if loaded.HiddenTasks != 0 || loaded.Data.Tasks[0].ThreadModel != nil || len(loaded.Data.Tasks[0].Messages) != 1 {
			t.Fatalf("%v", broken)
		}
	}
}

func TestDroppableFieldsCostTheThreadNothingElse(t *testing.T) {
	data := migratedStore(t)
	task := asRecord(t, data.Tasks[0])
	memory := decoded(t, `{"purpose":"Ship the v2 reader","notes":[{"id":"n1","text":"Propose threads first","at":1}],"revision":3,"heard":1}`)
	jsonEqual(t, parsedStore(t, storeOf(t, data, with(task, "memory", memory))).Data.Tasks[0].Memory, string(must(jsonx.Marshal(memory))), "memory")
	cases := map[string][]any{
		"memory":       {decoded(t, `{"notes":"none","revision":1}`), decoded(t, `{"notes":[{"id":"n1","text":3,"at":1}],"revision":1}`), decoded(t, `{"notes":[],"revision":-1}`), "remember this"},
		"resolvedAt":   {"yesterday"},
		"pullRequest":  {decoded(t, `{"number":0,"url":""}`)},
		"muted":        {false, "yes", 1.0},
		"report":       {decoded(t, `{"state":"blocked","summary":"   ","at":5}`), decoded(t, `{"state":"stuck","summary":"x","at":5}`), "blocked"},
		"brief":        {decoded(t, `{"intent":"x","doneWhen":"y","delivers":"release"}`)},
		"remoteLead":   {decoded(t, `{"threadId":"t","title":"Lead","computer":"Laptop","away":"yes"}`)},
		"remoteHeard":  {decoded(t, `{"far":"late"}`)},
		"remoteStops":  {decoded(t, `{"far":{"computer":"studio","runId":7}}`)},
		"caughtStarts": {decoded(t, `{"p0":{"threadId":"landed"}}`)},
		"usage":        {decoded(t, `{"opus":-1}`)},
		"proposals":    {"none"},
	}
	for field, values := range cases {
		for _, value := range values {
			loaded := parsedStore(t, storeOf(t, data, with(with(task, "parentId", "lead"), field, value)))
			if loaded.HiddenTasks != 0 || len(loaded.Data.Tasks[0].Messages) != 1 || *loaded.Data.Tasks[0].ParentID != "lead" {
				t.Fatalf("%s %v hid the thread", field, value)
			}
			if _, kept := asRecord(t, loaded.Data.Tasks[0])[field]; kept {
				t.Fatalf("%s %v was kept", field, value)
			}
		}
	}
	decision := decoded(t, `[{"id":"d1","question":"Merge?","options":[{"label":"Yes","description":"Merge now"},{"label":"Yes","description":"Merge after CI"}],"raisedAt":5}]`)
	if loaded := parsedStore(t, storeOf(t, data, with(task, "decisions", decision))); loaded.HiddenTasks != 0 || len(loaded.Data.Tasks[0].Decisions) != 1 {
		t.Fatal("a decision with shared labels still loads")
	}
}

func TestSuggestionsDropAlone(t *testing.T) {
	data := migratedStore(t)
	brief := `{"intent":"fix the login test","doneWhen":"it passes","delivers":"pull-request"}`
	suggested := `{"id":"p1","title":"Fix login","named":true,"prompt":"Fix it","brief":` + brief + `,"place":{"project":"project-1","worktree":true},"model":"gpt-6-luna","effort":"low","role":"implementer","proposedAt":3,"error":"No project matches."}`
	plain := `{"id":"p2","title":"Docs","prompt":"Write them","brief":` + brief + `,"place":{"worktreeId":"wt-1"},"proposedAt":4}`
	lead := with(asRecord(t, data.Tasks[0]), "role", "coordinator")
	parsed := parsedStore(t, storeOf(t, data, with(lead, "proposals", decoded(t, "["+suggested+","+plain+"]"))))
	jsonEqual(t, parsed.Data.Tasks[0].Proposals, "["+suggested+","+plain+"]", "round trip")
	retired := decoded(t, "["+suggested+"]").([]any)
	retired[0].(map[string]any)["model"] = "gpt-5.6-luna"
	if got := parsedStore(t, storeOf(t, data, with(lead, "proposals", retired))).Data.Tasks[0].Proposals[0].Model; *got != "gpt-6-luna" {
		t.Fatal(*got)
	}
	broken := decoded(t, "["+suggested+","+plain+","+plain+","+plain+"]").([]any)
	broken[0].(map[string]any)["brief"] = "fix it"
	broken[2] = with(broken[2].(map[string]any), "id", "p3")
	broken[2].(map[string]any)["model"] = "gpt 0"
	broken[3] = with(broken[3].(map[string]any), "id", "p4")
	broken[3].(map[string]any)["model"] = "openai/gpt-9"
	loaded := parsedStore(t, storeOf(t, data, with(lead, "proposals", broken)))
	if loaded.HiddenTasks != 0 || len(loaded.Data.Tasks[0].Proposals) != 2 || loaded.Data.Tasks[0].Proposals[1].ID != "p4" {
		t.Fatal("only unreadable suggestions are dropped")
	}
}

func TestRemoteBookkeepingSurvivesAReload(t *testing.T) {
	data := migratedStore(t)
	task := asRecord(t, data.Tasks[0])
	member := with(task, "remoteLead", decoded(t, `{"threadId":"lead-there","title":"Lead","computer":"Laptop","away":true,"backAt":20}`))
	lead := with(with(with(with(with(with(task, "id", "lead"), "role", "coordinator"),
		"remoteHeard", decoded(t, `{"far":40}`)),
		"remoteWorking", decoded(t, `{"far":{"computer":"studio","title":"Far work"}}`)),
		"remoteStops", decoded(t, `{"far":{"computer":"studio","runId":"run-far"},"waiting":{"computer":"studio"},"answering":{"computer":"studio","answer":{"at":30,"runId":"run-answer"}}}`)),
		"caughtStarts", decoded(t, `{"p0":{"computer":"studio"},"p2":{"computer":"studio","threadId":"landed"}}`))
	parsed := parsedStore(t, storeOf(t, data, member, lead))
	jsonEqual(t, parsed.Data.Tasks[0].RemoteLead, `{"threadId":"lead-there","title":"Lead","computer":"Laptop","away":true,"backAt":20}`, "remoteLead")
	jsonEqual(t, parsed.Data.Tasks[1].RemoteStops, `{"far":{"computer":"studio","runId":"run-far"},"waiting":{"computer":"studio"},"answering":{"computer":"studio","answer":{"at":30,"runId":"run-answer"}}}`, "remoteStops")
	jsonEqual(t, parsed.Data.Tasks[1].CaughtStarts, `{"p0":{"computer":"studio"},"p2":{"computer":"studio","threadId":"landed"}}`, "caughtStarts")
}

func TestStoredThreadsSanitize(t *testing.T) {
	data := migratedStore(t)
	task := asRecord(t, data.Tasks[0])
	old := with(with(with(task, "id", "old"), "model", "default"), "contextWindow", 200_000.0)
	delete(old, "engine")
	old["silencedKeys"] = []any{"k"}
	old["crewNotes"] = decoded(t, `[{"id":"n","threadId":"t","text":"x","at":1}]`)
	old["messages"] = decoded(t, `[{"id":"m","kind":"assistant","text":"x","quiet":true,"at":1}]`)
	old["subagents"] = decoded(t, `[{"id":"s","description":"d","status":"working","startedAt":1,"activity":[]}]`)
	old["worktree"] = decoded(t, `{"id":"wt-1","root":"/w","workspaceId":"ws","baseCommit":"abc","createdAt":1,"lastUsedAt":2,"enteredAt":3}`)
	old["continuation"] = decoded(t, `{"provider":"","value":"x"}`)
	old["continuationStatus"] = "available"
	gone := with(with(task, "id", "gone"), "worktreeId", "wt-missing")
	unplaced := with(with(task, "id", "unplaced"), "projectId", "nowhere")
	unreadable := with(with(task, "id", "unreadable"), "engine", "galaxy")
	wrongStatus := with(with(task, "id", "wrong"), "continuationStatus", "available")
	parsed := parsedStore(t, storeOf(t, data, old, gone, unplaced, unreadable, wrongStatus))
	if parsed.HiddenTasks != 3 || len(parsed.Data.Tasks) != 2 {
		t.Fatalf("hidden %v tasks %d", parsed.HiddenTasks, len(parsed.Data.Tasks))
	}
	jsonEqual(t, parsed.Data.Worktrees, `[{"id":"wt-1","projectId":"legacy-project-%2Fwork%2Fai-coding-tool","root":"/w","workspaceId":"ws","baseCommit":"abc","createdAt":1,"lastUsedAt":2}]`, "lifted worktree")
	read := parsed.Data.Tasks[0]
	if read.Model != nil || read.Engine != AgentEngineClaude || *read.WorktreeID != "wt-1" || *read.WorktreeEnteredAt != 3 || read.HandledIssues[0] != "k" || len(read.CoordinationNotes) != 1 {
		t.Fatalf("%+v", read)
	}
	if read.Continuation != nil || read.ContinuationStatus != ContinuationStatusInvalid || !*read.Messages[0].Withdrawn || read.Subagents[0].Status != SubagentStatusStopped || *read.Subagents[0].FinishedAt != 20 {
		t.Fatal("continuation, messages, subagents")
	}
	if parsed.Data.Tasks[1].WorktreeID != nil {
		t.Fatal("a missing checkout is let go")
	}
	ranked := RankedThread(read)
	if ranked.RankedAt != 1 || len(ranked.Messages) != 1 || ranked.Messages[0] != read.Messages[0] || ranked.ID != "old" {
		t.Fatalf("ranked %+v", ranked.RankedAt)
	}
}

func TestReadableRemoteThread(t *testing.T) {
	thread := coordThread("t", func(thread *Thread) {
		thread.Messages = []*ConversationMessage{{ID: "m", Kind: ConversationMessageKindUser, Detail: new("Thread updates"), At: 4}}
	})
	plain := coordThread("p", nil)
	if ReadableRemoteThread(plain) != plain {
		t.Fatal("a thread with nothing to leave out comes back as it was")
	}
	read := ReadableRemoteThread(thread)
	if read == thread || read.Messages[0].Origin != (MessageOriginCoordination{}) || thread.Messages[0].Origin != nil {
		t.Fatal("a labelled message gets its origin")
	}
	newer := coordThread("n", func(thread *Thread) {
		thread.Role = new(ThreadRole("designer"))
		thread.Outcome = new(ThreadOutcome("exploded"))
		thread.Model = new("gpt-6-sol")
		thread.RankedAt = 0
		thread.UpdatedAt = 7
		thread.ThreadModel = &ThreadModelChoice{Model: "future-model", Effort: new(AgentEffort("galaxy"))}
		thread.Proposals = []*ThreadProposal{{ID: "p1", Title: "t", Prompt: "p", Engine: new("galaxy"), Model: new("future"), ProposedAt: 1}, {ID: "", ProposedAt: 1}}
		thread.Memory = &CoordinatorMemory{Notes: []*MemoryNote{}, Revision: -1}
	})
	read = ReadableRemoteThread(newer)
	if read.Role != nil || read.Outcome != nil || *read.Model != "gpt-6.2-sol" || read.RankedAt != 7 || read.Memory != nil {
		t.Fatalf("%+v", read.ThreadCore)
	}
	if read.ThreadModel == nil || read.ThreadModel.Model != "future-model" || len(read.Proposals) != 1 || *read.Proposals[0].Engine != "galaxy" {
		t.Fatal("what a newer build names is kept as sent")
	}
}
