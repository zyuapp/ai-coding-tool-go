package domain

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

var tickSteps = []float64{5_000, 10_000, 15_000, 30_000, 60_000, 120_000, 300_000, 600_000, 1_800_000, 3_600_000}

const maxTicks = 6

// minBarWidth: a lane narrower than this reads as nothing at all, so instants are drawn at least this wide.
const minBarWidth = 0.6

// AgentStateIn: a workflow that ended took its unfinished agents with it, whatever its last frame said of them.
func AgentStateIn(workflow *Workflow, agent *WorkflowAgent) WorkflowAgentState {
	if workflow.Status == WorkflowStatusRunning {
		return agent.State
	}
	if agent.State == WorkflowAgentStateQueued || agent.State == WorkflowAgentStateRunning {
		return WorkflowAgentStateStopped
	}
	return agent.State
}

// WorkflowNow is the moment lanes are measured against: the workflow's end once it has one, otherwise the clock.
func WorkflowNow(workflow *Workflow, clock float64) float64 {
	if workflow.FinishedAt != nil {
		return *workflow.FinishedAt
	}
	return clock
}

func WorkflowAgentsDone(workflow *Workflow) int {
	done, _ := WorkflowAgentCounts(workflow)
	return done
}

func WorkflowAgentCounts(workflow *Workflow) (done int, failed int) {
	for _, agent := range workflow.Agents {
		if agent.State == WorkflowAgentStateDone || agent.State == WorkflowAgentStateError {
			done++
		}
		if agent.State == WorkflowAgentStateError {
			failed++
		}
	}
	return done, failed
}

// WorkflowGroups is phase order first, then spawn order, with anything the script never gave a phase left to the end.
func WorkflowGroups(workflow *Workflow) []*WorkflowGroup {
	agents := slices.Clone(workflow.Agents)
	slices.SortStableFunc(agents, func(left, right *WorkflowAgent) int { return cmp.Compare(left.Index, right.Index) })
	byPhase := map[float64][]*WorkflowAgent{}
	var loose []*WorkflowAgent
	for _, agent := range agents {
		if agent.PhaseIndex == nil {
			loose = append(loose, agent)
		} else {
			byPhase[*agent.PhaseIndex] = append(byPhase[*agent.PhaseIndex], agent)
		}
	}
	phases := slices.Clone(workflow.Phases)
	slices.SortStableFunc(phases, func(left, right *WorkflowPhase) int { return cmp.Compare(left.Index, right.Index) })
	seen := map[float64]bool{}
	for _, phase := range phases {
		seen[phase.Index] = true
	}
	for _, index := range slices.Sorted(maps.Keys(byPhase)) {
		if seen[index] {
			continue
		}
		title := "Phase " + featuresJSNumber(index+1)
		if named := byPhase[index][0].PhaseTitle; named != nil {
			title = *named
		}
		phases = append(phases, &WorkflowPhase{Index: index, Title: title})
	}
	groups := []*WorkflowGroup{}
	for _, phase := range phases {
		if members, ok := byPhase[phase.Index]; ok {
			groups = append(groups, &WorkflowGroup{Key: "phase-" + featuresJSNumber(phase.Index), Title: phase.Title, Agents: members})
		}
	}
	if len(loose) > 0 {
		groups = append(groups, &WorkflowGroup{Key: "loose", Title: "Agents", Agents: loose})
	}
	return groups
}

func WorkflowAgentEnd(agent *WorkflowAgent, now float64) float64 {
	if agent.State != WorkflowAgentStateDone && agent.State != WorkflowAgentStateError {
		return now
	}
	if agent.StartedAt != nil && agent.DurationMs != nil {
		return *agent.StartedAt + *agent.DurationMs
	}
	for _, at := range []*float64{agent.LastProgressAt, agent.StartedAt, agent.QueuedAt} {
		if at != nil {
			return *at
		}
	}
	return now
}

// WorkflowWorkflowSpan is the window every lane is drawn against.
func WorkflowWorkflowSpan(workflow *Workflow, now float64) WorkflowSpan {
	start := workflow.StartedAt
	end := WorkflowNow(workflow, now)
	for _, agent := range workflow.Agents {
		if agent.QueuedAt != nil {
			start = featuresMin(start, *agent.QueuedAt)
		}
		if agent.StartedAt != nil {
			start = featuresMin(start, *agent.StartedAt)
		}
		end = featuresMax(end, WorkflowAgentEnd(agent, now))
	}
	return WorkflowSpan{Start: start, End: featuresMax(end, start+1)}
}

