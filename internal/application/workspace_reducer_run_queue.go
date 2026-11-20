package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func WithPending(state *WorkspaceState, pending *PendingRun) *WorkspaceState {
	return Afterward(state, SliceTransition(PendingStep(state, pending)))
}

// PendingStep is a run on its way, which takes down the error a run before it left.
func PendingStep(state *WorkspaceState, pending *PendingRun) Step {
	return Step{Edits: []OwnerEdit{PendingIn(RunsOf(state), pending)}, ClearsError: true}
}

func WithoutPending(state *WorkspaceState, pendingID string) *WorkspaceState {
	next := *state
	next.PendingRuns = runsWithoutKey(state.PendingRuns, pendingID)
	return &next
}

// WithUnsent drops a run before its message reached the thread. A decision that message answers is open again.
func WithUnsent(state *WorkspaceState, pending *PendingRun) *WorkspaceState {
	return Afterward(state, SliceTransition(Unsent(state, pending)))
}

// Unsent is WithUnsent as the edits it makes. A queue whose next message never started has no run
// left to send it, so what the user wrote goes back to the composer.
func Unsent(state *WorkspaceState, pending *PendingRun) Step {
	runs := WithoutPendingIn(RunsOf(state), pending.ID)
	dropped := Step{Edits: []OwnerEdit{runs}}
	if pending.Answers != nil && pending.TaskID != nil {
		dropped.Edits = append(dropped.Edits, AnswerReopenedIn(ThreadsOf(state), *pending.TaskID, *pending.Answers))
	}
	if len(pending.QueuedIDs) == 0 || pending.TaskID == nil {
		return dropped
	}
	taskID := *pending.TaskID
	return AndThen(state, dropped, func(next *WorkspaceState) Step {
		if next.ActiveRuns[taskID] != nil {
			return Step{}
		}
		return Drained(next, taskID, QueueIntentHandBack)
	})
}

// QueuedFor is what waits behind a thread's run. The slice is the state's own: callers must not change it.
func QueuedFor(state *WorkspaceState, taskID string) []*QueuedMessage {
	return state.QueuedMessages[taskID]
}

func WithQueued(state *WorkspaceState, taskID string, messages []*QueuedMessage) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{QueuedIn(RunsOf(state), taskID, messages)}})
}

// SentPrompt flattens everything drafted alongside the text into the one prompt the agent reads.
func SentPrompt(text string, pastes []*domain.PastedText, annotations []*domain.Annotation, attachments []*domain.RunAttachment, files []*domain.AttachedFile) string {
	return PromptWithFiles(PromptWithAttachments(PromptWithAnnotations(PromptWithPastes(text, pastes), annotations), attachments), files)
}

// SideChatPrompt gives each side-chat send the parent's app state, not copied recovery notices.
func SideChatPrompt(state *WorkspaceState, taskID string, prompt string) string {
	if strings.HasPrefix(runsJSTrimStart(prompt), "/") {
		return prompt
	}
	var chat *SideChat
	for _, item := range state.SideChats {
		if item.ID == taskID {
			chat = item
			break
		}
	}
	if chat == nil {
		return prompt
	}
	parent := runThreadOf(state.Threads, chat.SourceThreadID)
	var working []*domain.Subagent
	for _, agent := range state.Subagents[chat.SourceThreadID] {
		if agent.Status == domain.SubagentStatusWorking {
			working = append(working, agent)
		}
	}
	status := parentTaskStatus{TaskID: chat.SourceThreadID, CapturedAt: Now(), Status: "unknown"}
	if parent != nil {
		status.Title = new(parent.Title)
		if record := RunRecord(RunOf(state, parent.ID)); record != nil {
			status.Status = string(record.Status)
		} else if ThreadBusy(state, parent.ID) {
			status.Status = string(ThreadRunStatusRunning)
		} else {
			status.Status = string(RunStatus(state, &parent.ID))
		}
		status.LastRunOutcome = jsonx.Null[domain.ThreadOutcome]()
		if outcome, ok := domain.LastRunOutcome(parent); ok {
			status.LastRunOutcome = jsonx.Some(outcome)
		}
		status.WorkingAgents = new(float64(len(working)))
		status.WorkingAgentIDs = []string{}
		for _, agent := range working[:min(len(working), 16)] {
			status.WorkingAgentIDs = append(status.WorkingAgentIDs, agent.ID)
		}
	}
	encoded, err := jsonx.Marshal(status)
	if err != nil {
		panic(err)
	}
	return "Context for the side-chat question below (not an assignment):\n<parent-task-status>\n" + string(encoded) + "\n</parent-task-status>\nThis is the parent's state from the app when the question was sent. The helpers listed as working are running in the parent. The fork cannot resume those processes: any copied process-exited or lost-state notice describes that limitation of the fork, not a failure of the parent or its helpers. This status supplies no findings or failure cause; do not infer either from those notices.\n\n" + prompt
}

