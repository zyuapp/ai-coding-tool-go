package headless

import "strings"

// A port of xterm.js's InputHandler, without the handlers that only answer queries.

func ident(prefix byte, intermediates string, final byte) int {
	result := int(prefix)
	for i := 0; i < len(intermediates); i++ {
		result = result<<8 | int(intermediates[i])
	}
	return result<<8 | int(final)
}

var (
	csiInsertChars       = ident(0, "", '@')
	csiScrollLeft        = ident(0, " ", '@')
	csiCursorUp          = ident(0, "", 'A')
	csiScrollRight       = ident(0, " ", 'A')
	csiCursorDown        = ident(0, "", 'B')
	csiCursorForward     = ident(0, "", 'C')
	csiCursorBackward    = ident(0, "", 'D')
	csiCursorNextLine    = ident(0, "", 'E')
	csiCursorPrecLine    = ident(0, "", 'F')
	csiCursorCharAbs     = ident(0, "", 'G')
	csiCursorPosition    = ident(0, "", 'H')
	csiCursorForwardTab  = ident(0, "", 'I')
	csiEraseInDisplay    = ident(0, "", 'J')
	csiEraseInDisplayP   = ident('?', "", 'J')
	csiEraseInLine       = ident(0, "", 'K')
	csiEraseInLineP      = ident('?', "", 'K')
	csiInsertLines       = ident(0, "", 'L')
	csiDeleteLines       = ident(0, "", 'M')
	csiDeleteChars       = ident(0, "", 'P')
	csiScrollUp          = ident(0, "", 'S')
	csiScrollDown        = ident(0, "", 'T')
	csiEraseChars        = ident(0, "", 'X')
	csiCursorBackwardTab = ident(0, "", 'Z')
	csiCharPosAbsolute   = ident(0, "", '`')
	csiHPosRelative      = ident(0, "", 'a')
	csiRepeatPreceding   = ident(0, "", 'b')
	csiLinePosAbsolute   = ident(0, "", 'd')
	csiVPosRelative      = ident(0, "", 'e')
	csiHVPosition        = ident(0, "", 'f')
	csiTabClear          = ident(0, "", 'g')
	csiSetMode           = ident(0, "", 'h')
	csiSetModePrivate    = ident('?', "", 'h')
	csiResetMode         = ident(0, "", 'l')
	csiResetModePrivate  = ident('?', "", 'l')
	csiCharAttributes    = ident(0, "", 'm')
	csiSoftReset         = ident(0, "!", 'p')
	csiSetScrollRegion   = ident(0, "", 'r')
	csiSaveCursor        = ident(0, "", 's')
	csiRestoreCursor     = ident(0, "", 'u')
	csiInsertColumns     = ident(0, "'", '}')
	csiDeleteColumns     = ident(0, "'", '~')
	csiSelectProtected   = ident(0, "\"", 'q')
)

func (t *Terminal) csiDispatch(id int, p *params) {
	switch id {
	case csiInsertChars:
		t.insertChars(p)
	case csiScrollLeft:
		t.scrollHorizontally(p, false)
	case csiCursorUp:
		t.cursorUp(p)
	case csiScrollRight:
		t.scrollHorizontally(p, true)
	case csiCursorDown:
		t.cursorDown(p)
	case csiCursorForward, csiHPosRelative:
		t.moveCursor(paramOr1(p, 0), 0)
	case csiCursorBackward:
		t.moveCursor(-paramOr1(p, 0), 0)
	case csiCursorNextLine:
		t.cursorDown(p)
		t.active.x = 0
	case csiCursorPrecLine:
		t.cursorUp(p)
		t.active.x = 0
	case csiCursorCharAbs, csiCharPosAbsolute:
		t.setCursor(paramOr1(p, 0)-1, t.active.y)
	case csiCursorPosition, csiHVPosition:
		t.cursorPosition(p)
	case csiCursorForwardTab:
		t.cursorTab(p, true)
	case csiEraseInDisplay:
		t.eraseInDisplay(p, false)
	case csiEraseInDisplayP:
		t.eraseInDisplay(p, true)
	case csiEraseInLine:
		t.eraseInLine(p, false)
	case csiEraseInLineP:
		t.eraseInLine(p, true)
	case csiInsertLines:
		t.insertLines(p)
	case csiDeleteLines:
		t.deleteLines(p)
	case csiDeleteChars:
		t.deleteChars(p)
	case csiScrollUp:
		t.scrollUp(p)
	case csiScrollDown:
		t.scrollDown(p)
	case csiEraseChars:
		t.eraseChars(p)
	case csiCursorBackwardTab:
		t.cursorTab(p, false)
	case csiRepeatPreceding:
		t.repeatPrecedingCharacter(p)
	case csiLinePosAbsolute:
		t.setCursor(t.active.x, paramOr1(p, 0)-1)
	case csiVPosRelative:
		t.moveCursor(0, paramOr1(p, 0))
	case csiTabClear:
		t.tabClear(p)
	case csiSetMode:
		t.setMode(p, true)
	case csiSetModePrivate:
		t.setModePrivate(p, true)
	case csiResetMode:
		t.setMode(p, false)
	case csiResetModePrivate:
		t.setModePrivate(p, false)
	case csiCharAttributes:
		t.charAttributes(p)
	case csiSoftReset:
		t.softReset()
	case csiSetScrollRegion:
		t.setScrollRegion(p)
	case csiSaveCursor:
		t.saveCursor()
	case csiRestoreCursor:
		t.restoreCursor()
	case csiInsertColumns:
		t.editColumns(p, true)
	case csiDeleteColumns:
		t.editColumns(p, false)
	case csiSelectProtected:
		t.selectProtected(p)
	}
}

