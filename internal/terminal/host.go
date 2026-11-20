// Package terminal runs the terminal panel's shells. It is the only place a pseudo-terminal is
// created, written to, or killed, and the only place that keeps what one has printed.
//
// Output is coalesced rather than forwarded per chunk: a build printing thousands of small writes
// would otherwise cost one window event each. Every flush is fed to a headless screen here and to
// the view at the same moment, from the same trimmed text, so what a caller reads is what the user
// sees rather than a second, drifting copy.
package terminal

import (
	"context"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zyuapp/ai-coding-tool-go/internal/terminal/headless"
)

const (
	flushInterval = 16 * time.Millisecond
	// maxFlushBytes is the most one flush carries, in UTF-16 code units. Anything past it is
	// dropped from the front, with a line saying so.
	maxFlushBytes   = 512 * 1024
	scrollbackLines = 5_000
	defaultCols     = 80
	defaultRows     = 24
	// exitDrain is how long an exited shell's last output is given to arrive before the exit is
	// reported, as node-pty waits for its socket to close.
	exitDrain = 200 * time.Millisecond
	// pendingCap bounds what accumulates between flushes; the front is dropped early, counted
	// as if the flush had dropped it.
	pendingCap = 4 * maxFlushBytes
)

var (
	ErrInvalidInput    = errors.New("Invalid terminal input.")
	ErrInvalidSize     = errors.New("Invalid terminal size.")
	ErrInvalidSequence = errors.New("Invalid terminal sequence.")
)

// Sink receives what the host publishes. Data goes straight to the window as terminal:data;
// updates go to the runtime as terminal.updated. Calls are made with the terminal's lock held,
// in order, so a sink must return promptly and must not call back into the Host.
type Sink interface {
	TerminalData(event DataEvent)
	TerminalUpdated(update Update)
}

// Options let the desktop give shells the login environment the platform layer resolved.
type Options struct {
	// Shell is the program and arguments a terminal runs. Defaults to $SHELL (else /bin/bash) -l.
	Shell func() (file string, args []string)
	// Env is the base environment of a shell. Defaults to the app's own.
	Env func() []string
}

// Host owns every shell. Its methods are safe for concurrent use.
type Host struct {
	mu       sync.Mutex
	sessions map[string]*session
	sink     Sink
	options  Options
}

type session struct {
	id   string
	cwd  string
	host *Host

	// mu guards the screen, the sequence, the output tail, the process, and publishing.
	mu       sync.Mutex
	screen   *headless.Terminal
	sequence int64
	output   *outputBuffer
	proc     *process
	closed   bool

	// pendingMu guards what the reader has decoded but not yet flushed.
	pendingMu      sync.Mutex
	pending        strings.Builder
	pendingDropped int
	timer          *time.Timer
}

func NewHost(sink Sink, options Options) *Host {
	if options.Shell == nil {
		options.Shell = defaultShell
	}
	if options.Env == nil {
		options.Env = os.Environ
	}
	return &Host{sessions: map[string]*session{}, sink: sink, options: options}
}

// SetSink replaces where output and updates go, as a new window does.
func (h *Host) SetSink(sink Sink) {
	h.mu.Lock()
	h.sink = sink
	h.mu.Unlock()
}

func (h *Host) currentSink() Sink {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sink
}

// Stop kills every shell, so a closed window leaves no process running.
func (h *Host) Stop() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.Close(id)
	}
}

func defaultShell() (string, []string) {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell, []string{"-l"}
	}
	return "/bin/bash", []string{"-l"}
}

func (h *Host) shellEnvironment(cwd string) []string {
	overrides := map[string]string{"TERM": "xterm-256color", "COLORTERM": "truecolor", "PWD": cwd}
	var env []string
	for _, entry := range h.options.Env() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "ELECTRON_RUN_AS_NODE" {
			continue
		}
		if _, replaced := overrides[key]; replaced {
			continue
		}
		env = append(env, entry)
	}
	for _, key := range []string{"PWD", "TERM", "COLORTERM"} {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

func (h *Host) session(terminalID string) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[terminalID]
}

