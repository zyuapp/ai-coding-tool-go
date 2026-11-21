package application

import (
	"reflect"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// AgentDraftChange is Partial<AgentDraft>: the fields of the draft a change names.
type AgentDraftChange struct {
	Engine   *domain.AgentEngine
	Model    *domain.AgentModel
	Effort   *domain.AgentEffort
	Policy   *domain.ExecutionPolicy
	FastMode *bool
}

// EmptyWorkspaceState is the workspace before anything is loaded. storageError nil is TypeScript's default null.
func EmptyWorkspaceState(storageError *string) *WorkspaceState {
	state := &WorkspaceState{}
	for _, part := range []any{
		InitialThreadsState(),
		InitialProjectsState(),
		InitialWorktreesState(),
		InitialNavigationState(),
		InitialNewThreadDraftState(),
		InitialEnginesState(),
		InitialComposerState(),
		StateProjectAddNoProjectAdd,
		InitialDialogsState(),
		InitialSettingsState(),
		InitialDocksState(),
		InitialFindBarState(),
		InitialBrowserState(),
		InitialSimulatorState(),
		InitialDictationState(),
		InitialGitState(),
		InitialDesktopState(),
		InitialCliState(),
		InitialPlanUsageState(),
		InitialComputerUseState(),
		InitialAutomationsState(),
		InitialRunsState(),
		InitialIdleCompactionState(),
		InitialSideChatsState(),
		InitialRemoteState(),
		InitialAppUpdatesState(),
		InitialToastsState(),
		InitialComputersState(),
		InitialStoreState(storageError),
		InitialErrorsState(),
	} {
		spreadInto(state, part)
	}
	return state
}

// CurrentFolder is the folder the app is pointed at: the thread's own checkout, else its project, else the last one opened.
func CurrentFolder(state *WorkspaceState) *string {
	thread := findThread(state.Threads, state.CurrentID)
	if root, ok := ThreadWorkspaceRoot(state, thread); ok {
		return &root
	}
	if state.DraftProjectID != nil && *state.DraftProjectID != "" {
		for _, project := range state.Projects {
			if project.ID == *state.DraftProjectID {
				root := project.Root
				return &root
			}
		}
	}
	return state.LastFolder
}

// ChangedDraft is the draft with change made and held to its engine, which is how its agent choices are ever written.
func ChangedDraft(state *WorkspaceState, change AgentDraftChange) domain.AgentDraft {
	draft := DraftOf(state)
	if change.Engine != nil {
		draft.Engine = *change.Engine
	}
	if change.Model != nil {
		draft.Model = *change.Model
	}
	if change.Effort != nil {
		draft.Effort = *change.Effort
	}
	if change.Policy != nil {
		draft.Policy = *change.Policy
	}
	if change.FastMode != nil {
		draft.FastMode = *change.FastMode
	}
	catalog := CatalogOf(state)
	return domain.NormalizeDraft(&catalog, draft, domain.EffortRoleOf(nil, deref(state.DraftRole)))
}

// WithDraft is ChangedDraft, on the whole state.
func WithDraft(state *WorkspaceState, change AgentDraftChange) *WorkspaceState {
	draft := ChangedDraft(state, change)
	next := *state
	next.DraftEngine = draft.Engine
	next.DraftModel = draft.Model
	next.DraftEffort = draft.Effort
	next.DraftPolicy = draft.Policy
	next.DraftFastMode = draft.FastMode
	return &next
}

// PromptKey is which composer the window is typing into. While a paired computer's thread is on
// screen it is that thread's, so the draft typed for it stays keyed to it here.
func PromptKey(state *WorkspaceState) string {
	current := state.CurrentID
	if remote := SelectedComputer(state); remote != nil && remote.State != nil {
		current = remote.State.CurrentID
	}
	if current == nil {
		return NewThreadDraft
	}
	return *current
}

// ReachableVisit is where the cursor lands moving step (-1 or 1) through history, stepping over
// threads that are gone or archived; false when there is none.
func ReachableVisit(state *WorkspaceState, step int) (int, bool) {
	misses := 0
	var reachable map[string]bool
	for index := int(state.HistoryIndex) + step; index >= 0 && index < len(state.History); index += step {
		id := state.History[index]
		if reachable != nil {
			if reachable[id] {
				return index, true
			}
		} else if slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool { return thread.ID == id && thread.ArchivedAt == nil }) {
			return index, true
		}
		misses++
		if misses == 128 {
			reachable = map[string]bool{}
			for _, thread := range state.Threads {
				if thread.ArchivedAt == nil {
					reachable[thread.ID] = true
				}
			}
		}
	}
	return 0, false
}

// RecordVisit remembers where the app took the user, dropping the forward entries the way a browser does.
func RecordVisit(state *WorkspaceState, threadID string) *WorkspaceState {
	end := min(max(int(state.HistoryIndex)+1, 0), len(state.History))
	history := slices.Clone(state.History[:end])
	if len(history) == 0 || history[len(history)-1] != threadID {
		history = append(history, threadID)
	}
	if history == nil {
		history = []string{}
	}
	next := *state
	next.History = history
	next.HistoryIndex = float64(len(history) - 1)
	return &next
}

// findThread is the thread id names, nil when id is nil or names none.
func findThread(threads []*domain.Thread, id *string) *domain.Thread {
	if id == nil {
		return nil
	}
	for _, thread := range threads {
		if thread.ID == *id {
			return thread
		}
	}
	return nil
}

// spreadInto writes every field of part, one slice's state, into the WorkspaceState field of the same name.
func spreadInto(state *WorkspaceState, part any) {
	source := reflect.ValueOf(part)
	target := reflect.ValueOf(state).Elem()
	for _, field := range reflect.VisibleFields(source.Type()) {
		if field.Anonymous || !field.IsExported() {
			continue
		}
		into := target.FieldByName(field.Name)
		if !into.IsValid() {
			panic("application: " + source.Type().String() + "." + field.Name + " is not a WorkspaceState field")
		}
		value := source.FieldByIndex(field.Index)
		if value.Type() != into.Type() {
			value = value.Convert(into.Type())
		}
		into.Set(value)
	}
}
