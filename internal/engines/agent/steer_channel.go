package agent

import (
	"context"
	"sync"
)

// Carries steered messages from the coordinator into the run that is already going. The coordinator
// pushes; the provider pulls them into the session's input stream.
type SteerChannel struct {
	mu      sync.Mutex
	waiting []SteerMessage
	wake    chan struct{}
	closed  bool
}

func NewSteerChannel() *SteerChannel {
	return &SteerChannel{wake: make(chan struct{})}
}

func (channel *SteerChannel) Push(message SteerMessage) bool {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.closed {
		return false
	}
	channel.waiting = append(channel.waiting, message)
	channel.signal()
	return true
}

func (channel *SteerChannel) Close() {
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.closed {
		return
	}
	channel.closed = true
	channel.signal()
}

func (channel *SteerChannel) signal() {
	close(channel.wake)
	channel.wake = make(chan struct{})
}

func (channel *SteerChannel) Next(ctx context.Context) (SteerMessage, bool) {
	for {
		channel.mu.Lock()
		if len(channel.waiting) > 0 {
			message := channel.waiting[0]
			channel.waiting = channel.waiting[1:]
			channel.mu.Unlock()
			return message, true
		}
		if channel.closed {
			channel.mu.Unlock()
			return SteerMessage{}, false
		}
		wake := channel.wake
		channel.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return SteerMessage{}, false
		}
	}
}
