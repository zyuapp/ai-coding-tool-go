package protocol

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// SDKVersion is the Agent SDK whose wire behaviour this package reproduces.
const SDKVersion = "0.3.330"

// SystemPromptKind says how a session's system prompt is set.
type SystemPromptKind int

const (
	// SystemPromptEmpty is the SDK's default when none is given: an empty prompt.
	SystemPromptEmpty SystemPromptKind = iota
	// SystemPromptCustom replaces Claude Code's prompt with Text.
	SystemPromptCustom
	// SystemPromptPreset keeps Claude Code's own prompt, with Append after it.
	SystemPromptPreset
)

// SystemPrompt is a session's system prompt.
type SystemPrompt struct {
	Kind SystemPromptKind
	Text string
	// Append is added to the preset prompt; nil adds nothing.
	Append *string
}

// McpServerConfig is an MCP server Claude Code starts itself (stdio) or connects to (sse, http).
type McpServerConfig struct {
	Type    *string           `json:"type,omitzero"`
	Command string            `json:"command,omitzero"`
	Args    []string          `json:"args,omitzero"`
	Env     map[string]string `json:"env,omitzero"`
	URL     string            `json:"url,omitzero"`
	Headers map[string]string `json:"headers,omitzero"`
	Timeout *float64          `json:"timeout,omitzero"`
}

// NamedMcpServer is one entry of --mcp-config, in the order given.
type NamedMcpServer struct {
	Name   string
	Config McpServerConfig
}

// ExtraArg is a flag passed through as written: `--name`, or with a value.
type ExtraArg struct {
	Name  string
	Value *string
}

// Plugin is a local plugin directory.
type Plugin struct {
	Path             string
	SkipMcpDiscovery bool
}

// CanUseTool decides one tool call. A nil result leaves the request unanswered.
type CanUseTool func(ctx context.Context, request PermissionRequest) (*PermissionResult, error)

// HookCallback answers one hook with its output object. A nil output answers with null.
type HookCallback func(ctx context.Context, input HookInput, toolUseID *string) (any, error)

// HookMatcher is a group of callbacks for one hook event, optionally narrowed by a matcher.
type HookMatcher struct {
	Matcher *string
	Hooks   []HookCallback
	Timeout *float64
}

// HookEvent registers matchers for one hook event ("PreToolUse", "SubagentStop", ...).
type HookEvent struct {
	Event    string
	Matchers []HookMatcher
}

// Options are one Claude Code process's options, named as the Agent SDK names them.
type Options struct {
	// Executable is the Claude Code binary (pathToClaudeCodeExecutable).
	Executable string
	Cwd        string
	// Env is laid over the environment the process would otherwise get. The SDK's own variables
	// are added after it.
	Env map[string]string
	// Command makes the process's command; nil uses exec.Command. Its Env, when set, is the base
	// environment Env is laid over.
	Command func(name string, args ...string) *exec.Cmd

	// Tools restricts the built-in tools when non-nil; an empty, non-nil list offers none.
	Tools []string
	// DefaultTools passes --tools=default when Tools is nil.
	DefaultTools    bool
	AllowedTools    []string
	DisallowedTools []string
	Resume          string
	ForkSession     bool
	PermissionMode  string
	// AllowDangerouslySkipPermissions lets the session switch to bypassPermissions.
	AllowDangerouslySkipPermissions bool
	Model                           string
	Effort                          string
	Betas                           []string
	MaxTurns                        int
	ExtraArgs                       []ExtraArg
	// McpServers are started by Claude Code itself, passed as --mcp-config.
	McpServers []NamedMcpServer
	// SdkMcpServers run in this process and are reached over the control channel.
	SdkMcpServers []*SdkMcpServer
	SystemPrompt  SystemPrompt
	// SettingSources, when non-nil, names the settings Claude Code reads; empty reads none.
	SettingSources []string
	// AllSkills lets the Skill tool load every skill (skills: "all").
	AllSkills bool
	// Skills lets the Skill tool load these skills only.
	Skills                 []string
	Plugins                []Plugin
	ForwardSubagentText    bool
	IncludePartialMessages bool
	// JSONSchema asks for structured output in this shape (outputFormat json_schema).
	JSONSchema jsontext.Value
	CanUseTool CanUseTool
	Hooks      []HookEvent

	// Stderr takes the process's stderr as it arrives. When set, an exit error carries none of it,
	// as with a custom spawner; when nil, the exit error ends with its redacted tail.
	Stderr func(text string)
	// OnSpawn is told the process id once it starts.
	OnSpawn func(pid int)
}

func flag(name, value string) string { return name + "=" + value }

