//go:build linux && !cgo

package desktop

func (app *App) nativeZoomPage(step int) {}
func (app *App) nativeFocusPage()        {}
func (app *App) nativeToggleInspector()  {}

func pasteboardFiles() []string { return []string{} }

func pasteboardImage() string { return "" }
