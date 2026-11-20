package application

import (
	"regexp"
	"slices"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// RunInputs are the input types ReduceRuns takes.
var RunInputs = []string{"run.resolved", "run.event"}

// RunControlInputs are what the user asks of a live run, and what a session reports from beside it,
// which change only the runs' own state: the input types ReduceRunControl takes.
var RunControlInputs = []string{"resource.resume", "run.compact", "run.cancel", "run.stop-process", "run.decide", "thread.event", "review.open", "review.close", "review.set-step", "review.start"}

// ReduceRunControl takes an input whose type is one of RunControlInputs.
func ReduceRunControl(state *WorkspaceState, input WorkspaceInput) Step {
	switch input := input.(type) {
	case contracts.RunControlCommandResourceResume:
		return ResourceResumed(state, input.TaskID)
	case contracts.ReviewCommandReviewOpen, contracts.ReviewCommandReviewClose, contracts.ReviewCommandReviewSetStep, contracts.ReviewCommandReviewStart:
		return reduceReview(state, input)

	case contracts.RunControlCommandRunCompact:
		taskID := TargetID(state, input.TaskID)
		var thread *domain.Thread
		if taskID != nil {
			thread = runThreadOf(state.Threads, *taskID)
		}
		if thread == nil || !CompactableSession(state, thread) || ThreadBusy(state, thread.ID) {
			return Step{}
		}
		if slices.Contains(state.CreatingWorktrees, thread.ID) {
			return Step{Refusal: new(WorktreeCreatingError)}
		}
		if LeavingThreadIds(state).Has(thread.ID) {
			return Step{Refusal: new(WorktreeReleasingError)}
		}
		project := ProjectFor(state, thread)
		pending := &PendingRun{
			ID:          NewID(),
			RunID:       NewID(),
			Origin:      PendingRunOriginComposer,
			Operation:   PendingRunOperationCompact{},
			TaskID:      new(thread.ID),
			Text:        "",
			Prompt:      "",
			Attachments: []string{},
		}
		if project != nil {
			pending.ProjectID = new(project.ID)
		}
		resolving := ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)
		step := PendingStep(state, pending)
		step.Effects = []WorkspaceEffect{resolving}
		return step

	case contracts.RunControlCommandRunCancel:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil {
			return Step{}
		}
		if active := state.ActiveRuns[*taskID]; active != nil {
			return Cancelling(state, active)
		}
		return Step{}

	case contracts.RunControlCommandRunStopProcess:
		taskID := TargetID(state, input.TaskID)
		if taskID == nil {
			return Step{}
		}
		return ProcessStopped(state, *taskID, input.ProcessID)

	case contracts.RunControlCommandRunDecide:
		active := state.ActiveRuns[input.TaskID]
		record := RunRecord(active)
		if record == nil {
			return Step{}
		}
		approval := state.Approvals[record.RunID]
		if approval == nil || record.RunID != input.RunID || approval.ApprovalID != input.ApprovalID {
			return Step{}
		}
		return AndThen(state, Step{Edits: []OwnerEdit{ApprovalAnsweredIn(RunsOf(state), record.TaskID)}}, func(next *WorkspaceState) Step {
			step := RunQueueAttendedRun(next, record.TaskID)
			step.Effects = []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.ApprovalDecisionCommand{TaskID: record.TaskID, RunID: record.RunID, ApprovalID: approval.ApprovalID, Allow: input.Allow}}}
			return step
		})

	case WorkspaceEventThreadEvent:
		if stopped, ok := input.Event.(contracts.ResourceStoppedEvent); ok {
			return ResourceStopped(state, stopped)
		}
		if runThreadOf(state.Threads, ThreadEventTaskID(input.Event)) == nil {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{ThreadEventIn(RunsOf(state), input.Event)}}
	}
	return Step{}
}

