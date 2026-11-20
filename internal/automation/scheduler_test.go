package automation

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type memoryStore struct {
	mu   sync.Mutex
	rows map[string]*domain.Automation
	// Hooks a test sets to hold or fail a write.
	save   func(*domain.Automation) error
	delete func(string) error
}

func newMemoryStore(initial ...*domain.Automation) *memoryStore {
	store := &memoryStore{rows: map[string]*domain.Automation{}}
	for _, automation := range initial {
		store.rows[automation.ID] = automation
	}
	return store
}

func (m *memoryStore) ListAutomations(context.Context) ([]*domain.Automation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var list []*domain.Automation
	for _, automation := range m.rows {
		list = append(list, automation)
	}
	return list, nil
}

func (m *memoryStore) SaveAutomation(_ context.Context, automation *domain.Automation) error {
	if m.save != nil {
		if err := m.save(automation); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[automation.ID] = automation
	return nil
}

func (m *memoryStore) DeleteAutomation(_ context.Context, id string) error {
	if m.delete != nil {
		if err := m.delete(id); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, id)
	return nil
}

func (m *memoryStore) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows)
}

func (m *memoryStore) row(id string) *domain.Automation {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[id]
}

type fixedClock struct {
	mu    sync.Mutex
	value float64
}

func (c *fixedClock) now() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value
}

func (c *fixedClock) advance(ms float64) {
	c.mu.Lock()
	c.value += ms
	c.mu.Unlock()
}

// hourly is half an hour out at the nearest, so a real tick never lands in a test's own count.
var hourly = fmt.Sprintf("%d * * * *", (time.Now().Minute()+30)%60)

func returning(status domain.AutomationRunStatus) Dispatch {
	return func(context.Context, *domain.Automation, domain.TickKind) (domain.AutomationRunStatus, error) {
		return status, nil
	}
}

func schedulerFor(t *testing.T, store Store, dispatch Dispatch, options Options) *Scheduler {
	t.Helper()
	scheduler := NewScheduler(store, dispatch, options)
	t.Cleanup(scheduler.Stop)
	return scheduler
}

func ptr[T any](value T) *T { return &value }

func draft(taskID, prompt, schedule string) domain.AutomationDraft {
	return domain.AutomationDraft{TaskID: taskID, Prompt: prompt, Schedule: schedule}
}

func must[T any](t *testing.T) func(T, error) T {
	return func(value T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
}

func wantError(t *testing.T, err error, pattern string) {
	t.Helper()
	if err == nil || !regexp.MustCompile(pattern).MatchString(err.Error()) {
		t.Fatalf("want error matching %q, got %v", pattern, err)
	}
}

func until(t *testing.T, held func() bool, what string) {
	t.Helper()
	for attempt := 0; attempt < 600 && !held(); attempt++ {
		time.Sleep(25 * time.Millisecond)
	}
	if !held() {
		t.Fatal(what)
	}
}

func stored(id, taskID, schedule string) *domain.Automation {
	return &domain.Automation{ID: id, TaskID: taskID, Prompt: "poll", Schedule: schedule, CreatedAt: 1, UpdatedAt: 1}
}

func TestSchedulesAreRejectedBeforeTheyCanBeStored(t *testing.T) {
	wantError(t, AssertSchedule("*/30 * * * * *", nil), "at most once a minute")
	wantError(t, AssertSchedule("not a schedule", nil), "not a valid schedule")
	wantError(t, AssertSchedule("2020-01-01T00:00:00Z", nil), "no future run")
	wantError(t, AssertSchedule("0 0 30 2 *", nil), "no future run")
	wantError(t, AssertSchedule("0 8 * * *", ptr("Nowhere/Land")), "^toTZ: Invalid timezone 'Nowhere/Land'")
	if err := AssertSchedule("* * * * *", nil); err != nil {
		t.Fatal(err)
	}
	if err := AssertSchedule("0 8 * * *", ptr("America/Los_Angeles")); err != nil {
		t.Fatal(err)
	}
}

func TestATaskKeepsExactlyOneAutomation(t *testing.T) {
	store := newMemoryStore()
	clock := &fixedClock{value: 1_000}
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{Now: clock.now})
	ctx := context.Background()
	first := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "check the PR", hourly), nil))
	clock.advance(5)
	second := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "check the PR again", "0 8 * * *"), nil))
	if second.ID != first.ID || second.CreatedAt != first.CreatedAt || second.UpdatedAt != 1_005 || second.Prompt != "check the PR again" {
		t.Fatalf("second save replaces the first: %+v", second)
	}
	if len(scheduler.List()) != 1 || store.size() != 1 {
		t.Fatal("one automation per task")
	}
}

