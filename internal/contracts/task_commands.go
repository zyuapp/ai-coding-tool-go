package contracts

import (
	"encoding/json/jsontext"
	"regexp"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var policy = declLiteralsOf(domain.ExecutionPolicies)
var dropTarget = Object(Shape{{"projectId", NullableText}, {"index", Number}})
var runAttachment = Object(Shape{{"path", Text}, {"labels", Array(Text)}, {"context", Optional(declGuard(domain.IsScreenshotContext))}})
var remoteSender = Object(Shape{{"threadId", DeviceID}, {"title", Text}, {"computer", DeviceID}})
var remoteLead = Object(Shape{{"threadId", DeviceID}, {"title", Text}, {"computer", DeviceID}, {"proposalId", Optional(DeviceID)}})

var memoryText Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && declUTF16Len(s) <= domain.MaxMemoryPrompt
}

// promptText: the name a thread starts with holds words, and runs no longer than a suggestion's, which becomes one.
var promptText Validator = func(value jsontext.Value) bool {
	if !PhoneCommandsIsString(value, PhoneCommandsMaxPromptLength) {
		return false
	}
	s, _ := declString(value)
	return !declBlank(s)
}

var StartTitle Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && !declBlank(s) && declUTF16Len(s) <= domain.MaxProposalTitle
}

var proposalPlace = Object(Shape{{"project", Text}, {"worktree", Boolean}})

// proposalIds: the suggestions Start all or Dismiss all names, each as a single one is named, and never more than wait at once.
var proposalIds Validator = func(value jsontext.Value) bool {
	if !Array(Text)(value) {
		return false
	}
	items, _ := declArray(value)
	return len(items) > 0 && len(items) <= domain.MaxProposals
}

var imageAnnotation = Object(Shape{{"kind", Literals("box", "arrow")}, {"x", Number}, {"y", Number}, {"width", Number}, {"height", Number}, {"text", Text}})

// attachmentSource: image data URLs need the encoded byte budget plus room for their MIME prefix.
var attachmentSource Validator = func(value jsontext.Value) bool {
	s, ok := declString(value)
	return ok && declUTF16Len(s) <= domain.MaxAttachmentEncodedBytes+256
}

var outgoingAttachment = Object(Shape{{"id", Text}, {"source", attachmentSource}, {"annotations", Array(imageAnnotation)}, {"path", OptionalText}, {"context", Optional(declGuard(domain.IsScreenshotContext))}})

func isWorktreeDestination(value jsontext.Value) bool {
	record, ok := declRecord(value)
	if !ok {
		return false
	}
	if declIsKind(record, "local") || declIsKind(record, "new") {
		return true
	}
	return declIsKind(record, "worktree") && Text(record["id"])
}

// givesRole: only the window says which thread on another computer asked.
func givesRole(command map[string]jsontext.Value) bool {
	return CommandDeclarationNamesThread(command) && declUndefined(command["sender"])
}

// agentSend: where a thread starts is held to what a suggestion's place is, since a thread started from one starts the same way.
func agentSend(command map[string]jsontext.Value) bool {
	if !AgentID(command["taskId"]) {
		return false
	}
	// A project is named by its folder name, its path, or its id.
	if !declUndefined(command["project"]) && !PhoneCommandsIsString(command["project"], domain.MaxProposalProject) {
		return false
	}
	// An agent's send carries only its text, so a blank one would be answered as sent with nothing sent.
	if !PhoneCommandsIsString(command["text"], PhoneCommandsMaxPromptLength) {
		return false
	}
	if text, _ := declString(command["text"]); declBlank(text) {
		return false
	}
	// An id, never a path: the reducer resolves it against the checkouts the app itself made.
	return AgentID(command["worktreeId"]) && SendStaysInReach(command, SendCallerAgent)
}

// movesToCheckout: into a new checkout, one that already exists, or back to the project's own. Without a thread
// named it is the thread that asked, and only the window says whether a move waits for that thread's run to end or
// which thread on another computer asked for it.
func movesToCheckout(command map[string]jsontext.Value) bool {
	if !AgentID(command["taskId"]) || !declUndefined(command["afterRun"]) || !declUndefined(command["sender"]) {
		return false
	}
	destination, _ := declRecord(command["destination"])
	return !declIsKind(destination, "worktree") || PhoneCommandsIsString(destination["id"])
}

func isDestination(value jsontext.Value) bool {
	record, ok := declRecordOf(value)
	if !ok {
		return false
	}
	if declIsKind(record, "local") || declIsKind(record, "new") {
		return true
	}
	return declIsKind(record, "worktree") && PhoneCommandsIsString(record["id"])
}

func declOptionalBool(value jsontext.Value) bool {
	_, ok := declBool(value)
	return declUndefined(value) || ok
}

