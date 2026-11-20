package platform

import "syscall"

// loginShellOwnSession is false on macOS: an app started from the Dock has no terminal for the shell to take.
const loginShellOwnSession = false

// stopLoginShell ends a shell that ran past its time the way `execFile` does.
func stopLoginShell(pid int) {
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