func TestAnInvalidScheduleLeavesTheStoredAutomationUntouched(t *testing.T) {
	scheduler := schedulerFor(t, newMemoryStore(), returning("succeeded"), Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", hourly), nil))
	_, err := scheduler.Update(ctx, "task-1", domain.AutomationPatch{Schedule: ptr("*/5 * * * * *")}, nil)
	wantError(t, err, "at most once a minute")
	if scheduler.ForThread("task-1").Schedule != hourly {
		t.Fatal("schedule kept")
	}
	_, err = scheduler.Update(ctx, "task-missing", domain.AutomationPatch{Paused: ptr(true)}, nil)
	wantError(t, err, "no automation")
}

func TestARunRecordsItsOutcomeAndASkipIsNotARun(t *testing.T) {
	store := newMemoryStore()
	clock := &fixedClock{value: 1_000}
	var outcome domain.AutomationRunStatus = "succeeded"
	scheduler := schedulerFor(t, store, func(context.Context, *domain.Automation, domain.TickKind) (domain.AutomationRunStatus, error) {
		return outcome, nil
	}, Options{Now: clock.now})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", hourly), nil))

	clock.advance(60)
	if status := must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1")); status != "succeeded" {
		t.Fatal(status)
	}
	view := scheduler.ForThread("task-1")
	if view.RunCount != 1 || *view.LastRunAt != 1_060 || *view.LastStatus != "succeeded" || *view.LastStatusAt != 1_060 {
		t.Fatalf("%+v", view.Automation)
	}

	outcome = "skipped"
	clock.advance(60)
	must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1"))
	view = scheduler.ForThread("task-1")
	if view.RunCount != 1 || *view.LastRunAt != 1_060 || *view.LastStatus != "skipped" || *view.LastStatusAt != 1_120 {
		t.Fatalf("a skipped tick never ran: %+v", view.Automation)
	}

	rewritten := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", "0 8 * * *"), nil))
	if *rewritten.LastRunAt != 1_060 || *rewritten.LastStatusAt != 1_120 {
		t.Fatal("rewriting the schedule keeps what the automation has done")
	}

	outcome = "failed"
	must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1"))
	if scheduler.ForThread("task-1").RunCount != 2 || *store.row(scheduler.ForThread("task-1").ID).LastStatus != "failed" {
		t.Fatal("outcomes survive a restart")
	}
}

func TestADispatchThatFailsSettlesTheRunAsFailed(t *testing.T) {
	scheduler := schedulerFor(t, newMemoryStore(), func(context.Context, *domain.Automation, domain.TickKind) (domain.AutomationRunStatus, error) {
		return "", errors.New("renderer is gone")
	}, Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", hourly), nil))
	for range 2 {
		if status := must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1")); status != "failed" {
			t.Fatal(status)
		}
	}
}

