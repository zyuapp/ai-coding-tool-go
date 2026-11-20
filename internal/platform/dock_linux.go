package platform

// InstallDockMenu does nothing on Linux, whose docks have no common menu an app can give its icon.
func InstallDockMenu(func()) {}

// SetUnreadBadge does nothing on Linux, where Electron badges only Unity's launcher.
func SetUnreadBadge(int) {}
