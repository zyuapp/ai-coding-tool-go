package tools

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const CoordinationServerName = "aicodingtool-coordination"

// MemoryNote is what the window tells a coordinator of a change to its memory or of its suggestions.
type MemoryNote struct {
	Note string `json:"note"`
}

// CoordinationBridge is what a thread working under a coordinator, or the coordinator itself, says
// for itself, answered by the window that keeps it.
type CoordinationBridge interface {
	Report(ctx context.Context, state domain.CoordinationState, summary string) (contracts.FindingResult, error)
	Decide(ctx context.Context, request domain.DecisionRequest) (contracts.FindingResult, error)
	// A coordinator's own memory: any change but an edit, which only the user makes. It fails with
	// what to tell the coordinator when the change cannot be made.
	Remember(ctx context.Context, change contracts.ThreadRequestMemoryChange) (MemoryNote, error)
	// A coordinator's suggested threads, kept for the user to start. It fails with what to tell the
	// coordinator when none could be.
	Propose(ctx context.Context, proposals []*domain.ProposalRequest) (MemoryNote, error)
}

func coordinationReport(failure string, work func() (string, error)) ToolResult {
	return reportAs(failure, work)
}

// coordinationAnswered answers a refused memory change or suggestion in its own words, since it
// already says what to do instead.
func coordinationAnswered(work func() (MemoryNote, error)) ToolResult {
	return coordinationReport("", func() (string, error) {
		note, err := work()
		return note.Note, err
	})
}

func findingNote(result contracts.FindingResult, err error) (string, error) {
	return result.Note, err
}

var reportStatusTool = Definition[CoordinationBridge]{
	Name:        "report_status",
	Description: "Tell the coordinator this thread works under where your work stands. Call it before you end the turn in which you finish, get blocked, or fail: your closing message is not a report. The coordinator hears it when your turn ends. A done report names what you delivered: the pull request URL, the commits, or the report.",
	Input: Shape{
		{"state", Enum(domain.CoordinationStates...).Describe("working, blocked, done, or failed.")},
		{"summary", String().Trim().Min(1).Max(domain.MaxSummary).Describe("One or two sentences: the outcome, or what stops you.")},
	},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationReport("Coordination error: ", func() (string, error) {
			return findingNote(bridge.Report(ctx, domain.CoordinationState(args.Str("state")), args.Str("summary")))
		})
	},
}

func distinctOptionLabels(value any) bool {
	items, _ := value.([]any)
	options := make([]*domain.DecisionOption, 0, len(items))
	for _, item := range items {
		option, _ := item.(Args)
		options = append(options, &domain.DecisionOption{Label: option.Str("label")})
	}
	return domain.DistinctLabels(options)
}

// raiseDecisionTool refuses a blank question or label, or a label two options share, on its own
// field, as the window would refuse it unnamed; a blank context or description is trimmed to nothing
// and left out.
var raiseDecisionTool = Definition[CoordinationBridge]{
	Name:        "raise_decision",
	Description: "Put a choice to the user that only they can make: one that changes scope, product behaviour, or anything destructive. It shows in the app at once; their answer arrives in this thread as a message. Offer the options you see with your recommendation, then end your turn if you cannot go on without the answer.",
	Input: Shape{
		{"question", String().Trim().Min(1).Max(domain.MaxQuestion).Describe("The decision, as one question.")},
		{"context", String().Trim().Max(domain.MaxDecisionContext).Optional().Describe("What the user needs to know to decide, including the consequences either way.")},
		{"options", Array(Object(Shape{
			{"label", String().Trim().Min(1).Max(domain.MaxOptionLabel)},
			{"description", String().Trim().Max(domain.MaxOptionDescription).Optional()},
			{"recommended", Boolean().Optional()},
		})).Max(domain.MaxOptions).Refine(distinctOptionLabels, "Give each option its own label.").Describe("The choices, each with a short label of its own and what it means. Mark the one you recommend.")},
	},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationReport("Coordination error: ", func() (string, error) {
			request := domain.DecisionRequest{Question: args.Str("question"), Context: nonEmpty(args.Str("context")), Options: []*domain.DecisionOption{}}
			for _, option := range args.List("options") {
				request.Options = append(request.Options, &domain.DecisionOption{
					Label:       option.Str("label"),
					Description: nonEmpty(option.Str("description")),
					Recommended: trueOrNil(option.Bool("recommended")),
				})
			}
			return findingNote(bridge.Decide(ctx, request))
		})
	},
}

