package application

import (
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The cards in the window's corner. Each one schedules its own leaving as it arrives, unless it waits
// for the user, and the user can close any of them sooner.

// The toasts as one input leaves them. Only the functions here make one, so a slice changes the toasts through them.
type toastsChange = ToastsEdit

func ToastsOf(state *WorkspaceState) ToastsEdit {
	return ToastsEdit{View: ToastsState{Toasts: state.Toasts, ToastSequence: state.ToastSequence, ToastsHeld: state.ToastsHeld, DismissedToasts: state.DismissedToasts}}
}

// WithToast, on the toasts an input is changing. `toast.ID` is ignored (TS: Omit<Toast, "id">).
func ToastIn(edit ToastsEdit, toast *domain.Toast, now float64) (ToastsEdit, []*ToastEffect) {
	view, effects := raisedToast(edit.View, toast, now)
	return ToastsEdit{View: view}, effects
}

// ToastEffects is toast effects as the workspace effects they are.
func ToastEffects(effects []*ToastEffect) []WorkspaceEffect {
	converted := make([]WorkspaceEffect, len(effects))
	for index, effect := range effects {
		converted[index] = WorkspaceEffectScheduleToastDismissal{ID: effect.ID, At: effect.At, FollowUp: effect.FollowUp}
	}
	return converted
}

// The pointer or the caret left the toasts, so each leaves on its own time again.
func ReleasedToasts(edit ToastsEdit) ToastsEdit {
	if !edit.View.ToastsHeld {
		return edit
	}
	view := edit.View
	view.ToastsHeld = false
	return ToastsEdit{View: view}
}

// When a toast leaves on its own, or nil for one that waits for the user or is held.
func leaving(held bool, toast *domain.Toast, now float64) *float64 {
	if toast.Persistent != nil && *toast.Persistent || held {
		return nil
	}
	at := now + domain.ToastLifetimeMs
	return &at
}

// Puts a toast up with the effect that takes it down. One about the same subject as a toast still up
// takes that toast's place and its time, so a result replaces the progress it ends; any other joins
// the stack, pushing the oldest out past the most the corner holds. `toast.ID` is ignored.
func WithToast(state *WorkspaceState, toast *domain.Toast, now float64) (*WorkspaceState, []*ToastEffect) {
	view, effects := raisedToast(ToastsOf(state).View, toast, now)
	next := *state
	next.Toasts, next.ToastSequence = view.Toasts, view.ToastSequence
	return &next, effects
}

// WithToast, answered as the toasts' own state.
func raisedToast(state ToastsState, toast *domain.Toast, now float64) (ToastsState, []*ToastEffect) {
	var replaced *domain.Toast
	if toast.Subject != nil && *toast.Subject != "" {
		for _, item := range state.Toasts {
			if item.Subject != nil && *item.Subject == *toast.Subject {
				replaced = item
				break
			}
		}
	}
	if replaced != nil {
		toasts := make([]*domain.Toast, len(state.Toasts))
		for index, item := range state.Toasts {
			if item == replaced {
				put := *toast
				put.ID = replaced.ID
				item = &put
			}
			toasts[index] = item
		}
		state.Toasts = toasts
		return state, []*ToastEffect{{ID: replaced.ID, At: leaving(state.ToastsHeld, toast, now)}}
	}
	id := state.ToastSequence + 1
	put := *toast
	put.ID = id
	toasts := append(slices.Clip(state.Toasts), &put)
	state.Toasts = slices.Clip(toasts[max(0, len(toasts)-domain.MaxToasts):])
	state.ToastSequence = id
	at := leaving(state.ToastsHeld, toast, now)
	if at == nil {
		return state, []*ToastEffect{}
	}
	return state, []*ToastEffect{{ID: id, At: at}}
}

// Gives every toast that leaves on its own its full time again from now. A toast raised while the
// app was starting would otherwise spend its time before the window had drawn it.
func RescheduledToasts(toasts []*domain.Toast, held bool, now float64) []*ToastEffect {
	effects := []*ToastEffect{}
	for _, toast := range toasts {
		if toast.Persistent != nil && *toast.Persistent {
			continue
		}
		effects = append(effects, &ToastEffect{ID: toast.ID, At: leaving(held, toast, now)})
	}
	return effects
}

// The errors of the last toasts list zen mode showed, so a redraw keeps the same list.
var zenToasts struct {
	sync.Mutex
	toasts []*domain.Toast
	errors []*domain.Toast
}

// The toasts the corner shows: all of them, or in zen mode only the errors, held so a redraw keeps the same list.
func VisibleToasts(state *WorkspaceState) []*domain.Toast {
	if !state.Zen {
		return state.Toasts
	}
	zenToasts.Lock()
	defer zenToasts.Unlock()
	if zenToasts.errors != nil && jsonx.Same(zenToasts.toasts, state.Toasts) {
		return zenToasts.errors
	}
	errors := []*domain.Toast{}
	for _, toast := range state.Toasts {
		if toast.Tone == domain.ToastToneError {
			errors = append(errors, toast)
		}
	}
	zenToasts.toasts, zenToasts.errors = state.Toasts, errors
	return errors
}

// `input` is a "view.dismiss-toast" or "view.hold-toasts" command.
func ReduceToasts(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.ViewCommandViewHoldToasts:
		if input.Held == state.ToastsHeld {
			return SliceTransition{}
		}
		return SliceTransition{
			Patch:   func(next *WorkspaceState) { next.ToastsHeld = input.Held },
			Effects: ToastEffects(RescheduledToasts(state.Toasts, input.Held, Now())),
		}
	case contracts.ViewCommandViewDismissToast:
		index := slices.IndexFunc(state.Toasts, func(item *domain.Toast) bool { return item.ID == input.ID })
		if index < 0 {
			return SliceTransition{}
		}
		toast := state.Toasts[index]
		toasts := slices.DeleteFunc(slices.Clone(state.Toasts), func(item *domain.Toast) bool { return item == toast })
		if toast.Remember == nil || *toast.Remember == "" || slices.Contains(state.DismissedToasts, *toast.Remember) {
			return SliceTransition{Patch: func(next *WorkspaceState) { next.Toasts = toasts }}
		}
		dismissed := append(slices.Clip(state.DismissedToasts), *toast.Remember)
		dismissed = slices.Clip(dismissed[max(0, len(dismissed)-domain.MaxRememberedToasts):])
		return SliceTransition{
			Patch: func(next *WorkspaceState) {
				next.Toasts = toasts
				next.DismissedToasts = dismissed
			},
			Persists: true,
		}
	}
	return SliceTransition{}
}
