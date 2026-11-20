package agent

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type fakeSession struct {
	key          string
	engine       string
	hooks        SessionHooks
	live         atomic.Bool
	busy         atomic.Bool
	answering    atomic.Bool
	mu           sync.Mutex
	continuation *string
	run          func(input *ProviderRunInput) (ProviderResult, error)
}

func newFakeSession(key, engine string, hooks SessionHooks) *fakeSession {
	session := &fakeSession{key: key, engine: engine, hooks: hooks}
	session.live.Store(true)
	return session
}

func (s *fakeSession) Key() string     { return s.key }
func (s *fakeSession) Live() bool      { return s.live.Load() }
func (s *fakeSession) Busy() bool      { return s.busy.Load() }
func (s *fakeSession) Answering() bool { return s.answering.Load() }
func (s *fakeSession) rest()           { s.hooks.Rested() }
func (s *fakeSession) Continues(continuation *string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return continuation == nil || (s.continuation != nil && *continuation == *s.continuation)
}
func (s *fakeSession) Run(input *ProviderRunInput) (ProviderResult, error) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run != nil {
		return run(input)
	}
	return ProviderResult{Status: domain.RunStatusSucceeded, Message: s.engine + ":" + input.TaskID}, nil
}

// Ends the session the way a process exiting would: Ended runs off the closing call.
func (s *fakeSession) Close() <-chan struct{} {
	if s.live.Swap(false) {
		go s.hooks.Ended()
	}
	return Settled()
}

type opened struct {
	mu       sync.Mutex
	sessions []*fakeSession
}

func (o *opened) add(session *fakeSession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sessions = append(o.sessions, session)
}

func (o *opened) at(index int) *fakeSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[index]
}

func (o *opened) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.sessions)
}

func (o *opened) lives() []bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	var lives []bool
	for _, session := range o.sessions {
		lives = append(lives, session.Live())
	}
	return lives
}

// Each engine opens its own sessions on the one pool, the way a channel's providers do.
func poolEngine(name string, pool *SessionPool, sessions *opened) func(input *ProviderRunInput) ProviderResult {
	return func(input *ProviderRunInput) ProviderResult {
		result, _ := pool.Execute(input, name+"-key", SessionOpener{Open: func(hooks SessionHooks) PooledSession {
			session := newFakeSession(name+"-key", name, hooks)
			sessions.add(session)
			return session
		}})
		return result
	}
}

func task(id string) *ProviderRunInput {
	return testInput(func(input *ProviderRunInput) { input.TaskID = id; input.Engine = domain.AgentEngineCodex })
}

func TestPoolEnginesShareTheCapAndColdestIdleGoes(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	claude := poolEngine("claude", pool, sessions)
	codex := poolEngine("codex", pool, sessions)
	equal(t, claude(task("a")), ProviderResult{Status: domain.RunStatusSucceeded, Message: "claude:a"})
	codex(task("b"))
	claude(task("c"))
	equal(t, sessions.lives(), []bool{true, true, true})
	codex(task("d"))
	equal(t, sessions.lives(), []bool{false, true, true, true}, "the Claude session of thread a was the coldest")
	claude(task("c"))
	equal(t, sessions.count(), 4, "thread c's session is warm and reused")
	equal(t, pool.LiveSession("c"), PooledSession(sessions.at(2)))
	equal(t, pool.LiveSession("a"), nil)
	pool.CloseAll()
	equal(t, sessions.lives(), []bool{false, false, false, false})
}

func TestPoolRunsStartOnceItHoldsTheSession(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	var seen []any
	result, _ := pool.Execute(task("t"), "key", SessionOpener{
		Open: func(hooks SessionHooks) PooledSession { return newFakeSession("key", "codex", hooks) },
		Start: func(session PooledSession) {
			seen = append(seen, pool.LiveSession("t") == session)
			session.(*fakeSession).live.Store(false)
			session.(*fakeSession).hooks.Ended()
			seen = append(seen, pool.LiveSession("t") == nil)
		},
	})
	equal(t, seen, []any{true, true})
	equal(t, result.Status, domain.RunStatusSucceeded)
}

func TestPoolReloadReleasesIdleSessionsAndResumesOnAFreshOne(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		pool := NewSessionPool(nil, 0)
		sessions := &opened{}
		poolEngine(name, pool, sessions)(task("reload"))
		sessions.at(0).continuation = ptr("existing-conversation")
		<-pool.ReloadSettings()
		equal(t, sessions.at(0).Live(), false)
		var resumed string
		pool.Execute(testInput(func(input *ProviderRunInput) {
			input.TaskID = "reload"
			input.Engine = domain.AgentEngine(name)
			input.Continuation = &domain.Continuation{Provider: name, Value: "existing-conversation"}
		}), name+"-key", SessionOpener{Open: func(hooks SessionHooks) PooledSession {
			session := newFakeSession(name+"-key", name, hooks)
			session.run = func(next *ProviderRunInput) (ProviderResult, error) {
				resumed = next.Continuation.Value
				return ProviderResult{Status: domain.RunStatusSucceeded}, nil
			}
			return session
		}})
		equal(t, resumed, "existing-conversation")
		pool.CloseAll()
	}
}

func closedSoon(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	case <-time.After(20 * time.Millisecond):
		return false
	}
}

