package terminal

import (
	"strings"
	"testing"
)

func TestSlowViewerRestoresSnapshotWhenOutputExceedsSharedBuffer(t *testing.T) {
	buffer := newOutputBuffer()
	for sequence := int64(1); sequence <= 5; sequence++ {
		buffer.push(sequence, strings.Repeat("x", 512*1024))
	}
	if _, ok := buffer.read(1, 5); ok {
		t.Fatal("a reader behind the tail got a delta")
	}
	if data, ok := buffer.read(4, 5); !ok || len(data) != 512*1024 {
		t.Fatalf("read after 4: %d bytes, %v", len(data), ok)
	}
	if data, ok := buffer.read(5, 5); !ok || data != "" {
		t.Fatal("a current reader did not get empty output")
	}
	buffer.reset()
	if _, ok := buffer.read(5, 6); ok {
		t.Fatal("resize must invalidate deltas from the old grid")
	}
	for sequence := int64(7); sequence <= 200; sequence++ {
		buffer.push(sequence, "x")
	}
	if _, ok := buffer.read(6, 200); ok {
		t.Fatal("small writes must also have a bounded entry count")
	}
}

func TestOversizedFlushKeepsItsTail(t *testing.T) {
	data := trimFlush(strings.Repeat("x", maxFlushBytes+2048), 0)
	if !strings.HasPrefix(data, "\r\n… 2 KB of output dropped\r\n") || !strings.HasSuffix(data, strings.Repeat("x", maxFlushBytes)) {
		t.Fatalf("unexpected flush head %q", data[:40])
	}
	if got := trimFlush("abc", 0); got != "abc" {
		t.Fatalf("a small flush changed: %q", got)
	}
	if got := trimFlush(strings.Repeat("y", maxFlushBytes), 1234*1024); !strings.HasPrefix(got, "\r\n… 1,234 KB of output dropped\r\n") {
		t.Fatalf("output dropped before the flush is not counted: %q", got[:40])
	}
}

func TestSplitUTF8WaitsForItsTail(t *testing.T) {
	bytes := []byte("aé")
	text, carry := decodeUTF8(bytes[:2])
	if text != "a" || len(carry) != 1 {
		t.Fatalf("got %q with %d carried", text, len(carry))
	}
	text, carry = decodeUTF8(append(carry, bytes[2:]...))
	if text != "é" || len(carry) != 0 {
		t.Fatalf("got %q with %d carried", text, len(carry))
	}
	if text, _ := decodeUTF8([]byte{'a', 0xff, 'b'}); text != "a�b" {
		t.Fatalf("invalid byte decoded as %q", text)
	}
}
