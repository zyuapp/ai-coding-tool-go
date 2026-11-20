package host

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// initialState is the stored workspace with the stored view preferences, read before anything runs.
func initialState(storage application.KeyValueStorage, viewportWidth *float64) *application.WorkspaceState {
	var state *application.WorkspaceState
	switch loaded := CreateTaskStore(storage).Load().(type) {
	case domain.ThreadStoreParseResultTrue:
		state = application.StateFromData(&loaded.Data, nil, nil, nil)
	case domain.ThreadStoreParseResultFalse:
		message := joinErrors(loaded.Errors)
		state = application.EmptyWorkspaceState(&message)
	default:
		state = application.EmptyWorkspaceState(nil)
	}
	next := application.Reduce(state, application.WorkspaceEventPreferencesLoaded{Preferences: LoadViewPreferences(storage, viewportWidth)})
	return &next.State
}

func joinErrors(errors []string) string {
	joined := ""
	for index, message := range errors {
		if index > 0 {
			joined += " "
		}
		joined += message
	}
	return joined
}

// settleSet counts work in flight; wait returns once none is left, including work added meanwhile.
// clear forgets everything held, so waiting no longer covers work already running.
type settleSet struct {
	mu      sync.Mutex
	epoch   int
	pending int
	idle    chan struct{}
}

func newSettleSet() *settleSet {
	idle := make(chan struct{})
	close(idle)
	return &settleSet{idle: idle}
}

func (s *settleSet) add() (done func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == 0 {
		s.idle = make(chan struct{})
	}
	s.pending++
	epoch := s.epoch
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.epoch != epoch {
				return
			}
			s.pending--
			if s.pending == 0 {
				close(s.idle)
			}
		})
	}
}

func (s *settleSet) wait(ctx context.Context) error {
	for {
		s.mu.Lock()
		idle := s.idle
		s.mu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
		s.mu.Lock()
		settled := s.pending == 0
		s.mu.Unlock()
		if settled {
			return nil
		}
	}
}

func (s *settleSet) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	if s.pending > 0 {
		s.pending = 0
		close(s.idle)
	}
}

// trackUntilSettled runs work on its own goroutine, held in set until it returns.
func trackUntilSettled(set *settleSet, work func()) {
	done := set.add()
	go func() {
		defer done()
		work()
	}()
}

// latch is a promise and the function that resolves it: Done closes once Resolve is first called.
type latch struct {
	once sync.Once
	done chan struct{}
}

func deferred() *latch { return &latch{done: make(chan struct{})} }

func (l *latch) Resolve()              { l.once.Do(func() { close(l.done) }) }
func (l *latch) Done() <-chan struct{} { return l.done }

// cameOnScreen lists the threads this change put on screen, which load their transcripts behind the
// loading state they show meanwhile.
func cameOnScreen(previous, next *application.WorkspaceState) []string {
	if sameText(next.CurrentID, previous.CurrentID) && jsonx.SameMap(next.Docks, previous.Docks) {
		return nil
	}
	var shown []string
	if next.CurrentID != nil && *next.CurrentID != "" && !sameText(next.CurrentID, previous.CurrentID) {
		shown = append(shown, *next.CurrentID)
	}
	if tab := application.ShownThreadTab(next); tab != nil && *tab != "" && !sameText(tab, application.ShownThreadTab(previous)) {
		shown = append(shown, *tab)
	}
	return shown
}

// sameState is TypeScript's `next === previous` for a state a transition hands back by value: every
// field holds what it held, compared as jsonx.Same compares.
func sameState(a, b *application.WorkspaceState) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return sameValue(reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem())
}

func sameValue(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Struct:
		for index := range a.NumField() {
			if !sameValue(a.Field(index), b.Field(index)) {
				return false
			}
		}
		return true
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() && b.IsNil()
		}
		if a.Elem().Type() != b.Elem().Type() {
			return false
		}
		return sameValue(a.Elem(), b.Elem())
	case reflect.Slice:
		return a.Len() == b.Len() && a.UnsafePointer() == b.UnsafePointer() && a.IsNil() == b.IsNil()
	case reflect.Map, reflect.Pointer, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return a.UnsafePointer() == b.UnsafePointer()
	case reflect.Array:
		for index := range a.Len() {
			if !sameValue(a.Index(index), b.Index(index)) {
				return false
			}
		}
		return true
	}
	return a.Equal(b)
}

