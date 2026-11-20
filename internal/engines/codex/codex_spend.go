package codex

import (
	"math"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// What a session's Codex threads spend, the main one and each child it spawns, read off the running
// total every usage report carries. A thread first seen in this session already holds what it spent
// before, as a resumed one does, so only its latest response counts; after that, each report adds
// what the total grew by. Only what arrives while a run is attached is counted: between runs the
// total still moves, so the next run counts from where it stands. A compaction's recount of the
// context it left carries no input or output, so it is no response and counts for nothing.
type CodexSpend struct {
	mu sync.Mutex
	// The last running total seen of each thread, by Codex's id for it.
	totals agent.RunningTotals
	// Reaches the run in flight, and drops what arrives while there is none.
	emit func(event agent.ProviderEvent)
}

func NewCodexSpend(emit func(event agent.ProviderEvent)) *CodexSpend {
	return &CodexSpend{totals: agent.RunningTotals{}, emit: emit}
}

func (spend *CodexSpend) Report(threadID string, usage protocol.ThreadTokenUsage) {
	total := usage.Total.TotalTokens
	latest := 0.0
	if usage.Last.InputTokens+usage.Last.OutputTokens > 0 {
		latest = math.Max(0, usage.Last.TotalTokens)
	}
	spend.mu.Lock()
	grown := spend.totals.Grow(threadID, total, math.Max(0, total-latest))
	spend.mu.Unlock()
	if grown != 0 {
		spend.emit(agent.EventSpend{Tokens: grown})
	}
}
