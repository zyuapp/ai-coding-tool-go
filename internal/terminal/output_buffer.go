package terminal

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	outputMaxBytes  = 1024 * 1024
	outputMaxChunks = 128
	outputWait      = time.Second
)

type outputChunk struct {
	sequence int64
	data     string
	length   int
}

// outputBuffer is a bounded tail shared by remote viewers. A reader that falls behind restores
// the screen instead.
type outputBuffer struct {
	mu     sync.Mutex
	chunks []outputChunk
	bytes  int
	wakeCh chan struct{}
}

func newOutputBuffer() *outputBuffer {
	return &outputBuffer{wakeCh: make(chan struct{})}
}

func (b *outputBuffer) push(sequence int64, data string) {
	b.mu.Lock()
	length := utf16Len(data)
	b.chunks = append(b.chunks, outputChunk{sequence: sequence, data: data, length: length})
	b.bytes += length
	for b.bytes > outputMaxBytes || len(b.chunks) > outputMaxChunks {
		b.bytes -= b.chunks[0].length
		b.chunks[0] = outputChunk{}
		b.chunks = b.chunks[1:]
	}
	b.mu.Unlock()
	b.wake()
}

// read is the output after a watermark, or false when the tail no longer reaches back to it.
func (b *outputBuffer) read(after, sequence int64) (string, bool) {
	if after == sequence {
		return "", true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if after > sequence || len(b.chunks) == 0 || after < b.chunks[0].sequence-1 {
		return "", false
	}
	var result strings.Builder
	for _, chunk := range b.chunks {
		if chunk.sequence > after {
			result.WriteString(chunk.data)
		}
	}
	return result.String(), true
}

// waiter is closed by the next wake.
func (b *outputBuffer) waiter() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wakeCh
}

// wait returns at the next wake, after a short deadline, or when ctx ends, so a read for a hidden
// tab never holds a query open indefinitely.
func wait(ctx context.Context, woken <-chan struct{}) {
	timer := time.NewTimer(outputWait)
	defer timer.Stop()
	select {
	case <-woken:
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (b *outputBuffer) wake() {
	b.mu.Lock()
	close(b.wakeCh)
	b.wakeCh = make(chan struct{})
	b.mu.Unlock()
}

func (b *outputBuffer) reset() {
	b.mu.Lock()
	b.chunks = nil
	b.bytes = 0
	b.mu.Unlock()
	b.wake()
}