// sends: a phone sends a message and nothing else: never files, never the shape of a thread being started, which
// the draft's settings set, and never who the message is from or who the thread works under.
var sends PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) &&
		(declUndefined(command["project"]) || PhoneCommandsIsString(command["project"], PhoneCommandsMaxPathLength)) &&
		(declUndefined(command["text"]) || PhoneCommandsIsString(command["text"], PhoneCommandsMaxPromptLength)) &&
		declOptionalBool(command["steer"]) &&
		declOptionalBool(command["worktree"]) &&
		(declUndefined(command["worktreeId"]) || PhoneCommandsIsString(command["worktreeId"])) &&
		SendStaysInReach(command, SendCallerPhone)
}

var starts PhoneRule = func(command map[string]jsontext.Value) bool {
	return (declUndefined(command["projectId"]) || PhoneCommandsIsString(command["projectId"])) &&
		(declUndefined(command["worktreeId"]) || PhoneCommandsIsString(command["worktreeId"]))
}

var dismissesAll PhoneRule = func(command map[string]jsontext.Value) bool {
	return declOptionalBool(command["localOnly"]) &&
		(declUndefined(command["projectId"]) || PhoneCommandsIsString(command["projectId"]))
}

// taskCommandsMaxTitleLength is the longest title a phone may give a thread, which its rename field holds it to.
const taskCommandsMaxTitleLength = 1_000

var renames PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsBlankable(command["title"], taskCommandsMaxTitleLength)
}

var forks PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && declOptionalBool(command["worktree"])
}

var setsPolicy PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && declGuard(domain.IsExecutionPolicy)(command["policy"])
}

var setsModel PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && declGuard(domain.IsAgentEngine)(command["engine"]) &&
		(declUndefined(command["model"]) || declGuard(domain.IsModelID)(command["model"]))
}

var setsFastMode PhoneRule = func(command map[string]jsontext.Value) bool {
	_, ok := declBool(command["fastMode"])
	return PhoneCommandsNamed(command) && ok
}

var setsEffort PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && declGuard(domain.IsAgentEngine)(command["engine"]) && declGuard(domain.IsAgentEffort)(command["effort"])
}

var setsWorktree PhoneRule = func(command map[string]jsontext.Value) bool {
	_, ok := declBool(command["worktree"])
	return PhoneCommandsNamed(command) && ok
}

// movesWorktree: only an agent moving its own thread waits for its run to end or says what it goes on with, and
// only the link names a thread elsewhere that asked: the host says each, never the caller.
var movesWorktree PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && isDestination(command["destination"]) &&
		declUndefined(command["afterRun"]) && declUndefined(command["sender"]) && declUndefined(command["then"])
}

var namesQueued PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && PhoneCommandsIsString(command["messageId"])
}

// editsQueued: an edit names its thread, since the phone's composer takes the words for the thread it shows.
var editsQueued PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsString(command["messageId"])
}

// answersDecision: an answer to a decision a thread raised, in the user's own words or an option's label.
var answersDecision PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsString(command["decisionId"]) && PhoneCommandsIsString(command["answer"], domain.MaxAnswer)
}

// startsProposal: one suggestion started where its coordinator suggested it, never several at once or in a place the phone picked.
var startsProposal PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && PhoneCommandsIsString(command["proposalId"]) &&
		declUndefined(command["proposalIds"]) && declUndefined(command["place"])
}

var snoozes PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsIsString(command["taskId"]) && declGuard(domain.IsSnoozeHours)(command["hours"])
}

// setsRole: a thread or the draft made a coordinator or made plain again; only an agent says who asked.
var setsRole PhoneRule = func(command map[string]jsontext.Value) bool {
	return PhoneCommandsNamed(command) && (declNull(command["role"]) || declGuard(domain.IsThreadRole)(command["role"])) && declUndefined(command["sender"])
}

// MaxPhoneImageLength is how large one image a phone sends may be, as a data URL: a photo the phone has already
// shrunk, never a raw camera file.
const MaxPhoneImageLength = 8 * 1024 * 1024

// MaxPhoneImages is how many images one phone send carries, as the desktop's composer strip holds.
const MaxPhoneImages = 6

var phoneImage = regexp.MustCompile(`^data:image/png;base64,[A-Za-z0-9+/]+={0,2}$`)

