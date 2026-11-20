package desktop

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
)

// Call names, as WindowDesktopCall names them.
const (
	CallOpenFolder            = "openFolder"
	CallProjectlessWorkspace  = "projectlessWorkspace"
	CallCommands              = "commands"
	CallBranches              = "branches"
	CallCommits               = "commits"
	CallDiffPatch             = "diffPatch"
	CallDescribeFiles         = "describeFiles"
	CallSaveAttachment        = "saveAttachment"
	CallReadAttachment        = "readAttachment"
	CallReadAttachmentContext = "readAttachmentContext"
	CallSetBrowserBounds      = "setBrowserBounds"
	CallFocusBrowserTab       = "focusBrowserTab"
	CallTerminalSnapshot      = "terminalSnapshot"
	CallReadRemoteTerminal    = "readRemoteTerminal"
	CallSetTheme              = "setTheme"
	CallSetWindowGlassRegions = "setWindowGlassRegions"
	CallWatchSimulator        = "watchSimulator"
	CallUnwatchSimulator      = "unwatchSimulator"
)

const (
	maxIDLength   = 256
	maxPathLength = 4_096
	// How many paths one drop may name.
	maxDescribedFiles = 20
	maxGlassRegions   = 32
	maxCommitChecks   = 64
)

// guard checks one argument; nil is an argument the window left undefined.
type guard func(value any, present bool) bool

// Calls about the window's own frame, answered without waiting for the services.
var frameCalls = map[string]bool{CallSetTheme: true, CallSetBrowserBounds: true, CallSetWindowGlassRegions: true}

// Every call the window makes, with the checks its arguments pass before anything runs.
var callGuards = map[string][]guard{
	CallOpenFolder:            {},
	CallProjectlessWorkspace:  {},
	CallCommands:              {isID, isAgentEngine},
	CallBranches:              {isID},
	CallCommits:               {isID, isCommitHashes},
	CallDiffPatch:             {isID, isDiffRange, isPath, optional(isPath), optional(isBool)},
	CallDescribeFiles:         {isArray},
	CallSaveAttachment:        {isAnyString, optional(isPath)},
	CallReadAttachment:        {isPath},
	CallReadAttachmentContext: {isPath},
	CallSetBrowserBounds:      {nullOr(isBrowserBounds)},
	CallFocusBrowserTab:       {isID},
	CallTerminalSnapshot:      {isID},
	CallReadRemoteTerminal:    {isID, isID, optional(isSequence)},
	CallSetTheme:              {isWindowThemeValue},
	CallSetWindowGlassRegions: {isGlassRegions},
	CallWatchSimulator:        {isID},
	CallUnwatchSimulator:      {isID},
}

// CallNames lists every WindowDesktopCall.
func CallNames() []string {
	names := make([]string, 0, len(callGuards))
	for name := range callGuards {
		names = append(names, name)
	}
	return names
}

var (
	errUnknownCall = errors.New("Unknown desktop call.")
)

// checkCall answers whether a call by this name with these arguments may run, with the error the
// window reads when it may not.
func checkCall(name string, args []jsontext.Value) error {
	guards, ok := callGuards[name]
	if !ok {
		return errUnknownCall
	}
	invalid := errors.New("Invalid " + name + " call.")
	if len(args) > len(guards) {
		return invalid
	}
	for index, check := range guards {
		var value any
		present := index < len(args) && args[index] != nil
		if present {
			if err := json.Unmarshal(args[index], &value); err != nil {
				return invalid
			}
		}
		if !check(value, present) {
			return invalid
		}
	}
	return nil
}

