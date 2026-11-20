package devices

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

static int actScreenCaptureAllowed(void) { return CGPreflightScreenCaptureAccess() ? 1 : 0; }
*/
import "C"

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// shutter is macOS's own screenshot sound, which is what the gesture already means to everyone.
const shutter = "/System/Library/Components/CoreAudio.component/Contents/SharedSupport/SystemSounds/system/Grab.aif"

// PlayShutter plays the shutter beside the capture rather than after it. `screencapture` is told not
// to make a sound of its own, so this stays one choice rather than two that can disagree.
func PlayShutter() {
	if _, err := os.Stat(shutter); err != nil {
		return
	}
	platform.LaunchDetached("/usr/bin/afplay", shutter)
}

// pickWindow finds the frontmost app's frontmost ordinary window, as CoreGraphics reports it. Reading
// the window list needs no permission of its own, and asking for it never activates this app, so
// the answer still describes whatever the user was looking at when the hotkey landed.
const pickWindow = `
ObjC.import("AppKit");
ObjC.bindFunction("CGWindowListCopyWindowInfo", ["id", ["unsigned int", "unsigned int"]]);
var ON_SCREEN = 1, NO_DESKTOP = 16;
var front = $.NSWorkspace.sharedWorkspace.frontmostApplication;
var pid = front.processIdentifier;
var app = ObjC.unwrap(front.localizedName);
var windows = ObjC.deepUnwrap($.CGWindowListCopyWindowInfo(ON_SCREEN | NO_DESKTOP, 0));
var found = windows.find(function (window) {
  return window.kCGWindowOwnerPID === pid
    && window.kCGWindowLayer === 0
    && window.kCGWindowBounds.Width > 60
    && window.kCGWindowBounds.Height > 60;
});
JSON.stringify(found
  ? { app: app, pid: pid, windowId: found.kCGWindowNumber, title: found.kCGWindowName || "", bounds: found.kCGWindowBounds }
  : { app: app, pid: pid, windowId: null });
`

type macWindowPick struct {
	App      string    `json:"app"`
	PID      int64     `json:"pid"`
	WindowID *int64    `json:"windowId"`
	Title    *string   `json:"title"`
	Bounds   *cgBounds `json:"bounds"`
}

type cgBounds struct {
	X      float64 `json:"X"`
	Y      float64 `json:"Y"`
	Width  float64 `json:"Width"`
	Height float64 `json:"Height"`
}

func (c *WindowCapture) frontmostMacWindow(ctx context.Context) (*macWindowPick, error) {
	output, err := c.run(ctx, "/usr/bin/osascript", []string{"-l", "JavaScript", "-e", pickWindow})
	if err != nil {
		return nil, err
	}
	var pick macWindowPick
	if err := jsonx.Unmarshal(output, &pick); err != nil {
		return nil, err
	}
	return &pick, nil
}

func (c *WindowCapture) captureFrontmostMacWindow(ctx context.Context, sound bool) WindowShot {
	if C.actScreenCaptureAllowed() == 0 {
		return WindowShot{Status: WindowShotDenied}
	}
	window, err := c.frontmostMacWindow(ctx)
	if err != nil {
		return WindowShot{Status: WindowShotFailed, Message: errorMessage(err)}
	}
	if window.PID == int64(c.pid) {
		return WindowShot{Status: WindowShotNoWindow, App: "AI Coding Tool"}
	}
	if window.WindowID == nil {
		return WindowShot{Status: WindowShotNoWindow, App: window.App}
	}
	title := ""
	if window.Title != nil {
		title = *window.Title
	}
	context := c.contextLater(ctx, ScreenshotTarget{Platform: domain.ScreenshotPlatformMacos, PID: window.PID, WindowID: *window.WindowID, App: window.App, Title: title})
	directory, err := os.MkdirTemp("", "aic-shot-")
	if err != nil {
		return WindowShot{Status: WindowShotFailed, Message: errorMessage(err)}
	}
	defer os.RemoveAll(directory)
	file := filepath.Join(directory, "window.png")
	if _, err := c.run(ctx, "/usr/sbin/screencapture", []string{"-l" + strconv.FormatInt(*window.WindowID, 10), "-x", "-o", file}); err != nil {
		return WindowShot{Status: WindowShotFailed, Message: errorMessage(err)}
	}
	if sound {
		PlayShutter()
	}
	png, err := os.ReadFile(file)
	if err != nil {
		return WindowShot{Status: WindowShotFailed, Message: errorMessage(err)}
	}
	if len(png) == 0 {
		return WindowShot{Status: WindowShotDenied}
	}
	bounds := cgBounds{}
	if window.Bounds != nil {
		bounds = *window.Bounds
	}
	return WindowShot{
		Status:  WindowShotCaptured,
		App:     window.App,
		Title:   title,
		PNG:     base64.StdEncoding.EncodeToString(png),
		Frame:   WindowFrame{X: bounds.X, Y: bounds.Y, Width: bounds.Width, Height: bounds.Height},
		Context: context(),
	}
}

func captureX11Pixels(int64, int, int) ([]byte, error) {
	return nil, errUnsupportedHere
}