func paramOr1(p *params, index int) int {
	if value := p.at(index); value != 0 {
		return value
	}
	return 1
}

func (t *Terminal) execute(code uint32) {
	switch code {
	case 0x0a, 0x0b, 0x0c:
		t.lineFeed()
	case 0x0d:
		t.active.x = 0
	case 0x08:
		t.backspace()
	case 0x09:
		t.tab()
	case 0x0e:
		t.setgLevel(1)
	case 0x0f:
		t.setgLevel(0)
	case 0x84:
		t.index()
	case 0x85:
		t.nextLine()
	case 0x88:
		t.active.tabs[t.active.x] = true
	}
}

func (t *Terminal) escDispatch(id int) {
	if id>>8 == 0 {
		switch byte(id) {
		case '7':
			t.saveCursor()
		case '8':
			t.restoreCursor()
		case 'D':
			t.index()
		case 'E':
			t.nextLine()
		case 'H':
			t.active.tabs[t.active.x] = true
		case 'M':
			t.reverseIndex()
		case '=':
			t.modes.ApplicationKeypad = true
		case '>':
			t.modes.ApplicationKeypad = false
		case 'c':
			t.parser.reset()
			t.reset()
		case 'n':
			t.setgLevel(2)
		case 'o', '|':
			t.setgLevel(3)
		case '}':
			t.setgLevel(2)
		case '~':
			t.setgLevel(1)
		}
		return
	}
	if id>>16 != 0 {
		return
	}
	intermediate, final := byte(id>>8), byte(id)
	switch {
	case intermediate == '%' && (final == '@' || final == 'G'):
		t.selectDefaultCharset()
	case intermediate == '#' && final == '8':
		t.screenAlignmentPattern()
	case strings.IndexByte("()*+-./", intermediate) >= 0:
		set, known := charsets[final]
		if !known || intermediate == '/' {
			return
		}
		t.setgCharset(gLevels[intermediate], set)
	}
}

func (t *Terminal) oscDispatch(id int, data string) {
	switch id {
	case 0, 2:
		if t.onTitleChange != nil {
			t.onTitleChange(data)
		}
	case 8:
		t.setHyperlink(data)
	}
}

func (t *Terminal) setgLevel(level int) {
	t.glevel = level
	t.charset = t.charsets[level]
}

func (t *Terminal) setgCharset(level int, set *charset) {
	t.charsets[level] = set
	if t.glevel == level {
		t.charset = set
	}
}

func (t *Terminal) selectDefaultCharset() {
	t.setgLevel(0)
	t.setgCharset(0, nil)
}

