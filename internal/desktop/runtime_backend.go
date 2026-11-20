package desktop

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"sync"
	"sync/atomic"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
)

var errRuntimeClosed = errors.New("The workspace runtime has closed.")

// RuntimeBackend hosts the application in this process (workspace-runtime-host.ts). The window is a
// view of it: it subscribes to revisions, submits inputs, and is handed the few effects that live in
// its own views.
type RuntimeBackend struct {
	Runtime   *host.Runtime
	Publisher *host.Publisher
	storage   *store.JSONStorage

	closed atomic.Bool
	// Requests wait for the store to load in arrival order, so an early window never sees the empty state.
	queue chan func()

	mu       sync.Mutex
	views    map[int]WorkspaceView
	nextView int
}

var _ WorkspaceBackend = (*RuntimeBackend)(nil)

// NewRuntimeBackend makes the runtime over desktop, keeping drafts and view preferences in storage.
func NewRuntimeBackend(desktop host.RuntimeDesktop, storage *store.JSONStorage) *RuntimeBackend {
	backend := &RuntimeBackend{storage: storage, queue: make(chan func(), 4096), views: map[int]WorkspaceView{}}
	backend.Runtime = host.CreateWorkspaceRuntime(host.WorkspaceRuntimeHost{
		Desktop: desktop,
		Storage: storage.Window(),
		Surface: backend.surface,
	})
	backend.Publisher = host.CreateRuntimePublisher(backend.Runtime)
	backend.Publisher.Subscribe(backend.update)
	go backend.serve()
	return backend
}

func (b *RuntimeBackend) serve() {
	for request := range b.queue {
		request()
	}
}

func (b *RuntimeBackend) eachView(send func(WorkspaceView)) {
	b.mu.Lock()
	views := make([]WorkspaceView, 0, len(b.views))
	for _, view := range b.views {
		views = append(views, view)
	}
	b.mu.Unlock()
	for _, view := range views {
		send(view)
	}
}

func (b *RuntimeBackend) update(update application.WorkspaceUpdate) {
	data, err := jsonx.Marshal(update)
	if err != nil {
		log.Printf("Could not encode a workspace update: %v", err)
		return
	}
	b.eachView(func(view WorkspaceView) { view.Update(data) })
}

func (b *RuntimeBackend) surface(effect application.WorkspaceSurfaceEffect) {
	data, err := jsonx.Marshal(effect)
	if err != nil {
		log.Printf("Could not encode a surface effect: %v", err)
		return
	}
	b.eachView(func(view WorkspaceView) { view.Surface(data) })
}

// loaded waits for the store, answering false once the runtime has closed.
func (b *RuntimeBackend) loaded() bool {
	if b.closed.Load() {
		return false
	}
	<-b.Runtime.Loaded()
	return !b.closed.Load()
}

func encodeResult(result application.WorkspaceResponseResult) (jsontext.Value, error) {
	return jsonx.Marshal(result)
}

// Request takes the window's input in arrival order: nil asks for a whole snapshot.
func (b *RuntimeBackend) Request(input jsontext.Value) Reply {
	reply := make(chan Result, 1)
	if b.closed.Load() {
		reply <- Result{Err: errRuntimeClosed}
		return reply
	}
	var decoded application.WorkspaceInput
	if input != nil {
		var value any
		if err := jsonx.Unmarshal(input, &value); err != nil || !contracts.IsWorkspaceViewInput(value) {
			reply <- Result{Err: errors.New("Invalid workspace input.")}
			return reply
		}
		if err := jsonx.Unmarshal(input, &decoded); err != nil {
			reply <- Result{Err: errors.New("Invalid workspace input.")}
			return reply
		}
	}
	b.enqueue(func() {
		if !b.loaded() {
			reply <- Result{Err: errRuntimeClosed}
			return
		}
		if decoded == nil {
			snapshot := b.Publisher.Snapshot()
			b.update(snapshot)
			value, err := encodeResult(contracts.ComputerServerMessageResultResultTrue{Revision: snapshot.Revision})
			reply <- Result{Value: value, Err: err}
			return
		}
		answer := b.Publisher.Begin(decoded)
		go func() {
			value, err := encodeResult(answer(context.Background()))
			reply <- Result{Value: value, Err: err}
		}()
	})
	return reply
}

func (b *RuntimeBackend) enqueue(request func()) { b.queue <- request }

// Dispatch runs an input the desktop raised itself, such as a menu's, as the window's buttons do.
func (b *RuntimeBackend) Dispatch(ctx context.Context, input application.WorkspaceInput) (application.WorkspaceResponseResult, error) {
	if !b.loaded() {
		return nil, errRuntimeClosed
	}
	return b.Publisher.Request(ctx, input), nil
}

// Migrate takes on values a window kept from hosting the runtime itself, once, without a restart.
func (b *RuntimeBackend) Migrate(values map[string]string) Reply {
	return Async(func() (jsontext.Value, error) {
		if !b.loaded() {
			return nil, errRuntimeClosed
		}
		changed, err := b.storage.Adopt(values)
		if err != nil || !changed {
			return nil, err
		}
		preferences := host.LoadViewPreferences(b.storage.Window(), nil)
		preferences.BrowserTabs = nil
		ctx := context.Background()
		if err := b.Runtime.Dispatch(ctx, application.WorkspaceEventPreferencesLoaded{Preferences: preferences}); err != nil {
			return nil, err
		}
		return nil, b.Runtime.RestoreDrafts(ctx)
	})
}

func (b *RuntimeBackend) Subscribe(view WorkspaceView) func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextView
	b.nextView++
	b.views[id] = view
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.views, id)
	}
}

func (b *RuntimeBackend) SetViewVisible(visible bool) { b.Runtime.SetViewVisible(visible) }
func (b *RuntimeBackend) SetViewInUse(inUse bool)     { b.Runtime.SetViewInUse(inUse) }

// Start starts the runtime: its subscriptions, and loading the store.
func (b *RuntimeBackend) Start() <-chan struct{} { return b.Runtime.Start() }

// Flush waits for every input, effect and write in flight.
func (b *RuntimeBackend) Flush(ctx context.Context) error {
	if failure, ok := b.Publisher.Flush(ctx).(contracts.ComputerServerMessageResultResultFalse); ok {
		return errors.New(failure.Message)
	}
	return nil
}

// Close refuses every later request and stops the runtime.
func (b *RuntimeBackend) Close() {
	if b.closed.Swap(true) {
		return
	}
	b.Publisher.Dispose()
	b.Runtime.Dispose()
}
