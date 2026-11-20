package platform

import "syscall"

// loginShellOwnSession is true on Linux: an interactive shell may open /dev/tty while discovering its
// job-control state, and a terminal-launched app would receive SIGTTIN with it.
const loginShellOwnSession = true

// stopLoginShell ends the shell's whole session, which it leads.
func stopLoginShell(pid int) {
	if syscall.Kill(-pid, syscall.SIGKILL) != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