func (t *Terminal) print(data []uint32, start, end int) {
	buffer := t.active
	cols := t.cols
	attr := t.curAttr
	row := buffer.lines.get(buffer.ybase + buffer.y)
	if buffer.x != 0 && end-start > 0 && row.getWidth(buffer.x-1) == 2 {
		row.setCellFromAttr(buffer.x-1, 0, 1, attr)
	}
	preceding := t.parser.precedingJoinState
	for pos := start; pos < end; pos++ {
		code := data[pos]
		if code < 127 && t.charset != nil {
			if mapped, ok := (*t.charset)[code]; ok {
				code = mapped
			}
		}
		info := charProperties(code, preceding)
		chWidth := extractWidth(info)
		shouldJoin := extractShouldJoin(info)
		oldWidth := 0
		if shouldJoin {
			oldWidth = extractWidth(preceding)
		}
		preceding = info
		if buffer.x+chWidth-oldWidth > cols {
			if t.modes.Wraparound {
				oldRow := row
				oldCol := buffer.x - oldWidth
				buffer.x = oldWidth
				buffer.y++
				if buffer.y == buffer.scrollBottom+1 {
					buffer.y--
					t.scroll(t.eraseAttr(), true)
				} else {
					if buffer.y >= t.rows {
						buffer.y = t.rows - 1
					}
					buffer.lines.get(buffer.ybase + buffer.y).isWrapped = true
				}
				row = buffer.lines.get(buffer.ybase + buffer.y)
				if oldWidth > 0 {
					row.copyCellsFrom(oldRow, oldCol, 0, oldWidth, false)
				}
				for ; oldCol < cols; oldCol++ {
					oldRow.setCellFromAttr(oldCol, 0, 1, attr)
				}
			} else {
				buffer.x = cols - 1
				if chWidth == 2 {
					continue
				}
			}
		}
		if shouldJoin && buffer.x != 0 {
			offset := 2
			if row.getWidth(buffer.x-1) != 0 {
				offset = 1
			}
			row.addCodepointToCell(buffer.x-offset, code, chWidth)
			for delta := chWidth - oldWidth; delta > 0; delta-- {
				row.setCellFromAttr(buffer.x, 0, 0, attr)
				buffer.x++
			}
			continue
		}
		if t.modes.Insert {
			row.insertCells(buffer.x, chWidth-oldWidth, buffer.nullCell(attr))
			if row.getWidth(cols-1) == 2 {
				row.setCellFromAttr(cols-1, nullCellCode, nullCellWidth, attr)
			}
		}
		row.setCellFromAttr(buffer.x, code, chWidth, attr)
		buffer.x++
		if chWidth > 0 {
			for chWidth--; chWidth > 0; chWidth-- {
				row.setCellFromAttr(buffer.x, 0, 0, attr)
				buffer.x++
			}
		}
	}
	t.parser.precedingJoinState = preceding
	if buffer.x < cols && end-start > 0 && row.getWidth(buffer.x) == 0 && !row.hasContent(buffer.x) {
		row.setCellFromAttr(buffer.x, 0, 1, attr)
	}
}

func (t *Terminal) scroll(eraseAttr attributeData, isWrapped bool) {
	buffer := t.active
	topRow := buffer.ybase + buffer.scrollTop
	bottomRow := buffer.ybase + buffer.scrollBottom
	if buffer.scrollTop == 0 {
		willBufferBeTrimmed := buffer.lines.isFull()
		if bottomRow == buffer.lines.length-1 {
			if willBufferBeTrimmed {
				buffer.lines.recycle().reset(t.cols, nullCell(eraseAttr), isWrapped)
			} else {
				buffer.lines.push(buffer.blankLine(eraseAttr, isWrapped))
			}
		} else {
			newLine := buffer.blankLine(eraseAttr, isWrapped)
			buffer.lines.splice(bottomRow+1, 0, newLine)
		}
		if !willBufferBeTrimmed {
			buffer.ybase++
		}
	} else {
		scrollRegionHeight := bottomRow - topRow + 1
		buffer.lines.shiftElements(topRow+1, scrollRegionHeight-1, -1)
		buffer.lines.set(bottomRow, buffer.blankLine(eraseAttr, isWrapped))
	}
}

func (t *Terminal) lineFeed() {
	buffer := t.active
	if t.convertEol {
		buffer.x = 0
	}
	buffer.y++
	if buffer.y == buffer.scrollBottom+1 {
		buffer.y--
		t.scroll(t.eraseAttr(), false)
	} else if buffer.y >= t.rows {
		buffer.y = t.rows - 1
	} else {
		buffer.lines.get(buffer.ybase + buffer.y).isWrapped = false
	}
	if buffer.x >= t.cols {
		buffer.x--
	}
}

func (t *Terminal) backspace() {
	buffer := t.active
	if !t.modes.ReverseWraparound {
		t.restrictCursor(t.cols - 1)
		if buffer.x > 0 {
			buffer.x--
		}
		return
	}
	t.restrictCursor(t.cols)
	if buffer.x > 0 {
		buffer.x--
	} else if buffer.y > buffer.scrollTop && buffer.y <= buffer.scrollBottom {
		if line := buffer.lines.get(buffer.ybase + buffer.y); line != nil && line.isWrapped {
			line.isWrapped = false
			buffer.y--
			buffer.x = t.cols - 1
			previous := buffer.lines.get(buffer.ybase + buffer.y)
			if previous.hasWidth(buffer.x) && !previous.hasContent(buffer.x) {
				buffer.x--
			}
		}
	}
	t.restrictCursor(t.cols - 1)
}

