package devices

import (
	"context"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	ScreenshotContextMaxElements = 2_000
	ScreenshotContextMaxDepth    = 50
	maxResultLength              = 1_000_000
	maxLineLength                = 1_200
)

// ScreenshotTarget is the window a screenshot was taken of.
type ScreenshotTarget struct {
	Platform domain.ScreenshotPlatform
	PID      int64
	WindowID int64
	App      string
	Title    string
}

// ToolAnswer is what one driver tool call answers: whether it failed, and its structured content as JSON ("" for none).
type ToolAnswer struct {
	IsError        bool
	StructuredJSON string
}

// ContextDriver is the computer-use driver's tools, however they are reached.
type ContextDriver interface {
	CallTool(ctx context.Context, name string, argumentsJSON string) (ToolAnswer, error)
}

func unavailableSnapshot(reason domain.AccessibilitySnapshotUnavailableReason) domain.AccessibilitySnapshot {
	return domain.AccessibilitySnapshotUnavailable{Reason: reason}
}

var hyprlandAddress = regexp.MustCompile(`(?i)^0x[\da-f]+$`)

// HyprlandAccessibilityWindowID is the window id the driver knows a Hyprland window by: its full
// compositor address, never grim's stableId. False for anything else.
func HyprlandAccessibilityWindowID(address any) (int64, bool) {
	text, ok := address.(string)
	if !ok || !hyprlandAddress.MatchString(text) {
		return 0, false
	}
	id, err := strconv.ParseUint(text[2:], 16, 64)
	if err != nil || id == 0 || id > 1<<53-1 {
		return 0, false
	}
	return int64(id), true
}

var sparseMarkers = regexp.MustCompile(`(?:^|\n)` + jsSpace + `*- (?:AXStaticText|AXTextArea|AXHeading|AXOutline|AXTable|label|static|paragraph|heading|text|table)(?:` + jsSpace + `|$)`)

// SnapshotWindowAccessibility is a bounded native read of the target window, with no screenshot,
// input action, or focus change. partial hears a first read that a retry may still improve on.
func SnapshotWindowAccessibility(ctx context.Context, driver ContextDriver, target ScreenshotTarget, partial func(domain.AccessibilitySnapshot)) (domain.AccessibilitySnapshot, error) {
	input, err := jsonx.Marshal(struct {
		PID               int64 `json:"pid"`
		WindowID          int64 `json:"window_id"`
		IncludeScreenshot bool  `json:"include_screenshot"`
		MaxElements       int   `json:"max_elements"`
		MaxDepth          int   `json:"max_depth"`
	}{target.PID, target.WindowID, false, ScreenshotContextMaxElements, ScreenshotContextMaxDepth})
	if err != nil {
		return nil, err
	}
	read := func() (domain.AccessibilitySnapshot, error) {
		result, err := driver.CallTool(ctx, "get_window_state", string(input))
		if err != nil {
			return nil, err
		}
		if result.IsError || result.StructuredJSON == "" || utf16Length(result.StructuredJSON) > maxResultLength {
			return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed), nil
		}
		var snapshot any
		if err := jsonx.Unmarshal([]byte(result.StructuredJSON), &snapshot); err != nil {
			return nil, err
		}
		return AccessibilityFromSnapshot(snapshot, target), nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	// A newly enabled Chromium tree can at first show only its title bar. It gets to settle once,
	// within the same process and deadline; the first usable read stands if the second stalls.
	captured, isCaptured := first.(domain.AccessibilitySnapshotCaptured)
	sparse := isCaptured && !sparseMarkers.MatchString(captured.Text)
	retry := sparse
	if missing, ok := first.(domain.AccessibilitySnapshotUnavailable); ok && (missing.Reason == domain.AccessibilitySnapshotUnavailableReasonWindow || missing.Reason == domain.AccessibilitySnapshotUnavailableReasonEmpty) {
		retry = true
	}
	if !retry {
		return first, nil
	}
	if isCaptured && partial != nil {
		partial(first)
	}
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if _, ok := second.(domain.AccessibilitySnapshotCaptured); ok {
		return second, nil
	}
	return first, nil
}

func record(value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	return object, ok && object != nil
}