// ReduceRunUnresolved drops a run whose workspace could not be resolved: its message goes back where
// it came from, and the failure says so where it belongs.
func ReduceRunUnresolved(state *WorkspaceState, input WorkspaceEventRunUnresolved) Step {
	pending := state.PendingRuns[input.PendingID]
	if pending == nil {
		return Step{}
	}
	dropped := Unsent(state, pending)
	if pending.Origin == PendingRunOriginAutomation {
		dropped.Effects = Ack(pending, false)
		return dropped
	}
	failed := contracts.WorkspaceCommandResultFalse{Message: input.Message}
	if coordination := pending.Coordination; coordination != nil && runsTruthy(coordination.ProposalID) && runsTruthy(coordination.CoordinatorID) {
		leadID, proposalID := *coordination.CoordinatorID, *coordination.ProposalID
		step := AndThen(state, dropped, func(next *WorkspaceState) Step { return ProposalFailure(next, leadID, proposalID, new(input.Message)) })
		step.Result = failed
		return step
	}
	if chatID := pending.TaskID; chatID != nil && *chatID != "" && slices.ContainsFunc(state.SideChats, func(chat *SideChat) bool { return chat.ID == *chatID }) {
		step := AndThen(state, dropped, func(next *WorkspaceState) Step {
			return Step{Edits: []OwnerEdit{ChatFailedIn(SideChatsOf(next), *chatID, input.Message)}}
		})
		step.Result = failed
		return step
	}
	dropped.Refusal = new(input.Message)
	return dropped
}

// ReduceRuns takes a run starting in the workspace it resolved to (WorkspaceEventRunResolved), and
// what it reports (WorkspaceEventRunEvent).
func ReduceRuns(state *WorkspaceState, input WorkspaceInput) WorkspaceTransition {
	switch input := input.(type) {
	case WorkspaceEventRunResolved:
		return runResolved(state, input)
	case WorkspaceEventRunEvent:
		return runReported(state, input.Event)
	}
	return Settled(state, nil, nil)
}

// turnEnding is how a run's turn ends, if this event ends it; nil otherwise.
func turnEnding(state *WorkspaceState, active ActiveRun, event contracts.RunEvent) *Settlement {
	status, ok := event.(contracts.RunEventRunStatus)
	if !ok || status.Status == domain.RunStatusRunning || status.Status == domain.RunStatusAwaitingApproval {
		return nil
	}
	settlement := SettleTurn(TurnEnd{
		Status:  TurnStatus(status.Status),
		Message: status.Message,
		Limit:   status.Limit,
		Run:     turnEndRunOf(active),
		Stop:    StopIntentOf(state, active),
	})
	return &settlement
}

// runNotice is what this event puts on the desktop, and why; false for nothing.
func runNotice(thread *domain.Thread, active ActiveRun, event contracts.RunEvent, surfacing *RunSurfacing) (headline string, reason NoticeReason, ok bool) {
	record := RunRecord(active)
	if record.Origin == ActiveRunRunningOriginComposer {
		switch event := event.(type) {
		case contracts.RunEventApprovalRequested:
			return WaitingHeadline(MemberFactWaitingOn{ToolName: new(event.Intent.Name)}), NoticeReasonWaiting, true
		case contracts.RunEventQuestionRequested:
			var on MemberFactWaitingOn
			if len(event.Request.Questions) > 0 {
				on.Question = new(event.Request.Questions[0].Question)
			}
			return WaitingHeadline(on), NoticeReasonWaiting, true
		}
	}
	var message *string
	if status, ok := event.(contracts.RunEventRunStatus); ok {
		message = status.Message
	}
	headline, ok = SettledHeadline(surfacing, message)
	if !ok {
		return "", "", false
	}
	if *surfacing == RunSurfacingFailed {
		return headline, NoticeReasonFailed, true
	}
	if domain.IsCoordinator(thread) {
		if posted, ok := PostedHeadline(thread, int(record.MessagesBefore)); ok && posted != "" {
			return posted, NoticeReasonFinished, true
		}
	}
	return headline, NoticeReasonFinished, true
}

