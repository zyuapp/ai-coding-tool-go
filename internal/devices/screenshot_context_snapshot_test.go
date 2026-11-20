package devices

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var hyprTarget = ScreenshotTarget{Platform: domain.ScreenshotPlatformLinuxHyprland, PID: 324, WindowID: 0x55aa12345678, App: "Editor", Title: "Draft"}

func baseSnapshot() map[string]any {
	return map[string]any{
		"pid": 324.0, "window_id": float64(0x55aa12345678), "window_title": "Draft",
		"tree_markdown": "- frame = \"Draft\"\n  - label = \"Document ready\"\n  - [0] check box \"Wrap\" [actions=[toggle]]\n  - [1] entry \"Name\" value=\"Ada\" [actions=[]]",
		"elements": []any{
			map[string]any{"element_index": 0.0, "enabled": true, "selected": false, "frame": map[string]any{"x": 5.0, "y": 10.0, "w": 80.0, "h": 25.0}},
			map[string]any{"element_index": 1.0, "value": "Ada", "enabled": true},
		},
	}
}

func merged(base map[string]any, changes map[string]any) map[string]any {
	next := map[string]any{}
	for key, value := range base {
		next[key] = value
	}
	for key, value := range changes {
		next[key] = value
	}
	return next
}

func capturedText(t *testing.T, snapshot domain.AccessibilitySnapshot) domain.AccessibilitySnapshotCaptured {
	t.Helper()
	captured, ok := snapshot.(domain.AccessibilitySnapshotCaptured)
	if !ok {
		t.Fatalf("not captured: %#v", snapshot)
	}
	return captured
}

func mustMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if !regexp.MustCompile(pattern).MatchString(text) {
		t.Fatalf("%q does not match %s", text, pattern)
	}
}

func mustNotMatch(t *testing.T, text, pattern string) {
	t.Helper()
	if regexp.MustCompile(pattern).MatchString(text) {
		t.Fatalf("%q matches %s", text, pattern)
	}
}

func TestHyprlandAddressKeepsAllBits(t *testing.T) {
	if id, ok := HyprlandAccessibilityWindowID("0x55aa12345678"); !ok || id != hyprTarget.WindowID {
		t.Fatalf("got %v %v", id, ok)
	}
	for _, invalid := range []any{"1800000a", nil, "0x0", "0x20000000000001", "0x123garbage", 42.0} {
		if _, ok := HyprlandAccessibilityWindowID(invalid); ok {
			t.Fatalf("%v read as an id", invalid)
		}
	}
}

func TestLinuxSnapshotsKeepPassiveTextAndTypedStateWithoutIndices(t *testing.T) {
	result := capturedText(t, AccessibilityFromSnapshot(baseSnapshot(), hyprTarget))
	mustMatch(t, result.Text, `Document ready`)
	mustMatch(t, result.Text, `selected=false`)
	mustMatch(t, result.Text, `desktop bounds=\[5,10,80,25\]`)
	mustMatch(t, result.Text, `value="Ada"`)
	mustNotMatch(t, result.Text, `\[0\]|\[1\]|actions=`)
	if result.Truncated {
		t.Fatal("truncated")
	}
}

func TestMacSnapshotsKeepLabelsAndNumericValues(t *testing.T) {
	snapshot := merged(baseSnapshot(), map[string]any{
		"tree_markdown": "- AXWindow \"Draft\"\n  - AXStaticText = \"Document ready\"\n  - [element_index 0] AXSlider \"Volume\"\n  - [1] AXCheckBox \"Wrap\"\n- AXMenuBar\n  - AXMenuItem \"Private recent document\"",
		"elements":      []any{map[string]any{"element_index": 0.0, "value": "8", "enabled": true}, map[string]any{"element_index": 1.0, "role": "AXCheckBox", "value": "1"}},
	})
	target := hyprTarget
	target.Platform = domain.ScreenshotPlatformMacos
	result := capturedText(t, AccessibilityFromSnapshot(snapshot, target))
	mustMatch(t, result.Text, `Document ready`)
	mustMatch(t, result.Text, `value="8"`)
	mustMatch(t, result.Text, `checked=true`)
	mustNotMatch(t, result.Text, `AXMenuBar|Private recent document`)
	mustNotMatch(t, result.Text, `element_index`)
}

