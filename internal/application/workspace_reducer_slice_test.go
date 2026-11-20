package application

import (
	"reflect"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

func TestAfterwardKeepsUnchangedState(t *testing.T) {
	state := &WorkspaceState{Focused: true, ViewingGallery: []string{}}
	if next := Afterward(state, SliceTransition{Edits: []OwnerEdit{FocusedIn(DesktopOf(state), true)}}); next != state {
		t.Fatal("an edit that changes nothing made a new state")
	}
	next := Afterward(state, SliceTransition{Edits: []OwnerEdit{FocusedIn(DesktopOf(state), false)}})
	if next == state || next.Focused || !state.Focused {
		t.Fatal("edit not applied as a copy")
	}
}

func TestUnsupersededKeepsWitness(t *testing.T) {
	base := &WorkspaceState{Focused: true}
	first := Step{Edits: []OwnerEdit{FocusedIn(DesktopOf(base), false)}}
	joined := AndThen(base, first, func(state *WorkspaceState) Step {
		return Step{Edits: []OwnerEdit{FocusedIn(DesktopOf(state), true)}}
	})
	if len(joined.Edits) != 2 {
		t.Fatalf("changed-and-restored owner should keep its witness, got %d edits", len(joined.Edits))
	}
	joined = AndThen(base, Step{Edits: []OwnerEdit{FocusedIn(DesktopOf(base), true)}}, func(state *WorkspaceState) Step {
		return Step{Edits: []OwnerEdit{FocusedIn(DesktopOf(state), false)}}
	})
	if len(joined.Edits) != 1 {
		t.Fatalf("superseded edit kept: %d edits", len(joined.Edits))
	}
}

func TestAppliedRefusal(t *testing.T) {
	state := &WorkspaceState{}
	refusal := "no"
	transition := Applied(state, SliceTransition{Refusal: &refusal, Effects: []WorkspaceEffect{WorkspaceEffectCloseWindow{}}})
	if transition.State.ActionError == nil || *transition.State.ActionError != "no" {
		t.Fatal("refusal not shown")
	}
	if result, ok := transition.Result.(contracts.WorkspaceCommandResultFalse); !ok || result.Message != "no" {
		t.Fatal("refusal not answered")
	}
	if !Failed(Step{Refusal: &refusal}) {
		t.Fatal("refusal not failed")
	}
}

func TestFollowUpsAndInputType(t *testing.T) {
	marked := FollowUps([]WorkspaceEffect{WorkspaceEffectCloseWindow{}})
	if effect := marked[0].(WorkspaceEffectCloseWindow); effect.FollowUp == nil || !*effect.FollowUp {
		t.Fatal("follow-up not marked")
	}
	if got := InputType(WorkspaceEventStoreAbsent{}); got != "store.absent" {
		t.Fatalf("InputType = %q", got)
	}
}

func TestEveryOwnerViewMapsOntoWorkspaceState(t *testing.T) {
	state := EmptyWorkspaceState(nil)
	for _, edit := range []OwnerEdit{
		AutomationsEdit{}, BrowserEdit{}, ComposerEdit{}, ComputerUseEdit{}, ComputersEdit{}, DesktopEdit{}, DialogsEdit{}, DocksEdit{},
		EnginesEdit{}, GitEdit{}, NavigationEdit{}, ProjectsEdit{}, RunsEdit{}, SettingsEdit{}, SideChatsEdit{}, ThreadsEdit{}, ToastsEdit{}, WorktreesEdit{},
	} {
		view := reflect.TypeOf(edit.EditView())
		if len(viewFieldsOf(view)) == 0 {
			t.Errorf("%s has no fields", view)
		}
		copied := *state
		writeView(&copied, reflect.Zero(view).Interface())
		if differs(edit, &copied) {
			t.Errorf("%s does not read back what it wrote", view)
		}
	}
}
