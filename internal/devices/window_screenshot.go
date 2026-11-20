package devices

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// WindowFrame is a window's place on screen, in the screen points CoreGraphics counts in.
type WindowFrame struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type WindowShotStatus string

const (
	WindowShotCaptured    WindowShotStatus = "captured"
	WindowShotDenied      WindowShotStatus = "denied"
	WindowShotNoWindow    WindowShotStatus = "no-window"
	WindowShotUnsupported WindowShotStatus = "unsupported"
	WindowShotFailed      WindowShotStatus = "failed"
)

// WindowShot is what grabbing the frontmost window came to. Captured shots carry App, Title, PNG
// (base64), Frame and Context; no-window ones the App that had none; unsupported and failed ones a Message.
type WindowShot struct {
	Status  WindowShotStatus
	App     string
	Title   string
	PNG     string
	Frame   WindowFrame
	Context domain.ScreenshotContext
	Message string
}

// captureRunTimeout bounds each tool a capture runs.
const captureRunTimeout = 10 * time.Second

// WindowCapture grabs the window the user is in without taking the keyboard from it.
type WindowCapture struct {
	// context reads the captured window's accessibility text.
	context func(ctx context.Context, target ScreenshotTarget) domain.ScreenshotContext
	// run runs one tool and answers its stdout.
	run func(ctx context.Context, name string, args []string) ([]byte, error)
	// prepare readies the context reader for a capture on a platform.
	prepare func(platform domain.ScreenshotPlatform)
	pid     int
	env     platform.Env
}

// NewWindowCapture captures with what contexts reads of each window.
func NewWindowCapture(contexts *ScreenshotContexts) *WindowCapture {
	return &WindowCapture{
		context: contexts.Capture,
		prepare: contexts.Prepare,
		run: func(ctx context.Context, name string, args []string) ([]byte, error) {
			stdout, _, err := platform.Run(ctx, name, args, platform.RunOptions{Timeout: captureRunTimeout})
			return stdout, err
		},
		pid: os.Getpid(),
		env: os.Getenv,
	}
}

// contextLater starts reading the window's context beside the capture; the answer waits on the returned func.
func (c *WindowCapture) contextLater(ctx context.Context, target ScreenshotTarget) func() domain.ScreenshotContext {
	answer := make(chan domain.ScreenshotContext, 1)
	go func() { answer <- c.context(ctx, target) }()
	return func() domain.ScreenshotContext { return <-answer }
}

// Prepare readies what reads a captured window's context, so the next capture reads it within its
// deadline: starting the computer-use driver takes longer than the deadline allows.
func (c *WindowCapture) Prepare() {
	if c.prepare == nil || platform.WindowCaptureCapability(c.env).Status == "unsupported" {
		return
	}
	switch {
	case platform.OS == "darwin":
		c.prepare(domain.ScreenshotPlatformMacos)
	case platform.LinuxWindowCaptureBackend(c.env) == "hyprland":
		c.prepare(domain.ScreenshotPlatformLinuxHyprland)
	default:
		c.prepare(domain.ScreenshotPlatformLinuxX11)
	}
}

// CaptureFrontmostWindow grabs the window the user is in without taking the keyboard from it.
func (c *WindowCapture) CaptureFrontmostWindow(ctx context.Context, sound bool) WindowShot {
	capability := platform.WindowCaptureCapability(c.env)
	if capability.Status == "unsupported" {
		return WindowShot{Status: WindowShotUnsupported, Message: capability.Message}
	}
	if platform.OS == "darwin" {
		return c.captureFrontmostMacWindow(ctx, sound)
	}
	if platform.LinuxWindowCaptureBackend(c.env) == "hyprland" {
		return c.captureFrontmostHyprlandWindow(ctx)
	}
	return c.captureFrontmostX11Window(ctx)
}

func errorMessage(err error) string {
	return err.Error()
}

func notFound(err error) bool {
	var failure *platform.RunError
	return errors.As(err, &failure) && failure.NotFound
}

type x11Window struct {
	id            int64
	pid           int64
	app, title    string
	width, height int
}

var x11ActiveID = regexp.MustCompile(`(?i)(?:^|` + jsSpace + `)(0x[\da-f]+)(?:` + jsSpace + `|$)`)

// X11ActiveWindowID reads xprop's _NET_ACTIVE_WINDOW without taking the root window for an app. False for none.
func X11ActiveWindowID(output string) (int64, bool) {
	match := x11ActiveID.FindStringSubmatch(output)
	if match == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(match[1][2:], 16, 64)
	if err != nil || id == 0 || id > 1<<53-1 {
		return 0, false
	}
	return int64(id), true
}

var quotedValue = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)

func quotedValues(value string) []string {
	var values []string
	for _, match := range quotedValue.FindAllString(value, -1) {
		var text string
		if jsonx.Unmarshal([]byte(match), &text) == nil {
			values = append(values, text)
		}
	}
	return values
}

var x11PID = regexp.MustCompile(`=` + jsSpace + `*(\d+)`)