// WithoutStartedDraft clears what a started send carried from its composer; whatever was typed or added since stays.
func WithoutStartedDraft(state *WorkspaceState, draftKey string, pending *PendingRun) *WorkspaceState {
	typed := state.Prompts[draftKey]
	sent := typed
	if pending.DraftText != nil {
		sent = *pending.DraftText
	}
	prompted := WithPrompt(state, draftKey, TextAfterSend(typed, sent))
	var annotations []*domain.Annotation
	for _, annotation := range AnnotationsFor(state, draftKey) {
		if !slices.ContainsFunc(pending.Annotations, func(item *domain.Annotation) bool { return item.ID == annotation.ID && item.Note == annotation.Note }) {
			annotations = append(annotations, annotation)
		}
	}
	annotated := WithAnnotations(prompted, draftKey, runsNonNil(annotations))
	var pastes []*domain.PastedText
	for _, paste := range PastesFor(state, draftKey) {
		if !slices.ContainsFunc(pending.Pastes, func(item *domain.PastedText) bool { return item.ID == paste.ID }) {
			pastes = append(pastes, paste)
		}
	}
	pasted := WithPastes(annotated, draftKey, runsNonNil(pastes))
	var images []*domain.StagedImage
	for _, image := range ImagesFor(state, draftKey) {
		if !slices.Contains(pending.Attachments, image.Path) {
			images = append(images, image)
		}
	}
	imaged := WithImages(pasted, draftKey, runsNonNil(images))
	var files []*domain.AttachedFile
	for _, file := range FilesFor(state, draftKey) {
		if !slices.ContainsFunc(pending.Files, func(item *domain.AttachedFile) bool { return item.Path == file.Path }) {
			files = append(files, file)
		}
	}
	return WithFiles(imaged, draftKey, runsNonNil(files))
}

// RunModelOf is the model a run on this thread goes to, for a thread its start has already found one
// for. It panics when there is none, as the TypeScript throws.
func RunModelOf(state *WorkspaceState, thread *domain.Thread) domain.AgentModel {
	catalog := CatalogOf(state)
	model, ok := domain.ThreadModel(&catalog, thread)
	if !ok {
		panic("No model to run the thread " + thread.ID + " on.")
	}
	return model
}

