// Package automation keeps scheduled automations: one per thread, armed on a croner-compatible
// schedule, fired through a dispatch that resolves when the run settles.
package automation

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Store is where automations persist.
type Store interface {
	ListAutomations(ctx context.Context) ([]*domain.Automation, error)
	SaveAutomation(ctx context.Context, automation *domain.Automation) error
	DeleteAutomation(ctx context.Context, id string) error
}

// Dispatch returns once the scheduled run reaches a terminal state, so overrun protection can hold
// the next tick. An error settles the run as failed.
type Dispatch func(ctx context.Context, automation *domain.Automation, tick domain.TickKind) (domain.AutomationRunStatus, error)

type Options struct {
	// Now is the clock in milliseconds; application.Now when nil.
	Now      func() float64
	OnChange func(automations []*domain.AutomationView)
}

// Authority is the run's policy ceiling, rechecked inside the mutation queue; only native user
// writes may omit it. An error refuses the write.
type Authority = func() (domain.ExecutionPolicy, error)

var policyRank = map[domain.ExecutionPolicy]int{
	domain.ExecutionPolicyPlan:       0,
	domain.ExecutionPolicyConfirm:    1,
	domain.ExecutionPolicyAllowEdits: 2,
	domain.ExecutionPolicyAutonomous: 3,
	domain.ExecutionPolicyBypass:     4,
}

func permittedPolicy(policy *domain.ExecutionPolicy, authority Authority) (*domain.ExecutionPolicy, error) {
	if authority == nil {
		return policy, nil
	}
	ceiling, err := authority()
	if err != nil {
		return nil, err
	}
	effective := ceiling
	if policy != nil {
		effective = *policy
	}
	if policyRank[effective] > policyRank[ceiling] {
		return nil, errors.New("An automation cannot use stronger permissions than this run. Change the task's permissions before scheduling it.")
	}
	return &effective, nil
}

// patchedSentence: an absent sentence keeps the stored one; an empty one drops it.
func patchedSentence(patched, stored *string) *string {
	if patched != nil {
		if *patched == "" {
			return nil
		}
		return patched
	}
	return stored
}

// quietSentence is the quiet sentence a schedule keeps. A one-time run is never quiet, as it removes
// itself once it has run, so a sentence given with one is refused rather than kept unused, and one a
// recurring schedule left behind goes with the rewrite.
func quietSentence(schedule string, given, kept *string) (*string, error) {
	if !domain.IsOneShotSchedule(schedule) {
		return kept, nil
	}
	if given != nil && *given != "" {
		return nil, errors.New("A one-time run always surfaces, so it takes no surfaceWhen. Leave surfaceWhen out, or use a cron schedule for runs that stay quiet.")
	}
	return nil, nil
}

// nonEmpty is a sentence only when it says something.
func nonEmpty(sentence *string) *string {
	if sentence == nil || *sentence == "" {
		return nil
	}
	return sentence
}

// change is one store mutation in the queue: it starts once the one before it has settled.
type change struct {
	done chan struct{}
	err  error
}

func (c *change) wait() error {
	<-c.done
	return c.err
}

type Scheduler struct {
	store    Store
	dispatch Dispatch
	options  Options
	now      func() float64

	mu          sync.Mutex
	automations map[string]*domain.Automation
	order       []string
	crons       map[string]*Cron
	// Guards manual runs the way croner's protect guards scheduled ticks: one run per automation at a time.
	firing  map[string]bool
	stopped bool

	queue sync.Mutex
	tail  *change
}

func NewScheduler(store Store, dispatch Dispatch, options Options) *Scheduler {
	now := options.Now
	if now == nil {
		now = func() float64 { return application.Now() }
	}
	return &Scheduler{
		store:       store,
		dispatch:    dispatch,
		options:     options,
		now:         now,
		automations: map[string]*domain.Automation{},
		crons:       map[string]*Cron{},
		firing:      map[string]bool{},
	}
}

