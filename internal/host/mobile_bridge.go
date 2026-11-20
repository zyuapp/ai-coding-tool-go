package host

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// MobileRefused is what a phone is refused with when it sends something outside the surface open to it.
const MobileRefused = "That command is not one a phone may send."

// MobileQueuedNotWords is what a phone is refused with when it asks to edit a queued message carrying more than words.
const MobileQueuedNotWords = "A message with images, files, pasted text or notes can only be edited on the computer."

// MobileNoCheckout is what a phone is refused with when it asks to review a thread that has no checkout.
const MobileNoCheckout = "This thread has no checkout to review."

// answerQuery answers a phone's read. A review query names a thread, and the thread names the
// checkout: a phone never says which directory to read. Whitespace-only changes are left out, as the
// desktop's review leaves them out by default. A folder query lists folders by path prefix, as the
// desktop's own add-project field does. A thread query reads a thread the phone may name without
// opening it, one on a paired computer from what that computer last published here. A history query
// reads older messages of a thread from the same place. An image query reads only an image the named
// reply links. An archived query lists what the Archive settings page lists.
func answerQuery(ctx context.Context, host MobileBridgeHost, query contracts.MobileQuery) (any, error) {
	if _, archived := query.(contracts.MobileQueryArchived); archived {
		return application.MobileArchivedThreads(stateOf(host)), nil
	}
	var taskID string
	var read func(state *application.WorkspaceState) (any, bool)
	switch query := query.(type) {
	case contracts.MobileQueryThread:
		taskID = query.TaskID
		read = func(state *application.WorkspaceState) (any, bool) {
			view := application.MobileThreadRead(state, query.TaskID)
			return view, view != nil
		}
	case contracts.MobileQueryHistory:
		taskID = query.TaskID
		read = func(state *application.WorkspaceState) (any, bool) {
			view := application.MobileThreadHistory(state, query.TaskID, jsInt(query.Before))
			return view, view != nil
		}
	}
	if read != nil {
		missing := fmt.Errorf("No thread has the ID %s.", taskID)
		if remote := application.RemoteMemberHolder(stateOf(host), taskID); remote != nil && remote.State != nil {
			if view, ok := read(remote.State); ok {
				return view, nil
			}
			return nil, missing
		}
		if findThread(stateOf(host).Threads, taskID) == nil {
			return nil, missing
		}
		if host.Hydrate != nil {
			if err := host.Hydrate(ctx, taskID); err != nil {
				return nil, err
			}
		}
		if view, ok := read(stateOf(host)); ok {
			return view, nil
		}
		return nil, missing
	}
	reader := host.Read
	if reader == nil {
		return nil, errors.New("This computer cannot answer that.")
	}
	var review contracts.MobileQuery
	switch query := query.(type) {
	case contracts.MobileQueryTranscribe:
		text, err := reader.TranscribeAudio(ctx, query.Audio)
		if err != nil {
			return nil, err
		}
		return struct {
			Text string `json:"text"`
		}{text}, nil
	case contracts.MobileQueryImage:
		return linkedImage(ctx, host, reader, query)
	case contracts.MobileQueryCommands:
		return composerCommands(ctx, host, reader, query.TaskID)
	case contracts.MobileQueryDiffSummary:
		taskID, review = query.TaskID, query
	case contracts.MobileQueryDiffPatch:
		taskID, review = query.TaskID, query
	case contracts.ComputerQueryDirectories:
		return reader.Directories(ctx, query.Prefix, nil)
	default:
		return nil, errors.New(MobileRefused)
	}
	state := stateOf(host)
	thread := findThread(state.Threads, taskID)
	if thread == nil {
		return nil, fmt.Errorf("No thread has the ID %s.", taskID)
	}
	workspaceID, ok := application.ThreadWorkspaceID(state, thread)
	if !ok || workspaceID == "" {
		return nil, errors.New(MobileNoCheckout)
	}
	ignoreWhitespace := new(true)
	if query, summary := review.(contracts.MobileQueryDiffSummary); summary {
		return reader.DiffSummary(ctx, workspaceID, query.Range, ignoreWhitespace)
	}
	patch := review.(contracts.MobileQueryDiffPatch)
	return reader.DiffPatch(ctx, workspaceID, patch.Range, patch.Path, patch.PreviousPath, ignoreWhitespace)
}

// stateOf is the state a phone request reads, as of now.
func stateOf(host MobileBridgeHost) *application.WorkspaceState {
	state := host.State()
	return &state
}