// StartRunCommand is the command that starts a run on thread. policy is the thread's own
// (thread.ExecutionPolicy) unless a caller overrides it.
func StartRunCommand(state *WorkspaceState, thread *domain.Thread, runID string, prompt string, workspaceID string, policy domain.ExecutionPolicy) contracts.StartRunCommand {
	model := RunModelOf(state, thread)
	catalog := CatalogOf(state)
	coordination := CoordinationRole(state.Threads, thread)
	effort := domain.DefaultEffortFor(domain.EngineID(thread.Engine))
	if thread.Effort != nil {
		effort = *thread.Effort
	}
	capabilities := domain.CapabilitiesFor(domain.EngineID(thread.Engine))
	command := contracts.StartRunCommand{
		MemoryProtection: new(state.MemoryProtection),
		Channel:          contracts.RunChannelMain,
		TaskID:           thread.ID,
		Title:            thread.Title,
		RunID:            runID,
		Prompt:           SideChatPrompt(state, thread.ID, prompt),
		WorkspaceID:      workspaceID,
		Policy:           domain.PolicyFor(thread.Engine, policy),
		Engine:           thread.Engine,
		Model:            model,
		Effort:           domain.EffortForModel(&catalog, thread.Engine, model, effort, coordination),
	}
	if capabilities.FastMode {
		command.FastMode = new(runsIsTrue(thread.FastMode))
	}
	if state.ChromeBrowser && capabilities.ChromeBrowser {
		command.ChromeBrowser = new(true)
	}
	if !state.ComputerUse {
		command.ComputerUseTools = new(false)
	}
	if !state.BrowserTools {
		command.BrowserTools = new(false)
	}
	if _, available := state.Simulator.Availability.(domain.SimulatorAvailabilityAvailable); !available {
		command.SimulatorTools = new(false)
	}
	if thread.Continuation != nil {
		command.Continuation = thread.Continuation
		if runsIsTrue(thread.InheritedContinuation) {
			command.ForkContinuation = new(true)
		}
	}
	if coordination != "" {
		command.CoordinationRole = new(coordination)
	}
	if coordination == "member" {
		if project := ProjectFor(state, thread); project != nil && project.WorkspaceID != nil && *project.WorkspaceID != "" && *project.WorkspaceID != workspaceID {
			command.ProjectWorkspaceID = new(*project.WorkspaceID)
		}
	}
	return command
}

// FileThread mirrors archiving or restoring into the engine's own record of the thread.
func FileThread(state *WorkspaceState, thread *domain.Thread, archived bool) []WorkspaceEffect {
	if thread == nil {
		return []WorkspaceEffect{}
	}
	continuation := thread.Continuation
	if runsIsTrue(thread.InheritedContinuation) {
		continuation = nil
	}
	if continuation == nil && !(archived && state.ActiveRuns[thread.ID] != nil) {
		return []WorkspaceEffect{}
	}
	channel := contracts.RunChannelMain
	if side := SideChannelFor(state, thread); side.Channel != nil {
		channel = *side.Channel
	}
	return []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.ArchiveThreadCommand{TaskID: thread.ID, Channel: channel, Continuation: continuation, Archived: archived}}}
}

// SideChannel is the part of a StartRunCommand SideChannelFor sets; nil fields are left alone.
type SideChannel struct {
	Channel          *contracts.RunChannel
	Continuation     *domain.Continuation
	ForkContinuation *bool
}

// Apply is command with the channel's fields set over it.
func (side SideChannel) Apply(command contracts.StartRunCommand) contracts.StartRunCommand {
	if side.Channel != nil {
		command.Channel = *side.Channel
	}
	if side.Continuation != nil {
		command.Continuation = side.Continuation
	}
	if side.ForkContinuation != nil {
		command.ForkContinuation = side.ForkContinuation
	}
	return command
}

// SideChannelFor is where a side chat's run goes: its first turn forks the source thread; every turn
// after resumes its own branch. Empty for a thread that is not a side chat.
func SideChannelFor(state *WorkspaceState, thread *domain.Thread) SideChannel {
	if !slices.ContainsFunc(state.SideChats, func(chat *SideChat) bool { return chat.ID == thread.ID }) {
		return SideChannel{}
	}
	side := SideChannel{Channel: new(contracts.RunChannelSide)}
	if thread.Continuation != nil {
		return side
	}
	if continuation := ForkableContinuation(state, thread.ID); continuation != nil {
		side.Continuation = continuation
		side.ForkContinuation = new(true)
	}
	return side
}

// ForkableContinuation is the continuation a side chat starts from: its own once it has one, the
// source thread's before that. Nil for none.
func ForkableContinuation(state *WorkspaceState, taskID string) *domain.Continuation {
	var chat *SideChat
	for _, item := range state.SideChats {
		if item.ID == taskID {
			chat = item
			break
		}
	}
	if chat == nil {
		return nil
	}
	if thread := runThreadOf(state.Threads, taskID); thread != nil && thread.Continuation != nil {
		return thread.Continuation
	}
	if source := runThreadOf(state.Threads, chat.SourceThreadID); source != nil {
		return source.Continuation
	}
	return nil
}

