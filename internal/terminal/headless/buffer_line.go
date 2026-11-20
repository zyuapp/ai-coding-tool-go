package headless

import "strings"

const cellSize = 3

// Line is one row of a buffer: content, foreground and background words per cell.
type Line struct {
	data          []uint32
	combined      map[int]string
	extendedAttrs map[int]extendedAttrs
	length        int
	isWrapped     bool
}

func newLine(cols int, fill Cell, isWrapped bool) *Line {
	line := &Line{data: make([]uint32, cols*cellSize), length: cols, isWrapped: isWrapped}
	line.fillRange(0, cols, fill)
	return line
}

// fillRange sets cells [start, end) to fill.
func (l *Line) fillRange(start, end int, fill Cell) {
	if fill.content&contentIsCombinedMask != 0 || fill.bg&bgHasExtended != 0 {
		for i := start; i < end; i++ {
			l.setCell(i, fill)
		}
		return
	}
	data := l.data[start*cellSize : end*cellSize]
	for i := 0; i < len(data); i += cellSize {
		data[i] = fill.content
		data[i+1] = fill.fg
		data[i+2] = fill.bg
	}
}

// Length is the number of cells in the row.
func (l *Line) Length() int { return l.length }

// IsWrapped is true when this row continues the previous one.
func (l *Line) IsWrapped() bool { return l.isWrapped }

// Cell loads the cell at x into cell. False when x is outside the row.
func (l *Line) Cell(x int, cell *Cell) bool {
	if x < 0 || x >= l.length {
		return false
	}
	l.loadCell(x, cell)
	return true
}

// The accessors below read and write like xterm.js's typed array: out of range is zero and a no-op.

func (l *Line) contentAt(index int) uint32 {
	if index < 0 || index >= l.length {
		return 0
	}
	return l.data[index*cellSize]
}

func (l *Line) getWidth(index int) int    { return int(l.contentAt(index) >> contentWidthShift) }
func (l *Line) hasWidth(index int) bool   { return l.contentAt(index)&contentWidthMask != 0 }
func (l *Line) hasContent(index int) bool { return l.contentAt(index)&contentHasContentMask != 0 }
func (l *Line) isProtected(index int) bool {
	return index >= 0 && index < l.length && l.data[index*cellSize+2]&bgProtected != 0
}

func (l *Line) getString(index int) string {
	content := l.data[index*cellSize]
	if content&contentIsCombinedMask != 0 {
		return l.combined[index]
	}
	if cp := content & contentCodepointMask; cp != 0 {
		return string(rune(cp))
	}
	return ""
}

func (l *Line) loadCell(index int, cell *Cell) {
	start := index * cellSize
	cell.content = l.data[start]
	cell.fg = l.data[start+1]
	cell.bg = l.data[start+2]
	if cell.content&contentIsCombinedMask != 0 {
		cell.combined = l.combined[index]
	}
	if cell.bg&bgHasExtended != 0 {
		cell.extended = l.extendedAttrs[index]
	}
}

func (l *Line) setCell(index int, cell Cell) {
	if index < 0 || index >= l.length {
		return
	}
	if cell.content&contentIsCombinedMask != 0 {
		if l.combined == nil {
			l.combined = map[int]string{}
		}
		l.combined[index] = cell.combined
	}
	if cell.bg&bgHasExtended != 0 {
		l.setExtended(index, cell.extended)
	}
	start := index * cellSize
	l.data[start] = cell.content
	l.data[start+1] = cell.fg
	l.data[start+2] = cell.bg
}

func (l *Line) setExtended(index int, ext extendedAttrs) {
	if l.extendedAttrs == nil {
		l.extendedAttrs = map[int]extendedAttrs{}
	}
	l.extendedAttrs[index] = ext
}

func (l *Line) setCellFromCodepoint(index int, codePoint uint32, width int, fg, bg uint32, ext extendedAttrs) {
	if index < 0 || index >= l.length {
		return
	}
	if bg&bgHasExtended != 0 {
		l.setExtended(index, ext)
	}
	start := index * cellSize
	l.data[start] = codePoint | uint32(width)<<contentWidthShift
	l.data[start+1] = fg
	l.data[start+2] = bg
}