// Start rebuilds the in-memory timers from storage. Ticks missed while the app was closed are not replayed.
func (s *Scheduler) Start(ctx context.Context) error {
	_ = s.Flush()
	s.mu.Lock()
	s.stopped = false
	s.mu.Unlock()
	automations, err := s.store.ListAutomations(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	clear(s.automations)
	s.order = nil
	var missed []string
	for _, automation := range automations {
		s.setLocked(automation)
		if !s.armLocked(automation) {
			missed = append(missed, automation.ID)
		}
	}
	s.mu.Unlock()
	for _, id := range missed {
		if err := s.markMissed(ctx, id); err != nil {
			return err
		}
	}
	s.notify()
	return nil
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for _, cron := range s.crons {
		cron.Stop()
	}
	clear(s.crons)
}

// Flush waits for every accepted change, answering the last one's error.
func (s *Scheduler) Flush() error {
	s.queue.Lock()
	tail := s.tail
	s.queue.Unlock()
	if tail == nil {
		return nil
	}
	return tail.wait()
}

func (s *Scheduler) List() []*domain.AutomationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Scheduler) listLocked() []*domain.AutomationView {
	views := make([]*domain.AutomationView, 0, len(s.order))
	for _, id := range s.order {
		views = append(views, s.viewLocked(s.automations[id]))
	}
	slices.SortStableFunc(views, func(left, right *domain.AutomationView) int {
		switch {
		case right.UpdatedAt > left.UpdatedAt:
			return 1
		case right.UpdatedAt < left.UpdatedAt:
			return -1
		}
		return 0
	})
	return views
}

// ForThread is the thread's automation, nil when it has none.
func (s *Scheduler) ForThread(taskID string) *domain.AutomationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if automation := s.findLocked(taskID); automation != nil {
		return s.viewLocked(automation)
	}
	return nil
}

// Save gives a thread its automation. One per thread: creating a second one replaces the first.
func (s *Scheduler) Save(ctx context.Context, draft domain.AutomationDraft, authority Authority) (*domain.AutomationView, error) {
	var view *domain.AutomationView
	err := s.change(func() error {
		var err error
		view, err = s.saveDraft(ctx, draft, authority)
		return err
	})
	return view, err
}

func (s *Scheduler) saveDraft(ctx context.Context, draft domain.AutomationDraft, authority Authority) (*domain.AutomationView, error) {
	policy, err := permittedPolicy(draft.Policy, authority)
	if err != nil {
		return nil, err
	}
	if err := AssertSchedule(draft.Schedule, draft.Timezone); err != nil {
		return nil, err
	}
	s.mu.Lock()
	existing := s.findLocked(draft.TaskID)
	s.mu.Unlock()
	at := s.now()
	kept := draft.SurfaceWhen
	if kept == nil && existing != nil {
		kept = existing.SurfaceWhen
	}
	// Rewriting a schedule leaves its quiet where it was: an empty sentence is how the quiet is taken off.
	surfaceWhen, err := quietSentence(draft.Schedule, draft.SurfaceWhen, kept)
	if err != nil {
		return nil, err
	}
	endsWhen := draft.EndsWhen
	if endsWhen == nil && existing != nil {
		endsWhen = existing.EndsWhen
	}
	automation := &domain.Automation{
		TaskID:      draft.TaskID,
		Prompt:      draft.Prompt,
		Schedule:    draft.Schedule,
		Timezone:    draft.Timezone,
		Policy:      policy,
		SurfaceWhen: nonEmpty(surfaceWhen),
		EndsWhen:    nonEmpty(endsWhen),
		Paused:      draft.Paused != nil && *draft.Paused,
		CreatedAt:   at,
		UpdatedAt:   at,
	}
	if existing != nil {
		automation.ID = existing.ID
		automation.CreatedAt = existing.CreatedAt
		automation.RunCount = existing.RunCount
		automation.LastRunAt = existing.LastRunAt
		automation.LastStatus = existing.LastStatus
		automation.LastStatusAt = existing.LastStatusAt
		automation.ConsecutiveDeclines = existing.ConsecutiveDeclines
		automation.OverrunCount = existing.OverrunCount
	} else {
		automation.ID = application.NewID()
	}
	if err := s.commit(ctx, automation); err != nil {
		return nil, err
	}
	return s.view(automation), nil
}