// MobileNoCommands is what a phone is told for a paired computer's thread in no project, whose
// projectless checkout only that computer can name.
const MobileNoCommands = "That computer does not say what this thread's commands are."

// composerCommands reads the "/" menu the desktop's composer would show: the engine's commands in the
// thread's checkout, read on the computer holding it, or in the draft's project. One in no project
// reads the projectless checkout. A nil taskID is the new thread's draft.
func composerCommands(ctx context.Context, host MobileBridgeHost, reader MobileBridgeReader, taskID *string) (contracts.CommandDiscoveryResult, error) {
	state := stateOf(host)
	var remote *application.WorkspaceState
	if taskID != nil {
		if holder := application.RemoteMemberHolder(state, *taskID); holder != nil {
			remote = holder.State
		}
	}
	holder := state
	if remote != nil {
		holder = remote
	}
	var thread *domain.Thread
	if taskID != nil {
		if thread = findThread(holder.Threads, *taskID); thread == nil {
			return nil, fmt.Errorf("No thread has the ID %s.", *taskID)
		}
	}
	engine := state.DraftEngine
	var workspaceID string
	if thread != nil {
		engine = thread.Engine
		workspaceID, _ = application.ThreadWorkspaceID(holder, thread)
	} else if state.DraftProjectID != nil {
		for _, project := range state.Projects {
			if project.ID == *state.DraftProjectID {
				if project.WorkspaceID != nil {
					workspaceID = *project.WorkspaceID
				}
				break
			}
		}
	}
	if workspaceID != "" {
		return reader.Commands(ctx, workspaceID, engine)
	}
	if remote != nil {
		return contracts.BranchesResultError{Message: MobileNoCommands}, nil
	}
	projectless, err := reader.ProjectlessWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	return reader.Commands(ctx, projectless.ID, engine)
}

// linkedImage reads an image a reply links. One in a reply of a thread on a paired computer is read
// there, from that computer's checkout.
func linkedImage(ctx context.Context, host MobileBridgeHost, reader MobileBridgeReader, query contracts.MobileQueryImage) (*contracts.FilePortRuntimeReadMessageImageResult, error) {
	var remote *application.WorkspaceState
	if holder := application.RemoteMemberHolder(stateOf(host), query.TaskID); holder != nil {
		remote = holder.State
	}
	if remote == nil && findThread(stateOf(host).Threads, query.TaskID) == nil {
		return nil, fmt.Errorf("No thread has the ID %s.", query.TaskID)
	}
	if remote == nil && host.Hydrate != nil {
		if err := host.Hydrate(ctx, query.TaskID); err != nil {
			return nil, err
		}
	}
	state := remote
	if state == nil {
		state = stateOf(host)
	}
	thread := findThread(state.Threads, query.TaskID)
	var message *domain.ConversationMessage
	if thread != nil {
		for _, item := range thread.Messages {
			if item.ID == query.MessageID {
				message = item
				break
			}
		}
	}
	file, linked := domain.MessageImagePath(query.Href)
	if thread == nil || message == nil || message.Kind != domain.ConversationMessageKindAssistant || !linked || !linksImage(message.Text, file) {
		return nil, errors.New("That reply links no such image.")
	}
	root, _ := application.ThreadWorkspaceRoot(state, thread)
	return reader.ReadMessageImage(ctx, file, root, message.ID, query.Preview != nil && *query.Preview, thread.ID)
}

func linksImage(text, file string) bool {
	for _, image := range application.MessageImages(text) {
		if image.Path == file {
			return true
		}
	}
	return false
}

// queuedEdit is what editedQueued takes out of a queue: the drop that takes it, and its words.
type queuedEdit struct {
	Drop   contracts.AppCommand
	TaskID string
	Text   string
}

// editedQueued takes a queued message's words back to a phone's own composer, so the computer's
// composer never holds them: the message leaves the queue of whichever computer holds the thread, or
// the phone is told why it cannot (refusal, with a nil edit).
func editedQueued(state *application.WorkspaceState, command contracts.TaskCommandTaskEditQueued) (edit *queuedEdit, refusal string) {
	if command.TaskID == nil {
		return nil, application.QueuedSent
	}
	taskID := *command.TaskID
	remote := application.RemoteMemberHolder(state, taskID)
	if remote != nil && !application.SaysQueuedGone(remote) {
		return nil, contracts.RemoteUnsupported
	}
	holder := state
	if remote != nil && remote.State != nil {
		holder = remote.State
	}
	message, refusal := application.EditableQueued(holder.QueuedMessages[taskID], command.MessageID)
	if message == nil {
		return nil, refusal
	}
	if application.DrainingQueued(holder, taskID, message.ID) {
		return nil, application.QueuedSent
	}
	if !application.PhoneEditsQueued(message) {
		return nil, MobileQueuedNotWords
	}
	drop := contracts.TaskCommandTaskDropQueued{TaskID: &taskID, MessageID: command.MessageID, IfWaiting: new(true)}
	return &queuedEdit{Drop: drop, TaskID: taskID, Text: message.Text}, ""
}

