package mobile

import "sync"

// serial runs work one after another in the order it was handed over, as a promise chain does.
type serial struct {
	mu   sync.Mutex
	tail chan struct{}
}

func (q *serial) run(work func()) {
	q.mu.Lock()
	previous := q.tail
	done := make(chan struct{})
	q.tail = done
	q.mu.Unlock()
	defer close(done)
	if previous != nil {
		<-previous
	}
	work()
}

// wait returns once everything handed over so far has run.
func (q *serial) wait() {
	q.mu.Lock()
	tail := q.tail
	q.mu.Unlock()
	if tail != nil {
		<-tail
	}
}