func (s *Scheduler) Update(ctx context.Context, taskID string, patch domain.AutomationPatch, authority Authority) (*domain.AutomationView, error) {
	var view *domain.AutomationView
	err := s.change(func() error {
		var err error
		view, err = s.updateDraft(ctx, taskID, patch, authority)
		return err
	})
	return view, err
}

func onlyPauses(patch domain.AutomationPatch) bool {
	return patch.Paused != nil && *patch.Paused && patch == domain.AutomationPatch{Paused: patch.Paused}
}

func (s *Scheduler) updateDraft(ctx context.Context, taskID string, patch domain.AutomationPatch, authority Authority) (*domain.AutomationView, error) {
	s.mu.Lock()
	existing := s.findLocked(taskID)
	s.mu.Unlock()
	if existing == nil {
		return nil, errors.New("This task has no automation.")
	}
	pausingOnly := onlyPauses(patch)
	policy := existing.Policy
	if pausingOnly {
		if authority != nil {
			if _, err := authority(); err != nil {
				return nil, err
			}
		}
	} else {
		wanted := patch.Policy
		if wanted == nil {
			wanted = existing.Policy
		}
		var err error
		if policy, err = permittedPolicy(wanted, authority); err != nil {
			return nil, err
		}
	}
	schedule := existing.Schedule
	if patch.Schedule != nil {
		schedule = *patch.Schedule
	}
	timezone := existing.Timezone
	if patch.Timezone != nil {
		timezone = patch.Timezone
	}
	if patch.Schedule != nil || patch.Timezone != nil {
		if err := AssertSchedule(schedule, timezone); err != nil {
			return nil, err
		}
	}
	surfaceWhen, err := quietSentence(schedule, patch.SurfaceWhen, patchedSentence(patch.SurfaceWhen, existing.SurfaceWhen))
	if err != nil {
		return nil, err
	}
	next := *existing
	next.SurfaceWhen = nonEmpty(surfaceWhen)
	next.EndsWhen = nonEmpty(patchedSentence(patch.EndsWhen, existing.EndsWhen))
	if patch.Prompt != nil {
		next.Prompt = *patch.Prompt
	}
	next.Schedule = schedule
	next.Timezone = timezone
	if policy != nil {
		next.Policy = policy
	}
	if patch.Paused != nil {
		next.Paused = *patch.Paused
	}
	next.UpdatedAt = s.now()
	if err := s.commit(ctx, &next); err != nil {
		return nil, err
	}
	return s.view(&next), nil
}

// Remove deletes the thread's automation, false when it had none.
func (s *Scheduler) Remove(ctx context.Context, taskID string) (bool, error) {
	var removed bool
	err := s.change(func() error {
		var err error
		removed, err = s.removeStored(ctx, taskID)
		return err
	})
	return removed, err
}

func (s *Scheduler) removeStored(ctx context.Context, taskID string) (bool, error) {
	s.mu.Lock()
	existing := s.findLocked(taskID)
	s.mu.Unlock()
	if existing == nil {
		return false, nil
	}
	if err := s.store.DeleteAutomation(ctx, existing.ID); err != nil {
		return false, err
	}
	s.mu.Lock()
	s.disarmLocked(existing.ID)
	s.deleteLocked(existing.ID)
	s.mu.Unlock()
	s.notify()
	return true, nil
}

// RunNow fires outside the schedule. It declines while a scheduled run is still in flight, like a
// real tick would. The button is the only way here, so the user is watching by construction and it is loud.
func (s *Scheduler) RunNow(ctx context.Context, taskID string) (contracts.AutomationPortRuntimeRunAutomationNowResult, error) {
	s.mu.Lock()
	existing := s.findLocked(taskID)
	if existing == nil {
		s.mu.Unlock()
		return "", errors.New("This task has no automation.")
	}
	cron := s.crons[existing.ID]
	if s.firing[existing.ID] || cron != nil && cron.IsBusy() {
		s.mu.Unlock()
		return contracts.AutomationPortRuntimeRunAutomationNowResultBusy, nil
	}
	s.mu.Unlock()
	status, err := s.fire(context.WithoutCancel(ctx), existing.ID, true)
	return contracts.AutomationPortRuntimeRunAutomationNowResult(status), err
}

