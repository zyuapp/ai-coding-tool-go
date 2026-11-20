package application

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const storeV1Tasks = `[{"id":"task-1","title":"Fix the app","folder":"/work/ai-coding-tool","sessionId":"session-1","mode":"acceptEdits","messages":[{"id":"message-1","kind":"user","text":"Fix it","at":10}],"changedFiles":[" M src/App.tsx"],"updatedAt":20}]`

// storeMemory is a storage that may refuse a read or a write the way a full disk throws.
type storeMemory struct {
	values   map[string]string
	reads    []string
	readErr  error
	writeErr func(key string) error
}

func (m *storeMemory) GetItem(key string) *string {
	m.reads = append(m.reads, key)
	if m.readErr != nil {
		panic(m.readErr)
	}
	if value, ok := m.values[key]; ok {
		return &value
	}
	return nil
}

func (m *storeMemory) SetItem(key, value string) {
	if m.writeErr != nil {
		if err := m.writeErr(key); err != nil {
			panic(err)
		}
	}
	m.values[key] = value
}

func storeLegacy(keys taskStoreKeySet) *storeMemory {
	return &storeMemory{values: map[string]string{keys.Tasks: storeV1Tasks, keys.Projects: `["/work/ai-coding-tool"]`, keys.LastFolder: "/work/ai-coding-tool"}}
}

func storeLoaded(t *testing.T, store *TaskStore) domain.ThreadStoreData {
	t.Helper()
	loaded, ok := store.Load().(domain.ThreadStoreParseResultTrue)
	if !ok {
		t.Fatal("the store did not load")
	}
	return loaded.Data
}

func storeSaved(result TaskStoreSaveResult) bool {
	_, ok := result.(TaskStoreSaveResultTrue)
	return ok
}

func storeEmpty() *domain.ThreadStoreData {
	return &domain.ThreadStoreData{Tasks: []*domain.StoredThread{}, Projects: []*domain.Project{}, Worktrees: []*domain.Worktree{}}
}

func TestLoadsTheThreadlineNamespaceAndSavesUnderAICodingTool(t *testing.T) {
	memory := storeLegacy(LegacyTaskStoreKeys.V1)
	store := NewTaskStore(memory)
	data := storeLoaded(t, store)
	if !storeSaved(store.Save(&data)) {
		t.Fatal("save failed")
	}
	if _, ok := memory.values[TaskStoreKeys.V2.Envelope]; !ok {
		t.Fatal("no envelope written")
	}
}

func TestMalformedStorageBlocksWrites(t *testing.T) {
	memory := storeLegacy(TaskStoreKeys.V1)
	memory.values[TaskStoreKeys.V1.Tasks] = "{not-json"
	store := NewTaskStore(memory)
	if _, ok := store.Load().(domain.ThreadStoreParseResultFalse); !ok {
		t.Fatal("malformed storage loaded")
	}
	if storeSaved(store.Save(storeEmpty())) {
		t.Fatal("a store that could not load wrote")
	}
	if _, ok := memory.values[TaskStoreKeys.V2.Tasks]; ok {
		t.Fatal("split v2 written")
	}
}

func TestSaveBeforeLoadIsRefused(t *testing.T) {
	coreEqualJSON(t, NewTaskStore(&storeMemory{values: map[string]string{}}).Save(storeEmpty()), `{"ok":false,"reason":"load-required"}`, "save")
}

func TestStorageWriteFailuresDisableFutureWrites(t *testing.T) {
	memory := storeLegacy(TaskStoreKeys.V1)
	original := map[string]string{}
	for key, value := range memory.values {
		original[key] = value
	}
	memory.writeErr = func(key string) error {
		if key == TaskStoreKeys.V2.Envelope {
			return errors.New("quota exceeded")
		}
		return nil
	}
	store := NewTaskStore(memory)
	data := storeLoaded(t, store)
	coreEqualJSON(t, store.Save(&data), `{"ok":false,"reason":"storage","error":"quota exceeded"}`, "save")
	for key, value := range original {
		if memory.values[key] != value {
			t.Fatalf("%s touched", key)
		}
	}
	memory.writeErr = nil
	if storeSaved(store.Save(&data)) {
		t.Fatal("a store whose write failed wrote again")
	}
}

