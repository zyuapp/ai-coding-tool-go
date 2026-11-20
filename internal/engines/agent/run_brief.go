package agent

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// What every engine tells a run beyond its prompt, the tools it offers, and what a warm session must agree on.
type RunBrief struct {
	Instructions []string
	Tools        []ServedToolSet
	// A run whose identity differs from a warm session's needs a session of its own. Engines append
	// their own parts and compare the IdentityKey of the whole.
	Identity []any
}

const linkInstructions = `Only Markdown links are clickable in your output. Link web pages as [label](https://example.com), workspace files as [label](/absolute/path:line), pushed commits as [short hash](https://github.com/owner/repo/commit/<hash>) with their repository's real host and path, and other threads as [title](aicodingtool://thread/<id>). Omit the line when it is unavailable.`
const computerUseInstructions = `When a requested outcome lives in another application's interface, use the provided computer-use MCP tools. Never invoke a separately installed cua-driver through Bash. Observe the exact target before every action and verify the result afterward. Prefer accessibility targets, then screenshot coordinates, and use foreground delivery only after background delivery fails.`
const workflowInstructions = `For ordinary AICodingTool operations, prefer the AICodingTool tools. They are defaults, not restrictions on the user's workflow: follow the user's explicitly requested target and method, and verify the result in the requested target before reporting success.`

func offers(tools []ServedToolSet, server string) Offers {
	var set *ServedToolSet
	for index := range tools {
		if tools[index].Server == server {
			set = &tools[index]
			break
		}
	}
	return func(name string) bool {
		if set == nil {
			return false
		}
		if name == "" {
			return true
		}
		for _, tool := range set.Tools {
			if tool.Name == name {
				return true
			}
		}
		return false
	}
}

// The brief every engine gives a run: the same words, gated on the same tools, whichever engine reads it.
func RunBriefFor(input *ProviderRunInput, dialect BriefDialect) RunBrief {
	tools := RunTools(input)
	var instructions []string
	if _, unavailable := input.ComputerUse.(domain.ComputerUseRunConfigUnavailable); !unavailable {
		instructions = append(instructions, computerUseInstructions)
	}
	instructions = append(instructions, linkInstructions)
	run := BriefRun{CoordinationRole: input.CoordinationRole, WorkspaceRoot: input.WorkspaceRoot, ProjectRoot: input.ProjectRoot}
	for _, server := range ToolServers() {
		if server.Brief != nil {
			instructions = append(instructions, server.Brief(offers(tools, server.Name), dialect, run)...)
		}
	}
	if len(tools) > 0 {
		instructions = append(instructions, workflowInstructions)
	}
	if input.Channel == contracts.RunChannelSide {
		instructions = append(instructions, SideChatInstructions)
	}
	return RunBrief{Instructions: instructions, Tools: tools, Identity: briefIdentity(input, instructions, tools)}
}

// The run's identity parts, in the order TypeScript lists them.
func briefIdentity(input *ProviderRunInput, instructions []string, tools []ServedToolSet) []any {
	var computerUse any
	switch config := input.ComputerUse.(type) {
	case domain.ComputerUseRunConfigAvailable:
		computerUse = config.MCP
	case domain.ComputerUseRunConfigSetupRequired:
		computerUse = "setup-required"
	default:
		computerUse = "unavailable"
	}
	var role any
	if input.CoordinationRole != nil {
		role = *input.CoordinationRole
	}
	served := make([]any, 0, len(tools))
	for _, set := range tools {
		names := make([]string, 0, len(set.Tools))
		for _, tool := range set.Tools {
			names = append(names, tool.Name)
		}
		served = append(served, []any{set.Server, names})
	}
	if instructions == nil {
		instructions = []string{}
	}
	return []any{input.Channel, input.WorkspaceRoot, input.Projectless, computerUse, role, instructions, served}
}

// The identity as one comparable string (TS JSON.stringify of it).
func IdentityKey(parts []any) string {
	encoded, err := jsonx.Marshal(parts)
	if err != nil {
		return ""
	}
	return string(encoded)
}