func (l *Line) setCellFromAttr(index int, codePoint uint32, width int, attr attributeData) {
	l.setCellFromCodepoint(index, codePoint, width, attr.fg, attr.bg, attr.extended)
}

func (l *Line) setCellFromFill(index int, codePoint uint32, width int, fill Cell) {
	l.setCellFromCodepoint(index, codePoint, width, fill.fg, fill.bg, fill.extended)
}

func (l *Line) addCodepointToCell(index int, codePoint uint32, width int) {
	if index < 0 || index >= l.length {
		return
	}
	content := l.data[index*cellSize]
	if content&contentIsCombinedMask != 0 {
		l.combined[index] += string(rune(codePoint))
	} else if content&contentCodepointMask != 0 {
		if l.combined == nil {
			l.combined = map[int]string{}
		}
		l.combined[index] = string(rune(content&contentCodepointMask)) + string(rune(codePoint))
		content &^= contentCodepointMask
		content |= contentIsCombinedMask
	} else {
		content = codePoint | 1<<contentWidthShift
	}
	if width != 0 {
		content &^= contentWidthMask
		content |= uint32(width) << contentWidthShift
	}
	l.data[index*cellSize] = content
}

func (l *Line) insertCells(pos, n int, fill Cell) {
	pos %= l.length
	if pos != 0 && l.getWidth(pos-1) == 2 {
		l.setCellFromFill(pos-1, 0, 1, fill)
	}
	if n < l.length-pos {
		var cell Cell
		for i := l.length - pos - n - 1; i >= 0; i-- {
			l.loadCell(pos+i, &cell)
			l.setCell(pos+n+i, cell)
		}
		for i := 0; i < n; i++ {
			l.setCell(pos+i, fill)
		}
	} else {
		for i := pos; i < l.length; i++ {
			l.setCell(i, fill)
		}
	}
	if l.getWidth(l.length-1) == 2 {
		l.setCellFromFill(l.length-1, 0, 1, fill)
	}
}

func (l *Line) deleteCells(pos, n int, fill Cell) {
	pos %= l.length
	if n < l.length-pos {
		var cell Cell
		for i := 0; i < l.length-pos-n; i++ {
			l.loadCell(pos+n+i, &cell)
			l.setCell(pos+i, cell)
		}
		for i := l.length - n; i < l.length; i++ {
			l.setCell(i, fill)
		}
	} else {
		for i := pos; i < l.length; i++ {
			l.setCell(i, fill)
		}
	}
	if pos != 0 && l.getWidth(pos-1) == 2 {
		l.setCellFromFill(pos-1, 0, 1, fill)
	}
	if l.getWidth(pos) == 0 && !l.hasContent(pos) {
		l.setCellFromFill(pos, 0, 1, fill)
	}
}

func (l *Line) replaceCells(start, end int, fill Cell, respectProtect bool) {
	if respectProtect {
		if start != 0 && l.getWidth(start-1) == 2 && !l.isProtected(start-1) {
			l.setCellFromFill(start-1, 0, 1, fill)
		}
		if end < l.length && end > 0 && l.getWidth(end-1) == 2 && !l.isProtected(end) {
			l.setCellFromFill(end, 0, 1, fill)
		}
		for ; start < end && start < l.length; start++ {
			if !l.isProtected(start) {
				l.setCell(start, fill)
			}
		}
		return
	}
	if start != 0 && l.getWidth(start-1) == 2 {
		l.setCellFromFill(start-1, 0, 1, fill)
	}
	if end < l.length && end > 0 && l.getWidth(end-1) == 2 {
		l.setCellFromFill(end, 0, 1, fill)
	}
	for ; start < end && start < l.length; start++ {
		l.setCell(start, fill)
	}
}

