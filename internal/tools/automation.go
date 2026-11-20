package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const AutomationServerName = "aicodingtool-automation"

// AutomationBridge is scoped to the running thread, so a run can only reach its own automation.
type AutomationBridge interface {
	Read(ctx context.Context) (*domain.AutomationView, error)
	List(ctx context.Context) ([]*domain.AutomationView, error)
	Save(ctx context.Context, draft contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error)
	Update(ctx context.Context, patch domain.AutomationPatch) (*domain.AutomationView, error)
	Remove(ctx context.Context) (bool, error)
}

// FindingBridge is what a scheduled run says about itself, answered by the window that keeps it.
// Scoped to the running thread: a run reports on its own thread or on none at all.
type FindingBridge interface {
	Notify(ctx context.Context, report contracts.FindingReport) (contracts.FindingResult, error)
	NothingToReport(ctx context.Context, checked string) (contracts.FindingResult, error)
}

var (
	automationScheduleField = String().Describe("A five-field cron expression in local time (\"0 8 * * *\" = 8AM daily, \"* * * * *\" = every minute), or an ISO 8601 timestamp for a single run. Seconds are not supported.")
	automationPolicyField   = Enum(domain.ExecutionPolicies...).Optional().Describe("Permission policy for scheduled runs. Defaults to this run's policy and cannot exceed it. Ask the user to change the task's permissions before scheduling work that needs stronger permissions.")
	automationSurfaceField  = String().Max(domain.MaxSurfaceWhen).Optional().Describe("One sentence naming when a tick of this schedule is worth the user's attention, such as \"an error in the logs was caused by the user's own code.\" Setting it makes the schedule quiet: a run that calls nothing_to_report settles without reaching anyone, and only one that calls notify surfaces. Leave it out and the schedule keeps whatever it surfaces for now, which for a new one is every run; pass an empty sentence to take the quiet off. A one-time run always surfaces, so it takes none.")
	automationEndsField     = String().Max(domain.MaxEndsWhen).Optional().Describe("One sentence naming what finishes this automation, such as \"the PR is approved.\" Set it when the automation watches something until it is done: its thread shows as running until a tick meets this and calls stop. Leave it out for a routine that repeats until someone stops it, such as a daily brief. Pass an empty sentence to make a watch a routine.")
	automationPromptField   = String().Describe("The prompt to run on every tick. Write it to stand alone. A watch's stop condition goes in endsWhen, which every tick is told.")
)

func describeAutomation(automation *domain.AutomationView) string {
	schedule := "schedule: " + automation.Schedule
	if automation.Timezone != nil && *automation.Timezone != "" {
		schedule += " (" + *automation.Timezone + ")"
	}
	paused := "false"
	if automation.Paused {
		paused = "true"
	}
	next := "none"
	if automation.NextRunAt != nil && *automation.NextRunAt != 0 {
		next = isoString(*automation.NextRunAt)
	}
	lines := []string{schedule, "paused: " + paused, "runs so far: " + jsNumber(automation.RunCount), "next run: " + next}
	if automation.SurfaceWhen != nil && *automation.SurfaceWhen != "" {
		lines = append(lines, "surfaces when: "+*automation.SurfaceWhen)
	}
	if automation.EndsWhen != nil && *automation.EndsWhen != "" {
		lines = append(lines, "ends when: "+*automation.EndsWhen)
	}
	last := "never"
	if automation.LastRunAt != nil && *automation.LastRunAt != 0 {
		status := "undefined"
		if automation.LastStatus != nil {
			status = string(*automation.LastStatus)
		}
		last = isoString(*automation.LastRunAt) + " (" + status + ")"
	}
	lines = append(lines, "last run: "+last)
	if automation.Policy != nil && *automation.Policy != "" {
		lines = append(lines, "policy: "+string(*automation.Policy))
	}
	lines = append(lines, "prompt: "+automation.Prompt)
	return strings.Join(lines, "\n")
}

func automationReport(work func() (string, error)) ToolResult {
	return reportAs("Automation error: ", work)
}

