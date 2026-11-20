package tools

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const ThreadServerName = "aicodingtool-threads"

// ThreadBridge is the window's workspace, reachable from the run: reads are projections, writes are commands.
type ThreadBridge interface {
	List(ctx context.Context, query contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error)
	Read(ctx context.Context, threadID string, limit *float64, computer *string) (contracts.ThreadTranscript, error)
	Wait(ctx context.Context, threadID string, timeoutMs float64) (contracts.ThreadWaitResult, error)
	// computer names the paired computer a new thread starts on; a named thread is acted on wherever it is.
	Command(ctx context.Context, command contracts.ExternalCommand, computer *string) (contracts.ThreadCommandResult, error)
}

// ThreadToolContext is the workspace bridge with the clock idle times are measured against.
type ThreadToolContext struct {
	Bridge ThreadBridge
	Now    func() float64
}

const (
	minuteMs            = 60_000
	threadDefaultWaitMs = 5 * minuteMs
)

// Every field is held to the limit the window reads it under, so one it would refuse is refused here,
// on its own field, rather than unnamed where the window reads the request.
var threadIDField = String().Min(1).Max(contracts.IpcMaxIDLength).Describe("The thread's known ID, an unambiguous prefix of it, or its exact title, on this computer or a paired one. Use list_threads only if you need to discover the target.")

var projectField = String().Min(1).Max(domain.MaxProposalProject).Optional().Describe(
	"\"current\" (the default) for the project this thread belongs to, \"all\" for every project, or a project named by its folder name or its path.",
)

type builtInModel struct {
	engine domain.AgentEngine
	model  domain.AgentModel
	label  string
}

func builtInModels() []builtInModel {
	var out []builtInModel
	for _, engine := range domain.AgentEngines {
		for _, model := range domain.ModelsFor(domain.BuiltInModels, domain.EngineID(engine)) {
			out = append(out, builtInModel{engine: engine, model: model.ID, label: model.Label})
		}
	}
	return out
}

// namedModelIDs lists each built-in id with the name the picker gives it, where that name is not the
// id itself, so a caller told "sol" knows which id it is.
func namedModelIDs() string {
	builtIn := builtInModels()
	var ids []domain.AgentModel
	for _, entry := range builtIn {
		if !slices.Contains(ids, entry.model) {
			ids = append(ids, entry.model)
		}
	}
	named := make([]string, len(ids))
	for index, id := range ids {
		named[index] = string(id)
		for _, entry := range builtIn {
			if entry.model == id {
				if entry.label != "" && strings.ToLower(entry.label) != string(id) {
					named[index] = fmt.Sprintf("%s (%s)", id, entry.label)
				}
				break
			}
		}
	}
	return strings.Join(named, ", ")
}

func engineNames() string {
	names := make([]string, len(domain.AgentEngines))
	for index, engine := range domain.AgentEngines {
		names[index] = string(engine)
	}
	return strings.Join(names, ", ")
}

var jsSpaces = regexp.MustCompile(`[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]+`)

// Models an engine names for itself are only known to the window, which refuses one no engine
// offers, so any well-formed id is taken here and the built-in ones are named to the caller. A name
// said with spaces, as "GLM 5.3 Flash", is taken hyphenated, which the window reads as the same name.
func modelFieldOf(omitted string) Field {
	return String().Trim().Max(256).
		Transform(func(name string) string { return jsSpaces.ReplaceAllString(name, "-") }).
		Refine(domain.IsModelID, "Not a model this thread can start on.").
		Optional().
		Describe(fmt.Sprintf("Model for the new thread: one of %s; an engine (%s) for that engine's default model; or any other model in the app's model picker. Name a model by its id or by the name the picker shows; a name several providers offer resolves to the one the user pinned. Omit to %s", namedModelIDs(), engineNames(), omitted))
}

func roleIDs() []domain.ThreadRole {
	roles := make([]domain.ThreadRole, len(domain.ThreadRoles))
	for index, option := range domain.ThreadRoles {
		roles[index] = option.Role
	}
	return roles
}

func deliveryIDs() []domain.Delivery {
	ids := make([]domain.Delivery, len(domain.Deliveries))
	for index, delivery := range domain.Deliveries {
		ids[index] = delivery.ID
	}
	return ids
}