var (
	macRoot          = regexp.MustCompile(`^AXWindow$`)
	linuxRoot        = regexp.MustCompile(`^(?:frame|window|dialog|alert|file chooser)$`)
	linuxRootPrefix  = regexp.MustCompile(`^- (?:\[\d+\] )?(?:frame|window|dialog|alert|file chooser)(?: =)? `)
	checkableRole    = regexp.MustCompile(`(?i)check.?box|radio.?button`)
	passiveRole      = regexp.MustCompile(`^(?:AXStaticText|AXHeading|label|static|heading|paragraph)$`)
	valueBreaks      = regexp.MustCompile(`\r?\n` + jsSpace + `*`)
	elementIndex     = regexp.MustCompile(`^(` + jsSpace + `*-?` + jsSpace + `*)\[(?:element_index )?\d+\]` + jsSpace + `*`)
	trailingTokens   = regexp.MustCompile(jsSpace + `*\[(?:id=|help=|actions=)[^\n\r\x{2028}\x{2029}]*\]` + jsSpace + `*$`)
	controlCharacter = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
)

// AccessibilityFromSnapshot reads a driver's window state. The structured element list leaves out
// passive labels, so the Markdown tree is kept for those, with indexed controls given their typed
// states. Actionable snapshot tokens are never passed on.
func AccessibilityFromSnapshot(snapshot any, target ScreenshotTarget) domain.AccessibilitySnapshot {
	object, ok := record(snapshot)
	windowTitle, hasTitle := object["window_title"].(string)
	if !ok || object["pid"] != float64(target.PID) || object["window_id"] != float64(target.WindowID) || (hasTitle && windowTitle != target.Title) {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow)
	}
	// The driver marks ambiguous Hyprland AT-SPI matches, among others, as application-scoped.
	if object["degraded"] == true {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow)
	}
	markdown, ok := object["tree_markdown"].(string)
	if !ok || strings.Trim(markdown, jsSpaces) == "" {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonEmpty)
	}
	allLines, truncated := ScreenshotTree(markdown, target.Platform)
	// macOS also supplies the application's menu bar, including menus outside the captured image.
	// Exactly the captured window's subtree is kept on every platform.
	rootPattern := linuxRoot
	if target.Platform == domain.ScreenshotPlatformMacos {
		rootPattern = macRoot
	}
	rootDepth := math.MaxInt
	var candidates []int
	for index, line := range allLines {
		if rootPattern.MatchString(line.Role) {
			candidates = append(candidates, index)
			rootDepth = min(rootDepth, line.Depth)
		}
	}
	roots := slices.DeleteFunc(candidates, func(index int) bool { return allLines[index].Depth != rootDepth })
	if len(roots) != 1 {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow)
	}
	root := roots[0]
	end := len(allLines)
	for index := root + 1; index < len(allLines); index++ {
		if allLines[index].Depth <= allLines[root].Depth {
			end = index
			break
		}
	}
	lines := allLines[root:end]
	if target.Platform != domain.ScreenshotPlatformMacos {
		// X11 can return an application-wide tree if geometry did not resolve its top-level. Only
		// one named window root is accepted, the captured window's; a sibling is never guessed.
		first := strings.TrimLeft(lines[0].Text, jsSpaces)
		prefix := linuxRootPrefix.FindString(first)
		title := ""
		if prefix != "" {
			title = first[len(prefix):]
		}
		// The driver's tree quotes labels literally, and newlines cannot establish a unique root.
		expected := `"` + target.Title + `"`
		if prefix == "" || strings.ContainsAny(target.Title, "\r\n") || (title != expected && !strings.HasPrefix(title, expected+" [actions=")) {
			return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow)
		}
	}
	elements := map[int]map[string]any{}
	if list, ok := object["elements"].([]any); ok {
		for _, item := range list[:min(len(list), ScreenshotContextMaxElements)] {
			if element, ok := record(item); ok {
				if index, ok := element["element_index"].(float64); ok && index == math.Trunc(index) && math.Abs(index) < 1<<53 {
					elements[int(index)] = element
				}
			}
		}
	}
	type formattedRow struct {
		text     string
		document bool
	}
	var formatted []formattedRow
	protectedDepth, documentDepth := -1, -1
	for _, row := range lines[:min(len(lines), ScreenshotContextMaxElements*2)] {
		if documentDepth >= 0 && row.Depth <= documentDepth {
			documentDepth = -1
		}
		if documentDepth < 0 && IsDocumentRole(row.Role) {
			documentDepth = row.Depth
		}
		if protectedDepth >= 0 && row.Depth > protectedDepth {
			continue
		}
		protectedDepth = -1
		if IsProtectedRole(row.Role) {
			protectedDepth = row.Depth
			continue
		}
		var element map[string]any
		if row.Index >= 0 {
			element = elements[row.Index]
		}
		var states []string
		if element != nil {
			for _, key := range []string{"enabled", "selected", "checked"} {
				if value, ok := element[key].(bool); ok {
					states = append(states, key+"="+strconv.FormatBool(value))
				}
			}
			role, hasRole := element["role"]
			if _, checked := element["checked"].(bool); !checked && checkableRole.MatchString(jsValueString(role, hasRole)) {
				value, hasValue := element["value"]
				switch jsValueString(value, hasValue) {
				case "1", "true", "on":
					states = append(states, "checked=true")
				case "0", "false", "off":
					states = append(states, "checked=false")
				}
			}
			if frame, ok := record(element["frame"]); ok && !passiveRole.MatchString(row.Role) {
				parts := []any{frame["x"], frame["y"], frame["w"], frame["h"]}
				numbers := make([]float64, 0, 4)
				for _, part := range parts {
					if number, ok := part.(float64); ok && !math.IsInf(number, 0) && !math.IsNaN(number) {
						numbers = append(numbers, number)
					}
				}
				if len(numbers) == 4 && numbers[2] > 0 && numbers[3] > 0 {
					encoded, _ := jsonx.Marshal(numbers)
					states = append(states, "desktop bounds="+string(encoded))
				}
			}
			// Numeric AX values (checkboxes, sliders) may only be on the structured side.
			if value, ok := element["value"].(string); ok && value != "" {
				rendered := valueBreaks.ReplaceAllString(strings.Trim(value, jsSpaces), `\n`)
				if !strings.Contains(row.Text, `= "`+rendered+`"`) && !strings.Contains(row.Text, `value="`+rendered+`"`) {
					states = append(states, "value="+jsJSONString(utf16Prefix(value, maxLineLength)))
				}
			}
		}
		line := elementIndex.ReplaceAllString(row.Text, "${1}")
		line = trailingTokens.ReplaceAllString(line, "")
		line = controlCharacter.ReplaceAllString(line, "")
		if len(states) > 0 {
			line += " (" + strings.Join(states, ", ") + ")"
		}
		if utf16Length(line) > maxLineLength {
			line = utf16Prefix(line, maxLineLength-1) + "…"
			truncated = true
		}
		formatted = append(formatted, formattedRow{text: line, document: documentDepth >= 0 || element != nil && element["in_web_content"] == true})
	}
	if len(lines) > ScreenshotContextMaxElements*2 {
		truncated = true
	}
	fit := func(rows []formattedRow, budget int) (string, int) {
		var kept []string
		used := 0
		for _, row := range rows {
			length := utf16Length(row.text)
			if used+length+1 > budget {
				truncated = true
				continue
			}
			kept = append(kept, row.text)
			used += length + 1
		}
		return strings.Join(kept, "\n"), used
	}
	var documents, others []formattedRow
	for _, row := range formatted {
		if row.document {
			documents = append(documents, row)
		} else {
			others = append(others, row)
		}
	}
	// Room is kept for document content even when bookmarks, tabs, or app controls are extensive.
	var chrome string
	var used int
	if len(documents) > 0 {
		chrome, used = fit(others, 2_000)
	} else {
		chrome, used = fit(formatted, domain.MaxScreenshotContextText)
	}
	page, _ := fit(documents, domain.MaxScreenshotContextText-used)
	var parts []string
	for _, part := range []string{chrome, page} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	text := strings.Trim(strings.Join(parts, "\n"), jsSpaces)
	if text == "" {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonEmpty)
	}
	return domain.AccessibilitySnapshotCaptured{Text: text, Truncated: truncated}
}

