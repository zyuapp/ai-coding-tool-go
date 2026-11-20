package domain

import (
	"math"
	"time"
)

const dayMs = 86_400_000

// The formats stand for Intl.DateTimeFormat in an en-US locale. TS names the first TIME; it is
// recencyTime here because a package-level `time` would collide with the time package.
const (
	recencyTime = "3:04 PM"
	weekday     = "Mon"
	date        = "Jan 2"
	dateYear    = "Jan 2, 2006"
)

// dayStart is local midnight of the day holding moment, in ms.
func dayStart(moment float64) float64 {
	day := time.UnixMilli(int64(moment)).Local()
	return float64(time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).UnixMilli())
}

// RecencyLabel is when something last moved, as short as the gap allows.
func RecencyLabel(at, now float64) string {
	days := math.Floor((dayStart(now)-dayStart(at))/dayMs + 0.5)
	moment := time.UnixMilli(int64(at)).Local()
	switch {
	case days <= 0:
		return moment.Format(recencyTime)
	case days == 1:
		return "Yesterday"
	case days < 7:
		return moment.Format(weekday)
	case moment.Year() == time.UnixMilli(int64(now)).Local().Year():
		return moment.Format(date)
	}
	return moment.Format(dateYear)
}
