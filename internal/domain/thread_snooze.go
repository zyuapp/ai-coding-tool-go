package domain

import "math"

// SnoozeOption is one entry of SnoozeOptions.
type SnoozeOption struct {
	Label string
	Hours SnoozeHours
}

var SnoozeOptions = []SnoozeOption{
	{Label: "1 hour", Hours: 1},
	{Label: "4 hours", Hours: 4},
	{Label: "1 day", Hours: 24},
	{Label: "3 days", Hours: 72},
	{Label: "1 week", Hours: 168},
}

func IsSnoozeHours(value any) bool {
	hours, ok := value.(float64)
	if !ok {
		return false
	}
	for _, option := range SnoozeOptions {
		if option.Hours == hours {
			return true
		}
	}
	return false
}

func WithoutSnooze(thread *Thread) *Thread {
	if thread.SnoozedUntil == nil {
		return thread
	}
	next := *thread
	next.SnoozedUntil = nil
	return &next
}

var deadlines threadsMemo[*float64]

// NextSnoozeExpiry is one wake-up for the entire workspace; false when nothing is snoozed.
func NextSnoozeExpiry(threads []*Thread) (float64, bool) {
	next := deadlines.get(threads, func(threads []*Thread) *float64 {
		var next *float64
		for _, thread := range threads {
			if thread.ArchivedAt == nil && thread.SnoozedUntil != nil {
				soonest := *thread.SnoozedUntil
				if next != nil {
					soonest = math.Min(*next, soonest)
				}
				next = &soonest
			}
		}
		return next
	})
	if next == nil {
		return 0, false
	}
	return *next, true
}
