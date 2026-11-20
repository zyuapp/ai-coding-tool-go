package host

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

func TestEachToastKeepsItsOwnTime(t *testing.T) {
	const unit = 20.0
	start := application.Now()
	var mu sync.Mutex
	var elapsed []float64
	seen := func() []float64 {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(elapsed)
	}
	timers := CreateToastTimers(func(id float64) {
		mu.Lock()
		elapsed = append(elapsed, id)
		mu.Unlock()
	})
	at := func(units float64) *float64 { return new(start + units*unit) }
	timers.Schedule(1, at(5))
	timers.Schedule(2, at(6))
	time.Sleep(4 * unit * time.Millisecond)
	timers.Schedule(1, at(9))
	time.Sleep(3 * unit * time.Millisecond)
	if got := seen(); !slices.Equal(got, []float64{2}) {
		t.Fatalf("the restarted toast came down at its first time: %v", got)
	}
	time.Sleep(4 * unit * time.Millisecond)
	if got := seen(); !slices.Equal(got, []float64{2, 1}) {
		t.Fatalf("elapsed = %v", got)
	}
	timers.Schedule(4, at(12))
	timers.Schedule(4, nil)
	timers.Schedule(3, at(13))
	timers.Dispose()
	time.Sleep(4 * unit * time.Millisecond)
	if got := seen(); !slices.Equal(got, []float64{2, 1}) {
		t.Fatalf("a stopped or cleared toast came down: %v", got)
	}
}

func TestSnoozeTimerFiresOnceTheClockHasPassed(t *testing.T) {
	fired := make(chan float64, 2)
	timer := CreateSnoozeTimer(func(at float64) { fired <- at })
	target := application.Now() + 30
	timer.Schedule(&target)
	select {
	case at := <-fired:
		if at < target {
			t.Fatalf("fired at %v before %v", at, target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never fired")
	}
	timer.Schedule(new(application.Now() + 30))
	timer.Dispose()
	select {
	case <-fired:
		t.Fatal("a disposed timer fired")
	case <-time.After(80 * time.Millisecond):
	}
}
