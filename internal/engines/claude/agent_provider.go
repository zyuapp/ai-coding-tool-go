package claude

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/claude/protocol"
)

// coordinatorTools are the only built-in tools a coordinator gets: it reads and searches, and runs no
// agents inside its own session. Named rather than withheld, so a tool a newer Claude Code adds never
// reaches a coordinator unasked. Its app tools are MCP servers, which this list leaves alone.
var coordinatorTools = []string{"Read", "Grep", "Glob", "LSP", "WebSearch", "WebFetch", "Skill", "ToolSearch", "ListMcpResourcesTool", "ReadMcpResourceTool", "EnterPlanMode", "ExitPlanMode"}

var claudeNativeWorktree = "the Agent tool's worktree isolation"

var claudeDialect = agent.BriefDialect{
	NativeWork:       "Claude background tasks, sessions, or agents; native TaskOutput, Agent, and SendMessage cannot access them",
	NativeWorktree:   &claudeNativeWorktree,
	CoordinatorReads: "Your own tools read and search; none edits files or runs shell commands.",
}

const chromeInstructions = `The user's own Chrome answers the mcp__claude-in-chrome__ tools, and those tools drive the windows and tabs they already have on screen: when they ask for the external browser, for their browser, or for Chrome by name, use them rather than the AICodingTool browser panel or an open command through Bash. Everything else stays in the panel, which reads a page without disturbing what the user is looking at.`

const contextBeta = "context-1m-2025-08-07"

func appPlugins(root string) []protocol.Plugin {
	if root == "" {
		return nil
	}
	return []protocol.Plugin{{Path: root}}
}

// appCommand offers the app's own skills by their alias: Claude qualifies a plugin skill as `plugin:skill`.
func appCommand(command protocol.SlashCommand) *contracts.AvailableCommand {
	if !strings.HasPrefix(command.Name, agent.AppPluginName+":") {
		return &contracts.AvailableCommand{Name: command.Name, Description: command.Description, ArgumentHint: command.ArgumentHint, Aliases: command.Aliases}
	}
	return &contracts.AvailableCommand{
		Name:         agent.UnqualifiedSkillName(command.Name),
		Description:  strings.Replace(command.Description, "("+agent.AppPluginName+") ", "", 1),
		ArgumentHint: command.ArgumentHint,
	}
}

func settingSources(projectless bool) []string {
	if projectless {
		return []string{"user"}
	}
	return []string{"user", "project", "local"}
}

// discoverClaudeCommands asks an idle workspace session which commands it takes, and closes it.
func discoverClaudeCommands(ctx context.Context, open QueryFactory, pluginRoot, workspaceRoot string, projectless bool) ([]*contracts.AvailableCommand, error) {
	options := claudeProcess()
	options.Cwd = workspaceRoot
	options.SettingSources = settingSources(projectless)
	options.AllSkills = true
	options.Plugins = appPlugins(pluginRoot)
	session, err := open(ctx, options)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	commands, err := session.SupportedCommands(ctx)
	if err != nil {
		return nil, err
	}
	available := make([]*contracts.AvailableCommand, 0, len(commands))
	for _, command := range commands {
		available = append(available, appCommand(command))
	}
	return available, nil
}

var widerContext = regexp.MustCompile(`\[[^\]]*\]$`)

// modelNames is what a row calls its model, as both the short name a run passes and the full one it
// resolves to. A row for a wider context window carries that as a `[1m]` suffix on either name, which
// names the same model.
func modelNames(info protocol.ModelInfo) []string {
	names := []string{widerContext.ReplaceAllString(info.Value, "")}
	if info.ResolvedModel != nil {
		names = append(names, widerContext.ReplaceAllString(*info.ResolvedModel, ""))
	}
	return names
}

// discoverClaudeModels asks the installed Claude Code which catalogue models it can actually run, so
// no version number has to be written down per model. Nil means it runs all of them, which is what an
// older command that cannot answer at all is given the benefit of.
func discoverClaudeModels(ctx context.Context, open QueryFactory) []domain.AgentModel {
	session, err := open(ctx, claudeProcess())
	if err != nil {
		return nil
	}
	defer session.Close()
	rows, err := session.SupportedModels(ctx)
	if err != nil {
		return nil
	}
	var offered []string
	for _, row := range rows {
		offered = append(offered, modelNames(row)...)
	}
	var supported []domain.AgentModel
	for _, spec := range domain.ModelsFor(domain.BuiltInModels, string(domain.AgentEngineClaude)) {
		id := string(spec.ID)
		if slices.ContainsFunc(offered, func(name string) bool { return name == id || strings.HasPrefix(name, "claude-"+id+"-") }) {
			supported = append(supported, spec.ID)
		}
	}
	return supported
}

// drivesChrome says whether the run drives the user's own Chrome. A coordinator changes nothing
// itself, so it never does, as it never drives the browser panel.
func drivesChrome(input *agent.ProviderRunInput) bool {
	return input.ChromeBrowser && !isCoordinator(input)
}