func (t *Terminal) tab() {
	if t.active.x >= t.cols {
		return
	}
	t.active.x = t.active.nextStop(t.active.x)
}

func (t *Terminal) restrictCursor(maxCol int) {
	buffer := t.active
	buffer.x = min(maxCol, max(0, buffer.x))
	if t.modes.Origin {
		buffer.y = min(buffer.scrollBottom, max(buffer.scrollTop, buffer.y))
	} else {
		buffer.y = min(t.rows-1, max(0, buffer.y))
	}
}

func (t *Terminal) setCursor(x, y int) {
	buffer := t.active
	buffer.x = x
	if t.modes.Origin {
		buffer.y = buffer.scrollTop + y
	} else {
		buffer.y = y
	}
	t.restrictCursor(t.cols - 1)
}

func (t *Terminal) moveCursor(x, y int) {
	t.restrictCursor(t.cols - 1)
	t.setCursor(t.active.x+x, t.active.y+y)
}

func (t *Terminal) cursorUp(p *params) {
	diffToTop := t.active.y - t.active.scrollTop
	if diffToTop >= 0 {
		t.moveCursor(0, -min(diffToTop, paramOr1(p, 0)))
	} else {
		t.moveCursor(0, -paramOr1(p, 0))
	}
}

func (t *Terminal) cursorDown(p *params) {
	diffToBottom := t.active.scrollBottom - t.active.y
	if diffToBottom >= 0 {
		t.moveCursor(0, min(diffToBottom, paramOr1(p, 0)))
	} else {
		t.moveCursor(0, paramOr1(p, 0))
	}
}

func (t *Terminal) cursorPosition(p *params) {
	x := 0
	if p.length >= 2 {
		x = paramOr1(p, 1) - 1
	}
	t.setCursor(x, paramOr1(p, 0)-1)
}

func (t *Terminal) cursorTab(p *params, forward bool) {
	if t.active.x >= t.cols {
		return
	}
	for count := paramOr1(p, 0); count > 0; count-- {
		if forward {
			t.active.x = t.active.nextStop(t.active.x)
		} else {
			t.active.x = t.active.prevStop(t.active.x)
		}
	}
}

func (t *Terminal) tabClear(p *params) {
	switch p.at(0) {
	case 0:
		delete(t.active.tabs, t.active.x)
	case 3:
		t.active.tabs = map[int]bool{}
	}
}

func (t *Terminal) selectProtected(p *params) {
	switch p.at(0) {
	case 1:
		t.curAttr.bg |= bgProtected
	case 2, 0:
		t.curAttr.bg &^= bgProtected
	}
}

// eraseAttr keeps only the current background colour, as erasing does in xterm.
func (t *Terminal) eraseAttr() attributeData {
	return attributeData{bg: t.curAttr.bg &^ 0xFC000000}
}

func (t *Terminal) eraseInBufferLine(y, start, end int, clearWrap, respectProtect bool) {
	line := t.active.lines.get(t.active.ybase + y)
	line.replaceCells(start, end, nullCell(t.eraseAttr()), respectProtect)
	if clearWrap {
		line.isWrapped = false
	}
}

func (t *Terminal) resetBufferLine(y int, respectProtect bool) {
	if line := t.active.lines.get(t.active.ybase + y); line != nil {
		line.fill(nullCell(t.eraseAttr()), respectProtect)
		line.isWrapped = false
	}
}

func (t *Terminal) eraseInDisplay(p *params, respectProtect bool) {
	t.restrictCursor(t.cols)
	buffer := t.active
	switch p.at(0) {
	case 0:
		j := buffer.y
		t.eraseInBufferLine(j, buffer.x, t.cols, buffer.x == 0, respectProtect)
		for j++; j < t.rows; j++ {
			t.resetBufferLine(j, respectProtect)
		}
	case 1:
		j := buffer.y
		t.eraseInBufferLine(j, 0, buffer.x+1, true, respectProtect)
		if buffer.x+1 >= t.cols {
			// xterm.js indexes this line without the scrollback offset.
			if line := buffer.lines.get(j + 1); line != nil {
				line.isWrapped = false
			}
		}
		for j--; j >= 0; j-- {
			t.resetBufferLine(j, respectProtect)
		}
	case 2:
		for j := t.rows - 1; j >= 0; j-- {
			t.resetBufferLine(j, respectProtect)
		}
	case 3:
		if scrollBackSize := buffer.lines.length - t.rows; scrollBackSize > 0 {
			buffer.lines.trimStart(scrollBackSize)
			buffer.ybase = max(buffer.ybase-scrollBackSize, 0)
		}
	}
}

