//go:build darwin || linux

package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Signal is what a child is asked to do.
type Signal int

const (
	Interrupt Signal = iota
	Terminate
	Kill
)

func (s Signal) number() syscall.Signal {
	switch s {
	case Interrupt:
		return syscall.SIGINT
	case Kill:
		return syscall.SIGKILL
	default:
		return syscall.SIGTERM
	}
}

// SignalProcess signals one process. False when it is gone.
func SignalProcess(pid int, signal Signal) bool {
	return pid > 0 && syscall.Kill(pid, signal.number()) == nil
}

// SignalGroup signals the process group `pid` leads. False when there is no such group.
func SignalGroup(pid int, signal Signal) bool {
	return pid > 0 && syscall.Kill(-pid, signal.number()) == nil
}

// SignalGroupOrProcess signals the group `pid` leads, or the process alone when it leads none.
func SignalGroupOrProcess(pid int, signal Signal) {
	if !SignalGroup(pid, signal) {
		SignalProcess(pid, signal)
	}
}

// Alive reports whether a process with this id exists, under any user.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// OwnGroup starts the child as the leader of a process group of its own, so signalling the group
// reaches everything it starts. Cancelling the command's context kills the whole group.
func OwnGroup(command *exec.Cmd) *exec.Cmd {
	attributes(command).Setpgid = true
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		if !SignalGroup(command.Process.Pid, Kill) {
			return command.Process.Kill()
		}
		return nil
	}
	return command
}

// OwnSession starts the child in a session of its own, without the launching terminal. It leads the
// session's process group, so cancelling the command's context kills the whole group.
func OwnSession(command *exec.Cmd) *exec.Cmd {
	ownSession(command)
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		if !SignalGroup(command.Process.Pid, Kill) {
			return command.Process.Kill()
		}
		return nil
	}
	return command
}

func ownSession(command *exec.Cmd) {
	attributes(command).Setsid = true
}

func attributes(command *exec.Cmd) *syscall.SysProcAttr {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	return command.SysProcAttr
}

// ExitSignal is the signal that ended a process, or 0 when it exited on its own.
func ExitSignal(state *os.ProcessState) syscall.Signal {
	if state == nil {
		return 0
	}
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return status.Signal()
	}
	return 0
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGILL: "SIGILL",
	syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT", syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE",
	syscall.SIGKILL: "SIGKILL", syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM", syscall.SIGXCPU: "SIGXCPU",
	syscall.SIGXFSZ: "SIGXFSZ",
}

// SignalName is a signal's name as Node reports it (`SIGTERM`), or empty for one it does not name here.
func SignalName(signal syscall.Signal) string {
	return signalNames[signal]
}

// executable reports whether a path is a regular file anyone may run.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// LaunchDetached starts a launcher detached from this process, the way `spawn` with `detached` does,
// and reports whether it was there at all. It is reaped in the background so it never lingers.
func LaunchDetached(name string, args ...string) bool {
	command := Command(context.Background(), name, args...)
	ownSession(command)
	if command.Start() != nil {
		return false
	}
	go func() { _ = command.Wait() }()
	return true
}
