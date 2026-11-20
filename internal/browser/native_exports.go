//go:build cgo && (darwin || linux)

package browser

/*
#include <stdint.h>
*/
import "C"

import (
	"encoding/json/jsontext"
	"errors"
	"unsafe"
)

func cBool(value bool) C.int {
	if value {
		return 1
	}
	return 0
}

// actBrowserDecide takes which document the navigation loads as a frameTarget.
//
//export actBrowserDecide
func actBrowserDecide(view C.uint64_t, url, method *C.char, frame C.int) C.int {
	if target := currentSignals(); target != nil {
		return cBool(target.decide(handle(view), C.GoString(url), C.GoString(method), frameTarget(frame)))
	}
	return 0
}

//export actBrowserStarted
func actBrowserStarted(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.started(handle(view))
	}
}

//export actBrowserResponse
func actBrowserResponse(view C.uint64_t, url *C.char, status, mainFrame C.int) C.int {
	if target := currentSignals(); target != nil {
		return cBool(target.response(handle(view), C.GoString(url), int(status), mainFrame != 0))
	}
	return 0
}

//export actBrowserCommitted
func actBrowserCommitted(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.committed(handle(view))
	}
}

//export actBrowserFinished
func actBrowserFinished(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.finished(handle(view))
	}
}

//export actBrowserFailed
func actBrowserFailed(view C.uint64_t, provisional C.int, description, message *C.char) {
	if target := currentSignals(); target != nil {
		target.failed(handle(view), provisional != 0, C.GoString(description), C.GoString(message))
	}
}

//export actBrowserTerminated
func actBrowserTerminated(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.terminated(handle(view))
	}
}

//export actBrowserChanged
func actBrowserChanged(view C.uint64_t, key, url, title *C.char, loading, back, forward C.int) {
	if target := currentSignals(); target != nil {
		target.changed(handle(view), C.GoString(key), pageState{
			URL: C.GoString(url), Title: C.GoString(title), Loading: loading != 0, CanGoBack: back != 0, CanGoForward: forward != 0,
		})
	}
}

// actBrowserMessage takes the document's origin, or with fromURL its address to read the origin from.
//
//export actBrowserMessage
func actBrowserMessage(view C.uint64_t, origin *C.char, fromURL C.int, body *C.char) {
	if target := currentSignals(); target != nil {
		from := C.GoString(origin)
		if fromURL != 0 {
			from = originOf(from)
		}
		target.message(handle(view), from, C.GoString(body))
	}
}

//export actBrowserPressed
func actBrowserPressed(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.pressed(handle(view))
	}
}

//export actBrowserKey
func actBrowserKey(view C.uint64_t, kind, key, code *C.char, meta, control, alt, shift, repeat C.int) C.int {
	target := currentSignals()
	if target == nil {
		return 0
	}
	input := KeyEvent{Type: C.GoString(kind), IsAutoRepeat: repeat != 0}
	input.Key, input.Code = C.GoString(key), C.GoString(code)
	input.Meta, input.Control, input.Alt, input.Shift = meta != 0, control != 0, alt != 0, shift != 0
	return cBool(target.key(handle(view), input))
}

//export actBrowserPopup
func actBrowserPopup(opener C.uint64_t, url *C.char) C.uint64_t {
	if target := currentSignals(); target != nil {
		if popup, ok := target.popup(handle(opener), C.GoString(url)); ok {
			return C.uint64_t(popup)
		}
	}
	return 0
}

//export actBrowserPopupClosed
func actBrowserPopupClosed(view C.uint64_t) {
	if target := currentSignals(); target != nil {
		target.popupClosed(handle(view))
	}
}

//export actBrowserEvaluated
func actBrowserEvaluated(call C.uint64_t, json, failure *C.char) {
	if failure != nil {
		answerCall(uint64(call), callResult{err: errors.New(C.GoString(failure))})
		return
	}
	answerCall(uint64(call), callResult{json: jsontext.Value(C.GoString(json))})
}

//export actBrowserDrawn
func actBrowserDrawn(call C.uint64_t, bytes unsafe.Pointer, length C.int, failure *C.char) {
	if failure != nil {
		answerCall(uint64(call), callResult{err: errors.New(C.GoString(failure))})
		return
	}
	answerCall(uint64(call), callResult{bytes: C.GoBytes(bytes, length)})
}

//export actBrowserFound
func actBrowserFound(call C.uint64_t, found C.int) {
	answerCall(uint64(call), callResult{ok: found != 0})
}

//export actBrowserCookieList
func actBrowserCookieList(call, snapshot C.uint64_t, json *C.char) {
	answerCall(uint64(call), callResult{json: jsontext.Value(C.GoString(json)), snapshot: uint64(snapshot)})
}

//export actBrowserDone
func actBrowserDone(call C.uint64_t, ok C.int) {
	answerCall(uint64(call), callResult{ok: ok != 0})
}
