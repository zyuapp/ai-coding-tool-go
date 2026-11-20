package automation

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// cronPattern is a five-part croner pattern (minute hour day month weekday) read the way croner 11
// reads it in its default legacy mode: day of month and day of week are OR'ed when both are restricted.
type cronPattern struct {
	minute          [60]bool
	hour            [24]bool
	day             [31]bool
	month           [12]bool
	nearestWeekdays [31]bool
	// Per weekday, Sunday first: 63 is every occurrence, 1-16 the nth, 32 the last in its month.
	dayOfWeek      [7]uint8
	lastDayOfMonth bool
	lastWeekday    bool
	starDOM        bool
	starDOW        bool
	useAndLogic    bool
	// One minute of one hour: such a pattern still fires in a skipped hour, at the moment the clocks jump.
	fixedTime bool
}

var nthBits = [5]uint8{1, 2, 4, 8, 16}

var cronNicknames = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

type alpha struct {
	name  *regexp.Regexp
	value string
}

func alphas(pairs ...string) []alpha {
	var names []alpha
	for index := 0; index < len(pairs); index += 2 {
		names = append(names, alpha{regexp.MustCompile("(?i)" + regexp.QuoteMeta(pairs[index])), pairs[index+1]})
	}
	return names
}

var monthNames = alphas("jan", "1", "feb", "2", "mar", "3", "apr", "4", "may", "5", "jun", "6", "jul", "7", "aug", "8", "sep", "9", "oct", "10", "nov", "11", "dec", "12")
var dayNames = alphas("-sun", "-7", "sun", "0", "mon", "1", "tue", "2", "wed", "3", "thu", "4", "fri", "5", "sat", "6")

var (
	illegalDay     = regexp.MustCompile(`[^/*0-9,\-WwLl]+`)
	illegalWeekday = regexp.MustCompile(`[^/*0-9,\-#Ll]+`)
	illegalOther   = regexp.MustCompile(`[^/*0-9,\-]+`)
	rangeStepping  = regexp.MustCompile(`^(\d+)-(\d+)/(\d+)$`)
)

func cronError(message string) error { return errors.New("CronPattern: " + message) }

func parseCronPattern(source string) (*cronPattern, error) {
	pattern := source
	if strings.Contains(pattern, "@") {
		lowered := strings.ToLower(strings.TrimSpace(pattern))
		if lowered == "@reboot" {
			return nil, cronError("@reboot is not supported in this environment. This is an event-based trigger that requires system startup detection.")
		}
		if replaced, ok := cronNicknames[lowered]; ok {
			pattern = replaced
		}
		pattern = strings.TrimSpace(pattern)
	}
	parts := strings.Fields(pattern)
	if len(parts) == 0 {
		parts = []string{""}
	}
	if len(parts) < 5 || len(parts) > 7 {
		return nil, cronError("invalid configuration format ('" + pattern + "'), exactly five, six, or seven space separated parts are required.")
	}
	if len(parts) != 5 {
		return nil, cronError("only five-part patterns are supported here ('" + pattern + "').")
	}
	if strings.Contains(pattern, "?") {
		for index := range parts {
			parts[index] = strings.ReplaceAll(parts[index], "?", "*")
		}
	}
	minute, hour, day, month, weekday := parts[0], parts[1], parts[2], parts[3], parts[4]
	p := &cronPattern{}
	if strings.ToUpper(day) == "LW" {
		p.lastWeekday = true
		day = ""
	} else if strings.Contains(strings.ToUpper(day), "L") {
		day = strings.NewReplacer("L", "", "l", "").Replace(day)
		p.lastDayOfMonth = true
	}
	p.starDOM = day == "*"
	if len(month) >= 3 {
		month = replaceAlpha(month, monthNames)
	}
	if len(weekday) >= 3 {
		weekday = replaceAlpha(weekday, dayNames)
	}
	if strings.HasPrefix(weekday, "+") {
		p.useAndLogic = true
		weekday = weekday[1:]
		if weekday == "" {
			return nil, cronError("Day-of-week field cannot be empty after '+' modifier.")
		}
	}
	p.starDOW = weekday == "*"
	entries := []string{"0", minute, hour, day, month, weekday, "*"}
	for index, entry := range entries {
		illegal := illegalOther
		switch index {
		case 3:
			illegal = illegalDay
		case 5:
			illegal = illegalWeekday
		}
		if illegal.MatchString(entry) {
			return nil, cronError("configuration entry " + strconv.Itoa(index) + " (" + entry + ") contains illegal characters.")
		}
	}
	for _, part := range []struct {
		name   string
		value  string
		offset int
	}{{"minute", minute, 0}, {"hour", hour, 0}, {"day", day, -1}, {"month", month, -1}, {"dayOfWeek", weekday, 0}} {
		fill := nthValue{number: 1}
		if part.name == "dayOfWeek" {
			fill = nthValue{number: 63}
		}
		if err := p.partToArray(part.name, part.value, part.offset, fill); err != nil {
			return nil, err
		}
	}
	single := func(entry string, set []bool) bool {
		count := 0
		for _, on := range set {
			if on {
				count++
			}
		}
		return !strings.Contains(entry, "*") && !strings.Contains(entry, "/") && count == 1
	}
	p.fixedTime = single(minute, p.minute[:]) && single(hour, p.hour[:])
	return p, nil
}