func (t *Terminal) eraseInLine(p *params, respectProtect bool) {
	t.restrictCursor(t.cols)
	buffer := t.active
	switch p.at(0) {
	case 0:
		t.eraseInBufferLine(buffer.y, buffer.x, t.cols, buffer.x == 0, respectProtect)
	case 1:
		t.eraseInBufferLine(buffer.y, 0, buffer.x+1, false, respectProtect)
	case 2:
		t.eraseInBufferLine(buffer.y, 0, t.cols, true, respectProtect)
	}
}

func (t *Terminal) insertLines(p *params) {
	t.restrictCursor(t.cols - 1)
	buffer := t.active
	if buffer.y > buffer.scrollBottom || buffer.y < buffer.scrollTop {
		return
	}
	row := buffer.ybase + buffer.y
	scrollBottomRowsOffset := t.rows - 1 - buffer.scrollBottom
	scrollBottomAbsolute := t.rows - 1 + buffer.ybase - scrollBottomRowsOffset + 1
	for count := paramOr1(p, 0); count > 0; count-- {
		buffer.lines.splice(scrollBottomAbsolute-1, 1)
		buffer.lines.splice(row, 0, buffer.blankLine(t.eraseAttr(), false))
	}
	buffer.x = 0
}

func (t *Terminal) deleteLines(p *params) {
	t.restrictCursor(t.cols - 1)
	buffer := t.active
	if buffer.y > buffer.scrollBottom || buffer.y < buffer.scrollTop {
		return
	}
	row := buffer.ybase + buffer.y
	j := t.rows - 1 + buffer.ybase - (t.rows - 1 - buffer.scrollBottom)
	for count := paramOr1(p, 0); count > 0; count-- {
		buffer.lines.splice(row, 1)
		buffer.lines.splice(j, 0, buffer.blankLine(t.eraseAttr(), false))
	}
	buffer.x = 0
}

func (t *Terminal) insertChars(p *params) {
	t.restrictCursor(t.cols - 1)
	if line := t.active.lines.get(t.active.ybase + t.active.y); line != nil {
		line.insertCells(t.active.x, paramOr1(p, 0), nullCell(t.eraseAttr()))
	}
}

func (t *Terminal) deleteChars(p *params) {
	t.restrictCursor(t.cols - 1)
	if line := t.active.lines.get(t.active.ybase + t.active.y); line != nil {
		line.deleteCells(t.active.x, paramOr1(p, 0), nullCell(t.eraseAttr()))
	}
}

func (t *Terminal) eraseChars(p *params) {
	t.restrictCursor(t.cols - 1)
	if line := t.active.lines.get(t.active.ybase + t.active.y); line != nil {
		line.replaceCells(t.active.x, t.active.x+paramOr1(p, 0), nullCell(t.eraseAttr()), false)
	}
}

func (t *Terminal) scrollUp(p *params) {
	buffer := t.active
	for count := paramOr1(p, 0); count > 0; count-- {
		buffer.lines.splice(buffer.ybase+buffer.scrollTop, 1)
		buffer.lines.splice(buffer.ybase+buffer.scrollBottom, 0, buffer.blankLine(t.eraseAttr(), false))
	}
}

func (t *Terminal) scrollDown(p *params) {
	buffer := t.active
	for count := paramOr1(p, 0); count > 0; count-- {
		buffer.lines.splice(buffer.ybase+buffer.scrollBottom, 1)
		buffer.lines.splice(buffer.ybase+buffer.scrollTop, 0, buffer.blankLine(attributeData{}, false))
	}
}

func (t *Terminal) scrollHorizontally(p *params, right bool) {
	buffer := t.active
	if buffer.y > buffer.scrollBottom || buffer.y < buffer.scrollTop {
		return
	}
	count := paramOr1(p, 0)
	for y := buffer.scrollTop; y <= buffer.scrollBottom; y++ {
		line := buffer.lines.get(buffer.ybase + y)
		if right {
			line.insertCells(0, count, nullCell(t.eraseAttr()))
		} else {
			line.deleteCells(0, count, nullCell(t.eraseAttr()))
		}
		line.isWrapped = false
	}
}

