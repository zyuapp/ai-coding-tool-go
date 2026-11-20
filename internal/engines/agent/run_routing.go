package agent

import (
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Far longer than any honest scheduled run, so only a run that never reports back reaches it.
const AutomationSettleTimeout = 6 * time.Hour

// The tick as the window receives it. Whether it may be quiet is the scheduler's decision, not this one's.
func AutomationFireFor(automation *domain.Automation, runID string, tick domain.TickKind) contracts.AutomationFire {
	fire := contracts.AutomationFire{
		AutomationID: automation.ID,
		TaskID:       automation.TaskID,
		RunID:        runID,
		Prompt:       automation.Prompt,
		Policy:       automation.Policy,
		EndsWhen:     automation.EndsWhen,
		RunNumber:    automation.RunCount + 1,
	}
	// The sentence only travels with a tick that may act on it: a watched run is framed as any other.
	if tick.Quiet {
		fire.Quiet = new(true)
		fire.SurfaceWhen = automation.SurfaceWhen
	}
	if tick.Unattended {
		fire.Unattended = new(true)
	}
	return fire
}

// A scheduled run that never reaches a terminal status would hold its automation's turnstile shut for
// good, skipping every later tick in silence. Past the bound the tick is called failed.
func SettledWithin(settled <-chan domain.AutomationRunStatus, bound time.Duration) domain.AutomationRunStatus {
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	select {
	case status := <-settled:
		return status
	case <-deadline.C:
		return domain.AutomationRunStatusFailed
	}
}