// RunPickup is how a run picks a thread up when nobody has written to it; empty for a run someone
// wrote to.
type RunPickup string

const (
	// RunPickupWarming resumes a thread after its usage limit lifted.
	RunPickupWarming RunPickup = "warming"
	// RunPickupCarryingOn resumes the turn an update's restart cut off.
	RunPickupCarryingOn RunPickup = "carrying-on"
	// RunPickupAgent is a turn the agent opened itself.
	RunPickupAgent RunPickup = "agent"
)

// BeginRun records the run against the thread and marks it the thread's latest. provenance is
// ThreadRunStateAttendedRun unless the run is someone else's; before is nil to take the thread's mark now.
func BeginRun(state *WorkspaceState, taskID string, runID string, provenance RunProvenance, before *ThreadMark, pickup RunPickup) *WorkspaceState {
	withoutVerdict := domain.WithoutOutcome(state.Threads, jsonx.SetOf(taskID))
	threads := make([]*domain.Thread, len(withoutVerdict))
	for index, thread := range withoutVerdict {
		if thread.ID == taskID {
			threads[index] = OpenedTurn(pickedUp(thread, pickup), provenance)
		} else {
			threads[index] = thread
		}
	}
	messagesBefore := 0.0
	if thread := runThreadOf(threads, taskID); thread != nil {
		messagesBefore = float64(len(thread.Messages))
	}
	var mark ThreadMark
	if before != nil {
		mark = *before
	} else {
		mark = ThreadRunStateThreadMark(runThreadOf(state.Threads, taskID))
	}
	begun, answersStop := WithTurnBegun(state, taskID, pickup == RunPickupAgent, Now())
	run := ActiveRunRunning{
		TaskID:         taskID,
		RunID:          runID,
		Sequence:       0,
		Status:         ActiveRunRunningStatusRunning,
		Origin:         ActiveRunRunningOrigin(provenance.Origin),
		Quiet:          provenance.Quiet,
		Operation:      (*ActiveRunRunningOperation)(provenance.Operation),
		UsersOwn:       provenance.UsersOwn,
		Notified:       false,
		Acknowledged:   false,
		ReportedIssues: []string{},
		MessagesBefore: messagesBefore,
		Before:         mark,
	}
	if pickup == RunPickupWarming {
		run.Warming = new(true)
	}
	if pickup == RunPickupAgent {
		run.AgentInitiated = new(true)
	}
	if answersStop {
		run.AnswersStop = new(true)
	}
	next := *begun
	next.Threads = threads
	next.ActionError = nil
	next.LastRunIDs = runsWithKey(state.LastRunIDs, taskID, runID)
	return WithRunStatus(WithActiveRun(&next, taskID, run), taskID, ThreadRunStatusRunning)
}

func pickedUp(thread *domain.Thread, pickup RunPickup) *domain.Thread {
	rest := *thread
	rest.RunEndedAs = nil
	if reportStands(thread, pickup) {
		return domain.WithoutLimitPause(&rest)
	}
	return domain.WithoutLimitPause(domain.WithoutReport(&rest))
}

// reportStands reports whether what the turn before reported still stands through a run picked up this way.
func reportStands(thread *domain.Thread, pickup RunPickup) bool {
	return pickup == RunPickupWarming || pickup == RunPickupCarryingOn ||
		pickup == RunPickupAgent && thread.Report != nil && GroupOf[CoordinatedThreadStatus(thread.Report.State)] == domain.OverviewGroupNeeds
}

// WithAttendedRun makes a scheduled run the human who joined it owns from then on.
func WithAttendedRun(state *WorkspaceState, taskID string) *WorkspaceState {
	return Afterward(state, SliceTransition(RunQueueAttendedRun(state, taskID)))
}

