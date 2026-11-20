package computers

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"regexp"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// WorkspaceSocketPath is where the bridge takes computers, beside the phones' socket.
const WorkspaceSocketPath = domain.MobileAppPath + "/workspace"

// How long a line waits on each thing.
var (
	// How long a dial may sit unanswered before the next try.
	connectTimeout = 10 * time.Second
	// How long each failed dial waits before the next, growing to a pause and no further.
	retryDelays = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	// How long a line the other computer refused waits before asking again, in case what it said has since changed.
	refusedRetry = 5 * time.Minute
	// How long a request may wait on the other computer. Its runs are its own; only the answer is waited for.
	requestTimeout = 30 * time.Second
	// How long a transfer may wait: it shares the socket with heartbeats, which wait behind large frames.
	transferTimeout = contracts.ComputerTransferTimeoutMs * time.Millisecond
	// How long a request waits in all while its line is down, to be carried again once it is back. A
	// blip of Wi-Fi costs the user a pause, not a failed send to retry and perhaps run twice.
	outageWait = 2 * time.Minute
	// Silence past this means the line is gone, though no one said so: the other computer pings more
	// often, and bytes still arriving count. Shorter than a request's own wait, so a request finds the
	// line down, and waits for it, rather than giving up on a line only thought to be up.
	silentAfter = ComputerSilentAfterMs * time.Millisecond
)

// ComputerSilentAfterMs is how long a line may stay silent before it counts as gone.
const ComputerSilentAfterMs = domain.MobilePingIntervalMs + 5_000

const ComputerOffline = "That computer cannot be reached right now."

const ComputerRestarted = "That computer restarted before it answered. Check whether it went through."

const unreadableWorkspace = "This computer sent workspace data this app cannot read. Updating AI Coding Tool may help."

var loopbackHost = regexp.MustCompile(`^(?:127\.0\.0\.1|localhost|\[::1\])(?::\d+)?$`)

// ComputerSocketURL is the socket a host is dialled on: the tailnet name over TLS, or a plain socket to
// this machine's own loopback.
func ComputerSocketURL(host string) string {
	scheme := "wss"
	if loopbackHost.MatchString(host) {
		scheme = "ws"
	}
	return scheme + "://" + host + WorkspaceSocketPath
}

// ComputerCredential is a token from an earlier pairing, or the code that earns one. Exactly one is set.
type ComputerCredential struct {
	Token string
	Code  string
}

func (c ComputerCredential) isToken() bool { return c.Code == "" }

// ClientOptions describe one line to a paired computer. The callbacks run one at a time on the
// line's own goroutine and must not wait on the client or the links that own it.
type ClientOptions struct {
	Host string
	// What this computer calls itself, which is how the other lists it.
	DeviceName string
	Credential ComputerCredential
	// The socket to dial; the tailnet name over TLS when empty.
	URL      string
	OnStatus func(status domain.ComputerStatus, err *string)
	// The token the other computer handed out, which is all that gets this one back in.
	OnPaired func(deviceID, deviceName, token string)
	OnState  func(state *application.WorkspaceState)
	// Nil capabilities mean a computer from before capability discovery.
	OnCapabilities func(capabilities contracts.ComputerCapabilities)
	// A notice that computer raised for this one to put on its desktop.
	OnNotice func(notice contracts.ThreadNotice)
	// What that computer calls itself, as the line opens and whenever it changes.
	OnName func(name string)

	loop *loop
}

// outcome settles one request: the wire result of an input, the raw answer of a query, or why it failed.
type outcome struct {
	result contracts.ComputerServerMessageResultResult
	answer jsontext.Value
	err    error
}

// waiting is a request still owed an answer, kept as it was sent so a line that comes back can carry
// it again. line counts the dial it was last written on, and instance names the running app that took it.
type waiting struct {
	done         chan outcome
	timer        *timer
	requestID    string
	payload      []byte
	transferring bool
	askedAt      float64
	line         int
	instance     *string
}

func (w *waiting) settle(result outcome) {
	w.timer.clear()
	w.done <- result
}

// line is what the links hold of a client. Its methods run in the loop.
type line interface {
	send(inputs []application.WorkspaceInput) <-chan outcome
	query(query contracts.ComputerQuery) <-chan outcome
	reconnect()
	stop()
}