// MobileEditWaitMs is how long a phone's edit waits for the computer holding the queue, well inside the relay's own deadline.
const MobileEditWaitMs = 5_000

// MobileEditMarginMs is kept back from the relay's deadline, so an edit's answer reaches the phone before the relay gives up on it.
const MobileEditMarginMs = 1_500

const MobileEditTooLate = "Your computer did not answer in time. Try again."

const MobileEditLate = "That computer has not answered yet. If it takes the message out, its words wait in this computer's composer."

// answerEdit tells the phone the edit went through only once the computer holding the queue says the
// message left it. A line that drops can still carry the drop minutes later, so a holder that has not
// answered in time leaves the phone told so, and the words of a drop it takes later wait in this
// computer's composer for the thread rather than being lost. Every answer is given before the server
// stops waiting at answerBy (ms since the epoch; nil waits without a deadline), and one that reaches
// this window too late takes nothing out.
func answerEdit(ctx context.Context, host MobileBridgeHost, command contracts.TaskCommandTaskEditQueued, answerBy *float64) (contracts.WorkspaceCommandResult, error) {
	wait := float64(MobileEditWaitMs)
	if answerBy != nil {
		wait = min(wait, *answerBy-application.Now()-MobileEditMarginMs)
	}
	if wait <= 0 {
		return contracts.WorkspaceCommandResultFalse{Message: MobileEditTooLate}, nil
	}
	edited, refusal := editedQueued(stateOf(host), command)
	if edited == nil {
		return contracts.WorkspaceCommandResultFalse{Message: refusal}, nil
	}
	execution := host.Execute(edited.Drop)
	type settled struct {
		result contracts.WorkspaceCommandResult
		err    error
	}
	outcome := make(chan settled, 1)
	// Acceptance can itself wait, behind a read of the thread's history, so the wait covers it too.
	go func() {
		accepted, err := execution.Accepted(background)
		if err == nil && isOK(accepted) {
			accepted, err = execution.Completed(background)
		}
		outcome <- settled{accepted, err}
	}()
	late := time.NewTimer(time.Duration(wait * float64(time.Millisecond)))
	defer late.Stop()
	select {
	case answer := <-outcome:
		return answer.result, answer.err
	case <-late.C:
	}
	go func() {
		answer := <-outcome
		if answer.err != nil || !isOK(answer.result) {
			return
		}
		typed := stateOf(host).Prompts[edited.TaskID]
		prompt := edited.Text
		if prompt != "" && typed != "" {
			prompt += "\n\n" + typed
		} else if typed != "" {
			prompt = typed
		}
		host.Execute(contracts.ViewCommandViewSetPrompt{TaskID: &edited.TaskID, Prompt: prompt})
	}()
	return contracts.WorkspaceCommandResultFalse{Message: MobileEditLate}, nil
}

// isOK says whether a result took.
func isOK(result contracts.WorkspaceCommandResult) bool {
	_, refused := result.(contracts.WorkspaceCommandResultFalse)
	return !refused
}

// MobileDraftBusy is what a phone is refused with when it sends from the new thread's draft before the last send from there has started.
const MobileDraftBusy = "The last message from this new thread is still on its way. Send again once its thread opens."

// touchesDraft reports whether a phone's command writes or sends the new thread's draft, which the computer has open.
func touchesDraft(state *application.WorkspaceState, command contracts.AppCommand) bool {
	if application.PromptKey(state) != application.NewThreadDraft {
		return false
	}
	switch command := command.(type) {
	case contracts.ViewCommandViewSetPrompt:
		return command.TaskID == nil
	case contracts.TaskCommandAttachmentsSend:
		return command.TaskID == nil
	case contracts.TaskCommandTaskSend:
		return command.TaskID == nil && command.Text == nil
	}
	return false
}

