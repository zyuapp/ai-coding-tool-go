package devices

import (
	"math"
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// WindowGlass is the native layer that puts Liquid Glass under a window's page. A window is named by
// its NSWindow or any NSView in it.
type WindowGlass struct {
	attach  func(window unsafe.Pointer) bool
	regions func(window unsafe.Pointer, flat []float64) bool
}

// Attach lays one sheet of glass under the whole window, hiding the vibrancy that would frost it. False when no glass went under it.
func (g *WindowGlass) Attach(window unsafe.Pointer) bool {
	return g.attach(window)
}

// Regions lays one pane of glass per region under the window's page, in the window's own points.
func (g *WindowGlass) Regions(window unsafe.Pointer, regions []contracts.GlassRegion) bool {
	flat := make([]float64, 0, len(regions)*5)
	for _, region := range regions {
		flat = append(flat, region.X, region.Y, region.Width, region.Height, region.Radius)
	}
	return g.regions(window, flat)
}

// LoadWindowGlass is the glass layer, or nil off macOS and on a macOS without glass, which leaves the
// window on vibrancy alone.
func LoadWindowGlass() *WindowGlass {
	return loadWindowGlass()
}

// GlassRegionsInPoints lays panes a page names in its own pixels in the window's points: a zoomed page draws them larger.
func GlassRegionsInPoints(regions []contracts.GlassRegion, zoom float64) []contracts.GlassRegion {
	points := make([]contracts.GlassRegion, len(regions))
	for index, region := range regions {
		if zoom == 1 {
			points[index] = region
			continue
		}
		points[index] = contracts.GlassRegion{X: region.X * zoom, Y: region.Y * zoom, Width: region.Width * zoom, Height: region.Height * zoom, Radius: region.Radius * zoom}
	}
	return points
}

// WindowMaterialFor is what goes under the window: macOS draws a material, glass where it has it;
// other desktops keep an opaque one. goos is runtime.GOOS.
func WindowMaterialFor(glass *WindowGlass, goos string) contracts.WindowMaterial {
	if goos != "darwin" {
		return contracts.WindowMaterialNone
	}
	if glass != nil {
		return contracts.WindowMaterialGlass
	}
	return contracts.WindowMaterialVibrancy
}

// SystemGlassTint is how tinted the person has set the system's glass, from 0 for the clearest to 1.
// The system keeps it as NSGlassTintAmount; a macOS that does not answers 0, which is also how its
// glass is drawn.
func SystemGlassTint(read func(key string) (float64, bool)) float64 {
	amount, ok := read("NSGlassTintAmount")
	if !ok || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0
	}
	return math.Min(1, math.Max(0, amount))
}

// WindowMaterialArgument is how the renderer is told which material is under it, before its first paint.
const WindowMaterialArgument = "--window-material="