func replaceAlpha(value string, names []alpha) string {
	for _, name := range names {
		value = name.name.ReplaceAllLiteralString(value, name.value)
	}
	return value
}

// nthValue is what a part is set to: a number, or for a weekday the nth text after `#`, or "L".
type nthValue struct {
	number float64
	text   string
	isText bool
}

func (p *cronPattern) length(name string) int {
	switch name {
	case "minute":
		return 60
	case "hour":
		return 24
	case "day", "nearestWeekdays":
		return 31
	case "month":
		return 12
	}
	return 7
}

func (p *cronPattern) partToArray(name, value string, offset int, fill nthValue) error {
	if value == "" && !(name == "day" && (p.lastDayOfMonth || p.lastWeekday)) {
		return cronError("configuration entry " + name + " (" + value + ") is empty, check for trailing spaces.")
	}
	if value == "*" {
		for index := range p.length(name) {
			if err := p.setPart(name, index, fill); err != nil {
				return err
			}
		}
		return nil
	}
	list := strings.Split(value, ",")
	if len(list) > 1 {
		for _, item := range list {
			if err := p.partToArray(name, item, offset, fill); err != nil {
				return err
			}
		}
		return nil
	}
	switch {
	case strings.Contains(value, "-") && strings.Contains(value, "/"):
		return p.handleRangeWithStepping(value, name, offset, fill)
	case strings.Contains(value, "-"):
		return p.handleRange(value, name, offset, fill)
	case strings.Contains(value, "/"):
		return p.handleStepping(value, name, fill)
	case value != "":
		return p.handleNumber(value, name, offset, fill)
	}
	return nil
}

// jsParseInt is parseInt(text, 10); false for NaN.
func jsParseInt(text string) (int, bool) {
	text = strings.TrimLeft(text, " \t\n\r\v\f")
	sign := 1
	if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		if text[0] == '-' {
			sign = -1
		}
		text = text[1:]
	}
	end := 0
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	value, err := strconv.Atoi(text[:end])
	if err != nil {
		return math.MaxInt32, true
	}
	return sign * value, true
}

func extractNth(value, name string) (string, *nthValue, error) {
	if strings.Contains(value, "#") {
		if name != "dayOfWeek" {
			return "", nil, errors.New("CronPattern: nth (#) only allowed in day-of-week field")
		}
		pieces := strings.Split(value, "#")
		return pieces[0], &nthValue{text: pieces[1], isText: true}, nil
	}
	if strings.HasSuffix(strings.ToUpper(value), "L") {
		if name != "dayOfWeek" {
			return "", nil, errors.New("CronPattern: L modifier only allowed in day-of-week field (use L alone for day-of-month)")
		}
		return value[:len(value)-1], &nthValue{text: "L", isText: true}, nil
	}
	return value, nil, nil
}

// orFill is `r[1] || s`: the extracted nth unless it is empty.
func orFill(nth *nthValue, fill nthValue) nthValue {
	if nth != nil && nth.text != "" {
		return *nth
	}
	return fill
}

func (p *cronPattern) handleNumber(value, name string, offset int, fill nthValue) error {
	number, nth, err := extractNth(value, name)
	if err != nil {
		return err
	}
	nearest := strings.Contains(strings.ToUpper(value), "W")
	if name != "day" && nearest {
		return cronError("Nearest weekday modifier (W) only allowed in day-of-month.")
	}
	if nearest {
		name = "nearestWeekdays"
	}
	parsed, ok := jsParseInt(number)
	if !ok {
		return cronError(name + " is not a number: '" + value + "'")
	}
	return p.setPart(name, parsed+offset, orFill(nth, fill))
}

func (p *cronPattern) setPart(name string, index int, value nthValue) error {
	invalid := func() error {
		return cronError("Invalid value for " + name + ": " + strconv.Itoa(index))
	}
	switch name {
	case "dayOfWeek":
		if index == 7 {
			index = 0
		}
		if index < 0 || index > 6 {
			return invalid()
		}
		return p.setNthWeekday(index, value)
	case "minute":
		if index < 0 || index >= 60 {
			return invalid()
		}
		p.minute[index] = true
	case "hour":
		if index < 0 || index >= 24 {
			return invalid()
		}
		p.hour[index] = true
	case "day":
		if index < 0 || index >= 31 {
			return invalid()
		}
		p.day[index] = true
	case "nearestWeekdays":
		if index < 0 || index >= 31 {
			return invalid()
		}
		p.nearestWeekdays[index] = true
	case "month":
		if index < 0 || index >= 12 {
			return invalid()
		}
		p.month[index] = true
	}
	return nil
}