// jobQueue holds work for the goroutine that owns the state, in the order it arrived. Posting never
// blocks, so work may be posted from that goroutine too.
type jobQueue struct {
	mu    sync.Mutex
	items []func()
	wake  chan struct{}
}

func (q *jobQueue) post(job func()) {
	q.mu.Lock()
	q.items = append(q.items, job)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *jobQueue) run() {
	for {
		q.mu.Lock()
		if len(q.items) == 0 {
			q.mu.Unlock()
			<-q.wake
			continue
		}
		job := q.items[0]
		q.items[0] = nil
		q.items = q.items[1:]
		q.mu.Unlock()
		job()
	}
}

// Runtime owns the workspace state, its effects and its durability, with one lifetime independent of
// whatever displays them. One goroutine owns the state: inputs reach it through a queue and run one at
// a time, each reduced and committed there; effects run on their own goroutines and answer with
// inputs sent back to it. Published states are immutable, so GetState may be read from any goroutine.
type Runtime struct {
	host    WorkspaceRuntimeHost
	desktop RuntimeDesktop
	state   atomic.Pointer[application.WorkspaceState]
	jobs    *jobQueue
	// ordered runs the effects that must reach the desktop in order, one after another.
	ordered *jobQueue
	loaded  *latch

	mu               sync.Mutex
	listeners        map[int]func()
	nextID           int
	persistenceReady bool
	started          *latch
	stopSubs         func()
	flushSubs        func()
	generation       int
	disposed         bool

	// Touched only by the goroutine that owns the state.
	badgeCount float64
	mobileView *MobileViewHolder

	effects              *settleSet
	environmentPoll      *environmentPoll
	persistence          *PersistenceQueue
	waiters              *ThreadWaiters
	environmentRefreshes EnvironmentRefreshes
	drafts               *DraftPersistence
	snoozeTimer          *SnoozeTimer
	limitTimer           *SnoozeTimer
	toastTimers          *ToastTimers
	history              *RuntimeHistory
	inputs               *RuntimeInputs

	// subscribe starts the host modules and perform carries out one effect.
	subscribe func(host SubscriptionHost) (stop func(), flush func())
	perform   func(ctx context.Context, effect application.WorkspaceEffect, host EffectHost) error
}

// WorkspaceRuntime is TypeScript's name for the runtime's type.
type WorkspaceRuntime = Runtime

func CreateWorkspaceRuntime(host WorkspaceRuntimeHost) *Runtime {
	r := &Runtime{
		host:                 host,
		desktop:              host.Desktop,
		jobs:                 &jobQueue{wake: make(chan struct{}, 1)},
		ordered:              &jobQueue{wake: make(chan struct{}, 1)},
		loaded:               deferred(),
		listeners:            map[int]func(){},
		badgeCount:           -1,
		mobileView:           NoMobileView(),
		effects:              newSettleSet(),
		persistence:          &PersistenceQueue{},
		waiters:              &ThreadWaiters{},
		environmentRefreshes: EnvironmentRefreshes{Current: map[string]*EnvironmentRefreshEffect{}},
		subscribe:            SubscribeWorkspaceRuntime,
		perform:              RunWorkspaceEffect,
	}
	r.state.Store(initialState(host.Storage, host.ViewportWidth))
	r.environmentPoll = createEnvironmentPoll(func() { r.send(contracts.ViewCommandViewRefreshEnvironment{}) })
	r.drafts = CreateDraftPersistence(host.Storage, r.GetState, r.Dispatch)
	r.snoozeTimer = CreateSnoozeTimer(func(at float64) { r.send(application.WorkspaceEventSnoozesElapsed{At: at}) })
	r.limitTimer = CreateSnoozeTimer(func(at float64) { r.send(application.WorkspaceEventLimitsElapsed{At: at}) })
	r.toastTimers = CreateToastTimers(func(id float64) { r.send(contracts.ViewCommandViewDismissToast{ID: id}) })
	r.history = createRuntimeHistory(historyHost{
		state:  r.GetState,
		load:   r.desktop.LoadThreadMessages,
		search: r.desktop.SearchThreadMessages,
		dispatch: func(ctx context.Context, input application.WorkspaceInput) error {
			_, err := r.rawExecute(input).Completed(ctx)
			return err
		},
		persistence: r.persistence,
	})
	r.inputs = CreateRuntimeInputs(RuntimeInputHost{
		Generation: r.currentGeneration,
		Active:     r.active,
		Screen:     func() string { return application.ScreenOf(r.GetState()) },
		History:    r.history,
		Execute:    r.rawExecute,
		Track: func(completed func(ctx context.Context) (contracts.WorkspaceCommandResult, error)) {
			trackUntilSettled(r.effects, func() { _, _ = completed(background) })
		},
	})
	go r.jobs.run()
	go r.ordered.run()
	return r
}

