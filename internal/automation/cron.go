package automation

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxTimerDelay is the longest croner sleeps before it looks at the clock again.
const maxTimerDelay = 30 * time.Second

// cronOptions are the croner options the scheduler uses.
type cronOptions struct {
	timezone string
	paused   bool
	// Called in place of a tick that fell due while the previous run was still going.
	protect func()
	// Called with what a run returned as an error.
	catch func(error)
}

// Cron is a croner job: a pattern or one moment, a timer that fires it, and the run in flight.
type Cron struct {
	pattern *cronPattern
	once    *time.Time
	zone    *time.Location
	options cronOptions
	run     func() error
	now     func() time.Time

	mu         sync.Mutex
	timer      *time.Timer
	killed     bool
	blocking   bool
	currentRun *time.Time
}

// newCron reads a schedule the way croner does: a string with a colon after its first character is
// one moment, anything else a pattern. run nil makes a job that only answers questions.
func newCron(schedule string, options cronOptions, run func() error) (*Cron, error) {
	zone, zoneErr := cronZone(options.timezone)
	cron := &Cron{zone: zone, options: options, run: run, now: time.Now}
	if strings.Index(schedule, ":") > 0 {
		if zoneErr != nil {
			return nil, zoneErr
		}
		at, err := parseOnce(schedule, zone, options.timezone != "")
		if err != nil {
			return nil, err
		}
		cron.once = &at
	} else {
		pattern, err := parseCronPattern(schedule)
		if err != nil {
			return nil, err
		}
		cron.pattern = pattern
		if zoneErr != nil {
			return nil, zoneErr
		}
	}
	if run != nil {
		cron.mu.Lock()
		cron.schedule(time.Time{})
		cron.mu.Unlock()
	}
	return cron, nil
}

// errTimezone carries croner's message for a zone Intl does not know.
type errTimezone struct{ message string }

func (e errTimezone) Error() string { return e.message }

func cronZone(name string) (*time.Location, error) {
	if name == "" {
		return time.Local, nil
	}
	zone, err := time.LoadLocation(name)
	if err != nil || name == "Local" {
		return nil, errTimezone{"toTZ: Invalid timezone '" + name + "' or date. Please provide a valid IANA timezone (e.g., 'America/New_York', 'Europe/Stockholm'). Original error: Invalid time zone specified: " + name}
	}
	return zone, nil
}

var isoMoment = regexp.MustCompile(`^([+-]\d{6}|\d{4})(?:-(\d{2})(?:-(\d{2}))?)?(?:[Tt ](\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?\s*([Zz]|[+-]\d{2}:?\d{2})?)?$`)

// jsDateParse is Date.parse for the ISO 8601 forms: a date alone is UTC, a time without an offset is local.
func jsDateParse(text string) (time.Time, bool) {
	match := isoMoment.FindStringSubmatch(text)
	if match == nil || match[1] == "-000000" {
		return time.Time{}, false
	}
	number := func(value string, fallback int) int {
		if value == "" {
			return fallback
		}
		parsed, _ := strconv.Atoi(value)
		return parsed
	}
	year := number(match[1], 0)
	month, day := number(match[2], 1), number(match[3], 1)
	hour, minute, second := number(match[4], 0), number(match[5], 0), number(match[6], 0)
	if month < 1 || month > 12 || day < 1 || day > daysIn(year, month) || hour > 24 || minute > 59 || second > 59 {
		return time.Time{}, false
	}
	if hour == 24 && (minute != 0 || second != 0) {
		return time.Time{}, false
	}
	millis := 0
	if fraction := match[7]; fraction != "" {
		millis = number((fraction + "00")[:3], 0)
	}
	zone := time.Local
	if match[4] == "" {
		zone = time.UTC
	}
	if offset := match[8]; offset != "" {
		if offset == "Z" || offset == "z" {
			zone = time.UTC
		} else {
			digits := strings.ReplaceAll(offset[1:], ":", "")
			seconds := number(digits[:2], 0)*3600 + number(digits[2:], 0)*60
			if offset[0] == '-' {
				seconds = -seconds
			}
			zone = time.FixedZone("", seconds)
		}
	}
	fraction := time.Duration(second)*time.Second + time.Duration(millis)*time.Millisecond
	if zone == time.Local {
		// A skipped local time takes the offset from before the jump, a repeated one its first pass, as V8 does.
		resolved := resolveWall(wall{year, month, day, hour, minute}, time.Local)
		if len(resolved.instants) > 0 {
			return resolved.instants[0].Add(fraction), true
		}
		return resolved.after.Add(fraction), true
	}
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, zone).Add(fraction), true
}