func (h *Host) current(s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[s.id] == s
}

// Start is idempotent: a terminal that already has a shell keeps it, so reopening the panel never
// restarts one.
func (h *Host) Start(terminalID, cwd string) {
	h.mu.Lock()
	if h.sessions[terminalID] != nil {
		h.mu.Unlock()
		return
	}
	s := &session{
		id:     terminalID,
		cwd:    cwd,
		host:   h,
		screen: headless.New(headless.Options{Cols: defaultCols, Rows: defaultRows, Scrollback: scrollbackLines}),
	}
	h.sessions[terminalID] = s
	h.mu.Unlock()

	s.screen.OnTitleChange(func(value string) {
		if value == "" {
			value = title(cwd)
		}
		s.publishUpdate(Update{TerminalID: terminalID, Title: &value})
	})
	file, args := h.options.Shell()
	proc, err := startProcess(file, args, cwd, h.shellEnvironment(cwd), defaultCols, defaultRows)
	if errors.Is(err, errExecFailed) {
		// node-pty forks before it changes directory and execs, so either failing is an exit 1.
		go func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.closed {
				code := 1.0
				s.publishUpdate(Update{TerminalID: terminalID, Status: ptr(StatusExited), ExitCode: &code})
			}
		}()
		return
	}
	if err != nil {
		message := err.Error()
		s.mu.Lock()
		s.publishUpdate(Update{TerminalID: terminalID, Status: ptr(StatusExited), Error: &message})
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.proc = proc
	s.mu.Unlock()
	go s.read(proc)
	go s.wait(proc)
}

func ptr[T any](value T) *T { return &value }

// Write sends keystrokes to the shell.
func (h *Host) Write(terminalID, data string) error {
	if utf16Len(data) > MaxInput {
		return ErrInvalidInput
	}
	if s := h.session(terminalID); s != nil {
		s.mu.Lock()
		proc := s.proc
		s.mu.Unlock()
		if proc != nil {
			proc.write([]byte(data))
		}
	}
	return nil
}

// Resize changes the size the shell believes it has. Output and grid changes share a sequence,
// so every viewer follows the shell's current size.
func (h *Host) Resize(terminalID string, cols, rows float64) error {
	if !IsDimension(cols) || !IsDimension(rows) {
		return ErrInvalidSize
	}
	s := h.session(terminalID)
	if s == nil {
		return nil
	}
	width, height := int(cols), int(rows)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.screen.Cols() == width && s.screen.Rows() == height {
		return nil
	}
	s.flushLocked()
	s.screen.Resize(width, height)
	if s.proc != nil {
		s.proc.resize(width, height)
	}
	s.sequence++
	if s.output != nil {
		s.output.reset()
	}
	s.publishData(DataEvent{TerminalID: terminalID, Data: "", Sequence: float64(s.sequence), Size: &Size{Cols: cols, Rows: rows}})
	return nil
}

// Close kills the shell and forgets everything it printed.
func (h *Host) Close(terminalID string) {
	h.mu.Lock()
	s := h.sessions[terminalID]
	delete(h.sessions, terminalID)
	h.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	proc := s.proc
	s.proc = nil
	output := s.output
	s.mu.Unlock()
	s.pendingMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.pending.Reset()
	s.pendingMu.Unlock()
	if output != nil {
		output.wake()
	}
	if proc != nil {
		proc.kill()
	}
}

// Snapshot is the resolved screen and the last flush it includes. Nil when the terminal is gone.
func (h *Host) Snapshot(terminalID string) *ScreenSnapshot {
	s := h.session(terminalID)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.flushLocked()
	return s.snapshotLocked()
}

func (s *session) snapshotLocked() *ScreenSnapshot {
	return &ScreenSnapshot{
		Data:     serializeTerminal(s.screen),
		Cols:     float64(s.screen.Cols()),
		Rows:     float64(s.screen.Rows()),
		Sequence: float64(s.sequence),
	}
}

