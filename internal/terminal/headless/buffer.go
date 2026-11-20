package headless

const tabStopWidth = 8

// Buffer is the normal screen with its scrollback, or the alternate screen.
type Buffer struct {
	term          *Terminal
	hasScrollback bool
	lines         *circularList
	ybase         int
	y             int
	x             int
	scrollTop     int
	scrollBottom  int
	tabs          map[int]bool
	savedX        int
	savedY        int
	savedFg       uint32
	savedBg       uint32
	savedCharset  *charset
	cols          int
	rows          int
}

func newBuffer(term *Terminal, hasScrollback bool) *Buffer {
	b := &Buffer{term: term, hasScrollback: hasScrollback, cols: term.cols, rows: term.rows}
	b.lines = newCircularList(b.correctBufferLength(b.rows))
	b.scrollBottom = b.rows - 1
	b.setupTabStops(-1)
	return b
}

// Length counts the rows held, scrollback included.
func (b *Buffer) Length() int { return b.lines.length }

// Line is the row at an absolute index, oldest first; nil past the end.
func (b *Buffer) Line(row int) *Line {
	if row < 0 || row >= b.lines.length {
		return nil
	}
	return b.lines.get(row)
}

// RingLine reads a row as xterm.js's public getLine does, without a bounds check: one past the
// end it can return a row the ring no longer holds.
func (b *Buffer) RingLine(row int) *Line { return b.lines.get(row) }

// CursorX is the cursor column; it equals the column count while a wrap is pending.
func (b *Buffer) CursorX() int { return b.x }

// CursorY is the cursor row within the viewport.
func (b *Buffer) CursorY() int { return b.y }

// BaseY is the absolute index of the viewport's top row.
func (b *Buffer) BaseY() int { return b.ybase }

func (b *Buffer) nullCell(attr attributeData) Cell { return nullCell(attr) }

func (b *Buffer) blankLine(attr attributeData, isWrapped bool) *Line {
	return newLine(b.term.cols, nullCell(attr), isWrapped)
}

func (b *Buffer) correctBufferLength(rows int) int {
	if !b.hasScrollback {
		return rows
	}
	return rows + b.term.scrollback
}

func (b *Buffer) fillViewportRows(fill attributeData) {
	if b.lines.length == 0 {
		for i := b.rows; i > 0; i-- {
			b.lines.push(b.blankLine(fill, false))
		}
	}
}

func (b *Buffer) clear() {
	b.ybase, b.y, b.x = 0, 0, 0
	b.lines = newCircularList(b.correctBufferLength(b.rows))
	b.scrollTop = 0
	b.scrollBottom = b.rows - 1
	b.setupTabStops(-1)
}

func (b *Buffer) resize(newCols, newRows int) {
	fill := nullCell(attributeData{})
	newMaxLength := b.correctBufferLength(newRows)
	if newMaxLength > b.lines.maxLength {
		b.lines.setMaxLength(newMaxLength)
	}
	if b.lines.length > 0 {
		if b.cols < newCols {
			for i := 0; i < b.lines.length; i++ {
				b.lines.get(i).resize(newCols, fill)
			}
		}
		addToY := 0
		if b.rows < newRows {
			for y := b.rows; y < newRows; y++ {
				if b.lines.length < newRows+b.ybase {
					if b.ybase > 0 && b.lines.length <= b.ybase+b.y+addToY+1 {
						b.ybase--
						addToY++
					} else {
						b.lines.push(newLine(newCols, fill, false))
					}
				}
			}
		} else {
			for y := b.rows; y > newRows; y-- {
				if b.lines.length > newRows+b.ybase {
					if b.lines.length > b.ybase+b.y+1 {
						b.lines.pop()
					} else {
						b.ybase++
					}
				}
			}
		}
		if newMaxLength < b.lines.maxLength {
			if amountToTrim := b.lines.length - newMaxLength; amountToTrim > 0 {
				b.lines.trimStart(amountToTrim)
				b.ybase = max(b.ybase-amountToTrim, 0)
				b.savedY = max(b.savedY-amountToTrim, 0)
			}
			b.lines.setMaxLength(newMaxLength)
		}
		b.x = min(b.x, newCols-1)
		b.y = min(b.y, newRows-1)
		if addToY != 0 {
			b.y += addToY
		}
		b.savedX = min(b.savedX, newCols-1)
		b.scrollTop = 0
	}
	b.scrollBottom = newRows - 1
	if b.hasScrollback {
		b.reflow(newCols, newRows)
		if b.cols > newCols {
			for i := 0; i < b.lines.length; i++ {
				b.lines.get(i).resize(newCols, fill)
			}
		}
	}
	b.cols = newCols
	b.rows = newRows
}

func (b *Buffer) setupTabStops(i int) {
	if i >= 0 {
		if !b.tabs[i] {
			i = b.prevStop(i)
		}
	} else {
		b.tabs = map[int]bool{}
		i = 0
	}
	for ; i < b.cols; i += tabStopWidth {
		b.tabs[i] = true
	}
}

func (b *Buffer) prevStop(x int) int {
	for {
		x--
		if b.tabs[x] || x <= 0 {
			break
		}
	}
	return clampStop(x, b.cols)
}

func (b *Buffer) nextStop(x int) int {
	for {
		x++
		if b.tabs[x] || x >= b.cols {
			break
		}
	}
	return clampStop(x, b.cols)
}

func clampStop(x, cols int) int {
	if x >= cols {
		return cols - 1
	}
	if x < 0 {
		return 0
	}
	return x
}
