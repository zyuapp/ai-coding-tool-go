package mobile

import (
	"context"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// BridgeOptions is what the bridge needs from the app: where files live, and what another computer
// is handed and may drive (nil takes no computers).
type BridgeOptions struct {
	UserData  string
	Workspace WorkspaceHooks
	// Tailscale and Port are as in HostOptions: nil drives the machine's Tailscale on the default port.
	Tailscale TailscaleHooks
	Port      *int
}

// Bridge is the phone bridge as the app holds it: it lives as long as the app rather than the window,
// because what a phone or another computer asks is answered by the workspace runtime in this process.
// It implements contracts.MobileDesktopAPI; the runtime subscribes to OnMobileRequest to answer
// phones and calls PublishMobileView with what they should see.
type Bridge struct {
	host *Host
	// lifecycle: a start that follows a stop waits for the previous host to have stopped.
	lifecycle serial

	mu         sync.Mutex
	generation int
	running    bool
	nextID     int
	states     map[int]func(domain.MobileServerState)
	requests   map[int]func(contracts.MobileRequest)
	release    func()
}

var _ contracts.MobileDesktopAPI = (*Bridge)(nil)

func NewBridge() *Bridge {
	return &Bridge{host: NewHost(), states: map[int]func(domain.MobileServerState){}, requests: map[int]func(contracts.MobileRequest){}}
}

// Host is the bridge's host, for the computer-facing calls (NoticeComputers) a headless serve makes.
func (b *Bridge) Host() *Host { return b.host }

func (b *Bridge) current(generation int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running && b.generation == generation
}

func (b *Bridge) emitRequest(request contracts.MobileRequest) bool {
	b.mu.Lock()
	listeners := make([]func(contracts.MobileRequest), 0, len(b.requests))
	for id := range b.nextID {
		if listener, ok := b.requests[id]; ok {
			listeners = append(listeners, listener)
		}
	}
	b.mu.Unlock()
	for _, listener := range listeners {
		listener(request)
	}
	return len(listeners) > 0
}

func (b *Bridge) publishState(generation int, state domain.MobileServerState) {
	if !b.current(generation) {
		return
	}
	b.mu.Lock()
	listeners := make([]func(domain.MobileServerState), 0, len(b.states))
	for id := range b.nextID {
		if listener, ok := b.states[id]; ok {
			listeners = append(listeners, listener)
		}
	}
	b.mu.Unlock()
	for _, listener := range listeners {
		listener(state)
	}
	b.keepAwake(len(state.Sessions) > 0)
}

// keepAwake: a phone on the line is a user at the keyboard, so the Mac may not sleep out from under
// it. Every session counts, not only the connected ones: a phone that locks its screen mid-run drops
// its socket, and letting the Mac sleep then would kill the run that phone is waiting on. A session
// outlives its socket by five minutes, which is the window this holds the machine awake for.
//
// This is idle sleep only. Nothing an application can assert keeps a Mac awake with the lid shut.
func (b *Bridge) keepAwake(needed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if needed == (b.release != nil) {
		return
	}
	if needed {
		b.release = holdAwake()
		return
	}
	b.release()
	b.release = nil
}

// Start starts the bridge. It returns once the host has read its settings and started the server
// the user left on.
func (b *Bridge) Start(options BridgeOptions) {
	b.mu.Lock()
	b.generation++
	generation := b.generation
	b.running = true
	b.mu.Unlock()
	b.lifecycle.run(func() {
		if !b.current(generation) {
			return
		}
		b.host.Start(&HostOptions{
			UserData:  options.UserData,
			Send:      func(request contracts.MobileRequest) bool { return b.current(generation) && b.emitRequest(request) },
			OnState:   func(state domain.MobileServerState) { b.publishState(generation, state) },
			Workspace: options.Workspace,
			Tailscale: options.Tailscale,
			Port:      options.Port,
		})
	})
}

func (b *Bridge) Stop() {
	b.mu.Lock()
	b.running = false
	b.mu.Unlock()
	b.keepAwake(false)
	b.lifecycle.run(b.host.Stop)
}

// Recheck looks at Serve again at once after the machine wakes, rather than at the next minute.
func (b *Bridge) Recheck() {
	b.mu.Lock()
	running := b.running
	b.mu.Unlock()
	if running {
		b.host.Recheck()
	}
}

func (b *Bridge) MobileState(context.Context) (*domain.MobileServerState, error) {
	b.lifecycle.wait()
	state := b.host.State()
	return &state, nil
}

func (b *Bridge) SetMobileEnabled(_ context.Context, enabled bool) (*domain.MobileServerState, error) {
	b.lifecycle.wait()
	state, err := b.host.SetEnabled(enabled)
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (b *Bridge) CreateMobilePairingCode(context.Context) (*domain.MobilePairingOffer, error) {
	b.lifecycle.wait()
	return b.host.CreatePairingCode()
}

func (b *Bridge) RevokeMobileDevice(_ context.Context, deviceID string) (*domain.MobileServerState, error) {
	b.lifecycle.wait()
	state := b.host.RevokeDevice(deviceID)
	return &state, nil
}

func (b *Bridge) RefreshTailscale(context.Context) (*domain.MobileServerState, error) {
	b.lifecycle.wait()
	state := b.host.RefreshTailscale()
	return &state, nil
}

// AnnounceName tells the computers on the line what this one now calls itself.
func (b *Bridge) AnnounceName(context.Context) error {
	b.lifecycle.wait()
	b.host.AnnounceComputerName()
	return nil
}

// NotifyPhones hands a notice to the phones that take them while closed.
func (b *Bridge) NotifyPhones(ctx context.Context, notice contracts.ThreadNotice) {
	b.host.NoticePhones(ctx, notice)
}

// NoticeComputers hands a notice to the computers on the line.
func (b *Bridge) NoticeComputers(notice contracts.ThreadNotice) {
	b.host.NoticeComputers(notice)
}

func (b *Bridge) OnMobileState(listener func(state domain.MobileServerState)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	b.states[id] = listener
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.states, id)
	}
}

func (b *Bridge) OnMobileRequest(listener func(request contracts.MobileRequest)) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	b.requests[id] = listener
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.requests, id)
	}
}

func (b *Bridge) AnswerMobileRequest(response contracts.MobileResponse) {
	b.host.AnswerRequest(response)
}

func (b *Bridge) PublishMobileView(update contracts.MobileViewUpdate) {
	b.host.PublishView(update)
}