// draftSending reports whether a send from the new thread's draft has yet to start: its images are
// being written, or its run is starting.
func draftSending(state *application.WorkspaceState) bool {
	if application.AttachmentSendFor(state.AttachmentSends, nil).Busy {
		return true
	}
	for _, pending := range state.PendingRuns {
		if pending.DraftKey != nil && *pending.DraftKey == application.NewThreadDraft {
			return true
		}
	}
	return false
}

// phoneCommands turns a phone's command into what the computer runs. A phone's images leave its
// composer at once, so a send to a thread carries its words, even none. One from the new thread's
// draft goes as the composer's own draft send does: its words become the draft's, so the thread starts
// where the draft points and opens once it runs.
func phoneCommands(state *application.WorkspaceState, command contracts.AppCommand) []contracts.AppCommand {
	send, ok := command.(contracts.TaskCommandAttachmentsSend)
	if !ok {
		return []contracts.AppCommand{command}
	}
	if send.TaskID == nil && application.PromptKey(state) == application.NewThreadDraft {
		prompt := ""
		if send.Text != nil {
			prompt = *send.Text
		}
		send.Text = nil
		return []contracts.AppCommand{contracts.ViewCommandViewSetPrompt{Prompt: prompt}, send}
	}
	if send.Text == nil {
		send.Text = new("")
	}
	return []contracts.AppCommand{send}
}

// AnswerMobileRequest answers one phone request: each phone reads a projection of runtime state and
// writes through the shared command path, its commands validated at this boundary first. It never
// fails; a failure is the response.
func AnswerMobileRequest(ctx context.Context, host MobileBridgeHost, request contracts.MobileRequest) contracts.MobileResponse {
	requestID := mobileRequestID(request)
	failed := func(message string) contracts.MobileResponse {
		return contracts.MobileResponseFalse{RequestID: requestID, Message: message}
	}
	result, refusal, err := answerMobile(ctx, host, request)
	if err != nil {
		return failed(errorMessage(err))
	}
	if refusal != "" {
		return failed(refusal)
	}
	raw, err := jsonx.Marshal(result)
	if err != nil {
		return failed(errorMessage(err))
	}
	return contracts.MobileResponseTrue{RequestID: requestID, Result: raw}
}

func mobileRequestID(request contracts.MobileRequest) string {
	switch request := request.(type) {
	case contracts.MobileRequestSnapshot:
		return request.RequestID
	case contracts.MobileRequestCommand:
		return request.RequestID
	case contracts.MobileRequestQuery:
		return request.RequestID
	}
	return ""
}

func answerMobile(ctx context.Context, host MobileBridgeHost, request contracts.MobileRequest) (result any, refusal string, err error) {
	switch request := request.(type) {
	case contracts.MobileRequestSnapshot:
		return application.ProjectMobileView(stateOf(host), application.Now()), "", nil
	case contracts.MobileRequestQuery:
		if request.Query == nil || !contracts.IsMobileQuery(plain(request.Query)) {
			return nil, MobileRefused, nil
		}
		answer, err := answerQuery(ctx, host, request.Query)
		return answer, "", err
	case contracts.MobileRequestCommand:
		// What a phone may send, and then every field as the window's own checks read it.
		raw := plain(request.Command)
		if request.Command == nil || !contracts.IsPhoneCommand(raw) || !contracts.IsWorkspaceViewInput(raw) {
			return nil, MobileRefused, nil
		}
		command, ok := request.Command.(contracts.AppCommand)
		if !ok {
			return nil, MobileRefused, nil
		}
		if taskID := commandTaskID(command); taskID != nil && !application.PhoneReachesThread(stateOf(host), *taskID) {
			return nil, fmt.Sprintf("No thread has the ID %s.", *taskID), nil
		}
		// The draft holds one send's words, so nothing may rewrite or send it until the last send from it has started.
		if touchesDraft(stateOf(host), command) && draftSending(stateOf(host)) {
			return nil, MobileDraftBusy, nil
		}
		if edit, editing := command.(contracts.TaskCommandTaskEditQueued); editing {
			edited, err := answerEdit(ctx, host, edit, request.AnswerBy)
			if err != nil {
				return nil, "", err
			}
			if refused, ok := edited.(contracts.WorkspaceCommandResultFalse); ok {
				return nil, refused.Message, nil
			}
			return application.ProjectMobileView(stateOf(host), application.Now()), "", nil
		}
		for _, command := range phoneCommands(stateOf(host), command) {
			accepted, err := host.Execute(command).Accepted(ctx)
			if err != nil {
				return nil, "", err
			}
			if refused, ok := accepted.(contracts.WorkspaceCommandResultFalse); ok {
				return nil, refused.Message, nil
			}
		}
		return application.ProjectMobileView(stateOf(host), application.Now()), "", nil
	}
	return nil, MobileRefused, nil
}

