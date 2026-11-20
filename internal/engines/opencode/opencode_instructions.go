package opencode

import "github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"

var openCodeDialect = agent.BriefDialect{
	NativeWork:       "OpenCode sessions or subagents",
	CoordinatorReads: "Your own tools read and search; none edits files or runs shell commands.",
}

// What an OpenCode session is told beyond its prompt, and the app tools it is served.
func Brief(input *agent.ProviderRunInput) agent.RunBrief {
	return agent.RunBriefFor(input, openCodeDialect)
}
