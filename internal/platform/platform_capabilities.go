package platform

import "strings"

// LinuxDisplayServer is the display connection a Linux process inherited.
type LinuxDisplayServer string

const (
	DisplayX11      LinuxDisplayServer = "x11"
	DisplayXWayland LinuxDisplayServer = "xwayland"
	DisplayWayland  LinuxDisplayServer = "wayland"
	DisplayNone     LinuxDisplayServer = "none"
	// DisplayMacOS is the display a capability names on macOS.
	DisplayMacOS LinuxDisplayServer = "macos"
)

// DesktopCapability is whether a desktop feature works here, and why not when it does not.
type DesktopCapability struct {
	Status  string             `json:"status"`
	Display LinuxDisplayServer `json:"display,omitzero"`
	Message string             `json:"message,omitzero"`
}

func available(display LinuxDisplayServer) DesktopCapability {
	return DesktopCapability{Status: "available", Display: display}
}

func unsupported(display LinuxDisplayServer, message string) DesktopCapability {
	return DesktopCapability{Status: "unsupported", Display: display, Message: message}
}

// Env reads one variable; os.Getenv in the app, a map in tests.
type Env func(key string) string

// LinuxDisplay classifies the display connections inherited by this process. XDG_SESSION_TYPE alone
// is a login type, not a usable display connection.
func LinuxDisplay(env Env) LinuxDisplayServer {
	wayland, x11 := env("WAYLAND_DISPLAY") != "", env("DISPLAY") != ""
	switch {
	case wayland && x11:
		return DisplayXWayland
	case wayland:
		return DisplayWayland
	case x11:
		return DisplayX11
	}
	return DisplayNone
}

func linuxComputerUse(env Env) DesktopCapability {
	display := LinuxDisplay(env)
	if display == DisplayNone {
		return unsupported(display, "Computer use needs a graphical Linux session, but neither DISPLAY nor WAYLAND_DISPLAY is available.")
	}
	if display == DisplayWayland && env("CUA_DRIVER_RS_ENABLE_WAYLAND") != "1" {
		return unsupported(display, "Native Wayland computer use is compositor-dependent and opt-in. Start AI Coding Tool with CUA_DRIVER_RS_ENABLE_WAYLAND=1, or use an X11/XWayland session.")
	}
	return available(display)
}

// LinuxWindowCaptureBackend selects by the compositor connection, so XWayland never sends native
// windows to X11 capture. Empty when neither backend applies.
func LinuxWindowCaptureBackend(env Env) string {
	if env("WAYLAND_DISPLAY") != "" && strings.TrimSpace(env("HYPRLAND_INSTANCE_SIGNATURE")) != "" {
		return "hyprland"
	}
	display := LinuxDisplay(env)
	waylandSession := strings.ToLower(strings.TrimSpace(env("XDG_SESSION_TYPE"))) == "wayland" || display == DisplayXWayland || display == DisplayWayland
	if display == DisplayX11 && !waylandSession {
		return "x11"
	}
	return ""
}

func linuxWindowCapture(env Env) DesktopCapability {
	display := LinuxDisplay(env)
	if LinuxWindowCaptureBackend(env) != "" {
		return available(display)
	}
	if display != DisplayNone {
		return unsupported(display, "Window capture is not available on this Wayland desktop yet. Supported Linux sessions are Hyprland and X11.")
	}
	return unsupported(display, "Window capture needs a graphical Linux session, but neither DISPLAY nor WAYLAND_DISPLAY is available.")
}
