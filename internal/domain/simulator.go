package domain

import (
	"math"
	"regexp"
	"slices"
	"strings"
)

var NoSimulators = SimulatorState{
	Availability: SimulatorAvailabilityUnknown{},
	Devices:      []*SimulatorDevice{},
	Booting:      []string{},
	Errors:       map[string]string{},
	Driver:       nil,
	Orientations: map[string]SimulatorOrientation{},
}

var SimulatorOrientations = []SimulatorOrientation{
	SimulatorOrientationPortrait,
	SimulatorOrientationLandscapeLeft,
	SimulatorOrientationLandscapeRight,
	SimulatorOrientationPortraitUpsideDown,
}

// OrientationCodes is UIDeviceOrientation's own number for each.
var OrientationCodes = map[SimulatorOrientation]int{
	SimulatorOrientationPortrait:           1,
	SimulatorOrientationPortraitUpsideDown: 2,
	SimulatorOrientationLandscapeLeft:      3,
	SimulatorOrientationLandscapeRight:     4,
}

// quarterTurns is how far clockwise the panel turns the device's portrait picture so it reads upright.
var quarterTurns = map[SimulatorOrientation]int{
	SimulatorOrientationPortrait:           0,
	SimulatorOrientationLandscapeRight:     1,
	SimulatorOrientationPortraitUpsideDown: 2,
	SimulatorOrientationLandscapeLeft:      3,
}

var byTurns = []SimulatorOrientation{
	SimulatorOrientationPortrait,
	SimulatorOrientationLandscapeRight,
	SimulatorOrientationPortraitUpsideDown,
	SimulatorOrientationLandscapeLeft,
}

func ScreenTurns(orientation SimulatorOrientation) int {
	return quarterTurns[orientation]
}

// Turned is the orientation after a quarter turn: clockwise for 1, counterclockwise for -1.
func Turned(orientation SimulatorOrientation, direction int) SimulatorOrientation {
	return byTurns[(quarterTurns[orientation]+direction+4)%4]
}

// Untilted is where a point on the turned picture lands on the device's own portrait screen, as fractions from the top left.
func Untilted(u, v float64, orientation SimulatorOrientation) (x, y float64) {
	switch quarterTurns[orientation] {
	case 1:
		return v, 1 - u
	case 2:
		return 1 - u, 1 - v
	case 3:
		return 1 - v, u
	default:
		return u, v
	}
}

const (
	MaxSimulatorText = 2_000
	MaxPasteText     = 100_000
	MaxSwipeMs       = 5_000
)

// maxKeyUsage is the longest a key's HID usage code runs: the keyboard page ends at 0xE7.
const maxKeyUsage = 0xe7

func fraction(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && number <= 1
}

func simulatorIsRecord(value any) bool {
	_, ok := value.(map[string]any)
	return ok
}

// maxPoints is the largest screen any simulator has, in points, with room to spare.
const maxPoints = 10_000

func positioned(value map[string]any, coordinates ...any) bool {
	points, present := value["points"]
	if !present {
		return !slices.ContainsFunc(coordinates, func(coordinate any) bool { return !fraction(coordinate) })
	}
	if points != true {
		return false
	}
	for _, coordinate := range coordinates {
		number, ok := featuresNumber(coordinate)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > maxPoints {
			return false
		}
	}
	return true
}

func keyUsage(usage any) bool {
	number, ok := featuresNumber(usage)
	return ok && featuresInteger(number) && number > 0 && number <= maxKeyUsage
}

// UsesMacClipboard: inputs that move text between the device and the Mac's clipboard, which only a person at the panel sends.
func UsesMacClipboard(input SimulatorInput) bool {
	switch input.(type) {
	case SimulatorInputShortcut, SimulatorInputPaste, SimulatorInputPasteboard:
		return true
	}
	return false
}

// IsSimulatorInput checks a JSON-decoded value (map[string]any, float64, …).
func IsSimulatorInput(value any) bool {
	input, ok := value.(map[string]any)
	if !ok {
		return false
	}
	textOf := func(key string, limit int) bool {
		text, ok := input[key].(string)
		length := featuresUTF16Len(text)
		return ok && length > 0 && length <= limit
	}
	switch input["kind"] {
	case "touch":
		_, hasX2 := input["x2"]
		_, hasY2 := input["y2"]
		phase := input["phase"]
		return (phase == "down" || phase == "move" || phase == "up") && fraction(input["x"]) && fraction(input["y"]) &&
			(!hasX2 && !hasY2 || fraction(input["x2"]) && fraction(input["y2"]))
	case "tap":
		return positioned(input, input["x"], input["y"])
	case "tap-element":
		return textOf("ref", 64)
	case "swipe":
		if !positioned(input, input["x"], input["y"]) || !positioned(input, input["toX"], input["toY"]) {
			return false
		}
		duration, present := input["durationMs"]
		if !present {
			return true
		}
		ms, ok := featuresNumber(duration)
		return ok && ms >= 0 && ms <= MaxSwipeMs
	case "button":
		return input["button"] == "home" || input["button"] == "lock"
	case "type":
		return textOf("text", MaxSimulatorText)
	case "rotate":
		orientation, ok := input["orientation"].(string)
		return ok && slices.Contains(SimulatorOrientations, SimulatorOrientation(orientation))
	case "key":
		_, down := input["down"].(bool)
		return keyUsage(input["usage"]) && down
	case "shortcut":
		return keyUsage(input["usage"])
	case "paste":
		return textOf("text", MaxPasteText)
	case "pasteboard":
		return input["phase"] == "enter" || input["phase"] == "leave"
	}
	return false
}

