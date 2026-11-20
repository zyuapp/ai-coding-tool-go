package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	MaxAutomationPrompt   = 100_000
	MaxAutomationSchedule = 200
	MaxAutomationTimezone = 100
	MaxSurfaceWhen        = 500
	MaxEndsWhen           = 500
)

// AutomationAutomationFields takes nil for no automation.
func AutomationAutomationFields(automation *Automation) AutomationFields {
	if automation == nil {
		return AutomationFields{}
	}
	return AutomationFields{
		Schedule:    automation.Schedule,
		Prompt:      automation.Prompt,
		SurfaceWhen: featuresDeref(automation.SurfaceWhen),
		EndsWhen:    featuresDeref(automation.EndsWhen),
	}
}

func SameAutomationFields(a, b AutomationFields) bool { return a == b }

// EditedAutomationFields is the unsaved edits while the saved fields they began from still stand, else the saved fields.
func EditedAutomationFields(automation *Automation, edit *AutomationEdit) AutomationFields {
	saved := AutomationAutomationFields(automation)
	if edit != nil && SameAutomationFields(edit.Base, saved) {
		return edit.Fields
	}
	return saved
}

func isText(value any, maxLength int) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	length := featuresUTF16Len(text)
	return length > 0 && length <= maxLength
}

func isTimestamp(value any) bool {
	number, ok := featuresNumber(value)
	return ok && featuresSafeInteger(number) && number >= 0
}

// IsAutomation checks a JSON-decoded value (map[string]any, float64, …).
func IsAutomation(value any) bool {
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	absentOr := func(key string, check func(any) bool) bool {
		field, present := record[key]
		return !present || check(field)
	}
	text := func(max int) func(any) bool { return func(value any) bool { return isText(value, max) } }
	_, paused := record["paused"].(bool)
	return isText(record["id"], 256) &&
		isText(record["taskId"], 256) &&
		isText(record["prompt"], MaxAutomationPrompt) &&
		isText(record["schedule"], MaxAutomationSchedule) &&
		absentOr("timezone", text(MaxAutomationTimezone)) &&
		absentOr("policy", IsExecutionPolicy) &&
		absentOr("surfaceWhen", text(MaxSurfaceWhen)) &&
		absentOr("endsWhen", text(MaxEndsWhen)) &&
		paused &&
		isTimestamp(record["createdAt"]) &&
		isTimestamp(record["updatedAt"]) &&
		isTimestamp(record["runCount"]) &&
		absentOr("lastRunAt", isTimestamp) &&
		absentOr("lastStatus", isRunStatus) &&
		absentOr("lastStatusAt", isTimestamp) &&
		absentOr("consecutiveDeclines", isTimestamp) &&
		absentOr("overrunCount", isTimestamp)
}

func isRunStatus(value any) bool {
	switch status := value.(type) {
	case AutomationRunStatus:
		value = string(status)
	}
	switch value {
	case "succeeded", "failed", "cancelled", "skipped", "missed":
		return true
	}
	return false
}

// IsAutomationDraft checks a JSON-decoded value (map[string]any, float64, …).
func IsAutomationDraft(value any) bool {
	draft, ok := value.(map[string]any)
	if !ok {
		return false
	}
	absentOr := func(key string, check func(any) bool) bool {
		field, present := draft[key]
		return !present || check(field)
	}
	text := func(max int) func(any) bool { return func(value any) bool { return isText(value, max) } }
	return isText(draft["taskId"], 256) &&
		isText(draft["prompt"], MaxAutomationPrompt) &&
		isText(draft["schedule"], MaxAutomationSchedule) &&
		absentOr("timezone", text(MaxAutomationTimezone)) &&
		absentOr("policy", IsExecutionPolicy) &&
		absentOr("surfaceWhen", text(MaxSurfaceWhen)) &&
		absentOr("endsWhen", text(MaxEndsWhen)) &&
		absentOr("paused", featuresIsBool)
}

// IsAutomationPatch checks a JSON-decoded value (map[string]any, float64, …).
func IsAutomationPatch(value any) bool {
	patch, ok := value.(map[string]any)
	if !ok {
		return false
	}
	absentOr := func(key string, check func(any) bool) bool {
		field, present := patch[key]
		return !present || check(field)
	}
	text := func(max int) func(any) bool { return func(value any) bool { return isText(value, max) } }
	emptyOr := func(max int) func(any) bool { return func(value any) bool { return value == "" || isText(value, max) } }
	return absentOr("prompt", text(MaxAutomationPrompt)) &&
		absentOr("schedule", text(MaxAutomationSchedule)) &&
		absentOr("timezone", text(MaxAutomationTimezone)) &&
		absentOr("policy", IsExecutionPolicy) &&
		absentOr("surfaceWhen", emptyOr(MaxSurfaceWhen)) &&
		absentOr("endsWhen", emptyOr(MaxEndsWhen)) &&
		absentOr("paused", featuresIsBool)
}

// AutomationAfterRun returns a new record; a tick that never ran leaves the counters alone.
func AutomationAfterRun(automation *Automation, status AutomationRunStatus, at float64) *Automation {
	next := *automation
	next.LastStatus = &status
	next.LastStatusAt = &at
	next.UpdatedAt = at
	switch status {
	case AutomationRunStatusMissed:
	case AutomationRunStatusSkipped:
		declines := DeclineCount(automation) + 1
		next.ConsecutiveDeclines = &declines
	default:
		next.ConsecutiveDeclines = nil
		next.RunCount = automation.RunCount + 1
		next.LastRunAt = &at
	}
	return &next
}

func DeclineCount(automation *Automation) float64 {
	if automation.ConsecutiveDeclines == nil {
		return 0
	}
	return *automation.ConsecutiveDeclines
}