func (t *Terminal) editColumns(p *params, insert bool) {
	buffer := t.active
	if buffer.y > buffer.scrollBottom || buffer.y < buffer.scrollTop {
		return
	}
	count := paramOr1(p, 0)
	for y := buffer.scrollTop; y <= buffer.scrollBottom; y++ {
		line := buffer.lines.get(buffer.ybase + y)
		if insert {
			line.insertCells(buffer.x, count, nullCell(t.eraseAttr()))
		} else {
			line.deleteCells(buffer.x, count, nullCell(t.eraseAttr()))
		}
		line.isWrapped = false
	}
}

func (t *Terminal) repeatPrecedingCharacter(p *params) {
	joinState := t.parser.precedingJoinState
	if joinState == 0 {
		return
	}
	length := paramOr1(p, 0)
	buffer := t.active
	x := buffer.x - extractWidth(joinState)
	row := buffer.lines.get(buffer.ybase + buffer.y)
	if x < 0 || x >= row.length {
		return
	}
	var codes []uint32
	for _, r := range row.getString(x) {
		codes = append(codes, uint32(r))
	}
	data := make([]uint32, 0, len(codes)*length)
	for i := 0; i < length; i++ {
		data = append(data, codes...)
	}
	t.print(data, 0, len(data))
}

func (t *Terminal) setMode(p *params, on bool) {
	for i := 0; i < p.length; i++ {
		switch p.values[i] {
		case 4:
			t.modes.Insert = on
		case 20:
			t.convertEol = on
		}
	}
}

func (t *Terminal) setModePrivate(p *params, on bool) {
	for i := 0; i < p.length; i++ {
		switch value := p.values[i]; value {
		case 1:
			t.modes.ApplicationCursorKeys = on
		case 2:
			if on {
				for level := range t.charsets {
					t.setgCharset(level, nil)
				}
			}
		case 6:
			t.modes.Origin = on
			t.setCursor(0, 0)
		case 7:
			t.modes.Wraparound = on
		case 45:
			t.modes.ReverseWraparound = on
		case 66:
			t.modes.ApplicationKeypad = on
		case 9:
			t.setMouse(on, MouseX10)
		case 1000:
			t.setMouse(on, MouseVT200)
		case 1002:
			t.setMouse(on, MouseDrag)
		case 1003:
			t.setMouse(on, MouseAny)
		case 1004:
			t.modes.SendFocus = on
		case 1048:
			if on {
				t.saveCursor()
			} else {
				t.restoreCursor()
			}
		case 1049, 47, 1047:
			if on {
				if value == 1049 {
					t.saveCursor()
				}
				t.activateAltBuffer()
			} else {
				t.activateNormalBuffer()
				if value == 1049 {
					t.restoreCursor()
				}
			}
		case 2004:
			t.modes.BracketedPaste = on
		case 2026:
			t.modes.SynchronizedOutput = on
		}
	}
}

func (t *Terminal) setMouse(on bool, protocol MouseTracking) {
	if on {
		t.modes.MouseTracking = protocol
	} else {
		t.modes.MouseTracking = MouseNone
	}
}

func (t *Terminal) activateAltBuffer() {
	if t.active == t.alt {
		return
	}
	t.alt.fillViewportRows(t.eraseAttr())
	t.alt.x = t.normal.x
	t.alt.y = t.normal.y
	t.active = t.alt
}

func (t *Terminal) activateNormalBuffer() {
	if t.active == t.normal {
		return
	}
	t.normal.x = t.alt.x
	t.normal.y = t.alt.y
	t.alt.clear()
	t.active = t.normal
}

func (t *Terminal) updateAttrColor(color uint32, mode int32, c1, c2, c3 int32) uint32 {
	switch mode {
	case 2:
		color |= attrCMRGB
		color &^= attrRGBMask
		color |= uint32(c1&255)<<16 | uint32(c2&255)<<8 | uint32(c3&255)
	case 5:
		color &^= attrCMMask | attrPColorMask
		color |= attrCMP256 | uint32(c1&0xff)
	}
	return color
}