// reduceReview takes the native review picker's inputs, and starts the review run.
func reduceReview(state *WorkspaceState, input WorkspaceInput) Step {
	if _, closing := input.(contracts.ReviewCommandReviewClose); closing {
		if state.ReviewPicker == nil {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{ReviewPickerIn(DialogsOf(state), nil)}}
	}
	if setStep, ok := input.(contracts.ReviewCommandReviewSetStep); ok {
		picker := state.ReviewPicker
		if picker == nil || state.CurrentID == nil || picker.TaskID != *state.CurrentID || reviewableThread(state, &picker.TaskID) == nil {
			return Step{}
		}
		stepped := *picker
		stepped.Step = ReviewPickerStep(setStep.Step)
		return Step{Edits: []OwnerEdit{ReviewPickerIn(DialogsOf(state), &stepped)}}
	}
	var asked *string
	switch input := input.(type) {
	case contracts.ReviewCommandReviewOpen:
		asked = input.TaskID
	case contracts.ReviewCommandReviewStart:
		asked = input.TaskID
	}
	thread := reviewableThread(state, TargetID(state, asked))
	if thread == nil {
		return Step{}
	}
	start, starting := input.(contracts.ReviewCommandReviewStart)
	if !starting {
		return Step{Edits: []OwnerEdit{ReviewPickerIn(DialogsOf(state), &ReviewPicker{TaskID: thread.ID, Step: "targets"})}, ClearsError: true}
	}
	if start.Target == nil || !domain.IsReviewTarget(start.Target) {
		return Step{}
	}
	project := ProjectFor(state, thread)
	pending := &PendingRun{
		ID:          NewID(),
		RunID:       NewID(),
		Origin:      PendingRunOriginComposer,
		Operation:   contracts.RunOperationReview{Target: start.Target},
		TaskID:      new(thread.ID),
		Text:        "",
		Prompt:      "",
		Attachments: []string{},
	}
	if project != nil {
		pending.ProjectID = new(project.ID)
	}
	resolving := ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)
	started := PendingStep(state, pending)
	started.Edits = append([]OwnerEdit{ReviewPickerIn(DialogsOf(state), nil)}, started.Edits...)
	started.Effects = []WorkspaceEffect{resolving}
	return started
}

// turnPullRequestRead is the read a thread under a coordinator asks for as its turn ends, if its work
// is or has been on a pull request. False for none.
func turnPullRequestRead(state *WorkspaceState, taskID string) (next *WorkspaceState, effect WorkspaceEffect, ok bool) {
	thread := runThreadOf(state.Threads, taskID)
	if thread == nil || !domain.WorksUnderCoordinator(state.Threads, thread) {
		return nil, nil, false
	}
	if thread.Brief != nil && thread.Brief.Delivers == "pull-request" || thread.PullRequest != nil {
		return MemberPullRequestRead(state, thread)
	}
	return nil, nil, false
}

// modelGone refuses a run whose thread's model its engine no longer offers; nil when it still does.
func modelGone(state *WorkspaceState, pending *PendingRun) *WorkspaceTransition {
	if pending.TaskID == nil || *pending.TaskID == "" {
		return nil
	}
	thread := runThreadOf(state.Threads, *pending.TaskID)
	if thread == nil {
		return nil
	}
	catalog := CatalogOf(state)
	model, ok := domain.ThreadModel(&catalog, thread)
	if ok && EngineOffersModel(state, thread.Engine, model) {
		return nil
	}
	var refused WorkspaceTransition
	switch {
	case pending.Origin == PendingRunOriginAutomation:
		refused = Settled(state, Ack(pending, false), nil)
	case !ok:
		refused = Rejected(WithUnsent(state, pending), domain.NoModel(thread.Engine), nil)
	default:
		refused = Rejected(WithUnsent(state, pending), domain.UnknownModel(model, ""), nil)
	}
	return &refused
}

func startCompaction(state *WorkspaceState, pending *PendingRun, workspace domain.WorkspaceRecord) WorkspaceTransition {
	var thread *domain.Thread
	if pending.TaskID != nil {
		thread = runThreadOf(state.Threads, *pending.TaskID)
	}
	if thread == nil || thread.ContextUsage == nil || !CompactableSession(state, thread) || state.ActiveRuns[thread.ID] != nil {
		return Settled(state, nil, nil)
	}
	command := StartRunCommand(state, thread, pending.RunID, "", workspace.ID, thread.ExecutionPolicy)
	command.Operation = contracts.RunOperationCompact{PreTokens: thread.ContextUsage.Tokens}
	provenance := ThreadRunStateAttendedRun
	provenance.Operation = new(RunProvenanceOperationCompact)
	return Settled(BeginRun(state, thread.ID, pending.RunID, provenance, nil, ""), []WorkspaceEffect{WorkspaceEffectStartRun{Command: command}}, nil)
}

func reviewableThread(state *WorkspaceState, taskID *string) *domain.Thread {
	if taskID == nil {
		return nil
	}
	thread := runThreadOf(state.Threads, *taskID)
	if thread != nil && ReviewableSession(state, thread) && !ThreadBusy(state, thread.ID) {
		return thread
	}
	return nil
}

