package client

import (
	"context"
	"encoding/json/v2"
	"sync"
	"time"
)

const (
	// loadingRetry and loadingAttempts are how often, and how long, a list is read again while the
	// server is still loading the folder it names.
	loadingRetry    = 500 * time.Millisecond
	loadingAttempts = 10
)

// loaded holds the lists each server has already loaded, which are read once from then on.
var (
	loadedMu sync.Mutex
	loaded   = map[Requester]map[string]bool{}
)

func forgetLoaded(r Requester) {
	loadedMu.Lock()
	delete(loaded, r)
	loadedMu.Unlock()
}

func settled(r Requester, path string) bool {
	loadedMu.Lock()
	defer loadedMu.Unlock()
	return loaded[r][path]
}

func settle(r Requester, path string) {
	loadedMu.Lock()
	defer loadedMu.Unlock()
	if loaded[r] == nil {
		loaded[r] = map[string]bool{}
	}
	loaded[r][path] = true
}

// RequestAs sends a request and decodes its answer; an empty answer is T's zero value.
func RequestAs[T any](ctx context.Context, r Requester, method, path string, body any, timeout time.Duration) (T, error) {
	var value T
	raw, err := r.Request(ctx, method, path, body, timeout)
	if err != nil || len(raw) == 0 || raw.Kind() == 'n' {
		return value, err
	}
	return value, json.Unmarshal(raw, &value)
}

// ReadLoaded reads a list. One read in a folder the server has not loaded yet comes back empty,
// then grows while it loads, so it is read again until two answers in a row agree, for a few
// seconds at most. Retry 0 waits the usual half second between reads.
func ReadLoaded[T any](ctx context.Context, r Requester, path string, retry time.Duration) ([]T, error) {
	if retry <= 0 {
		retry = loadingRetry
	}
	var previous []T
	havePrevious := false
	for attempt := 1; ; attempt++ {
		list, err := RequestAs[[]T](ctx, r, "GET", path, nil, 0)
		if err != nil {
			return nil, err
		}
		if list == nil {
			list = []T{}
		}
		if settled(r, path) {
			return list, nil
		}
		if len(list) > 0 && havePrevious && len(list) == len(previous) {
			settle(r, path)
		}
		if settled(r, path) || attempt >= loadingAttempts {
			return list, nil
		}
		previous, havePrevious = list, true
		select {
		case <-time.After(retry):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
