package agent

import (
	"reflect"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type RunEventGateState struct {
	LastSequence float64
	Terminal     bool
}

type CorrelatedRunState struct {
	RunEventGateState
	TaskID string
	RunID  string
}

// Provider sessions can begin follow-up turns, but cannot choose a new permission ceiling.
type RunSession struct {
	RunID  string
	Policy domain.ExecutionPolicy
}

// The statuses that close a run's books; every other one leaves it open.
func IsTerminalStatus(status domain.RunStatus) bool {
	return status == domain.RunStatusSucceeded || status == domain.RunStatusFailed || status == domain.RunStatusCancelled
}

// The taskId, runId and sequence every run event carries.
func RunEventBaseOf(event contracts.RunEvent) contracts.RunEventBase {
	value := reflect.ValueOf(event)
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return contracts.RunEventBase{}
	}
	if base, ok := value.FieldByName("RunEventBase").Interface().(contracts.RunEventBase); ok {
		return base
	}
	return contracts.RunEventBase{}
}

func AcceptRunEvent(state *RunEventGateState, event contracts.RunEvent) bool {
	base := RunEventBaseOf(event)
	if state.Terminal || base.Sequence <= state.LastSequence {
		return false
	}
	state.LastSequence = base.Sequence
	if status, ok := event.(contracts.RunEventRunStatus); ok && IsTerminalStatus(status.Status) {
		state.Terminal = true
	}
	return true
}

func FailedEventsForTransportLoss(states []*CorrelatedRunState, message string) []contracts.RunEvent {
	var events []contracts.RunEvent
	for _, state := range states {
		if state.Terminal {
			continue
		}
		text := message
		events = append(events, contracts.RunEventRunStatus{
			RunEventBase: contracts.RunEventBase{TaskID: state.TaskID, RunID: state.RunID, Sequence: state.LastSequence + 1},
			Status:       domain.RunStatusFailed,
			Message:      &text,
		})
	}
	return events
}

type runKey struct{ taskID, runID string }

// Insertion-ordered map, as the TS ledger's Maps iterate.
type ordered[V any] struct {
	keys   []runKey
	values map[runKey]V
}

func newOrdered[V any]() *ordered[V] { return &ordered[V]{values: map[runKey]V{}} }

func (o *ordered[V]) get(key runKey) (V, bool) {
	value, ok := o.values[key]
	return value, ok
}

func (o *ordered[V]) set(key runKey, value V) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

func (o *ordered[V]) remove(key runKey) {
	if _, ok := o.values[key]; !ok {
		return
	}
	delete(o.values, key)
	for index, existing := range o.keys {
		if existing == key {
			o.keys = append(o.keys[:index:index], o.keys[index+1:]...)
			break
		}
	}
}

func (o *ordered[V]) each(visit func(key runKey, value V) bool) {
	for _, key := range append([]runKey(nil), o.keys...) {
		if value, ok := o.values[key]; ok && !visit(key, value) {
			return
		}
	}
}

// What the host knows about the runs it has asked for: the sequence and terminal state of each one,
// the permission ceiling of each thread, and the starts still being resolved against their workspace.
// Not safe for concurrent use; the run host serializes it.
type RunLedger struct {
	states   *ordered[*CorrelatedRunState]
	sessions map[string]RunSession
	starts   *ordered[*contracts.StartRunCommand]
}

func NewRunLedger() *RunLedger {
	return &RunLedger{states: newOrdered[*CorrelatedRunState](), sessions: map[string]RunSession{}, starts: newOrdered[*contracts.StartRunCommand]()}
}

// A start is on the books from the moment it is asked for, and stays pending until it is sent or dropped.
func (ledger *RunLedger) BeginStart(command contracts.StartRunCommand) {
	key := runKey{command.TaskID, command.RunID}
	ledger.states.set(key, &CorrelatedRunState{TaskID: command.TaskID, RunID: command.RunID})
	ledger.sessions[command.TaskID] = RunSession{RunID: command.RunID, Policy: command.Policy}
	ledger.starts.set(key, &command)
}

func (ledger *RunLedger) Knows(taskID, runID string) bool {
	_, ok := ledger.states.get(runKey{taskID, runID})
	return ok
}

func (ledger *RunLedger) State(taskID, runID string) *CorrelatedRunState {
	state, _ := ledger.states.get(runKey{taskID, runID})
	return state
}

