package terminal

import (
	"encoding/json/v2"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/terminal/headless"
)

// Replays generated output through @xterm/headless and src/main/terminal-snapshot.ts in the
// TypeScript checkout, and requires the Go screen and snapshot to match them exactly.

type step struct {
	Write  *string `json:"write,omitzero"`
	Resize []int   `json:"resize,omitzero"`
}

type replayCase struct {
	Cols       int    `json:"cols"`
	Rows       int    `json:"rows"`
	Scrollback int    `json:"scrollback"`
	Steps      []step `json:"steps"`
}

type bufferState struct {
	Length  int      `json:"length"`
	X       int      `json:"x"`
	Y       int      `json:"y"`
	BaseY   int      `json:"baseY"`
	Lines   []string `json:"lines"`
	Wrapped []bool   `json:"wrapped"`
	Cells   []string `json:"cells"`
}

type screenState struct {
	Normal    bufferState `json:"normal"`
	Alternate bool        `json:"alternate"`
	Active    bufferState `json:"active"`
	Titles    []string    `json:"titles"`
	Snapshot  string      `json:"snapshot"`
	Error     string      `json:"error,omitzero"`
}

const replayScript = `
import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
const [repo, input] = process.argv.slice(2);
const require = createRequire(repo + "/package.json");
const { Terminal } = require("@xterm/headless");
const { serializeTerminal } = await import(repo + "/src/main/terminal-snapshot.ts");
let fail = () => undefined;
process.on("uncaughtException", (error) => fail(error));
const write = (t, d) => new Promise((resolve, reject) => { fail = reject; t.write(d, resolve); });
function state(buffer) {
  const lines = [], wrapped = [], cells = [];
  const cell = buffer.getNullCell();
  for (let row = 0; row < buffer.length; row++) {
    const line = buffer.getLine(row);
    lines.push(line.translateToString(true));
    wrapped.push(line.isWrapped);
    let text = "";
    for (let col = 0; col < line.length; col++) {
      line.getCell(col, cell);
      text += cell.getChars() + "/" + cell.getWidth() + "/" + cell.getFgColorMode() + ":" + cell.getFgColor() + "/" + cell.getBgColorMode() + ":" + cell.getBgColor()
        + "/" + [cell.isBold(), cell.isDim(), cell.isItalic(), cell.isUnderline(), cell.isBlink(), cell.isInverse(), cell.isInvisible(), cell.isStrikethrough(), cell.isOverline()].map((v) => v ? 1 : 0).join("") + "|";
    }
    cells.push(text);
  }
  return { length: buffer.length, x: buffer.cursorX, y: buffer.cursorY, baseY: buffer.baseY, lines, wrapped, cells };
}
const results = [];
for (const c of JSON.parse(readFileSync(input, "utf8"))) {
  const t = new Terminal({ cols: c.cols, rows: c.rows, scrollback: c.scrollback, allowProposedApi: true, logLevel: "off" });
  const titles = [];
  t.onTitleChange((title) => titles.push(title));
  try {
    for (const s of c.steps) {
      if (s.write !== undefined) await write(t, s.write);
      else t.resize(s.resize[0], s.resize[1]);
    }
    results.push({ normal: state(t.buffer.normal), alternate: t.buffer.active.type === "alternate", active: state(t.buffer.active), titles, snapshot: serializeTerminal(t) });
  } catch (error) {
    results.push({ error: String(error && error.message || error) });
  }
  t.dispose();
}
process.stdout.write(JSON.stringify(results));
`

func goState(buffer *headless.Buffer) bufferState {
	state := bufferState{Length: buffer.Length(), X: buffer.CursorX(), Y: buffer.CursorY(), BaseY: buffer.BaseY()}
	var cell headless.Cell
	for row := 0; row < buffer.Length(); row++ {
		line := buffer.Line(row)
		state.Lines = append(state.Lines, line.TranslateToString(true))
		state.Wrapped = append(state.Wrapped, line.IsWrapped())
		var text strings.Builder
		for col := 0; col < line.Length(); col++ {
			line.Cell(col, &cell)
			bits := ""
			for _, set := range []bool{cell.IsBold(), cell.IsDim(), cell.IsItalic(), cell.IsUnderline(), cell.IsBlink(), cell.IsInverse(), cell.IsInvisible(), cell.IsStrikethrough(), cell.IsOverline()} {
				if set {
					bits += "1"
				} else {
					bits += "0"
				}
			}
			fmt.Fprintf(&text, "%s/%d/%d:%d/%d:%d/%s|", cell.Chars(), cell.Width(), cell.FgColorMode(), cell.FgColor(), cell.BgColorMode(), cell.BgColor(), bits)
		}
		state.Cells = append(state.Cells, text.String())
	}
	return state
}

