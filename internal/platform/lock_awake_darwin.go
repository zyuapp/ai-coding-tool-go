package platform

import "sync"

// KeepsLockedMachineAwake is true on macOS, which can keep a locked machine awake while work is ahead.
const KeepsLockedMachineAwake = true

var screenLockWatchers = struct {
	sync.Mutex
	next  int
	watch map[int][2]func()
}{watch: map[int][2]func(){}}

func screenLockChanged(locked bool) {
	screenLockWatchers.Lock()
	var callbacks []func()
	for _, watcher := range screenLockWatchers.watch {
		if locked {
			callbacks = append(callbacks, watcher[0])
		} else {
			callbacks = append(callbacks, watcher[1])
		}
	}
	screenLockWatchers.Unlock()
	for _, callback := range callbacks {
		go callback()
	}
}

type cocoaLockMonitor struct{}

func (cocoaLockMonitor) Watch(lock, unlock func()) func() {
	screenLockWatchers.Lock()
	id := screenLockWatchers.next
	screenLockWatchers.next++
	screenLockWatchers.watch[id] = [2]func(){lock, unlock}
	first := len(screenLockWatchers.watch) == 1
	screenLockWatchers.Unlock()
	if first {
		watchScreenLock(true)
	}
	return func() {
		screenLockWatchers.Lock()
		delete(screenLockWatchers.watch, id)
		last := len(screenLockWatchers.watch) == 0
		screenLockWatchers.Unlock()
		if last {
			watchScreenLock(false)
		}
	}
}

func (cocoaLockMonitor) SystemIdleState(threshold int) IdleState {
	return SystemIdleState(threshold)
}

// SystemIdleState is whether the screen is locked, or how long nobody has touched the computer.
func SystemIdleState(thresholdSeconds int) IdleState {
	if screenLocked() {
		return IdleLocked
	}
	if idleSeconds() >= float64(thresholdSeconds) {
		return IdleIdle
	}
	return IdleActive
}

// powerBlocker holds an IOKit assertion against idle sleep per request.
type powerBlocker struct {
	mu         sync.Mutex
	next       int
	assertions map[int]uint32
}

func (b *powerBlocker) Start() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	b.assertions[b.next] = holdAwake()
	return b.next
}

func (b *powerBlocker) Stop(id int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	assertion, ok := b.assertions[id]
	if !ok {
		return false
	}
	delete(b.assertions, id)
	if assertion != 0 {
		releaseAwake(assertion)
	}
	return true
}

func (b *powerBlocker) IsStarted(id int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.assertions[id]
	return ok
}

var sharedPowerBlocker = &powerBlocker{assertions: map[int]uint32{}}

// PowerBlocker is the system's idle-sleep blocker, shared by everything in the app that needs one.
func PowerBlocker() SleepBlocker { return sharedPowerBlocker }

// NewLockAwake keeps a locked Mac awake while `working` says work is ahead. The screen-lock events
// arrive through the app's main run loop.
func NewLockAwake(working func() bool) *LockAwake {
	return StartLockAwake(cocoaLockMonitor{}, sharedPowerBlocker, working)
}