func (s *Scheduler) findLocked(taskID string) *domain.Automation {
	for _, id := range s.order {
		if automation := s.automations[id]; automation.TaskID == taskID {
			return automation
		}
	}
	return nil
}

func (s *Scheduler) setLocked(automation *domain.Automation) {
	if _, ok := s.automations[automation.ID]; !ok {
		s.order = append(slices.Clip(s.order), automation.ID)
	}
	s.automations[automation.ID] = automation
}

func (s *Scheduler) deleteLocked(id string) {
	delete(s.automations, id)
	s.order = slices.DeleteFunc(slices.Clone(s.order), func(held string) bool { return held == id })
}

func (s *Scheduler) commit(ctx context.Context, automation *domain.Automation) error {
	if err := s.store.SaveAutomation(ctx, automation); err != nil {
		return err
	}
	s.mu.Lock()
	s.setLocked(automation)
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.armLocked(automation)
	s.mu.Unlock()
	s.notify()
	return nil
}

// armLocked reports false when the schedule cannot fire again, which must never fail start.
func (s *Scheduler) armLocked(automation *domain.Automation) bool {
	s.disarmLocked(automation.ID)
	id := automation.ID
	timezone := ""
	if automation.Timezone != nil {
		timezone = *automation.Timezone
	}
	cron, err := newCron(automation.Schedule, cronOptions{
		timezone: timezone,
		paused:   automation.Paused,
		// A tick dropped for overrunning is recorded nowhere else: the timer never calls the run.
		protect: func() {
			pending := s.enqueue(func() error { return s.countOverrun(id) })
			go func() {
				if err := pending.wait(); err != nil {
					log.Printf("Could not record automation overrun: %v", err)
				}
			}()
		},
		catch: func(err error) { log.Printf("Could not record automation result: %v", err) },
	}, func() error {
		_, err := s.fire(context.Background(), id, false)
		return err
	})
	if err != nil {
		return false
	}
	if _, ok := cron.NextRun(); !ok {
		cron.Stop()
		return false
	}
	s.crons[id] = cron
	return true
}

func (s *Scheduler) disarmLocked(id string) {
	if cron := s.crons[id]; cron != nil {
		cron.Stop()
	}
	delete(s.crons, id)
}

func (s *Scheduler) fire(ctx context.Context, id string, manual bool) (domain.AutomationRunStatus, error) {
	s.mu.Lock()
	automation := s.automations[id]
	if automation == nil || s.stopped || s.firing[id] {
		s.mu.Unlock()
		return domain.AutomationRunStatusSkipped, nil
	}
	s.firing[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.firing, id)
		s.mu.Unlock()
	}()
	status, err := s.dispatch(ctx, automation, domain.TickKind{Quiet: domain.QuietTick(automation, manual), Unattended: !manual})
	if err != nil {
		status = domain.AutomationRunStatusFailed
	}
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return status, nil
	}
	err = s.change(func() error {
		s.mu.Lock()
		current := s.automations[id]
		s.mu.Unlock()
		// The run itself may have deleted the automation once its stop condition was met.
		if current == nil {
			return nil
		}
		updated := domain.AutomationAfterRun(current, status, s.now())
		if err := s.store.SaveAutomation(ctx, updated); err != nil {
			return err
		}
		s.mu.Lock()
		s.setLocked(updated)
		if s.stopped {
			s.mu.Unlock()
			return nil
		}
		again := s.canFireAgainLocked(id)
		s.mu.Unlock()
		// A spent schedule is only finished if this tick actually ran; otherwise the moment was missed.
		switch {
		case again:
			s.notify()
		case domain.DidRun(status):
			_, err := s.removeStored(ctx, updated.TaskID)
			return err
		default:
			return s.markMissed(ctx, id)
		}
		return nil
	})
	return status, err
}

