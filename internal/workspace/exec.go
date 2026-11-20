package workspace

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Command builds a child process for a program and its arguments. The app passes platform.Command,
// which finds the program on the login shell's search path and gives the child that environment.
type Command func(ctx context.Context, name string, args ...string) *exec.Cmd

// DefaultCommand runs the program found on this process's own PATH.
func DefaultCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// Node's default `maxBuffer` for `execFile`.
const defaultMaxBuffer = 1024 * 1024

type runOptions struct {
	dir       string
	input     *string
	timeout   time.Duration
	maxBuffer int
	// raw keeps stdout's bytes as they are instead of decoding them as UTF-8.
	raw bool
}

// ProcessError is a failed `execFile`, worded the way Node words it.
type ProcessError struct {
	Program string
	Args    []string
	// ExitCode is nil when the process never exited on its own (not started, killed, timed out).
	ExitCode *int
	Stdout   string
	Stderr   string
	// NotFound is Node's ENOENT: the program, or the directory it was to run in, is not there.
	NotFound bool
	// TooLarge is ERR_CHILD_PROCESS_STDIO_MAXBUFFER.
	TooLarge   bool
	tooLargeOn string
	cause      error
}

func (e *ProcessError) Error() string {
	switch {
	case e.NotFound:
		return "spawn " + e.Program + " ENOENT"
	case e.TooLarge:
		return e.tooLargeOn + " maxBuffer length exceeded"
	case e.cause != nil:
		return e.cause.Error()
	}
	return "Command failed: " + strings.Join(append([]string{e.Program}, e.Args...), " ") + "\n" + e.Stderr
}

func (e *ProcessError) Unwrap() error { return e.cause }

// cappedBuffer collects a stream and kills the process once it passes the limit, as Node does.
type cappedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	kill     func()
}

var errMaxBuffer = errors.New("maxBuffer length exceeded")

func (b *cappedBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return 0, errMaxBuffer
	}
	if b.buffer.Len()+len(chunk) > b.limit {
		b.buffer.Write(chunk[:b.limit-b.buffer.Len()])
		b.exceeded = true
		b.kill()
		return 0, errMaxBuffer
	}
	return b.buffer.Write(chunk)
}

// run is `execFile` with a timeout and a stdout ceiling: no shell, stdout as UTF-8 text.
func run(ctx context.Context, command Command, program string, args []string, options runOptions) (string, error) {
	if options.maxBuffer == 0 {
		options.maxBuffer = defaultMaxBuffer
	}
	ctx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	cmd := command(ctx, program, args...)
	cmd.Dir = options.dir
	if options.input != nil {
		cmd.Stdin = strings.NewReader(*options.input)
	}
	kill := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	stdout := &cappedBuffer{limit: options.maxBuffer, kill: kill}
	stderr := &cappedBuffer{limit: options.maxBuffer, kill: kill}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second

	err := cmd.Start()
	if err != nil {
		failure := &ProcessError{Program: program, Args: args}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
			failure.NotFound = true
		} else {
			failure.cause = err
		}
		return "", failure
	}
	err = cmd.Wait()
	text := func(b *cappedBuffer) string {
		if options.raw && b == stdout {
			return b.buffer.String()
		}
		return decodeUTF8(b.buffer.Bytes())
	}
	if err == nil && !stdout.exceeded && !stderr.exceeded {
		return text(stdout), nil
	}
	failure := &ProcessError{Program: program, Args: args, Stdout: text(stdout), Stderr: text(stderr)}
	switch {
	case stdout.exceeded:
		failure.TooLarge, failure.tooLargeOn = true, "stdout"
	case stderr.exceeded:
		failure.TooLarge, failure.tooLargeOn = true, "stderr"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 && !failure.TooLarge {
		code := exit.ExitCode()
		failure.ExitCode = &code
	}
	return failure.Stdout, failure
}

// decodeUTF8 decodes bytes the way Node's `toString("utf8")` does, each invalid byte a U+FFFD.
func decodeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var out strings.Builder
	out.Grow(len(data))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			out.WriteRune(utf8.RuneError)
		} else {
			out.Write(data[:size])
		}
		data = data[size:]
	}
	return out.String()
}

// future is a shared, settled-once result, which a cached promise is in the TypeScript.
type future[T any] struct {
	done  chan struct{}
	value T
	err   error
}

func startFuture[T any](work func() (T, error)) *future[T] {
	f := &future[T]{done: make(chan struct{})}
	go func() {
		defer close(f.done)
		f.value, f.err = work()
	}()
	return f
}

func (f *future[T]) wait() (T, error) {
	<-f.done
	return f.value, f.err
}