func replayInGo(c replayCase) screenState {
	screen := headless.New(headless.Options{Cols: c.Cols, Rows: c.Rows, Scrollback: c.Scrollback})
	var titles []string
	screen.OnTitleChange(func(title string) { titles = append(titles, title) })
	for _, s := range c.Steps {
		if s.Write != nil {
			screen.Write(*s.Write)
		} else {
			screen.Resize(s.Resize[0], s.Resize[1])
		}
	}
	return screenState{Normal: goState(screen.Normal()), Alternate: screen.IsAlternate(), Active: goState(screen.Buffer()), Titles: titles, Snapshot: serializeTerminal(screen)}
}

func typeScriptRepo(t *testing.T) string {
	repo := os.Getenv("ACT_TS_REPO")
	if repo == "" {
		repo, _ = filepath.Abs("../../../ai-coding-tool")
	}
	if _, err := os.Stat(filepath.Join(repo, "node_modules/@xterm/headless/package.json")); err != nil {
		t.Skip("needs the TypeScript checkout with node_modules beside this repository")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("needs node")
	}
	return repo
}

func replayInXterm(t *testing.T, repo string, cases []replayCase) []screenState {
	dir := t.TempDir()
	input := filepath.Join(dir, "cases.json")
	data, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "replay.mjs")
	if err := os.WriteFile(script, []byte(replayScript), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("node", "--no-warnings", "--experimental-strip-types", script, repo, input)
	command.Stderr = os.Stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("node replay: %v", err)
	}
	var states []screenState
	if err := json.Unmarshal(output, &states); err != nil {
		t.Fatal(err)
	}
	return states
}

var fragments = []string{
	"a", "hello", "word ", "0123456789", "你好", "😀", "e\u0301", "\u0301", "ｱ", "\t", "\r", "\n", "\r\n", "\b", "\x07",
	"\x1b[m", "\x1b[1m", "\x1b[2;3;4;5;7;8;9;53m", "\x1b[22;23;24;25;27;28;29;55m", "\x1b[31m", "\x1b[42m", "\x1b[93m", "\x1b[105m",
	"\x1b[38;5;200m", "\x1b[48;5;17m", "\x1b[38;2;10;20;30m", "\x1b[48:2::1:2:3m", "\x1b[38:5:99m", "\x1b[4:3m", "\x1b[21m", "\x1b[39;49m", "\x1b[58;5;3m", "\x1b[59m",
	"\x1b[H", "\x1b[2;3H", "\x1b[10;40f", "\x1b[A", "\x1b[3B", "\x1b[2C", "\x1b[D", "\x1b[E", "\x1b[F", "\x1b[5G", "\x1b[3d", "\x1b[2`", "\x1b[a", "\x1b[e",
	"\x1b[J", "\x1b[1J", "\x1b[2J", "\x1b[3J", "\x1b[K", "\x1b[1K", "\x1b[2K", "\x1b[?K", "\x1b[L", "\x1b[2M", "\x1b[@", "\x1b[2P", "\x1b[3X", "\x1b[S", "\x1b[2T",
	"\x1b[2;4r", "\x1b[r", "\x1bM", "\x1bD", "\x1bE", "\x1b7", "\x1b8", "\x1b[s", "\x1b[u", "\x1bH", "\x1b[g", "\x1b[3g", "\x1b[I", "\x1b[Z", "\x1b[3b",
	"\x1b[?1049h", "\x1b[?1049l", "\x1b[?47h", "\x1b[?47l", "\x1b[?1047h", "\x1b[?1047l", "\x1b[?1048h", "\x1b[?1048l",
	"\x1b[4h", "\x1b[4l", "\x1b[?7l", "\x1b[?7h", "\x1b[?6h", "\x1b[?6l", "\x1b[?45h", "\x1b[?45l", "\x1b[?1h", "\x1b[?1l", "\x1b=", "\x1b>",
	"\x1b[?2004h", "\x1b[?1004h", "\x1b[?1000h", "\x1b[?1002h", "\x1b[?1003l", "\x1b[?9h", "\x1b[20h", "\x1b[20l",
	"\x1b(0lqk\x1b(B", "\x1b)0\x0eqx\x0f", "\x1b(A#", "\x1b]0;title\x07", "\x1b]2;other\x1b\\", "\x1b]2;\x07", "\x1b]8;;http://x\x07link\x1b]8;;\x07",
	"\x1b[!p", "\x1b#8", "\x1b[1;2'}", "\x1b['~", "\x1b[2 @", "\x1b[ A", "\x1bP$qm\x1b\\", "\x1b_apc\x1b\\", "\x1b[1\"q", "\x1b[?25l",
	"\x1b[999C", "\x1b[999B", "\x1b[0;0H", "\x1b[38;5m", "\x1b[38;2;1m", "\x1b[",
	"longlonglonglonglonglonglonglong", "你好世界你好世界你好", "\x1b[?2026h", "\x1b[1;31;4:2m", "\x1b[38;2;300;20;30m", "\x1b[1;4;9;38;2;1;2;3;48;5;4m",
}