func NoMobileView() *MobileViewHolder { return &MobileViewHolder{} }

// NextMobileUpdate is what to push after a state change: nothing (nil) while no phone is connected, a
// whole view for the first change after one arrives, and the difference from then on.
func NextMobileUpdate(held *MobileViewHolder, state *application.WorkspaceState, at float64) contracts.MobileViewUpdate {
	if len(state.Remote.Sessions) == 0 {
		held.Current = nil
		return nil
	}
	view := application.ProjectMobileView(state, at)
	previous := held.Current
	if previous == nil {
		held.Current = view
		return contracts.MobileViewUpdateSnapshot{View: *view}
	}
	patch := application.DiffMobileView(previous, view)
	if patch == nil {
		return nil
	}
	if applied := application.ApplyMobilePatch(previous, patch); applied != nil {
		held.Current = applied
	} else {
		held.Current = view
	}
	return contracts.MobileViewUpdatePatch{Patch: *patch}
}

// RunRemoteEffect carries out one of the bridge's settings. Each answers with the whole bridge state,
// so the reducer never infers what a change did to it.
func RunRemoteEffect(ctx context.Context, effect RemoteEffect, desktop RuntimeDesktop) (*domain.MobileServerState, error) {
	switch effect := effect.(type) {
	case application.WorkspaceEffectRemoteSetEnabled:
		return desktop.SetMobileEnabled(ctx, effect.Enabled)
	case application.WorkspaceEffectRemoteCreatePairingCode:
		// The offer the QR draws is read back from the bridge state, which is all settings draws.
		if _, err := desktop.CreateMobilePairingCode(ctx); err != nil {
			return nil, err
		}
		return desktop.MobileState(ctx)
	case application.WorkspaceEffectRemoteRevokeDevice:
		return desktop.RevokeMobileDevice(ctx, effect.DeviceID)
	case application.WorkspaceEffectRemoteRefresh:
		return desktop.RefreshTailscale(ctx)
	}
	return nil, fmt.Errorf("unknown remote effect %T", effect)
}

// SubscribeToMobile listens to what main pushes about the bridge, and answers the phone messages it
// forwards. Returns how to stop.
func SubscribeToMobile(host SubscriptionHost) func() {
	desktop := host.Desktop
	go func() {
		remote, err := desktop.MobileState(background)
		if err != nil {
			host.send(failed(err))
			return
		}
		host.send(application.RemoteEvent{Remote: *remote})
	}()
	stopWatching := desktop.OnMobileState(func(remote domain.MobileServerState) {
		host.send(application.RemoteEvent{Remote: remote})
	})
	// A request is read as a stranger's even across the bridge, so a malformed one is never answered.
	answering := MobileBridgeHost{
		State:    func() application.WorkspaceState { return *host.State() },
		Dispatch: host.Dispatch,
		Execute: func(command contracts.AppCommand) application.WorkspaceExecution {
			return host.Execute(command.(application.WorkspaceInput))
		},
		Read:    desktop,
		Hydrate: host.Hydrate,
	}
	stopAnswering := desktop.OnMobileRequest(func(request contracts.MobileRequest) {
		if request == nil || !contracts.IsMobileRequest(plain(request)) {
			return
		}
		go func() {
			desktop.AnswerMobileRequest(AnswerMobileRequest(background, answering, request))
		}()
	})
	return func() {
		stopWatching()
		stopAnswering()
	}
}

// changeBridge changes one of the bridge's own settings; effect is a RemoteEffect.
func changeBridge(ctx context.Context, effect application.WorkspaceEffect, host EffectHost) error {
	remote, err := RunRemoteEffect(ctx, effect.(RemoteEffect), host.Desktop)
	if err == nil {
		err = host.Dispatch(ctx, application.RemoteEvent{Remote: *remote})
	}
	if err != nil {
		return host.Dispatch(ctx, failed(err))
	}
	return nil
}

var MobileHost = HostModule{
	Effects: EffectHandlers{
		"remote.set-enabled":         changeBridge,
		"remote.create-pairing-code": changeBridge,
		"remote.revoke-device":       changeBridge,
		"remote.refresh":             changeBridge,
	},
	Subscribe: func(host SubscriptionHost, _ RunReports) func() { return SubscribeToMobile(host) },
}
