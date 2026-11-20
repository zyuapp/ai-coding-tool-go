//go:build unix

package terminal

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

// errExecFailed marks a shell that could not start in its folder or could not be found.
var errExecFailed = errors.New("terminal: shell did not start")

// process is a shell on a pseudo-terminal. Writes are queued and sent in order on their own
// goroutine, so a shell that stops reading never blocks the caller.
type process struct {
	cmd    *exec.Cmd
	master *os.File
	eof    chan struct{}

	mu      sync.Mutex
	queue   [][]byte
	ready   chan struct{}
	done    chan struct{}
	stopped bool
}

func startProcess(file string, args []string, cwd string, env []string, cols, rows int) (*process, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer slave.Close()
	if err := pty.Setsize(master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}); err != nil {
		master.Close()
		return nil, err
	}
	cmd := exec.Command(file, args...)
	cmd.Dir = cwd
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		master.Close()
		return nil, errors.Join(errExecFailed, err)
	}
	p := &process{cmd: cmd, master: master, eof: make(chan struct{}), ready: make(chan struct{}, 1), done: make(chan struct{})}
	go p.writeLoop()
	return p, nil
}

func (p *process) write(data []byte) {
	if len(data) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.queue = append(p.queue, data)
	select {
	case p.ready <- struct{}{}:
	default:
	}
}

func (p *process) writeLoop() {
	for {
		select {
		case <-p.done:
			return
		case <-p.ready:
		}
		p.mu.Lock()
		queue := p.queue
		p.queue = nil
		p.mu.Unlock()
		for _, data := range queue {
			if _, err := p.master.Write(data); err != nil {
				return
			}
		}
	}
}

func (p *process) resize(cols, rows int) {
	_ = pty.Setsize(p.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// kill hangs the shell up, as node-pty's kill does, and lets go of the terminal.
func (p *process) kill() {
	_ = p.cmd.Process.Signal(syscall.SIGHUP)
	p.release()
}

// wait is the shell's exit code; a shell ended by a signal reports 0, as node-pty reports it.
func (p *process) wait() int {
	err := p.cmd.Wait()
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 0
		}
		return exit.ExitCode()
	}
	return 0
}

func (p *process) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	p.queue = nil
	close(p.done)
	_ = p.master.Close()
}