func generateCases(seed uint64, count int) []replayCase {
	random := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var cases []replayCase
	for i := 0; i < count; i++ {
		c := replayCase{Cols: 2 + random.IntN(30), Rows: 1 + random.IntN(10), Scrollback: random.IntN(20)}
		for s := 0; s < 1+random.IntN(8); s++ {
			if random.IntN(6) == 0 {
				c.Steps = append(c.Steps, step{Resize: []int{2 + random.IntN(35), 1 + random.IntN(12)}})
				continue
			}
			var text strings.Builder
			for f := 0; f < 1+random.IntN(40); f++ {
				if random.IntN(40) == 0 {
					text.WriteString("\x1bc")
					continue
				}
				text.WriteString(fragments[random.IntN(len(fragments))])
			}
			value := text.String()
			c.Steps = append(c.Steps, step{Write: &value})
		}
		cases = append(cases, c)
	}
	return cases
}

func TestScreenMatchesXtermHeadless(t *testing.T) {
	repo := typeScriptRepo(t)
	long := strings.Repeat("x", 30) + "你好世界" + strings.Repeat("y", 17) + "\r\n"
	fixed := []replayCase{
		{Cols: 12, Rows: 3, Scrollback: 50, Steps: []step{{Write: ptr("old\r\nprogress 10\rprogress 99\r\n\x1b[31m你好 red\x1b[0m\r\nprompt> ")}}},
		{Cols: 20, Rows: 4, Scrollback: 1000, Steps: []step{{Write: ptr("shell> \x1b[?1049h\x1b[?2004h\x1b[2;3Heditor")}}},
		{Cols: 12, Rows: 3, Scrollback: 1000, Steps: []step{{Write: ptr("123456789012")}}},
		{Cols: 10, Rows: 4, Scrollback: 30, Steps: []step{{Write: ptr(strings.Repeat(long, 6))}, {Resize: []int{7, 4}}, {Write: ptr("abc")}, {Resize: []int{25, 6}}, {Resize: []int{3, 2}}, {Resize: []int{40, 9}}}},
		{Cols: 80, Rows: 24, Scrollback: 100, Steps: []step{{Write: ptr(strings.Repeat("中文abc\x1b[31m😀x\x1b[0m e\u0301\r\n", 20000) + "\x1b]2;" + strings.Repeat("t", 140000) + "\x07")}}},
	}
	count, seed := 400, uint64(1)
	if value, err := strconv.Atoi(os.Getenv("ACT_REPLAY_CASES")); err == nil {
		count, seed = value, uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
	}
	cases := append(fixed, generateCases(seed, count)...)
	want := replayInXterm(t, repo, cases)
	failures, skipped := 0, 0
	for i, c := range cases {
		got := replayInGo(c)
		if want[i].Error != "" {
			skipped++
			continue
		}
		if !equalStates(got, want[i]) {
			failures++
			if failures <= 3 {
				data, _ := json.Marshal(c)
				t.Errorf("case %d differs\ninput: %s\n%s", i, data, describeDifference(got, want[i]))
			}
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d cases differ", failures, len(cases))
	}
	if skipped > len(cases)/10 {
		t.Errorf("xterm.js threw on %d of %d cases", skipped, len(cases))
	}
}

func equalStates(a, b screenState) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func describeDifference(got, want screenState) string {
	var out strings.Builder
	compare := func(name string, a, b any) {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if string(x) != string(y) {
			fmt.Fprintf(&out, "%s:\n  go:    %s\n  xterm: %s\n", name, x, y)
		}
	}
	compare("alternate", got.Alternate, want.Alternate)
	compare("titles", got.Titles, want.Titles)
	for _, pair := range []struct {
		name      string
		got, want bufferState
	}{{"normal", got.Normal, want.Normal}, {"active", got.Active, want.Active}} {
		compare(pair.name+".cursor", []int{pair.got.Length, pair.got.X, pair.got.Y, pair.got.BaseY}, []int{pair.want.Length, pair.want.X, pair.want.Y, pair.want.BaseY})
		compare(pair.name+".lines", pair.got.Lines, pair.want.Lines)
		compare(pair.name+".wrapped", pair.got.Wrapped, pair.want.Wrapped)
		for row := range min(len(pair.got.Cells), len(pair.want.Cells)) {
			compare(fmt.Sprintf("%s.cells[%d]", pair.name, row), pair.got.Cells[row], pair.want.Cells[row])
		}
	}
	compare("snapshot", got.Snapshot, want.Snapshot)
	return out.String()
}
