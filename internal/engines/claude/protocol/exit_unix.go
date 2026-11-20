//go:build unix

package protocol

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// exitSignal names the signal that ended the process, as Node names it ("SIGTERM").
func exitSignal(state *os.ProcessState) string {
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return ""
	}
	return unix.SignalName(status.Signal())
}

func terminate(process *os.Process) {
	_ = process.Signal(syscall.SIGTERM)
}
