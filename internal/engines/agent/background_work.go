package agent

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The work an agent leaves running under a session, which outlives the turn that started it. Engines
// report it as a level rather than as start and finish bookends, so the whole set is swapped for each
// report: a bookend never seen, or seen twice, cannot leave the session holding work that has stopped.
type BackgroundWork struct {
	mu     sync.Mutex
	ids    map[string]bool
	report func(report contracts.BackgroundReport)
	// Speaks for the whole session: work stopping is rest only when nothing else is going.
	busy   func() bool
	rested func()
}

func NewBackgroundWork(busy func() bool, rested func()) *BackgroundWork {
	return &BackgroundWork{ids: map[string]bool{}, report: func(contracts.BackgroundReport) {}, busy: busy, rested: rested}
}

// Whether work that closing the session would cut short is still running.
func (work *BackgroundWork) Running() bool {
	work.mu.Lock()
	defer work.mu.Unlock()
	return len(work.ids) > 0
}

// A fresh process has nothing running under it, so the thread's set starts empty.
func (work *BackgroundWork) OpenWith(report func(report contracts.BackgroundReport)) {
	work.mu.Lock()
	work.report = report
	work.mu.Unlock()
	work.Clear()
}

// Swaps the whole set. ids names the work that keeps the session busy, which is wider than what is
// reported when the engine runs work of its own through the same mechanism; nil means the processes' ids.
func (work *BackgroundWork) Replace(processes []*domain.BackgroundProcess, ids []string) {
	if ids == nil {
		ids = make([]string, 0, len(processes))
		for _, process := range processes {
			ids = append(ids, process.ID)
		}
	}
	work.mu.Lock()
	wasRunning := len(work.ids) > 0
	work.ids = make(map[string]bool, len(ids))
	for _, id := range ids {
		work.ids[id] = true
	}
	report := work.report
	work.mu.Unlock()
	if processes == nil {
		processes = []*domain.BackgroundProcess{}
	}
	report(contracts.BackgroundReport{Processes: processes})
	if wasRunning && !work.busy() {
		work.rested()
	}
}

// The session ending is the end of the work it holds: nothing is left to report it stopping.
func (work *BackgroundWork) Clear() {
	work.mu.Lock()
	work.ids = map[string]bool{}
	report := work.report
	work.mu.Unlock()
	report(contracts.BackgroundReport{Processes: []*domain.BackgroundProcess{}})
}
