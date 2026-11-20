package store

import (
	"iter"
	"slices"
)

// RunEventGateState is how far a run's events have been accepted.
type RunEventGateState struct {
	LastSequence float64
	Terminal     bool
}

// CorrelatedRunState is a run's gate, with the thread and run it belongs to.
type CorrelatedRunState struct {
	RunEventGateState
	TaskID string
	RunID  string
}

// RunSession is a provider session's permission ceiling: sessions can begin follow-up turns, but
// cannot choose a new one.
type RunSession struct {
	RunID  string
	Policy string
}

// RunEventHeader is the part of a contracts RunEvent the ledger reads.
type RunEventHeader struct {
	Type           string
	TaskID         string
	RunID          string
	Sequence       float64
	Status         string
	AgentInitiated bool
}

// RunStatusEvent is the contracts RunEvent { type: "run.status" } a lost transport fails a run with.
type RunStatusEvent struct {
	Type     string  `json:"type"`
	TaskID   string  `json:"taskId"`
	RunID    string  `json:"runId"`
	Sequence float64 `json:"sequence"`
	Status   string  `json:"status"`
	Message  string  `json:"message"`
}

// StartCommand is what the ledger needs of a contracts StartRunCommand. Implement it on a pointer,
// so a retitle reaches the command that is later sent.
type StartCommand interface {
	StartTaskID() string
	StartRunID() string
	StartPolicy() string
	SetStartTitle(title string)
}

// IsTerminalStatus is whether a status closes a run's books.
func IsTerminalStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

// AcceptRunEvent is whether an event is the next word of a run, advancing the gate when it is.
func AcceptRunEvent(state *RunEventGateState, sequence float64, eventType, status string) bool {
	if state.Terminal || sequence <= state.LastSequence {
		return false
	}
	state.LastSequence = sequence
	if eventType == "run.status" && IsTerminalStatus(status) {
		state.Terminal = true
	}
	return true
}

// FailedEventsForTransportLoss fails every run still open.
func FailedEventsForTransportLoss(states iter.Seq[*CorrelatedRunState], message string) []RunStatusEvent {
	events := []RunStatusEvent{}
	for state := range states {
		if state.Terminal {
			continue
		}
		events = append(events, RunStatusEvent{Type: "run.status", TaskID: state.TaskID, RunID: state.RunID, Sequence: state.LastSequence + 1, Status: "failed", Message: message})
	}
	return events
}

// orderedMap is a Map: keys iterate in insertion order.
type orderedMap[V any] struct {
	keys   []string
	values map[string]V
}

func newOrderedMap[V any]() *orderedMap[V] { return &orderedMap[V]{values: map[string]V{}} }

func (m *orderedMap[V]) get(key string) (V, bool) {
	value, ok := m.values[key]
	return value, ok
}

func (m *orderedMap[V]) set(key string, value V) {
	if _, ok := m.values[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.values[key] = value
}

func (m *orderedMap[V]) delete(key string) {
	if _, ok := m.values[key]; !ok {
		return
	}
	delete(m.values, key)
	m.keys = slices.DeleteFunc(m.keys, func(k string) bool { return k == key })
}

func (m *orderedMap[V]) all() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for _, key := range slices.Clone(m.keys) {
			value, ok := m.values[key]
			if ok && !yield(key, value) {
				return
			}
		}
	}
}

// supersedePendingStarts drops every pending start but keepKey that matches, returning them in
// the order they were added.
func supersedePendingStarts[V any](pending *orderedMap[V], keepKey string, matches func(V) bool) []keyed[V] {
	var superseded []keyed[V]
	for key, value := range pending.all() {
		if key == keepKey || !matches(value) {
			continue
		}
		pending.delete(key)
		superseded = append(superseded, keyed[V]{key, value})
	}
	return superseded
}

type keyed[V any] struct {
	key   string
	value V
}

func runKey(taskID, runID string) string { return taskID + "\x00" + runID }

// RunLedger is what the host knows about the runs it has asked for: the sequence and terminal
// state of each one, the permission ceiling of each thread, and the starts still being resolved
// against their workspace. It is not safe for concurrent use.
type RunLedger[C StartCommand] struct {
	states   *orderedMap[*CorrelatedRunState]
	sessions map[string]RunSession
	starts   *orderedMap[C]
}

func NewRunLedger[C StartCommand]() *RunLedger[C] {
	return &RunLedger[C]{states: newOrderedMap[*CorrelatedRunState](), sessions: map[string]RunSession{}, starts: newOrderedMap[C]()}
}