func effortFieldOf(omitted string) Field {
	return Enum(domain.AgentEfforts...).Optional().Describe("Effort for the new thread. Omit to " + omitted)
}

var (
	threadModelField  = modelFieldOf("inherit the calling thread's model.")
	threadEngineField = Enum(domain.AgentEngines...).Optional().Describe("The engine to run model on, needed only when more than one engine offers it.")
	threadRoleField   = Enum(roleIDs()...).Optional().Describe(
		"The part the new thread plays beside the others: coordinator, implementer, reviewer, or researcher. Set reviewer for review threads; a reviewer starts on Auto, or on Bypass where its engine has no Auto, and stays in Threads unless it needs the user's approval.",
	)
	threadEffortField = effortFieldOf("inherit the calling thread's effort when the selected model supports it, otherwise use that engine's default.")

	computerField = String().Trim().Max(domain.MaxProposalComputer).Optional().Describe(`A paired computer to start it on, named by its name or ID, or "this" (the default). There it starts in the project checkout unless worktree or worktreeId says otherwise.`)
	// A thread under a coordinator starts on another computer only once the user allows it there.
	ledComputerField = String().Trim().Max(domain.MaxProposalComputer).Optional().Describe("A paired computer to run it on. It starts there once the user allows it.")
	// Only a coordinator on this computer can ask the user for a start on another, so a member led from elsewhere is told so.
	memberComputerField = String().Trim().Max(domain.MaxProposalComputer).Optional().Describe("A paired computer to run it on. It starts there once the user allows it. A thread whose coordinator is on another computer starts its threads on this one.")
)

// newThreadInput is what a start takes beyond the computer it runs on. An optional field sent blank
// says the same as one left out, so it is trimmed to nothing and dropped here, and every field is held
// to the limit the window reads it under, so one too long is refused here on its own field.
var newThreadInput = Shape{
	// A blank prompt is refused here, on its own field, since it would start nothing.
	{"prompt", String().Trim().Min(1).Max(contracts.IpcMaxPromptLength).Describe("The first message of the new thread. It has none of this conversation's context, so say everything it needs.")},
	{"title", String().Trim().Min(1).Max(domain.MaxProposalTitle).Optional().Describe("A few words naming what the new thread does, shown wherever it is listed. Omit to have it named from its prompt.")},
	{"project", String().Trim().Max(domain.MaxProposalProject).Optional().Describe("Which project to start it in: its folder name, its path, or its id. Defaults to this thread's project, or on a paired computer the project there at the same path or with the same folder name.")},
	{"worktree", Boolean().Optional().Describe("Run the new thread in its own new git worktree, detached at whatever the project has checked out, so its edits never touch this thread's checkout. Name files in its prompt relative to the project, never by this checkout's absolute path, which would send it back here.")},
	{"worktreeId", String().Trim().Max(contracts.IpcMaxIDLength).Optional().Describe("Start the thread in another worktree that already exists, as list_threads reports it, on the computer it starts on. Omit both worktree fields to share this thread's checkout. Takes precedence over worktree.")},
	{"model", threadModelField},
	{"engine", threadEngineField},
	{"effort", threadEffortField},
	{"role", threadRoleField},
	{"intent", String().Trim().Max(domain.MaxBriefField).Optional().Describe("Part of the brief a coordinator must give: the user's own words for this piece of work.")},
	{"doneWhen", String().Trim().Max(domain.MaxBriefField).Optional().Describe("Part of the brief a coordinator must give: what finished looks like, checkably.")},
	{"delivers", Enum(deliveryIDs()...).Optional().Describe("Part of the brief a coordinator must give: pull-request, commit, or report (knowledge only, no code changes).")},
}

var startInput = newThreadInput.With("computer", computerField)

// ledStartInput is what a start for a thread that will work under a coordinator takes: it runs,
// unless it names otherwise, on what the coordinator's threads run on.
func ledStartInput(whose string) Shape {
	return newThreadInput.
		With("model", modelFieldOf("run it on the model "+whose+" threads run on.")).
		With("effort", effortFieldOf("run it at the effort "+whose+" threads run at, or the nearest one the selected model takes."))
}

// CoordinatorStartInput is what a coordinator's start_thread takes, and so each thread it suggests.
var CoordinatorStartInput = ledStartInput("this coordinator's").With("computer", ledComputerField)