// rememberTool refuses a blank note on its own field, as the window would refuse it unnamed.
var rememberTool = Definition[CoordinationBridge]{
	Name:        "remember",
	Description: "Save a note to your memory: something the user asked you to remember, or how they want you to work. You and every thread you start from now on read it.",
	Input:       Shape{{"text", String().Trim().Min(1).Max(domain.MaxMemoryNote).Describe("The note, in one or two sentences.")}},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationAnswered(func() (MemoryNote, error) {
			return bridge.Remember(ctx, domain.MemoryChangeRemember{Text: args.Str("text")})
		})
	},
}

var forgetTool = Definition[CoordinationBridge]{
	Name:        "forget",
	Description: "Drop a note from your memory when the user asks you to forget it or it no longer holds.",
	Input:       Shape{{"noteId", String().Trim().Min(1).Max(domain.MaxMemoryNoteID).Describe("The note's ID, such as n3.")}},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationAnswered(func() (MemoryNote, error) {
			return bridge.Remember(ctx, domain.MemoryChangeForget{NoteID: args.Str("noteId")})
		})
	},
}

var setPurposeTool = Definition[CoordinationBridge]{
	Name:        "set_purpose",
	Description: "Set what this coordinator is for, in the user's words, when they say what its purpose or goal is. Pass null to clear it.",
	Input:       Shape{{"purpose", String().Max(domain.MaxPurpose).Nullable().Describe("One line, or null to clear it.")}},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationAnswered(func() (MemoryNote, error) {
			return bridge.Remember(ctx, domain.MemoryChangePurpose{Purpose: args.OptStr("purpose")})
		})
	},
}

var proposeThreadsTool = Definition[CoordinationBridge]{
	Name:        "propose_threads",
	Description: "Suggest threads for the user to start, rather than starting them. Each takes what start_thread takes, brief included, and must have a title. Nothing starts until the user starts it; you hear from each one they start once it ends a turn.",
	Input: Shape{
		{"threads", Array(Object(CoordinatorStartInput.With(
			// Every suggestion is labelled for the user, and the thread started from it keeps the label as its name.
			"title", String().Trim().Min(1).Max(domain.MaxProposalTitle).Describe("A few words naming what the thread does, which label the suggestion for the user and name the thread once it starts."),
		))).Min(1).Max(domain.MaxProposals).Describe("The threads to suggest, one per piece of work.")},
	},
	Run: func(ctx context.Context, bridge CoordinationBridge, args Args) ToolResult {
		return coordinationAnswered(func() (MemoryNote, error) {
			threads := args.List("threads")
			proposals := make([]*domain.ProposalRequest, 0, len(threads))
			for _, thread := range threads {
				fields := NewThreadFieldsOf(thread)
				proposals = append(proposals, &domain.ProposalRequest{
					Title: thread.Str("title"), Prompt: thread.Str("prompt"), Brief: fields.Brief, Project: fields.Project,
					Worktree: fields.Worktree, WorktreeID: fields.WorktreeID, Engine: fields.Engine, Model: fields.Model,
					Effort: fields.Effort, Role: fields.Role, Computer: nonEmpty(thread.Str("computer")),
				})
			}
			return bridge.Propose(ctx, proposals)
		})
	},
}

var (
	memberCoordinationTools      = []Definition[CoordinationBridge]{reportStatusTool, raiseDecisionTool}
	coordinatorCoordinationTools = []Definition[CoordinationBridge]{raiseDecisionTool, rememberTool, forgetTool, setPurposeTool, proposeThreadsTool}
)

// CoordinationToolsFor binds what a run says for itself in its role. A coordinator reports to no one,
// so it raises decisions, keeps its memory, and suggests threads.
func CoordinationToolsFor(bridge CoordinationBridge, role domain.CoordinationRole) []BoundTool {
	if role == domain.CoordinationRoleMember {
		return Bind(bridge, memberCoordinationTools)
	}
	return Bind(bridge, coordinatorCoordinationTools)
}

var CoordinationServer = Server{
	Name:        CoordinationServerName,
	Definitions: infos([]Definition[CoordinationBridge]{reportStatusTool, raiseDecisionTool, rememberTool, forgetTool, setPurposeTool, proposeThreadsTool}),
	// A thread speaking for itself to its coordinator and the user reaches nothing but the app.
	Grants: GrantsAll,
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Coordination == nil || run.role() == "" || run.reviewing() {
			return nil, false
		}
		return CoordinationToolsFor(run.Coordination, run.role()), true
	},
	// A coordination role is told how to work whether or not its tools are offered.
	Brief: func(_ Offers, dialect BriefDialect, run BriefRun) []string {
		role := domain.CoordinationRole("")
		if run.CoordinationRole != nil {
			role = *run.CoordinationRole
		}
		return CoordinationInstructions(role, run.WorkspaceRoot, run.ProjectRoot, dialect)
	},
}
