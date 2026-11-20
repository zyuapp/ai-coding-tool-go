//go:build unix

package client

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
