package devices

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

func TestX11ActiveWindowIDsSkipTheRootWindow(t *testing.T) {
	if id, ok := X11ActiveWindowID("_NET_ACTIVE_WINDOW(WINDOW): window id # 0x4a00007\n"); !ok || id != 0x4a00007 {
		t.Fatalf("got %v %v", id, ok)
	}
	for _, output := range []string{"_NET_ACTIVE_WINDOW(WINDOW): window id # 0x0\n", "_NET_ACTIVE_WINDOW:  not found.\n"} {
		if _, ok := X11ActiveWindowID(output); ok {
			t.Fatalf("%q read as a window", output)
		}
	}
}

func TestX11PropertiesPreferTheVisibleTitleAndApplicationClass(t *testing.T) {
	pid, app, title := X11WindowProperties(17, `_NET_WM_PID(CARDINAL) = 4242
_NET_WM_NAME(UTF8_STRING) = "A project — AI Coding Tool"
WM_NAME(STRING) = "stale title"
WM_CLASS(STRING) = "ai-coding-tool", "AI Coding Tool"`)
	if pid != 4242 || app != "AI Coding Tool" || title != "A project — AI Coding Tool" {
		t.Fatalf("got %v %q %q", pid, app, title)
	}
}

func TestX11SizeIsTheWindowsDevicePixels(t *testing.T) {
	if width, height, ok := X11WindowSize("  Width: 1437\n  Height: 911\n"); !ok || width != 1437 || height != 911 {
		t.Fatalf("got %v %v %v", width, height, ok)
	}
	for _, output := range []string{"  Width: 0\n  Height: 911\n", "xwininfo: Window id: 0x1\n"} {
		if _, _, ok := X11WindowSize(output); ok {
			t.Fatalf("%q read as a size", output)
		}
	}
}

func TestMissingX11ToolsGiveAnActionableFailure(t *testing.T) {
	message := X11CaptureFailureMessage(&platform.RunError{NotFound: true})
	if !regexp.MustCompile(`(?i)xprop and xwininfo.*x11-utils.*try again`).MatchString(message) {
		t.Fatalf("got %q", message)
	}
}

var onePixelPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6F9sAAAAASUVORK5CYII=")

type hyprlandFixture struct {
	mu         sync.Mutex
	metadata   any
	captureErr error
	bytes      []byte
	calls      [][]string
	targets    []ScreenshotTarget
}

var activeHyprland = map[string]any{"stableId": "1800000a", "address": "0x55aa12345678", "pid": 424242.0, "class": "org.example.Editor", "title": "A window", "at": []any{-900.0, 32.0}, "size": []any{875.0, 600.0}, "mapped": true, "hidden": false}

var hyprlandContext = domain.ScreenshotContext{Platform: domain.ScreenshotPlatformLinuxHyprland, App: "org.example.Editor", Title: "A window", CapturedAt: 1, Accessibility: domain.AccessibilitySnapshotCaptured{Text: "Editor content"}}

func newHyprlandFixture(t *testing.T) (*hyprlandFixture, *WindowCapture) {
	fixture := &hyprlandFixture{metadata: activeHyprland, bytes: onePixelPNG}
	capture := &WindowCapture{
		pid: os.Getpid(),
		env: func(name string) string {
			return map[string]string{"DISPLAY": ":0", "WAYLAND_DISPLAY": "wayland-test", "HYPRLAND_INSTANCE_SIGNATURE": "hyprland-test"}[name]
		},
		context: func(_ context.Context, target ScreenshotTarget) domain.ScreenshotContext {
			fixture.mu.Lock()
			fixture.targets = append(fixture.targets, target)
			fixture.mu.Unlock()
			return hyprlandContext
		},
		run: func(_ context.Context, name string, args []string) ([]byte, error) {
			fixture.mu.Lock()
			fixture.calls = append(fixture.calls, append([]string{name}, args...))
			fixture.mu.Unlock()
			switch name {
			case "hyprctl":
				return jsonx.Marshal(fixture.metadata)
			case "grim":
				if fixture.captureErr != nil {
					return nil, fixture.captureErr
				}
				return nil, os.WriteFile(args[len(args)-1], fixture.bytes, 0o600)
			}
			t.Fatalf("unexpected command %s", name)
			return nil, nil
		},
	}
	return fixture, capture
}

func (f *hyprlandFixture) commands() []string {
	var names []string
	for _, call := range f.calls {
		names = append(names, call[0])
	}
	return names
}

