package terminal

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/terminal/headless"
)

func screenText(screen *headless.Terminal) []string {
	buffer := screen.Buffer()
	lines := make([]string, buffer.Length())
	for row := range lines {
		lines[row] = buffer.Line(row).TranslateToString(true)
	}
	return lines
}

func TestSnapshotRestoresProgressUnicodeColoursScrollbackAndCursor(t *testing.T) {
	source := headless.New(headless.Options{Cols: 12, Rows: 3, Scrollback: 50})
	target := headless.New(headless.Options{Cols: 12, Rows: 3, Scrollback: 50})
	source.Write("old\r\nprogress 10\rprogress 99\r\n\x1b[31m你好 red\x1b[0m\r\nprompt> ")
	target.Write(serializeTerminal(source))
	if got, want := screenText(target), screenText(source); !slices.Equal(got, want) {
		t.Fatalf("restored %q, want %q", got, want)
	}
	if target.Buffer().CursorX() != source.Buffer().CursorX() || target.Buffer().CursorY() != source.Buffer().CursorY() {
		t.Fatalf("cursor moved")
	}
	var cell headless.Cell
	target.Buffer().Line(source.Buffer().Length()-2).Cell(0, &cell)
	if cell.FgColor() != 1 {
		t.Fatalf("red became colour %d", cell.FgColor())
	}
	source.Write("next")
	target.Write("next")
	if got, want := screenText(target), screenText(source); !slices.Equal(got, want) {
		t.Fatalf("after more output %q, want %q", got, want)
	}
}

func TestAlternateScreenSnapshotKeepsNormalScreenAndModes(t *testing.T) {
	source := headless.New(headless.Options{Cols: 20, Rows: 4, Scrollback: 1000})
	target := headless.New(headless.Options{Cols: 20, Rows: 4, Scrollback: 1000})
	source.Write("shell> \x1b[?1049h\x1b[?2004h\x1b[2;3Heditor")
	target.Write(serializeTerminal(source))
	if got, want := screenText(target), screenText(source); !slices.Equal(got, want) {
		t.Fatalf("restored %q, want %q", got, want)
	}
	if !target.Modes().BracketedPaste {
		t.Fatal("bracketed paste was lost")
	}
	source.Write("!\x1b[?1049lback")
	target.Write("!\x1b[?1049lback")
	if got, want := screenText(target), screenText(source); !slices.Equal(got, want) {
		t.Fatalf("after leaving the alternate screen %q, want %q", got, want)
	}
}

func TestSnapshotAtTheRightMarginKeepsThePendingWrap(t *testing.T) {
	source := headless.New(headless.Options{Cols: 12, Rows: 3, Scrollback: 1000})
	target := headless.New(headless.Options{Cols: 12, Rows: 3, Scrollback: 1000})
	source.Write("123456789012")
	target.Write(serializeTerminal(source))
	source.Write("next")
	target.Write("next")
	if got, want := screenText(target), screenText(source); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSnapshotStaysBoundedWhenStyledScrollbackIsFull(t *testing.T) {
	source := headless.New(headless.Options{Cols: 100, Rows: 24, Scrollback: 5000})
	var line strings.Builder
	for index := range 100 {
		fmt.Fprintf(&line, "\x1b[38;2;%d;0;0mx", index)
	}
	source.Write(strings.Repeat(line.String()+"\r\n", 5000))
	data := serializeTerminal(source)
	if utf16Len(data) >= 2*1024*1024+1000 {
		t.Fatalf("snapshot is %d units", utf16Len(data))
	}
	if !strings.HasSuffix(data, "H") {
		t.Fatal("snapshot does not end by placing the cursor")
	}
}

func TestSnapshotKeepsEveryAttributeWhenAdjacentCellsDifferByOne(t *testing.T) {
	source := headless.New(headless.Options{Cols: 40, Rows: 3, Scrollback: 1000})
	target := headless.New(headless.Options{Cols: 40, Rows: 3, Scrollback: 1000})
	segments := []string{"1", "2", "3", "4", "5", "7", "8", "9", "53", "38;5;12", "38;5;13", "48;5;20", "48;5;21", "38;2;10;20;30", "48;2;40;50;60"}
	var input strings.Builder
	for _, code := range segments {
		input.WriteString("\x1b[" + code + "ma")
	}
	input.WriteString("\x1b[0mz")
	source.Write(input.String())
	target.Write(serializeTerminal(source))
	attributes := func(screen *headless.Terminal, col int) string {
		var cell headless.Cell
		screen.Buffer().Line(0).Cell(col, &cell)
		return fmt.Sprint(cell.IsBold(), cell.IsDim(), cell.IsItalic(), cell.IsUnderline(), cell.IsBlink(),
			cell.IsInverse(), cell.IsInvisible(), cell.IsStrikethrough(), cell.IsOverline(),
			cell.FgColorMode(), cell.FgColor(), cell.BgColorMode(), cell.BgColor())
	}
	for col := 0; col <= len(segments); col++ {
		if got, want := attributes(target, col), attributes(source, col); got != want {
			t.Fatalf("column %d: %s, want %s", col, got, want)
		}
	}
}