// NewThreadFields is what a start's arguments ask of the new thread beyond its prompt. A brief counts only once it is whole.
type NewThreadFields struct {
	Brief      *domain.ThreadBrief
	Title      *string
	Project    *string
	WorktreeID *string
	Worktree   *bool
	Model      *domain.AgentModel
	Engine     *domain.AgentEngine
	Effort     *domain.AgentEffort
	Role       *domain.ThreadRole
}

func NewThreadFieldsOf(args Args) NewThreadFields {
	var fields NewThreadFields
	intent, doneWhen, delivers := args.Str("intent"), args.Str("doneWhen"), args.Str("delivers")
	if intent != "" && doneWhen != "" && delivers != "" {
		fields.Brief = &domain.ThreadBrief{Intent: intent, DoneWhen: doneWhen, Delivers: domain.Delivery(delivers)}
	}
	fields.Title = nonEmpty(args.Str("title"))
	fields.Project = nonEmpty(args.Str("project"))
	if id := args.Str("worktreeId"); id != "" {
		fields.WorktreeID = &id
	} else if args.Bool("worktree") {
		fields.Worktree = trueOrNil(true)
	}
	if model := args.Str("model"); model != "" {
		value := domain.AgentModel(model)
		fields.Model = &value
	}
	if engine := args.Str("engine"); engine != "" {
		value := domain.AgentEngine(engine)
		fields.Engine = &value
	}
	if effort := args.Str("effort"); effort != "" {
		value := domain.AgentEffort(effort)
		fields.Effort = &value
	}
	if role := args.Str("role"); role != "" {
		value := domain.ThreadRole(role)
		fields.Role = &value
	}
	return fields
}

func elapsed(ms float64) string {
	minutes := math.Floor(ms / minuteMs)
	if minutes < 60 {
		return jsNumber(minutes) + "m"
	}
	hours := math.Floor(minutes / 60)
	if hours < 48 {
		rest := math.Mod(minutes, 60)
		if rest != 0 {
			return fmt.Sprintf("%sh %sm", jsNumber(hours), jsNumber(rest))
		}
		return jsNumber(hours) + "h"
	}
	return jsNumber(math.Floor(hours/24)) + "d"
}

func describeThread(thread *contracts.ThreadSummary, at float64) string {
	parts := []string{fmt.Sprintf("%s [%s]", thread.Title, thread.ID)}
	if thread.Role != nil && *thread.Role != "" {
		parts = append(parts, "role "+string(*thread.Role))
	}
	if thread.Computer != nil {
		offline := ""
		if thread.Computer.Offline {
			offline = " (offline, cached)"
		}
		parts = append(parts, fmt.Sprintf("computer %s [%s]%s", thread.Computer.Name, thread.Computer.ID, offline))
	}
	switch {
	case thread.WorktreeRoot != nil:
		parts = append(parts, *thread.WorktreeRoot)
	case thread.ProjectRoot != nil:
		parts = append(parts, *thread.ProjectRoot)
	default:
		parts = append(parts, "no project")
	}
	if thread.WorktreeID != nil && *thread.WorktreeID != "" {
		parts = append(parts, "worktree "+*thread.WorktreeID)
	}
	parts = append(parts, string(thread.Status))
	if thread.PausedUntil != nil {
		parts = append(parts, "paused by its usage limit until "+isoString(*thread.PausedUntil))
	}
	parts = append(parts, jsNumber(thread.MessageCount)+" messages")
	if thread.AttachmentCount != 0 {
		parts = append(parts, jsNumber(thread.AttachmentCount)+" with images")
	}
	parts = append(parts, "idle "+elapsed(max(0, at-thread.LastActivityAt)))
	if thread.Archived {
		parts = append(parts, "archived")
	}
	return strings.Join(parts, " · ")
}

func transcriptText(transcript *contracts.ThreadTranscript, at float64) string {
	lines := []string{describeThread(&transcript.Thread, at)}
	if transcript.Omitted != 0 {
		lines = append(lines, fmt.Sprintf("(%s earlier messages not shown)", jsNumber(transcript.Omitted)))
	}
	lines = append(lines, "")
	for _, message := range transcript.Messages {
		origin := ""
		if message.Origin != nil && *message.Origin != "" {
			origin = " · " + *message.Origin
		}
		lines = append(lines, fmt.Sprintf("[%s%s] %s", message.Kind, origin, message.Text))
	}
	return strings.Join(lines, "\n")
}

