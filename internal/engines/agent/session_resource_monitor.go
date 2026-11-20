package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// One process a session owns, with the start time that tells it from a later one reusing its pid.
type knownProcess struct {
	pid  int
	born string
}

type owned struct {
	taskID    string
	sessionID string
	roots     []int
	known     []knownProcess
	history   []*domain.MemorySample
	busy      *bool
	released  *float64
	stopped   *domain.ResourceStop
}

func (owner *owned) knows(pid int) bool {
	return slices.ContainsFunc(owner.known, func(process knownProcess) bool { return process.pid == pid })
}

type SystemMemory struct {
	Total     float64
	Available float64
}

type ResourceMonitorOptions struct {
	// Where owned processes are remembered across a crash; empty keeps nothing.
	Journal string
	Stop    func(taskID, sessionID string, stop domain.ResourceStop)
	// May be nil.
	Release func(taskID, sessionID string)
	Changed func(taskID string, stop domain.ResourceStop)
	// Nil means platform.ReadProcessSamples.
	Read func(ctx context.Context) ([]platform.ProcessSample, error)
	// Nil means platform.SignalProcess.
	Signal func(pid int, signal platform.Signal)
	// Nil means application.Now.
	Now func() float64
	// Nil means the machine's own.
	Memory func(ctx context.Context) SystemMemory
	// Zero means 5s; the first sample is taken at once.
	Interval time.Duration
	// Zero means 1s.
	RetryDelay time.Duration
	// Samples only when asked.
	manual bool
}

const (
	cleanupLimit     = 15_000
	maxHistory       = 12
	releaseThreshold = 512 * 1024 * 1024
)

func gib(bytes float64) string { return fmt.Sprintf("%.1f GiB", bytes/math.Pow(1024, 3)) }

// Watches the processes each session owns, stops the largest one when the machine runs out of memory,
// and makes sure a released session's processes exit. Runs in the host, apart from the engines.
type SessionResourceMonitor struct {
	options ResourceMonitorOptions

	mu          sync.Mutex
	sessions    map[string]*owned
	order       []string
	timer       *time.Timer
	sampling    chan struct{}
	samplingErr error
	closed      bool
	enabled     bool
	ready       chan struct{}
	lastJournal string
}

func NewSessionResourceMonitor(options ResourceMonitorOptions) *SessionResourceMonitor {
	if options.Read == nil {
		options.Read = platform.ReadProcessSamples
	}
	if options.Signal == nil {
		options.Signal = func(pid int, signal platform.Signal) { platform.SignalProcess(pid, signal) }
	}
	if options.Now == nil {
		options.Now = application.Now
	}
	if options.Memory == nil {
		options.Memory = func(ctx context.Context) SystemMemory {
			return SystemMemory{Total: platform.TotalMemory(), Available: platform.AvailableMemory(ctx)}
		}
	}
	if options.Interval <= 0 {
		options.Interval = 5 * time.Second
	}
	if options.RetryDelay <= 0 {
		options.RetryDelay = time.Second
	}
	monitor := &SessionResourceMonitor{options: options, sessions: map[string]*owned{}, enabled: true, ready: make(chan struct{})}
	go func() {
		monitor.restore()
		close(monitor.ready)
		monitor.schedule(0)
	}()
	return monitor
}

type journalEntry struct {
	TaskID    string           `json:"taskId"`
	SessionID string           `json:"sessionId"`
	Known     []jsontext.Value `json:"known"`
}

func (monitor *SessionResourceMonitor) addLocked(owner *owned) {
	monitor.sessions[owner.sessionID] = owner
	monitor.order = append(monitor.order, owner.sessionID)
}

func (monitor *SessionResourceMonitor) deleteLocked(sessionID string) {
	delete(monitor.sessions, sessionID)
	monitor.order = slices.DeleteFunc(monitor.order, func(id string) bool { return id == sessionID })
}

func (monitor *SessionResourceMonitor) owners() []*owned {
	owners := make([]*owned, 0, len(monitor.order))
	for _, id := range monitor.order {
		owners = append(owners, monitor.sessions[id])
	}
	return owners
}

