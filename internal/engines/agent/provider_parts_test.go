package agent

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func shell(id string) *domain.BackgroundProcess {
	return &domain.BackgroundProcess{ID: id, Kind: "shell", Description: "npm run dev"}
}

type openedWork struct {
	work     *BackgroundWork
	reported [][]string
	rests    []int
}

func openWork(otherWork func() bool) *openedWork {
	if otherWork == nil {
		otherWork = func() bool { return false }
	}
	opened := &openedWork{}
	opened.work = NewBackgroundWork(func() bool { return opened.work.Running() || otherWork() }, func() { opened.rests = append(opened.rests, len(opened.reported)) })
	opened.work.OpenWith(func(report contracts.BackgroundReport) {
		ids := []string{}
		for _, process := range report.Processes {
			ids = append(ids, process.ID)
		}
		opened.reported = append(opened.reported, ids)
	})
	return opened
}

func TestBackgroundWorkReportsWholeSet(t *testing.T) {
	o := openWork(nil)
	equal(t, o.work.Running(), false)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, nil)
	equal(t, o.work.Running(), true)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1"), shell("bash-2")}, nil)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-2")}, nil)
	equal(t, o.reported, [][]string{{}, {"bash-1"}, {"bash-1", "bash-2"}, {"bash-2"}})
}

func TestBackgroundWorkStoppingRestsTheSession(t *testing.T) {
	o := openWork(nil)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, nil)
	equal(t, o.rests, []int(nil))
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-2")}, nil)
	equal(t, o.rests, []int(nil))
	o.work.Replace(nil, nil)
	equal(t, o.work.Running(), false)
	equal(t, o.rests, []int{4})
}

func TestBackgroundWorkWithOtherWorkGoingDoesNotRest(t *testing.T) {
	answering := true
	o := openWork(func() bool { return answering })
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, nil)
	o.work.Replace(nil, nil)
	equal(t, o.rests, []int(nil))
	answering = false
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, nil)
	o.work.Replace(nil, nil)
	equal(t, o.rests, []int{5})
}

func TestBackgroundWorkBusyCanBeWiderThanReported(t *testing.T) {
	o := openWork(nil)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, []string{"bash-1", "agent-1"})
	equal(t, o.reported, [][]string{{}, {"bash-1"}})
	o.work.Replace(nil, []string{"agent-1"})
	equal(t, o.work.Running(), true)
	equal(t, o.rests, []int(nil))
}

func TestBackgroundWorkSessionEndingEmptiesWithoutRest(t *testing.T) {
	o := openWork(nil)
	o.work.Replace([]*domain.BackgroundProcess{shell("bash-1")}, nil)
	o.work.Clear()
	equal(t, o.work.Running(), false)
	equal(t, o.reported, [][]string{{}, {"bash-1"}, {}})
	equal(t, o.rests, []int(nil))
}

type routerEngine struct {
	name  string
	log   *[]string
	holds []string
}

func (e routerEngine) held(taskID string) bool {
	for _, held := range e.holds {
		if held == taskID {
			return true
		}
	}
	return false
}
func (e routerEngine) Execute(run *ProviderRunInput) (ProviderResult, error) {
	*e.log = append(*e.log, fmt.Sprintf("%s:%s:%s", e.name, run.Engine, run.TaskID))
	return ProviderResult{Status: domain.RunStatusSucceeded, Message: e.name}, nil
}
func (e routerEngine) StopProcess(taskID, _ string) bool {
	*e.log = append(*e.log, fmt.Sprintf("%s:stop:%s:%v", e.name, taskID, e.held(taskID)))
	return e.held(taskID)
}
func (e routerEngine) LabelThread(taskID, title string) bool {
	*e.log = append(*e.log, fmt.Sprintf("%s:label:%s:%s:%v", e.name, taskID, title, e.held(taskID)))
	return e.held(taskID)
}
func (e routerEngine) ArchiveThread(taskID string, continuation *domain.Continuation, archived bool) bool {
	held := continuation != nil && continuation.Provider == e.name
	*e.log = append(*e.log, fmt.Sprintf("%s:archive:%s:%v:%v", e.name, taskID, archived, held))
	return held
}
func (e routerEngine) RetireOutOfRole(taskID string, role *domain.CoordinationRole) {
	name := "none"
	if role != nil {
		name = string(*role)
	}
	*e.log = append(*e.log, fmt.Sprintf("%s:role:%s:%s", e.name, taskID, name))
}
func (e routerEngine) CloseAll() { *e.log = append(*e.log, e.name+":closeAll") }