func threadReport(work func() (string, error)) ToolResult { return reportAs("Thread error: ", work) }

func threadCommand(ctx context.Context, tool ThreadToolContext, command contracts.ExternalCommand, computer *string) (contracts.ThreadCommandResult, error) {
	return tool.Bridge.Command(ctx, command, computer)
}

var ThreadTools = []Definition[ThreadToolContext]{
	{
		Name:        "list_threads",
		Description: "List the other AICodingTool threads, newest activity first. Set computer to \"all\" to include paired computers. Use when the user asks what else is going on, points at recent or related work, or describes threads by age rather than by name.",
		Input: Shape{
			{"computer", String().Min(1).Max(domain.MaxProposalComputer).Optional().Describe(`"this" (the default), "all" paired computers and this one, or a paired computer named by its name or ID. With "all" or a paired computer selected, project defaults to "all". Remote rows include their computer and whether it is offline.`)},
			{"project", projectField},
			{"archived", Boolean().Optional().Describe("List archived threads instead of active ones.")},
			{"idleMinutes", Number().Nonnegative().Optional().Describe("Only threads that have done nothing for at least this many minutes.")},
			{"search", String().Min(1).Max(contracts.MaxSearchText).Optional().Describe("Only threads whose title or messages contain this text.")},
			{"hasImages", Boolean().Optional().Describe("Only threads where a message carries an image.")},
			{"limit", Number().Nonnegative().Optional().Describe("How many threads to return. Defaults to 20.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				at := tool.Now()
				query := contracts.ThreadListQuery{
					Computer:    args.OptStr("computer"),
					Project:     args.OptStr("project"),
					Archived:    args.OptBool("archived"),
					Search:      args.OptStr("search"),
					Attachments: args.OptBool("hasImages"),
				}
				if idle := args.OptNum("idleMinutes"); idle != nil {
					idleForMs := *idle * minuteMs
					query.IdleForMs = &idleForMs
				}
				limit := 20.0
				if given := args.OptNum("limit"); given != nil {
					limit = *given
				}
				query.Limit = &limit
				threads, err := tool.Bridge.List(ctx, query)
				if err != nil {
					return "", err
				}
				if len(threads) == 0 {
					return "No thread matches.", nil
				}
				lines := make([]string, len(threads))
				for index, thread := range threads {
					lines[index] = describeThread(thread, at)
				}
				return strings.Join(lines, "\n"), nil
			})
		},
	},
	{
		Name:        "read_thread",
		Description: "Read another thread's transcript, including threads on paired computers. Use a known ID directly, including one supplied in app context or an aicodingtool://thread/<id> link. The app resolves its computer automatically. Use list_threads first only when the target is unknown.",
		Input: Shape{
			{"threadId", threadIDField},
			{"computer", String().Min(1).Max(domain.MaxProposalComputer).Optional().Describe(`Optional computer name or ID, or "this" for local threads. Omit to resolve the thread across this computer and paired computers. Use to disambiguate matching threads.`)},
			{"limit", Number().Nonnegative().Optional().Describe("How many of the newest messages to read. Defaults to 30.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				transcript, err := tool.Bridge.Read(ctx, args.Str("threadId"), args.OptNum("limit"), args.OptStr("computer"))
				if err != nil {
					return "", err
				}
				return transcriptText(&transcript, tool.Now()), nil
			})
		},
	},
	{
		Name:        "wait_for_thread",
		Description: "Wait until a thread stops working and report what it last said. Use after start_thread or message_thread when the user is waiting on that work; polling read_thread instead just burns turns. A wait that runs out says so, and calling it again keeps waiting.",
		Input: Shape{
			{"threadId", threadIDField},
			{"timeoutSeconds", Number().Optional().Describe("How long to wait before reporting back regardless. Defaults to 300, and cannot exceed 900.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				timeoutMs := float64(threadDefaultWaitMs)
				if seconds := args.OptNum("timeoutSeconds"); seconds != nil {
					timeoutMs = max(0, *seconds) * 1_000
				}
				timeoutMs = min(timeoutMs, contracts.MaxThreadWaitMs)
				waited, err := tool.Bridge.Wait(ctx, args.Str("threadId"), timeoutMs)
				if err != nil {
					return "", err
				}
				heading := "Finished"
				if waited.TimedOut {
					heading = fmt.Sprintf("Still working after %ss", jsNumber(math.Floor(timeoutMs/1_000+0.5)))
				}
				lines := []string{heading + ": " + describeThread(&waited.Thread, tool.Now())}
				if waited.Reply != nil && *waited.Reply != "" {
					lines = append(lines, "", *waited.Reply)
				}
				return strings.Join(lines, "\n"), nil
			})
		},
	},
	{
		Name:        "start_thread",
		Description: "Start a new AICodingTool thread on its own prompt and run it. Use when the user asks for separate pieces of work to run side by side, one thread per piece. The new thread runs with the permission policy the app is set to on the computer it runs on, except a reviewer, which starts on Auto, or on Bypass where its engine has no Auto; write a prompt that stands on its own. It starts in this thread's checkout, worktree included. Pass worktree to give it an isolated checkout instead, which is what you want when it edits the same files as this thread.",
		Input:       startInput,
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				fields := NewThreadFieldsOf(args)
				text := args.Str("prompt")
				send := contracts.TaskCommandTaskSend{
					Text: &text, Brief: fields.Brief, Title: fields.Title, Project: fields.Project, WorktreeID: fields.WorktreeID,
					Worktree: fields.Worktree, Model: fields.Model, Engine: fields.Engine, Effort: fields.Effort, Role: fields.Role,
				}
				result, err := threadCommand(ctx, tool, send, nonEmpty(args.Str("computer")))
				if err != nil {
					return "", err
				}
				if result.Thread != nil {
					return "Started " + describeThread(result.Thread, tool.Now()), nil
				}
				if result.Notice != nil {
					return *result.Notice, nil
				}
				return "The thread did not start.", nil
			})
		},
	},
	{
		Name:        "message_thread",
		Description: "Send a message to another thread, on this computer or a paired one. A thread that is working reads it in the run already going, and an idle one starts a run on it. To learn where a thread stands, read it with read_thread instead.",
		Input: Shape{
			{"threadId", threadIDField},
			// A blank message is refused here rather than answered as sent, since nothing would reach the thread.
			{"text", String().Trim().Min(1).Max(contracts.IpcMaxPromptLength).Describe("The message to send.")},
		},
		// A message from another thread is meant for the work in hand, so it joins the run rather than
		// waiting behind it as a run of its own.
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				taskID, text := args.Str("threadId"), args.Str("text")
				result, err := threadCommand(ctx, tool, contracts.TaskCommandTaskSend{TaskID: &taskID, Text: &text, Steer: trueOrNil(true)}, nil)
				if err != nil {
					return "", err
				}
				if result.Thread != nil {
					return "Sent to " + describeThread(result.Thread, tool.Now()), nil
				}
				return "The message was not delivered.", nil
			})
		},
	},
	{
		Name:        "set_thread_role",
		Description: "Give a thread its part beside the others, or take it off. The role shows on the thread's row and in list_threads. A coordinator changes no files itself and delegates to threads it starts, and keeps its role until the user changes it; the other roles change nothing about what the thread may do.",
		Input: Shape{
			{"threadId", threadIDField},
			{"role", Enum(roleIDs()...).Nullable().Describe("coordinator, implementer, reviewer, or researcher. null takes the role off.")},
		},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				taskID := args.Str("threadId")
				var role *domain.ThreadRole
				if text := args.OptStr("role"); text != nil {
					value := domain.ThreadRole(*text)
					role = &value
				}
				result, err := threadCommand(ctx, tool, contracts.TaskCommandTaskSetRole{TaskID: &taskID, Role: role}, nil)
				if err != nil {
					return "", err
				}
				if result.Thread == nil {
					return "The thread's role was not changed.", nil
				}
				verb := "Took the role off"
				if role != nil && *role != "" {
					verb = "Set the role of"
				}
				return verb + " " + describeThread(result.Thread, tool.Now()), nil
			})
		},
	},
	{
		Name:        "archive_thread",
		Description: "Archive a thread, which also cancels a run it still has going and retires its automation. Archived threads stay recoverable for five days. This throws away work in progress, so only archive when the user asked for it.",
		Input:       Shape{{"threadId", threadIDField}},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				result, err := threadCommand(ctx, tool, contracts.TaskCommandTaskArchive{TaskID: args.Str("threadId")}, nil)
				if err != nil {
					return "", err
				}
				if result.Thread != nil {
					return "Archived " + describeThread(result.Thread, tool.Now()), nil
				}
				return "The thread was not archived.", nil
			})
		},
	},
	{
		Name:        "dismiss_thread",
		Description: "Dismiss a thread, taking it out of Priority as the sidebar's Dismiss does. Use when the user asks to dismiss a thread, such as this one once its last step is done. Dismissing this thread while it runs takes effect when this turn finishes; a turn that fails still surfaces.",
		Input: Shape{
			{"threadId", String().Min(1).Max(contracts.IpcMaxIDLength).Optional().Describe("The thread's ID, an unambiguous prefix of it, or its exact title. Omit to dismiss this thread.")},
		},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				result, err := threadCommand(ctx, tool, contracts.TaskCommandTaskDismiss{TaskID: nonEmpty(args.Str("threadId"))}, nil)
				if err != nil {
					return "", err
				}
				if result.Thread != nil {
					return "Dismissed " + describeThread(result.Thread, tool.Now()), nil
				}
				return "The thread was not dismissed.", nil
			})
		},
	},
	{
		Name:        "move_to_worktree",
		Description: "Move a thread into a new worktree, one the project already has, or back to the project checkout, as the thread menu's Move to worktree does. This is how a thread works in a worktree: use it whenever the user asks for one, never git worktree, so the app knows where the thread works. File changes already made stay in the old checkout. Moving this thread waits for this turn to end, and its next turn starts in the new checkout, on its own when you pass then. Another thread must not be running.",
		Input: Shape{
			{"threadId", String().Max(contracts.IpcMaxIDLength).Nullable().Optional().Describe("The thread to move: its ID, an unambiguous prefix of it, or its exact title. Omit or null to move this thread.")},
			{"worktreeId", String().Max(contracts.IpcMaxIDLength).Nullable().Describe(`"new" for a new worktree, the ID of one list_threads reports, or null for the project checkout.`)},
			{"then", String().Trim().Max(contracts.IpcMaxPromptLength).Nullable().Describe("When moving this thread, whatever else the user asked for along with the move, written as instructions to yourself. This thread's next turn starts in the new checkout on its own with it. null when the move is all the user asked for, or when moving another thread.")},
		},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				var destination domain.WorktreeDestination = domain.WorktreeDestinationLocal{}
				if id := args.Str("worktreeId"); id == "new" {
					destination = domain.WorktreeDestinationNew{}
				} else if id != "" {
					destination = domain.WorktreeDestinationWorktree{ID: id}
				}
				move := contracts.TaskCommandTaskMoveWorktree{TaskID: nonEmpty(args.Str("threadId")), Destination: destination, Then: nonEmpty(args.Str("then"))}
				result, err := threadCommand(ctx, tool, move, nil)
				if err != nil {
					return "", err
				}
				if result.Notice != nil && *result.Notice != "" {
					return *result.Notice, nil
				}
				if result.Thread != nil {
					return "Moved " + describeThread(result.Thread, tool.Now()), nil
				}
				return "The thread was not moved.", nil
			})
		},
	},
	{
		Name:        "delete_worktree",
		Description: "Delete a worktree the app made, by the id list_threads reports. Uncommitted work is committed first so it stays recoverable, then the folder is removed; git-ignored files are lost. Every thread in it moves back to the project checkout. Refused while any of those threads is running. Only delete when the user asked for it, such as cleaning up after coordinated work is done.",
		Input:       Shape{{"worktreeId", String().Min(1).Max(contracts.IpcMaxIDLength).Describe("The worktree's ID, as list_threads reports it.")}},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				id := args.Str("worktreeId")
				result, err := threadCommand(ctx, tool, contracts.WorktreeCommandWorktreeDelete{WorktreeID: &id}, nil)
				if err != nil {
					return "", err
				}
				if result.Notice != nil {
					return *result.Notice, nil
				}
				return "Deleted worktree " + id + ".", nil
			})
		},
	},
	{
		Name:        "stop_thread",
		Description: "Stop the run a thread has going, leaving the thread itself alone. This throws away work in progress, so only stop a run when the user asked for it.",
		Input:       Shape{{"threadId", threadIDField}},
		Run: func(ctx context.Context, tool ThreadToolContext, args Args) ToolResult {
			return threadReport(func() (string, error) {
				taskID := args.Str("threadId")
				result, err := threadCommand(ctx, tool, contracts.RunControlCommandRunCancel{TaskID: &taskID}, nil)
				if err != nil {
					return "", err
				}
				if result.Thread != nil {
					return "Asked this thread to stop: " + describeThread(result.Thread, tool.Now()), nil
				}
				return "The thread was not stopped.", nil
			})
		},
	},
}