// ReadOutput is what a remote viewer reads: only output after its watermark, or the screen when
// it has none or fell behind. A viewer that is already current is held for up to a second.
func (h *Host) ReadOutput(ctx context.Context, terminalID string, after *float64) (*OutputRead, error) {
	s := h.session(terminalID)
	if s == nil {
		return nil, nil
	}
	if after != nil && (*after != math.Trunc(*after) || *after < 0 || *after > 1<<53-1) {
		return nil, ErrInvalidSequence
	}
	s.mu.Lock()
	if s.output == nil {
		s.output = newOutputBuffer()
	}
	output := s.output
	current := after != nil && int64(*after) == s.sequence
	woken := output.waiter()
	s.mu.Unlock()
	if current {
		wait(ctx, woken)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil
	}
	if after != nil {
		if data, ok := s.output.read(int64(*after), s.sequence); ok {
			return &OutputRead{
				ScreenSnapshot: ScreenSnapshot{Data: data, Sequence: float64(s.sequence), Cols: float64(s.screen.Cols()), Rows: float64(s.screen.Rows())},
				Kind:           ReadOutput,
			}, nil
		}
	}
	s.flushLocked()
	return &OutputRead{ScreenSnapshot: *s.snapshotLocked(), Kind: ReadSnapshot}, nil
}

// ReadTerminal is the runtime port's read: the line limit is clamped as terminalLineLimit does.
func (h *Host) ReadTerminal(terminalID string, options ReadOptions) *Text {
	lines := options.Lines
	match := ""
	if options.Match != nil {
		match = *options.Match
	}
	return h.Read(terminalID, lineLimit(&lines), match)
}

// Read is the terminal as plain text. Pending output is flushed first, so a read taken straight
// after a command shows what that command printed. Nil when the terminal is gone.
func (h *Host) Read(terminalID string, lines int, match string) *Text {
	s := h.session(terminalID)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.flushLocked()
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		tail, total := screenTail(s.screen, lines)
		kept := withinReadBudget(tail)
		return &Text{Lines: kept, Omitted: float64(total - len(kept))}
	}
	var filtered []string
	for _, line := range screenLines(s.screen) {
		if strings.Contains(strings.ToLower(line), match) {
			filtered = append(filtered, line)
		}
	}
	kept := withinReadBudget(filtered[max(0, len(filtered)-lines):])
	matched := float64(len(filtered))
	return &Text{Lines: kept, Omitted: float64(len(filtered) - len(kept)), Matched: &matched}
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }

// screenLines is everything the terminal holds, oldest first, without the trailing blank lines a
// screen always has.
func screenLines(screen *headless.Terminal) []string {
	buffer := screen.Buffer()
	lines := make([]string, 0, buffer.Length())
	for row := 0; row < buffer.Length(); row++ {
		lines = append(lines, lineText(buffer, row))
	}
	for len(lines) > 0 && isBlank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// screenTail is the newest rows only, though the total still counts them all.
func screenTail(screen *headless.Terminal, limit int) ([]string, int) {
	buffer := screen.Buffer()
	end := buffer.Length()
	for end > 0 && isBlank(lineText(buffer, end-1)) {
		end--
	}
	lines := make([]string, 0, min(limit, end))
	for row := max(0, end-limit); row < end; row++ {
		lines = append(lines, lineText(buffer, row))
	}
	return lines, end
}

func lineText(buffer *headless.Buffer, row int) string {
	if line := buffer.Line(row); line != nil {
		return line.TranslateToString(true)
	}
	return ""
}

// push queues decoded output and arms the flush timer.
func (s *session) push(chunk string) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pending.WriteString(chunk)
	if s.pending.Len() > pendingCap*2 {
		text := s.pending.String()
		cut := len(text) - pendingCap
		for cut < len(text) && !utf8.RuneStart(text[cut]) {
			cut++
		}
		s.pendingDropped += utf16Len(text[:cut])
		s.pending.Reset()
		s.pending.WriteString(text[cut:])
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(flushInterval, s.flush)
	}
}