// BeginStart puts a start on the books from the moment it is asked for; it stays pending until it
// is sent or dropped.
func (l *RunLedger[C]) BeginStart(command C) {
	key := runKey(command.StartTaskID(), command.StartRunID())
	l.states.set(key, &CorrelatedRunState{TaskID: command.StartTaskID(), RunID: command.StartRunID()})
	l.sessions[command.StartTaskID()] = RunSession{RunID: command.StartRunID(), Policy: command.StartPolicy()}
	l.starts.set(key, command)
}

func (l *RunLedger[C]) Knows(taskID, runID string) bool {
	_, ok := l.states.get(runKey(taskID, runID))
	return ok
}

func (l *RunLedger[C]) State(taskID, runID string) *CorrelatedRunState {
	state, _ := l.states.get(runKey(taskID, runID))
	return state
}

func (l *RunLedger[C]) Session(taskID string) (RunSession, bool) {
	session, ok := l.sessions[taskID]
	return session, ok
}

// Reopen gives a run books to write into, whether or not the agent process ever saw it.
func (l *RunLedger[C]) Reopen(taskID, runID string) *CorrelatedRunState {
	key := runKey(taskID, runID)
	state, ok := l.states.get(key)
	if !ok {
		state = &CorrelatedRunState{TaskID: taskID, RunID: runID}
	}
	l.states.set(key, state)
	return state
}

// Accept is whether the event is the next word of a run the host is following. A turn the agent
// began itself opens its own books here, and takes the thread's ceiling with it; a terminal event
// closes them.
func (l *RunLedger[C]) Accept(event RunEventHeader) bool {
	key := runKey(event.TaskID, event.RunID)
	state, ok := l.states.get(key)
	if !ok && event.Type == "run.started" {
		state = &CorrelatedRunState{TaskID: event.TaskID, RunID: event.RunID}
		ok = true
		l.states.set(key, state)
		if session, held := l.sessions[event.TaskID]; held && event.AgentInitiated && !l.Knows(event.TaskID, session.RunID) {
			l.sessions[event.TaskID] = RunSession{RunID: event.RunID, Policy: session.Policy}
		}
	}
	if !ok || !AcceptRunEvent(&state.RunEventGateState, event.Sequence, event.Type, event.Status) {
		return false
	}
	if state.Terminal {
		l.states.delete(key)
	}
	return true
}

func (l *RunLedger[C]) PendingStart(taskID, runID string) (C, bool) {
	return l.starts.get(runKey(taskID, runID))
}

// TakeStart takes a start off the pending list, so whoever takes it is the only one who acts on it.
func (l *RunLedger[C]) TakeStart(taskID, runID string) (C, bool) {
	key := runKey(taskID, runID)
	command, ok := l.starts.get(key)
	l.starts.delete(key)
	return command, ok
}

// SupersedeStarts drops the thread's older pending starts in favour of this one, returning those
// whose run is not already settled.
func (l *RunLedger[C]) SupersedeStarts(taskID, runID string) []C {
	var commands []C
	for _, superseded := range supersedePendingStarts(l.starts, runKey(taskID, runID), func(command C) bool { return command.StartTaskID() == taskID }) {
		if state, ok := l.states.get(superseded.key); ok && state.Terminal {
			continue
		}
		commands = append(commands, superseded.value)
	}
	return commands
}

// StartsFor is whether a start of the thread's is still being resolved.
func (l *RunLedger[C]) StartsFor(taskID string) bool {
	for _, command := range l.starts.all() {
		if command.StartTaskID() == taskID {
			return true
		}
	}
	return false
}

// RetitleStarts carries a thread renamed while its start is still being resolved into the run.
func (l *RunLedger[C]) RetitleStarts(taskID, title string) {
	for _, command := range l.starts.all() {
		if command.StartTaskID() == taskID {
			command.SetStartTitle(title)
		}
	}
}

func (l *RunLedger[C]) CancelStarts(taskID string) {
	for key, command := range l.starts.all() {
		if command.StartTaskID() == taskID {
			l.starts.delete(key)
		}
	}
}

func (l *RunLedger[C]) ClearStarts() {
	l.starts = newOrderedMap[C]()
}

// ForgetSessions drops every ceiling: the agent process held every session, so its death leaves no
// thread with one to keep.
func (l *RunLedger[C]) ForgetSessions() {
	clear(l.sessions)
}

// FailuresFor fails every run still open, as a lost agent process does.
func (l *RunLedger[C]) FailuresFor(message string) []RunStatusEvent {
	return FailedEventsForTransportLoss(func(yield func(*CorrelatedRunState) bool) {
		for _, state := range l.states.all() {
			if !yield(state) {
				return
			}
		}
	}, message)
}
