package desktop

import (
	"encoding/json/v2"
	"math"
	"os"
	"sync"
)

// WindowPlacement is where the window sat, and whether the platform was holding it open at its largest.
type WindowPlacement struct {
	X          *float64 `json:"x,omitzero"`
	Y          *float64 `json:"y,omitzero"`
	Width      float64  `json:"width"`
	Height     float64  `json:"height"`
	Maximized  *bool    `json:"maximized,omitzero"`
	FullScreen *bool    `json:"fullScreen,omitzero"`
}

// ScreenArea is a display's work area, in global coordinates from the top left of the main display.
type ScreenArea struct {
	X, Y, Width, Height float64
}

// DefaultPlacement is the placement of a first launch.
var DefaultPlacement = WindowPlacement{Width: 1240, Height: 820}

const (
	MinWindowWidth  = 820
	MinWindowHeight = 620
	// How much of the window's top edge has to stay on a screen for the user to drag it back.
	visibleEdge = 80
	// PlacementFile is the placement's file in the profile.
	PlacementFile = "window-placement.v1.json"
)

func finitePixel(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

// parseWindowPlacement reads a placement the way isWindowPlacement accepts one.
func parseWindowPlacement(data []byte) (WindowPlacement, bool) {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil || value == nil {
		return WindowPlacement{}, false
	}
	width, okWidth := finitePixel(value["width"])
	height, okHeight := finitePixel(value["height"])
	if !okWidth || !okHeight || width < MinWindowWidth || height < MinWindowHeight {
		return WindowPlacement{}, false
	}
	placement := WindowPlacement{Width: width, Height: height}
	for key, target := range map[string]**float64{"x": &placement.X, "y": &placement.Y} {
		raw, present := value[key]
		if !present {
			continue
		}
		number, ok := finitePixel(raw)
		if !ok {
			return WindowPlacement{}, false
		}
		*target = &number
	}
	for key, target := range map[string]**bool{"maximized": &placement.Maximized, "fullScreen": &placement.FullScreen} {
		raw, present := value[key]
		if !present {
			continue
		}
		flag, ok := raw.(bool)
		if !ok {
			return WindowPlacement{}, false
		}
		*target = &flag
	}
	return placement, true
}

// LoadWindowPlacement reads the remembered placement. An unreadable file means the default, which
// is what a first launch reads anyway.
func LoadWindowPlacement(file string) WindowPlacement {
	data, err := os.ReadFile(file)
	if err != nil {
		return DefaultPlacement
	}
	placement, ok := parseWindowPlacement(data)
	if !ok {
		return DefaultPlacement
	}
	return placement
}

// FitPlacement fits the remembered placement to the screens the user has now. A window wider than
// every screen is cut down to the one it sits on, and a window on a screen that is gone loses its
// position, so the platform places it again rather than opening it out of reach.
func FitPlacement(placement WindowPlacement, screens []ScreenArea) WindowPlacement {
	if len(screens) == 0 {
		return placement
	}
	under := screenUnder(placement, screens)
	screen := screens[0]
	if under != nil {
		screen = *under
	}
	width := math.Max(MinWindowWidth, math.Min(placement.Width, screen.Width))
	height := math.Max(MinWindowHeight, math.Min(placement.Height, screen.Height))
	fitted := placement
	fitted.Width, fitted.Height = width, height
	if under == nil {
		fitted.X, fitted.Y = nil, nil
		return fitted
	}
	x := math.Min(math.Max(*placement.X, screen.X-width+visibleEdge), screen.X+screen.Width-visibleEdge)
	y := math.Min(math.Max(*placement.Y, screen.Y), screen.Y+screen.Height-visibleEdge)
	fitted.X, fitted.Y = &x, &y
	return fitted
}

// screenUnder is the screen that holds the window's title bar, which is the one the user reads it on.
func screenUnder(placement WindowPlacement, screens []ScreenArea) *ScreenArea {
	if placement.X == nil || placement.Y == nil {
		return nil
	}
	x, y := *placement.X, *placement.Y
	for index := range screens {
		screen := screens[index]
		if x+placement.Width > screen.X && x < screen.X+screen.Width && y+visibleEdge > screen.Y && y < screen.Y+screen.Height {
			return &screens[index]
		}
	}
	return nil
}

// placementWriter queues writes behind one another, since two overlapping ones leave the tail of the longer.
type placementWriter struct {
	mu sync.Mutex
}

func (writer *placementWriter) remember(file string, placement WindowPlacement) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	data, err := json.Marshal(placement)
	if err != nil {
		return
	}
	_ = os.WriteFile(file, data, 0o644)
}