func (ledger *RunLedger) Session(taskID string) (RunSession, bool) {
	session, ok := ledger.sessions[taskID]
	return session, ok
}

// Gives a run books to write into, whether or not the agent side ever saw it.
func (ledger *RunLedger) Reopen(taskID, runID string) *CorrelatedRunState {
	key := runKey{taskID, runID}
	state, ok := ledger.states.get(key)
	if !ok {
		state = &CorrelatedRunState{TaskID: taskID, RunID: runID}
	}
	ledger.states.set(key, state)
	return state
}

// Whether the event is the next word of a run the host is following. A turn the agent began itself
// opens its own books here, and takes the thread's ceiling with it; a terminal event closes them.
func (ledger *RunLedger) Accept(event contracts.RunEvent) bool {
	base := RunEventBaseOf(event)
	key := runKey{base.TaskID, base.RunID}
	state, ok := ledger.states.get(key)
	if started, isStart := event.(contracts.RunEventRunStarted); !ok && isStart {
		state = &CorrelatedRunState{TaskID: base.TaskID, RunID: base.RunID}
		ledger.states.set(key, state)
		ok = true
		session, has := ledger.sessions[base.TaskID]
		if has && started.AgentInitiated != nil && *started.AgentInitiated && !ledger.Knows(base.TaskID, session.RunID) {
			ledger.sessions[base.TaskID] = RunSession{RunID: base.RunID, Policy: session.Policy}
		}
	}
	if !ok || !AcceptRunEvent(&state.RunEventGateState, event) {
		return false
	}
	if state.Terminal {
		ledger.states.remove(key)
	}
	return true
}

func (ledger *RunLedger) PendingStart(taskID, runID string) *contracts.StartRunCommand {
	command, _ := ledger.starts.get(runKey{taskID, runID})
	return command
}

// Takes a start off the pending list, so whoever takes it is the only one who acts on it.
func (ledger *RunLedger) TakeStart(taskID, runID string) *contracts.StartRunCommand {
	key := runKey{taskID, runID}
	command, _ := ledger.starts.get(key)
	ledger.starts.remove(key)
	return command
}

// The thread's older pending starts, dropped in favour of this one, minus any run already settled.
func (ledger *RunLedger) SupersedeStarts(taskID, runID string) []*contracts.StartRunCommand {
	keep := runKey{taskID, runID}
	var superseded []*contracts.StartRunCommand
	ledger.starts.each(func(key runKey, command *contracts.StartRunCommand) bool {
		if key == keep || command.TaskID != taskID {
			return true
		}
		ledger.starts.remove(key)
		if state, ok := ledger.states.get(key); !ok || !state.Terminal {
			superseded = append(superseded, command)
		}
		return true
	})
	return superseded
}

// Whether a start of the thread's is still being resolved.
func (ledger *RunLedger) StartsFor(taskID string) bool {
	found := false
	ledger.starts.each(func(_ runKey, command *contracts.StartRunCommand) bool {
		found = command.TaskID == taskID
		return !found
	})
	return found
}

// A thread renamed while its start is still being resolved carries the new title into the run.
func (ledger *RunLedger) RetitleStarts(taskID, title string) {
	ledger.starts.each(func(_ runKey, command *contracts.StartRunCommand) bool {
		if command.TaskID == taskID {
			command.Title = title
		}
		return true
	})
}

func (ledger *RunLedger) CancelStarts(taskID string) {
	ledger.starts.each(func(key runKey, command *contracts.StartRunCommand) bool {
		if command.TaskID == taskID {
			ledger.starts.remove(key)
		}
		return true
	})
}

func (ledger *RunLedger) ClearStarts() {
	ledger.starts = newOrdered[*contracts.StartRunCommand]()
}

// The agent side held every session, so its death leaves no thread with a ceiling to keep.
func (ledger *RunLedger) ForgetSessions() {
	ledger.sessions = map[string]RunSession{}
}

func (ledger *RunLedger) FailuresFor(message string) []contracts.RunEvent {
	var states []*CorrelatedRunState
	ledger.states.each(func(_ runKey, state *CorrelatedRunState) bool {
		states = append(states, state)
		return true
	})
	return FailedEventsForTransportLoss(states, message)
}
