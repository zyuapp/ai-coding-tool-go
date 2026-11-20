package platform

// OS is this platform as the domain's catalogs name it.
const OS = "darwin"

// ManualUpdateRecovery is how to update by hand.
const ManualUpdateRecovery = "Download the new version and replace the app in Applications."

// QuitsWithLastWindow is false: macOS keeps the app running with no window.
const QuitsWithLastWindow = false

// HidesWindowMenuBar is false: macOS keeps its menu in the menu bar.
const HidesWindowMenuBar = false

// InsetTitleBar is true: macOS keeps the inset traffic lights.
const InsetTitleBar = true

// SimulatorSupported is true: iOS simulators run only on macOS.
const SimulatorSupported = true

// AsksMicrophoneAccess is true: macOS asks the user before an app may use the microphone.
const AsksMicrophoneAccess = true

// FlashesCapturedWindows is true: macOS brings the app forward for any window it shows.
const FlashesCapturedWindows = true

// AutomaticUpdatesAvailable reports whether the app can update itself.
func AutomaticUpdatesAvailable(Env) bool { return true }

// RunningAppImage is the AppImage this process runs from; never on macOS.
func RunningAppImage(Env) string { return "" }

// ComputerUseCapability is whether computer use works here.
func ComputerUseCapability(Env) DesktopCapability { return available(DisplayMacOS) }

// WindowCaptureCapability is whether window capture works here.
func WindowCaptureCapability(Env) DesktopCapability { return available(DisplayMacOS) }
