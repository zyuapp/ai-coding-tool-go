package platform

import "sync"

// IdleState is the system's idle state, as Electron's `powerMonitor.getSystemIdleState` names it.
type IdleState string

const (
	IdleActive  IdleState = "active"
	IdleIdle    IdleState = "idle"
	IdleLocked  IdleState = "locked"
	IdleUnknown IdleState = "unknown"
)

// LockMonitor tells when the screen locks and unlocks.
type LockMonitor interface {
	// Watch calls lock and unlock as the screen locks and unlocks, until the returned stop.
	Watch(lock, unlock func()) (stop func())
	SystemIdleState(thresholdSeconds int) IdleState
}

// SleepBlocker holds off idle sleep while a request it started stands.
type SleepBlocker interface {
	Start() int
	Stop(id int) bool
	IsStarted(id int) bool
}

// LockAwake lets the display lock and turn off while the app's work continues to run. The Mac may
// idle-sleep once the screen is locked and nothing is left to do, so a quiet app never drains the battery.
type LockAwake struct {
	mu        sync.Mutex
	blocker   SleepBlocker
	working   func() bool
	blockerID int
	holding   bool
	locked    bool
	stopWatch func()
	stopped   bool
}

// StartLockAwake starts keeping the machine awake whenever the screen is locked and `working` says
// work is ahead; nil `working` means there always is.
func StartLockAwake(monitor LockMonitor, blocker SleepBlocker, working func() bool) *LockAwake {
	if working == nil {
		working = func() bool { return true }
	}
	awake := &LockAwake{blocker: blocker, working: working}
	awake.stopWatch = monitor.Watch(awake.lock, awake.unlock)
	if monitor.SystemIdleState(1) == IdleLocked {
		awake.lock()
	}
	return awake
}

func (a *LockAwake) hold() {
	if a.holding {
		return
	}
	a.blockerID = a.blocker.Start()
	a.holding = true
}

func (a *LockAwake) release() {
	if !a.holding {
		return
	}
	if a.blocker.IsStarted(a.blockerID) {
		a.blocker.Stop(a.blockerID)
	}
	a.holding = false
}

func (a *LockAwake) update() {
	if a.locked && a.working() {
		a.hold()
	} else {
		a.release()
	}
}

func (a *LockAwake) lock() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped {
		return
	}
	a.locked = true
	a.update()
}

func (a *LockAwake) unlock() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.locked = false
	a.release()
}

// Update looks at the work again, after it may have started or finished.
func (a *LockAwake) Update() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.locked {
		a.update()
	}
}

// Stop stops watching and lets the machine sleep.
func (a *LockAwake) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	stopWatch := a.stopWatch
	a.stopWatch = nil
	a.stopped = true
	a.mu.Unlock()
	if stopWatch != nil {
		stopWatch()
	}
	a.unlock()
}