func startReview(state *WorkspaceState, pending *PendingRun, workspace domain.WorkspaceRecord) WorkspaceTransition {
	var thread *domain.Thread
	if pending.TaskID != nil && *pending.TaskID != "" {
		thread = reviewableThread(state, pending.TaskID)
	}
	review, ok := pending.Operation.(contracts.RunOperationReview)
	if thread == nil || !ok {
		return Settled(state, nil, nil)
	}
	command := StartRunCommand(state, thread, pending.RunID, "", workspace.ID, thread.ExecutionPolicy)
	command.Operation = contracts.RunOperationReview{Target: review.Target}
	if runsIsTrue(thread.InheritedContinuation) {
		command.ForkContinuation = new(true)
	}
	provenance := ThreadRunStateAttendedRun
	provenance.Operation = new(RunProvenanceOperationReview)
	return Settled(BeginRun(state, thread.ID, pending.RunID, provenance, nil, ""), []WorkspaceEffect{WorkspaceEffectStartRun{Command: command}}, nil)
}

func startComposerRun(state *WorkspaceState, pending *PendingRun, workspace domain.WorkspaceRecord, worktree *contracts.CreatedWorktree) WorkspaceTransition {
	var existing *domain.Thread
	if pending.TaskID != nil && *pending.TaskID != "" {
		existing = runThreadOf(state.Threads, *pending.TaskID)
		if existing == nil {
			return Rejected(state, "This thread was removed before its message could be sent.", nil)
		}
	}
	if existing != nil && existing.ArchivedAt != nil {
		return Rejected(WithUnsent(state, pending), "This thread was archived before its message could be sent. Restore it from Archived threads in Settings to send it again.", nil)
	}
	if pending.TaskID != nil && *pending.TaskID != "" && state.ActiveRuns[*pending.TaskID] != nil {
		return Rejected(WithUnsent(state, pending), "This thread started another run before its message could be sent. Send the message again.", nil)
	}
	var created *domain.Worktree
	if worktree != nil && runsTruthy(pending.ProjectID) {
		created = worktreeCreated(*worktree, *pending.ProjectID)
	}
	arriving := created
	if arriving == nil && existing != nil {
		arriving = WorktreeFor(state, existing)
	}
	if arriving == nil {
		arriving = WorktreeByID(state, pending.WorktreeID)
	}
	entering := arriving != nil && (existing == nil || existing.WorktreeEnteredAt == nil)
	catalog := CatalogOf(state)
	engine := state.DraftEngine
	if pending.Engine != nil {
		engine = *pending.Engine
	}
	model := state.DraftModel
	if pending.Model != nil {
		model = *pending.Model
	}
	if existing == nil && !EngineOffersModel(state, engine, model) {
		return Rejected(WithUnsent(state, pending), domain.UnknownModel(model, ""), nil)
	}
	draft := DraftOf(state)
	draft.Engine, draft.Model = engine, model
	if pending.Effort != nil && *pending.Effort != "" {
		draft.Effort = *pending.Effort
	}
	if pending.Role != nil && *pending.Role == domain.ThreadRoleReviewer {
		draft.Policy = domain.CapabilitiesFor(domain.EngineID(engine)).UnattendedPolicy
	}
	var role domain.CoordinationRole
	if pending.Role != nil && *pending.Role == domain.ThreadRoleCoordinator {
		role = "coordinator"
	}
	choice := domain.NormalizeDraft(&catalog, draft, role)
	thread := existing
	if thread == nil {
		title := ""
		if runsTruthy(pending.Title) {
			title = domain.ClampTitle(*pending.Title)
		} else {
			named := pending.Text
			if named == "" {
				named = PasteTitle(runsNonNil(pending.Pastes))
			}
			if named == "" {
				named = FileTitle(runsNonNil(pending.Files))
			}
			attachments := make([]*domain.RunAttachment, len(pending.Attachments))
			for index, path := range pending.Attachments {
				attachments[index] = &domain.RunAttachment{Path: path, Labels: []string{}}
			}
			title = ThreadTitleFor(named, attachments)
		}
		thread = &domain.Thread{}
		thread.ID = NewID()
		thread.Title = title
		if runsTruthy(pending.ProjectID) {
			thread.ProjectID = pending.ProjectID
		}
		thread.ExecutionPolicy = choice.Policy
		thread.Engine = choice.Engine
		if choice.Model != "" {
			thread.Model = new(choice.Model)
		}
		thread.Effort = new(choice.Effort)
		if domain.CapabilitiesFor(domain.EngineID(choice.Engine)).FastMode {
			thread.FastMode = new(choice.FastMode)
		}
		if pending.Role != nil && *pending.Role != "" {
			thread.Role = pending.Role
		}
		if pending.Coordination != nil && pending.Coordination.Brief != nil {
			thread.Brief = pending.Coordination.Brief
		}
		thread.Messages = []*domain.ConversationMessage{}
		thread.ContinuationStatus = domain.ContinuationStatusNone
		thread.LastChangeSnapshot = domain.ChangeSnapshot{Files: []string{}, CapturedAt: Now()}
		thread.SortIndex = new(NextSortIndex(state.Threads))
		thread.CreatedAt = new(Now())
		thread.RankedAt = Now()
		thread.UpdatedAt = Now()
	}
	if created != nil && (created.Name == nil || *created.Name == "") {
		created.Name = new(thread.Title)
	}
	message := domain.CreateSentMessage(domain.SentMessage{Text: pending.Text, Origin: pending.MessageOrigin, Attachments: pending.Attachments, Annotations: pending.Annotations, Pastes: pending.Pastes, Files: pending.Files})
	var arrival []*domain.ConversationMessage
	if arriving != nil && (existing == nil || existing.WorktreeID == nil || *existing.WorktreeID != arriving.ID) {
		detail := "Detached at " + runsJSSlice(arriving.BaseCommit, 7)
		arrival = []*domain.ConversationMessage{domain.CreateConversationMessage(domain.ConversationMessageKindSystem, "Moved into a worktree at "+arriving.Root, &detail, nil, nil, nil, nil)}
	}
	located := thread
	if arriving != nil {
		moved := *thread
		moved.WorktreeID = new(arriving.ID)
		if moved.WorktreeEnteredAt == nil {
			moved.WorktreeEnteredAt = new(Now())
		}
		located = &moved
	}
	inherited := runsIsTrue(located.InheritedContinuation)
	coordinated := CoordinatedStartCoordinatedStart(state, pending, located, existing)
	updated := new(coordinated.Thread)
	updated.Messages = slices.Concat(coordinated.Thread.Messages, arrival, []*domain.ConversationMessage{message})
	updated.UpdatedAt = Now()
	var listed []*domain.Thread
	if existing != nil {
		listed = threadsUpdated(state.Threads, thread.ID, func(*domain.Thread) *domain.Thread { return updated })
	} else {
		listed = append([]*domain.Thread{updated}, state.Threads...)
	}
	threads, startingProposals := WithSuggestionStarted(state, pending, existing, listed, updated)
	focusing := existing == nil && pending.DraftKey != nil
	owner := state
	if focusing {
		owner = HandOverDraftDock(state, thread.ID)
	}
	var arrivingID *string
	if arriving != nil {
		arrivingID = new(arriving.ID)
	}
	owning := *WithUsedWorktree(owner, created, arrivingID)
	owning.Threads = threads
	owning.StartingProposals = startingProposals
	if focusing {
		owning.DraftBranch = nil
		owning.DraftWorktree = false
		owning.DraftWorktreeID = nil
		owning.DraftRole = nil
		owning.CurrentID = new(thread.ID)
	}
	provenance := ThreadRunStateAttendedRun
	if runsIsTrue(pending.UsersOwn) {
		provenance.UsersOwn = new(true)
	}
	pickup := RunPickup("")
	if runsIsTrue(pending.Warming) {
		pickup = RunPickupWarming
	}
	started := BeginRun(&owning, thread.ID, pending.RunID, provenance, nil, pickup)
	drained := started
	if pending.QueuedIDs != nil {
		rest := []*QueuedMessage{}
		for _, queued := range QueuedFor(started, thread.ID) {
			if !slices.Contains(pending.QueuedIDs, queued.ID) {
				rest = append(rest, queued)
			}
		}
		drained = WithQueued(started, thread.ID, rest)
	}
	var titling []WorkspaceEffect
	if existing == nil && !runsTruthy(pending.Title) && (pending.Text != "" || len(pending.Attachments) > 0) {
		titling = []WorkspaceEffect{WorkspaceEffectSuggestTitle{TaskID: thread.ID, Engine: thread.Engine, Model: RunModelOf(state, thread), Text: pending.Text, Attachments: pending.Attachments}}
	}
	listedState := *state
	listedState.Threads = threads
	command := StartRunCommand(&listedState, updated, pending.RunID, coordinated.Prompt, workspace.ID, updated.ExecutionPolicy)
	if (entering || inherited) && updated.Continuation != nil {
		command.ForkContinuation = new(true)
	}
	command = SideChannelFor(state, updated).Apply(command)
	reviewing := Settled(drained, nil, nil)
	if handed := state.Diffs[DraftDock]; focusing && handed != nil {
		reviewing = ReadDiffFrom(drained, thread.ID, new(workspace.ID), handed.Range, nil)
	}
	final := &reviewing.State
	if pending.DraftKey != nil {
		final = WithoutStartedDraft(final, *pending.DraftKey, pending)
	}
	effects := slices.Concat([]WorkspaceEffect{WorkspaceEffectStartRun{Command: command}}, titling, FollowUps(reviewing.Effects))
	return Settled(final, effects, contracts.WorkspaceCommandResultTrue{TaskID: new(thread.ID)})
}

