package application

import (
	"reflect"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// findKey: the find bar is one bar whatever it searches, so its inputs follow each other.
const findKey = "view:find"

// ScreenOf is what the window shows: whose computer, which thread or draft there, and which dock tab holds the keyboard.
func ScreenOf(state *WorkspaceState) string {
	var shownCurrent, shownTab string
	if shown := SelectedComputer(state); shown != nil && shown.State != nil {
		shownCurrent = deref(shown.State.CurrentID)
		shownTab = deref(shown.State.KeyboardTab)
	}
	return strings.Join([]string{deref(state.Computers.Active), deref(state.CurrentID), deref(state.KeyboardTab), shownCurrent, shownTab}, "\x00")
}

// implicit is the commands that act on the thread on screen when they name none.
var implicit = jsonx.SetOf(
	"view.set-prompt", "task.send", "task.fork", "task.set-worktree", "task.move-worktree", "run.compact", "run.cancel", "review.start", "worktree.delete",
)

// InputScopeInputScope is what an input must follow before it may run, read from the state it arrived in.
func InputScopeInputScope(state *WorkspaceState, input WorkspaceInput) InputScope {
	named := WithNamedThread(state, input)
	keys := jsonx.Set[string]{}
	if collect(state, named, keys) {
		screen := ScreenOf(state)
		return InputScope{Input: named, Keys: keys, Screen: &screen}
	}
	return InputScope{Input: named, Keys: keys}
}

// WithNamedThread names the current thread in a command that would otherwise act on whatever thread is current when it runs.
func WithNamedThread(state *WorkspaceState, input WorkspaceInput) WorkspaceInput {
	if state.CurrentID == nil || state.Computers.Active != nil {
		return input
	}
	current := *state.CurrentID
	switch input := input.(type) {
	case contracts.ViewCommandViewSetPrompt:
		if input.TaskID == nil {
			input.TaskID = &current
		}
		return input
	case contracts.TaskCommandTaskSend:
		if input.Text != nil {
			return input
		}
		if input.TaskID == nil && isThread(state, current) {
			input.TaskID = &current
		}
		return input
	case contracts.ViewCommandViewFindOpen:
		if input.Target != nil {
			return input
		}
		if state.Find != nil {
			input.Target = state.Find.Target
		} else {
			input.Target = FindTargetFor(state, domain.ShortcutSurfaceAny)
		}
		return input
	}
	switch InputType(input) {
	case "task.fork", "task.set-worktree", "task.move-worktree", "run.compact", "run.cancel", "review.start", "worktree.delete":
		if _, _, defined := inputTaskID(input); !defined && isThread(state, current) {
			return withInputTaskID(input, current)
		}
	}
	return input
}

func isThread(state *WorkspaceState, taskID string) bool {
	return slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool { return thread.ID == taskID })
}

// collect adds the keys an input follows, and says whether it acts on the thread on screen without naming it.
func collect(state *WorkspaceState, input WorkspaceInput, keys jsonx.Set[string]) bool {
	switch input := input.(type) {
	case contracts.TaskCommandTaskSelect, contracts.WorktreeCommandWorktreeOpenThread, contracts.ViewCommandViewJumpChoose:
		return false
	case WorkspaceEventAgentEvents:
		for _, event := range input.Events {
			if taskID, ok, _ := inputTaskID(event); ok {
				keys[taskID] = struct{}{}
			}
		}
		return false
	case WorkspaceEventRunEvent:
		if taskID, ok, _ := inputTaskID(input.Event); ok {
			keys[taskID] = struct{}{}
		}
		return false
	case WorkspaceEventThreadEvent:
		if taskID, ok, _ := inputTaskID(input.Event); ok {
			keys[taskID] = struct{}{}
		}
		return false
	case contracts.ViewCommandViewFindOpen, contracts.ViewCommandViewFindQuery, contracts.ViewCommandViewFindStep, contracts.ViewCommandViewFindClose:
		keys[findKey] = struct{}{}
		var target domain.FindTarget
		if open, ok := input.(contracts.ViewCommandViewFindOpen); ok {
			target = open.Target
		} else if state.Find != nil {
			target = state.Find.Target
		}
		if thread, ok := target.(domain.FindTargetThread); ok && thread.TaskID != nil && *thread.TaskID != "" {
			keys[*thread.TaskID] = struct{}{}
		}
		return false
	case contracts.SideChatCommandSideChatOpen, contracts.ViewCommandViewMoveWorktree:
		if state.CurrentID != nil && *state.CurrentID != "" {
			keys[*state.CurrentID] = struct{}{}
		}
		return true
	case contracts.ViewCommandViewForkConfirm:
		if state.ForkName != nil {
			keys[state.ForkName.TaskID] = struct{}{}
		}
		return false
	case contracts.ViewCommandViewArchiveConfirm:
		if state.ArchivePrompt != nil && *state.ArchivePrompt != "" {
			keys[*state.ArchivePrompt] = struct{}{}
		}
		return false
	case contracts.ViewCommandViewShortcut, contracts.ViewCommandViewEscape:
		var commands []contracts.AppCommand
		if shortcut, ok := input.(contracts.ViewCommandViewShortcut); ok {
			commands = ShortcutCommands(state, shortcut.Action, shortcut.Surface)
		} else {
			commands = EscapeCommands(state)
		}
		for _, command := range commands {
			collect(state, WithNamedThread(state, command), keys)
		}
		return true
	}
	taskID, isString, defined := inputTaskID(input)
	if isString {
		keys[taskID] = struct{}{}
	}
	if defined || !implicit.Has(InputType(input)) {
		return false
	}
	if send, ok := input.(contracts.TaskCommandTaskSend); ok && send.Text != nil {
		return false
	}
	return true
}

func deref[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}

// inputTaskID is a value's taskId field: its string when it holds one, and whether it is there at all.
func inputTaskID(value any) (taskID string, isString, defined bool) {
	held := reflect.ValueOf(value)
	for held.IsValid() && (held.Kind() == reflect.Pointer || held.Kind() == reflect.Interface) {
		if held.IsNil() {
			return "", false, false
		}
		held = held.Elem()
	}
	if !held.IsValid() || held.Kind() != reflect.Struct {
		return "", false, false
	}
	field := held.FieldByName("TaskID")
	if !field.IsValid() {
		return "", false, false
	}
	switch field.Kind() {
	case reflect.String:
		return field.String(), true, true
	case reflect.Pointer:
		if field.IsNil() {
			return "", false, false
		}
		if field.Elem().Kind() == reflect.String {
			return field.Elem().String(), true, true
		}
	}
	return "", false, true
}

// withInputTaskID is input with its optional taskId set.
func withInputTaskID(input WorkspaceInput, taskID string) WorkspaceInput {
	held := reflect.ValueOf(input)
	copied := reflect.New(held.Type()).Elem()
	copied.Set(held)
	field := copied.FieldByName("TaskID")
	if !field.IsValid() || field.Kind() != reflect.Pointer {
		return input
	}
	field.Set(reflect.ValueOf(&taskID).Convert(field.Type()))
	return copied.Interface().(WorkspaceInput)
}
