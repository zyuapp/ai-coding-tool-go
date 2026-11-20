//go:build cgo && (darwin || linux)

package browser

import (
	"context"
	"encoding/json/jsontext"
	"sync"
	"sync/atomic"
)

// callResult is what an asynchronous native call answered with.
type callResult struct {
	json     jsontext.Value
	bytes    []byte
	ok       bool
	snapshot uint64
	err      error
}

// nativeCalls pairs asynchronous native work with the goroutine waiting on it. A late answer for a
// call whose waiter gave up is dropped.
var nativeCalls = struct {
	sync.Mutex
	next    atomic.Uint64
	waiting map[uint64]chan callResult
}{waiting: map[uint64]chan callResult{}}

// signalsTarget is the host the native side reports to; one per process.
var signalsTarget atomic.Pointer[signalsBox]

type signalsBox struct{ signals }

func currentSignals() signals {
	if box := signalsTarget.Load(); box != nil {
		return box.signals
	}
	return nil
}

func beginCall() (uint64, chan callResult) {
	id := nativeCalls.next.Add(1)
	answer := make(chan callResult, 1)
	nativeCalls.Lock()
	nativeCalls.waiting[id] = answer
	nativeCalls.Unlock()
	return id, answer
}

func answerCall(id uint64, result callResult) {
	nativeCalls.Lock()
	answer := nativeCalls.waiting[id]
	delete(nativeCalls.waiting, id)
	nativeCalls.Unlock()
	if answer != nil {
		answer <- result
	}
}

func awaitCall(ctx context.Context, id uint64, answer chan callResult) callResult {
	select {
	case result := <-answer:
		return result
	case <-ctx.Done():
		nativeCalls.Lock()
		delete(nativeCalls.waiting, id)
		nativeCalls.Unlock()
		return callResult{err: ctx.Err()}
	}
}

type cookieRecord struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Secure bool   `json:"secure"`
}

type cookieHandle struct {
	snapshot uint64
	index    int
}

// cookieJSON is how a cookie to set crosses to the native side.
type cookieJSON struct {
	URL            string   `json:"url"`
	Name           string   `json:"name"`
	Value          string   `json:"value"`
	Domain         *string  `json:"domain,omitzero"`
	Path           string   `json:"path"`
	Secure         bool     `json:"secure"`
	HTTPOnly       bool     `json:"httpOnly"`
	SameSite       SameSite `json:"sameSite"`
	ExpirationDate *float64 `json:"expirationDate,omitzero"`
}

func cookieJSONOf(cookie CookieDetails) cookieJSON {
	return cookieJSON{
		URL: cookie.URL, Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path,
		Secure: cookie.Secure, HTTPOnly: cookie.HTTPOnly, SameSite: cookie.SameSite, ExpirationDate: cookie.ExpirationDate,
	}
}