func (p *cronPattern) setNthWeekday(index int, value nthValue) error {
	if value.isText && strings.ToUpper(value.text) == "L" {
		p.dayOfWeek[index] |= 32
		return nil
	}
	if !value.isText && value.number == 63 {
		p.dayOfWeek[index] = 63
		return nil
	}
	number := value.number
	shown := strconv.FormatFloat(number, 'f', -1, 64)
	kind := "number"
	if value.isText {
		kind = "string"
		shown = value.text
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value.text), 64)
		if err != nil {
			parsed = math.NaN()
		}
		if strings.TrimSpace(value.text) == "" {
			parsed = 0
		}
		number = parsed
	}
	if number < 6 && number > 0 {
		if number == math.Trunc(number) {
			p.dayOfWeek[index] |= nthBits[int(number)-1]
		}
		return nil
	}
	return cronError("nth weekday out of range, should be 1-5 or L. Value: " + shown + ", Type: " + kind)
}

func (p *cronPattern) validateRange(from, to int, step *int, name, original string) error {
	if from > to {
		return cronError("From value is larger than to value: '" + original + "'")
	}
	if step != nil {
		if *step == 0 {
			return cronError("Syntax error, illegal stepping: 0")
		}
		if *step > p.length(name) {
			return cronError("Syntax error, steps cannot be greater than maximum value of part (" + strconv.Itoa(p.length(name)) + ")")
		}
	}
	return nil
}

func (p *cronPattern) handleRangeWithStepping(value, name string, offset int, fill nthValue) error {
	if strings.Contains(strings.ToUpper(value), "W") {
		return cronError("Syntax error, W is not allowed in ranges with stepping.")
	}
	bare, nth, err := extractNth(value, name)
	if err != nil {
		return err
	}
	match := rangeStepping.FindStringSubmatch(bare)
	if match == nil {
		return cronError("Syntax error, illegal range with stepping: '" + value + "'")
	}
	from, _ := jsParseInt(match[1])
	to, _ := jsParseInt(match[2])
	step, _ := jsParseInt(match[3])
	from, to = from+offset, to+offset
	if err := p.validateRange(from, to, &step, name, value); err != nil {
		return err
	}
	for index := from; index <= to; index += step {
		if err := p.setPart(name, index, orFill(nth, fill)); err != nil {
			return err
		}
	}
	return nil
}

func (p *cronPattern) handleRange(value, name string, offset int, fill nthValue) error {
	if strings.Contains(strings.ToUpper(value), "W") {
		return cronError("Syntax error, W is not allowed in a range.")
	}
	bare, nth, err := extractNth(value, name)
	if err != nil {
		return err
	}
	bounds := strings.Split(bare, "-")
	if len(bounds) != 2 {
		return cronError("Syntax error, illegal range: '" + value + "'")
	}
	from, ok := jsParseInt(bounds[0])
	if !ok {
		return cronError("Syntax error, illegal lower range (NaN)")
	}
	to, ok := jsParseInt(bounds[1])
	if !ok {
		return cronError("Syntax error, illegal upper range (NaN)")
	}
	from, to = from+offset, to+offset
	if err := p.validateRange(from, to, nil, name, value); err != nil {
		return err
	}
	for index := from; index <= to; index++ {
		if err := p.setPart(name, index, orFill(nth, fill)); err != nil {
			return err
		}
	}
	return nil
}

func (p *cronPattern) handleStepping(value, name string, fill nthValue) error {
	if strings.Contains(strings.ToUpper(value), "W") {
		return cronError("Syntax error, W is not allowed in parts with stepping.")
	}
	bare, nth, err := extractNth(value, name)
	if err != nil {
		return err
	}
	pieces := strings.Split(bare, "/")
	if len(pieces) != 2 {
		return cronError("Syntax error, illegal stepping: '" + value + "'")
	}
	if pieces[0] == "" {
		return cronError("Syntax error, stepping with missing prefix ('" + value + "') is not allowed. Use wildcard (*/step) or range (min-max/step) instead.")
	}
	if pieces[0] != "*" {
		return cronError("Syntax error, stepping with numeric prefix ('" + value + "') is not allowed. Use wildcard (*/step) or range (min-max/step) instead.")
	}
	step, ok := jsParseInt(pieces[1])
	if !ok {
		return cronError("Syntax error, illegal stepping: (NaN)")
	}
	if err := p.validateRange(0, p.length(name)-1, &step, name, value); err != nil {
		return err
	}
	if step < 0 {
		return nil
	}
	for index := 0; index < p.length(name); index += step {
		if err := p.setPart(name, index, orFill(nth, fill)); err != nil {
			return err
		}
	}
	return nil
}
