package application

import (
	"reflect"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// OwnPatch writes a slice's own fields into the copy of the state its transition makes. It must
// write only fields of the slice that returns it, and never into values the previous state reaches.
type OwnPatch func(next *WorkspaceState)

// OwnerEdit is one owner's state as an input leaves it: the TypeScript owners' Change classes.
// EditView returns the owner's view, a struct whose fields (embedded ones included) are
// WorkspaceState fields of the same name. Two edits of the same dynamic type have the same owner.
type OwnerEdit interface {
	EditView() any
}

func (e ToastsEdit) EditView() any      { return e.View }
func (e DocksEdit) EditView() any       { return e.View }
func (e ComposerEdit) EditView() any    { return e.View }
func (e EnginesEdit) EditView() any     { return e.View }
func (e ComputersEdit) EditView() any   { return e.View }
func (e ProjectsEdit) EditView() any    { return e.View }
func (e AutomationsEdit) EditView() any { return e.View }
func (e BrowserEdit) EditView() any     { return e.View }
func (e ComputerUseEdit) EditView() any { return e.View }
func (e DesktopEdit) EditView() any     { return e.View }
func (e DialogsEdit) EditView() any     { return e.View }
func (e GitEdit) EditView() any         { return e.View }
func (e SettingsEdit) EditView() any    { return e.View }
func (e NavigationEdit) EditView() any  { return e.View }
func (e RunsEdit) EditView() any        { return e.View }
func (e SideChatsEdit) EditView() any   { return e.View }
func (e WorktreesEdit) EditView() any   { return e.View }

// SliceError puts up, or takes down (nil Message), the error the window shows and the settings page that clears it.
type SliceError struct {
	Message *string
	Page    *domain.SettingsSection
}

// SliceTransition is how a slice's reducer answers when it changes only the state it owns
// (TypeScript's generic SliceTransition<Own, Facet>; Own and Facet are type-level only).
type SliceTransition struct {
	Patch OwnPatch
	// Joined in after the patch, in this order. One that changes nothing leaves the state as it was.
	Edits   []OwnerEdit
	Effects []WorkspaceEffect
	Result  contracts.WorkspaceCommandResult
	// Turns the input down, saying why where the window shows errors.
	Refusal *string
	// Takes down the error the window shows, which belongs to no slice that asks.
	ClearsError bool
	// Puts up, or takes down, the error the window shows and the settings page that clears it, without refusing the input.
	Error *SliceError
	// Closes whatever menu is open, which belongs to no slice that asks.
	ClosesMenu bool
	// Saves the window's preferences as they stand once the patch is in, after the slice's own effects.
	Persists bool
}

// Step is what one owner's operation changes: a SliceTransition whose Patch is always nil.
// Convert freely with SliceTransition(step) and Step(transition).
type Step SliceTransition

// OwnFields is every field of an owner's view, taken from state by name.
func OwnFields[View any](state *WorkspaceState) View {
	var view View
	target := reflect.ValueOf(&view).Elem()
	source := reflect.ValueOf(state).Elem()
	for _, field := range viewFieldsOf(target.Type()) {
		into := target.FieldByIndex(field.view)
		into.Set(source.Field(field.state).Convert(into.Type()))
	}
	return view
}

// AndThen is first, then what next makes of the state it leaves, as one answer: edits and effects in
// that order, the later error and result standing. A refusal ends it there.
func AndThen(state *WorkspaceState, first Step, next func(state *WorkspaceState) Step) Step {
	return InTurn(state, first, []func(*WorkspaceState) Step{next})
}

// InTurn is AndThen for any number of steps, each made on the state the ones before it leave.
func InTurn(state *WorkspaceState, first Step, next []func(state *WorkspaceState) Step) Step {
	current := state
	last := first
	joined := first
	for _, step := range next {
		if joined.Refusal != nil {
			return joined
		}
		current = Afterward(current, SliceTransition(last))
		last = step(current)
		joined = followed(joined, last, state)
	}
	return joined
}

func followed(first, second Step, base *WorkspaceState) Step {
	var errorShown *SliceError
	switch {
	case second.Error != nil:
		errorShown = second.Error
	case second.ClearsError && first.Error != nil:
		errorShown = &SliceError{Page: first.Error.Page}
	case second.ClearsError:
	default:
		errorShown = first.Error
	}
	result := second.Result
	if result == nil {
		result = first.Result
	}
	joined := Step{
		Error:      errorShown,
		Result:     result,
		Refusal:    second.Refusal,
		ClosesMenu: first.ClosesMenu || second.ClosesMenu,
		Persists:   first.Persists || second.Persists,
	}
	if errorShown == nil && (first.ClearsError || second.ClearsError) {
		joined.ClearsError = true
	}
	joined.Edits = slices.Concat(unsuperseded(first.Edits, second.Edits, base), second.Edits)
	joined.Effects = slices.Concat(first.Effects, second.Effects)
	return joined
}

// unsuperseded is earlier without the edits one of the same owner in later replaces, since each holds
// every field of its owner. Where every later edit of an owner leaves it as base has it, the last
// earlier edit that changed it stays, so a state changed and put back still comes out new.
func unsuperseded(earlier, later []OwnerEdit, base *WorkspaceState) []OwnerEdit {
	if len(earlier) == 0 || len(later) == 0 {
		return earlier
	}
	restoring := map[reflect.Type]bool{}
	for _, edit := range later {
		owner := reflect.TypeOf(edit)
		held, seen := restoring[owner]
		restoring[owner] = (!seen || held) && !differs(edit, base)
	}
	if !slices.ContainsFunc(earlier, func(edit OwnerEdit) bool { _, ok := restoring[reflect.TypeOf(edit)]; return ok }) {
		return earlier
	}
	witnesses := map[reflect.Type]int{}
	for index, edit := range earlier {
		if owner := reflect.TypeOf(edit); restoring[owner] && differs(edit, base) {
			witnesses[owner] = index
		}
	}
	kept := make([]OwnerEdit, 0, len(earlier))
	for index, edit := range earlier {
		owner := reflect.TypeOf(edit)
		if _, replaced := restoring[owner]; !replaced {
			kept = append(kept, edit)
		} else if witness, ok := witnesses[owner]; ok && witness == index {
			kept = append(kept, edit)
		}
	}
	return kept
}

func differs(edit OwnerEdit, state *WorkspaceState) bool {
	view := reflect.ValueOf(edit.EditView())
	source := reflect.ValueOf(state).Elem()
	for _, field := range viewFieldsOf(view.Type()) {
		if !sameValue(view.FieldByIndex(field.view), source.Field(field.state)) {
			return true
		}
	}
	return false
}

// Failed says whether a step turned its input down, refusing it or answering that it failed.
func Failed(step Step) bool {
	if step.Refusal != nil {
		return true
	}
	_, failed := step.Result.(contracts.WorkspaceCommandResultFalse)
	return failed
}

// Afterward is the state a slice's answer leaves, for reading what its own effects describe.
// Effects, Result, Refusal and Persists are ignored.
func Afterward(state *WorkspaceState, transition SliceTransition) *WorkspaceState {
	next := state
	if transition.Patch != nil || transition.ClearsError || transition.Error != nil || transition.ClosesMenu {
		copied := *state
		if transition.Patch != nil {
			transition.Patch(&copied)
		}
		if transition.ClearsError {
			copied.ActionError = nil
		}
		if transition.Error != nil {
			copied.ActionError = transition.Error.Message
			copied.ActionErrorPage = transition.Error.Page
		}
		if transition.ClosesMenu {
			copied.OpenMenu = nil
		}
		next = &copied
	}
	for _, edit := range transition.Edits {
		if differs(edit, next) {
			copied := *next
			writeView(&copied, edit.EditView())
			next = &copied
		}
	}
	return next
}

// Owned is a slice's reducer run on the whole state, with its patch joined in.
func Owned[In any](reducer func(state *WorkspaceState, input In) SliceTransition) func(state *WorkspaceState, input In) WorkspaceTransition {
	return func(state *WorkspaceState, input In) WorkspaceTransition {
		return Applied(state, reducer(state, input))
	}
}

// Applied is a slice's answer joined into the state it was given.
func Applied(state *WorkspaceState, transition SliceTransition) WorkspaceTransition {
	next := Afterward(state, transition)
	if transition.Refusal != nil {
		copied := *next
		copied.ActionError = transition.Refusal
		next = &copied
	}
	effects := transition.Effects
	if transition.Persists {
		effects = slices.Concat(effects, []WorkspaceEffect{WorkspaceEffectPersistPreferences{Preferences: ViewPreferences(next)}})
	}
	result := transition.Result
	if transition.Refusal != nil {
		result = contracts.WorkspaceCommandResultFalse{Message: *transition.Refusal}
	}
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	return WorkspaceTransition{State: *next, Effects: effects, Result: result}
}

// viewField maps one field of an owner's view to the WorkspaceState field of the same name.
type viewField struct {
	view  []int
	state int
}

var viewFields sync.Map // reflect.Type -> []viewField

func viewFieldsOf(view reflect.Type) []viewField {
	if held, ok := viewFields.Load(view); ok {
		return held.([]viewField)
	}
	stateType := reflect.TypeFor[WorkspaceState]()
	var fields []viewField
	for _, field := range reflect.VisibleFields(view) {
		if field.Anonymous || !field.IsExported() {
			continue
		}
		target, ok := stateType.FieldByName(field.Name)
		// A string-literal union is named for each declaration holding it, so a view may hold the state's under another name.
		if !ok || len(target.Index) != 1 || target.Type != field.Type && !(target.Type.Kind() == reflect.String && field.Type.Kind() == reflect.String) {
			panic("application: view field " + view.String() + "." + field.Name + " is not a WorkspaceState field")
		}
		fields = append(fields, viewField{view: field.Index, state: target.Index[0]})
	}
	viewFields.Store(view, fields)
	return fields
}

func writeView(state *WorkspaceState, view any) {
	source := reflect.ValueOf(view)
	target := reflect.ValueOf(state).Elem()
	for _, field := range viewFieldsOf(source.Type()) {
		into := target.Field(field.state)
		into.Set(source.FieldByIndex(field.view).Convert(into.Type()))
	}
}

// sameValue is TypeScript's === on state values: jsonx.Same, with struct values (objects held
// inline) the same when every field is.
func sameValue(a, b reflect.Value) bool {
	if a.Kind() == reflect.Interface {
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		a, b = a.Elem(), b.Elem()
		if a.Type() != b.Type() {
			return false
		}
	}
	switch a.Kind() {
	case reflect.Slice:
		return a.Len() == b.Len() && a.UnsafePointer() == b.UnsafePointer()
	case reflect.Map, reflect.Pointer, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.UnsafePointer() == b.UnsafePointer()
	case reflect.Struct:
		for index := range a.NumField() {
			if !sameValue(a.Field(index), b.Field(index)) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return a.Bool() == b.Bool()
	case reflect.String:
		return a.String() == b.String()
	case reflect.Float32, reflect.Float64:
		return a.Float() == b.Float()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() == b.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() == b.Uint()
	}
	return false
}