func TestASecondRunIsRefusedWhileTheFirstIsInFlight(t *testing.T) {
	releases := make(chan chan domain.AutomationRunStatus, 4)
	scheduler := schedulerFor(t, newMemoryStore(), func(context.Context, *domain.Automation, domain.TickKind) (domain.AutomationRunStatus, error) {
		release := make(chan domain.AutomationRunStatus)
		releases <- release
		return <-release, nil
	}, Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", hourly), nil))

	running := make(chan contracts.AutomationPortRuntimeRunAutomationNowResult)
	go func() { status, _ := scheduler.RunNow(ctx, "task-1"); running <- status }()
	first := <-releases
	if status, _ := scheduler.RunNow(ctx, "task-1"); status != "busy" {
		t.Fatal(status)
	}
	if len(releases) != 0 {
		t.Fatal("the refused tick never reached the renderer")
	}
	first <- "succeeded"
	if status := <-running; status != "succeeded" {
		t.Fatal(status)
	}
	go func() { status, _ := scheduler.RunNow(ctx, "task-1"); running <- status }()
	(<-releases) <- "succeeded"
	if status := <-running; status != "succeeded" {
		t.Fatal(status)
	}
}

func TestARunThatMeetsItsStopConditionIsNotResurrected(t *testing.T) {
	store := newMemoryStore()
	var scheduler *Scheduler
	scheduler = schedulerFor(t, store, func(ctx context.Context, automation *domain.Automation, _ domain.TickKind) (domain.AutomationRunStatus, error) {
		if _, err := scheduler.Remove(ctx, automation.TaskID); err != nil {
			return "", err
		}
		return "succeeded", nil
	}, Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "watch the PR until it is approved", hourly), nil))
	must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1"))
	if scheduler.ForThread("task-1") != nil || len(scheduler.List()) != 0 || store.size() != 0 {
		t.Fatal("the deleted automation is not rewritten by the run's bookkeeping")
	}
}

func soonOneShot(lead time.Duration) (string, time.Time) {
	at := time.Now().Add(lead).Truncate(time.Second)
	return at.UTC().Format("2006-01-02T15:04:05.000Z"), at
}

func TestAOneShotFiresAtItsTimeAndThenRetiresItself(t *testing.T) {
	store := newMemoryStore()
	fired := make(chan struct{}, 1)
	scheduler := schedulerFor(t, store, func(context.Context, *domain.Automation, domain.TickKind) (domain.AutomationRunStatus, error) {
		fired <- struct{}{}
		return "succeeded", nil
	}, Options{})
	when, at := soonOneShot(1100 * time.Millisecond)
	must[*domain.AutomationView](t)(scheduler.Save(context.Background(), draft("task-1", "ship the release", when), nil))
	if next := scheduler.ForThread("task-1").NextRunAt; next == nil || *next != float64(at.UnixMilli()) {
		t.Fatalf("next run %v", next)
	}
	<-fired
	until(t, func() bool { return scheduler.ForThread("task-1") == nil && store.size() == 0 }, "a spent one-shot does not linger in the panel")
}

func TestAOneShotWhoseMomentIsSkippedIsMarkedMissed(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("skipped"), Options{})
	when, _ := soonOneShot(1100 * time.Millisecond)
	must[*domain.AutomationView](t)(scheduler.Save(context.Background(), draft("task-1", "ship the release", when), nil))
	until(t, func() bool {
		view := scheduler.ForThread("task-1")
		return view != nil && view.LastStatus != nil && *view.LastStatus == "missed"
	}, "a one-shot that never ran is not thrown away")
	missed := scheduler.ForThread("task-1")
	if missed.RunCount != 0 || missed.NextRunAt != nil || store.size() != 1 {
		t.Fatalf("%+v", missed)
	}
}

func TestAOneShotMissedWhileClosedReloadsAsMissed(t *testing.T) {
	past := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	store := newMemoryStore(stored("gone", "task-past", past))
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded := scheduler.ForThread("task-past")
	if reloaded == nil || *reloaded.LastStatus != "missed" || reloaded.NextRunAt != nil {
		t.Fatalf("%+v", reloaded)
	}
	if *store.row("gone").LastStatus != "missed" {
		t.Fatal("the verdict is written down, not recomputed every launch")
	}
	if status := must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(context.Background(), "task-past")); status != "succeeded" {
		t.Fatal(status)
	}
	if scheduler.ForThread("task-past") != nil || store.size() != 0 {
		t.Fatal("re-running a missed one-shot by hand retires it")
	}
}

