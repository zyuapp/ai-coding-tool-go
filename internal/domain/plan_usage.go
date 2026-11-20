package domain

import (
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func FormatShare(utilization *float64) string {
	if utilization == nil {
		return "Not reported"
	}
	return jsNumber(math.Floor(min(max(*utilization, 0), 100)+0.5)) + "% used"
}

func BarShare(utilization *float64) float64 {
	if utilization == nil {
		return 0
	}
	return min(max(*utilization, 0), 100)
}

// clock is a time as en-US writes it, without spaces and in lower case: 3:05pm.
func clock(at time.Time, timeZone string) string {
	return strings.ToLower(strings.ReplaceAll(at.In(planLocation(timeZone)).Format("3:04 PM"), " ", ""))
}

// planLocation is the IANA zone a caller names, UTC when this machine does not know it.
func planLocation(timeZone string) *time.Location {
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return time.UTC
	}
	return location
}

// FormatReset reads a reset today as a time and one further out with its day; zone appends the time zone. False when resetsAt is missing or invalid.
func FormatReset(resetsAt *string, now float64, timeZone string, zone bool) (string, bool) {
	if resetsAt == nil || *resetsAt == "" {
		return "", false
	}
	ms, ok := planDateParse(*resetsAt)
	if !ok {
		return "", false
	}
	location := planLocation(timeZone)
	at := time.UnixMilli(int64(ms)).In(location)
	today := time.UnixMilli(int64(now)).In(location)
	suffix := ""
	if zone {
		suffix = " (" + timeZone + ")"
	}
	if at.Year() == today.Year() && at.YearDay() == today.YearDay() {
		return "Resets " + clock(at, timeZone) + suffix, true
	}
	return "Resets " + at.Format("Jan 2") + " at " + clock(at, timeZone) + suffix, true
}

var planLabels = map[string]string{
	"prolite":                         "Pro",
	"self_serve_business_prolite":     "Business",
	"self_serve_business_usage_based": "Business",
	"enterprise_cbp_automation":       "Enterprise",
	"enterprise_cbp_usage_based":      "Enterprise",
}

func PlanLabel(subscription *string) (string, bool) {
	if subscription == nil || *subscription == "" {
		return "", false
	}
	label, ok := planLabels[*subscription]
	if !ok {
		words := strings.Split(*subscription, "_")
		for i, word := range words {
			first, size := utf8.DecodeRuneInString(word)
			if size > 0 {
				words[i] = strings.ToUpper(string(first)) + word[size:]
			}
		}
		label = strings.Join(words, " ")
	}
	return label + " plan", true
}

// NearLimitShare is where a window starts to warn that it is running out.
const NearLimitShare = 80

var modelWordSeparator = regexp.MustCompile(`[^a-z0-9.]+`)

func countsModel(window *UsageWindow, label string) bool {
	if window.Model == nil {
		return true
	}
	return slices.Contains(modelWordSeparator.Split(strings.ToLower(*window.Model), -1), strings.ToLower(label))
}

// WindowsFor is the windows a run on the model named label spends. A nil usage has none.
func WindowsFor(usage PlanUsage, label string) []*UsageWindow {
	var windows []*UsageWindow
	switch usage := usage.(type) {
	case PlanUsageAvailable:
		windows = usage.Windows
	case *PlanUsageAvailable:
		windows = usage.Windows
	}
	counted := []*UsageWindow{}
	for _, window := range windows {
		if countsModel(window, label) {
			counted = append(counted, window)
		}
	}
	return counted
}

// TightestWindow is a copy of the window closest to running out, its Utilization always set; nil when none reports a number.
func TightestWindow(windows []*UsageWindow) *UsageWindow {
	var tightest *UsageWindow
	for _, window := range windows {
		if window.Utilization != nil && (tightest == nil || *window.Utilization > *tightest.Utilization) {
			copied := *window
			tightest = &copied
		}
	}
	return tightest
}

// WindowSpan is a window's length in a few characters: 5h, 1d, Weekly.
func WindowSpan(window *UsageWindow) (string, bool) {
	if window.Minutes == nil {
		return "", false
	}
	minutes := *window.Minutes
	if minutes == 0 || math.IsNaN(minutes) || math.IsInf(minutes, 0) || minutes <= 0 {
		return "", false
	}
	switch {
	case minutes == 10_080:
		return "Weekly", true
	case math.Mod(minutes, 1_440) == 0:
		return jsNumber(minutes/1_440) + "d", true
	case math.Mod(minutes, 60) == 0:
		return jsNumber(minutes/60) + "h", true
	}
	return jsNumber(minutes) + "m", true
}