var allPlatforms = []domain.ScreenshotPlatform{domain.ScreenshotPlatformMacos, domain.ScreenshotPlatformLinuxX11, domain.ScreenshotPlatformLinuxHyprland}

func TestMismatchedOwnersWindowsTitlesAndAmbiguousTreesGiveNoContext(t *testing.T) {
	for _, platform := range allPlatforms {
		target := hyprTarget
		target.Platform = platform
		for _, mismatch := range []map[string]any{{"pid": 123.0}, {"window_id": 42.0}, {"window_title": "Another window"}, {"degraded": true, "degraded_reason": "accessibility_window_identity_unproven"}} {
			if _, ok := AccessibilityFromSnapshot(merged(baseSnapshot(), mismatch), target).(domain.AccessibilitySnapshotUnavailable); !ok {
				t.Fatalf("%s %v gave context", platform, mismatch)
			}
		}
	}
	tree := baseSnapshot()["tree_markdown"].(string)
	for _, platform := range allPlatforms[1:] {
		target := hyprTarget
		target.Platform = platform
		for _, markdown := range []string{
			"- frame = \"Another window\"\n  - [0] button \"Send\"",
			tree + "\n- frame = \"Other\"\n  - label = \"Private\"",
			tree + "\n- frame = \"Draft\"\n  - label = \"Private\"",
			"- [0] button \"No window root\"",
		} {
			if _, ok := AccessibilityFromSnapshot(merged(baseSnapshot(), map[string]any{"tree_markdown": markdown}), target).(domain.AccessibilitySnapshotUnavailable); !ok {
				t.Fatalf("%s %q gave context", platform, markdown)
			}
		}
	}
}

func TestMultilineBrowserLabelsKeepThePageSubtree(t *testing.T) {
	for _, platform := range allPlatforms {
		mac := platform == domain.ScreenshotPlatformMacos
		pick := func(macText, linuxText string) string {
			if mac {
				return macText
			}
			return linuxText
		}
		markdown := strings.Join([]string{
			pick(`- AXWindow "Draft"`, `- frame = "Draft"`),
			pick(`  - [0] AXPopUpButton (Extension`, `  - [0] push button "Extension`),
			pick(`Has access to this site) [actions=[press,showmenu]]`, `Has access to this site" [actions=[press,showmenu]]`),
			pick(`  - AXWebArea "Page"`, `  - document web = "Page"`),
			`    - ` + pick("AXStaticText", "label") + ` = "Page text`,
			`with a second line"`,
			`    - ` + pick("AXSecureTextField", "password text") + ` = "secret`,
			`secret-continuation"`,
			`    - ` + pick("AXStaticText", "label") + ` = "Below the viewport"`,
			pick(`- AXMenuBar`, `- menu bar = "Application"`),
			pick(`  - AXMenuItem "Private recent document"`, `  - menu item = "Private recent document"`),
		}, "\n")
		target := hyprTarget
		target.Platform = platform
		result := capturedText(t, AccessibilityFromSnapshot(merged(baseSnapshot(), map[string]any{"tree_markdown": markdown}), target))
		mustMatch(t, result.Text, `Extension\\nHas access to this site`)
		mustMatch(t, result.Text, `Page text\\nwith a second line`)
		mustMatch(t, result.Text, `Below the viewport`)
		mustNotMatch(t, result.Text, `actions=|secret|Private recent document`)
	}
}

func TestNativeTruncationIsReported(t *testing.T) {
	snapshot := merged(baseSnapshot(), map[string]any{
		"element_count": 12.0, "total_element_count": 12.0,
		"tree_markdown": baseSnapshot()["tree_markdown"].(string) + "\n⚠️  AX tree truncated at 400 nodes (app has a very large accessibility tree). Use pixel clicks.",
	})
	result := capturedText(t, AccessibilityFromSnapshot(snapshot, hyprTarget))
	if !result.Truncated {
		t.Fatal("not truncated")
	}
	mustNotMatch(t, result.Text, `Use pixel clicks|AX tree truncated`)
}

