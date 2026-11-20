package domain

import (
	"regexp"
	"testing"
	"time"
)

func TestRecencyLabelCountsCalendarDays(t *testing.T) {
	at := func(day, hour, minute int) float64 {
		return float64(time.Date(2026, 11, day, hour, minute, 0, 0, time.Local).UnixMilli())
	}
	if label := RecencyLabel(at(9, 0, 10), at(9, 23, 0)); !regexp.MustCompile(`\d`).MatchString(label) {
		t.Fatal(label)
	}
	if label := RecencyLabel(at(8, 23, 50), at(9, 0, 30)); label != "Yesterday" {
		t.Fatal(label)
	}
	if label := RecencyLabel(at(3, 12, 0), at(9, 12, 0)); label != "Tue" {
		t.Fatal(label)
	}
	if label := RecencyLabel(at(2, 12, 0), at(9, 12, 0)); label != "Nov 2" {
		t.Fatal(label)
	}
	if label := RecencyLabel(float64(time.Date(2025, 3, 4, 12, 0, 0, 0, time.Local).UnixMilli()), at(9, 12, 0)); label != "Mar 4, 2025" {
		t.Fatal(label)
	}
	if label := RecencyLabel(at(9, 15, 4), at(9, 16, 0)); label != "3:04 PM" {
		t.Fatal(label)
	}
}
