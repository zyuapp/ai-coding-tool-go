package desktop

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/menu/keys"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	commandCheckForUpdates    = "app.check-for-updates"
	commandOpenSourceLicenses = "app.open-source-licenses"
)

// appMenu is the platform's default menu with app help added, so every item macOS expects stays
// where the user looks for it. Linux keeps no menu strip; the window's own keyboard handles its shortcuts.
func (app *App) appMenu() *menu.Menu {
	if platformName != "macos" {
		return nil
	}
	name := app.options.AppName
	root := menu.NewMenu()

	appMenu := root.AddSubmenu(name)
	appMenu.AddText("About "+name, nil, func(*menu.CallbackData) { app.nativeAbout() })
	appMenu.AddSeparator()
	app.updateItem = appMenu.AddText("Check for Updates…", nil, func(*menu.CallbackData) { go app.menuCommand(commandCheckForUpdates) })
	appMenu.AddText("Open Source Licenses…", nil, func(*menu.CallbackData) { go app.menuCommand(commandOpenSourceLicenses) })
	appMenu.AddSeparator()
	appMenu.AddText("Hide "+name, keys.CmdOrCtrl("h"), func(*menu.CallbackData) { app.nativeHideApp() })
	appMenu.AddText("Hide Others", keys.Combo("h", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(*menu.CallbackData) { app.nativeHideOthers() })
	appMenu.AddText("Show All", nil, func(*menu.CallbackData) { app.nativeShowAll() })
	appMenu.AddSeparator()
	appMenu.AddText("Quit "+name, keys.CmdOrCtrl("q"), func(*menu.CallbackData) { runtime.Quit(app.context()) })

	file := root.AddSubmenu("File")
	file.AddText("Close Window", keys.CmdOrCtrl("w"), func(*menu.CallbackData) {
		if app.nativeCloseLicenses() {
			return
		}
		runtime.WindowHide(app.context())
		go app.windowClosing()
	})

	root.Append(menu.EditMenu())

	view := root.AddSubmenu("View")
	view.AddText("Reload", keys.CmdOrCtrl("r"), func(*menu.CallbackData) { runtime.WindowReload(app.context()) })
	view.AddText("Force Reload", keys.Combo("r", keys.CmdOrCtrlKey, keys.ShiftKey), func(*menu.CallbackData) { runtime.WindowReloadApp(app.context()) })
	view.AddText("Toggle Developer Tools", keys.Combo("i", keys.CmdOrCtrlKey, keys.OptionOrAltKey), func(*menu.CallbackData) { app.nativeToggleInspector() })
	view.AddSeparator()
	view.AddText("Actual Size", keys.CmdOrCtrl("0"), func(*menu.CallbackData) { app.nativeZoomPage(0) })
	view.AddText("Zoom In", keys.CmdOrCtrl("+"), func(*menu.CallbackData) { app.nativeZoomPage(1) })
	view.AddText("Zoom Out", keys.CmdOrCtrl("-"), func(*menu.CallbackData) { app.nativeZoomPage(-1) })
	view.AddSeparator()
	view.AddText("Toggle Full Screen", keys.Combo("f", keys.CmdOrCtrlKey, keys.ControlKey), func(*menu.CallbackData) { app.nativeToggleFullScreen() })

	window := root.AddSubmenu("Window")
	window.AddText("Minimize", keys.CmdOrCtrl("m"), func(*menu.CallbackData) { app.nativeMinimize() })
	window.AddText("Zoom", nil, func(*menu.CallbackData) { app.nativeZoomWindow() })
	window.AddSeparator()
	window.AddText("Bring All to Front", nil, func(*menu.CallbackData) { app.nativeBringAllToFront() })
	return root
}

// SetUpdateChecking relabels the update item while a check runs.
func (app *App) SetUpdateChecking(checking bool) {
	item := app.updateItem
	if item == nil {
		return
	}
	if checking {
		item.Label = "Checking for Updates…"
	} else {
		item.Label = "Check for Updates…"
	}
	item.Disabled = checking
	runtime.MenuUpdateApplicationMenu(app.context())
}

// menuShortcut does what Electron's default menu item of that role does.
func (app *App) menuShortcut(item string) {
	ctx := app.context()
	switch item {
	case "quit":
		runtime.Quit(ctx)
	case "close":
		app.closeWindow()
	case "minimize":
		app.nativeMinimize()
	case "reload":
		runtime.WindowReload(ctx)
	case "forceReload":
		runtime.WindowReloadApp(ctx)
	case "toggleDevTools":
		app.nativeToggleInspector()
	case "resetZoom":
		app.nativeZoomPage(0)
	case "zoomIn":
		app.nativeZoomPage(1)
	case "zoomOut":
		app.nativeZoomPage(-1)
	case "togglefullscreen":
		app.nativeToggleFullScreen()
	}
}
