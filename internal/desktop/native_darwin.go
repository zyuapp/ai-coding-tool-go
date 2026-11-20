//go:build darwin

package desktop

/*
#cgo CFLAGS: -x objective-c -fno-objc-arc
#cgo LDFLAGS: -framework Foundation -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	"strings"
	"unsafe"
)

const platformName = "macos"

// activeApp receives what the native window reports; there is one window per process.
var activeApp *App

//export actWindowActive
func actWindowActive(active C.int) {
	if app := activeApp; app != nil {
		go app.windowActive(active != 0)
	}
}

//export actWindowChanged
func actWindowChanged(kind C.int) {
	if app := activeApp; app != nil {
		go app.windowChanged(kind == 1)
	}
}

//export actWindowCloseRequested
func actWindowCloseRequested() {
	if app := activeApp; app != nil {
		go app.windowClosing()
	}
}

//export actReopen
func actReopen() {
	if app := activeApp; app != nil {
		go app.reopen()
	}
}

//export actTransparencyChanged
func actTransparencyChanged(reduced C.int) {
	if app := activeApp; app != nil {
		go app.SendValue(ChannelReducedTransparency, reduced != 0)
	}
}

//export actDidWake
func actDidWake() {
	if app := activeApp; app != nil && app.options.OnWake != nil {
		go app.options.OnWake()
	}
}

//export actDockMarkAllRead
func actDockMarkAllRead() {
	if app := activeApp; app != nil {
		go app.markAllRead()
	}
}

type nativeState struct {
	frame                                WindowPlacement
	maximized, fullScreen, visible, mini bool
	focused                              bool
}

func nativeScreens() []ScreenArea {
	var out [64 * 4]C.double
	count := int(C.actScreens((*C.double)(unsafe.Pointer(&out[0])), 64))
	screens := make([]ScreenArea, count)
	for index := range screens {
		screens[index] = ScreenArea{X: float64(out[index*4]), Y: float64(out[index*4+1]), Width: float64(out[index*4+2]), Height: float64(out[index*4+3])}
	}
	return screens
}

func (app *App) nativeWindowState() (nativeState, bool) {
	var frame [4]C.double
	var flags [5]C.int
	if C.actWindowState(&frame[0], &flags[0]) == 0 {
		return nativeState{}, false
	}
	x, y := float64(frame[0]), float64(frame[1])
	return nativeState{
		frame:      WindowPlacement{X: &x, Y: &y, Width: float64(frame[2]), Height: float64(frame[3])},
		maximized:  flags[0] != 0,
		fullScreen: flags[1] != 0,
		visible:    flags[2] != 0,
		mini:       flags[3] != 0,
		focused:    flags[4] != 0,
	}, true
}

func nativeGlassAvailable() bool { return C.actGlassAvailable() != 0 }

func nativeReducedTransparency() bool { return C.actReducedTransparency() != 0 }

func (app *App) nativeInstall(material WindowMaterial) {
	activeApp = app
	kind := 0
	switch material {
	case MaterialVibrancy:
		kind = 1
	case MaterialGlass:
		kind = 2
	}
	C.actInstallWindowHooks(C.int(kind))
	C.actWatchTransparency()
}

func (app *App) nativeGlassTint() float64 { return float64(C.actGlassTint()) }

func (app *App) nativePlaceAndShow(placement WindowPlacement) {
	var x, y float64
	hasPosition := placement.X != nil && placement.Y != nil
	if hasPosition {
		x, y = *placement.X, *placement.Y
	}
	maximized := placement.Maximized != nil && *placement.Maximized
	fullScreen := placement.FullScreen != nil && *placement.FullScreen
	C.actPlaceAndShow(C.double(x), C.double(y), C.double(placement.Width), C.double(placement.Height), cBool(hasPosition), cBool(maximized), cBool(fullScreen))
}

func (app *App) nativeReveal()               { C.actReveal() }
func (app *App) nativeToggleFullScreen()     { C.actToggleFullScreen() }
func (app *App) nativeMinimize()             { C.actMinimize() }
func (app *App) nativeZoomWindow()           { C.actZoomWindow() }
func (app *App) nativeBringAllToFront()      { C.actBringAllToFront() }
func (app *App) nativeAbout()                { C.actAbout() }
func (app *App) nativeHideOthers()           { C.actHideOthers() }
func (app *App) nativeShowAll()              { C.actShowAll() }
func (app *App) nativeHideApp()              { C.actHideApp() }
func (app *App) nativeZoomPage(step int)     { C.actZoomPage(C.int(step)) }
func (app *App) nativeFocusPage()            { C.actFocusPage() }
func (app *App) nativeToggleInspector()      { C.actToggleInspector() }
func (app *App) nativeSetDockUnread(on bool) { C.actSetDockUnread(cBool(on)) }
func (app *App) nativeSetBadge(count int)    { C.actSetBadge(C.int(count)) }
func (app *App) nativeTitlebarDoubleClick()  { C.actTitlebarDoubleClick() }

func (app *App) nativeSetBackground(r, g, b, a uint8) {
	C.actSetBackground(C.int(r), C.int(g), C.int(b), C.int(a))
}

// nativeSetAppearance follows the system (0), or holds the frame dark (1) or light (2).
func (app *App) nativeSetAppearance(mode int) { C.actSetAppearance(C.int(mode)) }

func cBool(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

func localFontFamilies() []string {
	list := C.actFontFamilies()
	if list == nil {
		return []string{}
	}
	defer C.free(unsafe.Pointer(list))
	return strings.Split(C.GoString(list), "\n")
}

func pasteboardFiles() []string {
	list := C.actPasteboardFiles()
	if list == nil {
		return []string{}
	}
	defer C.free(unsafe.Pointer(list))
	if C.GoString(list) == "" {
		return []string{}
	}
	return strings.Split(C.GoString(list), "\n")
}

//export actLicenseResponse
func actLicenseResponse(address *C.char) C.ActLicenseAnswer {
	answer := servedLicense(C.GoString(address))
	out := C.ActLicenseAnswer{
		status:      C.int(answer.status),
		contentType: C.CString(answer.contentType),
		body:        C.CBytes(answer.body),
		length:      C.int(len(answer.body)),
	}
	if answer.csp != "" {
		out.csp = C.CString(answer.csp)
	}
	return out
}

func (app *App) nativeOpenLicenses(licenseDocuments) error {
	C.actOpenLicenses()
	return nil
}

// nativeCloseLicenses closes the licenses reader when it is the window in front.
func (app *App) nativeCloseLicenses() bool { return C.actCloseLicensesIfKey() != 0 }