func (s *session) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.flushLocked()
	}
}

// flushLocked feeds everything pending to the screen and the view as one write.
func (s *session) flushLocked() {
	s.pendingMu.Lock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	pending := s.pending.String()
	dropped := s.pendingDropped
	s.pending.Reset()
	s.pendingDropped = 0
	s.pendingMu.Unlock()
	if pending == "" {
		return
	}
	data := trimFlush(pending, dropped)
	s.screen.Write(data)
	s.sequence++
	event := DataEvent{TerminalID: s.id, Data: data, Sequence: float64(s.sequence)}
	if s.output != nil {
		s.output.push(s.sequence, data)
	}
	s.publishData(event)
}

// trimFlush keeps the newest maxFlushBytes of a flush, saying how much went before them.
func trimFlush(pending string, alreadyDropped int) string {
	length := utf16Len(pending)
	dropped := alreadyDropped + length - maxFlushBytes
	if dropped <= 0 {
		return pending
	}
	cut := 0
	for units := 0; units < length-maxFlushBytes && cut < len(pending); {
		r, size := utf8.DecodeRuneInString(pending[cut:])
		cut += size
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	kilobytes := int64(math.Round(float64(dropped) / 1024))
	return "\r\n… " + groupThousands(kilobytes) + " KB of output dropped\r\n" + pending[cut:]
}

// groupThousands formats as toLocaleString does in the en-US locale.
func groupThousands(value int64) string {
	digits := strconv.FormatInt(value, 10)
	var grouped strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return grouped.String()
}

func (s *session) publishData(event DataEvent) {
	if sink := s.host.currentSink(); sink != nil {
		sink.TerminalData(event)
	}
}

func (s *session) publishUpdate(update Update) {
	if sink := s.host.currentSink(); sink != nil {
		sink.TerminalUpdated(update)
	}
}

func (s *session) read(proc *process) {
	defer close(proc.eof)
	buffer := make([]byte, 64*1024)
	var carry []byte
	for {
		count, err := proc.master.Read(buffer)
		if count > 0 {
			chunk := append(carry, buffer[:count]...)
			text, rest := decodeUTF8(chunk)
			carry = append([]byte(nil), rest...)
			if text != "" && s.host.current(s) {
				s.push(text)
			}
		}
		if err != nil {
			return
		}
	}
}

// decodeUTF8 decodes complete UTF-8, keeping a sequence split at the end for the next read.
// Invalid bytes become U+FFFD, as a streaming decoder makes them.
func decodeUTF8(bytes []byte) (string, []byte) {
	end := len(bytes)
	for back := 1; back <= min(3, len(bytes)); back++ {
		b := bytes[len(bytes)-back]
		if b&0xC0 == 0x80 {
			continue
		}
		width := 0
		switch {
		case b >= 0xC0 && b <= 0xDF:
			width = 2
		case b >= 0xE0 && b <= 0xEF:
			width = 3
		case b >= 0xF0 && b <= 0xF7:
			width = 4
		}
		if width > back {
			end = len(bytes) - back
		}
		break
	}
	valid := bytes[:end]
	if utf8.Valid(valid) {
		return string(valid), bytes[end:]
	}
	var text strings.Builder
	for len(valid) > 0 {
		r, size := utf8.DecodeRune(valid)
		valid = valid[size:]
		text.WriteRune(r)
	}
	return text.String(), bytes[end:]
}

func (s *session) wait(proc *process) {
	code := proc.wait()
	select {
	case <-proc.eof:
	case <-time.After(exitDrain):
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.proc != proc {
		return
	}
	s.flushLocked()
	s.proc = nil
	proc.release()
	exitCode := float64(code)
	s.publishUpdate(Update{TerminalID: s.id, Status: ptr(StatusExited), ExitCode: &exitCode})
}