// startResume picks a thread back up after its usage limit lifted or an update restarted the app.
func startResume(state *WorkspaceState, pending *PendingRun, workspace domain.WorkspaceRecord) WorkspaceTransition {
	thread := ThreadToBegin(state, pending.TaskID)
	if thread == nil {
		return Settled(state, nil, nil)
	}
	resume, _ := pending.Operation.(PendingRunOperationResume)
	update := resume.After == PendingRunOperationResumeAfterUpdate
	text := "Usage limit reset. Resuming."
	switch {
	case resume.After == PendingRunOperationResumeAfterMemory:
		text = "Resuming after a memory stop. Check interrupted actions before repeating them."
	case update:
		text = "App updated. Resuming."
	}
	resumed := UpdateThread(state, thread.ID, func(item *domain.Thread) *domain.Thread {
		return runsWithMessages(item, domain.AppendMessages(item.Messages, []*domain.ConversationMessage{domain.CreateConversationMessage(domain.ConversationMessageKindSystem, text, nil, nil, nil, nil, nil)}))
	})
	command := StartRunCommand(state, thread, pending.RunID, pending.Prompt, workspace.ID, thread.ExecutionPolicy)
	if runsIsTrue(thread.InheritedContinuation) && thread.Continuation != nil {
		command.ForkContinuation = new(true)
	}
	command = SideChannelFor(state, thread).Apply(command)
	pickup := RunPickupWarming
	if update {
		pickup = RunPickupCarryingOn
	}
	return Settled(BeginRun(resumed, thread.ID, pending.RunID, ThreadRunStateAttendedRun, nil, pickup), []WorkspaceEffect{WorkspaceEffectStartRun{Command: command}}, nil)
}