func (monitor *SessionResourceMonitor) restore() {
	if monitor.options.Journal == "" {
		return
	}
	data, err := os.ReadFile(monitor.options.Journal)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("Memory protection recovery: %v", err)
		}
		return
	}
	var entries []jsontext.Value
	if err := jsonx.Unmarshal(data, &entries); err != nil {
		log.Printf("Memory protection recovery: %v", err)
		return
	}
	now := monitor.options.Now()
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	for _, raw := range entries {
		var entry journalEntry
		if jsonx.Unmarshal(raw, &entry) != nil || entry.Known == nil {
			continue
		}
		var known []knownProcess
		for _, pairRaw := range entry.Known {
			var pair []any
			if jsonx.Unmarshal(pairRaw, &pair) != nil || len(pair) < 2 {
				continue
			}
			pid, pidOK := pair[0].(float64)
			born, bornOK := pair[1].(string)
			if !pidOK || !bornOK || pid != math.Trunc(pid) || pid <= 1 || pid > maxSafeInteger {
				continue
			}
			if index := slices.IndexFunc(known, func(process knownProcess) bool { return process.pid == int(pid) }); index >= 0 {
				known[index].born = born
			} else {
				known = append(known, knownProcess{int(pid), born})
			}
		}
		if len(known) > 0 && monitor.sessions[entry.SessionID] == nil {
			released := now
			monitor.addLocked(&owned{taskID: entry.TaskID, sessionID: entry.SessionID, known: known, released: &released})
		}
	}
}

func (monitor *SessionResourceMonitor) persist() error {
	if monitor.options.Journal == "" {
		return nil
	}
	monitor.mu.Lock()
	entries := []any{}
	for _, owner := range monitor.owners() {
		if len(owner.known) == 0 {
			continue
		}
		known := make([][]any, 0, len(owner.known))
		for _, process := range owner.known {
			known = append(known, []any{process.pid, process.born})
		}
		entries = append(entries, map[string]any{"taskId": owner.taskID, "sessionId": owner.sessionID, "known": known})
	}
	encoded, err := jsonx.Marshal(entries)
	if err != nil {
		monitor.mu.Unlock()
		return err
	}
	data := string(encoded)
	if data == monitor.lastJournal {
		monitor.mu.Unlock()
		return nil
	}
	monitor.mu.Unlock()
	temporary := fmt.Sprintf("%s.%d.tmp", monitor.options.Journal, os.Getpid())
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, monitor.options.Journal); err != nil {
		return err
	}
	monitor.mu.Lock()
	monitor.lastJournal = data
	monitor.mu.Unlock()
	return nil
}