func (s *Scheduler) countOverrun(id string) error {
	s.mu.Lock()
	automation := s.automations[id]
	s.mu.Unlock()
	if automation == nil {
		return nil
	}
	counted := *automation
	overruns := 1.0
	if automation.OverrunCount != nil {
		overruns = *automation.OverrunCount + 1
	}
	counted.OverrunCount = &overruns
	counted.UpdatedAt = s.now()
	if err := s.store.SaveAutomation(context.Background(), &counted); err != nil {
		return err
	}
	s.mu.Lock()
	s.setLocked(&counted)
	stopped := s.stopped
	s.mu.Unlock()
	if !stopped {
		s.notify()
	}
	return nil
}

func (s *Scheduler) canFireAgainLocked(id string) bool {
	cron := s.crons[id]
	if cron == nil {
		return false
	}
	_, ok := cron.NextRun()
	return ok
}

// markMissed keeps a one-shot that can no longer fire, plainly marked, so it never disappears unrun.
func (s *Scheduler) markMissed(ctx context.Context, id string) error {
	s.mu.Lock()
	automation := s.automations[id]
	s.mu.Unlock()
	if automation == nil || automation.LastStatus != nil && *automation.LastStatus == domain.AutomationRunStatusMissed {
		return nil
	}
	missed := domain.AutomationAfterRun(automation, domain.AutomationRunStatusMissed, s.now())
	if err := s.store.SaveAutomation(ctx, missed); err != nil {
		return err
	}
	s.mu.Lock()
	s.setLocked(missed)
	stopped := s.stopped
	s.mu.Unlock()
	if !stopped {
		s.notify()
	}
	return nil
}

func (s *Scheduler) view(automation *domain.Automation) *domain.AutomationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewLocked(automation)
}

func (s *Scheduler) viewLocked(automation *domain.Automation) *domain.AutomationView {
	view := &domain.AutomationView{Automation: *automation}
	if !automation.Paused {
		if cron := s.crons[automation.ID]; cron != nil {
			if next, ok := cron.NextRun(); ok {
				at := float64(next.UnixMilli())
				view.NextRunAt = &at
			}
		}
	}
	return view
}

func (s *Scheduler) notify() {
	if s.options.OnChange != nil {
		s.options.OnChange(s.List())
	}
}

// enqueue starts operation once every change accepted before it has settled. Store mutations
// settle in order so concurrent requests cannot replace each other's updates.
func (s *Scheduler) enqueue(operation func() error) *change {
	s.queue.Lock()
	previous := s.tail
	next := &change{done: make(chan struct{})}
	s.tail = next
	s.queue.Unlock()
	go func() {
		if previous != nil {
			<-previous.done
		}
		next.err = operation()
		close(next.done)
	}()
	return next
}

func (s *Scheduler) change(operation func() error) error {
	return s.enqueue(operation).wait()
}

// AssertSchedule rejects anything croner cannot parse, sub-minute cadence, and one-shots that can never fire.
func AssertSchedule(schedule string, timezone *string) error {
	if !domain.IsOneShotSchedule(schedule) && domain.ScheduleFieldCount(schedule) > 5 {
		return errors.New("Automations run at most once a minute, so seconds are not supported. Use a five-field cron expression.")
	}
	zone := ""
	if timezone != nil {
		zone = *timezone
	}
	cron, err := newCron(schedule, cronOptions{timezone: zone, paused: true}, nil)
	var badZone errTimezone
	if errors.As(err, &badZone) && strings.Index(schedule, ":") <= 0 {
		return err
	}
	if err != nil {
		return errors.New(`"` + schedule + `" is not a valid schedule: ` + err.Error())
	}
	defer cron.Stop()
	if _, ok := cron.NextRun(); !ok {
		return errors.New(`"` + schedule + `" has no future run.`)
	}
	return nil
}