func (l *Line) resize(cols int, fill Cell) {
	if cols == l.length {
		return
	}
	if cols > l.length {
		if cap(l.data) >= cols*cellSize {
			l.data = l.data[:cols*cellSize]
		} else {
			data := make([]uint32, cols*cellSize)
			copy(data, l.data)
			l.data = data
		}
		previous := l.length
		l.length = cols
		for i := previous; i < cols; i++ {
			l.setCell(i, fill)
		}
	} else {
		l.data = l.data[:cols*cellSize]
		for key := range l.combined {
			if key >= cols {
				delete(l.combined, key)
			}
		}
		for key := range l.extendedAttrs {
			if key >= cols {
				delete(l.extendedAttrs, key)
			}
		}
	}
	l.length = cols
}

func (l *Line) fill(fill Cell, respectProtect bool) {
	if respectProtect {
		for i := 0; i < l.length; i++ {
			if !l.isProtected(i) {
				l.setCell(i, fill)
			}
		}
		return
	}
	l.combined = nil
	l.extendedAttrs = nil
	l.fillRange(0, l.length, fill)
}

// reset makes a recycled row blank, as copying a fresh blank row over it would.
func (l *Line) reset(cols int, fill Cell, isWrapped bool) {
	if cap(l.data) >= cols*cellSize {
		l.data = l.data[:cols*cellSize]
	} else {
		l.data = make([]uint32, cols*cellSize)
	}
	l.length = cols
	l.combined = nil
	l.extendedAttrs = nil
	l.fillRange(0, cols, fill)
	l.isWrapped = isWrapped
}

func (l *Line) copyFrom(line *Line) {
	if len(l.data) != len(line.data) {
		l.data = make([]uint32, len(line.data))
	}
	copy(l.data, line.data)
	l.length = line.length
	l.combined = cloneMap(line.combined)
	l.extendedAttrs = cloneMap(line.extendedAttrs)
	l.isWrapped = line.isWrapped
}

func (l *Line) clone() *Line {
	return &Line{
		data:          append([]uint32(nil), l.data...),
		combined:      cloneMap(l.combined),
		extendedAttrs: cloneMap(l.extendedAttrs),
		length:        l.length,
		isWrapped:     l.isWrapped,
	}
}

func cloneMap[V any](source map[int]V) map[int]V {
	if len(source) == 0 {
		return nil
	}
	result := make(map[int]V, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func (l *Line) getTrimmedLength() int {
	for i := l.length - 1; i >= 0; i-- {
		if content := l.data[i*cellSize]; content&contentHasContentMask != 0 {
			return i + int(content>>contentWidthShift)
		}
	}
	return 0
}

func (l *Line) copyCellsFrom(src *Line, srcCol, destCol, length int, applyInReverse bool) {
	copyOne := func(cell int) {
		for i := 0; i < cellSize; i++ {
			l.data[(destCol+cell)*cellSize+i] = src.data[(srcCol+cell)*cellSize+i]
		}
		if src.data[(srcCol+cell)*cellSize+2]&bgHasExtended != 0 {
			l.setExtended(destCol+cell, src.extendedAttrs[srcCol+cell])
		}
	}
	if applyInReverse {
		for cell := length - 1; cell >= 0; cell-- {
			copyOne(cell)
		}
	} else {
		for cell := 0; cell < length; cell++ {
			copyOne(cell)
		}
	}
	if len(src.combined) == 0 {
		return
	}
	combined := cloneMap(src.combined)
	if l.combined == nil {
		l.combined = map[int]string{}
	}
	for key, value := range combined {
		if key >= srcCol {
			l.combined[key-srcCol+destCol] = value
		}
	}
}

// TranslateToString is the row as text; trimRight drops the cells nothing was printed in.
func (l *Line) TranslateToString(trimRight bool) string {
	end := l.length
	if trimRight {
		end = min(end, l.getTrimmedLength())
	}
	var result strings.Builder
	for col := 0; col < end; {
		content := l.data[col*cellSize]
		switch {
		case content&contentIsCombinedMask != 0:
			result.WriteString(l.combined[col])
		case content&contentCodepointMask != 0:
			result.WriteRune(rune(content & contentCodepointMask))
		default:
			result.WriteByte(' ')
		}
		width := int(content >> contentWidthShift)
		if width == 0 {
			width = 1
		}
		col += width
	}
	return result.String()
}