// flagValue writes a flag as the SDK does for values that might look like flags themselves.
func flagValue(name, value string) []string {
	if len(value) > 1 && strings.HasPrefix(value, "-") {
		return []string{flag(name, value)}
	}
	return []string{name, value}
}

var invalidSkillCharacters = regexp.MustCompile(`[(),\x00-\x1f\x7f-\x9f]`)

func skillRule(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("Skill names must be non-empty strings.")
	}
	quoted, _ := marshal(name)
	if name != strings.TrimSpace(name) {
		return "", fmt.Errorf("Invalid skill name %s: leading or trailing whitespace is not allowed — the Skill tool trims the invoked name, so a padded rule can never match. Remove the padding.", quoted)
	}
	if invalidSkillCharacters.MatchString(name) {
		return "", fmt.Errorf("Invalid skill name %s: parentheses, commas, and control characters are not allowed in skill names. Skill names match the skill's directory name (or 'plugin:skill' for plugin-qualified skills); rename the skill if its directory name contains these characters.", quoted)
	}
	if name == "*" {
		return "", errors.New("Invalid skill name '*': use skills: 'all' to enable every skill.")
	}
	if strings.HasSuffix(name, ":*") || strings.HasSuffix(name, " *") {
		return "", fmt.Errorf("Invalid skill name %s: wildcard-suffix names are not allowed; list each skill by its exact name.", quoted)
	}
	return "Skill(" + name + ")", nil
}

// Args is the command line the SDK builds for these options, in its order.
func (o *Options) Args() ([]string, error) {
	args := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	allowed := append([]string(nil), o.AllowedTools...)
	if o.AllSkills || o.Skills != nil {
		var rules []string
		if o.AllSkills {
			rules = []string{"Skill"}
		} else {
			for _, name := range o.Skills {
				rule, err := skillRule(name)
				if err != nil {
					return nil, err
				}
				rules = append(rules, rule)
			}
		}
		seen := map[string]bool{}
		for _, tool := range allowed {
			seen[tool] = true
		}
		for _, rule := range rules {
			if !seen[rule] {
				allowed = append(allowed, rule)
			}
		}
	}
	if o.Effort != "" {
		args = append(args, flag("--effort", o.Effort))
	}
	if o.MaxTurns != 0 {
		args = append(args, flag("--max-turns", fmt.Sprint(o.MaxTurns)))
	}
	if o.Model != "" {
		args = append(args, flag("--model", o.Model))
	}
	if len(o.Betas) > 0 {
		args = append(args, flag("--betas", strings.Join(o.Betas, ",")))
	}
	if len(o.JSONSchema) > 0 {
		compact := jsontext.Value(append([]byte(nil), o.JSONSchema...))
		if err := compact.Compact(); err != nil {
			return nil, fmt.Errorf("jsonSchema: %w", err)
		}
		args = append(args, flag("--json-schema", string(compact)))
	}
	if o.CanUseTool != nil {
		args = append(args, "--permission-prompt-tool=stdio")
	}
	if o.Resume != "" {
		args = append(args, flag("--resume", o.Resume))
	}
	if len(allowed) > 0 {
		args = append(args, flag("--allowedTools", strings.Join(allowed, ",")))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, flag("--disallowedTools", strings.Join(o.DisallowedTools, ",")))
	}
	if o.Tools != nil {
		args = append(args, flag("--tools", strings.Join(o.Tools, ",")))
	} else if o.DefaultTools {
		args = append(args, "--tools=default")
	}
	if len(o.McpServers) > 0 {
		config, err := mcpConfig(o.McpServers)
		if err != nil {
			return nil, err
		}
		args = append(args, flagValue("--mcp-config", config)...)
	}
	if o.SettingSources != nil {
		args = append(args, flag("--setting-sources", strings.Join(o.SettingSources, ",")))
	}
	if o.PermissionMode != "" {
		args = append(args, flag("--permission-mode", o.PermissionMode))
	}
	if o.AllowDangerouslySkipPermissions {
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	if o.IncludePartialMessages {
		args = append(args, "--include-partial-messages")
	}
	for _, plugin := range o.Plugins {
		if plugin.SkipMcpDiscovery {
			args = append(args, flag("--plugin-dir-no-mcp", plugin.Path))
		} else {
			args = append(args, flag("--plugin-dir", plugin.Path))
		}
	}
	if o.ForkSession {
		args = append(args, "--fork-session")
	}
	for _, extra := range o.ExtraArgs {
		if extra.Value == nil {
			args = append(args, "--"+extra.Name)
		} else {
			args = append(args, flagValue("--"+extra.Name, *extra.Value)...)
		}
	}
	return args, nil
}

