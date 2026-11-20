package devices

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

var errUnsupportedHere = errors.New("Window capture is not available on this desktop.")

var stableIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var pngSignature = []byte{137, 80, 78, 71, 13, 10, 26, 10}

type hyprlandWindow struct {
	stableID string
	address  any
	pid      int64
	class    string
	title    string
	at, size [2]float64
}

func finiteNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

func pair(value any, positive bool) ([2]float64, bool) {
	items, ok := value.([]any)
	if !ok || len(items) != 2 {
		return [2]float64{}, false
	}
	var out [2]float64
	for index, item := range items {
		number, ok := finiteNumber(item)
		if !ok || positive && number <= 0 {
			return [2]float64{}, false
		}
		out[index] = number
	}
	return out, true
}

// parseHyprlandWindow checks hyprctl's active window has the stable id, owner, and geometry a capture needs.
func parseHyprlandWindow(active map[string]any) (*hyprlandWindow, bool) {
	stableID, ok := active["stableId"].(string)
	if !ok || stableID == "" || utf16Length(stableID) > 128 || !stableIDPattern.MatchString(stableID) {
		return nil, false
	}
	address, hasAddress := active["address"]
	if _, isString := address.(string); hasAddress && !isString {
		return nil, false
	}
	pid, ok := finiteNumber(active["pid"])
	if !ok || pid <= 0 || pid != math.Trunc(pid) || pid > 1<<53-1 {
		return nil, false
	}
	class, classOK := active["class"].(string)
	title, titleOK := active["title"].(string)
	at, atOK := pair(active["at"], false)
	size, sizeOK := pair(active["size"], true)
	if !classOK || !titleOK || !atOK || !sizeOK {
		return nil, false
	}
	return &hyprlandWindow{stableID: stableID, address: address, pid: int64(pid), class: class, title: title, at: at, size: size}, true
}

// captureFrontmostHyprlandWindow captures the selected toplevel, never a screen crop that could include an overlapping window.
func (c *WindowCapture) captureFrontmostHyprlandWindow(ctx context.Context) WindowShot {
	shot, err := c.captureHyprland(ctx)
	if err == nil {
		return shot
	}
	if notFound(err) {
		return WindowShot{Status: WindowShotFailed, Message: "Hyprland window capture needs hyprctl and grim. Install or update those tools, then try again."}
	}
	return WindowShot{Status: WindowShotFailed, Message: "Could not capture the active Hyprland window: " + errorMessage(err)}
}

func (c *WindowCapture) captureHyprland(ctx context.Context) (WindowShot, error) {
	output, err := c.run(ctx, "hyprctl", []string{"-j", "activewindow"})
	if err != nil {
		return WindowShot{}, err
	}
	var parsed any
	if err := jsonx.Unmarshal(output, &parsed); err != nil {
		return WindowShot{}, err
	}
	active, ok := parsed.(map[string]any)
	if !ok || active == nil {
		return WindowShot{}, errors.New("Hyprland returned invalid window information.")
	}
	if len(active) == 0 || active["mapped"] == false || active["hidden"] == true {
		return WindowShot{Status: WindowShotNoWindow, App: "the desktop"}, nil
	}
	if pid, ok := active["pid"].(float64); ok && pid == float64(c.pid) {
		return WindowShot{Status: WindowShotNoWindow, App: "AI Coding Tool"}, nil
	}
	window, ok := parseHyprlandWindow(active)
	if !ok {
		return WindowShot{Status: WindowShotFailed, Message: "Hyprland did not provide a usable window ID or size. Update Hyprland and grim, then try again."}, nil
	}
	app := window.class
	if app == "" {
		app = "Unknown app"
	}
	windowID, _ := HyprlandAccessibilityWindowID(window.address)
	context := c.contextLater(ctx, ScreenshotTarget{Platform: domain.ScreenshotPlatformLinuxHyprland, PID: window.pid, WindowID: windowID, App: app, Title: window.title})
	directory, err := os.MkdirTemp("", "aic-shot-")
	if err != nil {
		return WindowShot{}, err
	}
	defer os.RemoveAll(directory)
	file := filepath.Join(directory, "window.png")
	if _, err := c.run(ctx, "grim", []string{"-T", window.stableID, "-t", "png", file}); err != nil {
		return WindowShot{}, err
	}
	png, err := os.ReadFile(file)
	if err != nil {
		return WindowShot{}, err
	}
	if len(png) < 24 || !bytes.Equal(png[:8], pngSignature) {
		return WindowShot{Status: WindowShotFailed, Message: "Hyprland returned an empty or invalid window image. Try capturing the window again."}, nil
	}
	return WindowShot{
		Status:  WindowShotCaptured,
		App:     app,
		Title:   window.title,
		PNG:     base64.StdEncoding.EncodeToString(png),
		Frame:   WindowFrame{X: window.at[0], Y: window.at[1], Width: window.size[0], Height: window.size[1]},
		Context: context(),
	}, nil
}
