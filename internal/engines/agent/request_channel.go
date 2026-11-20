package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type pendingRequest struct {
	answer chan requestAnswer
	timer  *time.Timer
}

type requestAnswer struct {
	result jsontext.Value
	err    error
}

// Request/response correlation over a one-way post, shared by the thread and automation channels.
// A lost response has to surface as a tool error; a hung tool call reports nothing at all.
type requestChannel struct {
	mu      sync.Mutex
	pending map[string]*pendingRequest
	// What a timeout says, given the op and the patience in ms.
	timedOut func(op string, ms int64) string
}

func newRequestChannel(timedOut func(op string, ms int64) string) *requestChannel {
	return &requestChannel{pending: map[string]*pendingRequest{}, timedOut: timedOut}
}

func (channel *requestChannel) request(ctx context.Context, op string, timeout time.Duration, post func(requestID string) error) (jsontext.Value, error) {
	requestID := application.NewID()
	pending := &pendingRequest{answer: make(chan requestAnswer, 1)}
	channel.mu.Lock()
	channel.pending[requestID] = pending
	pending.timer = time.AfterFunc(timeout, func() {
		if channel.take(requestID) != nil {
			pending.answer <- requestAnswer{err: errors.New(channel.timedOut(op, timeout.Milliseconds()))}
		}
	})
	channel.mu.Unlock()
	if err := post(requestID); err != nil {
		if channel.take(requestID) != nil {
			pending.timer.Stop()
		}
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case answer := <-pending.answer:
		return answer.result, answer.err
	case <-ctx.Done():
		if channel.take(requestID) != nil {
			pending.timer.Stop()
		}
		return nil, ctx.Err()
	}
}

func (channel *requestChannel) take(requestID string) *pendingRequest {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	pending := channel.pending[requestID]
	delete(channel.pending, requestID)
	return pending
}

func (channel *requestChannel) settle(requestID string, ok bool, result jsontext.Value, message string) bool {
	pending := channel.take(requestID)
	if pending == nil {
		return false
	}
	pending.timer.Stop()
	if ok {
		pending.answer <- requestAnswer{result: result}
	} else {
		pending.answer <- requestAnswer{err: errors.New(message)}
	}
	return true
}

// Decodes a request's result into T; an absent result is T's zero value.
func decodeResult[T any](result jsontext.Value, err error) (T, error) {
	var value T
	if err != nil || len(result) == 0 {
		return value, err
	}
	if err := jsonx.Unmarshal(result, &value); err != nil {
		return value, fmt.Errorf("unreadable answer: %w", err)
	}
	return value, nil
}
