package application

import (
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// WithVerdict leaves a settled turn's verdict on its thread. Only a thread the user was not already
// looking at is marked unread by it; a thread already filed away is left none.
func WithVerdict(state *WorkspaceState, taskID string, outcome domain.ThreadOutcome) *WorkspaceState {
	return Afterward(state, SliceTransition{Edits: []OwnerEdit{VerdictIn(ThreadsOf(state), taskID, outcome, !ThreadOnScreen(state, taskID))}})
}

// ScheduledRun is the run a scheduled tick is executing, and nil for any other run.
func ScheduledRun(state *WorkspaceState, taskID string) ActiveRun {
	active := state.ActiveRuns[taskID]
	if record := RunRecord(active); record != nil && record.Origin == ActiveRunRunningOriginAutomation {
		return active
	}
	return nil
}

// NotifiedRun records what a run found and marks the run as having answered for itself. Anything the
// thread was not already carrying is news, so the run surfaces; only a duplicate leaves the silence intact.
func NotifiedRun(state *WorkspaceState, taskID string, report contracts.FindingReport, at float64, seen bool) Step {
	if ScheduledRun(state, taskID) == nil {
		return Step{}
	}
	key := ""
	if report.Key != nil {
		key = *report.Key
	}
	raised := slices.ContainsFunc(state.Threads, func(thread *domain.Thread) bool { return thread.ID == taskID && domain.IsNews(thread, key) })
	finding := domain.FindingReport{Headline: report.Headline, Key: key}
	if report.Detail != nil {
		finding.Detail = *report.Detail
	}
	return Step{Edits: []OwnerEdit{NotifiedIn(RunsOf(state), taskID, raised, report.Key), FindingIn(ThreadsOf(state), taskID, finding, at, seen)}}
}

// NothingToReport is a run saying it looked and found nothing. It never retracts what it already surfaced.
func NothingToReport(state *WorkspaceState, taskID string, checked string, at float64) Step {
	if ScheduledRun(state, taskID) == nil {
		return Step{}
	}
	checkedAt := &domain.FindingsThreadLastChecked{At: at, Note: checked}
	return Step{Edits: []OwnerEdit{AcknowledgedIn(RunsOf(state), taskID), EditThread(ThreadsOf(state), taskID, func(*domain.Thread) ThreadPatch {
		return func(next *domain.Thread) { next.LastChecked = checkedAt }
	})}}
}

// howRunEnded is how the run left off: "finished", "failed" or "cancelled". Only a run that reached an end of its own finished or failed.
func howRunEnded(status domain.RunStatus) string {
	switch status {
	case "succeeded":
		return "finished"
	case "failed":
		return "failed"
	}
	return "cancelled"
}

// whatRunSaid is what the run said for itself. Only a quiet scheduled run that answered and raised nothing is silent (nil).
func whatRunSaid(active TurnEndRun) *RunSurfacing {
	surfacing := RunSurfacingAttended
	switch {
	case active.Origin != TurnEndRunOriginAutomation:
	case !active.Quiet:
		surfacing = RunSurfacingLoud
	case !active.Acknowledged:
		surfacing = RunSurfacingNoAnswer
	case active.Notified:
		surfacing = RunSurfacingReported
	default:
		return nil
	}
	return &surfacing
}

// WhyTurnSurfaces is why a turn ending with this status reaches the user, or nil when it earned its
// silence. One that ends with no run behind it was the user's own. An empty status is undefined.
func WhyTurnSurfaces(run *TurnEndRun, status domain.RunStatus) *RunSurfacing {
	ending := howRunEnded(status)
	if ending != "finished" {
		surfacing := RunSurfacing(ending)
		return &surfacing
	}
	if run != nil {
		return whatRunSaid(*run)
	}
	surfacing := RunSurfacingAttended
	return &surfacing
}

// SettledHeadline is the line a settling run puts on the desktop, false when it has none to put there.
// A scheduled run speaks through what it finds, so only a failure of one is worth taking to the user this way.
func SettledHeadline(surfacing *RunSurfacing, message *string) (string, bool) {
	if surfacing == nil {
		return "", false
	}
	switch *surfacing {
	case RunSurfacingFailed:
		if message != nil {
			if trimmed := runsJSTrim(*message); trimmed != "" {
				return trimmed, true
			}
		}
		return "The run failed.", true
	case RunSurfacingAttended:
		return "The run finished.", true
	}
	return "", false
}

// runsJSTrim trims s as JavaScript's String.prototype.trim does.
func runsJSTrim(s string) string {
	return strings.TrimFunc(s, runsJSSpace)
}

// runsJSSpace reports whether JavaScript's trim takes r for whitespace.
func runsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}
