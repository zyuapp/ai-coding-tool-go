package devices

// accessibilityTrusted is always true: Linux grants no such permission.
func accessibilityTrusted() bool { return true }