func isCoordinator(input *agent.ProviderRunInput) bool {
	return input.CoordinationRole != nil && *input.CoordinationRole == domain.CoordinationRoleCoordinator
}

func runScope(input *agent.ProviderRunInput) *agent.ApprovalScope {
	return &agent.ApprovalScope{Policy: input.Policy, Channel: input.Channel}
}

// sessionKey is everything a session is built with. A run that disagrees with any of it needs a session of its own.
func sessionKey(input *agent.ProviderRunInput, brief agent.RunBrief) string {
	parts := append(slices.Clip(brief.Identity), agent.GrantsTool(agent.ReachWorkspace, runScope(input)), drivesChrome(input))
	return agent.IdentityKey(parts)
}

// Provider runs Claude turns on the sessions its channel's pool keeps warm.
type Provider struct {
	*agent.PooledProvider[*Session]
	queries    QueryFactory
	pluginRoot string
}

// NewProvider is a provider whose threads' sessions are kept in pool. A nil queries starts the
// installed Claude Code; pluginRoot is the app's own plugin, empty for none.
func NewProvider(pool *agent.SessionPool, queries QueryFactory, pluginRoot string) *Provider {
	if pool == nil {
		pool = agent.NewSessionPool(nil, 0)
	}
	if queries == nil {
		queries = StartQuery
	}
	return &Provider{PooledProvider: agent.NewPooledProvider[*Session](pool), queries: queries, pluginRoot: pluginRoot}
}

func (provider *Provider) Execute(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	brief := agent.RunBriefFor(input, claudeDialect)
	key := sessionKey(input, brief)
	return provider.Run(input, key, agent.SessionOpener{
		Open: func(hooks agent.SessionHooks) agent.PooledSession { return NewSession(key, hooks) },
		Start: func(pooled agent.PooledSession) {
			session := pooled.(*Session)
			_ = session.Open(func(canUseTool protocol.CanUseTool, hooks []protocol.HookEvent) (Query, error) {
				return provider.queries(context.Background(), provider.options(input, brief, canUseTool, hooks, session))
			}, input)
		},
	})
}

// LabelThread keeps the title the app's: Claude keeps no record of its own to name.
func (provider *Provider) LabelThread(string, string) bool { return false }

// ArchiveThread leaves filing a thread away to the app: Claude keeps no archive of its own.
func (provider *Provider) ArchiveThread(string, *domain.Continuation, bool) bool { return false }

// StopProcess reaches the thread's own session, so work that outlived the run that started it can still be stopped.
func (provider *Provider) StopProcess(taskID, processID string) bool {
	session, ok := provider.Session(taskID)
	if ok {
		session.StopProcess(processID)
	}
	return ok
}

func (provider *Provider) options(input *agent.ProviderRunInput, brief agent.RunBrief, canUseTool protocol.CanUseTool, hooks []protocol.HookEvent, session *Session) protocol.Options {
	options := claudeProcess()
	options.Cwd = input.WorkspaceRoot
	options.Stderr = session.RecordProcessStderr
	if resources := session.hooks.Resources; resources != nil {
		options.OnSpawn = resources.RegisterProcess
	}
	if isCoordinator(input) {
		options.Tools = slices.Clone(coordinatorTools)
	}
	options.DisallowedTools = agent.WithheldTools(input.Channel)
	continuation, resumes := agent.ContinuationOf(input)
	if resumes {
		options.Resume = continuation
		options.ForkSession = input.ForkContinuation
	}
	options.PermissionMode = ClaudePermissionMode(input.Policy)
	options.AllowDangerouslySkipPermissions = agent.GrantsTool(agent.ReachWorkspace, runScope(input))
	options.Model = string(input.Model)
	if effort := effortFlag(input.Model, input.Effort); effort != nil {
		options.Effort = string(*effort)
	}
	options.Betas = []string{contextBeta}
	chrome := drivesChrome(input)
	if chrome {
		options.ExtraArgs = []protocol.ExtraArg{{Name: "chrome"}}
	}
	if available, ok := input.ComputerUse.(domain.ComputerUseRunConfigAvailable); ok {
		stdio := "stdio"
		options.McpServers = []protocol.NamedMcpServer{{Name: "cua-driver", Config: protocol.McpServerConfig{
			Type: &stdio, Command: available.MCP.Command, Args: available.MCP.Args, Env: available.MCP.Env,
		}}}
	}
	for _, set := range brief.Tools {
		options.SdkMcpServers = append(options.SdkMcpServers, claudeMcpServer(set.Server, set.Tools))
	}
	instructions := slices.Clone(brief.Instructions)
	if chrome {
		instructions = append(instructions, chromeInstructions)
	}
	appended := strings.Join(instructions, "\n\n")
	options.SystemPrompt = protocol.SystemPrompt{Kind: protocol.SystemPromptPreset, Append: &appended}
	options.SettingSources = settingSources(input.Projectless)
	options.AllSkills = true
	options.Plugins = appPlugins(provider.pluginRoot)
	options.ForwardSubagentText = true
	options.IncludePartialMessages = true
	options.CanUseTool = canUseTool
	options.Hooks = hooks
	return options
}