// ComputerClient is one line to a paired computer. It dials, pairs or resumes, mirrors the other
// computer's state as it arrives, and carries inputs there. A dropped line is dialled again on a
// growing pause, and what was still owed an answer is carried again once it is back: the other
// computer remembers what it has run by request, so nothing runs twice. A line the other computer
// refuses outright, as unpaired or unreadable, is left down with the reason and asked again only
// rarely, or when told to.
type ComputerClient struct {
	options    ClientOptions
	loop       *loop
	url        string
	socket     *socket
	credential ComputerCredential
	sessionID  *string
	lastSeq    float64
	displayed  *application.WorkspaceState
	// capabilities are nil until the other computer names them, and for a computer that never does.
	capabilities contracts.ComputerCapabilities
	applyUpdate  func(update *contracts.ComputerWorkspaceUpdate) (*application.WorkspaceState, bool, error)
	attempt      int
	// How many lines have been dialled, and the running app the current one reached, once it says.
	dials    int
	instance *string
	stopped  bool
	// Whether the other computer refused the line, which is not asked again on the usual pause.
	refused bool
	retry   *timer
	// The watchdog: the server pings every so often, so a line nothing has arrived on for a while is
	// dead even when the network never said so.
	watch   *timer
	heardAt float64
	status  domain.ComputerStatus
	// Why the line last went down, kept through the dials that follow until one connects.
	lastError *string
	results   map[string]*waiting
	answers   map[string]*waiting
}

// NewComputerClient opens a line and starts dialling.
func NewComputerClient(options ClientOptions) *ComputerClient {
	c := newClient(options)
	c.loop.call(c.open)
	return c
}

func newClient(options ClientOptions) *ComputerClient {
	l := options.loop
	if l == nil {
		l = newLoop()
	}
	url := options.URL
	if url == "" {
		url = ComputerSocketURL(options.Host)
	}
	return &ComputerClient{
		options:     options,
		loop:        l,
		url:         url,
		credential:  options.Credential,
		applyUpdate: application.CreateRemoteWorkspaceReplica(),
		status:      domain.ComputerStatusConnecting,
		heardAt:     application.Now(),
		results:     map[string]*waiting{},
		answers:     map[string]*waiting{},
	}
}

// Status is where the line stands.
func (c *ComputerClient) Status() domain.ComputerStatus {
	var status domain.ComputerStatus
	c.loop.call(func() { status = c.status })
	return status
}

// State is the other computer's state as last received, or nil before any arrived.
func (c *ComputerClient) State() *application.WorkspaceState {
	var state *application.WorkspaceState
	c.loop.call(func() { state = c.displayed })
	return state
}

// Send carries inputs to the other computer's reducer and answers with its result, revision included.
func (c *ComputerClient) Send(ctx context.Context, inputs []application.WorkspaceInput) (contracts.ComputerServerMessageResultResult, error) {
	var done <-chan outcome
	c.loop.call(func() { done = c.send(inputs) })
	result, err := await(ctx, done)
	return result.result, err
}

// Query asks the other computer for a read and answers with its raw result.
func (c *ComputerClient) Query(ctx context.Context, query contracts.ComputerQuery) (jsontext.Value, error) {
	var done <-chan outcome
	c.loop.call(func() { done = c.query(query) })
	result, err := await(ctx, done)
	return result.answer, err
}

// Reconnect dials now rather than at the end of the pause, and asks a computer that refused the line
// again. A line that is up is left alone; one that only looks up, after the network moved under it,
// is cut first so the dial is a real one.
func (c *ComputerClient) Reconnect() { c.loop.call(c.reconnect) }

// Stop hangs up for good, refusing whatever is still waiting.
func (c *ComputerClient) Stop() { c.loop.call(c.stop) }

func await(ctx context.Context, done <-chan outcome) (outcome, error) {
	select {
	case result := <-done:
		return result, result.err
	case <-ctx.Done():
		return outcome{}, ctx.Err()
	}
}

func settled(result outcome) <-chan outcome {
	done := make(chan outcome, 1)
	done <- result
	return done
}

func refusal(message string) <-chan outcome {
	return settled(outcome{err: errors.New(message)})
}

