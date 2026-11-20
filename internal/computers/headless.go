package computers

import (
	"context"
	"errors"
	"sync"
)

// NeedsDesktop is the refusal for what only the desktop app can do, on a host that draws nothing.
func NeedsDesktop(what string) error {
	return errors.New(what + " needs the desktop app.")
}

// AnsweredOnce is a value answered once, then nil from then on.
func AnsweredOnce[V any](value *V) func(ctx context.Context) (*V, error) {
	var mu sync.Mutex
	held := value
	return func(context.Context) (*V, error) {
		mu.Lock()
		defer mu.Unlock()
		answer := held
		held = nil
		return answer, nil
	}
}

// unsubscribed is a subscription to something that never happens here.
func unsubscribed() func() { return func() {} }