func everyEngine(log *[]string, holds map[string][]string) map[domain.AgentEngine]EngineProvider {
	providers := map[domain.AgentEngine]EngineProvider{}
	for _, engine := range domain.AgentEngines {
		providers[engine] = routerEngine{name: string(engine), log: log, holds: holds[string(engine)]}
	}
	return providers
}

func each(format string) []string {
	var lines []string
	for _, engine := range domain.AgentEngines {
		lines = append(lines, fmt.Sprintf(format, engine))
	}
	return lines
}

func TestRouterRoutesRunsAsksEachForStopsClosesAll(t *testing.T) {
	var log []string
	router := NewEngineRouter(everyEngine(&log, map[string][]string{"codex": {"task-c"}}))
	result, _ := router.Execute(testInput(func(input *ProviderRunInput) { input.Engine = "claude"; input.TaskID = "task-a" }))
	equal(t, result, ProviderResult{Status: domain.RunStatusSucceeded, Message: "claude"})
	result, _ = router.Execute(testInput(func(input *ProviderRunInput) { input.Engine = "codex"; input.TaskID = "task-c" }))
	equal(t, result, ProviderResult{Status: domain.RunStatusSucceeded, Message: "codex"})
	equal(t, router.StopProcess("task-c", "process-1"), true)
	equal(t, router.StopProcess("task-z", "process-1"), false)
	router.CloseAll()
	want := []string{"claude:claude:task-a", "codex:codex:task-c", "claude:stop:task-c:false", "codex:stop:task-c:true"}
	want = append(want, each("%s:stop:task-z:false")...)
	want = append(want, each("%s:closeAll")...)
	equal(t, log, want)
}

func TestRouterRoleChangeAsksEveryEngine(t *testing.T) {
	var log []string
	role := domain.CoordinationRoleCoordinator
	NewEngineRouter(everyEngine(&log, nil)).RetireOutOfRole("task-a", &role)
	equal(t, log, each("%s:role:task-a:coordinator"))
}

func TestRouterArchiveGoesToTheContinuationsEngine(t *testing.T) {
	var log []string
	router := NewEngineRouter(everyEngine(&log, nil))
	equal(t, router.ArchiveThread("task-o", &domain.Continuation{Provider: "opencode", Value: "ses_1"}, true), true)
	equal(t, router.ArchiveThread("task-g", &domain.Continuation{Provider: "gemini", Value: "g-1"}, true), false)
	equal(t, router.ArchiveThread("task-n", nil, true), false)
	equal(t, log, append([]string{"opencode:archive:task-o:true:true"}, each("%s:archive:task-n:true:false")...))
}

type ownSession struct{ *fakeSession }
type otherSession struct{ *fakeSession }

type testProvider[S PooledSession] struct {
	*PooledProvider[S]
	mu   sync.Mutex
	log  []string
	make func(hooks SessionHooks, log func(string)) S
}

func (p *testProvider[S]) record(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, line)
}

func (p *testProvider[S]) entries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.log...)
}

func (p *testProvider[S]) execute(run *ProviderRunInput) (ProviderResult, error) {
	return p.Run(run, "key", SessionOpener{Open: func(hooks SessionHooks) PooledSession { return p.make(hooks, p.record) }})
}

func (p *testProvider[S]) archive(taskID string, landed <-chan error, name string) {
	p.File(taskID, func() error {
		p.record("filing " + name)
		if err := <-landed; err != nil {
			return err
		}
		p.record("filed " + name)
		return nil
	})
}

func newOwnProvider(pool *SessionPool) *testProvider[ownSession] {
	return &testProvider[ownSession]{PooledProvider: NewPooledProvider[ownSession](pool), make: func(hooks SessionHooks, log func(string)) ownSession {
		session := newFakeSession("key", "own", hooks)
		session.run = func(run *ProviderRunInput) (ProviderResult, error) {
			log("run " + run.TaskID)
			return ProviderResult{Status: domain.RunStatusSucceeded}, nil
		}
		return ownSession{session}
	}}
}

func landed(err error) <-chan error {
	done := make(chan error, 1)
	done <- err
	return done
}

