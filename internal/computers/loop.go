package computers

import (
	"sync"
	"time"
)

// loop runs functions one at a time in the order they were posted, the way the TypeScript event
// loop runs callbacks: every piece of a client's or the links' state is touched only from inside it.
// A function running in the loop must never wait on call.
type loop struct {
	mu      sync.Mutex
	queue   []func()
	running bool
}

func newLoop() *loop { return &loop{} }

// post queues fn without waiting for it.
func (l *loop) post(fn func()) {
	l.mu.Lock()
	l.queue = append(l.queue, fn)
	if l.running {
		l.mu.Unlock()
		return
	}
	l.running = true
	l.mu.Unlock()
	go l.drain()
}

func (l *loop) drain() {
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.running = false
			l.queue = nil
			l.mu.Unlock()
			return
		}
		fn := l.queue[0]
		l.queue[0] = nil
		l.queue = l.queue[1:]
		l.mu.Unlock()
		fn()
	}
}

// call runs fn in the loop and waits for it.
func (l *loop) call(fn func()) {
	done := make(chan struct{})
	l.post(func() {
		defer close(done)
		fn()
	})
	<-done
}

// timer is a timeout whose callback runs in the loop. Cleared from inside the loop, it never fires.
type timer struct {
	t       *time.Timer
	cleared bool
}

// after runs fn in the loop once d has passed, unless the timer is cleared first.
func (l *loop) after(d time.Duration, fn func()) *timer {
	held := &timer{}
	held.t = time.AfterFunc(max(d, 0), func() {
		l.post(func() {
			if !held.cleared {
				held.cleared = true
				fn()
			}
		})
	})
	return held
}

// clear stops the timer; it must be called from inside the loop.
func (t *timer) clear() {
	if t == nil {
		return
	}
	t.cleared = true
	t.t.Stop()
}