func TestRunningAOneShotEarlyLeavesItArmed(t *testing.T) {
	when, at := soonOneShot(time.Hour)
	scheduler := schedulerFor(t, newMemoryStore(), returning("succeeded"), Options{})
	must[*domain.AutomationView](t)(scheduler.Save(context.Background(), draft("task-1", "ship the release", when), nil))
	must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(context.Background(), "task-1"))
	view := scheduler.ForThread("task-1")
	if view.RunCount != 1 || view.NextRunAt == nil || *view.NextRunAt != float64(at.UnixMilli()) {
		t.Fatalf("%+v", view)
	}
}

func TestPausingStopsTheCountdown(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "poll", hourly), nil))
	if scheduler.ForThread("task-1").NextRunAt == nil {
		t.Fatal("armed")
	}
	paused := must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{Paused: ptr(true)}, nil))
	if !paused.Paused || paused.NextRunAt != nil || store.size() != 1 {
		t.Fatalf("%+v", paused)
	}
	if must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{Paused: ptr(false)}, nil)).NextRunAt == nil {
		t.Fatal("resumed")
	}
}

func TestStoredAutomationsAreRearmedAndBroadcast(t *testing.T) {
	record := stored("automation-1", "task-1", hourly)
	record.RunCount = 4
	var mu sync.Mutex
	var broadcasts [][]*domain.AutomationView
	scheduler := schedulerFor(t, newMemoryStore(record), returning("succeeded"), Options{OnChange: func(views []*domain.AutomationView) {
		mu.Lock()
		broadcasts = append(broadcasts, views)
		mu.Unlock()
	}})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(broadcasts) != 1 || broadcasts[0][0].RunCount != 4 || broadcasts[0][0].NextRunAt == nil {
		t.Fatal("a reloaded automation is armed again and broadcast")
	}
	must[bool](t)(scheduler.Remove(context.Background(), "task-1"))
	if last := broadcasts[len(broadcasts)-1]; len(last) != 0 {
		t.Fatal("removal is broadcast")
	}
}

func TestAStoredScheduleThisBuildCannotParseIsSkipped(t *testing.T) {
	store := newMemoryStore(stored("broken", "task-broken", "nonsense"), stored("sound", "task-sound", hourly))
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	if err := scheduler.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if scheduler.ForThread("task-broken").NextRunAt != nil || scheduler.ForThread("task-sound").NextRunAt == nil {
		t.Fatal("the broken automation is never armed, and does not take the others down with it")
	}
}

func TestAPausedAutomationStaysPausedAcrossARestart(t *testing.T) {
	store := newMemoryStore()
	first := schedulerFor(t, store, returning("succeeded"), Options{})
	paused := draft("task-1", "poll", hourly)
	paused.Paused = ptr(true)
	must[*domain.AutomationView](t)(first.Save(context.Background(), paused, nil))
	first.Stop()
	second := schedulerFor(t, store, returning("succeeded"), Options{})
	if err := second.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if view := second.ForThread("task-1"); !view.Paused || view.NextRunAt != nil {
		t.Fatalf("%+v", view)
	}
}

func TestWhatAScheduleSurfacesForSurvivesRewrites(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	quiet := draft("task-1", "poll", hourly)
	quiet.SurfaceWhen = ptr("an error is the user's own.")
	must[*domain.AutomationView](t)(scheduler.Save(ctx, quiet, nil))
	quiet.Schedule = "0 8 * * *"
	rewritten := must[*domain.AutomationView](t)(scheduler.Save(ctx, quiet, nil))
	if *rewritten.SurfaceWhen != "an error is the user's own." || *store.row(rewritten.ID).SurfaceWhen != "an error is the user's own." {
		t.Fatal("an agent changing the cadence must not silently make the schedule loud")
	}
	if *must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{Paused: ptr(true)}, nil)).SurfaceWhen != "an error is the user's own." {
		t.Fatal("pausing keeps it")
	}
	if must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{SurfaceWhen: ptr("")}, nil)).SurfaceWhen != nil {
		t.Fatal("an empty sentence is how the panel makes it loud again")
	}
	if *must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{SurfaceWhen: ptr("anything at all.")}, nil)).SurfaceWhen != "anything at all." {
		t.Fatal("set again")
	}
}

