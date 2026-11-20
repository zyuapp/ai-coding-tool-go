package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// SendInputs are the input types ReduceSending takes.
var SendInputs = []string{"task.send", "question.answer", "question.set-answer", "task.steer-queued", "task.drop-queued", "task.edit-queued"}

// ReduceSending takes an input whose type is one of SendInputs. Only coordination's ProposalID is
// read; coordination and said are what the reducer itself adds to a send, zero for nothing.
func ReduceSending(state *WorkspaceState, input WorkspaceInput, coordination CoordinationSend, said SendSaid) Step {
	switch input := input.(type) {
	case contracts.RunControlCommandQuestionSetAnswer:
		active := state.ActiveRuns[input.TaskID]
		question := waitingQuestion(active, input.RequestID, input.QuestionID)
		if active == nil || RunRecord(active).RunID != input.RunID || question == nil {
			return Step{}
		}
		if runsIsTrue(question.Submitting) || question.Answer != nil && *question.Answer == input.Text {
			return Step{}
		}
		return Step{Edits: []OwnerEdit{QuestionAnswerIn(RunsOf(state), input.TaskID, question, new(input.Text), nil)}}

	case contracts.RunControlCommandQuestionAnswer:
		active := state.ActiveRuns[input.TaskID]
		question := waitingQuestion(active, input.RequestID, input.QuestionID)
		if active == nil || RunRecord(active).RunID != input.RunID || question == nil {
			return Step{Refusal: new("That question is no longer waiting for an answer.")}
		}
		if runsIsTrue(question.Submitting) {
			return Step{}
		}
		text := ""
		switch {
		case input.Text != nil:
			text = *input.Text
		case question.Answer != nil:
			text = *question.Answer
		}
		text = runsJSTrim(text)
		if text == "" {
			return Step{}
		}
		answering := Step{Edits: []OwnerEdit{QuestionAnswerIn(RunsOf(state), input.TaskID, question, new(text), new(true))}}
		return AndThen(state, answering, func(next *WorkspaceState) Step {
			step := RunQueueAttendedRun(next, input.TaskID)
			step.Effects = []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.AnswerQuestionCommand{TaskID: input.TaskID, RunID: input.RunID, RequestID: input.RequestID, QuestionID: input.QuestionID, Text: text}}}
			return step
		})

	case contracts.TaskCommandTaskSend:
		return sendingSent(state, input, coordination, said)

	case contracts.TaskCommandTaskSteerQueued:
		target := TargetID(state, input.TaskID)
		if target == nil {
			return Step{}
		}
		taskID := *target
		active := state.ActiveRuns[taskID]
		queued := QueuedFor(state, taskID)
		index := slices.IndexFunc(queued, func(item *QueuedMessage) bool { return item.ID == input.MessageID })
		if active == nil || index == -1 || runsIsTrue(queued[index].Steering) {
			return Step{}
		}
		message := queued[index]
		var edits []OwnerEdit
		if thread := runThreadOf(state.Threads, taskID); thread != nil {
			edits = append(edits, unsnoozed(ThreadsOf(state), thread))
		}
		locked := make([]*QueuedMessage, len(queued))
		for position, item := range queued {
			if item.ID == message.ID {
				steering := *item
				steering.Steering = new(true)
				locked[position] = &steering
			} else {
				locked[position] = item
			}
		}
		edits = append(edits, QueuedIn(RunsOf(state), taskID, locked))
		runID := RunRecord(active).RunID
		return AndThen(state, Step{Edits: edits}, func(next *WorkspaceState) Step {
			step := RunQueueAttendedRun(next, taskID)
			step.Effects = []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.SteerRunCommand{TaskID: taskID, RunID: runID, MessageID: message.ID, Prompt: SideChatPrompt(state, taskID, message.Prompt)}}}
			return step
		})

	case contracts.TaskCommandTaskDropQueued:
		target := TargetID(state, input.TaskID)
		var queued []*QueuedMessage
		if target != nil {
			queued = QueuedFor(state, *target)
		}
		index := slices.IndexFunc(queued, func(item *QueuedMessage) bool { return item.ID == input.MessageID })
		if target == nil || index == -1 || runsIsTrue(queued[index].Steering) || DrainingQueued(state, *target, queued[index].ID) {
			if runsIsTrue(input.IfWaiting) {
				return Step{Result: contracts.WorkspaceCommandResultFalse{Message: QueuedSent}}
			}
			return Step{}
		}
		message := queued[index]
		return Step{Edits: []OwnerEdit{QueuedIn(RunsOf(state), *target, withoutQueued(queued, message.ID)), UndeliveredIn(ThreadsOf(state), *target, []*QueuedMessage{message})}}

	case contracts.TaskCommandTaskEditQueued:
		var remote *PairedComputer
		var heldID *string
		if len(state.Computers.Paired) > 0 {
			remote, heldID = QueueHolder(state, input.TaskID)
		}
		var target *string
		if remote != nil {
			target = heldID
		} else {
			target = TargetID(state, input.TaskID)
		}
		var queued []*QueuedMessage
		if target != nil {
			switch {
			case remote == nil:
				queued = QueuedFor(state, *target)
			case remote.State != nil:
				queued = QueuedFor(remote.State, *target)
			}
		}
		if target == nil {
			return Step{}
		}
		message, refusal := EditableQueued(queued, input.MessageID)
		if refusal != "" || remote == nil && DrainingQueued(state, *target, message.ID) {
			return Step{}
		}
		handed := HandedBackIn(ComposerOf(state), *target, []*domain.HeldMessage{new(message.HeldMessage)})
		var edits []OwnerEdit
		if remote == nil {
			edits = append(edits, QueuedIn(RunsOf(state), *target, withoutQueued(queued, message.ID)))
		}
		if holder, ok := TabHolderOf(state, *target); ok {
			edits = append(edits, handed, FocusedTabIn(DocksOf(state), holder, *target))
		} else {
			edits = append(edits, FocusComposerIn(handed))
		}
		return Step{Edits: edits}
	}
	return Step{}
}

