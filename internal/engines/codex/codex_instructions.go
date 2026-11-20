package codex

import "github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"

// A worktree keeps its git data in the project's .git, which its sandbox cannot write, and no set of
// writable folders could open that much without letting a command plant what git runs outside the
// sandbox. So a commit asks to run outside it, which the user or the reviewer decides.
const ownCheckoutCommits = "If your sandbox keeps a git command such as git add or git commit from writing the git data your checkout keeps in the main checkout's .git folder, ask to run it outside the sandbox, and report blocked only if that is refused. That is your checkout's own history, not the user's files."

var codexDialect = agent.BriefDialect{
	NativeWork: "Codex sessions or subagents",
	// A coordinator's sandbox is read-only, and the app refuses every escalation out of it.
	CoordinatorReads:   "Your shell is read-only: use it to read and search, and never ask to run a command outside its sandbox.",
	OwnCheckoutCommits: ptr(ownCheckoutCommits),
}

// What a Codex thread is told beyond its prompt, and the app tools it is served.
func CodexBrief(input *agent.ProviderRunInput) agent.RunBrief {
	return agent.RunBriefFor(input, codexDialect)
}
