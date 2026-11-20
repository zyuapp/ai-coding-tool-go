package platform

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// DefaultMaxBuffer is `execFile`'s default output ceiling per stream.
const DefaultMaxBuffer = 1024 * 1024

// RunOptions shape one Run the way `execFile`'s options do.
type RunOptions struct {
	Dir string
	// Input is written to the child's standard input, which is closed after it. Nil gives the child
	// no input at all.
	Input []byte
	// Timeout of zero waits as long as the context does. Past it the child gets SIGTERM.
	Timeout time.Duration
	// MaxBuffer bounds each of stdout and stderr; zero means DefaultMaxBuffer. Past it the child gets SIGTERM.
	MaxBuffer int
	// Env is appended to the child environment, each entry replacing one with the same key.
	Env []string
}

// RunError is how a Run that did not succeed fails, keeping whatever the child printed. Its message
// is Node's: `Command failed: <command>\n<stderr>`, `spawn <file> ENOENT`, or
// `stdout maxBuffer length exceeded`.
type RunError struct {
	// Code is the exit code, or -1 when the child did not exit on its own.
	Code int
	// Signal is what ended the child, when one did.
	Signal syscall.Signal
	// Killed is true when Run stopped the child for its timeout or buffer.
	Killed    bool
	NotFound  bool
	MaxBuffer bool
	Stdout    []byte
	Stderr    []byte
	message   string
	cause     error
}

func (e *RunError) Error() string { return e.message }
func (e *RunError) Unwrap() error { return e.cause }

// ErrorCode is Node's `error.code`: the exit code as a number, or `ENOENT` / `ERR_CHILD_PROCESS_STDIO_MAXBUFFER`.
func (e *RunError) ErrorCode() any {
	switch {
	case e.NotFound:
		return "ENOENT"
	case e.MaxBuffer:
		return "ERR_CHILD_PROCESS_STDIO_MAXBUFFER"
	case e.Code >= 0:
		return e.Code
	}
	return nil
}

// Run runs one program as Node's `execFile` does: no shell, an optional timeout, a bounded output
// buffer, and an error that keeps whatever the process printed before it failed.
func Run(ctx context.Context, name string, args []string, options RunOptions) (stdout, stderr []byte, err error) {
	limit := options.MaxBuffer
	if limit <= 0 {
		limit = DefaultMaxBuffer
	}
	runContext, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if options.Timeout > 0 {
		var stop context.CancelFunc
		runContext, stop = context.WithTimeoutCause(runContext, options.Timeout, errTimedOut)
		defer stop()
	}
	command := Command(runContext, name, args...)
	command.Dir = options.Dir
	command.Env = overlayEnv(command.Env, options.Env)
	if options.Input != nil {
		command.Stdin = bytes.NewReader(options.Input)
	}
	out, errOut := newBoundedBuffer(limit), newBoundedBuffer(limit)
	command.Stdout, command.Stderr = out, errOut
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 2 * time.Second

	commandLine := strings.Join(append([]string{name}, args...), " ")
	if startErr := command.Start(); startErr != nil {
		if errors.Is(startErr, exec.ErrNotFound) || errors.Is(startErr, syscall.ENOENT) {
			return nil, nil, &RunError{Code: -1, NotFound: true, message: "spawn " + name + " ENOENT", cause: startErr}
		}
		return nil, nil, &RunError{Code: -1, message: startErr.Error(), cause: startErr}
	}
	go func() {
		select {
		case <-out.full:
			cancel(errMaxBuffer)
		case <-errOut.full:
			cancel(errMaxBuffer)
		case <-runContext.Done():
		}
	}()
	waitErr := command.Wait()
	stdout, stderr = out.Bytes(), errOut.Bytes()
	if out.overflowed() || errOut.overflowed() {
		stream := "stdout"
		if !out.overflowed() {
			stream = "stderr"
		}
		return stdout, stderr, &RunError{Code: -1, Killed: true, MaxBuffer: true, Signal: ExitSignal(command.ProcessState), Stdout: stdout, Stderr: stderr, message: stream + " maxBuffer length exceeded"}
	}
	if waitErr == nil {
		return stdout, stderr, nil
	}
	failure := &RunError{Code: -1, Stdout: stdout, Stderr: stderr, message: "Command failed: " + commandLine + "\n" + string(stderr), cause: waitErr}
	if command.ProcessState != nil {
		failure.Code = command.ProcessState.ExitCode()
		failure.Signal = ExitSignal(command.ProcessState)
	}
	failure.Killed = context.Cause(runContext) != nil && runContext.Err() != nil
	return stdout, stderr, failure
}

var (
	errTimedOut  = errors.New("timed out")
	errMaxBuffer = errors.New("max buffer exceeded")
)