func startAutomationRun(state *WorkspaceState, pending *PendingRun, workspace domain.WorkspaceRecord, worktree *contracts.CreatedWorktree) WorkspaceTransition {
	taskID := *pending.TaskID
	thread := ThreadToBegin(state, pending.TaskID)
	if thread == nil {
		return Settled(state, Ack(pending, false), nil)
	}
	message := domain.CreateSentMessage(domain.SentMessage{Text: pending.Text, Origin: pending.MessageOrigin})
	if runsIsTrue(pending.Quiet) {
		withdrawn := *message
		withdrawn.Withdrawn = new(true)
		message = &withdrawn
	}
	var created *domain.Worktree
	if worktree != nil && runsTruthy(thread.ProjectID) {
		created = worktreeCreated(*worktree, *thread.ProjectID)
		if created.Name == nil {
			created.Name = new(thread.Title)
		}
	}
	entered := created
	if entered == nil {
		entered = WorktreeFor(state, thread)
	}
	var enteredID *string
	if entered != nil {
		enteredID = new(entered.ID)
	}
	withMessage := UpdateThread(WithUsedWorktree(state, created, enteredID), taskID, func(item *domain.Thread) *domain.Thread {
		next := *item
		if entered != nil {
			next.WorktreeID = new(entered.ID)
			if next.WorktreeEnteredAt == nil {
				next.WorktreeEnteredAt = new(Now())
			}
		}
		next.Messages = slices.Concat(item.Messages, []*domain.ConversationMessage{message})
		next.UpdatedAt = Now()
		return &next
	})
	policy := thread.ExecutionPolicy
	if pending.Policy != nil {
		policy = *pending.Policy
	}
	command := StartRunCommand(state, thread, pending.RunID, pending.Prompt, workspace.ID, policy)
	if runsIsTrue(pending.Unattended) {
		command.Unattended = new(true)
	}
	mark := ThreadRunStateThreadMark(thread)
	provenance := RunProvenance{Origin: RunProvenanceOriginAutomation, Quiet: runsIsTrue(pending.Quiet)}
	effects := slices.Concat([]WorkspaceEffect{WorkspaceEffectStartRun{Command: command}}, Ack(pending, true))
	return Settled(BeginRun(withMessage, taskID, pending.RunID, provenance, &mark, ""), effects, nil)
}

