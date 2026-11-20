package terminal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/terminal/headless"
)

type recorder struct {
	mu      sync.Mutex
	data    []DataEvent
	updates []Update
}

func (r *recorder) TerminalData(event DataEvent) {
	r.mu.Lock()
	r.data = append(r.data, event)
	r.mu.Unlock()
}

func (r *recorder) TerminalUpdated(update Update) {
	r.mu.Lock()
	r.updates = append(r.updates, update)
	r.mu.Unlock()
}

func (r *recorder) events() []DataEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.data)
}

func (r *recorder) exit(terminalID string) *Update {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, update := range r.updates {
		if update.TerminalID == terminalID && update.Status != nil && *update.Status == StatusExited {
			return &update
		}
	}
	return nil
}

func newTestHost(t *testing.T, file string, args ...string) (*Host, *recorder) {
	t.Helper()
	sink := &recorder{}
	host := NewHost(sink, Options{Shell: func() (string, []string) { return file, args }})
	t.Cleanup(host.Stop)
	return host, sink
}

// until polls rather than sleeping a fixed time, so a slow shell fails as a timeout instead of flaking.
func until[T any](t *testing.T, message string, check func() (T, bool)) T {
	t.Helper()
	for range 200 {
		if value, ok := check(); ok {
			return value
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(message)
	panic("unreachable")
}

func joined(text *Text) string {
	if text == nil {
		return ""
	}
	return strings.Join(text.Lines, "\n")
}

func waitForText(t *testing.T, host *Host, id, want string) *Text {
	t.Helper()
	return until(t, "the shell never printed "+want, func() (*Text, bool) {
		read := host.Read(id, 100, "")
		return read, strings.Contains(joined(read), want)
	})
}

func TestReloadSnapshotIncludesEarlierOutputAndMarksWhereLiveOutputResumes(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	host.Start("snapshot", t.TempDir())
	host.Write("snapshot", "printf 'before-%s\\n' reload\r")
	waitForText(t, host, "snapshot", "before-reload")
	snapshot := host.Snapshot("snapshot")
	if snapshot == nil {
		t.Fatal("no snapshot")
	}
	events := sink.events()
	if snapshot.Sequence != events[len(events)-1].Sequence {
		t.Fatalf("snapshot sequence %v, last flush %v", snapshot.Sequence, events[len(events)-1].Sequence)
	}
	restored := headless.New(headless.Options{Cols: int(snapshot.Cols), Rows: int(snapshot.Rows), Scrollback: 1000})
	restored.Write(snapshot.Data)
	if !strings.Contains(strings.Join(screenText(restored), "\n"), "before-reload") {
		t.Fatal("the restored screen lost earlier output")
	}
	host.Write("snapshot", "printf 'after-%s\\n' reload\r")
	waitForText(t, host, "snapshot", "after-reload")
	var later strings.Builder
	for _, event := range sink.events() {
		if event.Sequence > snapshot.Sequence {
			later.WriteString(event.Data)
		}
	}
	if !strings.Contains(later.String(), "after-reload") {
		t.Fatal("output after the snapshot was not published past its sequence")
	}
}

func TestShellRunsWhatTheUserTypesAndReadsItAsPlainText(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-1", t.TempDir())
	host.Write("terminal-1", "printf 'plain-%s\\n' output\r")
	read := waitForText(t, host, "terminal-1", "plain-output")
	if read.Omitted != 0 {
		t.Fatalf("omitted %v", read.Omitted)
	}
	if sink.exit("terminal-1") != nil {
		t.Fatal("the shell exited")
	}
}

func TestReadReturnsWhatTheScreenShowsNotTheBytesThatDrewIt(t *testing.T) {
	host, _ := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-2", t.TempDir())
	host.Write("terminal-2", "printf 'progress 10%%\\rprogress 100%%\\n\\033[31mred-text\\033[0m\\n'\r")
	text := joined(waitForText(t, host, "terminal-2", "red-text"))
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("escape sequences were returned: %q", text)
	}
	if !strings.Contains(text, "progress 100%") || strings.Contains(text, "progress 10%\r") {
		t.Fatalf("the overwritten progress line was not resolved: %q", text)
	}
}

func TestFilteredReadKeepsOnlyMatchingLinesAndCountsThem(t *testing.T) {
	host, _ := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-3", t.TempDir())
	host.Write("terminal-3", "printf 'info one\\nERROR two\\ninfo three\\nerror four\\n'\r")
	read := until(t, "the shell never printed", func() (*Text, bool) {
		read := host.Read("terminal-3", 100, "  Error ")
		return read, read != nil && read.Matched != nil && *read.Matched >= 2
	})
	for _, line := range read.Lines {
		if !strings.Contains(strings.ToLower(line), "error") {
			t.Fatalf("kept %q", line)
		}
	}
	if unfiltered := host.Read("terminal-3", 100, ""); len(read.Lines) >= len(unfiltered.Lines) {
		t.Fatal("lines that do not match were kept")
	}
}

func TestReadNeverReturnsMoreLinesThanAskedAndCountsTheRest(t *testing.T) {
	host, _ := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-4", t.TempDir())
	host.Write("terminal-4", "for i in $(seq 1 60); do echo line-$i; done\r")
	read := until(t, "the shell never printed", func() (*Text, bool) {
		read := host.ReadTerminal("terminal-4", ReadOptions{Lines: 10})
		return read, strings.Contains(joined(read), "line-60")
	})
	if len(read.Lines) != 10 || read.Omitted <= 0 {
		t.Fatalf("%d lines, %v omitted", len(read.Lines), read.Omitted)
	}
}

func TestOutputArrivesCoalesced(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-5", t.TempDir())
	host.Write("terminal-5", "for i in $(seq 1 300); do echo chunk-$i; done\r")
	until(t, "the shell never printed", func() (bool, bool) {
		return true, strings.Contains(joined(host.Read("terminal-5", 400, "")), "chunk-300")
	})
	if flushes := len(sink.events()); flushes >= 300 {
		t.Fatalf("300 lines arrived in %d flushes", flushes)
	}
}

func TestExitedShellKeepsWhatItPrintedAndClosingTakesItAway(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-6", t.TempDir())
	host.Write("terminal-6", "echo last-word; exit 3\r")
	exit := until(t, "the shell never exited", func() (*Update, bool) {
		exit := sink.exit("terminal-6")
		return exit, exit != nil
	})
	if exit.ExitCode == nil || *exit.ExitCode != 3 {
		t.Fatalf("exit %+v", exit)
	}
	if !strings.Contains(joined(host.Read("terminal-6", 100, "")), "last-word") {
		t.Fatal("an exited shell could not be read")
	}
	if err := host.Write("terminal-6", "ignored\r"); err != nil {
		t.Fatal(err)
	}
	host.Close("terminal-6")
	if host.Read("terminal-6", 100, "") != nil || host.Snapshot("terminal-6") != nil {
		t.Fatal("a closed terminal can still be read")
	}
}

func TestStartingARunningTerminalKeepsItsShell(t *testing.T) {
	host, _ := newTestHost(t, "/bin/sh", "-l")
	host.Start("terminal-7", t.TempDir())
	host.Write("terminal-7", "echo first-run\r")
	waitForText(t, host, "terminal-7", "first-run")
	host.Start("terminal-7", t.TempDir())
	if !strings.Contains(joined(host.Read("terminal-7", 100, "")), "first-run") {
		t.Fatal("restarting replaced the shell")
	}
}

func TestRemoteReadsWaitForOutputRecoverResizedScreensAndSettleOnClose(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	ctx := context.Background()
	host.Start("remote", t.TempDir())
	host.Write("remote", "printf 'remote-%s\\n' ready\r")
	waitForText(t, host, "remote", "remote-ready")
	first, err := host.ReadOutput(ctx, "remote", nil)
	if err != nil || first == nil || first.Kind != ReadSnapshot {
		t.Fatalf("first read %+v, %v", first, err)
	}
	pending := make(chan *OutputRead, 1)
	go func() {
		next, _ := host.ReadOutput(ctx, "remote", &first.Sequence)
		pending <- next
	}()
	time.Sleep(20 * time.Millisecond)
	host.Write("remote", "printf 'new-%s\\n' output\r")
	next := <-pending
	if next == nil || next.Kind != ReadOutput || next.Sequence <= first.Sequence || strings.Contains(next.Data, "remote-ready") {
		t.Fatalf("next read %+v", next)
	}
	if err := host.Resize("remote", 100, 30); err != nil {
		t.Fatal(err)
	}
	events := sink.events()
	sizeEvent := events[len(events)-1]
	if sizeEvent.Size == nil || sizeEvent.Size.Cols != 100 || sizeEvent.Size.Rows != 30 {
		t.Fatalf("the local viewer did not follow the resize: %+v", sizeEvent)
	}
	resized, err := host.ReadOutput(ctx, "remote", &next.Sequence)
	if err != nil || resized == nil || resized.Kind != ReadSnapshot || resized.Cols != 100 || resized.Rows != 30 || resized.Sequence < sizeEvent.Sequence {
		t.Fatalf("resized read %+v, %v", resized, err)
	}
	closing := make(chan *OutputRead, 1)
	go func() {
		read, _ := host.ReadOutput(ctx, "remote", &resized.Sequence)
		closing <- read
	}()
	host.Close("remote")
	select {
	case read := <-closing:
		if read != nil {
			t.Fatalf("a closed terminal answered %+v", read)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("closing did not settle a waiting read")
	}
	if err := host.Resize("remote", 0, -1); !errors.Is(err, ErrInvalidSize) || err.Error() != "Invalid terminal size." {
		t.Fatalf("resize error %v", err)
	}
	if err := host.Write("remote", strings.Repeat("x", 65_537)); !errors.Is(err, ErrInvalidInput) || err.Error() != "Invalid terminal input." {
		t.Fatalf("write error %v", err)
	}
	bad := 1.5
	host.Start("remote-2", t.TempDir())
	if _, err := host.ReadOutput(ctx, "remote-2", &bad); !errors.Is(err, ErrInvalidSequence) {
		t.Fatalf("sequence error %v", err)
	}
}

func TestShellThatCannotStartExitsWithCodeOne(t *testing.T) {
	for name, start := range map[string]func(*Host){
		"missing folder": func(host *Host) { host.Start("t", "/nonexistent/act-terminal-missing") },
	} {
		t.Run(name, func(t *testing.T) {
			host, sink := newTestHost(t, "/bin/sh", "-l")
			start(host)
			exit := until(t, "no exit was reported", func() (*Update, bool) {
				exit := sink.exit("t")
				return exit, exit != nil
			})
			if exit.ExitCode == nil || *exit.ExitCode != 1 || exit.Error != nil {
				t.Fatalf("exit %+v", exit)
			}
			if host.Snapshot("t") == nil {
				t.Fatal("the terminal was dropped")
			}
		})
	}
	host, sink := newTestHost(t, "/nonexistent/act-shell")
	host.Start("t", t.TempDir())
	exit := until(t, "no exit was reported", func() (*Update, bool) {
		exit := sink.exit("t")
		return exit, exit != nil
	})
	if exit.ExitCode == nil || *exit.ExitCode != 1 {
		t.Fatalf("exit %+v", exit)
	}
}

func TestClosingKillsTheShellAndPublishesNothingMore(t *testing.T) {
	host, sink := newTestHost(t, "/bin/sh", "-l")
	host.Start("kill", t.TempDir())
	host.Write("kill", "echo $$\r")
	read := until(t, "the shell never printed its pid", func() (string, bool) {
		match := regexp.MustCompile(`(?m)^(\d+)$`).FindStringSubmatch(joined(host.Read("kill", 100, "")))
		if match == nil {
			return "", false
		}
		return match[1], true
	})
	host.Close("kill")
	until(t, "the shell outlived its terminal", func() (bool, bool) {
		return true, exec.Command("kill", "-0", read).Run() != nil
	})
	time.Sleep(300 * time.Millisecond)
	if sink.exit("kill") != nil {
		t.Fatal("a closed terminal reported its exit")
	}
}

func TestInteractiveZshTitleAlternateScreenAndResize(t *testing.T) {
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("needs zsh")
	}
	host, sink := newTestHost(t, "/bin/zsh", "-f", "-i")
	cwd := t.TempDir()
	host.Start("zsh", cwd)
	host.Write("zsh", "print -n '\\e]0;custom-title\\a'; print -n '\\e]0;\\a'; echo ready-$((40+2))\r")
	waitForText(t, host, "zsh", "ready-42")
	sink.mu.Lock()
	var titles []string
	for _, update := range sink.updates {
		if update.Title != nil {
			titles = append(titles, *update.Title)
		}
	}
	sink.mu.Unlock()
	if !slices.Contains(titles, "custom-title") || !slices.Contains(titles, title(cwd)) {
		t.Fatalf("titles %q", titles)
	}
	if err := host.Resize("zsh", 50, 10); err != nil {
		t.Fatal(err)
	}
	host.Write("zsh", "echo cols-$COLUMNS; print -n '\\e[?1049h\\e[Hfull''-screen'\r")
	waitForText(t, host, "zsh", "full-screen")
	snapshot := host.Snapshot("zsh")
	if snapshot.Cols != 50 || snapshot.Rows != 10 || !strings.Contains(snapshot.Data, "\x1b[?1049h") {
		t.Fatalf("snapshot %v x %v", snapshot.Cols, snapshot.Rows)
	}
	host.Write("zsh", "print -n '\\e[?1049l'; echo back-$((1+1))\r")
	text := joined(waitForText(t, host, "zsh", "back-2"))
	if !strings.Contains(text, "cols-50") || strings.Contains(text, "full-screen") {
		t.Fatalf("the alternate screen leaked into the normal one: %q", text)
	}
}
