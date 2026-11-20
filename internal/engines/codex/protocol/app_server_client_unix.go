//go:build unix

package protocol

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	terminateSignal = syscall.SIGTERM
	killSignal      = syscall.SIGKILL
)

// ownProcessGroup starts the server in a process group of its own, so closing reaches what it
// started under itself as well.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalGroup signals the process's group, or the process alone when that fails.
func signalGroup(process *os.Process, signal syscall.Signal) {
	if process == nil {
		return
	}
	if syscall.Kill(-process.Pid, signal) != nil {
		_ = process.Signal(signal)
	}
}

// exitSignal names the signal that ended the process, as Node names it ("SIGTERM").
func exitSignal(state *os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return unix.SignalName(status.Signal())
}