// runResolved starts a run in the workspace it resolved to.
func runResolved(state *WorkspaceState, input WorkspaceEventRunResolved) WorkspaceTransition {
	pending := state.PendingRuns[input.PendingID]
	if pending == nil {
		return Settled(state, nil, nil)
	}
	next := WithoutPending(state, input.PendingID)
	var project *domain.Project
	if runsTruthy(pending.ProjectID) {
		for _, item := range next.Projects {
			if item.ID == *pending.ProjectID {
				project = item
				break
			}
		}
	}
	if project != nil && !runsTruthy(project.WorkspaceID) && input.Workspace.Kind == domain.WorkspaceKindProject && input.Workspace.Root == project.Root {
		projects := make([]*domain.Project, len(next.Projects))
		for index, item := range next.Projects {
			if item.ID == project.ID {
				adopted := *item
				adopted.WorkspaceID = new(input.Workspace.ID)
				projects[index] = &adopted
			} else {
				projects[index] = item
			}
		}
		copied := *next
		copied.Projects = projects
		next = &copied
	}
	if runsTruthy(pending.TaskID) {
		if thread := runThreadOf(next.Threads, *pending.TaskID); thread != nil && thread.ResourceStop != nil {
			if input.Worktree != nil && runsTruthy(pending.ProjectID) {
				next = WithUsedWorktree(next, worktreeCreated(*input.Worktree, *pending.ProjectID), new(input.Worktree.ID))
			}
			if pending.Origin == PendingRunOriginAutomation {
				return Settled(next, Ack(pending, false), nil)
			}
			return Rejected(WithUnsent(next, pending), "Resume this thread after its memory stop before starting another run.", nil)
		}
	}
	if gone := modelGone(next, pending); gone != nil {
		return *gone
	}
	switch pending.Operation.(type) {
	case PendingRunOperationCompact:
		return startCompaction(next, pending, input.Workspace)
	case contracts.RunOperationReview:
		return startReview(next, pending, input.Workspace)
	case PendingRunOperationResume:
		return startResume(next, pending, input.Workspace)
	}
	if pending.Origin == PendingRunOriginAutomation {
		return startAutomationRun(next, pending, input.Workspace, input.Worktree)
	}
	return startComposerRun(next, pending, input.Workspace, input.Worktree)
}

// imageReference is a streamed answer naming an image file, which the app keeps a copy of.
var imageReference = regexp.MustCompile(`(?i)\.(?:png|jpe?g|gif|webp)\b`)