func (r *Runtime) currentGeneration() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generation
}

func (r *Runtime) active(generation int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.disposed && r.generation == generation
}

func (r *Runtime) isDisposed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.disposed
}

// GetState is the newest committed state.
func (r *Runtime) GetState() *application.WorkspaceState { return r.state.Load() }

// Subscribe calls listener after every commit, on the goroutine that owns the state, until the
// returned func is called. A listener must not wait on the runtime.
func (r *Runtime) Subscribe(listener func()) func() {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextID
	r.nextID++
	r.listeners[id] = listener
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.listeners, id)
	}
}

// Execute runs an input after the inputs it has to follow. It returns at once: Accepted answers once
// the reducer has taken it, and Completed once its effects and their replies have settled.
func (r *Runtime) Execute(input application.WorkspaceInput) application.WorkspaceExecution {
	return r.inputs.Execute(input)
}

// Dispatch runs one input and waits for it and its effects to complete.
func (r *Runtime) Dispatch(ctx context.Context, input application.WorkspaceInput) error {
	_, err := r.inputs.Execute(input).Completed(ctx)
	return err
}

// send is TypeScript's `void dispatch(input)`: the input starts now, and nothing waits for it.
func (r *Runtime) send(input application.WorkspaceInput) { r.inputs.Execute(input) }

func (r *Runtime) commit(next *application.WorkspaceState, input application.WorkspaceInput) {
	if r.isDisposed() {
		return
	}
	previous := r.state.Load()
	if sameState(previous, next) {
		return
	}
	r.state.Store(next)
	if !jsonx.SameMap(next.Prompts, previous.Prompts) {
		r.drafts.Changed()
	}
	ReleaseThreadWaiters(r.waiters, next)
	if r.badgeCount == -1 || !jsonx.SameSlice(next.Threads, previous.Threads) || !jsonx.SameSlice(next.SideChats, previous.SideChats) ||
		!jsonx.SameSlice(next.Computers.Paired, previous.Computers.Paired) || next.Computers.Filter != previous.Computers.Filter {
		count := application.ThreadActivityThreadLists(next).UnreadCount + application.RemoteUnreadCount(&next.Computers)
		if count != r.badgeCount {
			r.desktop.SetBadgeCount(count)
		}
		r.badgeCount = count
	}
	if update := NextMobileUpdate(r.mobileView, next, application.Now()); update != nil {
		r.desktop.PublishMobileView(update)
	}
	r.mu.Lock()
	listeners := make([]func(), 0, len(r.listeners))
	for id := range r.nextID {
		if listener, ok := r.listeners[id]; ok {
			listeners = append(listeners, listener)
		}
	}
	started := r.started != nil
	persistenceReady := r.persistenceReady
	r.mu.Unlock()
	for _, listener := range listeners {
		listener()
	}
	if started {
		r.refreshEnvironment()
	}
	for _, taskID := range cameOnScreen(previous, next) {
		trackUntilSettled(r.effects, func() {
			if err := r.history.Hydrate(background, taskID); err != nil {
				_, _ = r.rawExecute(contracts.ActionFailedEvent{Message: errorMessage(err)}).Completed(background)
			}
		})
	}
	if !persistenceReady || !next.Writable || next.StorageError != nil {
		return
	}
	switch input.(type) {
	case application.WorkspaceEventSubagentActivityLoaded, application.WorkspaceEventStoreThreadLoaded:
		return
	}
	if !HasPersistenceChanges(WorkspacePersistencePersistenceState(previous), WorkspacePersistencePersistenceState(next)) {
		return
	}
	persistenceMu.Lock()
	r.persistence.Pending = WorkspacePersistencePersistenceState(next)
	persistenceMu.Unlock()
	go func() {
		if err := DrainLatestPersistence(background, r.persistence, r.desktop.PersistTaskStore); err != nil {
			r.storageFailed(err)
		}
	}()
}

