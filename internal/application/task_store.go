package application

import (
	"fmt"

	"encoding/json/jsontext"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// taskStoreKeySet names one version's storage keys. Envelope and Worktrees are empty where that version has none.
type taskStoreKeySet struct {
	Envelope   string
	Tasks      string
	Projects   string
	Worktrees  string
	LastFolder string
}

// taskStoreKeys is the stores' keys by version.
type taskStoreKeys struct {
	V1 taskStoreKeySet
	V2 taskStoreKeySet
}

var TaskStoreKeys = taskStoreKeys{
	V1: taskStoreKeySet{
		Tasks:      "aicodingtool.tasks.v1",
		Projects:   "aicodingtool.projects.v1",
		LastFolder: "aicodingtool.last-folder.v1",
	},
	V2: taskStoreKeySet{
		Envelope:   "aicodingtool.store.v2",
		Tasks:      "aicodingtool.tasks.v2",
		Projects:   "aicodingtool.projects.v2",
		Worktrees:  "aicodingtool.worktrees.v2",
		LastFolder: "aicodingtool.last-folder.v2",
	},
}

var LegacyTaskStoreKeys = taskStoreKeys{
	V1: taskStoreKeySet{
		Tasks:      "threadline.tasks.v1",
		Projects:   "threadline.projects.v1",
		LastFolder: "threadline.last-folder.v1",
	},
	V2: taskStoreKeySet{
		Envelope:   "threadline.store.v2",
		Tasks:      "threadline.tasks.v2",
		Projects:   "threadline.projects.v2",
		Worktrees:  "threadline.worktrees.v2",
		LastFolder: "threadline.last-folder.v2",
	},
}

// TaskStore reads and writes the thread store through a key-value storage. A storage that fails
// panics (KeyValueStorage has no error results); Load and Save recover it as TypeScript catches a throw.
type TaskStore struct {
	storage  KeyValueStorage
	loaded   bool
	writable bool
}

func NewTaskStore(storage KeyValueStorage) *TaskStore {
	return &TaskStore{storage: storage}
}

func (s *TaskStore) Load() domain.ThreadStoreParseResult {
	var envelope *string
	var values domain.StorageValues
	if failure, failed := catching(func() {
		envelope = s.storage.GetItem(TaskStoreKeys.V2.Envelope)
		if envelope != nil {
			return
		}
		v2 := read(s.storage, TaskStoreKeys.V2)
		if complete(v2) {
			values = v2
			return
		}
		v1 := read(s.storage, TaskStoreKeys.V1)
		if !empty(v2) || !empty(v1) {
			values = v1
			return
		}
		envelope = s.storage.GetItem(LegacyTaskStoreKeys.V2.Envelope)
		if envelope == nil {
			legacyV2 := read(s.storage, LegacyTaskStoreKeys.V2)
			if complete(legacyV2) {
				values = legacyV2
			} else {
				values = read(s.storage, LegacyTaskStoreKeys.V1)
			}
		}
	}); failed {
		s.loaded = true
		s.writable = false
		return domain.ThreadStoreParseResultFalse{
			SourceVersion: 0,
			ErrorKind:     domain.ThreadStoreParseResultFalseErrorKindStorage,
			Errors:        []string{errorMessage(failure)},
			PreservedV1:   nil,
			Raw:           domain.StorageValues{},
		}
	}
	var result domain.ThreadStoreParseResult
	if envelope != nil {
		if decoded, ok := parseEnvelope(*envelope); ok {
			result = domain.ParseThreadStore(decoded)
		} else {
			result = corruptEnvelope(*envelope)
		}
	} else {
		result = domain.ParseThreadStore(values)
	}
	s.loaded = true
	_, s.writable = result.(domain.ThreadStoreParseResultTrue)
	return result
}

func (s *TaskStore) Save(data *domain.ThreadStoreData) TaskStoreSaveResult {
	if !s.loaded {
		return TaskStoreSaveResultFalse{Reason: TaskStoreSaveResultFalseReasonLoadRequired}
	}
	if !s.writable {
		return TaskStoreSaveResultFalse{Reason: TaskStoreSaveResultFalseReasonCorrupt}
	}
	values := domain.SerializeThreadStore(data)
	if failure, failed := catching(func() {
		encoded, err := jsonx.Marshal(values)
		if err != nil {
			panic(err)
		}
		s.storage.SetItem(TaskStoreKeys.V2.Envelope, string(encoded))
	}); failed {
		s.writable = false
		message := errorMessage(failure)
		return TaskStoreSaveResultFalse{Reason: TaskStoreSaveResultFalseReasonStorage, Error: &message}
	}
	return TaskStoreSaveResultTrue{Values: values}
}

func read(storage KeyValueStorage, keys taskStoreKeySet) domain.StorageValues {
	values := domain.StorageValues{
		Tasks:      storage.GetItem(keys.Tasks),
		Projects:   storage.GetItem(keys.Projects),
		LastFolder: storage.GetItem(keys.LastFolder),
	}
	if keys.Worktrees != "" {
		values.Worktrees = storage.GetItem(keys.Worktrees)
	}
	return values
}

func empty(values domain.StorageValues) bool {
	return values.Tasks == nil && values.Projects == nil && values.LastFolder == nil
}

func complete(values domain.StorageValues) bool {
	return values.Tasks != nil && values.Projects != nil && values.LastFolder != nil
}

// parseEnvelope is the values a version 2 envelope holds; ok false is a corrupt envelope.
func parseEnvelope(raw string) (values domain.StorageValues, ok bool) {
	var value map[string]jsontext.Value
	if err := jsonx.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return domain.StorageValues{}, false
	}
	field := func(name string, required bool) (*string, bool) {
		held, present := value[name]
		if !present {
			return nil, !required
		}
		switch held.Kind() {
		case 'n':
			return nil, true
		case '"':
			var text string
			if err := jsonx.Unmarshal(held, &text); err != nil {
				return nil, false
			}
			return &text, true
		}
		return nil, false
	}
	var fine [4]bool
	values.Tasks, fine[0] = field("tasks", true)
	values.Projects, fine[1] = field("projects", true)
	values.LastFolder, fine[2] = field("lastFolder", true)
	values.Worktrees, fine[3] = field("worktrees", false)
	return values, fine[0] && fine[1] && fine[2] && fine[3]
}

func corruptEnvelope(raw string) domain.ThreadStoreParseResult {
	return domain.ThreadStoreParseResultFalse{
		SourceVersion: 2,
		ErrorKind:     domain.ThreadStoreParseResultFalseErrorKindCorrupt,
		Errors:        []string{"version 2 task storage envelope is invalid"},
		PreservedV1:   nil,
		Raw:           domain.StorageValues{Tasks: &raw},
	}
}

// errorMessage is a thrown (panicked) value as text.
func errorMessage(err any) string {
	if err, ok := err.(error); ok {
		return err.Error()
	}
	return fmt.Sprint(err)
}

// catching runs work, answering what it panicked with, as TypeScript's catch takes a throw.
func catching(work func()) (failure any, failed bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure, failed = recovered, true
		}
	}()
	work()
	return nil, false
}