// RunQueueAttendedRun is WithAttendedRun as the edits it makes.
func RunQueueAttendedRun(state *WorkspaceState, taskID string) Step {
	active := state.ActiveRuns[taskID]
	record := RunRecord(active)
	if record == nil || record.Origin == ActiveRunRunningOriginComposer {
		return Step{}
	}
	provenance := RunProvenance{Origin: RunProvenanceOriginComposer, Quiet: record.Quiet, Operation: (*RunProvenanceOperation)(record.Operation), UsersOwn: record.UsersOwn}
	return Step{Edits: []OwnerEdit{AttendedIn(RunsOf(state), taskID), TurnOpenedIn(ThreadsOf(state), taskID, provenance)}}
}

// WithDeliveredMessage moves a steered message from the queue into the thread.
func WithDeliveredMessage(state *WorkspaceState, taskID string, messageID string) *WorkspaceState {
	queued := QueuedFor(state, taskID)
	index := slices.IndexFunc(queued, func(message *QueuedMessage) bool { return message.ID == messageID })
	if index == -1 {
		return state
	}
	delivered := queued[index]
	joined := state
	if active := state.ActiveRuns[taskID]; active != nil && runsIsTrue(RunRecord(active).UsersOwn) && !runsIsTrue(delivered.UsersOwn) {
		joined = WithActiveRun(state, taskID, withoutUsersOwn(active))
	}
	rest := []*QueuedMessage{}
	for _, message := range queued {
		if message.ID != messageID {
			rest = append(rest, message)
		}
	}
	return UpdateThread(WithAttendedRun(WithQueued(joined, taskID, rest), taskID), taskID, func(thread *domain.Thread) *domain.Thread {
		return runsWithMessages(thread, slices.Concat(thread.Messages, []*domain.ConversationMessage{domain.CreateSentMessage(sentMessageOf(&delivered.HeldMessage))}))
	})
}

func withoutUsersOwn(run ActiveRun) ActiveRun {
	return EditRun(run, func(record *ActiveRunRunning) { record.UsersOwn = nil })
}

// WithSteeringFailure keeps a rejected steer queued, with its controls restored and the error on its own surface.
func WithSteeringFailure(state *WorkspaceState, taskID string, messageID string, message string) *WorkspaceState {
	queued := QueuedFor(state, taskID)
	if !slices.ContainsFunc(queued, func(item *QueuedMessage) bool { return item.ID == messageID && runsIsTrue(item.Steering) }) {
		return state
	}
	restored := make([]*QueuedMessage, len(queued))
	for index, item := range queued {
		if item.ID == messageID {
			unlocked := *item
			unlocked.Steering = new(false)
			restored[index] = &unlocked
		} else {
			restored[index] = item
		}
	}
	next := WithQueued(state, taskID, restored)
	if slices.ContainsFunc(next.SideChats, func(chat *SideChat) bool { return chat.ID == taskID }) {
		return WithSideChat(next, taskID, func(chat *SideChat) *SideChat {
			failed := *chat
			failed.Error = new(message)
			return &failed
		})
	}
	copied := *next
	copied.ActionError = new(message)
	copied.ActionErrorPage = nil
	return &copied
}

// Cancelling is the user stopping a thread's run.
func Cancelling(state *WorkspaceState, active ActiveRun) Step {
	record := RunRecord(active)
	return Step{
		Edits:   []OwnerEdit{CancellingIn(RunsOf(state), record.TaskID)},
		Effects: []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.CancelRunCommand{TaskID: record.TaskID, RunID: record.RunID}}},
	}
}

func StoppingProcess(state *WorkspaceState, taskID string, processID string) WorkspaceTransition {
	return Applied(state, SliceTransition(ProcessStopped(state, taskID, processID)))
}

// ProcessStopped is StoppingProcess as the edits it makes.
func ProcessStopped(state *WorkspaceState, taskID string, processID string) Step {
	marked, ok := StopAskedIn(RunsOf(state), taskID, processID)
	if !ok {
		return Step{}
	}
	return Step{Edits: []OwnerEdit{marked}, Effects: []WorkspaceEffect{WorkspaceEffectSendRunCommand{Command: contracts.StopProcessCommand{TaskID: taskID, ProcessID: processID}}}}
}