// SessionAndWeek is the session window and the fullest weekly one, each nil when the plan does not report it.
func SessionAndWeek(windows []*UsageWindow) (session, week *UsageWindow) {
	in := func(period UsageWindowPeriod) []*UsageWindow {
		matched := []*UsageWindow{}
		for _, window := range windows {
			if window.Period != nil && *window.Period == period {
				matched = append(matched, window)
			}
		}
		return matched
	}
	return TightestWindow(in(UsageWindowPeriodSession)), TightestWindow(in(UsageWindowPeriodWeek))
}

// UsageWarningKey takes a window with Utilization set, as TightestWindow returns.
func UsageWarningKey(engine string, window *UsageWindow) string {
	resets := ""
	if window.ResetsAt != nil {
		if ms, ok := planDateParse(*window.ResetsAt); ok {
			resets = jsNumber(ms)
		}
	}
	state := "near"
	if *window.Utilization >= 100 {
		state = "reached"
	}
	return engine + ":" + window.ID + ":" + state + "@" + resets
}

func warningParts(key string) (subject string, until *float64) {
	at := strings.LastIndex(key, "@")
	if at < 0 {
		return key, nil
	}
	text := jsTrim(key[at+1:])
	if value, err := strconv.ParseFloat(text, 64); err == nil && text != "" && !math.IsInf(value, 0) && !math.IsNaN(value) && !strings.ContainsAny(text, "xXpP_") {
		return key[:at], &value
	}
	return key[:at], nil
}

func UsageWarningDismissed(dismissed []string, key string, now float64) bool {
	subject, _ := warningParts(key)
	return slices.ContainsFunc(dismissed, func(entry string) bool {
		keptSubject, until := warningParts(entry)
		return keptSubject == subject && (until == nil || now < *until)
	})
}

func WithUsageWarningDismissed(dismissed []string, key string) []string {
	subject, _ := warningParts(key)
	kept := []string{}
	for _, entry := range dismissed {
		if entrySubject, _ := warningParts(entry); entrySubject != subject {
			kept = append(kept, entry)
		}
	}
	kept = append(kept, key)
	if len(kept) > MaxDismissedWarnings {
		kept = kept[len(kept)-MaxDismissedWarnings:]
	}
	return kept
}

const MaxDismissedWarnings = 20

var planISODate = regexp.MustCompile(`^([+-]\d{6}|\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[Tt ](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?)?([Zz]|[+-]\d{2}:?\d{2})?$`)

var planLooseDateLayouts = []string{time.RFC1123, time.RFC1123Z, time.RFC850, time.ANSIC, "Mon Jan 02 2006 15:04:05 GMT-0700", "Mon Jan 02 2006"}

// planDateParse is Date.parse for ISO 8601 text and the common written forms; false where it gives NaN.
// Days past a month's end roll into the next month, as V8 does.
func planDateParse(text string) (float64, bool) {
	match := planISODate.FindStringSubmatch(text)
	if match == nil {
		trimmed := jsTrim(text)
		for _, layout := range planLooseDateLayouts {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return float64(parsed.UnixMilli()), true
			}
		}
		return 0, false
	}
	if match[1] == "-000000" {
		return 0, false
	}
	number := func(text string, fallback int) int {
		if text == "" {
			return fallback
		}
		value, _ := strconv.Atoi(text)
		return value
	}
	year, month, day := number(match[1], 0), number(match[2], 1), number(match[3], 1)
	hour, minute, second := number(match[4], 0), number(match[5], 0), number(match[6], 0)
	if month < 1 || month > 12 || day < 1 || day > 31 || hour > 24 || minute > 59 || second > 59 || hour == 24 && (minute > 0 || second > 0) {
		return 0, false
	}
	millis := 0
	if fraction := match[7]; fraction != "" {
		millis = number((fraction + "00")[:3], 0)
	}
	location := time.UTC
	switch offset := match[8]; {
	case offset == "" && match[4] != "":
		location = time.Local
	case offset != "" && offset != "Z" && offset != "z":
		digits := strings.ReplaceAll(offset[1:], ":", "")
		minutes := number(digits[:2], 0)*60 + number(digits[2:], 0)
		if offset[0] == '-' {
			minutes = -minutes
		}
		location = time.FixedZone("", minutes*60)
	}
	return float64(time.Date(year, time.Month(month), day, hour, minute, second, millis*int(time.Millisecond), location).UnixMilli()), true
}