// mcpConfig writes {"mcpServers": {...}} with the servers in the order given.
func mcpConfig(servers []NamedMcpServer) (string, error) {
	var out jsontext.Encoder
	var buffer strings.Builder
	out.Reset(&buffer)
	if err := out.WriteToken(jsontext.BeginObject); err != nil {
		return "", err
	}
	if err := out.WriteToken(jsontext.String("mcpServers")); err != nil {
		return "", err
	}
	if err := out.WriteToken(jsontext.BeginObject); err != nil {
		return "", err
	}
	for _, server := range servers {
		config, err := marshal(server.Config)
		if err != nil {
			return "", err
		}
		if err := out.WriteToken(jsontext.String(server.Name)); err != nil {
			return "", err
		}
		if err := out.WriteValue(config); err != nil {
			return "", err
		}
	}
	if err := out.WriteToken(jsontext.EndObject); err != nil {
		return "", err
	}
	if err := out.WriteToken(jsontext.EndObject); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buffer.String(), "\n"), nil
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Environment is the environment the SDK starts Claude Code with: base, then Env over it, then the
// SDK's own variables.
func (o *Options) Environment(base []string) []string {
	env := map[string]string{}
	var order []string
	set := func(key, value string) {
		if _, ok := env[key]; !ok {
			order = append(order, key)
		}
		env[key] = value
	}
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			set(key, value)
		}
	}
	for key, value := range o.Env {
		set(key, value)
	}
	if env["CLAUDE_CODE_ENTRYPOINT"] == "" {
		set("CLAUDE_CODE_ENTRYPOINT", "sdk-ts")
	}
	if env["CLAUDE_AGENT_SDK_VERSION"] == "" {
		set("CLAUDE_AGENT_SDK_VERSION", SDKVersion)
	}
	readsState := false
	for key := range env {
		if strings.ToUpper(key) == "CLAUDE_CODE_SDK_READS_SESSION_STATE" {
			readsState = true
		}
	}
	if !readsState {
		set("CLAUDE_CODE_SDK_READS_SESSION_STATE", "1")
	}
	delete(env, "NODE_OPTIONS")
	if truthy(env["DEBUG_CLAUDE_AGENT_SDK"]) {
		set("DEBUG", "1")
	} else {
		delete(env, "DEBUG")
	}
	out := make([]string, 0, len(env))
	for _, key := range order {
		if value, ok := env[key]; ok {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// initializeRequest is the initialize control request for these options, and the hook callbacks
// by the ids it registers them under.
func (o *Options) initializeRequest(manifests map[string]McpManifest) (InitializeRequest, map[string]HookCallback) {
	request := InitializeRequest{Subtype: "initialize"}
	callbacks := map[string]HookCallback{}
	next := 0
	for _, event := range o.Hooks {
		if len(event.Matchers) == 0 {
			continue
		}
		if request.Hooks == nil {
			request.Hooks = map[string][]HookMatcherRegistration{}
		}
		registrations := []HookMatcherRegistration{}
		for _, matcher := range event.Matchers {
			ids := []string{}
			for _, hook := range matcher.Hooks {
				id := fmt.Sprintf("hook_%d", next)
				next++
				callbacks[id] = hook
				ids = append(ids, id)
			}
			registrations = append(registrations, HookMatcherRegistration{Matcher: matcher.Matcher, HookCallbackIDs: ids, Timeout: matcher.Timeout})
		}
		request.Hooks[event.Event] = registrations
	}
	for _, server := range o.SdkMcpServers {
		request.SdkMcpServers = append(request.SdkMcpServers, server.Name)
	}
	if len(manifests) > 0 {
		request.SdkMcpServerManifests = manifests
	}
	if len(o.JSONSchema) > 0 {
		request.JSONSchema = o.JSONSchema
	}
	switch o.SystemPrompt.Kind {
	case SystemPromptEmpty:
		request.SystemPrompt = []string{""}
	case SystemPromptCustom:
		request.SystemPrompt = []string{o.SystemPrompt.Text}
	case SystemPromptPreset:
		request.AppendSystemPrompt = o.SystemPrompt.Append
	}
	if !o.AllSkills && o.Skills != nil {
		request.Skills = o.Skills
		if request.Skills == nil {
			request.Skills = []string{}
		}
	}
	if o.ForwardSubagentText {
		request.ForwardSubagentText = ptr(true)
	}
	return request, callbacks
}

// manifestsDisabled reports whether the app was asked to leave in-process servers to handshake
// over the control channel.
func manifestsDisabled() bool {
	return truthy(os.Getenv("CLAUDE_AGENT_SDK_DISABLE_MCP_MANIFESTS"))
}
