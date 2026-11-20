package headless

import "unicode/utf8"

const (
	minimumCols   = 2
	minimumRows   = 1
	maxParseChunk = 131072
)

// MouseTracking is the mouse protocol a program asked for, named as xterm.js's modes name it.
type MouseTracking string

const (
	MouseNone  MouseTracking = "none"
	MouseX10   MouseTracking = "x10"
	MouseVT200 MouseTracking = "vt200"
	MouseDrag  MouseTracking = "drag"
	MouseAny   MouseTracking = "any"
)

// Modes mirrors xterm.js's IModes.
type Modes struct {
	ApplicationCursorKeys bool
	ApplicationKeypad     bool
	BracketedPaste        bool
	Insert                bool
	MouseTracking         MouseTracking
	Origin                bool
	ReverseWraparound     bool
	SendFocus             bool
	SynchronizedOutput    bool
	Wraparound            bool
}

// Options configure a Terminal; zero Cols or Rows take xterm.js's minimums.
type Options struct {
	Cols       int
	Rows       int
	Scrollback int
}

// Terminal is a screen without a view: it resolves output into cells exactly as xterm.js does.
// It is not safe for concurrent use.
type Terminal struct {
	cols       int
	rows       int
	scrollback int

	normal *Buffer
	alt    *Buffer
	active *Buffer

	parser parser

	curAttr       attributeData
	charset       *charset
	charsets      [4]*charset
	glevel        int
	modes         Modes
	convertEol    bool
	links         map[string]uint32
	nextLink      uint32
	decodeBuffer  []uint32
	onTitleChange func(string)
}

func New(options Options) *Terminal {
	t := &Terminal{
		cols:       max(options.Cols, minimumCols),
		rows:       max(options.Rows, minimumRows),
		scrollback: max(options.Scrollback, 0),
	}
	t.parser.reset()
	t.reset()
	return t
}

// OnTitleChange registers the listener for OSC 0 and 2. It runs inside Write.
func (t *Terminal) OnTitleChange(listener func(title string)) { t.onTitleChange = listener }

func (t *Terminal) Cols() int { return t.cols }
func (t *Terminal) Rows() int { return t.rows }

// Buffer is the active buffer.
func (t *Terminal) Buffer() *Buffer { return t.active }
func (t *Terminal) Normal() *Buffer { return t.normal }
func (t *Terminal) Alt() *Buffer    { return t.alt }

// IsAlternate reports whether a program holds the alternate screen.
func (t *Terminal) IsAlternate() bool { return t.active == t.alt }

func (t *Terminal) Modes() Modes { return t.modes }

// Write parses output. Invalid UTF-8 becomes U+FFFD and byte-order marks are dropped, as
// xterm.js's string decoder does. Long writes are parsed in chunks, as xterm.js parses them.
func (t *Terminal) Write(data string) {
	if t.decodeBuffer == nil {
		t.decodeBuffer = make([]uint32, 0, 4096)
	}
	for len(data) > 0 {
		codes := t.decodeBuffer[:0]
		for len(data) > 0 && len(codes) < maxParseChunk {
			r, size := utf8.DecodeRuneInString(data)
			data = data[size:]
			if r != 0xFEFF {
				codes = append(codes, uint32(r))
			}
		}
		t.parse(codes)
		t.decodeBuffer = codes[:0]
	}
}

// Resize changes the grid, reflowing the normal buffer's wrapped lines.
func (t *Terminal) Resize(cols, rows int) {
	if cols == t.cols && rows == t.rows {
		return
	}
	cols = max(cols, minimumCols)
	rows = max(rows, minimumRows)
	t.cols = cols
	t.rows = rows
	t.normal.resize(cols, rows)
	t.alt.resize(cols, rows)
	t.normal.setupTabStops(cols)
	t.alt.setupTabStops(cols)
}

// reset is xterm.js's full reset (RIS), which keeps the size and scrollback limit.
func (t *Terminal) reset() {
	t.curAttr = attributeData{}
	t.normal = newBuffer(t, true)
	t.normal.fillViewportRows(attributeData{})
	t.alt = newBuffer(t, false)
	t.active = t.normal
	t.charset = nil
	t.charsets = [4]*charset{}
	t.glevel = 0
	t.modes = Modes{Wraparound: true, MouseTracking: MouseNone}
}