func (r *Runtime) storageFailed(err error) {
	r.mu.Lock()
	r.persistenceReady = false
	r.mu.Unlock()
	r.send(application.WorkspaceEventStoreFailed{Message: errorMessage(err)})
}

// rawExecute runs an input now, without waiting for the inputs ahead of it.
func (r *Runtime) rawExecute(input application.WorkspaceInput) application.WorkspaceExecution {
	execution := r.executeAt(input, r.currentGeneration())
	trackUntilSettled(r.effects, func() { _, _ = execution.Completed(background) })
	return execution
}

// executeAt reduces and commits an input on the goroutine that owns the state, then performs its
// effects. Completion waits for this input's own effects and the replies they dispatch, but not for
// effects marked FollowUp, which are only tracked.
func (r *Runtime) executeAt(input application.WorkspaceInput, generation int) application.WorkspaceExecution {
	acceptance := newFuture[contracts.WorkspaceCommandResult]()
	completion := newFuture[contracts.WorkspaceCommandResult]()
	r.jobs.post(func() {
		if !r.active(generation) {
			result := contracts.WorkspaceCommandResultFalse{Message: closed}
			acceptance.settle(result, nil)
			completion.settle(result, nil)
			return
		}
		transition, failure := reduced(r.state.Load(), input)
		if failure != nil {
			acceptance.settle(nil, failure)
			completion.settle(nil, failure)
			return
		}
		r.commit(&transition.State, input)
		result := transition.Result
		if result == nil {
			result = acceptedResult
		}
		acceptance.settle(result, nil)
		var owned []*commandResult
		for _, effect := range transition.Effects {
			work := r.performEffect(effect, generation)
			if followUp(effect) {
				trackUntilSettled(r.effects, func() { _, _ = work.wait(background) })
			} else {
				owned = append(owned, work)
			}
		}
		go func() {
			replies := make([]contracts.WorkspaceCommandResult, 0, len(owned))
			for _, work := range owned {
				reply, err := work.wait(background)
				if err != nil {
					completion.settle(nil, err)
					return
				}
				replies = append(replies, reply)
			}
			completion.settle(combineResults(result, replies), nil)
		}()
	})
	return execution(acceptance, completion)
}

// reduced is application.Reduce, with a panic turned into the error a throwing reducer rejects with.
func reduced(state *application.WorkspaceState, input application.WorkspaceInput) (transition application.WorkspaceTransition, failure error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = panicError(recovered)
		}
	}()
	return application.Reduce(state, input), nil
}

func panicError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("%v", recovered)
}

// followUp says whether an effect only keeps a view current, which the input does not wait for.
func followUp(effect application.WorkspaceEffect) bool {
	field := reflect.ValueOf(effect).FieldByName("FollowUp")
	return field.IsValid() && !field.IsNil() && field.Elem().Bool()
}

// inOrder is whether an effect must reach the desktop in the order the reducer made it, as a
// shell's keystrokes must: TypeScript starts every effect synchronously, so its first call keeps order.
func inOrder(effect application.WorkspaceEffect) bool {
	switch effect.(type) {
	case application.WorkspaceEffectTerminalStart, application.WorkspaceEffectTerminalWrite, application.WorkspaceEffectTerminalResize, application.WorkspaceEffectTerminalClose:
		return true
	}
	return false
}