// sendsImages: images a phone sends into a thread or the draft, with the words that go with them: each a picture
// the phone holds, so never a path on this computer, a screenshot's context, or marks drawn over it.
var sendsImages PhoneRule = func(command map[string]jsontext.Value) bool {
	if !PhoneCommandsNamed(command) || !declOptionalBool(command["steer"]) ||
		!(declUndefined(command["text"]) || PhoneCommandsIsString(command["text"], PhoneCommandsMaxPromptLength)) {
		return false
	}
	attachments, ok := declArray(command["attachments"])
	if !ok || len(attachments) == 0 || len(attachments) > MaxPhoneImages {
		return false
	}
	for _, value := range attachments {
		attachment, ok := declRecordOf(value)
		if !ok || !PhoneCommandsIsString(attachment["id"]) {
			return false
		}
		source, ok := declString(attachment["source"])
		if !ok || declUTF16Len(source) > MaxPhoneImageLength || !phoneImage.MatchString(source) {
			return false
		}
		annotations, ok := declArray(attachment["annotations"])
		if !ok || len(annotations) != 0 || !declUndefined(attachment["path"]) || !declUndefined(attachment["context"]) {
			return false
		}
	}
	return true
}

var TaskCommands = Declarations{
	{Type: "task.new", Fields: Shape{{"projectId", OptionalText}, {"worktreeId", OptionalText}}, At: CommandPlacementOwn, Leaves: true, Phone: starts},
	{Type: "task.select", Fields: Shape{{"taskId", Text}}, At: CommandPlacementSelect, Leaves: true, Phone: PhoneCommandsNamesThread},
	{Type: "task.archive", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Agent: CommandDeclarationNamesThread, Phone: PhoneCommandsNamesThread},
	{Type: "task.restore", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
	{Type: "task.clear-archive", Fields: Shape{}, At: CommandPlacementThread},
	{Type: "task.rename", Fields: Shape{{"taskId", Text}, {"title", Text}}, At: CommandPlacementThread, Phone: renames},
	{Type: "task.set-role", Fields: Shape{{"taskId", OptionalText}, {"role", Nullable(declGuard(domain.IsThreadRole))}, {"sender", Optional(remoteSender)}}, At: CommandPlacementThread, Agent: givesRole, Phone: setsRole},
	{Type: "task.set-coordinator", Fields: Shape{{"taskId", Text}, {"coordinatorId", NullableText}}, At: CommandPlacementThread},
	{Type: "task.set-lead-away", Fields: Shape{{"taskId", Text}, {"away", Boolean}}, At: CommandPlacementThread},
	{Type: "task.resolve", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread},
	{Type: "task.reopen", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread},
	{Type: "decision.answer", Fields: Shape{{"taskId", Text}, {"decisionId", Text}, {"answer", Text}}, At: CommandPlacementThread, Phone: answersDecision},
	{Type: "coordination.set-thread-model", Fields: Shape{{"taskId", Text}, {"engine", Optional(declGuard(domain.IsAgentEngine))}, {"model", declGuard(domain.IsModelID)}, {"effort", Optional(declGuard(domain.IsAgentEffort))}}, At: CommandPlacementThread},
	{Type: "coordination.set-muted", Fields: Shape{{"taskId", Text}, {"muted", Boolean}}, At: CommandPlacementThread},
	{Type: "coordination.stop-all", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
	{Type: "memory.set-purpose", Fields: Shape{{"taskId", Text}, {"purpose", NullableText}}, At: CommandPlacementThread},
	{Type: "memory.add", Fields: Shape{{"taskId", Text}, {"text", Text}}, At: CommandPlacementThread},
	{Type: "memory.edit", Fields: Shape{{"taskId", Text}, {"noteId", Text}, {"text", Text}}, At: CommandPlacementThread},
	{Type: "memory.forget", Fields: Shape{{"taskId", Text}, {"noteId", Text}}, At: CommandPlacementThread},
	{Type: "coordination.next-step", Fields: Shape{{"taskId", Text}, {"step", declGuard(domain.IsNextStep)}}, At: CommandPlacementThread},
	{Type: "proposal.start", Fields: Shape{{"taskId", Text}, {"proposalId", OptionalText}, {"proposalIds", Optional(proposalIds)}, {"place", Optional(proposalPlace)}}, At: CommandPlacementThread, Phone: startsProposal},
	{Type: "proposal.dismiss", Fields: Shape{{"taskId", Text}, {"proposalId", OptionalText}, {"proposalIds", Optional(proposalIds)}}, At: CommandPlacementThread},
	{Type: "task.dismiss", Fields: Shape{{"taskId", OptionalText}}, At: CommandPlacementThread, Agent: AgentThread, Phone: PhoneCommandsNamesThread},
	{Type: "task.snooze", Fields: Shape{{"taskId", Text}, {"hours", declGuard(domain.IsSnoozeHours)}}, At: CommandPlacementThread, Phone: snoozes},
	{Type: "task.unsnooze", Fields: Shape{{"taskId", Text}}, At: CommandPlacementThread, Phone: PhoneCommandsNamesThread},
	// The reducer itself carries this to the computers shown in the sidebar.
	{Type: "task.dismiss-all", Fields: Shape{{"localOnly", OptionalBoolean}, {"projectId", OptionalText}}, At: CommandPlacementLocal, Phone: dismissesAll},
	{Type: "task.read-all", Fields: Shape{{"localOnly", OptionalBoolean}}, At: CommandPlacementLocal},
	{Type: "task.move", Fields: Shape{{"taskId", Text}, {"target", dropTarget}}, At: CommandPlacementThread},
	{Type: "task.fork", Fields: Shape{{"taskId", OptionalText}, {"worktree", OptionalBoolean}, {"title", Optional(StartTitle)}}, At: CommandPlacementThread, Phone: forks},
	{Type: "task.set-policy", Fields: Shape{{"taskId", OptionalText}, {"policy", policy}}, At: CommandPlacementThread, Phone: setsPolicy},
	{Type: "task.set-model", Fields: Shape{{"taskId", OptionalText}, {"engine", declGuard(domain.IsAgentEngine)}, {"model", Optional(declGuard(domain.IsModelID))}}, At: CommandPlacementThread, Phone: setsModel},
	{Type: "task.set-fast-mode", Fields: Shape{{"taskId", OptionalText}, {"fastMode", Boolean}}, At: CommandPlacementThread, Phone: setsFastMode},
	{Type: "task.set-effort", Fields: Shape{{"taskId", OptionalText}, {"engine", declGuard(domain.IsAgentEngine)}, {"effort", declGuard(domain.IsAgentEffort)}}, At: CommandPlacementThread, Phone: setsEffort},
	{Type: "task.set-worktree", Fields: Shape{{"taskId", OptionalText}, {"worktree", Boolean}}, At: CommandPlacementThread, Phone: setsWorktree},
	{Type: "task.move-worktree", Fields: Shape{{"taskId", OptionalText}, {"destination", isWorktreeDestination}, {"afterRun", Optional(Literals(true))}, {"sender", Optional(remoteSender)}, {"then", Optional(promptText)}}, At: CommandPlacementThread, Agent: movesToCheckout, Phone: movesWorktree},
	{Type: "task.set-branch", Fields: Shape{{"branch", NullableText}, {"create", OptionalBoolean}}, At: CommandPlacementThread},
	{Type: "task.checkout-branch", Fields: Shape{{"taskId", OptionalText}, {"branch", Text}, {"create", OptionalBoolean}}, At: CommandPlacementThread},
	{Type: "task.send", Fields: Shape{{"taskId", OptionalText}, {"project", OptionalText}, {"text", OptionalText}, {"attachments", Optional(Array(runAttachment))}, {"steer", OptionalBoolean}, {"worktree", OptionalBoolean}, {"worktreeId", OptionalText}, {"model", Optional(declGuard(domain.IsModelID))}, {"engine", Optional(declGuard(domain.IsAgentEngine))}, {"effort", Optional(declGuard(domain.IsAgentEffort))}, {"role", Optional(declGuard(domain.IsThreadRole))}, {"coordinatorId", OptionalText}, {"brief", Optional(declGuard(domain.IsThreadBrief))}, {"title", Optional(StartTitle)}, {"from", OptionalText}, {"sender", Optional(remoteSender)}, {"lead", Optional(remoteLead)}, {"memory", Optional(memoryText)}}, At: CommandPlacementOwn, Agent: agentSend, Phone: sends},
	{Type: "attachments.send", Fields: Shape{{"taskId", OptionalText}, {"steer", OptionalBoolean}, {"attachments", Array(outgoingAttachment)}, {"text", OptionalText}}, At: CommandPlacementOwn, Phone: sendsImages},
	{Type: "attachments.notice", Fields: Shape{{"taskId", OptionalText}, {"message", NullableText}}, At: CommandPlacementLocal},
	{Type: "task.steer-queued", Fields: Shape{{"taskId", OptionalText}, {"messageId", Text}}, At: CommandPlacementThread, Phone: namesQueued},
	{Type: "task.drop-queued", Fields: Shape{{"taskId", OptionalText}, {"messageId", Text}, {"ifWaiting", Optional(Literals(true))}}, At: CommandPlacementThread, Phone: namesQueued},
	{Type: "task.edit-queued", Fields: Shape{{"taskId", OptionalText}, {"messageId", Text}}, At: CommandPlacementOwn, Phone: editsQueued},
}

var SideChatCommands = Declarations{
	{Type: "side-chat.open", Fields: Shape{{"chatId", Text}}, At: CommandPlacementThread},
	{Type: "side-chat.close", Fields: Shape{{"chatId", Text}}, At: CommandPlacementThread},
}
