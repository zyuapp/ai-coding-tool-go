package domain

import (
	"strings"
	"testing"
)

func TestRepairCut(t *testing.T) {
	cases := [][2]string{
		{"Then a **partly", "Then a "},
		{"Use `npm ru", "Use "},
		{"See [the docs](http", "See "},
		{"Some ~~struck", "Some "},
		{"An <http", "An "},
		{"Then a **partly**", "Then a **partly**"},
		{"a `code` span", "a `code` span"},
		{"one *two* three", "one *two* three"},
		{"[the docs](https://x) and on", "[the docs](https://x) and on"},
		{"The task_id field is fine", "The task_id field is fine"},
		{"2 * 3 is six", "2 * 3 is six"},
		{"Multi **line\nemphasis still open", "Multi "},
		{"```ts\nconst a = 1;\nconst b", "```ts\nconst a = 1;\nconst b\n```\n"},
		{"```ts", ""},
		{"| a | b |\n| c | d |", ""},
		{"| a | b |\n| --- | --- |\n| c | d", "| a | b |\n| --- | --- |\n| c | d"},
		{"Intro line\n| a | b |", "Intro line\n"},
		{"## ", ""},
		{"## Head", "## Head"},
		{"- ", ""},
		{"Plain sentence.", "Plain sentence."},
		{"", ""},
		{"Café **ouvert", "Café "},
	}
	for _, c := range cases {
		if got := RepairCut(c[0]); got != c[1] {
			t.Errorf("RepairCut(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestInlineSafeEndFindsLateMarkers(t *testing.T) {
	plain := strings.Repeat("ordinary prose ", 1_000)
	beforeOpen := plain + "! still plain "
	if got := InlineSafeEnd(beforeOpen + "**open"); got != len(beforeOpen) {
		t.Errorf("open: %d", got)
	}
	for _, text := range []string{"Plain sentence.", plain + `\* escaped`, plain + "`closed` tail"} {
		if got := InlineSafeEnd(text); got != len(text) {
			t.Errorf("%q: %d", text[max(0, len(text)-20):], got)
		}
	}
}

func TestScanBlocksCutsAtWholeBlocks(t *testing.T) {
	if got := ScanBlocks("One block.\n\nStill writing", EmptyScan()).SafeEnd; got != float64(len("One block.\n\n")) {
		t.Errorf("block: %v", got)
	}
	if got := ScanBlocks("```ts\ncode\n", EmptyScan()); got.SafeEnd != 0 || got.Fence == nil {
		t.Errorf("fence: %+v", got)
	}
	first := ScanBlocks("```ts\ncode\n", EmptyScan())
	if got := ScanBlocks("```ts\ncode\n```\nnext", first); got.SafeEnd != float64(len("```ts\ncode\n```\n")) || got.Fence != nil {
		t.Errorf("resumed: %+v", got)
	}
}
