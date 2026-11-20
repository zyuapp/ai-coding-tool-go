package application

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func InitialThreadsState() ThreadsState {
	return ThreadsState{Threads: []*domain.Thread{}}
}

// threadsMemo remembers one filter of the threads list, keyed by the list's identity (the TypeScript WeakMap).
type threadsMemo struct {
	mu    sync.Mutex
	key   []*domain.Thread
	found []*domain.Thread
}

var (
	remotelyLed threadsMemo
	leadsAway   threadsMemo
)

func RemotelyLedThreads(threads []*domain.Thread) []*domain.Thread {
	return remotelyLed.filter(threads, func(thread *domain.Thread) bool {
		return domain.RemoteLeadOf(thread) != nil && thread.ArchivedAt == nil
	})
}

func LeadAwayThreads(threads []*domain.Thread) []*domain.Thread {
	return leadsAway.filter(threads, func(thread *domain.Thread) bool { return domain.AwayLeadOf(thread) != nil && thread.ArchivedAt == nil })
}

func (m *threadsMemo) filter(threads []*domain.Thread, keep func(*domain.Thread) bool) []*domain.Thread {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.found != nil && jsonx.Same(m.key, threads) {
		return m.found
	}
	found := []*domain.Thread{}
	for _, thread := range threads {
		if keep(thread) {
			found = append(found, thread)
		}
	}
	m.key, m.found = threads, found
	return found
}
