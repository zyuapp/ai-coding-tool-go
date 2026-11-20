package application

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// AutomationInputs are the input types ReduceAutomations takes.
var AutomationInputs = []string{
	"automation.save", "automation.update", "automation.delete", "automation.run-now", "view.edit-automation",
	"automation.fired", "automation.notify", "automation.nothing-to-report", "automations.changed",
}

// ReduceAutomations takes an input whose type is one of AutomationInputs. Its thread edits patch
// only FindingsThread fields.
func ReduceAutomations(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.AutomationCommandAutomationSave:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil || *taskID == "" {
			return SliceTransition{}
		}
		draft := input.Draft
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAutomationSave{Draft: domain.AutomationDraft{
			TaskID:      *taskID,
			Prompt:      draft.Prompt,
			Schedule:    draft.Schedule,
			Timezone:    draft.Timezone,
			Policy:      (*domain.ExecutionPolicy)(draft.Policy),
			SurfaceWhen: draft.SurfaceWhen,
			EndsWhen:    draft.EndsWhen,
			Paused:      draft.Paused,
		}}}}

	case contracts.AutomationCommandAutomationUpdate:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil || *taskID == "" {
			return SliceTransition{}
		}
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAutomationUpdate{TaskID: *taskID, Patch: input.Patch}}}

	case contracts.AutomationCommandAutomationDelete:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil || *taskID == "" {
			return SliceTransition{}
		}
		transition := withoutAutomationEdit(state, *taskID)
		transition.Effects = []WorkspaceEffect{WorkspaceEffectAutomationDelete{TaskID: *taskID}}
		return transition

	case contracts.ViewCommandViewEditAutomation:
		if domain.SameAutomationFields(input.Fields, input.Base) {
			return withoutAutomationEdit(state, input.TaskID)
		}
		edits := maps.Clone(state.AutomationEdits)
		if edits == nil {
			edits = map[string]*domain.AutomationEdit{}
		}
		edits[input.TaskID] = &domain.AutomationEdit{Base: input.Base, Fields: input.Fields}
		return SliceTransition{Patch: func(next *WorkspaceState) { next.AutomationEdits = edits }}

	case contracts.AutomationCommandAutomationRunNow:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil || *taskID == "" {
			return SliceTransition{}
		}
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAutomationRunNow{TaskID: *taskID}}}

	// The scheduler owns the cadence; the workspace decides whether this tick can actually run.
	case AutomationEventAutomationFired:
		fire := input.Fire
		thread := threadsFind(state.Threads, fire.TaskID)
		var project *domain.Project
		if thread != nil {
			project = ProjectFor(state, thread)
		}
		if refusal, refused := WhyTickCannotRun(state, fire, thread, project); refused {
			return SliceTransition(DeclinedTick(state, fire, thread, refusal))
		}
		pending := &PendingRun{
			ID:            NewID(),
			RunID:         fire.RunID,
			Origin:        PendingRunOriginAutomation,
			TaskID:        new(fire.TaskID),
			Text:          fire.Prompt,
			Prompt:        AutomationRunPrompt(fire.Prompt, fire.RunNumber, deref(fire.SurfaceWhen), deref(fire.EndsWhen)),
			MessageOrigin: domain.MessageOriginAutomation{RunNumber: fire.RunNumber},
			Attachments:   []string{},
			AutomationID:  new(fire.AutomationID),
		}
		if project != nil {
			pending.ProjectID = new(project.ID)
		}
		if fire.Policy != nil && *fire.Policy != "" {
			pending.Policy = fire.Policy
		}
		if fire.Quiet != nil && *fire.Quiet {
			pending.Quiet = new(true)
		}
		if fire.Unattended != nil && *fire.Unattended {
			pending.Unattended = new(true)
		}
		step := PendingStep(state, pending)
		step.Effects = []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)}
		return SliceTransition(step)

	case contracts.AutomationCommandAutomationNotify:
		return SliceTransition(RaisedFinding(state, input.TaskID, contracts.FindingReport{Headline: input.Headline, Detail: input.Detail, Key: input.Key}))

	case contracts.AutomationCommandAutomationNothingToReport:
		return SliceTransition(NothingToReport(state, input.TaskID, input.Checked, Now()))

	case AutomationEventAutomationsChanged:
		automations := input.Automations
		patch := func(next *WorkspaceState) { next.Automations = automations }
		ended := endedWatches(Afterward(state, SliceTransition{Patch: patch}), state.Automations)
		transition := SliceTransition(ended)
		transition.Patch = patch
		return transition
	}
	return SliceTransition{}
}

// endedWatches: a watch that is gone has finished, so its thread leaves Running for Priority the way
// a settled run does, and its coordinator hears it finished.
func endedWatches(state *WorkspaceState, before []*domain.AutomationView) Step {
	kept := jsonx.Set[string]{}
	for _, automation := range state.Automations {
		kept[automation.TaskID] = struct{}{}
	}
	ended := jsonx.Set[string]{}
	for _, automation := range before {
		if domain.IsWatching(&automation.Automation) && !kept.Has(automation.TaskID) {
			ended[automation.TaskID] = struct{}{}
		}
	}
	if len(ended) == 0 {
		return Step{}
	}
	at := Now()
	verdicts := map[string]*FinishedVerdict{}
	var finished []*domain.Thread
	for _, thread := range state.Threads {
		if !ended.Has(thread.ID) || thread.ArchivedAt != nil {
			continue
		}
		verdict := ThreadRunStateFinishedVerdict(at, ThreadOnScreen(state, thread.ID))
		verdicts[thread.ID] = &verdict
		finished = append(finished, thread)
	}
	runs := WatchesEndedBeforeIn(RunsOf(state), verdicts)
	threads := FinishedIn(ThreadsOf(state), verdicts)
	for _, thread := range finished {
		if lead := domain.CoordinatorOf(threads.View.Threads, thread); lead != nil {
			threads = NotedIn(threads, lead.ID, CoordinationNote(thread.ID, "\""+thread.Title+"\" finished watching.", at, CoordinationNoteOptions{}))
		}
	}
	return Step{Edits: []OwnerEdit{threads, runs}}
}

// RetireAutomations: an archived thread is unreachable, so its automation would tick forever with
// nowhere to run.
func RetireAutomations(state *WorkspaceState, taskIDs []string) []WorkspaceEffect {
	scheduled := jsonx.Set[string]{}
	for _, automation := range state.Automations {
		scheduled[automation.TaskID] = struct{}{}
	}
	effects := []WorkspaceEffect{}
	for _, taskID := range taskIDs {
		if scheduled.Has(taskID) {
			effects = append(effects, WorkspaceEffectAutomationDelete{TaskID: taskID})
		}
	}
	return effects
}

func Ack(pending *PendingRun, started bool) []WorkspaceEffect {
	if pending.AutomationID == nil || *pending.AutomationID == "" {
		return []WorkspaceEffect{}
	}
	return []WorkspaceEffect{WorkspaceEffectAutomationAck{Ack: contracts.AutomationAck{AutomationID: *pending.AutomationID, RunID: pending.RunID, Started: started}}}
}

func withoutAutomationEdit(state *WorkspaceState, taskID string) SliceTransition {
	if _, ok := state.AutomationEdits[taskID]; !ok {
		return SliceTransition{}
	}
	edits := maps.Clone(state.AutomationEdits)
	delete(edits, taskID)
	return SliceTransition{Patch: func(next *WorkspaceState) { next.AutomationEdits = edits }}
}