func (c *ComputerClient) send(inputs []application.WorkspaceInput) <-chan outcome {
	for _, input := range inputs {
		command, ok := input.(contracts.AppCommand)
		if !ok || !contracts.IsWorkspaceViewInput(input) || !contracts.IsAppCommandType(inputType(input)) ||
			!contracts.SupportsComputerCommand(contracts.ComputerCapabilitiesComputerCapabilities(), command) ||
			!contracts.SupportsComputerCommand(c.capabilities, command) {
			return refusal(contracts.RemoteUnsupported)
		}
	}
	return c.ask(c.results, application.ComputerInputRequest{RequestID: application.NewID(), Inputs: inputs})
}

func (c *ComputerClient) query(query contracts.ComputerQuery) <-chan outcome {
	if !contracts.SupportsComputerQuery(c.capabilities, query) {
		return refusal(contracts.RemoteUnsupported)
	}
	return c.ask(c.answers, contracts.ComputerQueryRequest{RequestID: application.NewID(), Query: query})
}

func inputType(input application.WorkspaceInput) string {
	encoded, err := jsonx.Marshal(input)
	if err != nil {
		return ""
	}
	var typed struct {
		Type string `json:"type"`
	}
	if jsonx.Unmarshal(encoded, &typed) != nil {
		return ""
	}
	return typed.Type
}

func (c *ComputerClient) live() bool {
	return c.socket != nil && c.socket.open && c.status == domain.ComputerStatusConnected
}

func (c *ComputerClient) ask(held map[string]*waiting, message application.ComputerClientMessage) <-chan outcome {
	if !c.live() {
		return refusal(ComputerOffline)
	}
	payload := requestPayload(message)
	if payload == nil {
		return refusal(contracts.ComputerSendTooLarge)
	}
	requestID := requestIDOf(message)
	transferring := application.IsComputerTransfer(message)
	entry := &waiting{
		done:         make(chan outcome, 1),
		requestID:    requestID,
		payload:      payload,
		transferring: transferring,
		askedAt:      application.Now(),
		line:         c.dials,
		instance:     c.instance,
	}
	// A request whose line is down waits out the outage, then is refused; one on a live line that
	// went unanswered is refused now.
	var expire func()
	expire = func() {
		waited := time.Duration(application.Now()-entry.askedAt) * time.Millisecond
		if !c.live() && waited < outageWait {
			entry.timer = c.loop.after(outageWait-waited, expire)
			return
		}
		delete(held, requestID)
		message := ComputerOffline
		if c.live() {
			message = "That computer did not answer in time."
		}
		entry.done <- outcome{err: errors.New(message)}
	}
	deadline := requestTimeout
	if transferring {
		deadline = transferTimeout
	}
	entry.timer = c.loop.after(deadline, expire)
	held[requestID] = entry
	c.deliver(entry)
	return entry.done
}

func requestIDOf(message application.ComputerClientMessage) string {
	switch message := message.(type) {
	case application.ComputerInputRequest:
		return message.RequestID
	case contracts.ComputerQueryRequest:
		return message.RequestID
	}
	return ""
}

var dataURLPrefix = regexp.MustCompile(`^data:[^,]*,`)

// requestPayload checks the encoded envelope before sending so an oversized request cannot kill the link.
func requestPayload(message application.ComputerClientMessage) []byte {
	if request, ok := message.(application.ComputerInputRequest); ok {
		for _, input := range request.Inputs {
			send, ok := input.(contracts.TaskCommandAttachmentsSend)
			if !ok {
				continue
			}
			for _, attachment := range send.Attachments {
				if attachment != nil && len(dataURLPrefix.ReplaceAllString(attachment.Source, "")) > domain.MaxAttachmentEncodedBytes {
					return nil
				}
			}
		}
	}
	payload, err := application.StringifyWorkspaceJSON(message)
	if err != nil || len(payload) > contracts.MaxComputerMessageBytes {
		return nil
	}
	return payload
}

func (c *ComputerClient) report(next domain.ComputerStatus, err *string) {
	c.status = next
	if c.options.OnStatus != nil {
		c.options.OnStatus(next, err)
	}
}