// performEffect carries out an effect on its own goroutine, or in the ordered lane, answering with
// what its replies said.
func (r *Runtime) performEffect(effect application.WorkspaceEffect, generation int) *commandResult {
	work := newFuture[contracts.WorkspaceCommandResult]()
	start := func(run func()) { go run() }
	if inOrder(effect) {
		start = r.ordered.post
	}
	start(func() {
		var (
			mu      sync.Mutex
			replies []contracts.WorkspaceCommandResult
		)
		dispatch := func(ctx context.Context, reply application.WorkspaceInput) error {
			if !r.active(generation) {
				return nil
			}
			for _, taskID := range r.history.Needed(reply) {
				if err := r.history.Hydrate(ctx, taskID); err != nil {
					return err
				}
			}
			result, err := r.executeAt(reply, generation).Completed(ctx)
			if err != nil {
				return err
			}
			mu.Lock()
			replies = append(replies, result)
			mu.Unlock()
			return nil
		}
		if err := r.runEffect(effect, dispatch); err != nil {
			if err := dispatch(background, contracts.ActionFailedEvent{Message: err.Error()}); err != nil {
				work.settle(nil, err)
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		work.settle(combineResults(acceptedResult, replies), nil)
	})
	return work
}

func (r *Runtime) runEffect(effect application.WorkspaceEffect, dispatch func(ctx context.Context, input application.WorkspaceInput) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError(recovered)
		}
	}()
	return r.perform(background, effect, EffectHost{
		Dispatch:               dispatch,
		Desktop:                r.desktop,
		Storage:                r.host.Storage,
		EnvironmentRefreshes:   r.environmentRefreshes,
		ScheduleSnoozeExpiry:   r.snoozeTimer.Schedule,
		ScheduleLimitReset:     r.limitTimer.Schedule,
		ScheduleToastDismissal: r.toastTimers.Schedule,
		Surface:                r.host.Surface,
	})
}

func combineResults(initial contracts.WorkspaceCommandResult, replies []contracts.WorkspaceCommandResult) contracts.WorkspaceCommandResult {
	if !isOK(initial) {
		return initial
	}
	result := initial
	for _, reply := range replies {
		if !isOK(reply) {
			return reply
		}
		if resultTaskID(reply) != nil {
			result = reply
		}
	}
	return result
}

func (r *Runtime) refreshEnvironment() {
	state := r.GetState()
	inputs := environmentInputs{CurrentID: state.CurrentID, DraftProjectID: state.DraftProjectID, Projects: state.Projects}
	if state.CurrentID != nil {
		if thread := findThread(state.Threads, *state.CurrentID); thread != nil {
			inputs.WorktreeID = thread.WorktreeID
		}
		if record := application.RunRecord(state.ActiveRuns[*state.CurrentID]); record != nil {
			inputs.RunID = &record.RunID
		}
	}
	r.environmentPoll.update(inputs, inputs.RunID != nil)
}

func (r *Runtime) initialize(generation int) {
	defer r.loaded.Resolve()
	err := func() error {
		data, err := r.desktop.LoadTaskStore(background)
		if err != nil {
			return err
		}
		if !r.active(generation) {
			return nil
		}
		persistenceMu.Lock()
		if data != nil {
			r.persistence.Persisted = PersistedStoreState(&data.ThreadStoreData)
		} else {
			r.persistence.Persisted = nil
		}
		persistenceMu.Unlock()
		if data != nil {
			loaded := application.WorkspaceEventStoreLoaded{Data: data.ThreadStoreData, HiddenTasks: &data.HiddenTasks}
			if data.ResumeAfterUpdate != nil {
				loaded.Resuming = data.ResumeAfterUpdate
			}
			err = r.Dispatch(background, loaded)
		} else {
			err = r.Dispatch(background, application.WorkspaceEventStoreAbsent{})
		}
		if err != nil {
			return err
		}
		if err := r.drafts.Restore(background); err != nil {
			return err
		}
		r.loaded.Resolve()
		if current := r.GetState().CurrentID; current != nil && *current != "" {
			if err := r.history.Hydrate(background, *current); err != nil {
				if _, err := r.rawExecute(contracts.ActionFailedEvent{Message: errorMessage(err)}).Completed(background); err != nil {
					return err
				}
			}
		}
		if !r.active(generation) {
			return nil
		}
		persistenceMu.Lock()
		r.persistence.Pending = WorkspacePersistencePersistenceState(r.GetState())
		persistenceMu.Unlock()
		r.mu.Lock()
		r.persistenceReady = true
		r.mu.Unlock()
		return DrainLatestPersistence(background, r.persistence, r.desktop.PersistTaskStore)
	}()
	if err != nil && r.active(generation) {
		r.storageFailed(err)
	}
}