func TestWritesV2AsOneEnvelopeAndLeavesV1Untouched(t *testing.T) {
	memory := storeLegacy(TaskStoreKeys.V1)
	store := NewTaskStore(memory)
	data := storeLoaded(t, store)
	if !storeSaved(store.Save(&data)) {
		t.Fatal("save failed")
	}
	for _, key := range []string{TaskStoreKeys.V2.Tasks, TaskStoreKeys.V2.Projects, TaskStoreKeys.V2.LastFolder} {
		if _, ok := memory.values[key]; ok {
			t.Fatalf("%s written", key)
		}
	}
	if memory.values[TaskStoreKeys.V1.Tasks] != storeV1Tasks {
		t.Fatal("v1 touched")
	}
	coreEqualJSON(t, storeLoaded(t, NewTaskStore(memory)), data, "reloaded")
}

func TestIncompleteSplitV2WriteRecoversFromV1(t *testing.T) {
	memory := storeLegacy(TaskStoreKeys.V1)
	memory.values[TaskStoreKeys.V2.Tasks] = *domain.SerializeThreadStore(storeEmpty()).Tasks
	loaded, ok := NewTaskStore(memory).Load().(domain.ThreadStoreParseResultTrue)
	if !ok || loaded.SourceVersion != 1 || len(loaded.Data.Tasks) == 0 || loaded.Data.Tasks[0].ID != "task-1" {
		t.Fatal("did not recover from v1")
	}
}

func TestStorageReadFailuresAreANonWritableError(t *testing.T) {
	memory := &storeMemory{values: map[string]string{}, readErr: errors.New("storage unavailable")}
	loaded, ok := NewTaskStore(memory).Load().(domain.ThreadStoreParseResultFalse)
	if !ok || loaded.ErrorKind != domain.ThreadStoreParseResultFalseErrorKindStorage || !strings.Contains(loaded.Errors[0], "storage unavailable") {
		t.Fatalf("loaded %v", coreJSON(t, loaded))
	}
}

func TestCorruptV2EnvelopeBlocksWrites(t *testing.T) {
	for _, envelope := range []string{"{broken", `{"tasks":"[]","projects":null}`, `{"tasks":"[]","projects":"[]","lastFolder":null,"worktrees":3}`, `[]`} {
		memory := storeLegacy(TaskStoreKeys.V1)
		memory.values[TaskStoreKeys.V2.Envelope] = envelope
		store := NewTaskStore(memory)
		loaded, ok := store.Load().(domain.ThreadStoreParseResultFalse)
		if !ok || loaded.SourceVersion != 2 || deref(loaded.Raw.Tasks) != envelope {
			t.Fatalf("%s loaded", envelope)
		}
		if storeSaved(store.Save(storeEmpty())) {
			t.Fatalf("%s: a corrupt store wrote", envelope)
		}
	}
}

func TestValidEnvelopeTakesPrecedence(t *testing.T) {
	envelope, err := jsonx.Marshal(domain.SerializeThreadStore(storeEmpty()))
	if err != nil {
		t.Fatal(err)
	}
	split := storeEmpty()
	split.LastFolder = new("/split")
	splitValues := domain.SerializeThreadStore(split)
	memory := storeLegacy(TaskStoreKeys.V1)
	memory.values[TaskStoreKeys.V2.Envelope] = string(envelope)
	memory.values[TaskStoreKeys.V2.Tasks] = *splitValues.Tasks
	memory.values[TaskStoreKeys.V2.Projects] = *splitValues.Projects
	memory.values[TaskStoreKeys.V2.LastFolder] = *splitValues.LastFolder
	coreEqualJSON(t, storeLoaded(t, NewTaskStore(memory)), storeEmpty(), "loaded")
	if !slices.Equal(memory.reads, []string{TaskStoreKeys.V2.Envelope}) {
		t.Fatalf("reads %v", memory.reads)
	}
}