func (t *Terminal) extractColor(p *params, pos int, attr *attributeData) int {
	accu := [6]int32{0, 0, -1, 0, 0, 0}
	cSpace := 0
	advance := 0
	for {
		accu[advance+cSpace] = p.values[pos+advance]
		if p.hasSubParams(pos + advance) {
			subparams := p.getSubParams(pos + advance)
			for i := 0; ; {
				if accu[1] == 5 {
					cSpace = 1
				}
				if index := advance + i + 1 + cSpace; index < len(accu) {
					accu[index] = subparams[i]
				}
				i++
				if i >= len(subparams) || i+advance+1+cSpace >= len(accu) {
					break
				}
			}
			break
		}
		if accu[1] == 5 && advance+cSpace >= 2 || accu[1] == 2 && advance+cSpace >= 5 {
			break
		}
		if accu[1] != 0 {
			cSpace = 1
		}
		advance++
		if advance+pos >= p.length || advance+cSpace >= len(accu) {
			break
		}
	}
	for i := 2; i < len(accu); i++ {
		if accu[i] == -1 {
			accu[i] = 0
		}
	}
	switch accu[0] {
	case 38:
		attr.fg = t.updateAttrColor(attr.fg, accu[1], accu[3], accu[4], accu[5])
	case 48:
		attr.bg = t.updateAttrColor(attr.bg, accu[1], accu[3], accu[4], accu[5])
	case 58:
		attr.extended.setUnderlineColor(t.updateAttrColor(attr.extended.underlineColor(), accu[1], accu[3], accu[4], accu[5]))
	}
	return advance
}

func processUnderline(style int32, attr *attributeData) {
	if style == -1 || style > 5 {
		style = 1
	}
	attr.extended.setUnderlineStyle(uint32(style))
	attr.fg |= fgUnderline
	if style == 0 {
		attr.fg &^= fgUnderline
	}
	attr.updateExtended()
}

func processSGR0(attr *attributeData) {
	attr.fg = 0
	attr.bg = 0
	attr.extended.setUnderlineStyle(underlineNone)
	attr.extended.setUnderlineColor(attr.extended.underlineColor() &^ (attrCMMask | attrRGBMask))
	attr.updateExtended()
}

func (t *Terminal) charAttributes(p *params) {
	attr := &t.curAttr
	if p.length == 1 && p.values[0] == 0 {
		processSGR0(attr)
		return
	}
	for i := 0; i < p.length; i++ {
		value := p.values[i]
		switch {
		case value >= 30 && value <= 37:
			attr.fg &^= attrCMMask | attrPColorMask
			attr.fg |= attrCMP16 | uint32(value-30)
		case value >= 40 && value <= 47:
			attr.bg &^= attrCMMask | attrPColorMask
			attr.bg |= attrCMP16 | uint32(value-40)
		case value >= 90 && value <= 97:
			attr.fg &^= attrCMMask | attrPColorMask
			attr.fg |= attrCMP16 | uint32(value-90) | 8
		case value >= 100 && value <= 107:
			attr.bg &^= attrCMMask | attrPColorMask
			attr.bg |= attrCMP16 | uint32(value-100) | 8
		case value == 0:
			processSGR0(attr)
		case value == 1:
			attr.fg |= fgBold
		case value == 3:
			attr.bg |= bgItalic
		case value == 4:
			attr.fg |= fgUnderline
			style := int32(underlineSingle)
			if p.hasSubParams(i) {
				style = p.getSubParams(i)[0]
			}
			processUnderline(style, attr)
		case value == 5:
			attr.fg |= fgBlink
		case value == 7:
			attr.fg |= fgInverse
		case value == 8:
			attr.fg |= fgInvisible
		case value == 9:
			attr.fg |= fgStrikethrough
		case value == 2:
			attr.bg |= bgDim
		case value == 21:
			processUnderline(underlineDouble, attr)
		case value == 22:
			attr.fg &^= fgBold
			attr.bg &^= bgDim
		case value == 23:
			attr.bg &^= bgItalic
		case value == 24:
			attr.fg &^= fgUnderline
			processUnderline(underlineNone, attr)
		case value == 25:
			attr.fg &^= fgBlink
		case value == 27:
			attr.fg &^= fgInverse
		case value == 28:
			attr.fg &^= fgInvisible
		case value == 29:
			attr.fg &^= fgStrikethrough
		case value == 39:
			attr.fg &^= attrCMMask | attrRGBMask
		case value == 49:
			attr.bg &^= attrCMMask | attrRGBMask
		case value == 38 || value == 48 || value == 58:
			i += t.extractColor(p, i, attr)
		case value == 53:
			attr.bg |= bgOverline
		case value == 55:
			attr.bg &^= bgOverline
		case value == 59:
			attr.extended.setUnderlineColor(0xFFFFFFFF)
			attr.updateExtended()
		}
	}
}

