package host

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// draftPrompt is one stored draft. Drafts are kept in the order they were stored, which decides
// which of several new-thread drafts is kept.
type draftPrompt struct {
	Owner string
	Text  string
}

var unreadableDrafts = errors.New("Saved draft text could not be read.")

// loadDraftPrompts reads the stored drafts, failing when they cannot be read.
func loadDraftPrompts(storage application.KeyValueStorage) ([]draftPrompt, error) {
	raw := storage.GetItem(application.DraftPromptsKey)
	if raw == nil {
		return nil, nil
	}
	var parsed any
	if err := jsonx.Unmarshal([]byte(*raw), &parsed); err != nil {
		return nil, err
	}
	if _, ok := parsed.(map[string]any); !ok {
		return nil, unreadableDrafts
	}
	// The object's members in the order they were written; a repeated name keeps its first place and last text.
	decoder := jsontext.NewDecoder(bytes.NewReader([]byte(*raw)), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	var prompts []draftPrompt
	place := map[string]int{}
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil {
			return nil, err
		}
		owner := name.String()
		value, err := decoder.ReadToken()
		if err != nil {
			return nil, err
		}
		if value.Kind() != '"' {
			return nil, unreadableDrafts
		}
		text := value.String()
		if index, seen := place[owner]; seen {
			prompts[index].Text = text
			continue
		}
		place[owner] = len(prompts)
		prompts = append(prompts, draftPrompt{Owner: owner, Text: text})
	}
	return withOneNewThreadDraft(prompts), nil
}

// withOneNewThreadDraft keeps the first of the new-thread drafts builds that kept one per project saved.
func withOneNewThreadDraft(prompts []draftPrompt) []draftPrompt {
	var kept []draftPrompt
	place := map[string]int{}
	for _, prompt := range prompts {
		key := prompt.Owner
		if strings.HasPrefix(key, application.NewThreadDraft) {
			key = application.NewThreadDraft
		}
		if index, seen := place[key]; seen {
			if kept[index].Text == "" {
				kept[index].Text = prompt.Text
			}
			continue
		}
		place[key] = len(kept)
		kept = append(kept, draftPrompt{Owner: key, Text: prompt.Text})
	}
	return kept
}

// draftOwners is whose drafts are known: the new thread's, and this computer's threads and every
// paired computer's once each has answered. Until then (nil) a draft may belong to a computer yet to
// answer, so none is nobody's.
func draftOwners(state *application.WorkspaceState) map[string]struct{} {
	held := []*application.WorkspaceState{state}
	for _, computer := range state.Computers.Paired {
		if computer.State == nil {
			return nil
		}
		held = append(held, computer.State)
	}
	owners := map[string]struct{}{application.NewThreadDraft: {}}
	for _, state := range held {
		for _, thread := range state.Threads {
			owners[thread.ID] = struct{}{}
		}
	}
	return owners
}

func saveDraftPrompts(storage application.KeyValueStorage, state *application.WorkspaceState) {
	temporary := sideChatIDs(state.SideChats)
	owners := draftOwners(state)
	kept := map[string]string{}
	for owner, text := range state.Prompts {
		if temporary[owner] {
			continue
		}
		if _, known := owners[owner]; owners != nil && !known {
			continue
		}
		kept[owner] = text
	}
	raw, err := jsonx.Marshal(kept)
	if err != nil {
		panic(err)
	}
	storage.SetItem(application.DraftPromptsKey, string(raw))
}

// draftSaveDelay is how long typing goes on before the drafts are written.
const draftSaveDelay = 250 * time.Millisecond

// DraftPersistence saves draft text off the typing path, and finishes the last write before quitting.
type DraftPersistence struct {
	mu         sync.Mutex
	storage    application.KeyValueStorage
	state      func() *application.WorkspaceState
	dispatch   func(ctx context.Context, input application.WorkspaceInput) error
	ready      bool
	generation int
	timer      *time.Timer
}

func CreateDraftPersistence(storage application.KeyValueStorage, state func() *application.WorkspaceState, dispatch func(ctx context.Context, input application.WorkspaceInput) error) *DraftPersistence {
	return &DraftPersistence{storage: storage, state: state, dispatch: dispatch}
}

// Restore brings every draft back; one nobody owns is dropped at the next save, once every paired
// computer has answered.
func (d *DraftPersistence) Restore(ctx context.Context) error {
	d.mu.Lock()
	d.generation++
	current := d.generation
	d.mu.Unlock()
	prompts, err := loadDraftPrompts(d.storage)
	if err != nil {
		return err
	}
	for _, prompt := range prompts {
		if !d.current(current) {
			return nil
		}
		if _, held := d.state().Prompts[prompt.Owner]; held {
			continue
		}
		if err := d.dispatch(ctx, contracts.ViewCommandViewSetPrompt{TaskID: &prompt.Owner, Prompt: prompt.Text}); err != nil {
			return err
		}
	}
	d.mu.Lock()
	if current == d.generation {
		d.ready = true
	}
	d.mu.Unlock()
	return nil
}

func (d *DraftPersistence) current(generation int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return generation == d.generation
}

// Changed saves the drafts shortly, once.
func (d *DraftPersistence) Changed() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.ready || d.timer != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(draftSaveDelay, func() {
		d.mu.Lock()
		if d.timer != timer {
			d.mu.Unlock()
			return
		}
		d.mu.Unlock()
		if failure := catching(d.Flush); failure != nil {
			go d.dispatch(background, contracts.ActionFailedEvent{Message: "Could not save draft text: " + contracts.ErrorMessage(failure)})
		}
	})
	d.timer = timer
}

// catching runs work, answering with what it panicked with.
func catching(work func()) (failure any) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if err, ok := recovered.(error); ok {
				failure = err
			} else {
				failure = fmt.Errorf("%v", recovered)
			}
		}
	}()
	work()
	return nil
}

// Flush writes the drafts now.
func (d *DraftPersistence) Flush() {
	d.mu.Lock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	ready := d.ready
	d.mu.Unlock()
	if ready {
		saveDraftPrompts(d.storage, d.state())
	}
}

func (d *DraftPersistence) Dispose() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.generation++
	d.ready = false
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}

func CreateTaskStore(storage application.KeyValueStorage) *application.TaskStore {
	return application.NewTaskStore(storage)
}
