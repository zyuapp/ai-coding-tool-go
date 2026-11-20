// Package computeruse is the app's side of computer use: what the platform grants the app, and the
// bundled Cua Driver a run reaches the desktop through. The driver daemon runs as this app's child, so
// macOS answers its permission checks with the app's own grants.
package computeruse

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// BundleID is the host the driver reports its grants for.
const BundleID = "com.zyuapp.aicodingtool"

const (
	startupTimeout  = 20 * time.Second
	shutdownTimeout = 5 * time.Second
	quitting        = "AI Coding Tool is quitting."
	missingDriver   = "The bundled CUA Driver executable is missing."
)

// DriverPath is where the bundled driver sits: beside a packaged app's resources, under vendor/ in a source checkout.
func DriverPath(packaged bool, resourcesPath, appPath string) string {
	if packaged {
		return filepath.Join(resourcesPath, "cua-driver")
	}
	return filepath.Join(appPath, "vendor", "cua-driver", "cua-driver")
}

// Host owns the permission checks and one private driver daemon that every run's MCP proxy talks to.
type Host struct {
	binary string
	env    platform.Env

	// Serializes starting the daemon, which can take seconds, apart from the fields mu guards.
	starting  sync.Mutex
	mu        sync.Mutex
	daemon    *daemon
	accepting bool
}

type daemon struct {
	cmd    *exec.Cmd
	socket string
	// Held open for the daemon's life: it shuts down when this pipe closes.
	liveness io.WriteCloser
	exited   chan struct{}
}

// NewHost takes the bundled driver's path, which DriverPath gives.
func NewHost(binary string) *Host {
	return &Host{binary: binary, env: os.Getenv, accepting: true}
}

func (h *Host) binaryExists() bool {
	info, err := os.Stat(h.binary)
	return err == nil && !info.IsDir()
}

// Permissions is what the machine grants computer use.
func (h *Host) Permissions(context.Context) (*domain.ComputerUsePermissions, error) {
	return h.permissions(), nil
}

// RequestPermission asks the platform for one permission and answers what it grants afterwards.
func (h *Host) RequestPermission(ctx context.Context, permission domain.ComputerUsePermission) (*domain.ComputerUsePermissions, error) {
	return h.request(permission), nil
}

func linuxPermissions(capability platform.DesktopCapability, binaryExists bool) *domain.ComputerUsePermissions {
	if capability.Status == "unsupported" {
		display := capability.Display
		if display == "" {
			display = platform.DisplayNone
		}
		return &domain.ComputerUsePermissions{LinuxRuntime: &domain.ComputerUsePermissionsLinuxRuntime{
			Status:  domain.ComputerUsePermissionsLinuxRuntimeStatusUnavailable,
			Display: domain.ComputerUsePermissionsLinuxRuntimeDisplay(display),
			Message: capability.Message,
		}}
	}
	display := capability.Display
	if display == platform.DisplayMacOS {
		display = platform.DisplayNone
	}
	if !binaryExists {
		return &domain.ComputerUsePermissions{LinuxRuntime: &domain.ComputerUsePermissionsLinuxRuntime{
			Status:  domain.ComputerUsePermissionsLinuxRuntimeStatusUnavailable,
			Display: domain.ComputerUsePermissionsLinuxRuntimeDisplay(display),
			Message: missingDriver,
		}}
	}
	status := domain.ComputerUsePermissionsLinuxRuntimeStatusAvailable
	if display == platform.DisplayXWayland || display == platform.DisplayWayland {
		status = domain.ComputerUsePermissionsLinuxRuntimeStatusLimited
	}
	message := "Native Wayland support is enabled, but its capabilities depend on the active compositor."
	switch display {
	case platform.DisplayX11:
		message = "Computer use is ready for this X11 session."
	case platform.DisplayXWayland:
		message = "X11 and XWayland apps are available. Native Wayland apps depend on the compositor and may not be reachable."
	}
	return &domain.ComputerUsePermissions{Accessibility: true, ScreenRecording: true, LinuxRuntime: &domain.ComputerUsePermissionsLinuxRuntime{
		Status:  status,
		Display: domain.ComputerUsePermissionsLinuxRuntimeDisplay(display),
		Message: message,
	}}
}

func unavailable(message string) domain.ComputerUseRunConfig {
	return domain.ComputerUseRunConfigUnavailable{Message: message}
}