// parseOnce is the moment croner reads a one-shot as: with an offset it is absolute, without one its
// local wall-clock time is taken in the schedule's zone, a skipped time landing after the jump.
func parseOnce(text string, zone *time.Location, named bool) (time.Time, error) {
	parsed, ok := jsDateParse(text)
	if !ok {
		return time.Time{}, errors.New("Invalid ISO8601 passed to timezone parser.")
	}
	rest := ""
	if len(text) > 9 {
		rest = text[9:]
	}
	at := parsed
	if !strings.ContainsAny(rest, "Z+-") {
		local := parsed.In(time.Local)
		resolved := resolveWall(wallOf(local), zone)
		offset := time.Duration(local.Second())*time.Second + time.Duration(local.Nanosecond())
		switch {
		case len(resolved.instants) > 0:
			at = resolved.instants[0].Add(offset)
		default:
			at = resolved.after.Add(offset)
		}
	}
	if named {
		at = at.Truncate(time.Second)
	}
	return at, nil
}

// nextTarget is the next firing after the given moment, or after now when it is nil.
func (c *Cron) nextTarget(previous *time.Time, now time.Time) (time.Time, bool) {
	if c.killed {
		return time.Time{}, false
	}
	if c.once != nil {
		if c.currentRun != nil && !c.once.After(*c.currentRun) {
			return time.Time{}, false
		}
		if previous != nil {
			if previous.Sub(*c.once) >= 0 {
				return time.Time{}, false
			}
		} else if now.Sub(*c.once) > time.Second {
			return time.Time{}, false
		}
		return *c.once, true
	}
	from := now
	if previous != nil {
		from = *previous
	}
	return c.pattern.next(from, c.zone)
}

// NextRun is when the job fires next, false when it never will.
func (c *Cron) NextRun() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nextTarget(nil, c.now())
}

// IsBusy reports whether a run the timer started is still going.
func (c *Cron) IsBusy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blocking
}

// Stop ends the job: it fires no more, and answers no next run.
func (c *Cron) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed = true
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// schedule runs with c.mu held. A zero at is now. A paused job never fires, so it sets no timer.
func (c *Cron) schedule(at time.Time) {
	if c.killed || c.options.paused || c.run == nil {
		return
	}
	now := at
	if now.IsZero() {
		now = c.now()
	}
	soonest, ok := c.nextTarget(nil, now)
	if !ok {
		return
	}
	previous := c.currentRun
	if previous != nil && previous.After(now) {
		previous = nil
	}
	target, ok := c.nextTarget(previous, now)
	if !ok {
		return
	}
	delay := min(soonest.Sub(now), maxTimerDelay)
	if delay < 0 {
		delay = 0
	}
	c.timer = time.AfterFunc(delay, func() { c.checkTrigger(target) })
}

func (c *Cron) checkTrigger(target time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.killed {
		return
	}
	now := c.now()
	due := !now.Before(target)
	protected := c.blocking && c.options.protect != nil
	switch {
	case due && !protected:
		c.trigger(now)
	case due && protected:
		go c.options.protect()
	}
	if due {
		c.schedule(time.Time{})
	} else {
		c.schedule(now)
	}
}

// trigger runs with c.mu held and starts the run without waiting for it.
func (c *Cron) trigger(at time.Time) {
	c.blocking = true
	started := at
	if c.options.timezone != "" {
		started = started.Truncate(time.Second)
	}
	c.currentRun = &started
	go func() {
		defer func() {
			c.mu.Lock()
			c.blocking = false
			c.mu.Unlock()
		}()
		if err := c.run(); err != nil && c.options.catch != nil {
			c.options.catch(err)
		}
	}()
}