func jsLength(text string) int {
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

func isStringOf(maxLength int) guard {
	return func(value any, present bool) bool {
		text, ok := value.(string)
		return present && ok && text != "" && jsLength(text) <= maxLength
	}
}

var (
	isID   = isStringOf(maxIDLength)
	isPath = isStringOf(maxPathLength)
)

func isAnyString(value any, present bool) bool {
	_, ok := value.(string)
	return present && ok
}

func isBool(value any, present bool) bool {
	_, ok := value.(bool)
	return present && ok
}

func isArray(value any, present bool) bool {
	_, ok := value.([]any)
	return present && ok
}

func optional(check guard) guard {
	return func(value any, present bool) bool { return !present || check(value, present) }
}

func nullOr(check guard) guard {
	return func(value any, present bool) bool { return present && value == nil || check(value, present) }
}

func isAgentEngine(value any, present bool) bool {
	switch value {
	case "claude", "codex", "opencode":
		return present
	}
	return false
}

var commitHash = regexp.MustCompile(`^(?i)[a-f\d]{7,40}$`)

func isCommitHashValue(value any) bool {
	text, ok := value.(string)
	return ok && commitHash.MatchString(text)
}

func isCommitHashes(value any, present bool) bool {
	hashes, ok := value.([]any)
	if !present || !ok || len(hashes) == 0 || len(hashes) > maxCommitChecks {
		return false
	}
	for _, hash := range hashes {
		if !isCommitHashValue(hash) {
			return false
		}
	}
	return true
}

func isDiffRange(value any, present bool) bool {
	candidate, ok := value.(map[string]any)
	if !present || !ok {
		return false
	}
	switch candidate["kind"] {
	case "uncommitted":
		return true
	case "commit":
		return isCommitHashValue(candidate["commit"])
	case "branches":
		base, ok := candidate["base"].(string)
		if !ok || base == "" {
			return false
		}
		compare, present := candidate["compare"]
		if !present {
			return false
		}
		_, isText := compare.(string)
		return compare == nil || isText
	}
	return false
}

func isSequence(value any, present bool) bool {
	number, ok := value.(float64)
	return present && ok && number >= 0 && number <= 9007199254740991 && number == math.Trunc(number)
}

func finiteNumbers(record map[string]any, keys ...string) bool {
	for _, key := range keys {
		number, ok := record[key].(float64)
		if !ok || math.IsInf(number, 0) || math.IsNaN(number) {
			return false
		}
	}
	return true
}

func isBrowserBounds(value any, present bool) bool {
	box, ok := value.(map[string]any)
	return present && ok && finiteNumbers(box, "x", "y", "width", "height")
}

func isGlassRegions(value any, present bool) bool {
	regions, ok := value.([]any)
	if !present || !ok || len(regions) > maxGlassRegions {
		return false
	}
	for _, item := range regions {
		region, ok := item.(map[string]any)
		if !ok || !finiteNumbers(region, "x", "y", "width", "height", "radius") {
			return false
		}
		if region["width"].(float64) < 0 || region["height"].(float64) < 0 || region["radius"].(float64) < 0 {
			return false
		}
	}
	return true
}

var canvasColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func isWindowThemeValue(value any, present bool) bool {
	theme, ok := value.(map[string]any)
	if !present || !ok {
		return false
	}
	variant := theme["variant"]
	canvas, isText := theme["canvas"].(string)
	if variant != "dark" && variant != "light" || !isText || !canvasColour.MatchString(canvas) {
		return false
	}
	follow, hasFollow := theme["follow"]
	_, isFlag := follow.(bool)
	return !hasFollow || isFlag
}

// AttachedFileDraft mirrors domain/conversation.ts.
type AttachedFileDraft struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Folder bool   `json:"folder,omitzero"`
}

// describeFiles says what each dropped path is: the name to show, and whether it is a folder.
func describeFiles(ctx context.Context, raw jsontext.Value) ([]AttachedFileDraft, error) {
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	named := make([]string, 0, maxDescribedFiles)
	for _, value := range values {
		if len(named) < maxDescribedFiles && isPath(value, true) {
			named = append(named, value.(string))
		}
	}
	described := []AttachedFileDraft{}
	for _, candidate := range named {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		entry, err := os.Stat(candidate)
		if err != nil || !entry.Mode().IsRegular() && !entry.IsDir() {
			continue
		}
		resolved, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		described = append(described, AttachedFileDraft{Path: resolved, Name: filepath.Base(resolved), Folder: entry.IsDir()})
	}
	return described, nil
}
