package devices

import "sync"

// turns runs work for each key one piece at a time, in the order it came, whether the work before it
// failed or not.
type turns struct {
	mu    sync.Mutex
	tails map[string]chan struct{}
}

func runInTurn[Value any](queue *turns, key string, work func() (Value, error)) (Value, error) {
	queue.mu.Lock()
	if queue.tails == nil {
		queue.tails = map[string]chan struct{}{}
	}
	previous := queue.tails[key]
	done := make(chan struct{})
	queue.tails[key] = done
	queue.mu.Unlock()
	if previous != nil {
		<-previous
	}
	defer func() {
		close(done)
		queue.mu.Lock()
		if queue.tails[key] == done {
			delete(queue.tails, key)
		}
		queue.mu.Unlock()
	}()
	return work()
}

func inTurn(queue *turns, key string, work func() error) error {
	_, err := runInTurn(queue, key, func() (struct{}, error) { return struct{}{}, work() })
	return err
}
