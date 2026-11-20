// Package services opens and closes everything the runtime's desktop is answered from, whether a
// window is drawn over it or a headless server runs it: ports the startup of src/main/main.ts,
// service-desktop.ts and serve.ts's service half.
package services

import (
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Topic is one channel of the desktop's events: its listeners, told in the order they subscribed.
type Topic[T any] struct {
	mu   sync.Mutex
	next int
	ids  []int
	held map[int]func(T)
}

// On adds a listener until the returned func is called.
func (t *Topic[T]) On(listener func(T)) func() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.held == nil {
		t.held = map[int]func(T){}
	}
	id := t.next
	t.next++
	t.held[id] = listener
	t.ids = append(t.ids, id)
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, ok := t.held[id]; ok {
			delete(t.held, id)
			t.ids = slices.DeleteFunc(slices.Clone(t.ids), func(held int) bool { return held == id })
		}
	}
}

// Emit tells every listener, and answers whether anyone heard it: a push nobody listens for is what
// a request has to be refused over.
func (t *Topic[T]) Emit(payload T) bool {
	t.mu.Lock()
	listeners := make([]func(T), 0, len(t.ids))
	for _, id := range t.ids {
		listeners = append(listeners, t.held[id])
	}
	t.mu.Unlock()
	for _, listener := range listeners {
		listener(payload)
	}
	return len(listeners) > 0
}

// ComputersChanged is the "computers:changed" payload.
type ComputersChanged struct {
	Name  string
	Links []*domain.ComputerLink
}

// ComputerState is the "computer:state" payload.
type ComputerState struct {
	ID    string
	State application.WorkspaceState
}

// ComputerNotice is the "computer:notice" payload.
type ComputerNotice struct {
	ID     string
	Notice contracts.ThreadNotice
}

// Events is everything main pushes at the runtime, named by the channel each travelled on when the
// runtime was a window (desktop-events.ts).
type Events struct {
	RunEvent          Topic[contracts.AgentEvent]
	AutomationFire    Topic[contracts.AutomationFire]
	AutomationChanged Topic[[]*domain.AutomationView]
	ThreadRequest     Topic[contracts.ThreadRequest]
	MobileChanged     Topic[domain.MobileServerState]
	UpdateChanged     Topic[domain.AppUpdate]
	MobileRequest     Topic[contracts.MobileRequest]
	BrowserEvent      Topic[contracts.BrowserPageEvent]
	BrowserFind       Topic[contracts.BrowserFindEvent]
	TerminalEvent     Topic[domain.TerminalUpdate]
	OpenProject       Topic[domain.WorkspaceRecord]
	ComputersChanged  Topic[ComputersChanged]
	ComputerState     Topic[ComputerState]
	ComputerNotice    Topic[ComputerNotice]
}

func (e *Events) OnOpenProject(listener func(workspace domain.WorkspaceRecord)) func() {
	return e.OpenProject.On(listener)
}

func (e *Events) OnAgentEvent(listener func(event contracts.AgentEvent)) func() {
	return e.RunEvent.On(listener)
}

func (e *Events) OnThreadRequest(listener func(request contracts.ThreadRequest)) func() {
	return e.ThreadRequest.On(listener)
}

func (e *Events) OnTerminalEvent(listener func(update domain.TerminalUpdate)) func() {
	return e.TerminalEvent.On(listener)
}

func (e *Events) OnAppUpdate(listener func(update domain.AppUpdate)) func() {
	return e.UpdateChanged.On(listener)
}

func (e *Events) OnAutomationsChanged(listener func(automations []*domain.AutomationView)) func() {
	return e.AutomationChanged.On(listener)
}

func (e *Events) OnAutomationFire(listener func(fire contracts.AutomationFire)) func() {
	return e.AutomationFire.On(listener)
}

func (e *Events) OnBrowserEvent(listener func(event contracts.BrowserPageEvent)) func() {
	return e.BrowserEvent.On(listener)
}

func (e *Events) OnBrowserFind(listener func(event contracts.BrowserFindEvent)) func() {
	return e.BrowserFind.On(listener)
}