func TestPooledProviderRunWaitsForFilingsInOrder(t *testing.T) {
	provider := newOwnProvider(NewSessionPool(nil, 0))
	first := make(chan error, 1)
	provider.archive("t", first, "archive")
	provider.archive("t", landed(nil), "restore")
	running := make(chan struct{})
	go func() { provider.execute(task("t")); close(running) }()
	time.Sleep(5 * time.Millisecond)
	equal(t, provider.entries(), []string{"filing archive"})
	first <- nil
	<-running
	equal(t, provider.entries(), []string{"filing archive", "filed archive", "filing restore", "filed restore", "run t"})
}

func TestPooledProviderFailedFilingHoldsNothingUp(t *testing.T) {
	provider := newOwnProvider(NewSessionPool(nil, 0))
	provider.archive("t", landed(errors.New("offline")), "archive")
	provider.archive("t", landed(nil), "restore")
	provider.execute(task("t"))
	equal(t, provider.entries(), []string{"filing archive", "filing restore", "filed restore", "run t"})
}

func TestPooledProviderReadsBackOnlyItsOwnLiveSession(t *testing.T) {
	pool := NewSessionPool(nil, 0)
	own := newOwnProvider(pool)
	other := &testProvider[otherSession]{PooledProvider: NewPooledProvider[otherSession](pool), make: func(hooks SessionHooks, _ func(string)) otherSession {
		return otherSession{newFakeSession("key", "other", hooks)}
	}}
	own.execute(task("mine"))
	other.execute(task("theirs"))
	_, ok := own.Session("mine")
	equal(t, ok, true)
	_, ok = own.Session("theirs")
	equal(t, ok, false, "the other engine's session is not this one's")
	_, ok = other.Session("theirs")
	equal(t, ok, true)
	own.CloseAll()
	_, ok = own.Session("mine")
	equal(t, ok, false)
	_, ok = other.Session("theirs")
	equal(t, ok, false, "the engines share one pool, so closing it closes both")
}

func TestWarmSessionKeepsProcessOwnershipAndStaleTerminationMisses(t *testing.T) {
	var mu sync.Mutex
	var events []contracts.SessionResourceMessage
	pool := NewSessionPool(func(message contracts.SessionResourceMessage) {
		mu.Lock()
		events = append(events, message)
		mu.Unlock()
	}, 0)
	defer pool.CloseAll()
	sessions := &opened{}
	execute := func(fork bool) {
		var hooks SessionHooks
		pool.Execute(testInput(func(input *ProviderRunInput) {
			input.TaskID = "a"
			input.Engine = domain.AgentEngineCodex
			if fork {
				input.Continuation = &domain.Continuation{Provider: "codex", Value: "thread"}
				input.ForkContinuation = true
			}
		}), "engine", SessionOpener{
			Open: func(given SessionHooks) PooledSession {
				hooks = given
				session := newFakeSession("engine", "codex", given)
				session.busy.Store(true)
				sessions.add(session)
				return session
			},
			Start: func(PooledSession) { hooks.Resources.RegisterProcess(100 + sessions.count()) },
		})
	}
	processes := func() []contracts.SessionProcessEvent {
		mu.Lock()
		defer mu.Unlock()
		var found []contracts.SessionProcessEvent
		for _, event := range events {
			if process, ok := event.(contracts.SessionProcessEvent); ok {
				found = append(found, process)
			}
		}
		return found
	}
	execute(false)
	execute(false)
	first := processes()[0]
	equal(t, sessions.count(), 1)
	equal(t, pool.Terminate("other", first.SessionID, false), false)
	equal(t, pool.Terminate("a", first.SessionID, true), false, "a busy session is not reclaimed as idle")
	execute(true)
	equal(t, sessions.count(), 2)
	registrations := processes()
	equal(t, len(registrations), 2)
	equal(t, registrations[0].SessionID != registrations[1].SessionID, true)
	equal(t, pool.Terminate("a", first.SessionID, false), false)
	equal(t, pool.LiveSession("a"), PooledSession(sessions.at(1)))
	equal(t, pool.Terminate("a", registrations[1].SessionID, false), true)
	equal(t, pool.LiveSession("a"), nil)
	mu.Lock()
	defer mu.Unlock()
	released, busy := false, false
	for _, event := range events {
		if release, ok := event.(contracts.SessionReleaseEvent); ok && release.SessionID == first.SessionID {
			released = true
		}
		if state, ok := event.(contracts.SessionResourceMessageSessionState); ok && state.Busy {
			busy = true
		}
	}
	equal(t, released && busy, true)
}
