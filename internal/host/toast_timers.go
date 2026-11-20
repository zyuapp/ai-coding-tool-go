package host

import (
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// ToastTimers keeps one timer per toast. Scheduling a toast again restarts its time, nil stops it,
// and disposing drops every one.
type ToastTimers struct {
	mu      sync.Mutex
	timers  map[float64]*time.Timer
	elapsed func(id float64)
}

func CreateToastTimers(elapsed func(id float64)) *ToastTimers {
	return &ToastTimers{timers: map[float64]*time.Timer{}, elapsed: elapsed}
}

func (t *ToastTimers) Schedule(id float64, at *float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if timer := t.timers[id]; timer != nil {
		timer.Stop()
	}
	delete(t.timers, id)
	if at == nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(time.Duration(max(0, *at-application.Now()))*time.Millisecond, func() {
		t.mu.Lock()
		if t.timers[id] != timer {
			t.mu.Unlock()
			return
		}
		delete(t.timers, id)
		t.mu.Unlock()
		t.elapsed(id)
	})
	t.timers[id] = timer
}

func (t *ToastTimers) Dispose() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, timer := range t.timers {
		timer.Stop()
	}
	clear(t.timers)
}
