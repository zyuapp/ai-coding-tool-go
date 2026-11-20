//go:build darwin && cgo

package browser

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework Cocoa -framework WebKit
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"unsafe"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// webkitEngine is WKWebView: each page a view placed over the Wails window's content view.
type webkitEngine struct{}

func platformEngine(Options) engine { return webkitEngine{} }

func cString(text string) *C.char { return C.CString(text) }

func takeError(failure *C.char) error {
	if failure == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(failure))
	return errors.New(C.GoString(failure))
}

func (webkitEngine) start(signals signals, userAgent string) error {
	signalsTarget.Store(&signalsBox{signals})
	agent, diagnostics, mouse, handler := cString(userAgent), cString(diagnosticsScript), cString(mouseNavigationScript), cString(messageHandler)
	defer C.free(unsafe.Pointer(agent))
	defer C.free(unsafe.Pointer(diagnostics))
	defer C.free(unsafe.Pointer(mouse))
	defer C.free(unsafe.Pointer(handler))
	return takeError(C.actBrowserStart(agent, diagnostics, mouse, handler))
}

func (webkitEngine) stop() {
	C.actBrowserStop()
	signalsTarget.Store(nil)
}

func (webkitEngine) open(view handle, width, height float64) error {
	return takeError(C.actBrowserOpen(C.uint64_t(view), C.double(width), C.double(height)))
}

func (webkitEngine) load(view handle, url string) error {
	text := cString(url)
	defer C.free(unsafe.Pointer(text))
	return takeError(C.actBrowserLoad(C.uint64_t(view), text))
}

func (webkitEngine) goBack(view handle)    { C.actBrowserBack(C.uint64_t(view)) }
func (webkitEngine) goForward(view handle) { C.actBrowserForward(C.uint64_t(view)) }
func (webkitEngine) reload(view handle)    { C.actBrowserReload(C.uint64_t(view)) }
func (webkitEngine) close(view handle)     { C.actBrowserClose(C.uint64_t(view)) }

func (webkitEngine) closePopup(popup handle) { C.actBrowserClose(C.uint64_t(popup)) }

func (webkitEngine) place(view handle, shown bool, frame rect) {
	C.actBrowserPlace(C.uint64_t(view), C.int(cBool(shown)), C.double(frame.X), C.double(frame.Y), C.double(frame.Width), C.double(frame.Height))
}

func (webkitEngine) wake(view handle) { C.actBrowserWake(C.uint64_t(view)) }

func (webkitEngine) zoom() float64 { return float64(C.actBrowserZoom()) }

func (webkitEngine) state(view handle) pageState {
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

func (webkitEngine) evaluate(ctx context.Context, view handle, script string) (jsontext.Value, error) {
	id, answer := beginCall()
	text := cString(script)
	C.actBrowserEvaluate(C.uint64_t(view), text, C.uint64_t(id))
	C.free(unsafe.Pointer(text))
	result := awaitCall(ctx, id, answer)
	return result.json, result.err
}

func (webkitEngine) draw(ctx context.Context, view handle, fullPage bool, width, height float64) ([]byte, error) {
	id, answer := beginCall()
	C.actBrowserDraw(C.uint64_t(view), C.int(cBool(fullPage)), C.double(width), C.double(height), C.uint64_t(id))
	result := awaitCall(ctx, id, answer)
	return result.bytes, result.err
}

func (webkitEngine) find(ctx context.Context, view handle, query string, backwards bool) bool {
	id, answer := beginCall()
	text := cString(query)
	C.actBrowserFind(C.uint64_t(view), text, C.int(cBool(backwards)), C.uint64_t(id))
	C.free(unsafe.Pointer(text))
	return awaitCall(ctx, id, answer).ok
}

func (webkitEngine) focus(view handle) { C.actBrowserFocus(C.uint64_t(view)) }

func (webkitEngine) cookies(ctx context.Context) ([]sessionCookie, error) {
	id, answer := beginCall()
	C.actBrowserCookies(C.uint64_t(id))
	result := awaitCall(ctx, id, answer)
	if result.err != nil {
		return nil, result.err
	}
	var records []cookieRecord
	if err := jsonx.Unmarshal(result.json, &records); err != nil {
		return nil, err
	}
	cookies := make([]sessionCookie, len(records))
	for index, record := range records {
		cookies[index] = sessionCookie{Domain: record.Domain, Name: record.Name, Path: record.Path, Secure: record.Secure, native: cookieHandle{result.snapshot, index}}
	}
	return cookies, nil
}

func (webkitEngine) deleteCookie(ctx context.Context, cookie sessionCookie) error {
	held, ok := cookie.native.(cookieHandle)
	if !ok {
		return nil
	}
	id, answer := beginCall()
	C.actBrowserDeleteCookie(C.uint64_t(held.snapshot), C.int(held.index), C.uint64_t(id))
	return awaitCall(ctx, id, answer).err
}

func (webkitEngine) setCookie(ctx context.Context, cookie CookieDetails) bool {
	data, err := jsonx.Marshal(cookieJSONOf(cookie))
	if err != nil {
		return false
	}
	id, answer := beginCall()
	text := cString(string(data))
	C.actBrowserSetCookie(text, C.uint64_t(id))
	C.free(unsafe.Pointer(text))
	return awaitCall(ctx, id, answer).ok
}

func (webkitEngine) clearData(ctx context.Context) error {
	id, answer := beginCall()
	C.actBrowserClearData(C.uint64_t(id))
	return awaitCall(ctx, id, answer).err
}