// FindingTools are what a quiet schedule uses to say whether this tick was worth the user's
// attention. Both are stateless: they name nothing about the run, because the window is what knows
// which run is calling.
var FindingTools = []Definition[FindingBridge]{
	{
		Name:        "notify",
		Description: "Surface this scheduled run to the user, saying what you found. Call it the moment you find something, not at the end: a run that is stopped afterwards keeps what it already raised. Call it once per finding; they accumulate. Nothing later retracts it, and a quiet run that never calls it settles without reaching anyone.",
		Input: Shape{
			{"headline", String().Min(1).Max(domain.MaxHeadline).Describe("One line naming what you found. It is what the user reads in the sidebar, so write the finding itself rather than that you looked.")},
			{"detail", String().Max(domain.MaxDetail).Optional().Describe("The finding in full, in plain text: it lands in the thread under the headline, as written.")},
			{"key", String().Min(1).Max(domain.MaxFindingKey).Optional().Describe("What this finding is about, worded the same way on every run, so the same one is not raised twice while the user has yet to read it.")},
		},
		Run: func(ctx context.Context, bridge FindingBridge, args Args) ToolResult {
			return automationReport(func() (string, error) {
				result, err := bridge.Notify(ctx, contracts.FindingReport{Headline: args.Str("headline"), Detail: args.OptStr("detail"), Key: args.OptStr("key")})
				if err != nil {
					// Whatever went wrong, the run has said it found something, and silence is no longer its to buy.
					return "", errors.New(errorText(err) + " This run found something either way, so it must not call nothing_to_report.")
				}
				return result.Note, nil
			})
		},
	},
	{
		Name:        "nothing_to_report",
		Description: "Say this run looked and found nothing worth surfacing, which is the only thing that lets a quiet scheduled run settle without disturbing the user. Say nothing at all and the run surfaces as an ordinary one, which is what a run that could not do its job should do.",
		Input:       Shape{{"checked", String().Min(1).Max(domain.MaxHeadline).Describe("What you actually checked, in a few words. A run that could not check what it came for has something to report, not nothing.")}},
		Run: func(ctx context.Context, bridge FindingBridge, args Args) ToolResult {
			return automationReport(func() (string, error) {
				result, err := bridge.NothingToReport(ctx, args.Str("checked"))
				if err != nil {
					return "", err
				}
				return result.Note, nil
			})
		},
	},
}

func executionPolicy(args Args) *domain.ExecutionPolicy {
	text := args.OptStr("policy")
	if text == nil {
		return nil
	}
	policy := domain.ExecutionPolicy(*text)
	return &policy
}

