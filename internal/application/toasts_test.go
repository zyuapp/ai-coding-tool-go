package application

import (
	"fmt"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func threadsToastIDs(toasts []*domain.Toast) []float64 {
	ids := make([]float64, len(toasts))
	for index, toast := range toasts {
		ids[index] = toast.ID
	}
	return ids
}

func TestToastsStackAndDismiss(t *testing.T) {
	corePin(t, 1000)
	state := coreWorkspace()
	for index := range domain.MaxToasts + 1 {
		state, _ = WithToast(state, &domain.Toast{Tone: domain.ToastToneSuccess, Title: "t", Message: new(fmt.Sprint(index))}, Now())
	}
	coreEqualJSON(t, threadsToastIDs(state.Toasts), []float64{2, 3, 4, 5}, "the newest pushes the oldest out")
	dismissed := Applied(state, ReduceToasts(state, contracts.ViewCommandViewDismissToast{ID: 2})).State
	coreEqualJSON(t, threadsToastIDs(dismissed.Toasts), []float64{3, 4, 5}, "dismissed")
	if again := ReduceToasts(&dismissed, contracts.ViewCommandViewDismissToast{ID: 2}); again.Patch != nil {
		t.Fatal("a toast already gone changed the state")
	}
}

func TestHeldToastsWaitAndLettingGoRestartsThem(t *testing.T) {
	corePin(t, 1000)
	launched, effects := WithToast(coreWorkspace(), &domain.Toast{Tone: domain.ToastToneSuccess, Title: "updated"}, Now())
	if len(effects) != 1 || effects[0].At == nil || *effects[0].At != 1000+domain.ToastLifetimeMs {
		t.Fatalf("raised: %s", coreJSON(t, effects))
	}
	held := Applied(launched, ReduceToasts(launched, contracts.ViewCommandViewHoldToasts{Held: true}))
	if !held.State.ToastsHeld {
		t.Fatal("not held")
	}
	coreEqualJSON(t, held.Effects, `[{"type":"schedule-toast-dismissal","id":1,"at":null}]`, "holding stops the time")
	if again := ReduceToasts(&held.State, contracts.ViewCommandViewHoldToasts{Held: true}); again.Patch != nil || len(again.Effects) != 0 {
		t.Fatal("holding again changed something")
	}
	raised, raisedEffects := WithToast(&held.State, &domain.Toast{Tone: domain.ToastToneSuccess, Title: "again"}, Now())
	if len(raisedEffects) != 0 || len(raised.Toasts) != 2 {
		t.Fatalf("raised while held: %s", coreJSON(t, raisedEffects))
	}
	released := Applied(raised, ReduceToasts(raised, contracts.ViewCommandViewHoldToasts{Held: false}))
	if released.State.ToastsHeld || len(released.Effects) != 2 {
		t.Fatalf("released: %s", coreJSON(t, released.Effects))
	}
	for _, effect := range released.Effects {
		if effect.(WorkspaceEffectScheduleToastDismissal).At == nil {
			t.Fatal("a released toast has no time")
		}
	}
}

func TestResultReplacesItsProgressToast(t *testing.T) {
	corePin(t, 1000)
	held := coreWorkspace(func(state *WorkspaceState) { state.ToastsHeld = true })
	progress, _ := WithToast(held, &domain.Toast{Tone: domain.ToastToneProgress, Title: "Updating Codex", Subject: new("codex"), Persistent: new(true)}, Now())
	result, effects := WithToast(progress, &domain.Toast{Tone: domain.ToastToneSuccess, Title: "Codex updated", Subject: new("codex")}, Now())
	if len(result.Toasts) != 1 || result.Toasts[0].Title != "Codex updated" || result.Toasts[0].ID != 1 || result.Toasts[0].Persistent != nil {
		t.Fatalf("replaced: %s", coreJSON(t, result.Toasts))
	}
	coreEqualJSON(t, ToastEffects(effects), `[{"type":"schedule-toast-dismissal","id":1,"at":null}]`, "held replacement")
}

func TestClosedRememberedToastIsPersisted(t *testing.T) {
	corePorted(t, func() {
		state, _ := WithToast(coreWorkspace(), &domain.Toast{Tone: domain.ToastToneUpdate, Title: "offer", Remember: new("engine-update:claude@2.1.300"), Persistent: new(true)}, 1)
		closed := Applied(state, ReduceToasts(state, contracts.ViewCommandViewDismissToast{ID: 1}))
		coreEqualJSON(t, closed.State.DismissedToasts, []string{"engine-update:claude@2.1.300"}, "remembered")
		persisted := coreEffect[WorkspaceEffectPersistPreferences](t, closed)
		coreEqualJSON(t, persisted.Preferences.DismissedToasts, []string{"engine-update:claude@2.1.300"}, "persisted")
	})
}

func TestZenShowsOnlyErrorToasts(t *testing.T) {
	state, _ := WithToast(coreWorkspace(), &domain.Toast{Tone: domain.ToastToneSuccess, Title: "fine"}, 1)
	state, _ = WithToast(state, &domain.Toast{Tone: domain.ToastToneError, Title: "broken"}, 1)
	if got := VisibleToasts(state); len(got) != 2 {
		t.Fatal("not zen")
	}
	zen := *state
	zen.Zen = true
	first := VisibleToasts(&zen)
	if len(first) != 1 || first[0].Title != "broken" {
		t.Fatalf("zen: %s", coreJSON(t, first))
	}
	if again := VisibleToasts(&zen); &again[0] != &first[0] {
		t.Fatal("a redraw built a new list")
	}
}