func TestLongAndProtectedContentIsBounded(t *testing.T) {
	lines := []string{`- frame = "Draft"`, `  - [0] password text "Secret" value="do-not-send"`, `    - label = "secret-child"`}
	for index := range 500 {
		lines = append(lines, fmt.Sprintf(`  - label = "Line %d: %s"`, index, strings.Repeat("a", 2000)))
	}
	result := capturedText(t, AccessibilityFromSnapshot(merged(baseSnapshot(), map[string]any{"tree_markdown": strings.Join(lines, "\n")}), hyprTarget))
	mustNotMatch(t, result.Text, `do-not-send|secret-child`)
	if utf16Length(result.Text) > domain.MaxScreenshotContextText {
		t.Fatalf("length %d", utf16Length(result.Text))
	}
	mustMatch(t, result.Text, `Line 0`)
	if !result.Truncated {
		t.Fatal("not truncated")
	}
}

type fakeDriver struct {
	calls []string
	read  func(call int) string
}

func (d *fakeDriver) CallTool(_ context.Context, name string, argumentsJSON string) (ToolAnswer, error) {
	d.calls = append(d.calls, name+" "+argumentsJSON)
	return ToolAnswer{StructuredJSON: d.read(len(d.calls))}, nil
}

func TestTheDriverReadIsWindowSpecificBoundedAndSkipsTheScreenshot(t *testing.T) {
	snapshot, _ := jsonx.Marshal(baseSnapshot())
	driver := &fakeDriver{read: func(int) string { return string(snapshot) }}
	result, err := SnapshotWindowAccessibility(t.Context(), driver, hyprTarget, nil)
	if err != nil {
		t.Fatal(err)
	}
	capturedText(t, result)
	var arguments map[string]any
	if len(driver.calls) != 1 || jsonx.Unmarshal([]byte(strings.TrimPrefix(driver.calls[0], "get_window_state ")), &arguments) != nil {
		t.Fatalf("calls %v", driver.calls)
	}
	want := map[string]any{"pid": 324.0, "window_id": float64(hyprTarget.WindowID), "include_screenshot": false, "max_elements": 2000.0, "max_depth": 50.0}
	if !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments %v", arguments)
	}
}

func TestPersistedContextValidatesBoundedTextAndRealTimestamps(t *testing.T) {
	context := map[string]any{"version": 1.0, "platform": "macos", "app": "Editor", "title": "Draft", "capturedAt": 123.0, "accessibility": map[string]any{"status": "captured", "text": "Content", "truncated": false}}
	if !domain.IsScreenshotContext(context) {
		t.Fatal("valid context refused")
	}
	if domain.IsScreenshotContext(merged(context, map[string]any{"capturedAt": 1.7976931348623157e308})) {
		t.Fatal("huge timestamp accepted")
	}
	if domain.IsScreenshotContext(merged(context, map[string]any{"accessibility": map[string]any{"status": "captured", "text": strings.Repeat("a", domain.MaxScreenshotContextText+1), "truncated": false}})) {
		t.Fatal("long text accepted")
	}
	if domain.IsScreenshotContext(merged(context, map[string]any{"accessibility": map[string]any{"status": "unavailable", "reason": map[string]any{}}})) {
		t.Fatal("odd reason accepted")
	}
}

var macTarget = ScreenshotTarget{Platform: domain.ScreenshotPlatformMacos, PID: 42, WindowID: 17, App: "Browser", Title: "Page"}

func pageContext(markdown string, platform domain.ScreenshotPlatform) domain.AccessibilitySnapshot {
	target := macTarget
	target.Platform = platform
	return AccessibilityFromSnapshot(map[string]any{"pid": 42.0, "window_id": 17.0, "window_title": "Page", "tree_markdown": markdown, "elements": []any{}}, target)
}

func TestPageTextAboutPasswordsDoesNotHideThePage(t *testing.T) {
	result := capturedText(t, pageContext("- AXWindow \"Page\"\n  - AXWebArea \"Reset your password\"\n    - AXStaticText = \"Password reset instructions\"\n    - AXSecureTextField = \"private-value\"\n    - AXButton \"Continue\"", domain.ScreenshotPlatformMacos))
	mustMatch(t, result.Text, `Password reset instructions`)
	mustMatch(t, result.Text, `Continue`)
	mustNotMatch(t, result.Text, `private-value`)
}

