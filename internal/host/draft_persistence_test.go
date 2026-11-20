package host

import (
	"context"
	"maps"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var drafts = map[string]string{"remote-thread": "typed for elsewhere", "gone-thread": "for a thread deleted", "draft:": "loose"}

type restoredDrafts struct {
	drafts  *DraftPersistence
	storage *memoryStorage
	state   func() *application.WorkspaceState
}

func (r restoredDrafts) saved(t *testing.T) map[string]string {
	t.Helper()
	saved := map[string]string{}
	if raw := r.storage.GetItem(application.DraftPromptsKey); raw != nil {
		if err := jsonx.Unmarshal([]byte(*raw), &saved); err != nil {
			t.Fatal(err)
		}
	}
	return saved
}

func restoreDrafts(t *testing.T, initial *application.WorkspaceState, raw string) restoredDrafts {
	t.Helper()
	storage := newMemoryStorage(map[string]string{application.DraftPromptsKey: raw})
	state := initial
	persistence := CreateDraftPersistence(storage, func() *application.WorkspaceState { return state }, func(_ context.Context, input application.WorkspaceInput) error {
		set, ok := input.(contracts.ViewCommandViewSetPrompt)
		if !ok || set.TaskID == nil {
			return nil
		}
		next := *state
		next.Prompts = maps.Clone(state.Prompts)
		if next.Prompts == nil {
			next.Prompts = map[string]string{}
		}
		next.Prompts[*set.TaskID] = set.Prompt
		state = &next
		return nil
	})
	if err := persistence.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	return restoredDrafts{persistence, storage, func() *application.WorkspaceState { return state }}
}

func encoded(t *testing.T, value any) string {
	raw, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEveryDraftComesBackAndNobodysIsDroppedOnceEveryComputerAnswered(t *testing.T) {
	withProject := func(state *application.WorkspaceState) { state.Projects = []*domain.Project{project} }
	linux := domain.ComputerLink{ID: "linux", Name: "linux", Host: "linux.tail.ts.net", Status: domain.ComputerStatusConnected, PairedAt: 1}

	alone := restoreDrafts(t, workspace(withProject), encoded(t, drafts))
	if !maps.Equal(alone.state().Prompts, drafts) {
		t.Fatalf("prompts = %v", alone.state().Prompts)
	}
	alone.drafts.Flush()
	if saved := alone.saved(t); !maps.Equal(saved, map[string]string{"draft:": "loose"}) {
		t.Fatalf("saved = %v", saved)
	}

	waiting := restoreDrafts(t, workspace(withProject, func(state *application.WorkspaceState) {
		state.Computers.Paired = []*application.PairedComputer{{ComputerLink: linux}}
	}), encoded(t, drafts))
	waiting.drafts.Flush()
	if saved := waiting.saved(t); !maps.Equal(saved, drafts) {
		t.Fatalf("with a computer yet to answer, a draft was dropped: %v", saved)
	}

	remote := workspace(func(state *application.WorkspaceState) { state.Threads = []*domain.Thread{task("remote-thread")} })
	answered := restoreDrafts(t, workspace(withProject, func(state *application.WorkspaceState) {
		state.Computers.Paired = []*application.PairedComputer{{ComputerLink: linux, State: remote}}
	}), encoded(t, drafts))
	answered.drafts.Flush()
	if saved := answered.saved(t); !maps.Equal(saved, map[string]string{"remote-thread": "typed for elsewhere", "draft:": "loose"}) {
		t.Fatalf("saved = %v", saved)
	}
}

func TestNewThreadDraftsSavedPerProjectComeBackAsOne(t *testing.T) {
	withProject := func(state *application.WorkspaceState) { state.Projects = []*domain.Project{project} }
	legacy := restoreDrafts(t, workspace(withProject), `{"draft:project-a":"for a folder here","draft:gone":"for a folder removed"}`)
	if !maps.Equal(legacy.state().Prompts, map[string]string{"draft:": "for a folder here"}) {
		t.Fatalf("prompts = %v", legacy.state().Prompts)
	}
	empty := restoreDrafts(t, workspace(withProject), `{"draft:":"","draft:project-a":"for a folder here"}`)
	if !maps.Equal(empty.state().Prompts, map[string]string{"draft:": "for a folder here"}) {
		t.Fatalf("prompts = %v", empty.state().Prompts)
	}
}

func TestUnreadableDraftsFailTheRestore(t *testing.T) {
	for _, raw := range []string{`["text"]`, `{"a":1}`, `null`} {
		storage := newMemoryStorage(map[string]string{application.DraftPromptsKey: raw})
		persistence := CreateDraftPersistence(storage, func() *application.WorkspaceState { return workspace() }, func(context.Context, application.WorkspaceInput) error { return nil })
		if err := persistence.Restore(context.Background()); err == nil || err.Error() != "Saved draft text could not be read." {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}
