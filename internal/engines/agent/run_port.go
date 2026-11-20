package agent

import (
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
)

// Fans what the agent side reports out to the window's listeners: the TS main process's
// "run:event" and "thread:request" emitter channels. Use Publish and Ask as RunHostConfig's.
type RunEvents struct {
	mu       sync.Mutex
	next     int
	events   map[int]func(event contracts.AgentEvent)
	requests map[int]func(request contracts.ThreadRequest)
}

func NewRunEvents() *RunEvents {
	return &RunEvents{events: map[int]func(contracts.AgentEvent){}, requests: map[int]func(contracts.ThreadRequest){}}
}

func (events *RunEvents) Publish(event contracts.AgentEvent) {
	events.mu.Lock()
	listeners := sortedListeners(events.events)
	events.mu.Unlock()
	for _, listener := range listeners {
		listener(event)
	}
}

// Whether anyone was there to take the request.
func (events *RunEvents) Ask(request contracts.ThreadRequest) bool {
	events.mu.Lock()
	listeners := sortedListeners(events.requests)
	events.mu.Unlock()
	for _, listener := range listeners {
		listener(request)
	}
	return len(listeners) > 0
}

func sortedListeners[T any](listeners map[int]T) []T {
	ids := make([]int, 0, len(listeners))
	for id := range listeners {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	ordered := make([]T, 0, len(ids))
	for _, id := range ids {
		ordered = append(ordered, listeners[id])
	}
	return ordered
}

func subscribe[T any](events *RunEvents, listeners map[int]T, listener T) func() {
	events.mu.Lock()
	defer events.mu.Unlock()
	id := events.next
	events.next++
	listeners[id] = listener
	return func() {
		events.mu.Lock()
		defer events.mu.Unlock()
		delete(listeners, id)
	}
}

// The RunPort runtime the host calls: commands go to the run host, events come back through RunEvents.
type RunPort struct {
	Bridge RunBridge
	Events *RunEvents
}

var _ contracts.RunPortRuntime = (*RunPort)(nil)

func (port *RunPort) Send(command contracts.RunCommand) { port.Bridge.Submit(command) }

func (port *RunPort) OnAgentEvent(listener func(event contracts.AgentEvent)) func() {
	return subscribe(port.Events, port.Events.events, listener)
}

func (port *RunPort) OnThreadRequest(listener func(request contracts.ThreadRequest)) func() {
	return subscribe(port.Events, port.Events.requests, listener)
}

func (port *RunPort) AnswerThreadRequest(response contracts.ThreadResponse) {
	port.Bridge.AnswerThread(response)
}
