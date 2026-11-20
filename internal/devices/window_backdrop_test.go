package devices

import (
	"math"
	"reflect"
	"testing"
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

func TestOnlyMacOSDrawsAMaterialAndGlassOnlyWhereItIsOffered(t *testing.T) {
	glass := &WindowGlass{attach: func(unsafe.Pointer) bool { return true }, regions: func(unsafe.Pointer, []float64) bool { return true }}
	if WindowMaterialFor(glass, "darwin") != contracts.WindowMaterialGlass || WindowMaterialFor(nil, "darwin") != contracts.WindowMaterialVibrancy || WindowMaterialFor(glass, "linux") != contracts.WindowMaterialNone {
		t.Fatal("wrong material")
	}
}

func TestGlassRegionsAreHandedOverFlat(t *testing.T) {
	var flat []float64
	glass := &WindowGlass{regions: func(_ unsafe.Pointer, values []float64) bool {
		flat = values
		return true
	}}
	glass.Regions(nil, []contracts.GlassRegion{{X: 1, Y: 2, Width: 3, Height: 4, Radius: 5}, {X: 6, Y: 7, Width: 8, Height: 9, Radius: 10}})
	if !reflect.DeepEqual(flat, []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Fatalf("got %v", flat)
	}
}

func TestSystemGlassTintIsAnAmountBetweenClearAndFull(t *testing.T) {
	for _, case_ := range []struct {
		value float64
		kept  bool
		want  float64
	}{{0.5, true, 0.5}, {7, true, 1}, {-1, true, 0}, {0, false, 0}, {math.NaN(), true, 0}} {
		if got := SystemGlassTint(func(string) (float64, bool) { return case_.value, case_.kept }); got != case_.want {
			t.Fatalf("%v: got %v", case_, got)
		}
	}
}

func TestPanesInAZoomedPageAreLaidInWindowPoints(t *testing.T) {
	pane := contracts.GlassRegion{X: 10, Y: 20, Width: 300, Height: 100, Radius: 10}
	if got := GlassRegionsInPoints([]contracts.GlassRegion{pane}, 1); !reflect.DeepEqual(got, []contracts.GlassRegion{pane}) {
		t.Fatalf("got %v", got)
	}
	got := GlassRegionsInPoints([]contracts.GlassRegion{pane}, 1.2)
	want := contracts.GlassRegion{X: 12, Y: 24, Width: 360, Height: 120, Radius: 12}
	close := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	if len(got) != 1 || !close(got[0].X, want.X) || !close(got[0].Y, want.Y) || !close(got[0].Width, want.Width) || !close(got[0].Height, want.Height) || !close(got[0].Radius, want.Radius) {
		t.Fatalf("got %v", got)
	}
}
