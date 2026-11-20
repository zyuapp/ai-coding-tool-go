package host

import (
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// SnoozeTimer is a single timer that follows wall time, including clock changes and time spent
// asleep: it wakes at least once a minute and fires once the clock has passed the time.
type SnoozeTimer struct {
	mu      sync.Mutex
	timer   *time.Timer
	elapsed func(at float64)
}

func CreateSnoozeTimer(elapsed func(at float64)) *SnoozeTimer {
	return &SnoozeTimer{elapsed: elapsed}
}

// Schedule fires elapsed at `at`, replacing any earlier time; nil stops it.
func (s *SnoozeTimer) Schedule(at *float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scheduleLocked(at)
}

func (s *SnoozeTimer) scheduleLocked(at *float64) {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if at == nil {
		return
	}
	target := *at
	remaining := max(0, min(target-application.Now(), 60_000))
	var timer *time.Timer
	timer = time.AfterFunc(time.Duration(remaining)*time.Millisecond, func() {
		s.mu.Lock()
		if s.timer != timer {
			s.mu.Unlock()
			return
		}
		s.timer = nil
		now := application.Now()
		if now < target {
			s.scheduleLocked(&target)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		s.elapsed(now)
	})
	s.timer = timer
}

func (s *SnoozeTimer) Dispose() { s.Schedule(nil) }