func (c *ComputerClient) write(message application.ComputerClientMessage) {
	if c.socket == nil || !c.socket.open {
		return
	}
	payload, err := application.StringifyWorkspaceJSON(message)
	if err != nil {
		log.Printf("computers: could not encode a message: %v", err)
		return
	}
	c.socket.send(payload)
}

// deliver writes a request, announcing a large one first so the other computer allows for it.
func (c *ComputerClient) deliver(entry *waiting) {
	if entry.transferring {
		c.write(application.ComputerClientMessageTransfer{RequestID: entry.requestID})
	}
	if c.socket != nil && c.socket.open {
		c.socket.send(entry.payload)
	}
}

// connected marks the line as answering again. What is still waiting goes again once the line names
// the app it reached.
func (c *ComputerClient) connected() {
	c.attempt = 0
	c.refused = false
	c.lastError = nil
	c.report(domain.ComputerStatusConnected, nil)
}

func (c *ComputerClient) schedule() {
	if c.stopped || c.retry != nil {
		return
	}
	delay := refusedRetry
	if !c.refused {
		delay = retryDelays[min(c.attempt, len(retryDelays)-1)]
	}
	c.attempt++
	c.retry = c.loop.after(delay, func() {
		c.retry = nil
		c.open()
	})
}

// dropped leaves a refused line down a long while; every other drop is dialled again soon. Requests
// still waiting outlast a drop, each on its own deadline, unless the other computer refused us.
func (c *ComputerClient) dropped(err *string, refusal bool) {
	c.stopWatch()
	closing := c.socket
	c.socket = nil
	if closing != nil {
		closing.terminate()
	}
	if c.stopped {
		return
	}
	if err != nil {
		c.lastError = err
	}
	if refusal {
		message := ComputerOffline
		if err != nil {
			message = *err
		}
		c.refuse(message)
		c.refused = true
		// A code is spent or wrong for good, so a pairing line is not dialled again at all.
		if !c.credential.isToken() {
			c.stopped = true
		}
	}
	c.report(domain.ComputerStatusOffline, c.lastError)
	c.schedule()
}

func (c *ComputerClient) receive(message contracts.ComputerServerMessage) {
	c.lastSeq = sequenceOf(message)
	c.heard()
	// A resumed line holding a view already is live on its first word: a resume replays only what
	// changed, and a computer where nothing did sends no state at all. A new line waits for its state.
	switch message.(type) {
	case contracts.ComputerServerMessageWorkspace, contracts.ComputerServerMessageError:
	default:
		if c.displayed != nil && c.sessionID != nil && c.status != domain.ComputerStatusConnected {
			c.connected()
		}
	}
	switch message := message.(type) {
	case contracts.ComputerServerMessagePaired:
		c.credential = ComputerCredential{Token: message.Token}
		if c.options.OnPaired != nil {
			c.options.OnPaired(message.DeviceID, message.DeviceName, message.Token)
		}
	case contracts.ComputerServerMessageWorkspace:
		if message.SessionID != nil {
			c.sessionID = message.SessionID
		}
		next, resync, err := c.applyUpdate(&message.Update)
		if err != nil {
			text := unreadableWorkspace
			c.dropped(&text, true)
			return
		}
		if resync {
			c.sessionID = nil
			if c.socket != nil {
				c.socket.terminate()
			}
			return
		}
		if next == nil {
			return
		}
		c.displayed = next
		if c.status != domain.ComputerStatusConnected {
			c.connected()
		}
		if c.options.OnState != nil {
			c.options.OnState(c.displayed)
		}
	case contracts.ComputerServerMessageResult:
		if entry := claim(c.results, message.RequestID); entry != nil {
			entry.done <- outcome{result: message.Result}
		}
	case contracts.ComputerServerMessageAnswer:
		entry := claim(c.answers, message.RequestID)
		if entry == nil {
			return
		}
		if message.Ok {
			entry.done <- outcome{answer: message.Result}
		} else {
			text := ""
			if message.Message != nil {
				text = *message.Message
			}
			entry.done <- outcome{err: errors.New(text)}
		}
	case contracts.ComputerServerMessageError:
		refusal := message.Code == contracts.MobileErrorCodeUnauthorized || message.Code == contracts.MobileErrorCodeVersion ||
			message.Code == contracts.MobileErrorCodeExpiredCode || message.Code == contracts.MobileErrorCodeRateLimited
		text := message.Message
		c.dropped(&text, refusal)
	case contracts.ComputerServerMessageNotice:
		if c.options.OnNotice != nil {
			c.options.OnNotice(message.Notice)
		}
	case contracts.ComputerServerMessageCapabilities:
		c.capabilities = message.Capabilities
		if c.capabilities == nil {
			c.capabilities = contracts.ComputerCapabilities{}
		}
		c.instance = message.Instance
		c.carry()
		if c.options.OnCapabilities != nil {
			c.options.OnCapabilities(c.capabilities)
		}
	case contracts.ComputerServerMessageName:
		if c.options.OnName != nil {
			c.options.OnName(message.Name)
		}
	case contracts.ComputerServerMessagePing:
		c.write(application.ComputerClientMessagePong{At: message.At})
	}
}

