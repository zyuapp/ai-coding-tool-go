package agent

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// What every engine's provider shares: the pool its threads' sessions are kept warm in, each read back
// as the engine's own (S), and the filings that hold a thread while it is archived or restored.
// Engines embed it.
type PooledProvider[S PooledSession] struct {
	Pool *SessionPool

	mu sync.Mutex
	// The last filing asked of each thread, so an archive and a restore land in the order given and a run waits for both.
	filings map[string]chan struct{}
}

func NewPooledProvider[S PooledSession](pool *SessionPool) *PooledProvider[S] {
	return &PooledProvider[S]{Pool: pool, filings: map[string]chan struct{}{}}
}

func (provider *PooledProvider[S]) RetireOutOfRole(taskID string, role *domain.CoordinationRole) {
	provider.Pool.RetireOutOfRole(taskID, role)
}

func (provider *PooledProvider[S]) CloseAll() {
	provider.Pool.CloseAll()
}

// Runs on the thread's session. A thread still being filed away or brought back is held by that filing until it lands.
func (provider *PooledProvider[S]) Run(input *ProviderRunInput, key string, opener SessionOpener) (ProviderResult, error) {
	provider.mu.Lock()
	filing := provider.filings[input.TaskID]
	provider.mu.Unlock()
	if filing != nil {
		<-filing
	}
	return provider.Pool.Execute(input, key, opener)
}

// The thread's live session, when this engine opened it.
func (provider *PooledProvider[S]) Session(taskID string) (S, bool) {
	session, ok := provider.Pool.LiveSession(taskID).(S)
	return session, ok
}

// Files the thread after its last filing has landed. A filing that fails changes nothing, and holds nothing up.
func (provider *PooledProvider[S]) File(taskID string, filing func() error) {
	provider.mu.Lock()
	previous := provider.filings[taskID]
	queued := make(chan struct{})
	provider.filings[taskID] = queued
	provider.mu.Unlock()
	go func() {
		if previous != nil {
			<-previous
		}
		_ = filing()
		provider.mu.Lock()
		if provider.filings[taskID] == queued {
			delete(provider.filings, taskID)
		}
		provider.mu.Unlock()
		close(queued)
	}()
}