// HandedBack puts queued messages back in the thread's composer. The caller has already taken them out of the queue.
func HandedBack(state *WorkspaceState, taskID string, messages []*QueuedMessage) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{HandedBackIn(ComposerOf(state), taskID, runsHeldMessages(messages))}})
}

// WithUndelivered settles queued messages that will never reach the thread. The caller has already taken them out of the queue.
func WithUndelivered(state *WorkspaceState, taskID string, messages []*QueuedMessage) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{UndeliveredIn(ThreadsOf(state), taskID, messages)}})
}

// UndeliveredIn is WithUndelivered on the threads an input is changing.
func UndeliveredIn(edit ThreadsEdit, taskID string, messages []*QueuedMessage) ThreadsEdit {
	recipient := runThreadOf(edit.View.Threads, taskID)
	if recipient == nil {
		return edit
	}
	next := edit
	for _, message := range messages {
		if message.Answers != nil {
			next = AnswerReopenedIn(next, taskID, *message.Answers)
		}
		origin, ok := message.Origin.(domain.MessageOriginThread)
		if !ok || origin.Computer != nil || origin.ThreadID == nil {
			continue
		}
		if sender := runThreadOf(next.View.Threads, *origin.ThreadID); sender != nil && domain.IsCoordinator(sender) {
			next = NotedIn(next, sender.ID, UndeliveredNote(recipient, message.Text, Now()))
		}
	}
	return next
}

// Drained hands a thread's queue on as intent says.
func Drained(state *WorkspaceState, taskID string, intent QueueIntent) Step {
	queued := QueuedFor(state, taskID)
	if len(queued) == 0 {
		return Step{}
	}
	thread := runThreadOf(state.Threads, taskID)
	starting := []*QueuedMessage{}
	for _, message := range queued {
		if DrainingQueued(state, taskID, message.ID) {
			starting = append(starting, message)
		}
	}
	if intent == QueueIntentHandBack || thread != nil && thread.ArchivedAt != nil {
		var own, others []*QueuedMessage
		for _, message := range queued {
			if slices.Contains(starting, message) {
				continue
			}
			if domain.IsUsersOwn(message.Origin, message.Detail, message.Answers) {
				own = append(own, message)
			} else {
				others = append(others, message)
			}
		}
		edits := []OwnerEdit{QueuedIn(RunsOf(state), taskID, starting), UndeliveredIn(ThreadsOf(state), taskID, others)}
		if len(own) > 0 {
			edits = append(edits, HandedBackIn(ComposerOf(state), taskID, runsHeldMessages(own)))
		}
		return Step{Edits: edits}
	}
	if thread == nil {
		return Step{Edits: []OwnerEdit{QueuedIn(RunsOf(state), taskID, []*QueuedMessage{})}}
	}
	if len(starting) > 0 || thread.LimitPause != nil || thread.ResourceStop != nil || slices.Contains(state.CreatingWorktrees, taskID) {
		return Step{}
	}
	next := queued[0]
	origin := next.Origin
	if origin == nil && runsTruthy(next.Detail) {
		origin = domain.LegacyOrigin(*next.Detail)
	}
	project := ProjectFor(state, thread)
	pending := &PendingRun{
		ID:            NewID(),
		RunID:         NewID(),
		Origin:        PendingRunOriginComposer,
		TaskID:        new(taskID),
		Text:          next.Text,
		Prompt:        next.Prompt,
		Attachments:   next.Attachments,
		Annotations:   next.Annotations,
		Pastes:        next.Pastes,
		Files:         next.Files,
		MessageOrigin: origin,
		QueuedIDs:     []string{next.ID},
	}
	if project != nil {
		pending.ProjectID = new(project.ID)
	}
	if runsTruthy(next.Answers) {
		pending.Answers = next.Answers
	}
	if runsIsTrue(next.UsersOwn) {
		pending.UsersOwn = new(true)
	}
	if intent == QueueIntentResume {
		pending.Warming = new(true)
	}
	step := PendingStep(state, pending)
	step.Effects = []WorkspaceEffect{ResolveWorkspaceEffect(pending.ID, thread, project, WorktreeFor(state, thread), false, nil)}
	return step
}

