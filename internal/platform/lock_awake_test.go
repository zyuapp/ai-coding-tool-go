package platform

import (
	"slices"
	"testing"
)

type fakeLockMonitor struct {
	state        IdleState
	lock, unlock func()
	stopped      bool
}

func (m *fakeLockMonitor) Watch(lock, unlock func()) func() {
	m.lock, m.unlock = lock, unlock
	return func() { m.stopped = true }
}

func (m *fakeLockMonitor) SystemIdleState(int) IdleState { return m.state }

func (m *fakeLockMonitor) emit(locked bool) {
	if m.stopped {
		return
	}
	if locked {
		m.lock()
	} else {
		m.unlock()
	}
}

type fakeBlocker struct {
	started, stopped []int
	active           map[int]bool
}

func newFakeBlocker() *fakeBlocker { return &fakeBlocker{active: map[int]bool{}} }

func (b *fakeBlocker) Start() int {
	id := len(b.started) + 1
	b.started = append(b.started, id)
	b.active[id] = true
	return id
}

func (b *fakeBlocker) Stop(id int) bool {
	b.stopped = append(b.stopped, id)
	was := b.active[id]
	delete(b.active, id)
	return was
}

func (b *fakeBlocker) IsStarted(id int) bool { return b.active[id] }

func TestALockedScreenKeepsTheAppAwakeUntilItUnlocks(t *testing.T) {
	monitor, blocker := &fakeLockMonitor{state: IdleActive}, newFakeBlocker()
	awake := StartLockAwake(monitor, blocker, nil)
	monitor.emit(true)
	monitor.emit(true)
	if !slices.Equal(blocker.started, []int{1}) {
		t.Fatalf("started %v", blocker.started)
	}
	monitor.emit(false)
	monitor.emit(false)
	if !slices.Equal(blocker.stopped, []int{1}) {
		t.Fatalf("stopped %v", blocker.stopped)
	}
	awake.Stop()
}

func TestAnAlreadyLockedScreenIsHeldAndReleasedOnStop(t *testing.T) {
	monitor, blocker := &fakeLockMonitor{state: IdleLocked}, newFakeBlocker()
	awake := StartLockAwake(monitor, blocker, nil)
	if len(blocker.started) != 1 {
		t.Fatal("not held at start")
	}
	awake.Stop()
	awake.Stop()
	monitor.emit(true)
	if !slices.Equal(blocker.stopped, []int{1}) || len(blocker.started) != 1 {
		t.Fatalf("started %v stopped %v", blocker.started, blocker.stopped)
	}
}

func TestUnlockLeavesAnotherSleepRequestActive(t *testing.T) {
	monitor, blocker := &fakeLockMonitor{state: IdleActive}, newFakeBlocker()
	phone := blocker.Start()
	awake := StartLockAwake(monitor, blocker, nil)
	monitor.emit(true)
	monitor.emit(false)
	if !slices.Equal(blocker.stopped, []int{2}) || !blocker.IsStarted(phone) {
		t.Fatalf("stopped %v", blocker.stopped)
	}
	awake.Stop()
}

func TestALockedScreenWithNoWorkLeftLetsTheMacSleep(t *testing.T) {
	monitor, blocker := &fakeLockMonitor{state: IdleActive}, newFakeBlocker()
	working := false
	awake := StartLockAwake(monitor, blocker, func() bool { return working })
	monitor.emit(true)
	if len(blocker.started) != 0 {
		t.Fatal("held with no work")
	}
	working = true
	awake.Update()
	if !slices.Equal(blocker.started, []int{1}) {
		t.Fatal("not held once work started")
	}
	working = false
	awake.Update()
	if !slices.Equal(blocker.stopped, []int{1}) {
		t.Fatal("held after work finished")
	}
	monitor.emit(false)
	working = true
	awake.Update()
	if len(blocker.started) != 1 {
		t.Fatal("held while unlocked")
	}
	awake.Stop()
}
