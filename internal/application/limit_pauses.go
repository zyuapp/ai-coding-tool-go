package application

import (
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// HeldByLimit reports whether a message to this thread waits with it. One that resumes on its own
// waits for its turn in line even after the limit lifts; a weekly one is taken up by the user's own
// send once it has.
func HeldByLimit(state *WorkspaceState, taskID string, at float64) bool {
	thread := runThreadOf(state.Threads, taskID)
	if thread == nil || thread.LimitPause == nil {
		return false
	}
	return domain.ResumesOnItsOwn(thread.LimitPause.Window) || at < thread.LimitPause.ResetsAt
}

// ResumeThread takes a thread out of line and starts it: with the first message written to it while
// it waited, or with a prompt to carry on. Either run warms up before the next thread in line goes.
func ResumeThread(state *WorkspaceState, taskID string) WorkspaceTransition {
	return Applied(state, SliceTransition(Resumed(state, taskID)))
}

// Resumed is ResumeThread as the edits it makes.
func Resumed(state *WorkspaceState, taskID string) Step {
	thread := runThreadOf(state.Threads, taskID)
	if thread == nil || thread.LimitPause == nil || ThreadBusy(state, taskID) {
		return Step{}
	}
	pause := thread.LimitPause
	lifted := UnpausedIn(ThreadsOf(state), taskID)
	if queued := QueuedFor(state, taskID); len(queued) > 0 {
		written := *queued[0]
		written.Prompt = domain.ResumePrompt(pause, &written.Prompt)
		requeued := append([]*QueuedMessage{&written}, queued[1:]...)
		first := Step{Edits: []OwnerEdit{lifted, QueuedIn(RunsOf(state), taskID, requeued)}}
		return AndThen(state, first, func(next *WorkspaceState) Step { return Drained(next, taskID, QueueIntentResume) })
	}
	project := ProjectFor(state, thread)
	pending := &PendingRun{
		ID:          NewID(),
		RunID:       NewID(),
		Origin:      PendingRunOriginComposer,
		Operation:   PendingRunOperationResume{After: PendingRunOperationResumeAfterLimit},
		TaskID:      new(taskID),
		Text:        "",
		Prompt:      domain.ResumePrompt(pause, nil),
		Attachments: []string{},
		Warming:     new(true),
	}
	if project != nil {
		pending.ProjectID = new(project.ID)
	}
	return Step{
		Edits:   []OwnerEdit{lifted, PendingIn(RunsOf(state), pending)},
		Effects: []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)},
	}
}

// PauseCancelled takes a thread out of line, handing anything written to it while it waited back to
// the composer. One that would have carried on by itself has its turn end here instead, as a run the
// user stops does.
func PauseCancelled(state *WorkspaceState, taskID string) Step {
	thread := runThreadOf(state.Threads, taskID)
	if thread == nil || thread.LimitPause == nil {
		return Step{}
	}
	lifted := Step{Edits: []OwnerEdit{UnpausedIn(ThreadsOf(state), taskID)}}
	if !domain.ResumesOnItsOwn(thread.LimitPause.Window) {
		return AndThen(state, lifted, func(next *WorkspaceState) Step { return Drained(next, taskID, QueueIntentHandBack) })
	}
	return AndThen(state, lifted, func(next *WorkspaceState) Step {
		return AndThen(next, TurnEnded(next, taskID, TurnStatusCancelled), func(ended *WorkspaceState) Step {
			return TurnSettled(ended, taskID, SettleTurn(TurnEnd{Status: TurnStatusCancelled, Stop: new(StopIntentUser)}), nil, Now())
		})
	})
}

// warming reports whether an engine's line has a thread still warming up: the next waits until the one before has answered once.
func warming(state *WorkspaceState, engine domain.AgentEngine) bool {
	engineOf := func(taskID *string) (domain.AgentEngine, bool) {
		if taskID == nil {
			return "", false
		}
		if thread := runThreadOf(state.Threads, *taskID); thread != nil {
			return thread.Engine, true
		}
		return "", false
	}
	for _, run := range state.ActiveRuns {
		record := RunRecord(run)
		if !runsIsTrue(record.Warming) {
			continue
		}
		if held, ok := engineOf(&record.TaskID); ok && held == engine {
			return true
		}
	}
	for _, pending := range state.PendingRuns {
		if !runsIsTrue(pending.Warming) {
			continue
		}
		if held, ok := engineOf(pending.TaskID); ok && held == engine {
			return true
		}
	}
	return false
}

// advanceLines starts the first thread in each engine's line whose limit has lifted, unless one is still warming up.
func advanceLines(state *WorkspaceState, at float64) WorkspaceTransition {
	transition := Settled(state, nil, nil)
	leaving := LeavingThreadIds(state)
	for _, line := range domain.LimitLines(state.Threads) {
		if warming(&transition.State, line.Engine) {
			continue
		}
		index := slices.IndexFunc(line.Threads, func(thread *domain.Thread) bool {
			return thread.LimitPause.ResetsAt <= at && !leaving.Has(thread.ID) && !slices.Contains(transition.State.CreatingWorktrees, thread.ID)
		})
		if index == -1 {
			continue
		}
		resumed := ResumeThread(&transition.State, line.Threads[index].ID)
		resumed.Effects = slices.Concat(transition.Effects, resumed.Effects)
		transition = resumed
	}
	return transition
}