func withStartInput(definitions []Definition[ThreadToolContext], input Shape) []Definition[ThreadToolContext] {
	out := slices.Clone(definitions)
	for index := range out {
		if out[index].Name == "start_thread" {
			out[index].Input = input
		}
	}
	return out
}

// A coordinator is woken with its threads' news, so it never holds its turn open waiting on one, and
// it does no work itself, so it never moves into a worktree: work there is a thread started in one.
var coordinatorThreadTools = withStartInput(filterDefinitions(ThreadTools, func(tool Definition[ThreadToolContext]) bool {
	return tool.Name != "wait_for_thread" && tool.Name != "move_to_worktree"
}), CoordinatorStartInput)

// A thread one working under a coordinator starts works under that coordinator too.
var memberThreadTools = withStartInput(ThreadTools, ledStartInput("your coordinator's").With("computer", memberComputerField))

// ThreadToolsFor binds the thread tools for a run in its coordination role (empty for none). A nil
// now measures idle times against application.Now.
func ThreadToolsFor(bridge ThreadBridge, now func() float64, role domain.CoordinationRole) []BoundTool {
	if now == nil {
		now = func() float64 { return application.Now() }
	}
	tool := ThreadToolContext{Bridge: bridge, Now: now}
	switch role {
	case domain.CoordinationRoleCoordinator:
		return Bind(tool, coordinatorThreadTools)
	case domain.CoordinationRoleMember:
		return Bind(tool, memberThreadTools)
	}
	return Bind(tool, ThreadTools)
}