var AutomationTools = []Definition[AutomationBridge]{
	{
		Name:        "schedule",
		Description: "Turn this task into a recurring automation that re-runs a prompt on a schedule. Use when the user asks to repeat, babysit, poll, or watch something on a cadence. The task keeps one automation, so calling this again replaces it. Runs never overlap: a tick that arrives while the previous run is still going is dropped.",
		Input: Shape{
			{"prompt", automationPromptField},
			{"schedule", automationScheduleField},
			{"timezone", String().Optional().Describe("IANA timezone such as \"America/Los_Angeles\". Defaults to the machine's local time.")},
			{"policy", automationPolicyField},
			{"surfaceWhen", automationSurfaceField},
			{"endsWhen", automationEndsField},
		},
		Run: func(ctx context.Context, bridge AutomationBridge, args Args) ToolResult {
			return automationReport(func() (string, error) {
				draft := contracts.AutomationRequestSaveDraft{
					Prompt:      args.Str("prompt"),
					Schedule:    args.Str("schedule"),
					Timezone:    args.OptStr("timezone"),
					SurfaceWhen: args.OptStr("surfaceWhen"),
					EndsWhen:    args.OptStr("endsWhen"),
				}
				if policy := args.OptStr("policy"); policy != nil {
					value := contracts.AutomationRequestSaveDraftPolicy(*policy)
					draft.Policy = &value
				}
				view, err := bridge.Save(ctx, draft)
				if err != nil {
					return "", err
				}
				return "Automation scheduled.\n" + describeAutomation(view), nil
			})
		},
	},
	{
		Name:        "status",
		Description: "Read this task's automation: schedule, next run, how many times it has run, and its prompt.",
		Input:       Shape{},
		ReadOnly:    true,
		Run: func(ctx context.Context, bridge AutomationBridge, _ Args) ToolResult {
			return automationReport(func() (string, error) {
				automation, err := bridge.Read(ctx)
				if err != nil {
					return "", err
				}
				if automation == nil {
					return "This task has no automation.", nil
				}
				return describeAutomation(automation), nil
			})
		},
	},
	{
		Name:        "update",
		Description: "Change this task's existing automation without recreating it. Pass only the fields that change. Pause instead of stopping when the user wants to keep the automation around.",
		Input: Shape{
			{"prompt", automationPromptField.Optional()},
			{"schedule", automationScheduleField.Optional()},
			{"timezone", String().Optional().Describe("IANA timezone for the schedule.")},
			{"policy", automationPolicyField},
			{"surfaceWhen", automationSurfaceField},
			{"endsWhen", automationEndsField},
			{"paused", Boolean().Optional().Describe("Pause or resume without discarding the automation.")},
		},
		Run: func(ctx context.Context, bridge AutomationBridge, args Args) ToolResult {
			return automationReport(func() (string, error) {
				view, err := bridge.Update(ctx, domain.AutomationPatch{
					Prompt:      args.OptStr("prompt"),
					Schedule:    args.OptStr("schedule"),
					Timezone:    args.OptStr("timezone"),
					Policy:      executionPolicy(args),
					SurfaceWhen: args.OptStr("surfaceWhen"),
					EndsWhen:    args.OptStr("endsWhen"),
					Paused:      args.OptBool("paused"),
				})
				if err != nil {
					return "", err
				}
				return "Automation updated.\n" + describeAutomation(view), nil
			})
		},
	},
	{
		Name:        "stop",
		Description: "Delete this task's automation so it stops firing. Call this from inside a scheduled run the moment its stop condition is met — nothing else ends an automation, so skipping this leaves it running forever.",
		Input:       Shape{},
		Run: func(ctx context.Context, bridge AutomationBridge, _ Args) ToolResult {
			return automationReport(func() (string, error) {
				removed, err := bridge.Remove(ctx)
				if err != nil {
					return "", err
				}
				if removed {
					return "Automation stopped and removed.", nil
				}
				return "This task has no automation.", nil
			})
		},
	},
	{
		Name:        "list_all",
		Description: "List every automation across all tasks, not just this one.",
		Input:       Shape{},
		ReadOnly:    true,
		Run: func(ctx context.Context, bridge AutomationBridge, _ Args) ToolResult {
			return automationReport(func() (string, error) {
				automations, err := bridge.List(ctx)
				if err != nil {
					return "", err
				}
				if len(automations) == 0 {
					return "No automations exist.", nil
				}
				parts := make([]string, len(automations))
				for index, automation := range automations {
					parts[index] = "task " + automation.TaskID + "\n" + describeAutomation(automation)
				}
				return strings.Join(parts, "\n\n"), nil
			})
		},
	},
}

func FindingToolsFor(bridge FindingBridge) []BoundTool {
	return Bind(bridge, FindingTools)
}

// A coordinator has no automation of its own; it starts a thread to schedule recurring work.
var coordinatorAutomationTools = filterDefinitions(AutomationTools, func(tool Definition[AutomationBridge]) bool { return tool.ReadOnly })

func AutomationToolsFor(bridge AutomationBridge, role domain.CoordinationRole) []BoundTool {
	if role == domain.CoordinationRoleCoordinator {
		return Bind(bridge, coordinatorAutomationTools)
	}
	return Bind(bridge, AutomationTools)
}

const automationInstructions = `This task can schedule itself. When the user asks to repeat, babysit, poll, or watch something on a cadence, use the schedule tool instead of looping yourself or reaching for cron.`

var AutomationServer = Server{
	Name:        AutomationServerName,
	Definitions: append(infos(AutomationTools), infos(FindingTools)...),
	// Scheduled runs have nobody to approve anything, and these tools only reach the run's own automation.
	Grants: GrantsAll,
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Automations == nil {
			return nil, false
		}
		tools := AutomationToolsFor(run.Automations, run.role())
		if run.Findings != nil {
			tools = append(tools, FindingToolsFor(run.Findings)...)
		}
		return tools, true
	},
	Brief: func(offers Offers, _ BriefDialect, _ BriefRun) []string {
		if offers("schedule") {
			return []string{automationInstructions}
		}
		return nil
	},
}
