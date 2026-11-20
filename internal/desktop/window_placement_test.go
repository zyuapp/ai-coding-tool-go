package desktop

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var screen = ScreenArea{Width: 1920, Height: 1080}

func number(value float64) *float64 { return &value }
func flag(value bool) *bool         { return &value }

func TestPlacementRoundTrips(t *testing.T) {
	file := filepath.Join(t.TempDir(), PlacementFile)
	placement := WindowPlacement{X: number(40), Y: number(60), Width: 1400, Height: 900, Maximized: flag(false), FullScreen: flag(true)}
	var writer placementWriter
	writer.remember(file, placement)
	if loaded := LoadWindowPlacement(file); !reflect.DeepEqual(loaded, placement) {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestUnreadablePlacementIsTheDefault(t *testing.T) {
	file := filepath.Join(t.TempDir(), PlacementFile)
	if loaded := LoadWindowPlacement(file); !reflect.DeepEqual(loaded, DefaultPlacement) {
		t.Fatalf("missing file loaded %+v", loaded)
	}
	for _, content := range []string{`{"width":10,"height":10}`, `{"width":900,"height":700,"x":"1"}`, `[]`, `nope`} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if loaded := LoadWindowPlacement(file); !reflect.DeepEqual(loaded, DefaultPlacement) {
			t.Fatalf("%s loaded %+v", content, loaded)
		}
	}
}

func TestFitPlacement(t *testing.T) {
	cases := []struct {
		name      string
		placement WindowPlacement
		screens   []ScreenArea
		want      WindowPlacement
	}{
		{"cut down to the screen", WindowPlacement{X: number(0), Y: number(0), Width: 3000, Height: 2000}, []ScreenArea{screen}, WindowPlacement{X: number(0), Y: number(0), Width: 1920, Height: 1080}},
		{"screen gone loses position", WindowPlacement{X: number(2400), Y: number(200), Width: 1240, Height: 820, FullScreen: flag(true)}, []ScreenArea{screen}, WindowPlacement{Width: 1240, Height: 820, FullScreen: flag(true)}},
		{"title bar stays on screen", WindowPlacement{X: number(100), Y: number(1040), Width: 1240, Height: 820}, []ScreenArea{screen}, WindowPlacement{X: number(100), Y: number(1000), Width: 1240, Height: 820}},
		{"second screen keeps its window", WindowPlacement{X: number(2000), Y: number(100), Width: 1240, Height: 820}, []ScreenArea{screen, {X: 1920, Width: 1440, Height: 900}}, WindowPlacement{X: number(2000), Y: number(100), Width: 1240, Height: 820}},
	}
	for _, c := range cases {
		if got := FitPlacement(c.placement, c.screens); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}