func (t *Terminal) softReset() {
	buffer := t.active
	buffer.scrollTop = 0
	buffer.scrollBottom = t.rows - 1
	t.curAttr = attributeData{}
	mouse := t.modes.MouseTracking
	t.modes = Modes{Wraparound: true, MouseTracking: mouse}
	t.charset = nil
	t.charsets = [4]*charset{}
	t.glevel = 0
	buffer.savedX = 0
	buffer.savedY = buffer.ybase
	buffer.savedFg = t.curAttr.fg
	buffer.savedBg = t.curAttr.bg
	buffer.savedCharset = t.charset
}

func (t *Terminal) setScrollRegion(p *params) {
	top := paramOr1(p, 0)
	bottom := 0
	if p.length < 2 {
		bottom = t.rows
	} else if bottom = p.at(1); bottom > t.rows || bottom == 0 {
		bottom = t.rows
	}
	if bottom > top {
		t.active.scrollTop = top - 1
		t.active.scrollBottom = bottom - 1
		t.setCursor(0, 0)
	}
}

func (t *Terminal) saveCursor() {
	buffer := t.active
	buffer.savedX = buffer.x
	buffer.savedY = buffer.ybase + buffer.y
	buffer.savedFg = t.curAttr.fg
	buffer.savedBg = t.curAttr.bg
	buffer.savedCharset = t.charset
}

func (t *Terminal) restoreCursor() {
	buffer := t.active
	buffer.x = buffer.savedX
	buffer.y = max(buffer.savedY-buffer.ybase, 0)
	t.curAttr.fg = buffer.savedFg
	t.curAttr.bg = buffer.savedBg
	t.charset = buffer.savedCharset
	t.restrictCursor(t.cols - 1)
}

func (t *Terminal) setHyperlink(data string) {
	idx := strings.IndexByte(data, ';')
	if idx == -1 {
		return
	}
	id := strings.TrimSpace(data[:idx])
	uri := data[idx+1:]
	if uri != "" {
		if t.curAttr.extended.urlID != 0 {
			t.curAttr.extended.urlID = 0
			t.curAttr.updateExtended()
		}
		linkID := ""
		for _, part := range strings.Split(id, ":") {
			if strings.HasPrefix(part, "id=") {
				linkID = part[3:]
				break
			}
		}
		t.curAttr.extended.urlID = t.registerLink(linkID, uri)
		t.curAttr.updateExtended()
		return
	}
	if strings.TrimSpace(id) != "" {
		return
	}
	t.curAttr.extended.urlID = 0
	t.curAttr.updateExtended()
}

// registerLink numbers links as OscLinkService does: a link with an id keeps its number.
func (t *Terminal) registerLink(id, uri string) uint32 {
	if id != "" {
		key := id + ";;" + uri
		if t.links == nil {
			t.links = map[string]uint32{}
		}
		if number, ok := t.links[key]; ok {
			return number
		}
		t.nextLink++
		t.links[key] = t.nextLink
		return t.nextLink
	}
	t.nextLink++
	return t.nextLink
}

func (t *Terminal) nextLine() {
	t.active.x = 0
	t.index()
}

func (t *Terminal) index() {
	t.restrictCursor(t.cols - 1)
	buffer := t.active
	buffer.y++
	if buffer.y == buffer.scrollBottom+1 {
		buffer.y--
		t.scroll(t.eraseAttr(), false)
	} else if buffer.y >= t.rows {
		buffer.y = t.rows - 1
	}
	t.restrictCursor(t.cols - 1)
}

func (t *Terminal) reverseIndex() {
	t.restrictCursor(t.cols - 1)
	buffer := t.active
	if buffer.y == buffer.scrollTop {
		scrollRegionHeight := buffer.scrollBottom - buffer.scrollTop
		buffer.lines.shiftElements(buffer.ybase+buffer.y, scrollRegionHeight, 1)
		buffer.lines.set(buffer.ybase+buffer.y, buffer.blankLine(t.eraseAttr(), false))
	} else {
		buffer.y--
		t.restrictCursor(t.cols - 1)
	}
}

func (t *Terminal) screenAlignmentPattern() {
	cell := Cell{content: 1<<contentWidthShift | 'E', fg: t.curAttr.fg, bg: t.curAttr.bg}
	t.setCursor(0, 0)
	buffer := t.active
	for yOffset := 0; yOffset < t.rows; yOffset++ {
		// Past the end this reads the ring as xterm.js does, which can wrap to the top row.
		if line := buffer.lines.get(buffer.ybase + buffer.y + yOffset); line != nil {
			line.fill(cell, false)
			line.isWrapped = false
		}
	}
	t.setCursor(0, 0)
}
