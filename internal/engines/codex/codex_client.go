package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

// A request the server sent. Exactly one of Respond and Fail answers it.
type ServerRequest struct {
	ID      protocol.RequestID
	Method  string
	Params  jsontext.Value
	Respond func(result any)
	Fail    func(err protocol.JSONRPCError)
}

func (request *ServerRequest) decode(value any) error {
	return protocol.Unmarshal(request.Params, value)
}

// What a session asks of its connection. The real client fits through ConnectAppServer; a scripted
// one can stand in for it.
type CodexClient interface {
	Initialize(ctx context.Context, info protocol.ClientInfo, capabilities *protocol.InitializeCapabilities) (*protocol.InitializeResponse, error)
	Request(ctx context.Context, method string, params any) (jsontext.Value, error)
	// Handlers run one at a time, in the order notifications arrive.
	On(method string, handler func(params jsontext.Value)) func()
	OnRequest(handler func(request *ServerRequest)) func()
	Close() protocol.ExitStatus
	// Closed once the process has gone; Exit then says how.
	Exited() <-chan struct{}
	Exit() *protocol.ExitStatus
}

type CodexConnect func(command protocol.Command) CodexClient

type appServerClient struct{ *protocol.Client }

func (client appServerClient) OnRequest(handler func(request *ServerRequest)) func() {
	return client.Client.OnRequest(func(request *protocol.IncomingRequest) {
		handler(&ServerRequest{ID: request.ID, Method: request.Method, Params: request.Params, Respond: request.Respond, Fail: request.Fail})
	})
}

// Starts a Codex app server and speaks to it.
func ConnectAppServer(command protocol.Command) CodexClient {
	return appServerClient{protocol.Connect(command)}
}

// How the app introduces itself to the server.
var ClientInfo = protocol.DefaultClientInfo

// Sends a request and decodes its answer.
func call[R any](ctx context.Context, client CodexClient, method string, params any) (R, error) {
	var result R
	raw, err := client.Request(ctx, method, params)
	if err != nil {
		return result, err
	}
	if err := protocol.Unmarshal(raw, &result); err != nil {
		return result, fmt.Errorf("%s: %w", method, err)
	}
	return result, nil
}

func initialize(ctx context.Context, client CodexClient) error {
	_, err := client.Initialize(ctx, ClientInfo, nil)
	return err
}

// Calls handler with each notification of method decoded; one that does not decode is skipped.
func onNotification[P any](client CodexClient, method string, handler func(params P)) func() {
	return client.On(method, func(raw jsontext.Value) {
		var params P
		if protocol.Unmarshal(raw, &params) == nil {
			handler(params)
		}
	})
}

// What went wrong, without the method the server was answering when it did.
func reasonOf(err error) string {
	var refused *protocol.AppServerError
	if errors.As(err, &refused) {
		return agent.FirstLine(refused.Message)
	}
	return agent.ReasonOf(err)
}

func isAppServerError(err error) bool {
	var refused *protocol.AppServerError
	return errors.As(err, &refused)
}

func ptr[T any](value T) *T { return &value }

func deref[T any](value *T) T {
	if value == nil {
		var zero T
		return zero
	}
	return *value
}