func TestWhatEndsAWatchSurvivesRewrites(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	watch := draft("task-1", "babysit", hourly)
	watch.EndsWhen = ptr("the PR is approved.")
	must[*domain.AutomationView](t)(scheduler.Save(ctx, watch, nil))
	rewritten := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "babysit", "0 8 * * *"), nil))
	if *rewritten.EndsWhen != "the PR is approved." || *store.row(rewritten.ID).EndsWhen != "the PR is approved." {
		t.Fatal("kept")
	}
	updated := must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{SurfaceWhen: ptr("CI fails.")}, nil))
	if *updated.EndsWhen != "the PR is approved." || *updated.SurfaceWhen != "CI fails." {
		t.Fatal("both kept")
	}
	routine := must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{EndsWhen: ptr("")}, nil))
	if routine.EndsWhen != nil || *routine.SurfaceWhen != "CI fails." {
		t.Fatal("dropping one sentence leaves the other")
	}
}

func TestTheButtonIsNeverAQuietTickAndNeitherIsAOneShot(t *testing.T) {
	type tick struct {
		taskID string
		kind   domain.TickKind
	}
	var mu sync.Mutex
	var ticks []tick
	when, _ := soonOneShot(1500 * time.Millisecond)
	once := stored("once", "task-2", when)
	once.SurfaceWhen = ptr("there is an error.")
	scheduler := schedulerFor(t, newMemoryStore(once), func(_ context.Context, automation *domain.Automation, kind domain.TickKind) (domain.AutomationRunStatus, error) {
		mu.Lock()
		ticks = append(ticks, tick{automation.TaskID, kind})
		mu.Unlock()
		return "succeeded", nil
	}, Options{})
	ctx := context.Background()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	quiet := draft("task-1", "poll", hourly)
	quiet.SurfaceWhen = ptr("there is an error.")
	must[*domain.AutomationView](t)(scheduler.Save(ctx, quiet, nil))
	must[contracts.AutomationPortRuntimeRunAutomationNowResult](t)(scheduler.RunNow(ctx, "task-1"))
	mu.Lock()
	if len(ticks) != 1 || ticks[0] != (tick{"task-1", domain.TickKind{}}) {
		t.Fatalf("the panel's button is only reachable with the user watching: %+v", ticks)
	}
	mu.Unlock()
	until(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(ticks) == 2 }, "the one-shot never fired")
	if ticks[1] != (tick{"task-2", domain.TickKind{Unattended: true}}) {
		t.Fatalf("a one-shot that vanishes when it runs must leave a trace of having run: %+v", ticks[1])
	}
}

func TestAOneShotRefusesASentenceItWouldNeverActOn(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	later, _ := soonOneShot(time.Hour)
	once := draft("task-1", "once", later)
	once.SurfaceWhen = ptr("there is an error.")
	_, err := scheduler.Save(ctx, once, nil)
	wantError(t, err, "one-time run always surfaces")
	if store.size() != 0 {
		t.Fatal("nothing is saved that would read as quiet and never be")
	}
	quiet := draft("task-1", "poll", hourly)
	quiet.SurfaceWhen = ptr("there is an error.")
	must[*domain.AutomationView](t)(scheduler.Save(ctx, quiet, nil))
	if must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-1", domain.AutomationPatch{Schedule: &later}, nil)).SurfaceWhen != nil {
		t.Fatal("becoming a one-shot takes the quiet off")
	}
	_, err = scheduler.Update(ctx, "task-1", domain.AutomationPatch{SurfaceWhen: ptr("there is an error.")}, nil)
	wantError(t, err, "one-time run always surfaces")
	_, err = scheduler.Save(ctx, once, nil)
	wantError(t, err, "one-time run always surfaces")
	if scheduler.ForThread("task-1").SurfaceWhen != nil {
		t.Fatal("still loud")
	}
	second := draft("task-2", "poll", hourly)
	second.SurfaceWhen = ptr("there is an error.")
	must[*domain.AutomationView](t)(scheduler.Save(ctx, second, nil))
	if must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-2", "once", later), nil)).SurfaceWhen != nil {
		t.Fatal("rewriting a quiet schedule as a one-shot drops the sentence it kept")
	}
	if must[*domain.AutomationView](t)(scheduler.Update(ctx, "task-2", domain.AutomationPatch{SurfaceWhen: ptr("")}, nil)).SurfaceWhen != nil {
		t.Fatal("taking off a quiet it has not got is no error")
	}
}

