package platform

// OS is this platform as the domain's catalogs name it.
const OS = "linux"

// ManualUpdateRecovery is how to update by hand.
const ManualUpdateRecovery = "Download the new AppImage and replace the one you run."

// QuitsWithLastWindow is true: other desktops quit the app with its last window.
const QuitsWithLastWindow = true

// HidesWindowMenuBar is true: Linux keeps the window's menu accelerators but never shows its menu strip, Alt included.
const HidesWindowMenuBar = true

// InsetTitleBar is false: other desktops keep their native window controls.
const InsetTitleBar = false

const SimulatorSupported = false
const AsksMicrophoneAccess = false
const FlashesCapturedWindows = false

// AutomaticUpdatesAvailable reports whether the app can update itself: only when it runs as an AppImage.
func AutomaticUpdatesAvailable(env Env) bool { return env("APPIMAGE") != "" }

// RunningAppImage is the AppImage this process runs from.
func RunningAppImage(env Env) string { return env("APPIMAGE") }

// ComputerUseCapability is whether computer use works in this session.
func ComputerUseCapability(env Env) DesktopCapability { return linuxComputerUse(env) }

// WindowCaptureCapability is whether window capture works in this session.
func WindowCaptureCapability(env Env) DesktopCapability { return linuxWindowCapture(env) }