// sendingSent is a message sent to a thread, or to start one: queued behind a run already going, or a run set on its way.
func sendingSent(state *WorkspaceState, input contracts.TaskCommandTaskSend, coordination CoordinationSend, said SendSaid) Step {
	answers := said.Answers
	attachments := input.Attachments
	var draftKey *string
	if input.Text == nil {
		if input.TaskID != nil {
			draftKey = input.TaskID
		} else {
			draftKey = new(PromptKey(state))
		}
	}
	drafted := ""
	switch {
	case input.Text != nil:
		drafted = *input.Text
	case draftKey != nil:
		drafted = state.Prompts[*draftKey]
	}
	drafted = runsJSTrim(drafted)
	text := drafted
	if draftKey != nil && strings.Contains(drafted, "@") {
		text = domain.ExpandThreadHandles(drafted, ThreadHandleOptions(state, *draftKey))
	}
	annotations := []*domain.Annotation{}
	pastes := []*domain.PastedText{}
	files := []*domain.AttachedFile{}
	if draftKey != nil {
		for _, annotation := range AnnotationsFor(state, *draftKey) {
			sentAnnotation := *annotation
			sentAnnotation.Anchor = nil
			annotations = append(annotations, &sentAnnotation)
		}
		pastes = runsNonNil(PastesFor(state, *draftKey))
		files = runsNonNil(FilesFor(state, *draftKey))
	}
	alreadySending := false
	if draftKey != nil {
		for _, pending := range state.PendingRuns {
			if pending.DraftKey != nil && *pending.DraftKey == *draftKey {
				alreadySending = true
				break
			}
		}
	}
	if text == "" && len(attachments) == 0 && len(annotations) == 0 && len(pastes) == 0 && len(files) == 0 || alreadySending {
		return Step{}
	}
	if input.TaskID != nil && TargetID(state, input.TaskID) == nil {
		return Step{}
	}
	if input.TaskID == nil && input.Lead != nil {
		if asked, ok := askedBefore(state, *input.Lead); ok {
			return asked
		}
	}
	if input.TaskID != nil && slices.ContainsFunc(state.SideChats, func(chat *SideChat) bool { return chat.ID == *input.TaskID }) && ForkableContinuation(state, *input.TaskID) == nil {
		return Step{}
	}
	var thread *domain.Thread
	switch {
	case input.TaskID != nil:
		thread = runThreadOf(state.Threads, *input.TaskID)
	case draftKey != nil && state.CurrentID != nil:
		thread = runThreadOf(state.Threads, *state.CurrentID)
	}
	fromThread := input.From != nil || input.Sender != nil
	refuse := func(message string) Step {
		if fromThread {
			return Step{Result: contracts.WorkspaceCommandResultFalse{Message: message}}
		}
		return Step{Refusal: new(message)}
	}
	if thread != nil && thread.ResourceStop != nil {
		return refuse("Resume this thread after its memory stop before sending another message.")
	}
	if thread != nil && thread.ArchivedAt != nil {
		if fromThread {
			return refuse(ArchivedSendError)
		}
		restored := AndThen(state, Step{Edits: []OwnerEdit{RestoredIn(ThreadsOf(state), thread.ID)}}, func(next *WorkspaceState) Step {
			return ReduceSending(next, input, coordination, said)
		})
		restored.Effects = slices.Concat(FileThread(state, thread, false), restored.Effects)
		return restored
	}
	if thread != nil && slices.Contains(state.CreatingWorktrees, thread.ID) {
		return refuse(WorktreeCreatingError)
	}
	if thread != nil && LeavingThreadIds(state).Has(thread.ID) {
		return refuse(WorktreeReleasingError)
	}
	catalog := CatalogOf(state)
	var runEngine domain.AgentEngine
	switch {
	case thread != nil:
		runEngine = thread.Engine
	case input.Model == nil:
		runEngine = state.DraftEngine
	case input.Engine != nil:
		runEngine = *input.Engine
	default:
		runEngine, _ = domain.ModelEngine(&catalog, *input.Model, "")
	}
	if runEngine != "" {
		readiness := EngineReadinessOf(state, runEngine)
		if blocked, ok := domain.EngineBlocker(runEngine, &readiness); ok && blocked != "" {
			engines, effects := RefreshedEnginesIn(EnginesOf(state))
			return Step{Error: &SliceError{Message: new(blocked), Page: new(domain.SettingsSectionEngines)}, Edits: []OwnerEdit{engines}, Effects: effects, Refusal: new(blocked)}
		}
	}
	var runModel domain.AgentModel
	hasModel := true
	switch {
	case thread != nil:
		runModel, hasModel = domain.ThreadModel(&catalog, thread)
	case input.Model != nil:
		runModel = *input.Model
	default:
		runModel = state.DraftModel
	}
	if !hasModel {
		engine := state.DraftEngine
		if thread != nil {
			engine = thread.Engine
		}
		return refuse(domain.NoModel(engine))
	}
	if runEngine == "" || !EngineOffersModel(state, runEngine, runModel) {
		return refuse(domain.UnknownModel(runModel, ""))
	}
	var sender *SenderPromptSender
	if input.From != nil && (thread == nil || *input.From != thread.ID) {
		if local := runThreadOf(state.Threads, *input.From); local != nil {
			sender = &SenderPromptSender{ID: local.ID, Title: local.Title}
		}
	}
	if sender == nil && input.Sender != nil {
		sender = &SenderPromptSender{ID: input.Sender.ThreadID, Title: input.Sender.Title, Computer: input.Sender.Computer}
	}
	prompt := SentPrompt(text, pastes, annotations, runsNonNil(attachments), files)
	var sentBy domain.MessageOrigin
	signed := prompt
	if sender != nil {
		origin := domain.MessageOriginThread{ThreadID: new(sender.ID), Title: sender.Title}
		if sender.Computer != "" {
			origin.Computer = new(sender.Computer)
		}
		sentBy = origin
		signed = SenderPrompt(state.Threads, *sender, thread) + "\n\n" + prompt
	}
	usersOwn := thread != nil && input.From == nil && input.Sender == nil && answers == nil && !runsIsTrue(said.Step)
	paths := make([]string, len(attachments))
	for index, attachment := range attachments {
		paths[index] = attachment.Path
	}
	held := thread != nil && state.ActiveRuns[thread.ID] == nil && HeldByLimit(state, thread.ID, Now())
	draining := false
	if thread != nil {
		for _, pending := range state.PendingRuns {
			if pending.TaskID != nil && *pending.TaskID == thread.ID && pending.QueuedIDs != nil {
				draining = true
				break
			}
		}
	}
	if thread != nil && (state.ActiveRuns[thread.ID] != nil || held || draining) {
		queued := &QueuedMessage{HeldMessage: domain.HeldMessage{ID: NewID(), Text: text, Prompt: signed, Attachments: paths}}
		queued.Origin, queued.Detail = domain.OriginFields(sentBy)
		if runsTruthy(answers) {
			queued.Answers = answers
		}
		if usersOwn {
			queued.UsersOwn = new(true)
		}
		if len(annotations) > 0 {
			queued.Annotations = annotations
		}
		if len(pastes) > 0 {
			queued.Pastes = pastes
		}
		if len(files) > 0 {
			queued.Files = files
		}
		written := unsnoozed(ThreadsOf(state), thread)
		var edits []OwnerEdit
		if draftKey != nil {
			edits = append(edits, ClearedDraftIn(ComposerOf(state), *draftKey))
		}
		if held {
			edits = append(edits, NudgedIn(written, thread.ID, Now()))
		} else {
			edits = append(edits, written)
		}
		edits = append(edits, QueuedIn(RunsOf(state), thread.ID, append(slices.Clip(QueuedFor(state, thread.ID)), queued)))
		waiting := Step{Edits: edits}
		if runsIsTrue(input.Steer) && !held {
			threadID := thread.ID
			return AndThen(state, waiting, func(next *WorkspaceState) Step {
				return ReduceSending(next, contracts.TaskCommandTaskSteerQueued{TaskID: &threadID, MessageID: queued.ID}, CoordinationSend{}, SendSaid{})
			})
		}
		return waiting
	}
	var worktreeID *string
	if thread == nil {
		switch {
		case input.WorktreeID != nil:
			worktreeID = input.WorktreeID
		case draftKey != nil:
			worktreeID = state.DraftWorktreeID
		}
	}
	namedProject, namedWorktree, err := NamedPlace(state, input.Project, worktreeID)
	if err != nil {
		return Step{Refusal: new(err.Error())}
	}
	var projectID *string
	switch {
	case thread != nil && thread.ProjectID != nil:
		projectID = thread.ProjectID
	case namedWorktree != nil:
		projectID = new(namedWorktree.ProjectID)
	case namedProject != nil:
		projectID = new(namedProject.ID)
	case draftKey != nil:
		projectID = state.DraftProjectID
	}
	var project *domain.Project
	if runsTruthy(projectID) {
		for _, item := range state.Projects {
			if item.ID == *projectID {
				project = item
				break
			}
		}
		if project == nil {
			return Step{Refusal: new(MissingProjectError)}
		}
	}
	var role *domain.ThreadRole
	if thread == nil {
		switch {
		case input.Role != nil:
			role = input.Role
		case draftKey != nil:
			role = state.DraftRole
		}
	}
	pending := &PendingRun{
		ID:          NewID(),
		RunID:       NewID(),
		Origin:      PendingRunOriginComposer,
		Text:        text,
		Prompt:      signed,
		Attachments: paths,
	}
	if thread != nil {
		pending.TaskID = new(thread.ID)
	}
	if project != nil {
		pending.ProjectID = new(project.ID)
	}
	if namedWorktree != nil {
		pending.WorktreeID = new(namedWorktree.ID)
	}
	if thread == nil && input.Model != nil {
		pending.Model = input.Model
		pending.Engine = new(runEngine)
	}
	if thread == nil && input.Effort != nil {
		pending.Effort = input.Effort
	}
	if role != nil && *role != "" {
		pending.Role = role
	}
	if thread == nil && input.Title != nil {
		pending.Title = input.Title
	}
	if thread == nil {
		pending.Coordination = CoordinationSendOf(state, CoordinationSend{CoordinatorID: input.CoordinatorID, Lead: input.Lead, Brief: input.Brief, Memory: input.Memory, ProposalID: coordination.ProposalID})
	}
	if draftKey != nil {
		pending.DraftKey = draftKey
		pending.DraftText = new(state.Prompts[*draftKey])
	}
	pending.MessageOrigin = sentBy
	if runsTruthy(answers) {
		pending.Answers = answers
	}
	if usersOwn {
		pending.UsersOwn = new(true)
	}
	if len(annotations) > 0 {
		pending.Annotations = annotations
	}
	if len(pastes) > 0 {
		pending.Pastes = pastes
	}
	if len(files) > 0 {
		pending.Files = files
	}
	wantsWorktree := false
	if thread == nil && namedWorktree == nil {
		if input.Worktree != nil {
			wantsWorktree = *input.Worktree
		} else {
			wantsWorktree = draftKey != nil && state.DraftWorktree
		}
	}
	var branch *DraftBranch
	if thread == nil && draftKey != nil {
		branch = state.DraftBranch
	}
	if branch != nil && !wantsWorktree && project != nil && RunsInWorkspace(state, project.WorkspaceID) {
		return Step{Refusal: new(CheckoutRunningError)}
	}
	place := namedWorktree
	if place == nil {
		place = WorktreeFor(state, thread)
	}
	resolving := ResolveWorkspaceEffect(pending.ID, thread, project, place, wantsWorktree, branch)
	if resolving.CreateWorktree != nil {
		pending.CreatingWorktree = new(true)
	}
	pended := PendingStep(state, pending)
	if thread != nil {
		pended.Edits = append([]OwnerEdit{unsnoozed(ThreadsOf(state), thread)}, pended.Edits...)
	}
	pended.Effects = []WorkspaceEffect{resolving}
	return pended
}