// QueryThreads answers a paired computer's thread query from this runtime. The answer is a
// *contracts.ThreadTranscript or []*contracts.ThreadSummary.
func (r *Runtime) QueryThreads(ctx context.Context, query contracts.ComputerThreadQuery) (any, error) {
	return LocalThreadReader(r.GetState, r.history.PrepareThreadRequest, func() {
		r.mu.Lock()
		flush := r.flushSubs
		r.mu.Unlock()
		if flush != nil {
			flush()
		}
	}, r.isDisposed)(ctx, query)
}

func (r *Runtime) isStarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started != nil
}

func (r *Runtime) SetViewVisible(visible bool) {
	r.environmentPoll.setVisible(visible, r.isStarted())
}

func (r *Runtime) SetViewInUse(inUse bool) {
	r.environmentPoll.setInUse(inUse, r.isStarted())
}

// RestoreDrafts reads the stored drafts again, for text that arrived in storage after the start.
func (r *Runtime) RestoreDrafts(ctx context.Context) error {
	return r.drafts.Restore(ctx)
}

// Loaded closes once the store and drafts are in, before the current thread's history, or on dispose.
func (r *Runtime) Loaded() <-chan struct{} { return r.loaded.Done() }

// Start starts the subscriptions and loads the store. Starting again while started answers the same
// channel, which closes once loading has finished, whether or not it succeeded.
func (r *Runtime) Start() <-chan struct{} {
	r.mu.Lock()
	if r.started != nil {
		started := r.started
		r.mu.Unlock()
		return started.Done()
	}
	r.disposed = false
	// Subscriptions can start effects immediately; their replies belong to this generation.
	r.generation++
	generation := r.generation
	started := deferred()
	r.started = started
	r.mu.Unlock()
	stop, flush := r.subscribe(SubscriptionHost{
		State:                r.GetState,
		Dispatch:             r.Dispatch,
		Execute:              r.inputs.Execute,
		Waiters:              r.waiters,
		PrepareThreadRequest: r.history.PrepareThreadRequest,
		Hydrate:              r.history.Hydrate,
		Desktop:              r.desktop,
		Frame:                r.host.Frame,
	})
	r.mu.Lock()
	if r.started == started {
		r.stopSubs, r.flushSubs = stop, flush
	} else {
		defer stop()
	}
	r.mu.Unlock()
	go func() {
		defer started.Resolve()
		r.initialize(generation)
	}()
	r.refreshEnvironment()
	return started.Done()
}

