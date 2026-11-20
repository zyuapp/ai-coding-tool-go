package devices

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// ScreenshotContextTimeout bounds one accessibility read of a captured window.
const ScreenshotContextTimeout = 1_500 * time.Millisecond

// ScreenshotContexts reads what a captured window said through accessibility near the moment it was
// captured. The native walk runs in a computer-use driver process of its own, so a hung app or a
// native failure cannot hold the app up, and the deadline stops the work itself. At most one walk
// runs at a time. A driver is started and introduced ahead of the next capture, since starting one
// leaves the walk's own deadline no room.
type ScreenshotContexts struct {
	// driver is the cua-driver program, "" when there is none.
	driver func() string
	// trusted is whether macOS lets this app read other apps through accessibility, without asking.
	trusted func() bool
	now     func() time.Time
	active  atomic.Bool

	warmMu  sync.Mutex
	warm    *mcpDriver
	warming bool
}

// NewScreenshotContexts reads through the cua-driver program driver names ("" for none). It runs as
// `<driver> mcp`, the driver's MCP server over stdio.
func NewScreenshotContexts(driver func() string) *ScreenshotContexts {
	return &ScreenshotContexts{driver: driver, trusted: accessibilityTrusted, now: time.Now}
}

func jsSafeID(value int64) bool { return value > 0 && value <= 1<<53-1 }

// Capture never fails: what could not be read says why instead.
func (s *ScreenshotContexts) Capture(ctx context.Context, target ScreenshotTarget) domain.ScreenshotContext {
	context := domain.ScreenshotContext{
		Platform:   target.Platform,
		App:        utf16Prefix(target.App, domain.MaxScreenshotContextName),
		Title:      utf16Prefix(target.Title, domain.MaxScreenshotContextName),
		CapturedAt: float64(s.now().UnixMilli()),
	}
	switch {
	case !jsSafeID(target.PID) || !jsSafeID(target.WindowID):
		context.Accessibility = unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonWindow)
	case target.Platform == domain.ScreenshotPlatformMacos && !s.trusted():
		context.Accessibility = unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonPermission)
	case !s.active.CompareAndSwap(false, true):
		context.Accessibility = unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonBusy)
	default:
		context.Accessibility = s.read(ctx, target)
		s.active.Store(false)
	}
	return context
}

type driverKey struct {
	path     string
	hyprland bool
}

// mcpDriver is the driver's own MCP server over stdio.
type mcpDriver struct {
	key     driverKey
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	next    int
}

func (d *mcpDriver) stop() {
	if d.command.Process != nil {
		_ = d.command.Process.Kill()
	}
	go func() { _ = d.command.Wait() }()
}

func (d *mcpDriver) write(message any) error {
	line, err := jsonx.Marshal(message)
	if err != nil {
		return err
	}
	_, err = d.stdin.Write(append(line, '\n'))
	return err
}

func (d *mcpDriver) request(method string, params any) (jsontext.Value, error) {
	d.next++
	id := d.next
	if err := d.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		line, err := d.stdout.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			return nil, errors.New("The driver stopped.")
		}
		var message struct {
			ID     *float64       `json:"id"`
			Error  jsontext.Value `json:"error"`
			Result jsontext.Value `json:"result"`
		}
		if jsonx.Unmarshal(line, &message) != nil || message.ID == nil || *message.ID != float64(id) {
			if err != nil {
				return nil, errors.New("The driver stopped.")
			}
			continue
		}
		if len(message.Error) > 0 {
			return nil, errors.New(string(message.Error))
		}
		return message.Result, nil
	}
}

func (d *mcpDriver) CallTool(_ context.Context, name string, argumentsJSON string) (ToolAnswer, error) {
	result, err := d.request("tools/call", map[string]any{"name": name, "arguments": jsontext.Value(argumentsJSON)})
	if err != nil {
		return ToolAnswer{}, err
	}
	var answer struct {
		IsError           bool           `json:"isError"`
		StructuredContent jsontext.Value `json:"structuredContent"`
	}
	if len(result) > 0 && result.Kind() == '{' {
		if err := jsonx.Unmarshal(result, &answer); err != nil {
			return ToolAnswer{}, err
		}
	}
	return ToolAnswer{IsError: answer.IsError, StructuredJSON: string(answer.StructuredContent)}, nil
}

