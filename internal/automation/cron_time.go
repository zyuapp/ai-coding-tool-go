package automation

import (
	"time"
)

// wall is a wall-clock minute in some zone, with no zone attached.
type wall struct {
	year, month, day, hour, minute int // month 1-12
}

func wallOf(t time.Time) wall {
	return wall{t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute()}
}

func (w wall) naive() time.Time {
	return time.Date(w.year, time.Month(w.month), w.day, w.hour, w.minute, 0, 0, time.UTC)
}

func (w wall) sameDate(other wall) bool {
	return w.year == other.year && w.month == other.month && w.day == other.day
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func weekdayOf(year, month, day int) int {
	return int(time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Weekday())
}

func lastWeekdayOf(year, month int) int {
	last := daysIn(year, month)
	switch weekdayOf(year, month, last) {
	case 0:
		return last - 2
	case 6:
		return last - 1
	}
	return last
}

func nearestWeekday(year, month, day int) int {
	last := daysIn(year, month)
	if day > last {
		return -1
	}
	switch weekdayOf(year, month, day) {
	case 0:
		if day == last {
			return day - 2
		}
		return day + 1
	case 6:
		if day == 1 {
			return day + 2
		}
		return day - 1
	}
	return day
}

func isNthWeekdayOfMonth(year, month, day int, nth uint8) bool {
	occurrence := (day-1)/7 + 1
	if nth&63 != 0 && occurrence <= 5 && nthBits[occurrence-1]&nth != 0 {
		return true
	}
	if nth&32 != 0 {
		return day+7 > daysIn(year, month)
	}
	return false
}

func (p *cronPattern) matchesDay(year, month, day int) bool {
	matched := p.day[day-1]
	if !matched {
		for index, on := range p.nearestWeekdays {
			if on {
				if nearest := nearestWeekday(year, month, index+1); nearest != -1 && nearest == day {
					matched = true
					break
				}
			}
		}
	}
	if p.lastWeekday && day == lastWeekdayOf(year, month) {
		matched = true
	}
	if p.lastDayOfMonth && day == daysIn(year, month) {
		matched = true
	}
	if !p.starDOW {
		nth := p.dayOfWeek[weekdayOf(year, month, day)]
		weekday := nth != 0 && isNthWeekdayOfMonth(year, month, day, nth)
		if p.useAndLogic || p.starDOM {
			matched = matched && weekday
		} else {
			matched = matched || weekday
		}
	}
	return matched
}

// searchYears is how far a pattern that never matches is followed before croner gives up on it.
const searchYears = 400

// firstFrom is the first wall-clock minute at or after from that the pattern matches.
func (p *cronPattern) firstFrom(from wall) (wall, bool) {
	date := time.Date(from.year, time.Month(from.month), from.day, 0, 0, 0, 0, time.UTC)
	first := true
	for date.Year()-from.year <= searchYears {
		year, month, day := date.Year(), int(date.Month()), date.Day()
		if !p.month[month-1] {
			date = time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.UTC)
			first = false
			continue
		}
		if p.matchesDay(year, month, day) {
			startHour, startMinute := 0, 0
			if first {
				startHour, startMinute = from.hour, from.minute
			}
			for hour := startHour; hour < 24; hour++ {
				if !p.hour[hour] {
					continue
				}
				minute := 0
				if hour == startHour {
					minute = startMinute
				}
				for ; minute < 60; minute++ {
					if p.minute[minute] {
						return wall{year, month, day, hour, minute}, true
					}
				}
			}
		}
		date = date.AddDate(0, 0, 1)
		first = false
	}
	return wall{}, false
}

// resolution is what a wall-clock time names in a zone: one instant, two in a repeated hour, or none
// in a skipped one, where before and after bound the moment the clocks jumped.
type resolution struct {
	instants      []time.Time
	before, after time.Time
}