// jsValueString is String(value) for a value read from JSON.
func jsValueString(value any, present bool) string {
	switch value := value.(type) {
	case nil:
		if present {
			return "null"
		}
		return "undefined"
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		encoded, _ := jsonx.Marshal(value)
		return string(encoded)
	case []any:
		parts := make([]string, len(value))
		for index, item := range value {
			if item != nil {
				parts[index] = jsValueString(item, true)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// utf16Prefix is text.slice(0, count), never splitting a surrogate pair.
func utf16Prefix(text string, count int) string {
	units := 0
	for at, r := range text {
		width := 1
		if r >= 0x10000 {
			width = 2
		}
		if units+width > count {
			return text[:at]
		}
		units += width
	}
	return text
}

// jsJSONString is JSON.stringify of a string.
func jsJSONString(text string) string {
	const hex = "0123456789abcdef"
	var out strings.Builder
	out.WriteByte('"')
	for _, r := range text {
		switch {
		case r == '"':
			out.WriteString(`\"`)
		case r == '\\':
			out.WriteString(`\\`)
		case r == '\b':
			out.WriteString(`\b`)
		case r == '\f':
			out.WriteString(`\f`)
		case r == '\n':
			out.WriteString(`\n`)
		case r == '\r':
			out.WriteString(`\r`)
		case r == '\t':
			out.WriteString(`\t`)
		case r < 0x20:
			out.WriteString(`\u00`)
			out.WriteByte(hex[r>>4])
			out.WriteByte(hex[r&15])
		default:
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return out.String()
}
