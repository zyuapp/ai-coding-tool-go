package agent

import (
	"slices"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// How long a thread's session waits, with nothing left running under it, before giving its process back.
const IdleSession = 5 * time.Minute

// How many threads keep a session warm. Beyond this the least recently used idle one is let go.
const MaxLiveSessions = 3

// What the pool asks of a session, whichever engine it runs on. Methods may be called from any goroutine.
type PooledSession interface {
	Key() string
	Live() bool
	// Anything closing the session would cut short.
	Busy() bool
	// A turn is in flight, so the session owes an answer before it can take another.
	Answering() bool
	// Whether the session continues the given handle (nil: a run that names none).
	Continues(continuation *string) bool
	Run(input *ProviderRunInput) (ProviderResult, error)
	// Idempotent. The channel closes once the session's process has exited; nil means already settled.
	// Must not call the session's hooks synchronously.
	Close() <-chan struct{}
}

// What a session is built with: Ended forgets it, Rested restarts its idle clock, Resources owns its processes.
type SessionHooks struct {
	Ended     func()
	Rested    func()
	Resources *ResourceScope
}

type SessionOpener struct {
	// Builds the session. Must not call the hooks synchronously.
	Open func(hooks SessionHooks) PooledSession
	// Runs once the pool holds the session, so a session that ends at once is already known to it. May be nil.
	Start func(session PooledSession)
}

// A closed channel, for a session whose Close has nothing to wait for.
func Settled() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

type waiter struct {
	done chan struct{}
	once sync.Once
}

func newWaiter() *waiter { return &waiter{done: make(chan struct{})} }

func (w *waiter) resolve() { w.once.Do(func() { close(w.done) }) }

// Resolves once closed has settled.
func (w *waiter) resolveAfter(closed <-chan struct{}) {
	if closed == nil {
		w.resolve()
		return
	}
	go func() {
		<-closed
		w.resolve()
	}()
}

type held struct {
	resources *ResourceScope
	session   PooledSession
	// The coordination role of the run the session was opened for, which decides the tools it holds.
	role   *domain.CoordinationRole
	usedAt float64
	// When the pool took it, which breaks usedAt ties as the TS Map's insertion order does.
	inserted uint64
	// The turn the pool last handed the session, until it answers.
	running *waiter
	idle    *time.Timer
	reload  *waiter
	// Set once the thread is filed away: the session goes as soon as its turn has answered.
	retire *waiter
}

// One warm session per thread: the process it holds is what makes a second turn cheap. Engines share
// one pool, so the cap bounds their processes together.
type SessionPool struct {
	mu       sync.Mutex
	sessions map[string]*held
	idle     time.Duration
	report   ResourceReporter
	inserts  uint64
}

// idle <= 0 means IdleSession.
func NewSessionPool(report ResourceReporter, idle time.Duration) *SessionPool {
	if idle <= 0 {
		idle = IdleSession
	}
	return &SessionPool{sessions: map[string]*held{}, idle: idle, report: report}
}

// Runs on the thread's session where it fits the run, and on a fresh one otherwise.
func (pool *SessionPool) Execute(input *ProviderRunInput, key string, opener SessionOpener) (ProviderResult, error) {
	if answering := pool.answering(input.TaskID); answering != nil {
		<-answering
		if input.Aborted() {
			return ProviderResult{Status: domain.RunStatusCancelled}, nil
		}
	}
	running := newWaiter()
	current, start := pool.sessionFor(input, key, opener, running)
	current.resources.reportBusy(true)
	if start != nil {
		start()
	}
	defer func() {
		running.resolve()
		pool.mu.Lock()
		if current.running == running {
			current.running = nil
		}
		pool.mu.Unlock()
		pool.rest(input.TaskID, current)
	}()
	return current.session.Run(input)
}

// A cancelled run ends before its turn does. The next run waits for that rather than closing the session under it.
func (pool *SessionPool) answering(taskID string) <-chan struct{} {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	current := pool.sessions[taskID]
	if current != nil && current.running != nil && current.session.Answering() {
		return current.running.done
	}
	return nil
}

// The thread's session, while it is live.
func (pool *SessionPool) LiveSession(taskID string) PooledSession {
	pool.mu.Lock()
	current := pool.sessions[taskID]
	pool.mu.Unlock()
	if current != nil && current.session.Live() {
		return current.session
	}
	return nil
}

// Retires the current sessions once their work finishes; new sessions load settings on launch.
// The channel closes once every one has gone.
func (pool *SessionPool) ReloadSettings() <-chan struct{} {
	pool.mu.Lock()
	var waiting []*waiter
	var releases []func()
	for taskID, current := range pool.sessions {
		if current.reload == nil {
			current.reload = newWaiter()
		}
		waiting = append(waiting, current.reload)
		if !current.session.Busy() {
			releases = append(releases, pool.releaseLocked(taskID, current))
		}
	}
	pool.mu.Unlock()
	run(releases)
	return all(waiting)
}

// Lets the thread's session go once its turn has answered, background work and all. The channel
// closes when its process is gone.
func (pool *SessionPool) Retire(taskID string) <-chan struct{} {
	pool.mu.Lock()
	current := pool.sessions[taskID]
	if current == nil {
		pool.mu.Unlock()
		return Settled()
	}
	if current.retire == nil {
		current.retire = newWaiter()
	}
	done := current.retire.done
	var release func()
	if !current.session.Answering() {
		release = pool.releaseLocked(taskID, current)
	}
	pool.mu.Unlock()
	if release != nil {
		release()
	}
	return done
}

// Retires the thread's session when it was opened for a coordination role other than role.
func (pool *SessionPool) RetireOutOfRole(taskID string, role *domain.CoordinationRole) <-chan struct{} {
	pool.mu.Lock()
	current := pool.sessions[taskID]
	differs := current != nil && !sameRole(current.role, role)
	pool.mu.Unlock()
	if differs {
		return pool.Retire(taskID)
	}
	return Settled()
}

func sameRole(left, right *domain.CoordinationRole) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (pool *SessionPool) Terminate(taskID, sessionID string, idleOnly bool) bool {
	pool.mu.Lock()
	current := pool.sessions[taskID]
	if current == nil || current.resources.SessionID != sessionID {
		pool.mu.Unlock()
		return false
	}
	if idleOnly && current.session.Busy() {
		pool.mu.Unlock()
		current.resources.reportBusy(true)
		return false
	}
	release := pool.releaseLocked(taskID, current)
	pool.mu.Unlock()
	release()
	return true
}

// Lets every session go, which is what ends the processes they hold.
func (pool *SessionPool) CloseAll() {
	pool.mu.Lock()
	var releases []func()
	for taskID, current := range pool.sessions {
		releases = append(releases, pool.releaseLocked(taskID, current))
	}
	pool.mu.Unlock()
	run(releases)
}

// The held session for the run, opened when none fits; start, when not nil, runs the opener's Start.
func (pool *SessionPool) sessionFor(input *ProviderRunInput, key string, opener SessionOpener, running *waiter) (*held, func()) {
	pool.mu.Lock()
	current := pool.sessions[input.TaskID]
	if current != nil && pool.reusable(current, input, key) {
		if current.idle != nil {
			current.idle.Stop()
			current.idle = nil
		}
		current.usedAt = application.Now()
		current.running = running
		pool.mu.Unlock()
		return current, nil
	}
	var releases []func()
	if current != nil {
		releases = append(releases, pool.releaseLocked(input.TaskID, current))
	}
	releases = append(releases, pool.evictLocked()...)
	pool.mu.Unlock()
	run(releases)

	resources := newResourceScope(input.TaskID, pool.report)
	opened := &held{resources: resources, role: input.CoordinationRole, usedAt: application.Now(), running: running}
	taskID := input.TaskID
	opened.session = opener.Open(SessionHooks{
		Ended: func() {
			pool.mu.Lock()
			ended := pool.sessions[taskID]
			if ended != opened {
				pool.mu.Unlock()
				return
			}
			delete(pool.sessions, taskID)
			if ended.idle != nil {
				ended.idle.Stop()
			}
			pool.mu.Unlock()
			resources.release()
			if ended.reload != nil {
				ended.reload.resolve()
			}
			if ended.retire != nil {
				ended.retire.resolveAfter(ended.session.Close())
			}
		},
		Rested: func() {
			pool.mu.Lock()
			settled := pool.sessions[taskID]
			pool.mu.Unlock()
			if settled == opened {
				pool.rest(taskID, settled)
			}
		},
		Resources: resources,
	})
	pool.mu.Lock()
	if previous := pool.sessions[taskID]; previous != nil && previous != opened {
		release := pool.releaseLocked(taskID, previous)
		pool.mu.Unlock()
		release()
		pool.mu.Lock()
	}
	pool.inserts++
	opened.inserted = pool.inserts
	pool.sessions[taskID] = opened
	pool.mu.Unlock()
	if opener.Start == nil {
		return opened, nil
	}
	return opened, func() { opener.Start(opened.session) }
}

func (pool *SessionPool) reusable(current *held, input *ProviderRunInput, key string) bool {
	if !current.session.Live() || current.retire != nil {
		return false
	}
	if current.reload != nil && !current.session.Busy() {
		return false
	}
	if current.session.Key() != key || current.session.Answering() || input.ForkContinuation {
		return false
	}
	var handle *string
	if value, ok := ContinuationOf(input); ok {
		handle = &value
	}
	return current.session.Continues(handle)
}

// A session with nothing left to do is kept warm for a while, then handed back. Work the agent left
// running is not nothing: the deadline finds the session busy and starts over.
func (pool *SessionPool) rest(taskID string, current *held) {
	pool.mu.Lock()
	if pool.sessions[taskID] != current || !current.session.Live() {
		pool.mu.Unlock()
		return
	}
	if current.retire != nil && !current.session.Answering() {
		release := pool.releaseLocked(taskID, current)
		pool.mu.Unlock()
		release()
		return
	}
	busy := current.session.Busy()
	if current.reload != nil && !busy {
		release := pool.releaseLocked(taskID, current)
		pool.mu.Unlock()
		release()
		return
	}
	current.usedAt = application.Now()
	if current.idle != nil {
		current.idle.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(pool.idle, func() {
		pool.mu.Lock()
		if current.idle != timer {
			pool.mu.Unlock()
			return
		}
		current.idle = nil
		pool.mu.Unlock()
		if current.session.Busy() {
			pool.rest(taskID, current)
			return
		}
		pool.mu.Lock()
		release := pool.releaseLocked(taskID, current)
		pool.mu.Unlock()
		release()
	})
	current.idle = timer
	pool.mu.Unlock()
	current.resources.reportBusy(busy)
}

// Takes the session off the books under the lock; the returned func closes it and must run unlocked.
func (pool *SessionPool) releaseLocked(taskID string, current *held) func() {
	if current.idle != nil {
		current.idle.Stop()
		current.idle = nil
	}
	if pool.sessions[taskID] == current {
		delete(pool.sessions, taskID)
	}
	return func() {
		current.resources.release()
		closed := current.session.Close()
		if current.reload != nil {
			current.reload.resolve()
		}
		if current.retire != nil {
			current.retire.resolveAfter(closed)
		}
	}
}

func (pool *SessionPool) evictLocked() []func() {
	type entry struct {
		taskID string
		held   *held
	}
	var idle []entry
	for taskID, current := range pool.sessions {
		if !current.session.Busy() {
			idle = append(idle, entry{taskID, current})
		}
	}
	slices.SortStableFunc(idle, func(left, right entry) int {
		switch {
		case left.held.usedAt < right.held.usedAt:
			return -1
		case left.held.usedAt > right.held.usedAt:
			return 1
		case left.held.inserted < right.held.inserted:
			return -1
		case left.held.inserted > right.held.inserted:
			return 1
		}
		return 0
	})
	var releases []func()
	for over := len(pool.sessions) - MaxLiveSessions + 1; over > 0 && len(idle) > 0; over-- {
		next := idle[0]
		idle = idle[1:]
		releases = append(releases, pool.releaseLocked(next.taskID, next.held))
	}
	return releases
}

func run(actions []func()) {
	for _, action := range actions {
		action()
	}
}

func all(waiting []*waiter) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		for _, w := range waiting {
			<-w.done
		}
		close(done)
	}()
	return done
}