// Flush waits for every input and effect in flight and every write behind them.
func (r *Runtime) Flush(ctx context.Context) error {
	r.mu.Lock()
	started, flushSubs := r.started, r.flushSubs
	r.mu.Unlock()
	if started != nil {
		select {
		case <-started.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if flushSubs != nil {
		flushSubs()
	}
	if err := r.inputs.Settled(ctx); err != nil {
		return err
	}
	if err := r.effects.wait(ctx); err != nil {
		return err
	}
	r.drafts.Flush()
	if storageError := r.GetState().StorageError; storageError != nil {
		persistenceMu.Lock()
		if r.persistence.Pending == nil {
			persistenceMu.Unlock()
			return errors.New(*storageError)
		}
		r.persistence.Pending = WorkspacePersistencePersistenceState(r.GetState())
		persistenceMu.Unlock()
		if err := DrainLatestPersistence(ctx, r.persistence, r.desktop.PersistTaskStore); err != nil {
			return err
		}
		r.mu.Lock()
		r.persistenceReady = true
		r.mu.Unlock()
		if _, err := r.rawExecute(application.WorkspaceEventStorePersisted{}).Completed(ctx); err != nil {
			return err
		}
		persistenceMu.Lock()
		r.persistence.Pending = WorkspacePersistencePersistenceState(r.GetState())
		persistenceMu.Unlock()
	}
	return DrainLatestPersistence(ctx, r.persistence, r.desktop.PersistTaskStore)
}

func (r *Runtime) Dispose() {
	r.mu.Lock()
	r.disposed = true
	stop := r.stopSubs
	r.stopSubs, r.flushSubs = nil, nil
	r.mu.Unlock()
	r.loaded.Resolve()
	if stop != nil {
		stop()
	}
	r.environmentPoll.stop()
	r.drafts.Dispose()
	r.snoozeTimer.Dispose()
	r.limitTimer.Dispose()
	r.toastTimers.Dispose()
	r.mu.Lock()
	r.started = nil
	r.generation++
	r.mu.Unlock()
	r.history.Invalidate()
	r.inputs.Reset()
	r.effects.clear()
}

// environmentInputs is what decides the checkout in front; a change to any of it reads Git again.
// Projects is compared by identity.
type environmentInputs struct {
	CurrentID      *string
	DraftProjectID *string
	WorktreeID     *string
	Projects       []*domain.Project
	RunID          *string
}

func (a environmentInputs) same(b environmentInputs) bool {
	return sameText(a.CurrentID, b.CurrentID) && sameText(a.DraftProjectID, b.DraftProjectID) && sameText(a.WorktreeID, b.WorktreeID) &&
		jsonx.SameSlice(a.Projects, b.Projects) && (a.Projects == nil) == (b.Projects == nil) && sameText(a.RunID, b.RunID)
}

// environmentPoll asks Git about the checkout in front at once, then on a timer: often during a run
// someone is watching, rarely otherwise. Git is polled only while the window can show the answer; a
// run's end reads it regardless.
type environmentPoll struct {
	mu      sync.Mutex
	refresh func()
	ticker  *time.Ticker
	stopped chan struct{}
	inputs  *environmentInputs
	running bool
	visible bool
	inUse   bool
}

func createEnvironmentPoll(refresh func()) *environmentPoll {
	return &environmentPoll{refresh: refresh, visible: true, inUse: true}
}

// restart runs with p.mu held.
func (p *environmentPoll) restart(read bool) {
	p.halt()
	if !p.visible {
		return
	}
	if read {
		p.refresh()
	}
	interval := 15 * time.Second
	if p.running {
		interval = 10 * time.Second
		if p.inUse {
			interval = 2 * time.Second
		}
	}
	ticker, stopped := time.NewTicker(interval), make(chan struct{})
	p.ticker, p.stopped = ticker, stopped
	go func() {
		for {
			select {
			case <-ticker.C:
				p.refresh()
			case <-stopped:
				return
			}
		}
	}()
}

// halt runs with p.mu held.
func (p *environmentPoll) halt() {
	if p.ticker != nil {
		p.ticker.Stop()
		close(p.stopped)
		p.ticker, p.stopped = nil, nil
	}
}

func (p *environmentPoll) update(next environmentInputs, run bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inputs != nil && p.inputs.same(next) {
		return
	}
	p.inputs = &next
	p.running = run
	p.restart(true)
}

func (p *environmentPoll) setVisible(next, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if next == p.visible {
		return
	}
	p.visible = next
	if active {
		p.restart(true)
	}
}

func (p *environmentPoll) setInUse(next, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if next == p.inUse {
		return
	}
	p.inUse = next
	if active && p.running {
		p.restart(false)
	}
}

func (p *environmentPoll) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.halt()
	p.inputs = nil
}