func TestATickDroppedForOverrunningIsCounted(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	view := must[*domain.AutomationView](t)(scheduler.Save(context.Background(), draft("task-1", "poll", hourly), nil))
	scheduler.mu.Lock()
	job := scheduler.crons[view.ID]
	scheduler.mu.Unlock()
	job.options.protect()
	job.options.protect()
	if err := scheduler.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := scheduler.ForThread("task-1"); *got.OverrunCount != 2 || got.RunCount != 0 {
		t.Fatalf("%+v", got)
	}
	if *store.row(view.ID).OverrunCount != 2 {
		t.Fatal("so a restart still knows how many ticks were lost")
	}
}

func TestConcurrentChangesSerializeAndPublishOnlyAfterStorage(t *testing.T) {
	store := newMemoryStore()
	held := make(chan struct{})
	writes := make(chan *domain.Automation, 4)
	store.save = func(automation *domain.Automation) error {
		writes <- automation
		<-held
		return nil
	}
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	results := make(chan *domain.AutomationView, 2)
	go func() { view, _ := scheduler.Save(ctx, draft("task-1", "Original", hourly), nil); results <- view }()
	<-writes
	go func() { view, _ := scheduler.Save(ctx, draft("task-1", "Rewritten", hourly), nil); results <- view }()
	time.Sleep(20 * time.Millisecond)
	if len(writes) != 0 || len(scheduler.List()) != 0 {
		t.Fatal("the second write waits, and nothing publishes before storage succeeds")
	}
	close(held)
	created, rewritten := <-results, <-results
	if created.ID != rewritten.ID || store.size() != 1 || scheduler.ForThread("task-1").Prompt != "Rewritten" {
		t.Fatal("serialized")
	}
}

func TestARejectedWriteLeavesThePreviousScheduleUsable(t *testing.T) {
	store := newMemoryStore()
	var fail bool
	var mu sync.Mutex
	failing := func() error {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return errors.New("disk full")
		}
		return nil
	}
	store.save = func(*domain.Automation) error { return failing() }
	store.delete = func(string) error { return failing() }
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	saved := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "Original", hourly), nil))
	mu.Lock()
	fail = true
	mu.Unlock()
	_, err := scheduler.Update(ctx, "task-1", domain.AutomationPatch{Paused: ptr(true)}, nil)
	wantError(t, err, "disk full")
	if scheduler.ForThread("task-1").Paused {
		t.Fatal("not paused")
	}
	_, err = scheduler.Remove(ctx, "task-1")
	wantError(t, err, "disk full")
	wantError(t, scheduler.Flush(), "disk full")
	if view := scheduler.ForThread("task-1"); view.ID != saved.ID || view.NextRunAt == nil {
		t.Fatal("still armed")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if !must[bool](t)(scheduler.Remove(ctx, "task-1")) {
		t.Fatal("removed")
	}
	if err := scheduler.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownDrainsAcceptedChangesWithoutRearming(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	original := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "Original", hourly), nil))
	held := make(chan struct{})
	saving := make(chan struct{}, 4)
	store.save = func(*domain.Automation) error {
		saving <- struct{}{}
		<-held
		return nil
	}
	done := make(chan error, 2)
	go func() {
		_, err := scheduler.Update(ctx, "task-1", domain.AutomationPatch{Prompt: ptr("Saved before exit")}, nil)
		done <- err
	}()
	<-saving
	pause := scheduler.enqueue(func() error {
		_, err := scheduler.updateDraft(ctx, "task-1", domain.AutomationPatch{Paused: ptr(true)}, nil)
		return err
	})
	scheduler.Stop()
	close(held)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := pause.wait(); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Flush(); err != nil {
		t.Fatal(err)
	}
	if row := store.row(original.ID); row.Prompt != "Saved before exit" || !row.Paused {
		t.Fatalf("%+v", row)
	}
	if scheduler.ForThread("task-1").NextRunAt != nil {
		t.Fatal("not rearmed")
	}
}

