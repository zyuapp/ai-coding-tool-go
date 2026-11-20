package devices

import (
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var contextTarget = ScreenshotTarget{Platform: domain.ScreenshotPlatformMacos, PID: 42, WindowID: 24, App: "Editor", Title: "Draft"}

func windowState(target ScreenshotTarget, markdown string) string {
	state, _ := jsonx.Marshal(map[string]any{"pid": target.PID, "window_id": target.WindowID, "window_title": target.Title, "tree_markdown": markdown, "elements": []any{}})
	return string(state)
}

// fakeContexts reads through the test binary standing in for the driver, serving snapshots in turn.
func fakeContexts(t *testing.T, snapshots []string, delay time.Duration) (*ScreenshotContexts, *atomic.Int32) {
	t.Helper()
	t.Setenv(testRole, "mcp-driver")
	encoded, _ := jsonx.Marshal(snapshots)
	t.Setenv("FAKE_SNAPSHOTS", string(encoded))
	t.Setenv("FAKE_DELAY", delay.String())
	asked := &atomic.Int32{}
	contexts := NewScreenshotContexts(func() string {
		asked.Add(1)
		return os.Args[0]
	})
	contexts.trusted = func() bool { return true }
	t.Cleanup(func() {
		time.Sleep(50 * time.Millisecond)
		contexts.Close()
	})
	return contexts, asked
}

func TestMissingMacAccessibilityKeepsBasicContextWithoutSpawning(t *testing.T) {
	contexts, asked := fakeContexts(t, nil, 0)
	contexts.trusted = func() bool { return false }
	context := contexts.Capture(t.Context(), contextTarget)
	if context.App != "Editor" || context.Accessibility != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonPermission) || asked.Load() != 0 {
		t.Fatalf("got %+v after %d", context, asked.Load())
	}
}

func TestContextIsCapturedInADisposableProcess(t *testing.T) {
	contexts, _ := fakeContexts(t, []string{windowState(contextTarget, "- AXWindow \"Draft\"\n  - AXStaticText = \"Document ready\"")}, 0)
	context := contexts.Capture(t.Context(), contextTarget)
	mustMatch(t, capturedText(t, context.Accessibility).Text, `Document ready`)
	if context.Platform != domain.ScreenshotPlatformMacos || context.Title != "Draft" || context.CapturedAt <= 0 {
		t.Fatalf("got %+v", context)
	}
}

func TestHungWalkStopsAtTheDeadlineAndCapturesDoNotPileUp(t *testing.T) {
	contexts, asked := fakeContexts(t, []string{windowState(contextTarget, "- AXWindow \"Draft\"\n  - AXStaticText = \"late\"")}, 10*time.Second)
	done := make(chan domain.ScreenshotContext, 1)
	go func() { done <- contexts.Capture(t.Context(), contextTarget) }()
	time.Sleep(100 * time.Millisecond)
	if busy := contexts.Capture(t.Context(), contextTarget); busy.Accessibility != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonBusy) {
		t.Fatalf("second capture %+v", busy.Accessibility)
	}
	if asked.Load() != 1 {
		t.Fatalf("a busy capture started a driver too (%d)", asked.Load())
	}
	started := time.Now()
	first := <-done
	if first.Accessibility != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonTimeout) {
		t.Fatalf("got %+v", first.Accessibility)
	}
	if waited := time.Since(started); waited > ScreenshotContextTimeout {
		t.Fatalf("waited %v", waited)
	}
}

func TestRetryThatStallsKeepsTheFirstUsableContext(t *testing.T) {
	titleBar := windowState(contextTarget, "- AXWindow \"Draft\"\n  - AXButton \"Window controls\"")
	contexts, _ := fakeContexts(t, []string{titleBar}, 0)
	t.Setenv("FAKE_DELAY", "700ms")
	context := contexts.Capture(t.Context(), contextTarget)
	captured := capturedText(t, context.Accessibility)
	if !captured.Truncated {
		t.Fatalf("got %+v", captured)
	}
	mustMatch(t, captured.Text, `Window controls`)
}

func TestFinalSnapshotReplacesAProvisionalTitleBarRead(t *testing.T) {
	contexts, _ := fakeContexts(t, []string{
		windowState(contextTarget, "- AXWindow \"Draft\"\n  - AXButton \"Window controls\""),
		windowState(contextTarget, "- AXWindow \"Draft\"\n  - AXWebArea \"Page\"\n    - AXStaticText = \"Page content\""),
	}, 0)
	captured := capturedText(t, contexts.Capture(t.Context(), contextTarget).Accessibility)
	if captured.Truncated {
		t.Fatalf("got %+v", captured)
	}
	mustMatch(t, captured.Text, `Page content`)
}

func TestHyprlandReadsEnableOnlyTheirOwnDriversWayland(t *testing.T) {
	linux := ScreenshotTarget{Platform: domain.ScreenshotPlatformLinuxHyprland, PID: 42, WindowID: 0x55aa12345678, App: "Editor", Title: "Draft"}
	contexts, _ := fakeContexts(t, []string{windowState(linux, "- frame = \"Draft\"\n  - label = \"Linux text\"")}, 0)
	trustAsked := false
	contexts.trusted = func() bool {
		trustAsked = true
		return false
	}
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("FAKE_ENV_FILE", envFile)
	t.Setenv("CUA_WAYLAND_NEST", "1")
	context := contexts.Capture(t.Context(), linux)
	mustMatch(t, capturedText(t, context.Accessibility).Text, `Linux text`)
	if context.Platform != domain.ScreenshotPlatformLinuxHyprland || trustAsked {
		t.Fatalf("got %+v, asked for trust %v", context, trustAsked)
	}
	written, _ := os.ReadFile(envFile)
	if string(written) != "enable=1 nest=/false" {
		t.Fatalf("driver environment %q", written)
	}
}

func TestStartupExitAndMalformedRepliesDegradeWithoutFailing(t *testing.T) {
	missing := NewScreenshotContexts(func() string { return filepath.Join(t.TempDir(), "no-driver") })
	missing.trusted = func() bool { return true }
	if got := missing.Capture(t.Context(), contextTarget).Accessibility; got != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed) {
		t.Fatalf("missing driver: %+v", got)
	}
	none := NewScreenshotContexts(func() string { return "" })
	none.trusted = func() bool { return true }
	if got := none.Capture(t.Context(), contextTarget).Accessibility; got != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed) {
		t.Fatalf("no driver: %+v", got)
	}

	exiting, _ := fakeContexts(t, nil, 0)
	t.Setenv("FAKE_EXIT", "1")
	if _, ok := exiting.Capture(t.Context(), contextTarget).Accessibility.(domain.AccessibilitySnapshotUnavailable); !ok {
		t.Fatal("an exited driver gave context")
	}
	t.Setenv("FAKE_EXIT", "")

	malformed, _ := fakeContexts(t, []string{`{"status":"captured","text":17}`}, 0)
	if _, ok := malformed.Capture(t.Context(), contextTarget).Accessibility.(domain.AccessibilitySnapshotUnavailable); !ok {
		t.Fatal("a malformed reply gave context")
	}
	refused, _ := fakeContexts(t, []string{"!"}, 0)
	if got := refused.Capture(t.Context(), contextTarget).Accessibility; !reflect.DeepEqual(got, unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed)) {
		t.Fatalf("an error reply: %+v", got)
	}
	if got := refused.Capture(t.Context(), ScreenshotTarget{Platform: domain.ScreenshotPlatformMacos, PID: 0, WindowID: 24}).Accessibility; got != unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow) {
		t.Fatalf("no pid: %+v", got)
	}
}
