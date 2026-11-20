package store

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"sync"
)

// ErrClosing answers a request made once the store has begun to close.
var ErrClosing = errors.New("Task database is closing.")

// Pending is a request's answer, which arrives once every request sent before it has run.
type Pending[T any] struct {
	done  chan struct{}
	value T
	err   error
}

func newPending[T any]() *Pending[T] { return &Pending[T]{done: make(chan struct{})} }

func failed[T any](err error) *Pending[T] {
	pending := newPending[T]()
	pending.settle(*new(T), err)
	return pending
}

func (p *Pending[T]) settle(value T, err error) {
	p.value, p.err = value, err
	close(p.done)
}

// Done is closed once the answer has arrived.
func (p *Pending[T]) Done() <-chan struct{} { return p.done }

// Wait blocks until the answer arrives.
func (p *Pending[T]) Wait() (T, error) {
	<-p.done
	return p.value, p.err
}

// WaitContext waits for the answer, or for ctx to end; the request still runs either way.
func (p *Pending[T]) WaitContext(ctx context.Context) (T, error) {
	select {
	case <-p.done:
		return p.value, p.err
	case <-ctx.Done():
		return *new(T), ctx.Err()
	}
}

type request struct {
	run func(*TaskDatabase) (stop bool)
}

// TaskDatabaseService runs the TaskDatabase on one goroutine of its own, so SQLite and JSON work
// never block the caller. Requests run in the order they are sent: a Flush or Close answers once
// every write sent before it has landed.
type TaskDatabaseService struct {
	mu      sync.Mutex
	queue   []request
	wake    chan struct{}
	failure error
	closing *Pending[struct{}]
	stopped chan struct{}
}

// OpenTaskDatabaseService starts the store's goroutine and waits for the store to open, which may
// migrate an older one.
func OpenTaskDatabaseService(file string, options OpenOptions) (*TaskDatabaseService, error) {
	service := &TaskDatabaseService{wake: make(chan struct{}, 1), stopped: make(chan struct{})}
	ready := make(chan error, 1)
	go service.work(file, options, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return service, nil
}

func (s *TaskDatabaseService) work(file string, options OpenOptions, ready chan<- error) {
	defer close(s.stopped)
	database, err := OpenTaskDatabase(file, options)
	ready <- err
	if err != nil {
		return
	}
	for {
		s.mu.Lock()
		for len(s.queue) == 0 {
			s.mu.Unlock()
			<-s.wake
			s.mu.Lock()
		}
		next := s.queue[0]
		s.queue[0] = request{}
		s.queue = s.queue[1:]
		s.mu.Unlock()
		if s.execute(database, next) {
			return
		}
	}
}

// execute runs one request; a panic answers it as a failure and leaves the store serving.
func (s *TaskDatabaseService) execute(database *TaskDatabase, next request) (stop bool) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stop = false
		}
	}()
	return next.run(database)
}

// enqueue sends a request unless the store has failed or is closing.
func enqueue[T any](s *TaskDatabaseService, operation func(*TaskDatabase) (T, error)) *Pending[T] {
	pending := newPending[T]()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return failed[T](s.failure)
	}
	if s.closing != nil {
		return failed[T](ErrClosing)
	}
	s.push(request{run: func(database *TaskDatabase) bool {
		settled := false
		defer func() {
			if recovered := recover(); recovered != nil && !settled {
				pending.settle(*new(T), fmt.Errorf("%v", recovered))
			}
		}()
		value, err := operation(database)
		settled = true
		pending.settle(value, err)
		return false
	}})
	return pending
}

func (s *TaskDatabaseService) push(next request) {
	s.queue = append(s.queue, next)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *TaskDatabaseService) Load() *Pending[*LoadedTaskStore] {
	return enqueue(s, func(database *TaskDatabase) (*LoadedTaskStore, error) { return database.Load(false) })
}

func (s *TaskDatabaseService) LoadSummaries() *Pending[*LoadedTaskStore] {
	return enqueue(s, func(database *TaskDatabase) (*LoadedTaskStore, error) { return database.Load(true) })
}

func (s *TaskDatabaseService) LoadThreadMessages(taskID string) *Pending[[]jsontext.Value] {
	return enqueue(s, func(database *TaskDatabase) ([]jsontext.Value, error) { return database.LoadThreadMessages(taskID) })
}

func (s *TaskDatabaseService) SearchThreadMessages(search string, taskIDs []string) *Pending[[]string] {
	return enqueue(s, func(database *TaskDatabase) ([]string, error) { return database.SearchThreadMessages(search, taskIDs) })
}

func (s *TaskDatabaseService) AttachmentPaths() *Pending[[]string] {
	return enqueue(s, func(database *TaskDatabase) ([]string, error) { return database.AttachmentPaths() })
}

func (s *TaskDatabaseService) SubagentActivity(taskID, subagentID string) *Pending[[]jsontext.Value] {
	return enqueue(s, func(database *TaskDatabase) ([]jsontext.Value, error) {
		return database.SubagentActivity(taskID, subagentID)
	})
}

// Persist writes a TaskStoreDelta, or a value that encodes to one. A typed value is encoded on the
// store's goroutine, so it must not change after it is handed over.
func (s *TaskDatabaseService) Persist(delta any) *Pending[struct{}] {
	return enqueue(s, func(database *TaskDatabase) (struct{}, error) { return struct{}{}, database.Persist(delta) })
}

func (s *TaskDatabaseService) ListAutomations() *Pending[[]jsontext.Value] {
	return enqueue(s, func(database *TaskDatabase) ([]jsontext.Value, error) { return database.ListAutomations() })
}

func (s *TaskDatabaseService) SaveAutomation(automation any) *Pending[struct{}] {
	return enqueue(s, func(database *TaskDatabase) (struct{}, error) { return struct{}{}, database.SaveAutomation(automation) })
}

func (s *TaskDatabaseService) DeleteAutomation(id string) *Pending[struct{}] {
	return enqueue(s, func(database *TaskDatabase) (struct{}, error) { return struct{}{}, database.DeleteAutomation(id) })
}

// Flush answers once every request sent before it has run.
func (s *TaskDatabaseService) Flush() *Pending[struct{}] {
	return enqueue(s, func(*TaskDatabase) (struct{}, error) { return struct{}{}, nil })
}

// Close runs every request already sent, then closes the store. Calls while it is closing share
// one answer; a close that fails leaves the store open for another try.
func (s *TaskDatabaseService) Close() *Pending[struct{}] {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing != nil {
		return s.closing
	}
	if s.failure != nil {
		return failed[struct{}](s.failure)
	}
	closing := newPending[struct{}]()
	s.closing = closing
	s.push(request{run: func(database *TaskDatabase) bool {
		err := database.Close()
		if err != nil {
			s.mu.Lock()
			s.closing = nil
			s.mu.Unlock()
			closing.settle(struct{}{}, err)
			return false
		}
		s.mu.Lock()
		s.failure = ErrClosing
		s.mu.Unlock()
		closing.settle(struct{}{}, nil)
		return true
	}})
	return closing
}
