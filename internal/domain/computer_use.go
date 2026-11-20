package domain

// SameComputerUsePermissions reports whether two readings of the platform say the same thing.
func SameComputerUsePermissions(left, right ComputerUsePermissions) bool {
	if left.Accessibility != right.Accessibility || left.ScreenRecording != right.ScreenRecording {
		return false
	}
	if left.LinuxRuntime == nil || right.LinuxRuntime == nil {
		return left.LinuxRuntime == right.LinuxRuntime
	}
	return *left.LinuxRuntime == *right.LinuxRuntime
}