func threadInstructions(dialect BriefDialect) string {
	return fmt.Sprintf("Use the AICodingTool thread tools, such as start_thread, list_threads and read_thread, when the user's request requires creating, fetching or acting on another AICodingTool thread. App task IDs identify AICodingTool threads, not %s. Conversation history and app-supplied thread context are already available evidence; answering from them does not require a tool call.", dialect.NativeWork)
}

func worktreeInstructions(dialect BriefDialect, moves bool) string {
	how := `Start a thread with start_thread and worktree to do it.`
	if moves {
		how = `To do the work in this thread, call move_to_worktree with "new" and the rest of the request as then; this thread moves into a new worktree when this turn ends and its next turn starts there on its own with then, so end the turn right after the call. Start a thread with start_thread and worktree only when the user wants the work in a separate thread.`
	}
	native := ""
	if dialect.NativeWorktree != nil && *dialect.NativeWorktree != "" {
		native = " or " + *dialect.NativeWorktree
	}
	return "When the user asks for work in a worktree, use the AICodingTool worktree tools so the app knows where each thread works. " + how + " Never make one yourself for that with git worktree" + native + "."
}

var ThreadServer = Server{
	Name:        ThreadServerName,
	Definitions: infos(ThreadTools),
	// Reading the workspace changes nothing, so it needs no approval; starting or stopping a run does.
	Grants: GrantsReadOnly,
	// A native review is already isolated in a thread of the engine's own. App thread tools would create sidebar tasks instead.
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Threads == nil || run.reviewing() {
			return nil, false
		}
		return ThreadToolsFor(run.Threads, nil, run.role()), true
	},
	Brief: func(offers Offers, dialect BriefDialect, _ BriefRun) []string {
		var out []string
		if offers("") {
			out = append(out, threadInstructions(dialect))
		}
		if offers("start_thread") {
			out = append(out, worktreeInstructions(dialect, offers("move_to_worktree")))
		}
		return out
	},
}
