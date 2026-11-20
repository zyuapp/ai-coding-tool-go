package application

import (
	"slices"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Where a scheduled tick meets the workspace: whether it can run at all, what a run of it raises,
// and what a schedule turned away says for itself. A function belongs here when it needs the whole
// workspace or has to tell the desktop.

// Who the thread is working for, if anyone: TickRefusalBusyUser or TickRefusalBusyAgent, or false.
// Any status but idle counts, so a send still resolving, a message still queued, and a usage-limit
// pause all hold a tick back: a run would clear the pause.
func whoIsBusy(state *WorkspaceState, taskID string) (TickRefusal, bool) {
	if ThreadActivityThreadStatus(state, taskID) == ThreadStatusIdle {
		return "", false
	}
	if UsersTurn(state, taskID) {
		return TickRefusalBusyUser, true
	}
	return TickRefusalBusyAgent, true
}

// Why this tick cannot run, or false when it can. A thread its own user is working in is named apart
// from one an agent is busy in, and answered first: the two would otherwise both read as busy, and
// only the second is a schedule failing to get a turn.
func WhyTickCannotRun(state *WorkspaceState, fire contracts.AutomationFire, thread *domain.Thread, project *domain.Project) (TickRefusal, bool) {
	if thread == nil {
		return TickRefusalNoThread, true
	}
	if thread.ResourceStop != nil {
		return TickRefusalBusyUser, true
	}
	if busy, ok := whoIsBusy(state, fire.TaskID); ok {
		return busy, true
	}
	if thread.ArchivedAt != nil {
		return TickRefusalArchived, true
	}
	// A thread in a project runs in that project's checkout, so it waits until there is one.
	if thread.ProjectID != nil && *thread.ProjectID != "" && (project == nil || project.WorkspaceID == nil || *project.WorkspaceID == "") {
		return TickRefusalNoWorkspace, true
	}
	return "", false
}

// Only a scheduled run may raise one: a turn the user is present for answers them directly.
func RaisedFinding(state *WorkspaceState, taskID string, report contracts.FindingReport) Step {
	if ScheduledRun(state, taskID) == nil {
		return Step{}
	}
	thread := threadsFind(state.Threads, taskID)
	if thread == nil {
		return Step{}
	}
	key := ""
	if report.Key != nil {
		key = *report.Key
	}
	raised := domain.IsNews(thread, key)
	// A thread the user is watching cannot have missed it.
	seen := state.Focused && ThreadOnScreen(state, taskID)
	next := NotifiedRun(state, taskID, report, Now(), seen)
	if raised {
		next.Effects = Announced(state, thread, report.Headline, NoticeReasonFinding)
	} else {
		next.Effects = []WorkspaceEffect{}
	}
	return next
}

// A tick with nowhere to run is acknowledged and dropped, which the scheduler counts. A schedule
// turned away over and over is a silence of its own, so the thread says so out loud, once.
func DeclinedTick(state *WorkspaceState, fire contracts.AutomationFire, thread *domain.Thread, refusal TickRefusal) Step {
	acked := []WorkspaceEffect{WorkspaceEffectAutomationAck{Ack: contracts.AutomationAck{AutomationID: fire.AutomationID, RunID: fire.RunID, Started: false}}}
	var automation *domain.AutomationView
	for _, item := range state.Automations {
		if item.ID == fire.AutomationID {
			automation = item
			break
		}
	}
	if thread == nil || automation == nil || domain.DeclineCount(&automation.Automation)+1 < domain.DeclinesBeforeSurfacing {
		return Step{Effects: acked}
	}
	// A thread its own user is working in is not a broken schedule: they are here, and the ticks resume when they stop.
	if refusal == TickRefusalBusyUser {
		return Step{Effects: acked}
	}
	key := "declined:" + automation.ID
	// Once per stretch of declines, however many the scheduler counted before the workspace saw one:
	// ticks turned away where no reducer was listening still climb the count. A notice from an earlier
	// stretch was about a schedule that has run since, so it does not stand in for this one.
	since := 0.0
	if automation.LastRunAt != nil {
		since = *automation.LastRunAt
	}
	said := !domain.IsNews(thread, key) || slices.ContainsFunc(thread.Findings, func(finding *domain.AutomationFinding) bool {
		return finding.Key != nil && *finding.Key == key && finding.At >= since
	})
	if said {
		return Step{Effects: acked}
	}
	stopped := automation.CreatedAt
	if automation.LastRunAt != nil {
		stopped = *automation.LastRunAt
	}
	headline := "This automation has not been able to run since " + localeDateTime(stopped)
	seen := state.Focused && ThreadOnScreen(state, thread.ID)
	return Step{
		Edits:   []OwnerEdit{FindingIn(ThreadsOf(state), thread.ID, domain.FindingReport{Headline: headline, Key: key}, Now(), seen)},
		Effects: slices.Concat(acked, Announced(state, thread, headline, NoticeReasonFinding)),
	}
}

// localeDateTime is new Date(ms).toLocaleString() in an en-US runtime, in local time.
func localeDateTime(ms float64) string {
	return time.UnixMilli(int64(ms)).Format("1/2/2006, 3:04:05 PM")
}