// X11WindowProperties reads xprop's answer for a window, preferring its visible title and application class.
func X11WindowProperties(id int64, output string) (pid int64, app string, title string) {
	lines := strings.Split(output, "\n")
	find := func(prefix string) string {
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				return line
			}
		}
		return ""
	}
	nameLine := find("_NET_WM_NAME")
	if nameLine == "" {
		nameLine = find("WM_NAME")
	}
	classLine := find("WM_CLASS")
	if pidLine := find("_NET_WM_PID"); pidLine != "" {
		if match := x11PID.FindStringSubmatch(pidLine); match != nil {
			if parsed, err := strconv.ParseInt(match[1], 10, 64); err == nil && parsed > 0 && parsed <= 1<<53-1 {
				pid = parsed
			}
		}
	}
	if nameLine != "" {
		if values := quotedValues(nameLine); len(values) > 0 {
			title = values[0]
		}
	}
	var classes []string
	if classLine != "" {
		classes = quotedValues(classLine)
	}
	switch {
	case len(classes) > 0 && classes[len(classes)-1] != "":
		app = classes[len(classes)-1]
	case len(classes) > 0 && classes[0] != "":
		app = classes[0]
	case title != "":
		app = title
	default:
		app = "Unknown app"
	}
	return pid, app, title
}

var (
	x11Width  = regexp.MustCompile(`(?m)^` + jsSpace + `*Width:` + jsSpace + `*(\d+)` + jsSpace + `*$`)
	x11Height = regexp.MustCompile(`(?m)^` + jsSpace + `*Height:` + jsSpace + `*(\d+)` + jsSpace + `*$`)
)

// X11WindowSize reads xwininfo's size, in the device pixels a capture of the window comes in.
func X11WindowSize(output string) (width, height int, ok bool) {
	widthMatch, heightMatch := x11Width.FindStringSubmatch(output), x11Height.FindStringSubmatch(output)
	if widthMatch == nil || heightMatch == nil {
		return 0, 0, false
	}
	width, widthErr := strconv.Atoi(widthMatch[1])
	height, heightErr := strconv.Atoi(heightMatch[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return width, height, true
}

func (c *WindowCapture) frontmostX11Window(ctx context.Context) (*x11Window, error) {
	active, err := c.run(ctx, "xprop", []string{"-root", "_NET_ACTIVE_WINDOW"})
	if err != nil {
		return nil, err
	}
	id, ok := X11ActiveWindowID(string(active))
	if !ok {
		return nil, nil
	}
	target := "0x" + strconv.FormatInt(id, 16)
	type answer struct {
		output []byte
		err    error
	}
	propertiesAnswer, geometryAnswer := make(chan answer, 1), make(chan answer, 1)
	go func() {
		output, err := c.run(ctx, "xprop", []string{"-id", target, "_NET_WM_PID", "_NET_WM_NAME", "WM_NAME", "WM_CLASS"})
		propertiesAnswer <- answer{output, err}
	}()
	go func() {
		output, err := c.run(ctx, "xwininfo", []string{"-id", target})
		geometryAnswer <- answer{output, err}
	}()
	properties, geometry := <-propertiesAnswer, <-geometryAnswer
	if properties.err != nil {
		return nil, properties.err
	}
	if geometry.err != nil {
		return nil, geometry.err
	}
	width, height, ok := X11WindowSize(string(geometry.output))
	if !ok {
		return nil, errors.New("xwininfo did not report a usable active-window size.")
	}
	pid, app, title := X11WindowProperties(id, string(properties.output))
	return &x11Window{id: id, pid: pid, app: app, title: title, width: width, height: height}, nil
}

// X11CaptureFailureMessage is what a failed X11 capture tells the person, with what to install when a tool is missing.
func X11CaptureFailureMessage(err error) string {
	if notFound(err) {
		return "X11 window capture needs xprop and xwininfo. Install those tools (the x11-utils package on Debian or Ubuntu), then try again."
	}
	return "Could not capture the active X11 window: " + errorMessage(err)
}

const emptyX11Image = "The desktop capture service returned an empty image for the active window."

func (c *WindowCapture) captureFrontmostX11Window(ctx context.Context) WindowShot {
	window, err := c.frontmostX11Window(ctx)
	if err != nil {
		return WindowShot{Status: WindowShotFailed, Message: X11CaptureFailureMessage(err)}
	}
	if window == nil {
		return WindowShot{Status: WindowShotNoWindow, App: "the desktop"}
	}
	if window.pid == int64(c.pid) {
		return WindowShot{Status: WindowShotNoWindow, App: "AI Coding Tool"}
	}
	context := c.contextLater(ctx, ScreenshotTarget{Platform: domain.ScreenshotPlatformLinuxX11, PID: window.pid, WindowID: window.id, App: window.app, Title: window.title})
	png, err := captureX11Pixels(window.id, window.width, window.height)
	if err != nil {
		return WindowShot{Status: WindowShotFailed, Message: X11CaptureFailureMessage(err)}
	}
	if len(png) == 0 {
		return WindowShot{Status: WindowShotFailed, Message: emptyX11Image}
	}
	// The capture flash stays on macOS, so Linux needs no compositor geometry.
	return WindowShot{Status: WindowShotCaptured, App: window.app, Title: window.title, PNG: base64.StdEncoding.EncodeToString(png), Context: context()}
}