func TestCapturesASpecificHyprlandToplevelAndRemovesItsImage(t *testing.T) {
	fixture, capture := newHyprlandFixture(t)
	shot := capture.captureFrontmostHyprlandWindow(t.Context())
	want := WindowShot{Status: WindowShotCaptured, App: "org.example.Editor", Title: "A window", PNG: base64.StdEncoding.EncodeToString(onePixelPNG), Frame: WindowFrame{X: -900, Y: 32, Width: 875, Height: 600}, Context: hyprlandContext}
	if !reflect.DeepEqual(shot, want) {
		t.Fatalf("got %+v", shot)
	}
	if !reflect.DeepEqual(fixture.targets, []ScreenshotTarget{{Platform: domain.ScreenshotPlatformLinuxHyprland, PID: 424242, WindowID: 0x55aa12345678, App: "org.example.Editor", Title: "A window"}}) {
		t.Fatalf("targets %+v", fixture.targets)
	}
	if !slices.Equal(fixture.calls[0], []string{"hyprctl", "-j", "activewindow"}) || !slices.Equal(fixture.calls[1][1:5], []string{"-T", "1800000a", "-t", "png"}) {
		t.Fatalf("calls %v", fixture.calls)
	}
	if _, err := os.Stat(filepath.Dir(fixture.calls[1][len(fixture.calls[1])-1])); !os.IsNotExist(err) {
		t.Fatal("the temporary image stays")
	}
}

func TestWaylandWithDisplayStillCapturesThroughHyprland(t *testing.T) {
	if platform.OS != "linux" {
		t.Skip("Linux routing")
	}
	fixture, capture := newHyprlandFixture(t)
	if shot := capture.CaptureFrontmostWindow(t.Context(), false); shot.Status != WindowShotCaptured {
		t.Fatalf("got %+v", shot)
	}
	if !slices.Equal(fixture.commands(), []string{"hyprctl", "grim"}) {
		t.Fatalf("calls %v", fixture.commands())
	}
}

func with(base map[string]any, key string, value any) map[string]any {
	next := map[string]any{}
	for name, item := range base {
		next[name] = item
	}
	if value == nil {
		delete(next, key)
	} else {
		next[key] = value
	}
	return next
}

func TestDesktopHiddenWindowsAndTheAppItselfNeverStartACapture(t *testing.T) {
	fixture, capture := newHyprlandFixture(t)
	for _, candidate := range []any{map[string]any{}, with(activeHyprland, "mapped", false), with(activeHyprland, "hidden", true), with(activeHyprland, "pid", float64(os.Getpid()))} {
		fixture.metadata = candidate
		if shot := capture.captureFrontmostHyprlandWindow(t.Context()); shot.Status != WindowShotNoWindow {
			t.Fatalf("%v: got %+v", candidate, shot)
		}
	}
	for _, name := range fixture.commands() {
		if name != "hyprctl" {
			t.Fatalf("calls %v", fixture.commands())
		}
	}
}

func TestMissingToplevelIDsAndBadGeometryFailWithoutAScreenCrop(t *testing.T) {
	fixture, capture := newHyprlandFixture(t)
	for _, candidate := range []any{with(activeHyprland, "stableId", nil), with(activeHyprland, "stableId", ""), with(activeHyprland, "size", []any{0.0, 600.0}), nil} {
		fixture.metadata = candidate
		if shot := capture.captureFrontmostHyprlandWindow(t.Context()); shot.Status != WindowShotFailed {
			t.Fatalf("%v: got %+v", candidate, shot)
		}
	}
	for _, name := range fixture.commands() {
		if name != "hyprctl" {
			t.Fatalf("calls %v", fixture.commands())
		}
	}
}

func TestWindowClosingDuringCaptureFailsAndCleansUp(t *testing.T) {
	fixture, capture := newHyprlandFixture(t)
	fixture.captureErr = errors.New("toplevel not found")
	shot := capture.captureFrontmostHyprlandWindow(t.Context())
	if shot.Status != WindowShotFailed || !regexp.MustCompile(`toplevel not found`).MatchString(shot.Message) {
		t.Fatalf("got %+v", shot)
	}
	if _, err := os.Stat(filepath.Dir(fixture.calls[1][len(fixture.calls[1])-1])); !os.IsNotExist(err) {
		t.Fatal("the temporary folder stays")
	}
}

func TestMissingGrimIsActionableAndInvalidImagesAreNeverAttached(t *testing.T) {
	fixture, capture := newHyprlandFixture(t)
	fixture.captureErr = &platform.RunError{NotFound: true}
	missing := capture.captureFrontmostHyprlandWindow(t.Context())
	if missing.Status != WindowShotFailed || !regexp.MustCompile(`hyprctl and grim.*[Ii]nstall or update`).MatchString(missing.Message) {
		t.Fatalf("got %+v", missing)
	}
	fixture.captureErr = nil
	for _, invalid := range [][]byte{{}, []byte("not a PNG")} {
		fixture.bytes = invalid
		if shot := capture.captureFrontmostHyprlandWindow(t.Context()); shot.Status != WindowShotFailed {
			t.Fatalf("%q: got %+v", invalid, shot)
		}
	}
}
