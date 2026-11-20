package domain

import (
	"strings"
	"testing"
)

func TestSizeRanges(t *testing.T) {
	for _, r := range []SizeRange{ReadingSize, TerminalSize} {
		for rung, px := range r.Legacy {
			if got, ok := SizeByID(r, rung); !ok || got != px {
				t.Errorf("%s: %v", rung, got)
			}
		}
	}
	for _, value := range []any{ReadingSize.Max + 1, ReadingSize.Min - 1, "a-size-we-dropped", nil} {
		if _, ok := SizeByID(ReadingSize, value); ok {
			t.Errorf("%v", value)
		}
	}
	if SizeOrDefault(ReadingSize, nil) != ReadingSize.Default || SizeOrDefault(ReadingSize, 14.6) != 15 {
		t.Error("sizeOrDefault")
	}
	if StepSize(ReadingSize, ReadingSize.Min, -1) != ReadingSize.Min || StepSize(ReadingSize, ReadingSize.Max, 1) != ReadingSize.Max || StepSize(ReadingSize, ReadingSize.Default, 1) != ReadingSize.Default+1 {
		t.Error("stepSize")
	}
}

func TestCustomFonts(t *testing.T) {
	if family, ok := CustomFontFamily(CustomFontID("Helvetica Neue")); !ok || family != "Helvetica Neue" || UiFontOrDefault(CustomFontID("Helvetica Neue")).Label != "Helvetica Neue" {
		t.Error("round trip")
	}
	for _, hostile := range []string{`Fake", monospace; color: red; --x: "`, `Fake\`, "Fake'", "", strings.Repeat("A", 65)} {
		if _, ok := CustomFontFamily(CustomFontID(hostile)); ok || UiFontOrDefault(CustomFontID(hostile)).ID != DefaultUiFont {
			t.Errorf("%q", hostile)
		}
	}
	if UiFontOrDefault("a-face-we-dropped").ID != DefaultUiFont || MonoFontOrDefault("a-face-we-dropped").ID != DefaultMonoFont || MonoFontOrDefault("fira-code").ID != "fira-code" {
		t.Error("dropped face")
	}
}