// ResolveWorkspaceEffect is where a run happens. thread, project, worktree and branch may be nil.
func ResolveWorkspaceEffect(pendingID string, thread *domain.Thread, project *domain.Project, worktree *domain.Worktree, wantsWorktree bool, branch *DraftBranch) WorkspaceEffectResolveRunWorkspace {
	effect := WorkspaceEffectResolveRunWorkspace{PendingID: pendingID}
	if worktree != nil {
		effect.Workspace = &domain.WorkspaceRecord{ID: worktree.WorkspaceID, Kind: domain.WorkspaceKindWorktree, Root: worktree.Root}
		return effect
	}
	projectWorkspace := ""
	if project != nil && project.WorkspaceID != nil {
		projectWorkspace = *project.WorkspaceID
	}
	if branch != nil && branch.Create && projectWorkspace != "" {
		effect.CreateBranch = &WorkspaceEffectResolveRunWorkspaceCreateBranch{WorkspaceID: projectWorkspace, Branch: branch.Name}
	}
	if wantsWorktree && projectWorkspace != "" {
		effect.Root = new(project.Root)
		effect.CreateWorktree = &WorkspaceEffectResolveRunWorkspaceCreateWorktree{ProjectRoot: project.Root, CarryChanges: thread != nil}
		if branch != nil {
			effect.CreateWorktree.Branch = new(branch.Name)
		}
		return effect
	}
	effect.Picker = project != nil && projectWorkspace == ""
	if projectWorkspace != "" {
		effect.Workspace = &domain.WorkspaceRecord{ID: projectWorkspace, Kind: domain.WorkspaceKindProject, Root: project.Root}
	}
	if project != nil {
		effect.Root = new(project.Root)
	}
	if branch != nil && projectWorkspace != "" {
		effect.Checkout = &WorkspaceEffectResolveRunWorkspaceCheckout{WorkspaceID: projectWorkspace, Branch: branch.Name}
	}
	return effect
}

func WithSideChat(state *WorkspaceState, chatID string, update func(chat *SideChat) *SideChat) *WorkspaceState {
	chats := make([]*SideChat, len(state.SideChats))
	for index, chat := range state.SideChats {
		if chat.ID == chatID {
			chats[index] = update(chat)
		} else {
			chats[index] = chat
		}
	}
	next := *state
	next.SideChats = chats
	return &next
}

// parentTaskStatus is what a side chat's send says of the thread it forks, keys in the order the prompt carries them.
type parentTaskStatus struct {
	TaskID          string                          `json:"taskId"`
	CapturedAt      float64                         `json:"capturedAt"`
	Title           *string                         `json:"title,omitzero"`
	Status          string                          `json:"status"`
	LastRunOutcome  jsonx.Opt[domain.ThreadOutcome] `json:"lastRunOutcome,omitzero"`
	WorkingAgents   *float64                        `json:"workingAgents,omitzero"`
	WorkingAgentIDs []string                        `json:"workingAgentIds,omitzero"`
}

// runsJSTrimStart trims the start of s as JavaScript's String.prototype.trimStart does.
func runsJSTrimStart(s string) string {
	return strings.TrimLeftFunc(s, runsJSSpace)
}

// runsNonNil is items, or an empty list for none.
func runsNonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}

// runsHeldMessages is queued messages as the composer takes them back, without their steering lock.
func runsHeldMessages(messages []*QueuedMessage) []*domain.HeldMessage {
	held := make([]*domain.HeldMessage, len(messages))
	for index, message := range messages {
		held[index] = new(message.HeldMessage)
	}
	return held
}

// sentMessageOf is a held message as createSentMessage takes it.
func sentMessageOf(message *domain.HeldMessage) domain.SentMessage {
	return domain.SentMessage{
		Text:        message.Text,
		Origin:      message.Origin,
		Attachments: message.Attachments,
		Annotations: message.Annotations,
		Pastes:      message.Pastes,
		Files:       message.Files,
	}
}
