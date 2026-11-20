package agent

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// The bridges app tools call through, scoped to one thread. Ports the bridge types of src/main/tools.

type AutomationBridge interface {
	Read(ctx context.Context) (*domain.AutomationView, error)
	List(ctx context.Context) ([]*domain.AutomationView, error)
	Save(ctx context.Context, draft contracts.AutomationRequestSaveDraft) (*domain.AutomationView, error)
	Update(ctx context.Context, patch domain.AutomationPatch) (*domain.AutomationView, error)
	Remove(ctx context.Context) (bool, error)
}

type FindingBridge interface {
	Notify(ctx context.Context, report contracts.FindingReport) (contracts.FindingResult, error)
	NothingToReport(ctx context.Context, checked string) (contracts.FindingResult, error)
}

type ThreadBridge interface {
	List(ctx context.Context, query contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error)
	Read(ctx context.Context, threadID string, limit *float64, computer *string) (contracts.ThreadTranscript, error)
	Wait(ctx context.Context, threadID string, timeoutMs float64) (contracts.ThreadWaitResult, error)
	// The command lands on the thread named in it, the run's own when it names none.
	Command(ctx context.Context, command contracts.ExternalCommand, computer *string) (contracts.ThreadCommandResult, error)
}

type MemoryNote struct {
	Note string `json:"note"`
}

type CoordinationBridge interface {
	Report(ctx context.Context, state domain.CoordinationState, summary string) (contracts.FindingResult, error)
	Decide(ctx context.Context, request domain.DecisionRequest) (contracts.FindingResult, error)
	// Any change but an edit, which only the user makes.
	Remember(ctx context.Context, change contracts.ThreadRequestMemoryChange) (MemoryNote, error)
	Propose(ctx context.Context, proposals []*domain.ProposalRequest) (MemoryNote, error)
}

type BrowserBridge interface {
	Command(ctx context.Context, write contracts.BrowserWrite) error
	Read(ctx context.Context, read contracts.BrowserRead) (contracts.BrowserReadResult, error)
}

type SimulatorBridge interface {
	Command(ctx context.Context, write contracts.SimulatorWrite) error
	Read(ctx context.Context, read contracts.SimulatorRead) (contracts.SimulatorReadResult, error)
}

type TerminalBridge interface {
	Read(ctx context.Context, read contracts.TerminalRead) (contracts.TerminalReadResult, error)
}
