package domain

import (
	"regexp"
	"strconv"
)

const TitleLimit = 52

// ClampTitle trims a title and cuts it to TitleLimit UTF-16 code units with an ellipsis.
func ClampTitle(text string) string {
	trimmed := jsTrim(text)
	if length := utf16Length(trimmed); length > TitleLimit {
		return utf16Slice(trimmed, 0, TitleLimit-3) + "…"
	}
	return trimmed
}

var forkSuffix = regexp.MustCompile(`[` + handleSpaceClass + `]*\((?:fork|fork \d+)\)$`)

// ForkTitle is what a copy of a thread is called, numbered past every name already taken.
func ForkTitle(title string, taken []string) string {
	base := jsTrim(forkSuffix.ReplaceAllString(title, ""))
	if base == "" {
		base = jsTrim(title)
	}
	names := make(map[string]bool, len(taken))
	for _, name := range taken {
		names[name] = true
	}
	for number := 1; ; number++ {
		suffix := " (fork)"
		if number > 1 {
			suffix = " (fork " + strconv.Itoa(number) + ")"
		}
		room := TitleLimit - utf16Length(suffix)
		named := base
		if utf16Length(base) > room {
			named = utf16Slice(base, 0, room-1) + "…"
		}
		if candidate := jsTrim(named + suffix); !names[candidate] {
			return candidate
		}
	}
}

func ThreadCreatedAt(thread *Thread) float64 {
	switch {
	case thread.CreatedAt != nil:
		return *thread.CreatedAt
	case thread.HistorySummary != nil && thread.HistorySummary.FirstMessageAt != nil:
		return *thread.HistorySummary.FirstMessageAt
	case len(thread.Messages) > 0:
		return thread.Messages[0].At
	}
	return thread.UpdatedAt
}

// ThreadLastOutputAt is when the thread last said anything.
func ThreadLastOutputAt(thread *Thread) float64 {
	runEndedAt := 0.0
	if thread.RunEndedAt != nil {
		runEndedAt = *thread.RunEndedAt
	}
	return max(ThreadCreatedAt(thread), lastAudibleAt(thread), runEndedAt)
}

func lastAudibleAt(thread *Thread) float64 {
	if thread.HistorySummary != nil {
		if thread.HistorySummary.LastAudibleAt != nil {
			return *thread.HistorySummary.LastAudibleAt
		}
		return 0
	}
	for index := len(thread.Messages) - 1; index >= 0; index-- {
		if message := thread.Messages[index]; message.Withdrawn == nil || !*message.Withdrawn {
			return message.At
		}
	}
	return 0
}

// LastRunOutcome is how the thread's newest run ended; false when none has.
func LastRunOutcome(thread *Thread) (ThreadOutcome, bool) {
	if thread.RunEndedAs != nil {
		return *thread.RunEndedAs, true
	}
	if thread.Outcome != nil {
		return *thread.Outcome, true
	}
	return "", false
}
