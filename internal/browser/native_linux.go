//go:build linux && cgo

package browser

/*
#cgo linux pkg-config: gtk+-3.0
#cgo !webkit2_41 pkg-config: webkit2gtk-4.0
#cgo webkit2_41 pkg-config: webkit2gtk-4.1
#cgo LDFLAGS: -ldl
#include <stdlib.h>
#include "native_linux.h"
*/
import "C"

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unsafe"
)

// gtkEngine is WebKitGTK: each page a view laid over the Wails window's own page in a GtkOverlay.
type gtkEngine struct{ sessionDir string }

func platformEngine(options Options) engine {
	dir := options.SessionDir
	if dir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			config = os.TempDir()
		}
		dir = filepath.Join(config, "aicodingtool-browser")
	}
	return gtkEngine{sessionDir: dir}
}

func cString(text string) *C.char { return C.CString(text) }

func takeError(failure *C.char) error {
	if failure == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(failure))
	return errors.New(C.GoString(failure))
}

// start ignores userAgent: WebKitGTK's own already names the engine it is.
func (e gtkEngine) start(signals signals, _ string) error {
	signalsTarget.Store(&signalsBox{signals})
	dir, diagnostics, mouse, handler := cString(e.sessionDir), cString(diagnosticsScript), cString(mouseNavigationScript), cString(messageHandler)
	defer C.free(unsafe.Pointer(dir))
	defer C.free(unsafe.Pointer(diagnostics))
	defer C.free(unsafe.Pointer(mouse))
	defer C.free(unsafe.Pointer(handler))
	return takeError(C.actBrowserStart(dir, diagnostics, mouse, handler))
}

func (gtkEngine) stop() {
	C.actBrowserStop()
	signalsTarget.Store(nil)
}

func (gtkEngine) open(view handle, width, height float64) error {
	return takeError(C.actBrowserOpen(C.uint64_t(view), C.double(width), C.double(height)))
}

func (gtkEngine) load(view handle, address string) error {
	text := cString(address)
	defer C.free(unsafe.Pointer(text))
	return takeError(C.actBrowserLoad(C.uint64_t(view), text))
}

func (gtkEngine) goBack(view handle)      { C.actBrowserBack(C.uint64_t(view)) }
func (gtkEngine) goForward(view handle)   { C.actBrowserForward(C.uint64_t(view)) }
func (gtkEngine) reload(view handle)      { C.actBrowserReload(C.uint64_t(view)) }
func (gtkEngine) close(view handle)       { C.actBrowserClose(C.uint64_t(view)) }
func (gtkEngine) closePopup(popup handle) { C.actBrowserClose(C.uint64_t(popup)) }

func (gtkEngine) place(view handle, shown bool, frame rect) {
	C.actBrowserPlace(C.uint64_t(view), cBool(shown), C.double(frame.X), C.double(frame.Y), C.double(frame.Width), C.double(frame.Height))
}

// wake does nothing: a parked page is already drawn, off screen.
func (gtkEngine) wake(handle) {}

func (gtkEngine) zoom() float64 { return float64(C.actBrowserZoom()) }

func (gtkEngine) state(view handle) pageState {
	var out C.ACTBrowserState
	C.actBrowserState(C.uint64_t(view), &out)
	defer C.free(unsafe.Pointer(out.url))
	defer C.free(unsafe.Pointer(out.title))
	return pageState{
		URL:          C.GoString(out.url),
		Title:        C.GoString(out.title),
		Loading:      out.loading != 0,
		CanGoBack:    out.canGoBack != 0,
		CanGoForward: out.canGoForward != 0,
	}
}

func (gtkEngine) evaluate(ctx context.Context, view handle, script string) (jsontext.Value, error) {
	id, answer := beginCall()
	text := cString(script)
	C.actBrowserEvaluate(C.uint64_t(view), text, C.uint64_t(id))
	C.free(unsafe.Pointer(text))
	result := awaitCall(ctx, id, answer)
	return result.json, result.err
}

func (gtkEngine) draw(ctx context.Context, view handle, fullPage bool, width, height float64) ([]byte, error) {
	id, answer := beginCall()
	C.actBrowserDraw(C.uint64_t(view), cBool(fullPage), C.double(width), C.double(height), C.uint64_t(id))
	result := awaitCall(ctx, id, answer)
	return result.bytes, result.err
}

func (gtkEngine) find(ctx context.Context, view handle, query string, backwards bool) bool {
	id, answer := beginCall()
	text := cString(query)
	C.actBrowserFind(C.uint64_t(view), text, cBool(backwards), C.uint64_t(id))
	C.free(unsafe.Pointer(text))
	return awaitCall(ctx, id, answer).ok
}

func (gtkEngine) focus(view handle) { C.actBrowserFocus(C.uint64_t(view)) }

// cookies is the session's cookie records, one per site: WebKitGTK removes a site's cookies together.
func (gtkEngine) cookies(ctx context.Context) ([]sessionCookie, error) {
	id, answer := beginCall()
	C.actBrowserCookies(C.uint64_t(id))
	result := awaitCall(ctx, id, answer)
	if result.err != nil {
		return nil, result.err
	}
	if len(result.json) == 0 {
		return nil, nil
	}
	names := strings.Split(string(result.json), "\n")
	cookies := make([]sessionCookie, len(names))
	for index, name := range names {
		cookies[index] = sessionCookie{Domain: name, native: cookieHandle{result.snapshot, index}}
	}
	return cookies, nil
}

func (gtkEngine) deleteCookie(ctx context.Context, cookie sessionCookie) error {
	held, ok := cookie.native.(cookieHandle)
	if !ok {
		return nil
	}
	id, answer := beginCall()
	C.actBrowserDeleteCookie(C.uint64_t(held.snapshot), C.int(held.index), C.uint64_t(id))
	return awaitCall(ctx, id, answer).err
}

var sameSiteCodes = map[SameSite]C.int{SameSiteUnspecified: 0, SameSiteNoRestriction: 1, SameSiteLax: 2, SameSiteStrict: 3}

func (gtkEngine) setCookie(ctx context.Context, cookie CookieDetails) bool {
	domain := ""
	switch {
	case cookie.Domain != nil && strings.HasPrefix(*cookie.Domain, "."):
		domain = *cookie.Domain
	case cookie.Domain != nil:
		domain = "." + *cookie.Domain
	default:
		parsed, err := url.Parse(cookie.URL)
		if err != nil {
			return false
		}
		domain = parsed.Hostname()
	}
	expires := -1.0
	if cookie.ExpirationDate != nil {
		expires = *cookie.ExpirationDate
	}
	name, value, scope, path := cString(cookie.Name), cString(cookie.Value), cString(domain), cString(cookie.Path)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(value))
	defer C.free(unsafe.Pointer(scope))
	defer C.free(unsafe.Pointer(path))
	id, answer := beginCall()
	C.actBrowserSetCookie(name, value, scope, path, cBool(cookie.Secure), cBool(cookie.HTTPOnly), sameSiteCodes[cookie.SameSite], C.double(expires), C.uint64_t(id))
	return awaitCall(ctx, id, answer).ok
}

func (gtkEngine) clearData(ctx context.Context) error {
	id, answer := beginCall()
	C.actBrowserClearData(C.uint64_t(id))
	return awaitCall(ctx, id, answer).err
}