// runReported folds what a run reports into its thread and the runs, settling its turn when the report ends it.
func runReported(state *WorkspaceState, reported contracts.RunEvent) WorkspaceTransition {
	base := RunEventBaseOf(reported)
	terminal := false
	if status, ok := reported.(contracts.RunEventRunStatus); ok {
		terminal = status.Status == domain.RunStatusCancelled || status.Status == domain.RunStatusFailed || status.Status == domain.RunStatusSucceeded
	}
	if thread := runThreadOf(state.Threads, base.TaskID); !terminal && thread != nil && thread.ResourceStop != nil {
		return Settled(state, nil, nil)
	}
	opened := state
	if started, ok := reported.(contracts.RunEventRunStarted); ok && runsIsTrue(started.AgentInitiated) && state.ActiveRuns[base.TaskID] == nil && runThreadOf(state.Threads, base.TaskID) != nil {
		opened = BeginRun(state, base.TaskID, base.RunID, ThreadRunStateAttendedRun, nil, RunPickupAgent)
	}
	active := opened.ActiveRuns[base.TaskID]
	record := RunRecord(active)
	if record == nil || base.RunID != record.RunID || base.Sequence <= record.Sequence {
		return Settled(state, nil, nil)
	}
	ending := turnEnding(opened, active, reported)
	event := reported
	if status, ok := reported.(contracts.RunEventRunStatus); ok && ending != nil {
		event = SettledEvent(status, *ending)
	}
	applied := ApplyRunEvent(opened, event)
	if delta, ok := event.(contracts.RunEventAssistantDelta); ok {
		thread := runThreadOf(applied.Threads, delta.TaskID)
		messageID := delta.MessageID
		if !runsIsTrue(delta.Artifact) {
			messageID = AnswerPartID(applied.ActiveRuns[delta.TaskID], delta.MessageID)
		}
		var message *domain.ConversationMessage
		if thread != nil {
			for _, item := range thread.Messages {
				if item.ID == messageID {
					message = item
					break
				}
			}
		}
		if message == nil || !imageReference.MatchString(delta.Text) {
			return Settled(applied, []WorkspaceEffect{}, nil)
		}
		root, _ := ThreadWorkspaceRoot(applied, thread)
		return Settled(applied, []WorkspaceEffect{WorkspaceEffectPreserveMessageImages{Text: message.Text, Root: root, MessageID: messageID}}, nil)
	}
	if ending != nil && ending.RestoresTestimony {
		return SettledTurn(applied, base.TaskID, *ending, active, Now())
	}
	next := applied
	if ending != nil {
		next = WithTurnTestimony(applied, base.TaskID, *ending, active)
	}
	if _, ok := event.(contracts.RunEventComputerUseSetupRequired); ok {
		copied := *next
		copied.ComputerUseSetup = true
		next = &copied
	}
	var said []WorkspaceEffect
	if noticed := runThreadOf(next.Threads, base.TaskID); noticed != nil && (ending == nil || !ending.Dismisses) {
		var surfacing *RunSurfacing
		if ending != nil {
			surfacing = ending.Surfacing
		}
		if headline, reason, ok := runNotice(noticed, active, event, surfacing); ok {
			said = Announced(next, noticed, headline, reason)
		}
	}
	switch event := event.(type) {
	case contracts.RunEventQueuedDelivered:
		next = WithDeliveredMessage(next, event.TaskID, event.MessageID)
	case contracts.RunEventQueuedSteerFailed:
		next = WithSteeringFailure(next, event.TaskID, event.MessageID, event.Message)
	}
	finished := ending != nil && (ending.Status == TurnStatusSucceeded || ending.Status == TurnStatusFailed)
	workspaceID, hasWorkspace := "", false
	if thread := runThreadOf(state.Threads, base.TaskID); thread != nil {
		workspaceID, hasWorkspace = ThreadWorkspaceID(state, thread)
		hasWorkspace = hasWorkspace && workspaceID != ""
	}
	settledDiff := Settled(next, nil, nil)
	if finished && hasWorkspace {
		settledDiff = RereadReviewsOf(next, base.TaskID, workspaceID)
	}
	next = &settledDiff.State
	var reread WorkspaceEffect
	if finished {
		if read, effect, ok := turnPullRequestRead(next, base.TaskID); ok {
			next, reread = read, effect
		}
	}
	var environment []WorkspaceEffect
	if finished && hasWorkspace {
		environment = append(environment, WorkspaceEffectRefreshEnvironment{WorkspaceID: workspaceID, TaskID: new(base.TaskID), RunID: new(base.RunID), FollowUp: new(true)})
		environment = append(environment, settledDiff.Effects...)
		if reread != nil {
			environment = append(environment, withFollowUp(reread))
		}
	}
	if ending == nil {
		return Settled(next, slices.Concat(environment, said), nil)
	}
	moved := Applied(next, SliceTransition(MoveAfterRunSettled(next, base.TaskID, domain.RunStatus(ending.Status))))
	drained := SettledAfterTestimony(&moved.State, base.TaskID, *ending, active, Now())
	return Settled(&drained.State, slices.Concat(environment, said, moved.Effects, drained.Effects), nil)
}

// worktreeCreated is a checkout made on the way to a run, recorded under the project the run resolved in.
func worktreeCreated(worktree contracts.CreatedWorktree, projectID string) *domain.Worktree {
	return &domain.Worktree{
		ID:          worktree.ID,
		Name:        worktree.Name,
		ProjectID:   projectID,
		Root:        worktree.Root,
		WorkspaceID: worktree.WorkspaceID,
		BaseCommit:  worktree.BaseCommit,
		CreatedAt:   worktree.CreatedAt,
		LastUsedAt:  worktree.LastUsedAt,
	}
}

// runsJSSlice is String.prototype.slice(0, end), counted in UTF-16 code units.
func runsJSSlice(text string, end int) string {
	units := 0
	for index, r := range text {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > end {
			return text[:index]
		}
		units += width
	}
	return text
}