func TestAManualRunStaysBusyUntilItsOutcomeIsStored(t *testing.T) {
	store := newMemoryStore()
	scheduler := schedulerFor(t, store, returning("succeeded"), Options{})
	ctx := context.Background()
	must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("task-1", "Poll", hourly), nil))
	held := make(chan struct{})
	saving := make(chan struct{}, 4)
	store.save = func(*domain.Automation) error {
		saving <- struct{}{}
		<-held
		return nil
	}
	running := make(chan contracts.AutomationPortRuntimeRunAutomationNowResult)
	go func() { status, _ := scheduler.RunNow(ctx, "task-1"); running <- status }()
	<-saving
	if status, _ := scheduler.RunNow(ctx, "task-1"); status != "busy" {
		t.Fatal(status)
	}
	close(held)
	if status := <-running; status != "succeeded" || scheduler.ForThread("task-1").RunCount != 1 {
		t.Fatal(status)
	}
}

func TestAgentSchedulingCannotExceedTheRunsPolicy(t *testing.T) {
	scheduler := schedulerFor(t, newMemoryStore(), returning("succeeded"), Options{})
	ctx := context.Background()
	if err := scheduler.Start(ctx); err != nil {
		t.Fatal(err)
	}
	policies := []domain.ExecutionPolicy{"plan", "confirm", "allow-edits", "autonomous", "bypass"}
	ceilingOf := func(policy domain.ExecutionPolicy) Authority {
		return func() (domain.ExecutionPolicy, error) { return policy, nil }
	}
	for ceilingIndex, ceiling := range policies {
		for policyIndex, policy := range policies {
			request := draft(fmt.Sprintf("task-%s-%s", ceiling, policy), "poll", hourly)
			request.Policy = &policy
			view, err := scheduler.Save(ctx, request, ceilingOf(ceiling))
			if policyIndex > ceilingIndex {
				wantError(t, err, "stronger permissions")
			} else if err != nil || *view.Policy != policy {
				t.Fatalf("%s under %s: %v", policy, ceiling, err)
			}
		}
		inherited := must[*domain.AutomationView](t)(scheduler.Save(ctx, draft("inherited-"+string(ceiling), "poll", hourly), ceilingOf(ceiling)))
		if *inherited.Policy != ceiling {
			t.Fatal("inherits the ceiling")
		}
	}
	bypass := draft("user-bypass", "poll", hourly)
	bypass.Policy = ptr(domain.ExecutionPolicyBypass)
	bypass.Paused = ptr(false)
	must[*domain.AutomationView](t)(scheduler.Save(ctx, bypass, nil))
	if !must[*domain.AutomationView](t)(scheduler.Update(ctx, "user-bypass", domain.AutomationPatch{Paused: ptr(true)}, ceilingOf("confirm"))).Paused {
		t.Fatal("pausing alone is always allowed")
	}
	if *scheduler.ForThread("user-bypass").Policy != "bypass" {
		t.Fatal("policy kept")
	}
	_, err := scheduler.Update(ctx, "user-bypass", domain.AutomationPatch{Paused: ptr(false)}, ceilingOf("confirm"))
	wantError(t, err, "stronger permissions")
	if !scheduler.ForThread("user-bypass").Paused {
		t.Fatal("still paused")
	}
	plan := domain.ExecutionPolicyPlan
	if *must[*domain.AutomationView](t)(scheduler.Update(ctx, "user-bypass", domain.AutomationPatch{Paused: ptr(false), Policy: &plan}, ceilingOf("confirm"))).Policy != "plan" {
		t.Fatal("lowered")
	}
	_, err = scheduler.Update(ctx, "user-bypass", domain.AutomationPatch{Prompt: ptr("changed")}, func() (domain.ExecutionPolicy, error) {
		return "", errors.New("expired run")
	})
	wantError(t, err, "expired run")
	if scheduler.ForThread("user-bypass").Prompt != "poll" {
		t.Fatal("unchanged")
	}
}
