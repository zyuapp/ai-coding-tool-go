package domain

import (
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// threadsMemo remembers one value computed from a threads slice, keyed by the slice's identity.
type threadsMemo[T any] struct {
	mu      sync.Mutex
	threads []*Thread
	value   T
	ok      bool
}

// get is the value computed for threads, computing it when threads is not the slice last asked about.
func (memo *threadsMemo[T]) get(threads []*Thread, compute func([]*Thread) T) T {
	memo.mu.Lock()
	defer memo.mu.Unlock()
	if memo.ok && jsonx.SameSlice(memo.threads, threads) {
		return memo.value
	}
	memo.threads, memo.value, memo.ok = threads, compute(threads), true
	return memo.value
}

// LimitLine is one engine's line of paused threads, first to go first.
type LimitLine struct {
	Engine  AgentEngine
	Threads []*Thread
}

func finitePositive(value any) bool {
	number, ok := value.(float64)
	return jsFinite(value) && ok && number > 0
}

func IsUsageLimit(value any) bool {
	limit, ok := record(value)
	return ok && finitePositive(limit["resetsAt"]) && (limit["window"] == "session" || limit["window"] == "weekly")
}

func IsLimitPause(value any) bool {
	if !IsUsageLimit(value) {
		return false
	}
	pause, _ := record(value)
	nudgedAt, hasNudge := pause["nudgedAt"]
	workflow, hasWorkflow := pause["workflow"]
	held, hasHeld := pause["held"]
	_, heldList := held.([]any)
	return finitePositive(pause["pausedAt"]) &&
		(!hasNudge || finitePositive(nudgedAt)) &&
		(!hasWorkflow || workflow == true) &&
		(!hasHeld || heldList)
}

func WithoutLimitPause(thread *Thread) *Thread {
	if thread.LimitPause == nil {
		return thread
	}
	next := *thread
	next.LimitPause = nil
	return &next
}

// ResumesOnItsOwn reports whether a pause in this window resumes once its limit lifts. A weekly limit waits for the user.
func ResumesOnItsOwn(window UsageLimitWindow) bool {
	return window == UsageLimitWindowSession
}

func lineOrder(left, right *Thread) int {
	a, b := left.LimitPause, right.LimitPause
	if (a.NudgedAt == nil) != (b.NudgedAt == nil) {
		if a.NudgedAt == nil {
			return 1
		}
		return -1
	}
	if a.NudgedAt != nil && b.NudgedAt != nil && *a.NudgedAt != *b.NudgedAt {
		return limitCompare(*b.NudgedAt, *a.NudgedAt)
	}
	leftReviews, rightReviews := limitReviewer(left), limitReviewer(right)
	if leftReviews != rightReviews {
		if leftReviews {
			return -1
		}
		return 1
	}
	if leftFlow, rightFlow := a.Workflow != nil && *a.Workflow, b.Workflow != nil && *b.Workflow; leftFlow != rightFlow {
		if leftFlow {
			return 1
		}
		return -1
	}
	return limitCompare(a.PausedAt, b.PausedAt)
}

func limitReviewer(thread *Thread) bool {
	return thread.Role != nil && *thread.Role == ThreadRoleReviewer
}

// limitCompare is the sign of left - right, as a sort comparator reads it.
func limitCompare(left, right float64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	}
	return 0
}

var usageLimitLines threadsMemo[[]LimitLine]

// LimitLines is every engine's line of threads that resume on their own, engines in the order their first thread appears. Shared: never write into it.
func LimitLines(threads []*Thread) []LimitLine {
	return usageLimitLines.get(threads, func(threads []*Thread) []LimitLine {
		lines := []LimitLine{}
		for _, thread := range threads {
			if thread.ArchivedAt != nil || thread.LimitPause == nil || !ResumesOnItsOwn(thread.LimitPause.Window) {
				continue
			}
			index := slices.IndexFunc(lines, func(line LimitLine) bool { return line.Engine == thread.Engine })
			if index < 0 {
				lines = append(lines, LimitLine{Engine: thread.Engine})
				index = len(lines) - 1
			}
			lines[index].Threads = append(lines[index].Threads, thread)
		}
		for _, line := range lines {
			slices.SortStableFunc(line.Threads, lineOrder)
		}
		return lines
	})
}

var positions threadsMemo[map[string]float64]

// LinePositions is where each paused thread stands in its engine's line, counting from 1. Shared: never write into it.
func LinePositions(threads []*Thread) map[string]float64 {
	return positions.get(threads, func(threads []*Thread) map[string]float64 {
		places := map[string]float64{}
		for _, line := range LimitLines(threads) {
			for index, thread := range line.Threads {
				places[thread.ID] = float64(index + 1)
			}
		}
		return places
	})
}

func WithEngineLimit(threads []*Thread, engine AgentEngine, limit UsageLimit) []*Thread {
	var next []*Thread
	for index, thread := range threads {
		pause := thread.LimitPause
		if thread.Engine != engine || pause == nil || pause.ResetsAt >= limit.ResetsAt {
			continue
		}
		if next == nil {
			next = slices.Clone(threads)
		}
		held := *pause
		held.ResetsAt = limit.ResetsAt
		if limit.Window == UsageLimitWindowWeekly {
			held.Window = UsageLimitWindowWeekly
		}
		copied := *thread
		copied.LimitPause = &held
		next[index] = &copied
	}
	if next == nil {
		return threads
	}
	return next
}

const workflowNote = "A dynamic workflow was cut short when the usage limit hit: resume it with the Workflow tool's resumeFromRunId, using the run ID its launch returned, so finished agents are not run again."

// ResumePrompt takes nil written when nothing was written to the thread while it waited.
func ResumePrompt(pause *LimitPause, written *string) string {
	prompt := "The usage limit that paused this thread has reset. Continue where you left off."
	if written != nil {
		prompt = *written
	}
	if pause.Workflow != nil && *pause.Workflow {
		return prompt + "\n\n" + workflowNote
	}
	return prompt
}

// LiftTime reads in this machine's time zone.
func LiftTime(resetsAt, now float64) string {
	at := time.UnixMilli(int64(resetsAt)).Local()
	today := time.UnixMilli(int64(now)).Local()
	shown := at.Format("3:04 PM")
	if at.Year() == today.Year() && at.YearDay() == today.YearDay() {
		return shown
	}
	return at.Format("Mon") + " " + shown
}

func ordinal(position int) string {
	tens := position % 100
	suffix := "th"
	if tens < 11 || tens > 13 {
		switch position % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(position) + suffix
}

// PauseSummary takes nil position for a thread with no place in line.
func PauseSummary(pause *LimitPause, position *float64, now float64) string {
	if !ResumesOnItsOwn(pause.Window) {
		if now < pause.ResetsAt {
			return "Weekly limit · resets " + LiftTime(pause.ResetsAt, now)
		}
		return "Weekly limit reset"
	}
	place := ""
	if position != nil && *position > 1 {
		place = " · " + ordinal(int(*position))
	}
	if now < pause.ResetsAt {
		return "Resumes " + LiftTime(pause.ResetsAt, now) + place
	}
	return "Resuming" + place
}