func place(from, to float64, span WorkflowSpan) (left, width float64) {
	total := span.End - span.Start
	left = featuresMax(0, featuresMin(100, (from-span.Start)/total*100))
	width = featuresMax(minBarWidth, featuresMin(100-left, (to-from)/total*100))
	return left, width
}

func WorkflowWorkflowBar(agent *WorkflowAgent, span WorkflowSpan, now float64) WorkflowBar {
	started := agent.StartedAt
	if started == nil && agent.State != WorkflowAgentStateQueued {
		started = agent.QueuedAt
	}
	var bar WorkflowBar
	if agent.QueuedAt != nil && (started == nil || *agent.QueuedAt < *started) {
		until := now
		if started != nil {
			until = *started
		}
		left, width := place(*agent.QueuedAt, until, span)
		bar.Queue = &WorkflowBarQueue{Left: left, Width: width}
	}
	if started != nil {
		left, width := place(*started, WorkflowAgentEnd(agent, now), span)
		bar.Run = &WorkflowBarRun{Left: left, Width: width}
	}
	return bar
}

// WorkflowTicks is round marks along the span, never more than a handful.
func WorkflowTicks(span WorkflowSpan) []*WorkflowTick {
	total := span.End - span.Start
	step := math.Ceil(total / maxTicks)
	for _, candidate := range tickSteps {
		if total/candidate <= maxTicks {
			step = candidate
			break
		}
	}
	ticks := []*WorkflowTick{}
	for at := 0.0; at <= total; at += step {
		ticks = append(ticks, &WorkflowTick{At: at, Label: FormatElapsed(at), Left: at / total * 100})
	}
	return ticks
}

func FormatElapsed(ms float64) string {
	seconds := int64(featuresMax(0, math.Floor(ms/1_000+0.5)))
	hours := seconds / 3_600
	minutes := seconds % 3_600 / 60
	rest := seconds % 60
	if hours != 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, rest)
	}
	return fmt.Sprintf("%d:%02d", minutes, rest)
}

// WorkflowAgentDuration is false for an agent that has neither queued nor started.
func WorkflowAgentDuration(agent *WorkflowAgent, now float64) (float64, bool) {
	started := agent.StartedAt
	if started == nil {
		started = agent.QueuedAt
	}
	if started == nil {
		return 0, false
	}
	return WorkflowAgentEnd(agent, now) - *started, true
}

// WorkflowAgentNote is one steady line per agent; callers pass agent.State for the default state.
func WorkflowAgentNote(agent *WorkflowAgent, state WorkflowAgentState) string {
	switch state {
	case WorkflowAgentStateError:
		if agent.Error != nil {
			return *agent.Error
		}
		return "Failed"
	case WorkflowAgentStateStopped:
		return "Stopped with the run"
	case WorkflowAgentStateQueued:
		return "Queued"
	case WorkflowAgentStateRunning:
		if agent.LastToolName != nil && *agent.LastToolName != "" {
			return "Using " + *agent.LastToolName
		}
		return "Working"
	}
	if agent.Cached != nil && *agent.Cached {
		return "Replayed from an earlier run"
	}
	if agent.ResultPreview != nil {
		for line := range strings.SplitSeq(*agent.ResultPreview, "\n") {
			if featuresJSTrim(line) != "" {
				return line
			}
		}
	}
	return "Done"
}

func WorkflowStatusLabel(workflow *Workflow) string {
	if workflow.Stopping != nil && *workflow.Stopping {
		return "Stopping"
	}
	return workflowStatusLabels[workflow.Status]
}

func WorkflowLabel(workflow *Workflow) string {
	return WorkflowStatusLabel(workflow) + " · " + strconv.Itoa(WorkflowAgentsDone(workflow)) + "/" + strconv.Itoa(len(workflow.Agents)) + " agents"
}

var workflowStatusLabels = map[WorkflowStatus]string{
	WorkflowStatusRunning:   "Running",
	WorkflowStatusCompleted: "Completed",
	WorkflowStatusFailed:    "Failed",
	WorkflowStatusStopped:   "Stopped",
}

// featuresMin is Math.min of two numbers: NaN wins.
func featuresMin(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Min(a, b)
}

// featuresMax is Math.max of two numbers: NaN wins.
func featuresMax(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	return math.Max(a, b)
}
