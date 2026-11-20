//go:build linux

package desktop

import (
	"errors"
	"os/exec"
	"slices"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const platformName = "linux"

type nativeState struct {
	frame                                WindowPlacement
	maximized, fullScreen, visible, mini bool
	focused                              bool
}

// nativeScreens is empty on Linux: Wails reports no work areas, so placements keep their size only.
func nativeScreens() []ScreenArea { return nil }

func (app *App) nativeWindowState() (nativeState, bool) {
	if app.ctx == nil {
		return nativeState{}, false
	}
	width, height := runtime.WindowGetSize(app.ctx)
	return nativeState{
		frame:      WindowPlacement{Width: float64(width), Height: float64(height)},
		maximized:  runtime.WindowIsMaximised(app.ctx),
		fullScreen: runtime.WindowIsFullscreen(app.ctx),
		visible:    true,
		mini:       runtime.WindowIsMinimised(app.ctx),
	}, true
}

func nativeGlassAvailable() bool { return false }

func nativeReducedTransparency() bool { return false }

func (app *App) nativeInstall(material WindowMaterial) {}
func (app *App) nativeGlassTint() float64              { return 0 }

func (app *App) nativePlaceAndShow(placement WindowPlacement) {
	runtime.WindowSetSize(app.ctx, int(placement.Width), int(placement.Height))
	runtime.WindowShow(app.ctx)
	if placement.FullScreen != nil && *placement.FullScreen {
		runtime.WindowFullscreen(app.ctx)
	} else if placement.Maximized != nil && *placement.Maximized {
		runtime.WindowMaximise(app.ctx)
	}
}

func (app *App) nativeReveal() {
	runtime.WindowUnminimise(app.ctx)
	runtime.WindowShow(app.ctx)
}

func (app *App) nativeToggleFullScreen() {
	if runtime.WindowIsFullscreen(app.ctx) {
		runtime.WindowUnfullscreen(app.ctx)
	} else {
		runtime.WindowFullscreen(app.ctx)
	}
}

func (app *App) nativeMinimize()                 { runtime.WindowMinimise(app.ctx) }
func (app *App) nativeZoomWindow()               { runtime.WindowToggleMaximise(app.ctx) }
func (app *App) nativeBringAllToFront()          { runtime.WindowShow(app.ctx) }
func (app *App) nativeAbout()                    {}
func (app *App) nativeHideOthers()               {}
func (app *App) nativeShowAll()                  {}
func (app *App) nativeHideApp()                  { runtime.WindowHide(app.ctx) }
func (app *App) nativeZoomPage(step int)         {}
func (app *App) nativeFocusPage()                {}
func (app *App) nativeToggleInspector()          {}
func (app *App) nativeSetDockUnread(unread bool) {}
func (app *App) nativeSetAppearance(mode int)    {}
func (app *App) nativeSetBadge(count int)        {}
func (app *App) nativeTitlebarDoubleClick()      { runtime.WindowToggleMaximise(app.ctx) }
func (app *App) nativeSetBackground(r, g, b, a uint8) {
	runtime.WindowSetBackgroundColour(app.ctx, r, g, b, a)
}

// nativeOpenLicenses shows the overview in the system's own viewer; Linux has no second web view window.
func (app *App) nativeOpenLicenses(files licenseDocuments) error {
	if failure := platform.OpenPath(files.paths[licenseOverview]); failure != "" {
		return errors.New(failure)
	}
	return nil
}

func (app *App) nativeCloseLicenses() bool { return false }

func pasteboardFiles() []string { return []string{} }

func localFontFamilies() []string {
	output, err := exec.Command("fc-list", ":", "family").Output()
	if err != nil {
		return []string{}
	}
	families := []string{}
	for _, line := range strings.Split(string(output), "\n") {
		for _, family := range strings.Split(line, ",") {
			if family = strings.TrimSpace(family); family != "" && !slices.Contains(families, family) {
				families = append(families, family)
			}
		}
	}
	return families
}