// askedBefore answers a suggestion a coordinator on another computer asked for that was already
// started or is still starting; false to start it.
func askedBefore(state *WorkspaceState, lead domain.RemoteLead) (Step, bool) {
	if lead.ProposalID == nil {
		return Step{}, false
	}
	same := func(held *domain.RemoteLead) bool {
		return held != nil && held.ThreadID == lead.ThreadID && held.ProposalID != nil && *held.ProposalID == *lead.ProposalID
	}
	for _, thread := range state.Threads {
		if thread.RemoteLead != nil && same(&thread.RemoteLead.RemoteLead) {
			return Step{Result: contracts.WorkspaceCommandResultTrue{TaskID: new(thread.ID)}}, true
		}
	}
	for _, pending := range state.PendingRuns {
		if pending.Coordination != nil && same(pending.Coordination.Lead) {
			return Step{Result: contracts.WorkspaceCommandResultFalse{Message: "That thread is still starting."}}, true
		}
	}
	return Step{}, false
}

// unsnoozed is a thread written to, which ends its snooze if it had one.
func unsnoozed(edit ThreadsEdit, thread *domain.Thread) ThreadsEdit {
	if thread.SnoozedUntil == nil {
		return edit
	}
	return UnsnoozedIn(edit, thread.ID)
}

// waitingQuestion is the question a run is waiting on with this address, or nil.
func waitingQuestion(active ActiveRun, requestID, questionID string) *domain.PendingQuestion {
	record := RunRecord(active)
	if record == nil {
		return nil
	}
	for _, question := range record.Questions {
		if question.RequestID == requestID && question.QuestionID == questionID {
			return question
		}
	}
	return nil
}

// withoutQueued is queued without the message with this id, as a new list.
func withoutQueued(queued []*QueuedMessage, messageID string) []*QueuedMessage {
	kept := []*QueuedMessage{}
	for _, item := range queued {
		if item.ID != messageID {
			kept = append(kept, item)
		}
	}
	return kept
}
