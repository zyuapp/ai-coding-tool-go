package browser

import (
	"maps"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// publisher hands events to listeners in order on a goroutine of its own, so the UI thread that
// reports them never waits on a listener.
type publisher struct {
	mu     sync.Mutex
	queue  []func()
	next   int
	pages  map[int]func(contracts.BrowserPageEvent)
	finds  map[int]func(contracts.BrowserFindEvent)
	runner bool
}

func newPublisher() *publisher {
	p := &publisher{pages: map[int]func(contracts.BrowserPageEvent){}, finds: map[int]func(contracts.BrowserFindEvent){}}
	return p
}

func (p *publisher) enqueue(deliver func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queue = append(p.queue, deliver)
	if !p.runner {
		p.runner = true
		go p.run()
	}
}

func (p *publisher) run() {
	p.mu.Lock()
	for len(p.queue) > 0 {
		deliver := p.queue[0]
		p.queue[0] = nil
		p.queue = p.queue[1:]
		p.mu.Unlock()
		deliver()
		p.mu.Lock()
	}
	p.queue = nil
	p.runner = false
	p.mu.Unlock()
}

func (p *publisher) page(event contracts.BrowserPageEvent) {
	p.enqueue(func() {
		p.mu.Lock()
		listeners := make([]func(contracts.BrowserPageEvent), 0, len(p.pages))
		for _, id := range sortedKeys(p.pages) {
			listeners = append(listeners, p.pages[id])
		}
		p.mu.Unlock()
		for _, listener := range listeners {
			listener(event)
		}
	})
}

func (p *publisher) find(event contracts.BrowserFindEvent) {
	p.enqueue(func() {
		p.mu.Lock()
		listeners := make([]func(contracts.BrowserFindEvent), 0, len(p.finds))
		for _, id := range sortedKeys(p.finds) {
			listeners = append(listeners, p.finds[id])
		}
		p.mu.Unlock()
		for _, listener := range listeners {
			listener(event)
		}
	})
}

func sortedKeys[V any](entries map[int]V) []int { return slices.Sorted(maps.Keys(entries)) }

func (p *publisher) subscribePage(listener func(contracts.BrowserPageEvent)) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := p.next
	p.pages[id] = listener
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.pages, id)
	}
}

func (p *publisher) subscribeFind(listener func(contracts.BrowserFindEvent)) func() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	id := p.next
	p.finds[id] = listener
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		delete(p.finds, id)
	}
}