// A crash's leftover processes must exit before the same thread starts another engine.
func (monitor *SessionResourceMonitor) BeforeStart(ctx context.Context, taskID string) error {
	select {
	case <-monitor.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	for attempt := 0; attempt < 20; attempt++ {
		monitor.mu.Lock()
		leftover := slices.ContainsFunc(monitor.owners(), func(owner *owned) bool { return owner.taskID == taskID && owner.released != nil })
		monitor.mu.Unlock()
		if !leftover {
			return nil
		}
		if err := monitor.Sample(ctx); err != nil {
			log.Printf("Memory protection cleanup retry: %v", err)
		}
		select {
		case <-time.After(monitor.options.RetryDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.New("The previous session still has processes running. Wait for them to exit before resuming.")
}

func (monitor *SessionResourceMonitor) SetEnabled(enabled bool) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.enabled = enabled
	for _, owner := range monitor.sessions {
		owner.history = nil
	}
}

func (monitor *SessionResourceMonitor) Receive(event contracts.SessionResourceMessage) {
	monitor.mu.Lock()
	if monitor.closed {
		monitor.mu.Unlock()
		return
	}
	var taskID, sessionID string
	switch event := event.(type) {
	case contracts.SessionProcessEvent:
		taskID, sessionID = event.TaskID, event.SessionID
	case contracts.SessionResourceMessageSessionState:
		taskID, sessionID = event.TaskID, event.SessionID
	case contracts.SessionReleaseEvent:
		taskID, sessionID = event.TaskID, event.SessionID
	default:
		monitor.mu.Unlock()
		return
	}
	owner := monitor.sessions[sessionID]
	if owner != nil && owner.taskID != taskID {
		monitor.mu.Unlock()
		return
	}
	switch event := event.(type) {
	case contracts.SessionProcessEvent:
		if owner == nil {
			owner = &owned{taskID: taskID, sessionID: sessionID}
			monitor.addLocked(owner)
		}
		owner.roots = append(owner.roots, int(event.Pid))
	case contracts.SessionResourceMessageSessionState:
		if owner != nil {
			busy := event.Busy
			owner.busy = &busy
		}
	case contracts.SessionReleaseEvent:
		if owner != nil && owner.released == nil {
			now := monitor.options.Now()
			owner.released = &now
		}
	}
	monitor.mu.Unlock()
	monitor.schedule(0)
}

// Shutdown releases only owned processes; shared services never enter this registry.
func (monitor *SessionResourceMonitor) ReleaseAll() {
	monitor.mu.Lock()
	now := monitor.options.Now()
	for _, owner := range monitor.sessions {
		if owner.released == nil {
			released := now
			owner.released = &released
		}
	}
	monitor.mu.Unlock()
	monitor.schedule(0)
}

func (monitor *SessionResourceMonitor) Close() {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.closed = true
	if monitor.timer != nil {
		monitor.timer.Stop()
	}
}

// Takes one sample; a second ask while one is being taken joins it.
func (monitor *SessionResourceMonitor) Sample(ctx context.Context) error {
	monitor.mu.Lock()
	sampling := monitor.sampling
	if sampling == nil {
		sampling = make(chan struct{})
		monitor.sampling = sampling
		go func() {
			err := monitor.poll(context.WithoutCancel(ctx))
			monitor.mu.Lock()
			monitor.samplingErr = err
			monitor.sampling = nil
			monitor.mu.Unlock()
			close(sampling)
		}()
	}
	monitor.mu.Unlock()
	select {
	case <-sampling:
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		return monitor.samplingErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (monitor *SessionResourceMonitor) schedule(delay time.Duration) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	monitor.scheduleLocked(delay)
}

func (monitor *SessionResourceMonitor) scheduleLocked(delay time.Duration) {
	if monitor.closed || monitor.options.manual || monitor.timer != nil || len(monitor.sessions) == 0 {
		return
	}
	select {
	case <-monitor.ready:
	default:
		return
	}
	monitor.timer = time.AfterFunc(delay, func() {
		monitor.mu.Lock()
		monitor.timer = nil
		monitor.mu.Unlock()
		if err := monitor.Sample(context.Background()); err != nil {
			log.Printf("Memory protection: %v", err)
		}
		monitor.schedule(monitor.options.Interval)
	})
}

func (monitor *SessionResourceMonitor) poll(ctx context.Context) error {
	<-monitor.ready
	rows, err := monitor.options.Read(ctx)
	if err != nil {
		return err
	}
	byPid := make(map[int]platform.ProcessSample, len(rows))
	children := map[int][]platform.ProcessSample{}
	for _, row := range rows {
		byPid[row.PID] = row
		children[row.Parent] = append(children[row.Parent], row)
	}
	memory := monitor.options.Memory(ctx)
	self := os.Getpid()

	var after []func()
	monitor.mu.Lock()
	now := monitor.options.Now()
	type total struct {
		owner *owned
		bytes float64
	}
	var totals []total
	claimed := map[int]bool{}
	for _, owner := range monitor.owners() {
		// Remember descendants before reparenting; never adopt a PID whose birth changed.
		var live []knownProcess
		var visit func(pid int, born *string)
		visit = func(pid int, born *string) {
			row, ok := byPid[pid]
			if !ok || row.PID == self || (born != nil && row.Born != *born) || claimed[pid] {
				return
			}
			claimed[pid] = true
			live = append(live, knownProcess{pid, row.Born})
			for _, child := range children[pid] {
				visit(child.PID, nil)
			}
		}
		for _, process := range owner.known {
			born := process.born
			visit(process.pid, &born)
		}
		for _, pid := range owner.roots {
			// A root is adopted once; stale roots cannot claim later PID reuse.
			if !owner.knows(pid) {
				visit(pid, nil)
			}
		}
		owner.roots = nil
		owner.known = live
		if len(live) == 0 {
			if owner.stopped != nil {
				stop := *owner.stopped
				stop.Cleanup = domain.ResourceStopCleanupComplete
				taskID := owner.taskID
				after = append(after, func() { monitor.options.Changed(taskID, stop) })
			}
			monitor.deleteLocked(owner.sessionID)
			continue
		}
		bytes := 0.0
		for _, process := range live {
			bytes += byPid[process.pid].Bytes
		}
		totals = append(totals, total{owner, bytes})
		owner.history = append(owner.history, &domain.MemorySample{At: now, Bytes: bytes, Available: memory.Available})
		if len(owner.history) > maxHistory {
			owner.history = owner.history[1:]
		}
	}
	// Pressure is system-wide, so each sample may stop at most the one largest owner.
	var active []total
	for _, entry := range totals {
		if entry.owner.released == nil {
			active = append(active, entry)
		}
	}
	slices.SortStableFunc(active, func(left, right total) int {
		switch {
		case left.bytes > right.bytes:
			return -1
		case left.bytes < right.bytes:
			return 1
		}
		return 0
	})
	var largest, idle *owned
	if len(active) > 0 {
		largest = active[0].owner
	}
	for _, entry := range active {
		if entry.owner.busy != nil && !*entry.owner.busy {
			idle = entry.owner
			break
		}
	}
	var releasing *owned
	if monitor.enabled && memory.Available < releaseThreshold && idle != nil {
		releasing = idle
		busy := true
		releasing.busy = &busy
		if monitor.options.Release != nil {
			taskID, sessionID := releasing.taskID, releasing.sessionID
			after = append(after, func() { monitor.options.Release(taskID, sessionID) })
		}
	}
	// An owner that outlives the cleanup limit cannot be freed, so it no longer defers the next stop.
	stopping := releasing != nil || slices.ContainsFunc(monitor.owners(), func(owner *owned) bool {
		return owner.released != nil && now-*owner.released < cleanupLimit
	})
	if monitor.enabled && largest != nil && !stopping {
		if verdict, unsafe := domain.UnsafeMemory(largest.history, memory.Total); unsafe {
			bytes := largest.history[len(largest.history)-1].Bytes
			log.Printf("Memory protection stopped thread %s (%s): %s used, %s available of %s.", largest.taskID, verdict, gib(bytes), gib(memory.Available), gib(memory.Total))
			stop := domain.ResourceStop{At: now, Bytes: bytes, Cleanup: domain.ResourceStopCleanupStopping}
			largest.stopped = &stop
			released := now
			largest.released = &released
			taskID, sessionID := largest.taskID, largest.sessionID
			after = append(after, func() { monitor.options.Stop(taskID, sessionID, stop) })
		}
	}
	type signalled struct {
		pid    int
		signal platform.Signal
	}
	var signals []signalled
	var late []func()
	for _, entry := range totals {
		owner := entry.owner
		if owner.released == nil {
			continue
		}
		elapsed := now - *owner.released
		if elapsed < 1000 {
			continue
		}
		signal := platform.Terminate
		if elapsed >= 5000 {
			signal = platform.Kill
		}
		// Identity is checked against this sample immediately before signalling each known process.
		for _, process := range owner.known {
			if row, ok := byPid[process.pid]; ok && row.Born == process.born {
				signals = append(signals, signalled{process.pid, signal})
			}
		}
		if elapsed >= cleanupLimit && (owner.stopped == nil || owner.stopped.Cleanup != domain.ResourceStopCleanupIncomplete) {
			if owner.stopped != nil {
				stop := *owner.stopped
				stop.Cleanup = domain.ResourceStopCleanupIncomplete
				owner.stopped = &stop
				taskID := owner.taskID
				late = append(late, func() { monitor.options.Changed(taskID, stop) })
			}
		}
	}
	monitor.mu.Unlock()
	for _, act := range after {
		act()
	}
	for _, entry := range signals {
		monitor.options.Signal(entry.pid, entry.signal)
	}
	for _, act := range late {
		act()
	}
	return monitor.persist()
}