// startDriver runs and introduces a driver. The person's own compositor is read, even with
// computer-use tools off or XWayland present.
func startDriver(key driverKey) (*mcpDriver, error) {
	command := platform.Command(context.Background(), key.path, "mcp")
	environment := command.Env[:0:0]
	for _, entry := range command.Env {
		if !strings.HasPrefix(entry, "CUA_WAYLAND_NEST=") {
			environment = append(environment, entry)
		}
	}
	if key.hyprland {
		environment = append(environment, "CUA_DRIVER_RS_ENABLE_WAYLAND=1")
	}
	command.Env = environment
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, err
	}
	driver := &mcpDriver{key: key, command: command, stdin: stdin, stdout: bufio.NewReaderSize(stdout, 256*1024)}
	if _, err := driver.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "aicodingtool-screenshot-context", "version": "1"},
	}); err != nil {
		driver.stop()
		return nil, err
	}
	if err := driver.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		driver.stop()
		return nil, err
	}
	return driver, nil
}

// Prepare readies a driver for the next capture in the background, so it starts within its deadline.
func (s *ScreenshotContexts) Prepare(platform domain.ScreenshotPlatform) {
	path := s.driver()
	if path == "" {
		return
	}
	key := driverKey{path: path, hyprland: platform == domain.ScreenshotPlatformLinuxHyprland}
	s.warmMu.Lock()
	if (s.warm != nil && s.warm.key == key) || s.warming {
		s.warmMu.Unlock()
		return
	}
	s.warming = true
	s.warmMu.Unlock()
	go func() {
		driver, err := startDriver(key)
		s.warmMu.Lock()
		s.warming = false
		var stale *mcpDriver
		if err == nil {
			stale, s.warm = s.warm, driver
		}
		s.warmMu.Unlock()
		if stale != nil {
			stale.stop()
		}
	}()
}

// Close stops the driver kept ready for the next capture.
func (s *ScreenshotContexts) Close() {
	s.warmMu.Lock()
	warm := s.warm
	s.warm = nil
	s.warmMu.Unlock()
	if warm != nil {
		warm.stop()
	}
}

func (s *ScreenshotContexts) read(ctx context.Context, target ScreenshotTarget) domain.AccessibilitySnapshot {
	path := s.driver()
	if path == "" {
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed)
	}
	key := driverKey{path: path, hyprland: target.Platform == domain.ScreenshotPlatformLinuxHyprland}
	s.warmMu.Lock()
	warm := s.warm
	if warm != nil && warm.key == key {
		s.warm = nil
	} else {
		warm = nil
	}
	s.warmMu.Unlock()

	var mu sync.Mutex
	var owned *mcpDriver
	var partial domain.AccessibilitySnapshot
	done := make(chan domain.AccessibilitySnapshot, 1)
	reading, cancel := context.WithTimeout(ctx, ScreenshotContextTimeout)
	defer cancel()
	go func() {
		driver := warm
		if driver == nil {
			started, err := startDriver(key)
			if err != nil {
				done <- unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed)
				return
			}
			driver = started
		}
		mu.Lock()
		owned = driver
		stopped := reading.Err() != nil
		mu.Unlock()
		if stopped {
			driver.stop()
			return
		}
		snapshot, err := SnapshotWindowAccessibility(reading, driver, target, func(first domain.AccessibilitySnapshot) {
			if captured, ok := first.(domain.AccessibilitySnapshotCaptured); ok {
				mu.Lock()
				partial = domain.AccessibilitySnapshotCaptured{Text: captured.Text, Truncated: true}
				mu.Unlock()
			}
		})
		if err != nil {
			snapshot = unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonFailed)
		}
		done <- snapshot
	}()
	var outcome domain.AccessibilitySnapshot
	timedOut := false
	select {
	case outcome = <-done:
	case <-reading.Done():
		timedOut = true
	}
	mu.Lock()
	if owned != nil {
		owned.stop()
	}
	kept := partial
	mu.Unlock()
	s.Prepare(target.Platform)
	if timedOut {
		if kept != nil {
			return kept
		}
		return unavailableSnapshot(domain.AccessibilitySnapshotUnavailableReasonTimeout)
	}
	if _, ok := outcome.(domain.AccessibilitySnapshotCaptured); ok {
		return outcome
	}
	if kept != nil {
		return kept
	}
	return outcome
}
