package browser

import (
	"context"
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// handle names one native web view: a tab's page or a popup a page opened. A tab reopened under
// the same id gets a new handle, so a closed page's late signals never reach the new one.
type handle uint64

// pageState is what a page's view says about itself.
type pageState struct {
	URL          string
	Title        string
	Loading      bool
	CanGoBack    bool
	CanGoForward bool
}

// rect is a frame in the app window's points, from its top left.
type rect struct{ X, Y, Width, Height float64 }

// sessionCookie is one cookie the panel's session holds, by what deleting it needs.
type sessionCookie struct {
	Domain string
	Name   string
	Path   string
	Secure bool
	// native is the engine's own record of the cookie.
	native any
}

// engine is the native web views: WKWebView on macOS, WebKitGTK on Linux. Every method may be
// called from any goroutine and runs the work on the UI thread; none is called with the host's lock
// held, since the UI thread may be inside a signal waiting for that lock.
type engine interface {
	// start finds the app window the pages are placed over and starts reporting to signals.
	start(signals signals, userAgent string) error
	stop()
	// open makes a tab's view, hidden in a window nobody sees, laid out at width by height.
	open(view handle, width, height float64) error
	load(view handle, url string) error
	goBack(view handle)
	goForward(view handle)
	reload(view handle)
	close(view handle)
	closePopup(popup handle)
	// place puts a view over the panel at frame when shown, else parked at frame's size.
	place(view handle, shown bool, frame rect)
	// wake makes a parked view draw for a capture. The next place puts it back.
	wake(view handle)
	// zoom is how far the app's own page is zoomed, which panel bounds are measured in.
	zoom() float64
	state(view handle) pageState
	// evaluate runs an expression in a script world the page cannot reach and answers what it
	// settles to as JSON, null for undefined.
	evaluate(ctx context.Context, view handle, script string) (jsontext.Value, error)
	// draw answers a PNG of the viewport, or of the page's top left width by height points.
	draw(ctx context.Context, view handle, fullPage bool, width, height float64) ([]byte, error)
	// find selects the next match of query, wrapping at either end, and answers whether there was one.
	find(ctx context.Context, view handle, query string, backwards bool) bool
	focus(view handle)
	cookies(ctx context.Context) ([]sessionCookie, error)
	deleteCookie(ctx context.Context, cookie sessionCookie) error
	setCookie(ctx context.Context, cookie CookieDetails) bool
	// clearData removes every cookie, cache and storage the panel's session holds.
	clearData(ctx context.Context) error
}

// signals is what an engine reports, always on the UI thread. Handles it does not know are ignored.
type signals interface {
	// decide answers whether a navigation may happen.
	decide(view handle, url, method string, target frameTarget) bool
	started(view handle)
	// response answers whether a response may be shown.
	response(view handle, url string, status int, mainFrame bool) bool
	// committed is a new document in the view.
	committed(view handle)
	finished(view handle)
	// failed is a load that ended without a page; message is empty for one only replaced or refused.
	failed(view handle, provisional bool, description, message string)
	terminated(view handle)
	// changed is one of the view's properties: loading, title, URL, canGoBack, canGoForward.
	changed(view handle, key string, state pageState)
	// message is what the page's diagnostics hooks posted, from a document of origin.
	message(view handle, origin, body string)
	// pressed is a mouse button going down in the view.
	pressed(view handle)
	// key is a key in the view, answering whether the app took it.
	key(view handle, input KeyEvent) bool
	// popup is a page asking to open a window; it answers the new window's handle, or false to refuse.
	popup(opener handle, url string) (handle, bool)
	popupClosed(popup handle)
}

// frameTarget is which document a navigation loads.
type frameTarget int

const (
	targetPage frameTarget = iota
	targetFrame
	// targetUnknown: WebKitGTK before 2.56 does not say.
	targetUnknown
)

// KeyEvent is a key a page was given, as Electron's before-input-event describes it.
type KeyEvent struct {
	domain.KeyInput
	// Type is "keyDown" or "keyUp".
	Type         string
	IsAutoRepeat bool
}
