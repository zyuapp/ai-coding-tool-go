package desktop

import (
	"context"
	"encoding/json/jsontext"
)

// Result is what one window call settles with: a JSON value, or the error its promise rejects with.
type Result struct {
	Value jsontext.Value
	Err   error
}

// Reply is a call that has started and settles once, later.
type Reply <-chan Result

// Settled is a reply that has already settled.
func Settled(value jsontext.Value, err error) Reply {
	reply := make(chan Result, 1)
	reply <- Result{Value: value, Err: err}
	return reply
}

// Async runs work on its own goroutine and settles the reply with what it returns.
func Async(work func() (jsontext.Value, error)) Reply {
	reply := make(chan Result, 1)
	go func() {
		value, err := work()
		reply <- Result{Value: value, Err: err}
	}()
	return reply
}

// WorkspaceView is what the workspace runtime publishes to the window.
type WorkspaceView interface {
	// Update carries an encoded WorkspaceUpdate: `{revision, state}` or `{revision, patches}`.
	Update(update jsontext.Value)
	// Surface carries an encoded WorkspaceSurfaceEffect.
	Surface(effect jsontext.Value)
}

// WorkspaceBackend is the workspace runtime as the window reaches it (`workspace-runtime-host.ts`).
type WorkspaceBackend interface {
	// Request takes one encoded WorkspaceInput, or nil to publish a whole snapshot, and settles with
	// the encoded `WorkspaceCommandResult & { revision }`. Calls arrive in the order the window made
	// them; Request must take its input in that order and return promptly.
	Request(input jsontext.Value) Reply
	// Migrate hands over the window's stored values once they passed the bridge's checks.
	Migrate(values map[string]string) Reply
	// Subscribe sends every later update and surface effect to view until the returned func is called.
	Subscribe(view WorkspaceView) (unsubscribe func())
	SetViewVisible(visible bool)
	SetViewInUse(inUse bool)
}

// DesktopCalls answers the window's `desktop:call` channel for every call the desktop does not
// answer itself. Args hold one value per argument the window passed; an absent (undefined)
// argument is nil. Calls arrive in the order the window made them, after their argument guards
// passed; Call must return promptly and do slow work behind the reply.
type DesktopCalls interface {
	Call(ctx context.Context, name string, args []jsontext.Value) Reply
}

// ImageReads answers the `attachment:` and `message-image:` schemes the window draws images from.
type ImageReads interface {
	// Attachment reads a kept attachment by its file name, as the thread names it.
	Attachment(ctx context.Context, name, thread string) ([]byte, error)
	// MessageImage reads an image a reply linked, or its thumbnail.
	MessageImage(ctx context.Context, query MessageImageQuery, thread string) (bytes []byte, contentType string, err error)
}

// MessageImageQuery mirrors the `{ kind: "message-image" }` computer query.
type MessageImageQuery struct {
	Path      string `json:"path"`
	Root      string `json:"root"`
	Message   string `json:"message"`
	Thumbnail bool   `json:"thumbnail"`
}