// DefaultSimulator is the device a panel shows when its dock has none; remembered nil for none. Nil when there are no devices.
func DefaultSimulator(devices []*SimulatorDevice, remembered *string) *SimulatorDevice {
	if remembered != nil {
		for _, device := range devices {
			if device.UDID == *remembered {
				return device
			}
		}
	}
	for _, device := range devices {
		if device.State == SimulatorDeviceStateBooted {
			return device
		}
	}
	for _, device := range devices {
		if strings.HasPrefix(device.Name, "iPhone") {
			return device
		}
	}
	if len(devices) == 0 {
		return nil
	}
	return devices[0]
}

func SimulatorLabel(device *SimulatorDevice) string {
	return device.Name + " · " + device.Runtime
}

const letters = "abcdefghijklmnopqrstuvwxyz"

var (
	simulatorDigit  = regexp.MustCompile(`^Digit[1-9]$`)
	simulatorNumpad = regexp.MustCompile(`^Numpad[1-9]$`)
)

var simulatorNamedKeys = map[string]int{
	"Digit0": 39, "Enter": 40, "NumpadEnter": 40, "Escape": 41, "Backspace": 42, "Tab": 43, "Space": 44, "Minus": 45, "Equal": 46,
	"BracketLeft": 47, "BracketRight": 48, "Backslash": 49, "Semicolon": 51, "Quote": 52, "Backquote": 53, "Comma": 54, "Period": 55, "Slash": 56,
	"IntlBackslash": 100, "Home": 74, "PageUp": 75, "Delete": 76, "End": 77, "PageDown": 78,
	"ArrowRight": 79, "ArrowLeft": 80, "ArrowDown": 81, "ArrowUp": 82,
	"NumpadDivide": 84, "NumpadMultiply": 85, "NumpadSubtract": 86, "NumpadAdd": 87, "Numpad0": 98, "NumpadDecimal": 99, "NumpadEqual": 103,
	"ControlLeft": 224, "ShiftLeft": 225, "AltLeft": 226, "ControlRight": 228, "ShiftRight": 229, "AltRight": 230,
}

// HidUsage is the USB HID usage of a physical key, by `KeyboardEvent.code`; false for a key a phone's keyboard lacks.
func HidUsage(code string) (int, bool) {
	switch {
	case letterCode.MatchString(code):
		return 4 + strings.IndexByte(letters, code[3]+('a'-'A')), true
	case simulatorDigit.MatchString(code):
		return 29 + int(code[5]-'0'), true
	case simulatorNumpad.MatchString(code):
		return 88 + int(code[6]-'0'), true
	}
	usage, ok := simulatorNamedKeys[code]
	return usage, ok
}

// ScreenCornerRadius is the screen's corner radius as a fraction of its width.
func ScreenCornerRadius(name string) float64 {
	if strings.HasPrefix(name, "iPad") {
		return 0.024
	}
	return 0.137
}

// SimulatorGroups is the devices a picker lists, narrowed by what is typed.
func SimulatorGroups(devices []*SimulatorDevice, query string) []*SimulatorGroup {
	ranked := RankedMatches(devices, query, func(device *SimulatorDevice) []string {
		return []string{device.Name, device.Runtime, device.Runtime + " " + device.Name}
	})
	// A query that names a device outright puts it first, ahead of the longer names it is the start of.
	wanted := featuresJSLower(featuresJSTrim(query))
	var exact, rest []*SimulatorDevice
	for _, device := range ranked {
		if featuresJSLower(device.Name) == wanted {
			exact = append(exact, device)
		} else {
			rest = append(rest, device)
		}
	}
	matched := append(exact, rest...)
	var running []*SimulatorDevice
	for _, device := range matched {
		if device.State == SimulatorDeviceStateBooted || device.State == SimulatorDeviceStateBooting {
			running = append(running, device)
		}
	}
	groups := []*SimulatorGroup{}
	if len(running) > 0 {
		groups = append(groups, &SimulatorGroup{Label: "Running", Running: true, Devices: running})
	}
	for _, device := range matched {
		if slices.Contains(running, device) {
			continue
		}
		index := slices.IndexFunc(groups, func(group *SimulatorGroup) bool { return !group.Running && group.Label == device.Runtime })
		if index >= 0 {
			groups[index].Devices = append(groups[index].Devices, device)
		} else {
			groups = append(groups, &SimulatorGroup{Label: device.Runtime, Devices: []*SimulatorDevice{device}})
		}
	}
	// While searching, the group holding the best match leads, so Enter takes what heads the list.
	if wanted != "" {
		slices.SortStableFunc(groups, func(left, right *SimulatorGroup) int {
			return slices.Index(matched, left.Devices[0]) - slices.Index(matched, right.Devices[0])
		})
	}
	return groups
}