// claim takes the one waiting on an answer off the list as it is answered.
func claim(held map[string]*waiting, requestID string) *waiting {
	entry, ok := held[requestID]
	if !ok {
		return nil
	}
	delete(held, requestID)
	entry.timer.clear()
	return entry
}

// carry: the line names the running app it reached. What was written on an earlier line is carried
// again only to the same running app, which remembers what it ran; an app that restarted since has
// forgotten, and would run it twice, so the request is refused instead. Written on this line, a
// request learns which app took it.
func (c *ComputerClient) carry() {
	for _, held := range []map[string]*waiting{c.results, c.answers} {
		for _, id := range sortedRequests(held) {
			entry := held[id]
			if entry.line != c.dials && (c.instance == nil || entry.instance == nil || *entry.instance != *c.instance) {
				delete(held, id)
				entry.settle(outcome{err: errors.New(ComputerRestarted)})
				continue
			}
			if entry.line != c.dials {
				c.deliver(entry)
			}
			entry.line = c.dials
			entry.instance = c.instance
		}
	}
}

// refuse settles every request still waiting: the line it was asked on is gone, and its answer with it.
func (c *ComputerClient) refuse(message string) {
	for _, held := range []map[string]*waiting{c.results, c.answers} {
		for _, id := range sortedRequests(held) {
			entry := held[id]
			delete(held, id)
			entry.settle(outcome{err: errors.New(message)})
		}
	}
}