func TestPoolReloadWaitsForActiveRunAndBackgroundWork(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	finish := make(chan ProviderResult)
	running := make(chan ProviderResult, 1)
	go func() {
		result, _ := pool.Execute(task("busy"), "codex-key", SessionOpener{Open: func(hooks SessionHooks) PooledSession {
			session := newFakeSession("codex-key", "codex", hooks)
			session.busy.Store(true)
			session.answering.Store(true)
			session.run = func(*ProviderRunInput) (ProviderResult, error) { return <-finish, nil }
			sessions.add(session)
			return session
		}})
		running <- result
	}()
	eventually(t, func() bool { return sessions.count() == 1 })
	reload := pool.ReloadSettings()
	equal(t, closedSoon(reload), false)
	equal(t, sessions.at(0).Live(), true)
	sessions.at(0).answering.Store(false)
	finish <- ProviderResult{Status: domain.RunStatusSucceeded}
	equal(t, (<-running).Status, domain.RunStatusSucceeded)
	equal(t, closedSoon(reload), false, "background work still owns the session")
	poolEngine("claude", pool, sessions)(task("new"))
	sessions.at(0).busy.Store(false)
	sessions.at(0).rest()
	<-reload
	equal(t, sessions.at(0).Live(), false)
	equal(t, sessions.at(1).Live(), true, "a session launched after reload already has fresh settings")
	pool.CloseAll()
}

func TestPoolRepeatedReloadsSettleIfBusySessionExits(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	poolEngine("codex", pool, sessions)(task("busy"))
	sessions.at(0).busy.Store(true)
	first, second := pool.ReloadSettings(), pool.ReloadSettings()
	sessions.at(0).Close()
	<-first
	<-second
	eventually(t, func() bool { return pool.LiveSession("busy") == nil })
	<-pool.ReloadSettings()
}

func TestPoolRetireLetsSessionGoOnceTurnAnswered(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	codex := poolEngine("codex", pool, sessions)
	codex(task("t"))
	first := sessions.at(0)
	first.answering.Store(true)
	retiring := pool.Retire("t")
	equal(t, closedSoon(retiring), false)
	equal(t, first.Live(), true, "a turn still answering keeps its session")
	first.answering.Store(false)
	codex(task("t"))
	equal(t, first.Live(), false)
	equal(t, sessions.count(), 2, "a retiring session is never reused")
	<-retiring
	<-pool.Retire("t")
	equal(t, sessions.at(1).Live(), false, "an idle session goes at once")
	<-pool.Retire("missing")
}

func TestPoolRetiresSessionOfAnotherRole(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	claude := poolEngine("claude", pool, sessions)
	member, coordinator := domain.CoordinationRoleMember, domain.CoordinationRoleCoordinator
	claude(testInput(func(input *ProviderRunInput) { input.TaskID = "t"; input.CoordinationRole = &member }))
	session := sessions.at(0)
	<-pool.RetireOutOfRole("t", &member)
	equal(t, session.Live(), true)
	session.answering.Store(true)
	retiring := pool.RetireOutOfRole("t", &coordinator)
	equal(t, closedSoon(retiring), false)
	equal(t, session.Live(), true, "a turn the window asked for still answers first")
	session.answering.Store(false)
	claude(testInput(func(input *ProviderRunInput) { input.TaskID = "t"; input.CoordinationRole = &coordinator }))
	equal(t, session.Live(), false)
	<-retiring
	<-pool.RetireOutOfRole("missing", nil)
}

func TestPoolRunAfterCancelledTurnWaitsAndKeepsWarmSession(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	sessions := &opened{}
	answer := make(chan struct{})
	open := func(hooks SessionHooks) PooledSession {
		session := newFakeSession("key", "claude", hooks)
		if sessions.count() == 0 {
			session.run = func(run *ProviderRunInput) (ProviderResult, error) {
				session.answering.Store(true)
				session.mu.Lock()
				session.run = nil
				session.mu.Unlock()
				<-answer
				session.answering.Store(false)
				return ProviderResult{Status: domain.RunStatusCancelled, Message: run.TaskID}, nil
			}
		}
		sessions.add(session)
		return session
	}
	execute := func(input *ProviderRunInput) <-chan ProviderResult {
		result := make(chan ProviderResult, 1)
		go func() {
			r, _ := pool.Execute(input, "key", SessionOpener{Open: open})
			result <- r
		}()
		return result
	}
	first := task("t")
	cancelled := execute(first)
	eventually(t, func() bool { return sessions.count() == 1 && sessions.at(0).Answering() })
	first.Abort()
	superseded := task("t")
	skipped := execute(superseded)
	time.Sleep(5 * time.Millisecond)
	next := execute(task("t"))
	superseded.Abort()
	time.Sleep(10 * time.Millisecond)
	equal(t, sessions.at(0).Live(), true, "the session is not closed under its interrupted turn")
	close(answer)
	equal(t, (<-cancelled).Status, domain.RunStatusCancelled)
	equal(t, <-skipped, ProviderResult{Status: domain.RunStatusCancelled}, "a run cancelled while it waited never takes the session")
	equal(t, (<-next).Status, domain.RunStatusSucceeded)
	equal(t, sessions.count(), 1, "the next run reuses the warm session")
}

func TestPoolIdleSessionIsHandedBackAfterItsDeadline(t *testing.T) {
	pool := NewSessionPool(nil, 10*time.Millisecond)
	sessions := &opened{}
	codex := poolEngine("codex", pool, sessions)
	codex(task("t"))
	sessions.at(0).busy.Store(true)
	time.Sleep(30 * time.Millisecond)
	equal(t, sessions.at(0).Live(), true, "busy work keeps it")
	sessions.at(0).busy.Store(false)
	eventually(t, func() bool { return !sessions.at(0).Live() })
}
