package agent

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// Hears which processes a session owns, for the resource monitor.
type ResourceReporter func(message contracts.SessionResourceMessage)

// One session's claim on the processes it starts.
type ResourceScope struct {
	TaskID    string
	SessionID string

	mu       sync.Mutex
	released bool
	report   ResourceReporter
}

func newResourceScope(taskID string, report ResourceReporter) *ResourceScope {
	if report == nil {
		report = func(contracts.SessionResourceMessage) {}
	}
	return &ResourceScope{TaskID: taskID, SessionID: application.NewID(), report: report}
}

func (scope *ResourceScope) release() {
	if scope == nil {
		return
	}
	scope.mu.Lock()
	if scope.released {
		scope.mu.Unlock()
		return
	}
	scope.released = true
	scope.mu.Unlock()
	scope.report(contracts.SessionReleaseEvent{TaskID: scope.TaskID, SessionID: scope.SessionID})
}

func (scope *ResourceScope) reportBusy(busy bool) {
	if scope == nil {
		return
	}
	scope.mu.Lock()
	released := scope.released
	scope.mu.Unlock()
	if !released {
		scope.report(contracts.SessionResourceMessageSessionState{TaskID: scope.TaskID, SessionID: scope.SessionID, Busy: busy})
	}
}

// Registers a conversation engine's process as the session's. Discovery and shared authentication
// processes run outside ownership and are never registered. Safe on a nil scope.
func (scope *ResourceScope) RegisterProcess(pid int) {
	if scope == nil || pid <= 0 {
		return
	}
	scope.report(contracts.SessionProcessEvent{TaskID: scope.TaskID, SessionID: scope.SessionID, Pid: float64(pid)})
	scope.mu.Lock()
	released := scope.released
	scope.mu.Unlock()
	if released {
		scope.report(contracts.SessionReleaseEvent{TaskID: scope.TaskID, SessionID: scope.SessionID})
	}
}