// DeclinesBeforeSurfacing is how many declines in a row it takes before the schedule's silence is itself worth surfacing.
const DeclinesBeforeSurfacing = 3

// QuietTick reports whether this tick may settle unseen.
func QuietTick(automation *Automation, manual bool) bool {
	return !manual && automation.SurfaceWhen != nil && !IsOneShotSchedule(automation.Schedule)
}

// IsWatching: a paused watch no longer holds its thread in Running.
func IsWatching(automation *Automation) bool {
	return automation.EndsWhen != nil && !automation.Paused
}

// WithWatchedThreads returns busy itself when no live watch adds a thread.
func WithWatchedThreads(busy jsonx.Set[string], automations []*Automation) jsonx.Set[string] {
	var watched []string
	for _, automation := range automations {
		if IsWatching(automation) && !busy.Has(automation.TaskID) {
			watched = append(watched, automation.TaskID)
		}
	}
	if len(watched) == 0 {
		return busy
	}
	next := make(jsonx.Set[string], len(busy)+len(watched))
	for id := range busy {
		next[id] = struct{}{}
	}
	for _, id := range watched {
		next[id] = struct{}{}
	}
	return next
}

func DidRun(status AutomationRunStatus) bool {
	return status != AutomationRunStatusSkipped && status != AutomationRunStatusMissed
}

// IsOneShotSchedule: a one-shot automation is an ISO 8601 timestamp rather than a cron expression.
func IsOneShotSchedule(schedule string) bool {
	return strings.Contains(schedule, "-") && featuresParsesAsDate(schedule)
}

// ScheduleFieldCount counts an empty schedule as one field, as splitting "" does.
func ScheduleFieldCount(schedule string) int {
	return max(1, len(strings.Fields(schedule)))
}

// featuresDeref is the value p points at, or the zero value for nil.
func featuresDeref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}

// featuresNumber is a JSON-decoded number, which TypeScript sees as `typeof value === "number"`.
func featuresNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case float32:
		return float64(number), true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	}
	return 0, false
}

const featuresMaxSafeInteger = 1<<53 - 1

// featuresSafeInteger is Number.isSafeInteger.
func featuresSafeInteger(number float64) bool {
	return number == math.Trunc(number) && math.Abs(number) <= featuresMaxSafeInteger
}

// featuresInteger is Number.isInteger.
func featuresInteger(number float64) bool {
	return !math.IsInf(number, 0) && number == math.Trunc(number)
}

func featuresIsBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

// featuresUTF16Len is a string's JavaScript length.
func featuresUTF16Len(text string) int {
	length := 0
	for _, r := range text {
		if r >= 0x10000 {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// featuresIsJSSpace is JavaScript's `\s`, which is also what trim removes.
func featuresIsJSSpace(r rune) bool {
	if r == 0x85 {
		return false
	}
	return r == 0xfeff || unicode.IsSpace(r)
}

// featuresJSTrim is String.prototype.trim.
func featuresJSTrim(text string) string {
	return strings.TrimFunc(text, featuresIsJSSpace)
}

var (
	featuresISODate   = regexp.MustCompile(`^([+-]\d{6}|\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[Tt](\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?([Zz]|[+-]\d{2}:\d{2})?)?$`)
	featuresLooseDate = regexp.MustCompile(`^(\d{1,6})-(\d{1,2})(?:-(\d{1,6}))?(?: +(\d{1,2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?(?:\s*([AaPp][Mm]))?)?\s*(?:[Zz]|(?:GMT|UTC)?[+-]\d{2}:?\d{2}|GMT|UTC)?$`)
)

// featuresParsesAsDate is whether Date.parse reads a string as a moment rather than NaN: the ISO 8601
// forms it must accept, and the dashed numeric dates its fallback parser also takes.
func featuresParsesAsDate(text string) bool {
	if match := featuresISODate.FindStringSubmatch(text); match != nil {
		if match[1] == "-000000" {
			return false
		}
		if !featuresDayInRange(match[2], match[3]) {
			return false
		}
		if match[4] == "" {
			return true
		}
		return featuresTimeInRange(match[4], match[5], match[6], 24) && (len(match[7]) < 2 || featuresTimeInRange(match[7][1:3], match[7][4:], "", 23))
	}
	match := featuresLooseDate.FindStringSubmatch(featuresJSTrim(text))
	if match == nil {
		return false
	}
	first, _ := strconv.Atoi(match[1])
	month, day := match[2], match[3]
	switch {
	case len(match[1]) > 2 || first > 31 || first == 0 && match[3] == "":
	case first >= 1 && first <= 12:
		month, day = match[1], match[2]
	default:
		return false
	}
	if !featuresDayInRange(month, day) {
		return false
	}
	if match[4] == "" {
		return true
	}
	limit := 24
	if match[7] != "" {
		limit = 12
	}
	return featuresTimeInRange(match[4], match[5], match[6], limit)
}

func featuresDayInRange(month, day string) bool {
	if month != "" {
		if value, _ := strconv.Atoi(month); value < 1 || value > 12 {
			return false
		}
	}
	if day != "" {
		if value, _ := strconv.Atoi(day); value < 1 || value > 31 {
			return false
		}
	}
	return true
}

// featuresTimeInRange allows 24:00 exactly, as ES does for the end of a day.
func featuresTimeInRange(hour, minute, second string, maxHour int) bool {
	h, _ := strconv.Atoi(hour)
	m, _ := strconv.Atoi(minute)
	s := 0
	if second != "" {
		s, _ = strconv.Atoi(second)
	}
	if m > 59 || s > 59 {
		return false
	}
	if maxHour == 24 && h == 24 {
		return m == 0 && s == 0
	}
	return h <= maxHour
}