func TestBulletedValueTextCannotEndItsWindowSubtree(t *testing.T) {
	for _, platform := range allPlatforms {
		role, root := "label", `- frame = "Page"`
		if platform == domain.ScreenshotPlatformMacos {
			role, root = "AXStaticText", `- AXWindow "Page"`
		}
		result := capturedText(t, pageContext(strings.Join([]string{root, `  - ` + role + ` = "A checklist:`, `- keep this paragraph`, `- preserve this line too"`, `  - ` + role + ` = "Content after the checklist"`}, "\n"), platform))
		mustMatch(t, result.Text, `keep this paragraph`)
		mustMatch(t, result.Text, `Content after the checklist`)
	}
}

func TestDialogNestedInALinuxPageStaysInTheWindow(t *testing.T) {
	result := capturedText(t, pageContext("- frame = \"Page\"\n  - document web = \"Page\"\n    - dialog = \"Settings\"\n      - label = \"Choose your language\"", domain.ScreenshotPlatformLinuxHyprland))
	mustMatch(t, result.Text, `Choose your language`)
}

func TestLargeToolbarCannotUseUpThePagesBudget(t *testing.T) {
	lines := []string{`- AXWindow "Page"`}
	for index := range 200 {
		lines = append(lines, fmt.Sprintf(`  - AXButton "Bookmark %d with a long accessible description %s"`, index, strings.Repeat("x", 80)))
	}
	lines = append(lines, `  - AXWebArea "Page"`, `    - AXStaticText = "The document content the user actually captured"`)
	result := capturedText(t, pageContext(strings.Join(lines, "\n"), domain.ScreenshotPlatformMacos))
	mustMatch(t, result.Text, `The document content the user actually captured`)
}

func TestWebContentMarkersKeepPageTextWithoutANamedDocumentRoot(t *testing.T) {
	lines := []string{`- AXWindow "Page"`}
	for index := range 150 {
		lines = append(lines, fmt.Sprintf(`  - AXButton "Bookmark %d %s"`, index, strings.Repeat("x", 100)))
	}
	lines = append(lines, `  - [200] AXStaticText = "Page with no named web-area wrapper `+strings.Repeat("document text ", 120)+`"`)
	result := capturedText(t, AccessibilityFromSnapshot(map[string]any{"pid": 42.0, "window_id": 17.0, "window_title": "Page", "tree_markdown": strings.Join(lines, "\n"),
		"elements": []any{map[string]any{"element_index": 200.0, "role": "AXStaticText", "in_web_content": true}}}, macTarget))
	if !strings.Contains(result.Text, "Page with no named web-area wrapper") {
		t.Fatalf("got %q", result.Text)
	}
}

func TestNumericControlValuesAreNotMistakenForTheirIndexOrName(t *testing.T) {
	result := capturedText(t, AccessibilityFromSnapshot(map[string]any{"pid": 42.0, "window_id": 17.0, "window_title": "Page", "tree_markdown": "- AXWindow \"Page\"\n  - [8] AXSlider \"Volume 8\"",
		"elements": []any{map[string]any{"element_index": 8.0, "role": "AXSlider", "value": "8"}}}, macTarget))
	mustMatch(t, result.Text, `value="8"`)
}

func TestColdBrowserTreeGetsOneBoundedRetry(t *testing.T) {
	driver := &fakeDriver{read: func(call int) string {
		markdown := "- AXWindow \"Page\"\n  - AXButton"
		if call > 1 {
			markdown = "- AXWindow \"Page\"\n  - AXWebArea \"Page\"\n    - AXStaticText = \"Loaded page content\""
		}
		snapshot, _ := jsonx.Marshal(map[string]any{"pid": 42, "window_id": 17, "window_title": "Page", "elements": []any{}, "tree_markdown": markdown})
		return string(snapshot)
	}}
	var partial []domain.AccessibilitySnapshot
	result, err := SnapshotWindowAccessibility(t.Context(), driver, macTarget, func(snapshot domain.AccessibilitySnapshot) { partial = append(partial, snapshot) })
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.calls) != 2 || len(partial) != 1 {
		t.Fatalf("reads %d, partial %d", len(driver.calls), len(partial))
	}
	mustMatch(t, capturedText(t, result).Text, `Loaded page content`)
}
