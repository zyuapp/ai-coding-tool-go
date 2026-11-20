package codex

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

const (
	metadataSlots         = 4
	metadataUnknownTTL    = 30_000
	metadataUnknownMax    = 256
	defaultMetadataReadIn = 20 * time.Second
)

type metadataRead struct {
	done     chan struct{}
	metadata domain.SubagentMetadata
}

type cachedMetadata struct {
	at       float64
	metadata domain.SubagentMetadata
}

var metadataReads = struct {
	sync.Mutex
	pending     map[string]*metadataRead
	unavailable map[string]cachedMetadata
	// unavailable's ids, oldest first.
	order   []string
	reading int
	waiting []chan struct{}
}{pending: map[string]*metadataRead{}, unavailable: map[string]cachedMetadata{}}

func forgetUnavailable(id string) {
	if _, ok := metadataReads.unavailable[id]; ok {
		delete(metadataReads.unavailable, id)
		metadataReads.order = slices.DeleteFunc(metadataReads.order, func(held string) bool { return held == id })
	}
}

// Inspecting a saved child can recover settings even when its original session is no longer
// running. Concurrent reads of one child share one request. Nil connect is ConnectAppServer;
// timeout <= 0 is 20 seconds.
func ReadCodexSubagentMetadata(ctx context.Context, id string, connect CodexConnect, timeout time.Duration) domain.SubagentMetadata {
	if connect == nil {
		connect = ConnectAppServer
	}
	if timeout <= 0 {
		timeout = defaultMetadataReadIn
	}
	metadataReads.Lock()
	if held := metadataReads.pending[id]; held != nil {
		metadataReads.Unlock()
		<-held.done
		return held.metadata
	}
	if cached, ok := metadataReads.unavailable[id]; ok && application.Now()-cached.at < metadataUnknownTTL {
		metadataReads.Unlock()
		return cached.metadata
	}
	forgetUnavailable(id)
	reading := &metadataRead{done: make(chan struct{})}
	metadataReads.pending[id] = reading
	metadataReads.Unlock()

	metadata := readWithSlot(ctx, id, connect, timeout)
	metadataReads.Lock()
	if metadata.Model == nil || metadata.Effort == nil {
		forgetUnavailable(id)
		metadataReads.unavailable[id] = cachedMetadata{at: application.Now(), metadata: metadata}
		metadataReads.order = append(metadataReads.order, id)
		if len(metadataReads.unavailable) > metadataUnknownMax {
			forgetUnavailable(metadataReads.order[0])
		}
	}
	delete(metadataReads.pending, id)
	metadataReads.Unlock()
	reading.metadata = metadata
	close(reading.done)
	return metadata
}

// Rapid inspection changes share four process slots; absent settings are briefly cached above.
func readWithSlot(ctx context.Context, id string, connect CodexConnect, timeout time.Duration) domain.SubagentMetadata {
	metadataReads.Lock()
	if metadataReads.reading < metadataSlots {
		metadataReads.reading++
		metadataReads.Unlock()
	} else {
		turn := make(chan struct{})
		metadataReads.waiting = append(metadataReads.waiting, turn)
		metadataReads.Unlock()
		<-turn
	}
	defer func() {
		metadataReads.Lock()
		defer metadataReads.Unlock()
		if len(metadataReads.waiting) > 0 {
			next := metadataReads.waiting[0]
			metadataReads.waiting = metadataReads.waiting[1:]
			close(next)
		} else {
			metadataReads.reading--
		}
	}()
	return readMetadata(ctx, id, connect, timeout)
}

// Old or deleted provider threads have no recoverable settings; the inspector keeps its empty fields.
func readMetadata(ctx context.Context, id string, connect CodexConnect, timeout time.Duration) domain.SubagentMetadata {
	command, err := CodexAppServer(bareServer, AppServerOptions{})
	if err != nil {
		return domain.SubagentMetadata{}
	}
	client := connect(command)
	defer client.Close()
	metadata, err := withTimeout(ctx, timeout, func(ctx context.Context) (domain.SubagentMetadata, error) {
		if err := initialize(ctx, client); err != nil {
			return domain.SubagentMetadata{}, err
		}
		read, err := call[protocol.ThreadResponse](ctx, client, "thread/read", protocol.ThreadReadParams{ThreadID: id, IncludeTurns: ptr(false)})
		if err != nil || read.Thread.ID != id {
			return domain.SubagentMetadata{}, err
		}
		var metadata domain.SubagentMetadata
		if deref(read.Thread.Model) != "" {
			metadata.Model = read.Thread.Model
		}
		if deref(read.Thread.ReasoningEffort) != "" {
			metadata.Effort = read.Thread.ReasoningEffort
		}
		return metadata, nil
	})
	if err != nil {
		return domain.SubagentMetadata{}
	}
	return metadata
}
