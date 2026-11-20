package mobile

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// MobileRequestTimeout is shorter than a phone's own patience, so a lost answer comes back as a refusal rather than a hang.
const MobileRequestTimeout = 8_000 * time.Millisecond

// MobileTranscribeTimeout: a recording may take its computer two minutes to write down, and loading the speech model first.
const MobileTranscribeTimeout = 130_000 * time.Millisecond

// MobileCommandsTimeout: Codex starts a server to list its skills, which may take the whole twenty seconds it is given.
const MobileCommandsTimeout = 22_000 * time.Millisecond

// RelayHost hands one request to the window. False when there is no window to hand it to.
type RelayHost func(request contracts.MobileRequest) bool

type pendingRequest struct {
	settle chan contracts.MobileResponse
	timer  *time.Timer
}

// Relay carries a phone's request to whoever holds workspace state, and its answer back. Nothing is
// answered here: a request never answered times out rather than being quietly dropped, because a
// phone waiting on an acknowledgement it will never get looks the same as a Mac that has stopped working.
type Relay struct {
	host               RelayHost
	patience           time.Duration
	transcribePatience time.Duration
	commandsPatience   time.Duration
	mu                 sync.Mutex
	pending            map[string]*pendingRequest
}

// NewRelay waits the default patience for each kind of request.
func NewRelay(host RelayHost) *Relay {
	return NewRelayWithPatience(host, MobileRequestTimeout, MobileTranscribeTimeout, MobileCommandsTimeout)
}

func NewRelayWithPatience(host RelayHost, patience, transcribe, commands time.Duration) *Relay {
	return &Relay{host: host, patience: patience, transcribePatience: transcribe, commandsPatience: commands, pending: map[string]*pendingRequest{}}
}

// isView is whether a result has the shape of a MobileView.
func isView(value any) bool {
	view, ok := value.(map[string]any)
	if !ok {
		return false
	}
	groups, ok := view["groups"].([]any)
	if !ok || groups == nil {
		return false
	}
	thread, has := view["thread"]
	if !has {
		return false
	}
	_, object := thread.(map[string]any)
	_, array := thread.([]any)
	return thread == nil || object || array
}

func (r *Relay) Snapshot(ctx context.Context, sessionID string) (*contracts.MobileView, error) {
	response := r.ask(ctx, contracts.MobileRequestSnapshot{RequestID: application.NewID(), SessionID: sessionID}, r.patience)
	result, err := responseResult(response)
	if err != nil {
		return nil, err
	}
	var shape any
	view := &contracts.MobileView{}
	if jsonx.Unmarshal(result, &shape) != nil || !isView(shape) || jsonx.Unmarshal(result, view) != nil {
		return nil, errors.New("Your computer sent a view this phone could not read.")
	}
	return view, nil
}

func (r *Relay) Command(ctx context.Context, sessionID string, command contracts.MobileCommand) error {
	_, err := responseResult(r.ask(ctx, contracts.MobileRequestCommand{RequestID: application.NewID(), SessionID: sessionID, Command: command}, r.patience))
	return err
}

// Query is the window's own answer, passed through as it came: what a read returns is the phone's to draw.
func (r *Relay) Query(ctx context.Context, sessionID string, query contracts.MobileQuery) (jsontext.Value, error) {
	patience := r.patience
	switch query.(type) {
	case contracts.MobileQueryTranscribe:
		patience = r.transcribePatience
	case contracts.MobileQueryCommands:
		patience = r.commandsPatience
	}
	return responseResult(r.ask(ctx, contracts.MobileRequestQuery{RequestID: application.NewID(), SessionID: sessionID, Query: query}, patience))
}

func responseResult(response contracts.MobileResponse) (jsontext.Value, error) {
	switch response := response.(type) {
	case contracts.MobileResponseTrue:
		return response.Result, nil
	case contracts.MobileResponseFalse:
		return nil, errors.New(response.Message)
	}
	return nil, errors.New("The phone bridge read an answer it does not know.")
}

func responseID(response contracts.MobileResponse) string {
	switch response := response.(type) {
	case contracts.MobileResponseTrue:
		return response.RequestID
	case contracts.MobileResponseFalse:
		return response.RequestID
	}
	return ""
}

// Answer is the window's answer to one request. An answer to nothing pending is a late one, and is dropped.
func (r *Relay) Answer(response contracts.MobileResponse) {
	r.mu.Lock()
	pending, ok := r.pending[responseID(response)]
	if ok {
		pending.timer.Stop()
		delete(r.pending, responseID(response))
	}
	r.mu.Unlock()
	if ok {
		pending.settle <- response
	}
}

// FailAll gives up on every waiting request at once, which is what a window that has gone means for all of them.
func (r *Relay) FailAll(message string) {
	r.mu.Lock()
	failed := r.pending
	r.pending = map[string]*pendingRequest{}
	r.mu.Unlock()
	for requestID, pending := range failed {
		pending.timer.Stop()
		pending.settle <- contracts.MobileResponseFalse{RequestID: requestID, Message: message}
	}
}

func (r *Relay) ask(ctx context.Context, request contracts.MobileRequest, patience time.Duration) contracts.MobileResponse {
	answerBy := application.Now() + float64(patience.Milliseconds())
	var requestID string
	switch typed := request.(type) {
	case contracts.MobileRequestSnapshot:
		requestID, typed.AnswerBy = typed.RequestID, &answerBy
		request = typed
	case contracts.MobileRequestCommand:
		requestID, typed.AnswerBy = typed.RequestID, &answerBy
		request = typed
	case contracts.MobileRequestQuery:
		requestID, typed.AnswerBy = typed.RequestID, &answerBy
		request = typed
	}
	pending := &pendingRequest{settle: make(chan contracts.MobileResponse, 1)}
	r.mu.Lock()
	pending.timer = time.AfterFunc(patience, func() {
		r.mu.Lock()
		held, ok := r.pending[requestID]
		if ok && held == pending {
			delete(r.pending, requestID)
		}
		r.mu.Unlock()
		if ok && held == pending {
			pending.settle <- contracts.MobileResponseFalse{RequestID: requestID, Message: "Your computer did not answer in time. Try again."}
		}
	})
	r.pending[requestID] = pending
	r.mu.Unlock()
	if !r.host(request) {
		r.Answer(contracts.MobileResponseFalse{RequestID: requestID, Message: "The AI Coding Tool window is not open on your computer."})
	}
	select {
	case response := <-pending.settle:
		return response
	case <-ctx.Done():
		r.mu.Lock()
		if r.pending[requestID] == pending {
			pending.timer.Stop()
			delete(r.pending, requestID)
		}
		r.mu.Unlock()
		return contracts.MobileResponseFalse{RequestID: requestID, Message: errorMessage(ctx.Err())}
	}
}