// nextReset is the next moment after `after` a thread in line has its limit lift, nil for none.
func nextReset(state *WorkspaceState, after float64) *float64 {
	var next *float64
	for _, line := range domain.LimitLines(state.Threads) {
		for _, thread := range line.Threads {
			if resetsAt := thread.LimitPause.ResetsAt; resetsAt > after && (next == nil || resetsAt < *next) {
				next = new(resetsAt)
			}
		}
	}
	return next
}

// mayAdvance reports whether a thread in line may be free to go after input: time passing, or a run warming up or ending.
func mayAdvance(input WorkspaceInput) bool {
	switch input := input.(type) {
	case WorkspaceEventRunEvent:
		switch input.Event.(type) {
		case contracts.RunEventContextUsage, contracts.RunEventRunStatus:
			return true
		}
		return false
	case WorkspaceEventLimitsElapsed, WorkspaceEventStoreLoaded, WorkspaceEventStoreAbsent, contracts.TaskCommandTaskSend,
		WorkspaceEventRunUnresolved, contracts.RunControlCommandLimitCancel:
		return true
	case contracts.ViewCommandViewSetFocused:
		return input.Focused
	}
	return false
}

// restoreHeld hands each paused thread back what was written to it while it waited, after a restart.
func restoreHeld(state *WorkspaceState) *WorkspaceState {
	queuedMessages := state.QueuedMessages
	for _, thread := range state.Threads {
		if thread.LimitPause == nil || len(thread.LimitPause.Held) == 0 || len(queuedMessages[thread.ID]) > 0 {
			continue
		}
		held := make([]*QueuedMessage, len(thread.LimitPause.Held))
		for index, message := range thread.LimitPause.Held {
			held[index] = &QueuedMessage{HeldMessage: *message}
		}
		queuedMessages = runsWithKey(queuedMessages, thread.ID, held)
	}
	if jsonx.SameMap(queuedMessages, state.QueuedMessages) {
		return state
	}
	next := *state
	next.QueuedMessages = queuedMessages
	return &next
}

func sameMessages(queued []*QueuedMessage, held []*domain.HeldMessage) bool {
	if len(queued) != len(held) {
		return false
	}
	for index, message := range queued {
		if message.ID != held[index].ID {
			return false
		}
	}
	return true
}

// recordHeld writes down what each paused thread holds, so the store keeps it with the pause.
func recordHeld(state *WorkspaceState) *WorkspaceState {
	stale := func(thread *domain.Thread) bool {
		return thread.LimitPause != nil && !sameMessages(state.QueuedMessages[thread.ID], thread.LimitPause.Held)
	}
	if !slices.ContainsFunc(state.Threads, stale) {
		return state
	}
	threads := make([]*domain.Thread, len(state.Threads))
	for index, thread := range state.Threads {
		if !stale(thread) {
			threads[index] = thread
			continue
		}
		pause := *thread.LimitPause
		pause.Held = nil
		if queued := state.QueuedMessages[thread.ID]; len(queued) > 0 {
			pause.Held = make([]*domain.HeldMessage, len(queued))
			for position, message := range queued {
				pause.Held[position] = new(message.HeldMessage)
			}
		}
		recorded := *thread
		recorded.LimitPause = &pause
		threads[index] = &recorded
	}
	next := *state
	next.Threads = threads
	return &next
}

// ReconcileLimitPauses resumes threads whose limit lifted and keeps the one timer watching for the next reset.
func ReconcileLimitPauses(previous *WorkspaceState, transition WorkspaceTransition, input WorkspaceInput) WorkspaceTransition {
	check := mayAdvance(input)
	if !check && jsonx.SameSlice(previous.Threads, transition.State.Threads) && jsonx.SameMap(previous.QueuedMessages, transition.State.QueuedMessages) {
		return transition
	}
	at := Now()
	if elapsed, ok := input.(WorkspaceEventLimitsElapsed); ok {
		at = elapsed.At
	}
	loaded := &transition.State
	if _, ok := input.(WorkspaceEventStoreLoaded); ok {
		loaded = restoreHeld(loaded)
	}
	var advanced WorkspaceTransition
	if check {
		advanced = advanceLines(loaded, at)
	} else {
		advanced = Settled(loaded, nil, nil)
	}
	state := recordHeld(&advanced.State)
	before := nextReset(previous, at)
	next := nextReset(state, at)
	effects := slices.Concat(transition.Effects, advanced.Effects)
	if effects == nil {
		effects = []WorkspaceEffect{}
	}
	sameReset := before == nil && next == nil || before != nil && next != nil && *before == *next
	if !(sameReset && !(check && next != nil)) {
		effects = append(effects, WorkspaceEffectScheduleLimitReset{At: next})
	}
	return WorkspaceTransition{State: *state, Effects: effects, Result: transition.Result}
}
