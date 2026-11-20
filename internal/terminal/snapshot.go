package terminal

import (
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/terminal/headless"
)

const maxSnapshotSize = 2 * 1024 * 1024

func style(cell *headless.Cell) string {
	var b strings.Builder
	b.WriteString("\x1b[0")
	code := func(value int) {
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(value))
	}
	if cell.IsBold() {
		code(1)
	}
	if cell.IsDim() {
		code(2)
	}
	if cell.IsItalic() {
		code(3)
	}
	if cell.IsUnderline() {
		code(4)
	}
	if cell.IsBlink() {
		code(5)
	}
	if cell.IsInverse() {
		code(7)
	}
	if cell.IsInvisible() {
		code(8)
	}
	if cell.IsStrikethrough() {
		code(9)
	}
	if cell.IsOverline() {
		code(53)
	}
	for _, foreground := range []bool{true, false} {
		color, rgb, palette, extended := cell.FgColor(), cell.IsFgRGB(), cell.IsFgPalette(), 38
		if !foreground {
			color, rgb, palette, extended = cell.BgColor(), cell.IsBgRGB(), cell.IsBgPalette(), 48
		}
		if rgb {
			code(extended)
			code(2)
			code(color >> 16 & 255)
			code(color >> 8 & 255)
			code(color & 255)
		} else if palette {
			code(extended)
			code(5)
			code(color)
		}
	}
	b.WriteByte('m')
	return b.String()
}

// flags packs the nine attributes style renders, so a run of identical cells is found without allocating.
func flags(cell *headless.Cell) int {
	result := 0
	for i, set := range []bool{
		cell.IsBold(), cell.IsDim(), cell.IsItalic(), cell.IsUnderline(), cell.IsBlink(),
		cell.IsInverse(), cell.IsInvisible(), cell.IsStrikethrough(), cell.IsOverline(),
	} {
		if set {
			result |= 1 << i
		}
	}
	return result
}

func cursor(buffer *headless.Buffer, cols int) string {
	col := min(buffer.CursorX(), cols-1)
	position := "\x1b[" + strconv.Itoa(buffer.CursorY()+1) + ";"
	if buffer.CursorX() < cols {
		return position + strconv.Itoa(col+1) + "H"
	}
	line := buffer.Line(buffer.BaseY() + buffer.CursorY())
	var cell headless.Cell
	if line != nil && line.Cell(col, &cell) && cell.Width() == 0 {
		col--
	}
	position += strconv.Itoa(col+1) + "H"
	if line == nil || !line.Cell(col, &cell) {
		return position
	}
	chars := cell.Chars()
	if chars == "" {
		chars = " "
	}
	return position + style(&cell) + chars + "\x1b[0m"
}

type snapshotLine struct {
	data    string
	wrapped bool
}

// serializeBuffer reconstructs cells and scrollback from the resolved screen, keeping the newest rows within budget.
func serializeBuffer(buffer *headless.Buffer, cols int, budget int) string {
	var lines []snapshotLine
	var cell headless.Cell
	size := 0
	var data strings.Builder
	for row := buffer.Length() - 1; row >= 0; row-- {
		line := buffer.Line(row)
		if line == nil {
			continue
		}
		width := cols
		if next := buffer.RingLine(row + 1); next == nil || !next.IsWrapped() {
			for width > 0 && line.Cell(width-1, &cell) && cell.Chars() == "" && cell.IsAttributeDefault() {
				width--
			}
			if width < cols && line.Cell(width, &cell) && cell.Width() == 0 {
				width++
			}
		}
		data.Reset()
		previousStyle := ""
		previousFgMode, previousFgColor, previousBgMode, previousBgColor, previousFlags := ^uint32(0), -2, ^uint32(0), -2, -1
		for col := 0; col < width; col++ {
			if !line.Cell(col, &cell) || cell.Width() == 0 {
				continue
			}
			fgMode, fgColor := cell.FgColorMode(), cell.FgColor()
			bgMode, bgColor := cell.BgColorMode(), cell.BgColor()
			cellFlags := flags(&cell)
			if fgMode != previousFgMode || fgColor != previousFgColor || bgMode != previousBgMode ||
				bgColor != previousBgColor || cellFlags != previousFlags {
				attributes := style(&cell)
				if attributes != previousStyle {
					data.WriteString(attributes)
				}
				previousStyle = attributes
				previousFgMode, previousFgColor = fgMode, fgColor
				previousBgMode, previousBgColor = bgMode, bgColor
				previousFlags = cellFlags
			}
			if chars := cell.Chars(); chars != "" {
				data.WriteString(chars)
			} else {
				data.WriteByte(' ')
			}
		}
		text := data.String()
		length := utf16Len(text)
		if size+length > budget {
			break
		}
		lines = append(lines, snapshotLine{data: text, wrapped: line.IsWrapped()})
		size += length + 2
	}
	var result strings.Builder
	result.Grow(size + 64)
	for row := len(lines) - 1; row >= 0; row-- {
		if row != len(lines)-1 && !lines[row].wrapped {
			result.WriteString("\r\n")
		}
		result.WriteString(lines[row].data)
	}
	result.WriteString("\x1b[0m")
	result.WriteString(cursor(buffer, cols))
	return result.String()
}

// serializeTerminal is what a reload restores: both buffers, then the modes, before live output
// resumes at the snapshot's sequence.
func serializeTerminal(screen *headless.Terminal) string {
	alternate := screen.IsAlternate()
	budget := maxSnapshotSize
	if alternate {
		budget = maxSnapshotSize / 2
	}
	var data strings.Builder
	data.WriteString("\x1bc")
	data.WriteString(serializeBuffer(screen.Normal(), screen.Cols(), budget))
	if alternate {
		data.WriteString("\x1b[?1049h\x1b[H")
		data.WriteString(serializeBuffer(screen.Alt(), screen.Cols(), maxSnapshotSize/2))
	}
	modes := screen.Modes()
	for _, mode := range []struct {
		code    int
		enabled bool
	}{
		{1, modes.ApplicationCursorKeys}, {6, modes.Origin}, {7, modes.Wraparound},
		{45, modes.ReverseWraparound}, {66, modes.ApplicationKeypad},
		{1004, modes.SendFocus}, {2004, modes.BracketedPaste},
	} {
		data.WriteString("\x1b[?" + strconv.Itoa(mode.code))
		if mode.enabled {
			data.WriteByte('h')
		} else {
			data.WriteByte('l')
		}
	}
	if modes.Insert {
		data.WriteString("\x1b[4h")
	} else {
		data.WriteString("\x1b[4l")
	}
	mouse := map[headless.MouseTracking]int{headless.MouseX10: 9, headless.MouseVT200: 1000, headless.MouseDrag: 1002, headless.MouseAny: 1003}[modes.MouseTracking]
	if mouse != 0 {
		data.WriteString("\x1b[?" + strconv.Itoa(mouse) + "h")
	}
	data.WriteString(cursor(screen.Buffer(), screen.Cols()))
	return data.String()
}