// ForRun is what a run is given: the driver's MCP proxy once the daemon is up, setup when macOS has
// yet to grant both permissions, or why it cannot have computer use.
func (h *Host) ForRun(ctx context.Context) (domain.ComputerUseRunConfig, error) {
	if !h.isAccepting() {
		return unavailable(quitting), nil
	}
	capability := platform.ComputerUseCapability(h.env)
	if capability.Status == "unsupported" {
		return unavailable(capability.Message), nil
	}
	if needsGrants {
		permissions := h.permissions()
		if !h.isAccepting() {
			return unavailable(quitting), nil
		}
		if !permissions.Accessibility || !permissions.ScreenRecording {
			return domain.ComputerUseRunConfigSetupRequired{}, nil
		}
	}
	if !h.binaryExists() {
		return unavailable(missingDriver), nil
	}
	socket, err := h.ensureDaemon(ctx)
	if err != nil {
		if errors.Is(err, errQuitting) {
			return unavailable(quitting), nil
		}
		if platform.OS != "linux" {
			return nil, err
		}
		session := "this X11 session"
		switch capability.Display {
		case platform.DisplayWayland:
			session = "this Wayland compositor"
		case platform.DisplayXWayland:
			session = "this Wayland/XWayland session"
		}
		return unavailable("CUA Driver could not start in " + session + ": " + err.Error()), nil
	}
	return domain.ComputerUseRunConfigAvailable{MCP: domain.ComputerUseMCP{
		Command: h.binary,
		Args:    []string{"mcp", "--embedded", "--socket", socket, "--host-bundle-id", BundleID},
		Env:     map[string]string{},
	}}, nil
}

func (h *Host) isAccepting() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.accepting
}

var errQuitting = errors.New(quitting)

// daemonEnv is all the daemon inherits: who and where the user is, and the session's display.
var daemonEnv = []string{"PATH", "HOME", "LOGNAME", "SHELL", "TMPDIR", "USER", "LANG",
	"DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "XDG_SESSION_TYPE", "DBUS_SESSION_BUS_ADDRESS", "CUA_DRIVER_RS_ENABLE_WAYLAND"}

func (h *Host) ensureDaemon(ctx context.Context) (string, error) {
	h.starting.Lock()
	defer h.starting.Unlock()
	h.mu.Lock()
	if running := h.daemon; running != nil {
		select {
		case <-running.exited:
			h.daemon = nil
		default:
			h.mu.Unlock()
			return running.socket, nil
		}
	}
	accepting := h.accepting
	h.mu.Unlock()
	if !accepting {
		return "", errQuitting
	}
	generation := strings.ReplaceAll(application.NewID(), "-", "")[:8]
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("cua-%d-%s.sock", os.Getpid(), generation))
	_ = os.Remove(socket)
	cmd := platform.Command(context.Background(), h.binary, "serve", "--embedded", "--parent-liveness-stdio", "--no-permissions-gate",
		"--socket", socket, "--host-bundle-id", BundleID, "--permission-mode", "standard")
	inherited := map[string]string{}
	for _, entry := range cmd.Env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			inherited[key] = value
		}
	}
	cmd.Env = []string{"CUA_DRIVER_EMBEDDED_HOST_PID=" + strconv.Itoa(os.Getpid())}
	for _, key := range daemonEnv {
		if value, ok := inherited[key]; ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	stderr := &tailBuffer{}
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	liveness, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("CUA Driver could not start: %w", err)
	}
	started := &daemon{cmd: cmd, socket: socket, liveness: liveness, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(started.exited)
	}()
	deadline := time.NewTimer(startupTimeout)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		if connection, err := net.DialTimeout("unix", socket, time.Second); err == nil {
			_ = connection.Close()
			h.mu.Lock()
			if !h.accepting {
				h.mu.Unlock()
				_ = liveness.Close()
				_ = cmd.Process.Kill()
				_ = os.Remove(socket)
				return "", errQuitting
			}
			h.daemon = started
			h.mu.Unlock()
			return socket, nil
		}
		select {
		case <-started.exited:
			_ = os.Remove(socket)
			if line := stderr.lastLine(); line != "" {
				return "", errors.New(line)
			}
			return "", fmt.Errorf("CUA Driver exited with %s.", cmd.ProcessState)
		case <-deadline.C:
			_ = cmd.Process.Kill()
			_ = os.Remove(socket)
			return "", errors.New("CUA Driver did not start in time.")
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = os.Remove(socket)
			return "", ctx.Err()
		case <-poll.C:
		}
	}
}

// Stop refuses new runs and ends the daemon; awaited on quit.
func (h *Host) Stop(context.Context) error {
	h.mu.Lock()
	h.accepting = false
	running := h.daemon
	h.daemon = nil
	h.mu.Unlock()
	if running == nil {
		return nil
	}
	defer os.Remove(running.socket)
	_ = running.liveness.Close()
	select {
	case <-running.exited:
		return nil
	case <-time.After(shutdownTimeout):
	}
	platform.SignalProcess(running.cmd.Process.Pid, platform.Terminate)
	select {
	case <-running.exited:
	case <-time.After(shutdownTimeout):
		_ = running.cmd.Process.Kill()
		<-running.exited
	}
	return nil
}

// Resume lets the next run start a fresh daemon after a cancelled quit.
func (h *Host) Resume() {
	h.mu.Lock()
	h.accepting = true
	h.mu.Unlock()
}

// tailBuffer keeps the end of what the daemon wrote, for the reason it gives when it fails to start.
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

const tailLimit = 16 << 10

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > tailLimit {
		b.data = append([]byte(nil), b.data[len(b.data)-tailLimit:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) lastLine() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := bytes.Split(b.data, []byte("\n"))
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(string(lines[index])); line != "" {
			return line
		}
	}
	return ""
}