// sortedRequests lists requests in the order they were asked, as a Map iterates.
func sortedRequests(held map[string]*waiting) []string {
	ids := make([]string, 0, len(held))
	for id := range held {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && held[ids[j]].askedAt < held[ids[j-1]].askedAt; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	return ids
}

func sequenceOf(message contracts.ComputerServerMessage) float64 {
	switch message := message.(type) {
	case contracts.ComputerServerMessagePaired:
		return message.Sequence
	case contracts.ComputerServerMessageWorkspace:
		return message.Sequence
	case contracts.ComputerServerMessageResult:
		return message.Sequence
	case contracts.ComputerServerMessageAnswer:
		return message.Sequence
	case contracts.ComputerServerMessageError:
		return message.Sequence
	case contracts.ComputerServerMessageNotice:
		return message.Sequence
	case contracts.ComputerServerMessageName:
		return message.Sequence
	case contracts.ComputerServerMessageCapabilities:
		return message.Sequence
	case contracts.ComputerServerMessagePing:
		return message.Sequence
	}
	return 0
}

func (c *ComputerClient) heard() { c.heardAt = application.Now() }

// lastHeard counts bytes still on their way as well as whole messages.
func (c *ComputerClient) lastHeard() float64 {
	heard := c.heardAt
	if c.socket != nil {
		heard = max(heard, float64(c.socket.heardAt.Load()))
	}
	return heard
}

func (c *ComputerClient) startWatch() {
	c.heardAt = application.Now()
	if c.socket != nil {
		c.socket.heardAt.Store(0)
	}
	c.arm()
}

func (c *ComputerClient) arm() {
	c.watch.clear()
	silent := float64(silentAfter / time.Millisecond)
	wait := time.Duration(max(0, c.lastHeard()+silent-application.Now())) * time.Millisecond
	c.watch = c.loop.after(wait, func() {
		c.watch = nil
		if application.Now()-c.lastHeard() >= silent {
			if c.socket != nil {
				c.socket.terminate()
			}
		} else {
			c.arm()
		}
	})
}

func (c *ComputerClient) stopWatch() {
	c.watch.clear()
	c.watch = nil
}

func (c *ComputerClient) open() {
	if c.stopped || c.socket != nil {
		return
	}
	c.report(domain.ComputerStatusConnecting, c.lastError)
	c.dials++
	c.instance = nil
	var dialled *socket
	dialled = dial(c.loop, c.url, connectTimeout, socketHandlers{
		open: func() {
			if c.socket != dialled {
				return
			}
			c.capabilities = nil
			if c.options.OnCapabilities != nil {
				c.options.OnCapabilities(nil)
			}
			c.startWatch()
			if c.credential.isToken() {
				c.write(contracts.ComputerResumeRequest{Version: contracts.ComputerProtocolVersion, Token: c.credential.Token, SessionID: c.sessionID, LastSequence: c.lastSeq})
			} else {
				c.write(contracts.ComputerPairRequest{Version: contracts.ComputerProtocolVersion, Code: c.credential.Code, DeviceName: c.options.DeviceName})
			}
		},
		message: func(data []byte) {
			if c.socket != dialled {
				return
			}
			if message := readServerMessage(data); message != nil {
				c.receive(message)
			}
		},
		close: func(code int, said string) {
			if c.socket != dialled {
				return
			}
			if code == closeNormal || said == "" || said == "unauthorized" {
				c.dropped(nil, false)
			} else {
				c.dropped(&said, false)
			}
		},
	})
	c.socket = dialled
}

func (c *ComputerClient) reconnect() {
	if c.stopped || (c.refused && !c.credential.isToken()) {
		return
	}
	c.refused = false
	c.attempt = 0
	c.retry.clear()
	c.retry = nil
	recent := application.Now()-c.lastHeard() < float64(domain.MobilePingIntervalMs+5_000)
	if c.socket != nil && (c.status != domain.ComputerStatusConnected || recent) {
		return
	}
	if c.socket != nil {
		c.socket.terminate()
	} else {
		c.open()
	}
}

func (c *ComputerClient) stop() {
	c.stopped = true
	c.retry.clear()
	c.retry = nil
	c.stopWatch()
	closing := c.socket
	c.socket = nil
	c.refuse(ComputerOffline)
	if closing != nil {
		closing.close(closeNormal, "stopped")
	}
}

// readServerMessage reads what the other computer sent, or nil for anything that is not one of its
// messages. Only the envelope is checked: the state inside is the other computer's own derivation.
func readServerMessage(data []byte) contracts.ComputerServerMessage {
	var envelope map[string]jsontext.Value
	if err := jsonx.Unmarshal(data, &envelope); err != nil || envelope == nil {
		return nil
	}
	shallow := make(map[string]any, len(envelope))
	for key, value := range envelope {
		if key == "update" {
			shallow[key] = shallowUpdate(value)
			continue
		}
		var decoded any
		if err := jsonx.Unmarshal(value, &decoded); err != nil {
			return nil
		}
		shallow[key] = decoded
	}
	if !contracts.IsComputerServerMessage(shallow) {
		return nil
	}
	var message contracts.ComputerServerMessage
	if err := application.ParseWorkspaceJSON(data, &message); err != nil {
		return nil
	}
	return message
}

// shallowUpdate is a workspace update with its state left unread: the envelope check asks only
// whether it holds a state object or a patch list.
func shallowUpdate(value jsontext.Value) any {
	var fields map[string]jsontext.Value
	if jsonx.Unmarshal(value, &fields) != nil || fields == nil {
		var decoded any
		_ = jsonx.Unmarshal(value, &decoded)
		return decoded
	}
	update := make(map[string]any, len(fields))
	for key, field := range fields {
		switch {
		case key == "state" && field.Kind() == '{':
			update[key] = map[string]any{}
		case key == "patches" && field.Kind() == '[':
			update[key] = []any{}
		default:
			var decoded any
			if jsonx.Unmarshal(field, &decoded) == nil {
				update[key] = decoded
			}
		}
	}
	return update
}