func resolveWall(w wall, zone *time.Location) resolution {
	naive := w.naive()
	offsetAt := func(t time.Time) int {
		_, offset := t.In(zone).Zone()
		return offset
	}
	candidates := []int{offsetAt(naive.Add(-24 * time.Hour)), offsetAt(naive.Add(24 * time.Hour))}
	if candidates[1] == candidates[0] {
		candidates = candidates[:1]
	}
	var found []time.Time
	var tried []time.Time
	for _, offset := range candidates {
		instant := naive.Add(-time.Duration(offset) * time.Second)
		tried = append(tried, instant)
		if offsetAt(instant) == offset {
			found = append(found, instant)
		}
	}
	if len(found) == 2 && found[1].Before(found[0]) {
		found[0], found[1] = found[1], found[0]
	}
	if len(found) > 0 {
		return resolution{instants: found}
	}
	if len(tried) == 1 {
		return resolution{before: tried[0], after: tried[0]}
	}
	before, after := tried[0], tried[1]
	if after.Before(before) {
		before, after = after, before
	}
	return resolution{before: before, after: after}
}

// transition is the first whole second after low at which the zone's offset differs from low's.
func transition(low, high time.Time, zone *time.Location) time.Time {
	_, start := low.In(zone).Zone()
	for high.Sub(low) > time.Second {
		middle := low.Add(high.Sub(low) / (2 * time.Second) * time.Second)
		if _, offset := middle.In(zone).Zone(); offset == start {
			low = middle
		} else {
			high = middle
		}
	}
	return high
}

// runAt is the moment a matched wall time fires that comes after threshold, if any. A skipped hour
// is fired at the jump only by a fixed-time pattern; a repeated one fires in both passes otherwise.
func (p *cronPattern) runAt(w wall, threshold time.Time, zone *time.Location) (time.Time, bool) {
	resolved := resolveWall(w, zone)
	switch len(resolved.instants) {
	case 1:
		if resolved.instants[0].After(threshold) {
			return resolved.instants[0], true
		}
		return time.Time{}, false
	case 2:
		candidates := resolved.instants
		if p.fixedTime {
			candidates = candidates[:1]
		}
		for _, candidate := range candidates {
			if candidate.After(threshold) {
				return candidate, true
			}
		}
		return time.Time{}, false
	}
	if !p.fixedTime {
		return time.Time{}, false
	}
	jump := transition(resolved.before, resolved.after, zone)
	if wallOf(jump.In(zone)).sameDate(w) && jump.After(threshold) {
		return jump, true
	}
	return time.Time{}, false
}

func nextMinute(w wall) wall {
	return wallOf(w.naive().Add(time.Minute))
}

// walk follows matched wall times from start until one fires after threshold.
func (p *cronPattern) walk(start wall, threshold time.Time, zone *time.Location, inclusive bool) (time.Time, bool) {
	current := start
	if !inclusive {
		current = nextMinute(current)
	}
	for {
		matched, ok := p.firstFrom(current)
		if !ok {
			return time.Time{}, false
		}
		if at, ok := p.runAt(matched, threshold, zone); ok {
			return at, true
		}
		current = nextMinute(matched)
	}
}

// inEarlierPass reports whether t falls in the first pass of an hour the clocks repeat.
func inEarlierPass(t time.Time, zone *time.Location) bool {
	local := t.In(zone)
	resolved := resolveWall(wall{local.Year(), int(local.Month()), local.Day(), local.Hour(), local.Minute()}, zone)
	if len(resolved.instants) != 2 {
		return false
	}
	later := resolved.instants[1].Add(time.Duration(local.Second()) * time.Second)
	return t.Before(later)
}

// next is the first firing strictly after the whole second t falls in.
func (p *cronPattern) next(t time.Time, zone *time.Location) (time.Time, bool) {
	second := t.Truncate(time.Second)
	at, found := p.walk(wallOf(second.In(zone)), second, zone, false)
	if p.fixedTime || !inEarlierPass(second, zone) {
		return at, found
	}
	// Wall times the second pass repeats were behind t in the first, so walk them again from the jump.
	jump := transition(second, second.Add(24*time.Hour), zone)
	if found && at.Before(jump) {
		return at, true
	}
	again, ok := p.walk(wallOf(jump.In(zone)), jump.Add(-time.Second), zone, true)
	switch {
	case !found:
		return again, ok
	case !ok:
		return at, true
	case again.Before(at):
		return again, true
	}
	return at, true
}
