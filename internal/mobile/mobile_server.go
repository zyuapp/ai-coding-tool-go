package mobile

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// socketPath is where the phone app talks. Pairing happens on the same socket, as its first message.
const socketPath = domain.MobileAppPath + "/socket"

// WorkspaceSocketPath is where another computer talks, in the window's own inputs rather than the phone's.
const WorkspaceSocketPath = domain.MobileAppPath + "/workspace"

// authDeadline is how long a socket that has said nothing is given to name itself before it is shown the door.
const authDeadline = 10 * time.Second

// sessionGrace is how long a dropped session waits to be resumed before its buffer is thrown away.
const sessionGrace = 5 * time.Minute

// maxSocketMessage: the shared socket also carries a computer's complete attachment send.
const maxSocketMessage = contracts.MaxComputerMessageBytes

// maxHandledRequests is how many settled request IDs a device remembers. Comfortably more than a phone's own outbox holds.
const maxHandledRequests = 256

// maxCommandsInFlight is how many of a device's commands may be waiting on the window at once.
const maxCommandsInFlight = 64

// maxQueriesInFlight is how many of a session's reads may be waiting on the window at once. A review asks for a few files, not a tree.
const maxQueriesInFlight = 8

// catchUp is how often a phone behind a congested line is looked at again, to hand it the view once it drains.
const catchUp = 250 * time.Millisecond

// phoneUnreadable is what a phone is told when it sends something the server cannot read, instead of being hung up on.
const phoneUnreadable = "Your computer could not read that request."

// securityHeaders: nothing this server answers is meant to be sniffed, framed or referred from.
var securityHeaders = map[string]string{
	"x-content-type-options":  "nosniff",
	"x-frame-options":         "DENY",
	"content-security-policy": "frame-ancestors 'none'",
	"referrer-policy":         "no-referrer",
}

// WorkspaceHooks is the workspace as another computer reads and drives it: whole, and in the
// window's own inputs.
type WorkspaceHooks interface {
	// Name is what this computer calls itself to the computers it hands the workspace to. "" leaves it
	// what they paired it as.
	Name() string
	Snapshot() application.WorkspaceUpdate
	// Subscribe hands every later update to listener, in revision order, until the returned stop.
	Subscribe(listener func(application.WorkspaceUpdate)) func()
	// Input runs inputs one after another and answers with the last one's result and the revision it left.
	Input(ctx context.Context, inputs []application.WorkspaceInput) (contracts.ComputerServerMessageResultResult, error)
	Query(ctx context.Context, query contracts.ComputerQuery) (any, error)
}

// ServerOptions is what a Server is built from.
type ServerOptions struct {
	Devices *PairingStore
	Port    int
	// AllowedOrigins are the origins a browser may open a socket from, which move as Tailscale comes and goes.
	AllowedOrigins func() []string
	Snapshot       func(ctx context.Context, sessionID string) (*contracts.MobileView, error)
	Command        func(ctx context.Context, sessionID string, command contracts.MobileCommand) error
	// Query answers one read with whatever the window returns, which is the phone's to draw.
	Query func(ctx context.Context, sessionID string, query contracts.MobileQuery) (jsontext.Value, error)
	// OnChange: something a phone did changed what settings should say. Called without the server's lock.
	OnChange func()
	// Workspace is what another computer is handed. Nil on a host that takes no computers.
	Workspace WorkspaceHooks
	// SessionGrace is how long a dropped session is held for its phone; nil holds it five minutes.
	SessionGrace *time.Duration
}

// outcome is how a command ended, kept so a resend is answered rather than run a second time, with what it named.
type outcome struct {
	ok      bool
	message string
	taskID  *string
	answer  contracts.CoordinationAnswer
}

func outcomeOf(result contracts.ComputerServerMessageResultResult) outcome {
	switch result := result.(type) {
	case contracts.ComputerServerMessageResultResultTrue:
		return outcome{ok: true, taskID: result.TaskID, answer: result.Answer}
	case contracts.ComputerServerMessageResultResultFalse:
		return outcome{message: result.Message}
	}
	return outcome{message: "The computer answered with something this one could not read."}
}

func (o outcome) result(revision float64) contracts.ComputerServerMessageResultResult {
	if o.ok {
		return contracts.ComputerServerMessageResultResultTrue{TaskID: o.taskID, Answer: o.answer, Revision: revision}
	}
	return contracts.ComputerServerMessageResultResultFalse{Message: o.message, Revision: revision}
}

// handledRequests is every request ID a device has sent, in the order it sent them, and what came of
// each. A nil outcome is one still in flight.
type handledRequests struct {
	order    []string
	outcomes map[string]*outcome
}

func newHandledRequests() *handledRequests {
	return &handledRequests{outcomes: map[string]*outcome{}}
}

func (h *handledRequests) has(requestID string) bool {
	_, ok := h.outcomes[requestID]
	return ok
}

func (h *handledRequests) set(requestID string, settled *outcome) {
	if !h.has(requestID) {
		h.order = append(h.order, requestID)
	}
	h.outcomes[requestID] = settled
}

func (h *handledRequests) inFlight() int {
	count := 0
	for _, settled := range h.outcomes {
		if settled == nil {
			count++
		}
	}
	return count
}

// trim forgets only settled requests: forgetting one in flight would let a resend run it twice.
func (h *handledRequests) trim() {
	kept := h.order[:0:0]
	for index, requestID := range h.order {
		if len(h.outcomes) <= maxHandledRequests {
			kept = append(kept, h.order[index:]...)
			break
		}
		if h.outcomes[requestID] != nil {
			delete(h.outcomes, requestID)
			continue
		}
		kept = append(kept, requestID)
	}
	h.order = kept
}

// bufferedMessage is one outbound message, kept as the text it was sent as so replaying it costs nothing.
type bufferedMessage struct {
	sequence float64
	text     []byte
}

type liveSession struct {
	domain.MobileSession
	deviceName string
	socket     *socket
	// buffer is recent outbound messages, newest last, so a phone that dropped is given only what it missed.
	buffer      []bufferedMessage
	bufferBytes int
	// awaitingSnapshot: whether this session's first snapshot is still being fetched. Patches sent
	// before it lands describe a view the phone has never seen.
	awaitingSnapshot bool
	// expiresAt is when an offline session stops being worth keeping. Nil while a socket is attached.
	expiresAt *float64
	// queries are reads waiting on the window. A read is never resent, so nothing about it is remembered once answered.
	queries int
	// transfers are upload announcements and attachment requests still being handled or written to the socket.
	transfers map[string]float64
	// catchingUp: a phone that fell behind a congested line, waiting for it to drain before it is handed the view whole.
	catchingUp *time.Timer
	// snapshotting: a computer's snapshot is being read, and the updates published meanwhile are held for after it.
	snapshotting bool
	held         []contracts.ComputerWorkspaceUpdate
}

// accepted is one socket's state before and after it names itself.
type accepted struct {
	session  *liveSession
	deadline *time.Timer
	closers  []func()
}

// Server is the local server a phone reaches. It trades pairing codes for tokens and holds one socket
// per phone. Nothing here decides anything about the workspace: a command is relayed to the window
// and acknowledged with what the window said.
//
// Sessions outlive their sockets. Every message a session sends is numbered and kept, so a phone
// that loses the line resumes by naming the last number it saw and is handed the rest, and a phone
// that fell further behind than the buffer reaches pays for a fresh snapshot instead. What a phone
// has already run is remembered against its device rather than its session, so falling that far
// behind still does not run a resent command twice.
type Server struct {
	options ServerOptions

	mu       sync.Mutex
	sessions map[string]*liveSession
	order    []*liveSession
	// instance is which running server this is. What a device has run is remembered only as long as
	// the server runs, so a device that finds a new one does not send it what the old one may have run
	// already. An edit is the exception: its outcome is stored with the device, so it is safe to send again.
	instance string
	handled  map[string]*handledRequests
	http     *http.Server
	sockets  map[*socket]*accepted
	stopTick chan struct{}
	port     *int
	failure  *string
	// pending is sockets that have connected but not yet said who they are.
	pending       int
	stopWorkspace func()
	// view is what the published patches build, which is exactly what every phone on the line holds.
	// A phone that arrives is handed this rather than asking the window, so nothing published while it
	// waited can fall between its snapshot and the patches after it. Nil until there is one to keep.
	view    *contracts.MobileView
	changed bool
}

func NewServer(options ServerOptions) *Server {
	return &Server{
		options:  options,
		sessions: map[string]*liveSession{},
		instance: application.NewID(),
		handled:  map[string]*handledRequests{},
		sockets:  map[*socket]*accepted{},
	}
}

func (s *Server) lock() { s.mu.Lock() }

// unlock lets go, then says what changed.
func (s *Server) unlock() {
	changed := s.changed
	s.changed = false
	s.mu.Unlock()
	if changed && s.options.OnChange != nil {
		s.options.OnChange()
	}
}

func (s *Server) runLocked(fn func()) {
	s.lock()
	defer s.unlock()
	fn()
}

func (s *Server) Port() *int {
	s.lock()
	defer s.unlock()
	if s.port == nil {
		return nil
	}
	port := *s.port
	return &port
}

func (s *Server) Status() domain.MobileServerStatus {
	s.lock()
	defer s.unlock()
	if s.failure != nil {
		return domain.MobileServerStatusError
	}
	if s.port == nil {
		return domain.MobileServerStatusOff
	}
	return domain.MobileServerStatusListening
}

func (s *Server) Error() *string {
	s.lock()
	defer s.unlock()
	return s.failure
}

func (s *Server) SessionViews() []*domain.MobileSessionView {
	s.lock()
	defer s.unlock()
	views := make([]*domain.MobileSessionView, len(s.order))
	for index, session := range s.order {
		views[index] = &domain.MobileSessionView{MobileSession: session.MobileSession, DeviceName: session.deviceName}
	}
	return views
}

func (s *Server) Start(host string) error {
	s.lock()
	if s.http != nil {
		s.unlock()
		return nil
	}
	s.failure = nil
	s.instance = application.NewID()
	listener, err := listen(host, s.options.Port)
	if err != nil {
		message := err.Error()
		s.failure = &message
		s.unlock()
		s.Stop()
		return err
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	s.http = server
	port := listener.Addr().(*net.TCPAddr).Port
	s.port = &port
	stopTick := make(chan struct{})
	s.stopTick = stopTick
	s.unlock()
	go s.serveOn(server, listener)
	go s.heartbeat(stopTick)
	if s.options.Workspace == nil {
		return nil
	}
	stop := s.options.Workspace.Subscribe(s.workspaceUpdate)
	s.lock()
	if s.http != server {
		s.unlock()
		stop()
		return nil
	}
	s.stopWorkspace = stop
	s.unlock()
	return nil
}

// serveOn: once the port is up an error is a server gone, which is reported as a failure for the host to start again.
func (s *Server) serveOn(server *http.Server, listener net.Listener) {
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return
	}
	log.Printf("The phone bridge server failed: %v", err)
	s.lock()
	defer s.unlock()
	if s.http != server {
		return
	}
	message := err.Error()
	s.failure = &message
	s.changed = true
}

func (s *Server) heartbeat(stop chan struct{}) {
	ticker := time.NewTicker(domain.MobilePingIntervalMs * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Server) workspaceUpdate(update application.WorkspaceUpdate) {
	handed, err := handedOver(update)
	if err != nil {
		log.Printf("Could not hand the workspace to a computer: %v", err)
		return
	}
	s.lock()
	defer s.unlock()
	for _, session := range s.order {
		if session.Kind != domain.PairedDeviceKindComputer {
			continue
		}
		if !session.awaitingSnapshot {
			s.emit(session, &contracts.ComputerServerMessageWorkspace{Update: handed}, nil)
		} else if session.snapshotting {
			session.held = append(session.held, handed)
		}
	}
}

// handedOver is a workspace as it is handed to another computer: without the states of the computers
// this one is paired with, which the other computer has no use for and which would otherwise nest without end.
func handedOver(update application.WorkspaceUpdate) (contracts.ComputerWorkspaceUpdate, error) {
	if update.State == nil {
		patches := make([]*contracts.WorkspacePatch, 0, len(update.Patches))
		for _, patch := range update.Patches {
			if len(patch.Path) > 0 && !patch.Path[0].IsIndex && patch.Path[0].Key == "computers" {
				continue
			}
			patches = append(patches, patch)
		}
		return contracts.ComputerWorkspaceUpdate{Revision: update.Revision, Patches: patches}, nil
	}
	state := *update.State
	paired := make([]*application.PairedComputer, len(state.Computers.Paired))
	for index, computer := range state.Computers.Paired {
		bare := *computer
		bare.State = nil
		paired[index] = &bare
	}
	state.Computers.Paired = paired
	encoded, err := application.StringifyWorkspaceJSON(&state)
	if err != nil {
		return contracts.ComputerWorkspaceUpdate{}, err
	}
	return contracts.ComputerWorkspaceUpdate{Revision: update.Revision, State: encoded}, nil
}

// Notice is a notice this workspace raised, handed to every computer on the line, which puts it on its own desktop.
func (s *Server) Notice(notice contracts.ThreadNotice) {
	s.lock()
	defer s.unlock()
	for _, session := range s.order {
		if session.Kind == domain.PairedDeviceKindComputer && !session.awaitingSnapshot {
			s.emit(session, &contracts.ComputerServerMessageNotice{Notice: notice}, nil)
		}
	}
}

// AnnounceName tells every computer on the line what this computer now calls itself.
func (s *Server) AnnounceName() {
	name := s.name()
	s.lock()
	defer s.unlock()
	for _, session := range s.order {
		s.sendName(session, name)
	}
}

func (s *Server) name() string {
	if s.options.Workspace == nil {
		return ""
	}
	return s.options.Workspace.Name()
}

func (s *Server) sendCapabilities(session *liveSession) {
	if session.Kind == domain.PairedDeviceKindComputer {
		instance := s.instance
		s.emit(session, &contracts.ComputerServerMessageCapabilities{Capabilities: contracts.ComputerCapabilitiesComputerCapabilities(), Instance: &instance}, nil)
	}
}

func (s *Server) sendName(session *liveSession, name string) {
	if session.Kind == domain.PairedDeviceKindComputer && name != "" {
		s.emit(session, &contracts.ComputerServerMessageName{Name: name}, nil)
	}
}

func (s *Server) Stop() {
	s.lock()
	if s.stopTick != nil {
		close(s.stopTick)
		s.stopTick = nil
	}
	stopWorkspace := s.stopWorkspace
	s.stopWorkspace = nil
	for _, session := range s.order {
		hangUp(session.socket, websocket.CloseGoingAway, "The bridge was turned off.")
		if session.catchingUp != nil {
			session.catchingUp.Stop()
		}
	}
	s.sessions = map[string]*liveSession{}
	s.order = nil
	s.handled = map[string]*handledRequests{}
	for socket := range s.sockets {
		hangUp(socket, websocket.CloseGoingAway, "The bridge was turned off.")
	}
	server := s.http
	s.http = nil
	s.port = nil
	s.unlock()
	if stopWorkspace != nil {
		stopWorkspace()
	}
	if server != nil {
		_ = server.Close()
	}
}

func (s *Server) computerName() *string {
	if name := s.name(); name != "" {
		return &name
	}
	return nil
}

func (s *Server) snapshotMessage(session *liveSession) *contracts.MobileSnapshotMessage {
	instance, push := s.instance, true
	return &contracts.MobileSnapshotMessage{
		SessionID:  session.ID,
		View:       *s.view,
		Instance:   &instance,
		Vocabulary: application.MobileVocabulary(),
		Computer:   s.computerName(),
		Push:       &push,
	}
}

// Publish is what every phone should see now. Offline sessions are numbered too, so a resume replays
// them. A whole view is ground truth, so a phone still waiting for one is handed it too.
func (s *Server) Publish(update contracts.MobileViewUpdate) {
	s.lock()
	defer s.unlock()
	var patch *contracts.MobilePatch
	switch update := update.(type) {
	case contracts.MobileViewUpdateSnapshot:
		view := update.View
		s.view = &view
	case contracts.MobileViewUpdatePatch:
		patch = &update.Patch
		if s.view != nil {
			s.view = application.ApplyMobilePatch(s.view, patch)
		}
	default:
		return
	}
	for _, session := range s.order {
		if session.Kind != domain.PairedDeviceKindPhone {
			continue
		}
		if patch == nil {
			session.awaitingSnapshot = false
			s.emit(session, s.snapshotMessage(session), nil)
		} else if !session.awaitingSnapshot {
			if congested(session) {
				s.fallBehind(session)
			} else {
				s.emit(session, &contracts.MobilePatchMessage{Patch: *patch}, nil)
			}
		}
	}
}

// fallBehind: a phone whose socket still holds what it was handed earlier is handed no more: patches
// would queue faster than they drain, and the phone would read a reply seconds late, step by step. It
// is owed the view instead, handed whole once the line drains, or kept for its resume if it drops.
func (s *Server) fallBehind(session *liveSession) {
	session.awaitingSnapshot = true
	if session.catchingUp != nil {
		return
	}
	session.catchingUp = time.AfterFunc(catchUp, func() {
		s.lock()
		defer s.unlock()
		session.catchingUp = nil
		if s.sessions[session.ID] != session || !session.awaitingSnapshot {
			return
		}
		if congested(session) {
			s.fallBehind(session)
		} else {
			s.sendSnapshot(session)
		}
	})
}

// DropDevice: a revoked phone loses its session with its token, mid-conversation if that is where it is.
func (s *Server) DropDevice(deviceID string) {
	s.lock()
	defer s.unlock()
	for _, session := range slices.Clone(s.order) {
		if session.DeviceID != deviceID {
			continue
		}
		s.forget(session)
		reason := "This phone was unpaired."
		if session.Kind == domain.PairedDeviceKindComputer {
			reason = "This computer was unpaired."
		}
		hangUp(session.socket, 4003, reason)
	}
	delete(s.handled, deviceID)
}

// forget is a session the server no longer holds, whose socket may still be delivering messages. What
// its device has already run is kept: a phone that sleeps past the session's grace wakes with the same
// outbox, and would otherwise run every command whose acknowledgement it never read a second time.
func (s *Server) forget(session *liveSession) {
	delete(s.sessions, session.ID)
	s.order = slices.DeleteFunc(slices.Clone(s.order), func(held *liveSession) bool { return held == session })
	// With nobody left the window stops publishing, so what was kept would only go stale.
	if len(s.sessions) == 0 {
		s.view = nil
	}
}

// emit numbers one message for a session, keeps it for a resume, and writes it. message is a pointer
// to a server message; sent runs with the lock held once it is written or never will be.
func (s *Server) emit(session *liveSession, message any, sent func()) {
	session.Sequence++
	stamp(message, session.Sequence)
	text, err := application.StringifyWorkspaceJSON(message)
	if err != nil {
		log.Printf("Could not write a message for a phone: %v", err)
		if sent != nil {
			sent()
		}
		return
	}
	session.buffer = append(session.buffer, bufferedMessage{sequence: session.Sequence, text: text})
	session.bufferBytes += len(text)
	for len(session.buffer) > domain.MobileEventBuffer || session.bufferBytes > domain.MobileBufferBytes && len(session.buffer) > 1 {
		session.bufferBytes -= len(session.buffer[0].text)
		session.buffer = session.buffer[1:]
	}
	write(session.socket, text, sent)
}

func stamp(message any, sequence float64) {
	switch message := message.(type) {
	case *contracts.MobilePairedMessage:
		message.Sequence = sequence
	case *contracts.MobileSnapshotMessage:
		message.Sequence = sequence
	case *contracts.MobilePatchMessage:
		message.Sequence = sequence
	case *contracts.MobileAckMessageTrue:
		message.Sequence = sequence
	case *contracts.MobileAckMessageFalse:
		message.Sequence = sequence
	case *contracts.MobileAnswerMessageTrue:
		message.Sequence = sequence
	case *contracts.MobileAnswerMessageFalse:
		message.Sequence = sequence
	case *contracts.MobileErrorMessage:
		message.Sequence = sequence
	case *contracts.MobilePingMessage:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageWorkspace:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageResult:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageAnswer:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageNotice:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageName:
		message.Sequence = sequence
	case *contracts.ComputerServerMessageCapabilities:
		message.Sequence = sequence
	default:
		panic("mobile: unsequenced message")
	}
}

func (s *Server) openSession(kind domain.PairedDeviceKind, deviceID, deviceName string, at float64) *liveSession {
	opened := &liveSession{
		MobileSession: domain.MobileSession{
			ID:         application.NewID(),
			Kind:       kind,
			DeviceID:   deviceID,
			StartedAt:  at,
			LastSeenAt: at,
			Connection: domain.MobileConnectionStateLive,
		},
		deviceName:       deviceName,
		awaitingSnapshot: true,
		transfers:        map[string]float64{},
	}
	s.sessions[opened.ID] = opened
	s.order = append(slices.Clip(s.order), opened)
	if _, ok := s.handled[deviceID]; !ok {
		s.handled[deviceID] = newHandledRequests()
	}
	var held []*liveSession
	for _, entry := range s.order {
		if entry.DeviceID == deviceID {
			held = append(held, entry)
		}
	}
	for _, stale := range held[:max(0, len(held)-domain.MaxSessionsPerDevice)] {
		s.forget(stale)
		hangUp(stale.socket, 4002, "This phone connected again.")
	}
	// A phone that opens a session has given up on the ones it dropped: it resumes the one it names,
	// and here that failed. Kept, they would be published to, and keep the computer awake, until they expire.
	if kind == domain.PairedDeviceKindPhone {
		for _, dropped := range held {
			if dropped != opened && dropped.Kind == domain.PairedDeviceKindPhone && dropped.socket == nil {
				s.forget(dropped)
			}
		}
	}
	return opened
}

func (s *Server) grace() time.Duration {
	if s.options.SessionGrace != nil {
		return *s.options.SessionGrace
	}
	return sessionGrace
}

// attach: one socket per session. A phone that opens a second one takes the session, and the first is closed.
func (s *Server) attach(session *liveSession, socket *socket) {
	if session.socket != nil && session.socket != socket {
		hangUp(session.socket, 4002, "This phone connected again.")
	}
	session.socket = socket
	clear(session.transfers)
	session.Connection = domain.MobileConnectionStateLive
	session.expiresAt = nil
	session.LastSeenAt = application.Now()
	state := s.sockets[socket]
	if state == nil {
		return
	}
	state.closers = append(state.closers, func() {
		if session.socket != socket {
			return
		}
		session.socket = nil
		clear(session.transfers)
		session.Connection = domain.MobileConnectionStateOffline
		expires := application.Now() + float64(s.grace().Milliseconds())
		session.expiresAt = &expires
		// With the line to the computer that was looking gone, nothing here is being watched.
		if session.Kind == domain.PairedDeviceKindComputer && s.options.Workspace != nil {
			workspace := s.options.Workspace
			go func() {
				_, _ = workspace.Input(context.Background(), []application.WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: false}})
			}()
		}
		s.changed = true
	})
}

// sendSnapshot hands a session the view whole, asking the window only when no view is kept.
func (s *Server) sendSnapshot(session *liveSession) {
	if session.Kind == domain.PairedDeviceKindComputer {
		workspace := s.options.Workspace
		if workspace == nil {
			s.snapshotFailed(session, errors.New("This computer takes no other computers."))
			return
		}
		session.snapshotting = true
		session.held = nil
		go func() {
			update := workspace.Snapshot()
			handed, err := handedOver(update)
			s.lock()
			defer s.unlock()
			session.snapshotting = false
			held := session.held
			session.held = nil
			if err != nil {
				s.snapshotFailed(session, err)
				return
			}
			session.awaitingSnapshot = false
			sessionID := session.ID
			s.emit(session, &contracts.ComputerServerMessageWorkspace{SessionID: &sessionID, Update: handed}, nil)
			for _, later := range held {
				if later.Revision > handed.Revision {
					s.emit(session, &contracts.ComputerServerMessageWorkspace{Update: later}, nil)
				}
			}
		}()
		return
	}
	if s.view != nil {
		session.awaitingSnapshot = false
		s.emit(session, s.snapshotMessage(session), nil)
		return
	}
	go func() {
		view, err := s.options.Snapshot(context.Background(), session.ID)
		s.lock()
		defer s.unlock()
		if err != nil {
			s.snapshotFailed(session, err)
			return
		}
		// A whole view published while this one was fetched has been handed over already, and is newer.
		if !session.awaitingSnapshot {
			return
		}
		s.view = view
		session.awaitingSnapshot = false
		s.emit(session, s.snapshotMessage(session), nil)
	}()
}

// snapshotFailed: a session with no view to patch is no session. The phone hears why, is hung up on,
// and redials on its backoff into a fresh session that asks for the view again.
func (s *Server) snapshotFailed(session *liveSession, err error) {
	message := errorMessage(err)
	s.emit(session, &contracts.MobileErrorMessage{Code: contracts.MobileErrorCodeInternal, Message: message}, nil)
	s.forget(session)
	reason := "The view could not be read."
	if session.Kind == domain.PairedDeviceKindComputer {
		reason = message
	}
	hangUp(session.socket, websocket.CloseInternalServerErr, reason)
	s.changed = true
}

// requestPath is the request's path as a URL parser reads it, dot segments resolved.
func requestPath(request *http.Request) string {
	base := &url.URL{Scheme: "http", Host: "127.0.0.1", Path: "/"}
	return base.ResolveReference(&url.URL{Path: request.URL.Path}).Path
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if websocket.IsWebSocketUpgrade(request) {
		s.upgrade(response, request)
		return
	}
	s.serve(response, request)
}

func (s *Server) upgrade(response http.ResponseWriter, request *http.Request) {
	path := requestPath(request)
	var kind domain.PairedDeviceKind
	switch {
	case path == socketPath:
		kind = domain.PairedDeviceKindPhone
	case path == WorkspaceSocketPath && s.options.Workspace != nil:
		kind = domain.PairedDeviceKindComputer
	default:
		refuseUpgrade(response, http.StatusBadRequest)
		return
	}
	if !s.originAllowed(request) {
		refuseUpgrade(response, http.StatusForbidden)
		return
	}
	s.lock()
	running, crowded := s.http != nil, s.pending >= domain.MaxPendingSockets
	s.unlock()
	if !running {
		refuseUpgrade(response, http.StatusBadRequest)
		return
	}
	if crowded {
		refuseUpgrade(response, http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(response, request, nil)
	if err != nil {
		return
	}
	s.accept(conn, sourceOf(request), kind)
}

// originAllowed: a browser may open a socket only from one of this server's own addresses. A request
// with no origin at all is not one a browser made, and is left to the token to judge.
func (s *Server) originAllowed(request *http.Request) bool {
	values := request.Header.Values("Origin")
	if len(values) == 0 {
		return true
	}
	origin := joinHeader(values)
	if s.options.AllowedOrigins != nil && slices.Contains(s.options.AllowedOrigins(), origin) {
		return true
	}
	// An origin naming the very host the socket was opened on is our own, whatever the host is called:
	// a browser cannot forge the Host header, and Tailscale Serve passes the name and scheme it answered on.
	host := forwardedHost(request)
	return host != "" && origin == forwardedScheme(request)+"://"+host
}

func joinHeader(values []string) string {
	joined := values[0]
	for _, value := range values[1:] {
		joined += ", " + value
	}
	return joined
}

func (s *Server) accept(conn *websocket.Conn, source string, kind domain.PairedDeviceKind) {
	conn.SetReadLimit(maxSocketMessage)
	socket := newSocket(conn, s.runLocked)
	state := &accepted{}
	s.lock()
	if s.http == nil {
		s.unlock()
		socket.terminate()
		return
	}
	s.sockets[socket] = state
	s.pending++
	state.deadline = time.AfterFunc(authDeadline, func() {
		s.lock()
		defer s.unlock()
		if state.session == nil {
			hangUp(socket, 4008, "This phone never said who it was.")
		}
	})
	s.unlock()
	named := func() {
		if s.pending > 0 {
			s.pending--
		}
		state.deadline.Stop()
	}
	go socket.read(func(data []byte) {
		s.lock()
		defer s.unlock()
		s.onFrame(socket, state, data, source, kind, named)
	}, func() {
		s.lock()
		defer s.unlock()
		delete(s.sockets, socket)
		if state.session == nil {
			named()
		} else {
			state.deadline.Stop()
		}
		for _, closer := range state.closers {
			closer()
		}
	})
}

func (s *Server) current(session *liveSession) bool {
	return session != nil && s.sessions[session.ID] == session
}

func (s *Server) onFrame(socket *socket, state *accepted, data []byte, source string, kind domain.PairedDeviceKind, named func()) {
	message := readClientMessage(data, kind)
	session := state.session
	if message == nil && s.current(session) && session.Kind == domain.PairedDeviceKindComputer {
		if rejected := rejectedComputerRequest(data); rejected != nil {
			delete(session.transfers, rejected.requestID)
			if rejected.kind == "input" {
				s.emit(session, &contracts.ComputerServerMessageResult{RequestID: rejected.requestID, Result: contracts.ComputerServerMessageResultResultFalse{Message: contracts.RemoteUnsupported}}, nil)
			} else {
				unsupported := contracts.RemoteUnsupported
				s.emit(session, &contracts.ComputerServerMessageAnswer{RequestID: rejected.requestID, Message: &unsupported}, nil)
			}
			return
		}
	}
	// A phone's request that is not one it may make is answered as refused; hanging up would only have it sent again.
	if message == nil && s.current(session) && session.Kind == domain.PairedDeviceKindPhone {
		if rejected := rejectedPhoneRequest(data); rejected != nil {
			if rejected.kind == "command" {
				s.ack(session, rejected.requestID, outcome{message: phoneUnreadable})
			} else {
				s.emit(session, &contracts.MobileAnswerMessageFalse{RequestID: rejected.requestID, Message: phoneUnreadable}, nil)
			}
			return
		}
	}
	if message == nil {
		if kind == domain.PairedDeviceKindComputer {
			refuse(socket, contracts.MobileErrorCodeUnreadable, "The remote computer could not read this request. Updating AI Coding Tool on the remote computer may help.")
		} else {
			refuse(socket, contracts.MobileErrorCodeUnreadable, "That message could not be read.")
		}
		return
	}
	if session != nil {
		// A session dropped while its socket lingers — revoked, or the bridge turned off — is over.
		if !s.current(session) {
			return
		}
		session.LastSeenAt = application.Now()
		if session.Kind == domain.PairedDeviceKindComputer {
			if computer, ok := message.(application.ComputerClientMessage); ok {
				s.onComputerMessage(session, computer)
			}
		} else if phone, ok := message.(contracts.MobileClientMessage); ok {
			s.onSessionMessage(session, phone)
		}
		return
	}
	var opened *liveSession
	switch request := message.(type) {
	case contracts.MobilePairRequest:
		opened = s.onPair(socket, pairing{version: request.Version, oldestComputer: request.OldestComputer, code: request.Code, deviceName: request.DeviceName}, source, kind)
	case contracts.ComputerPairRequest:
		opened = s.onPair(socket, pairing{version: request.Version, code: request.Code, deviceName: request.DeviceName}, source, kind)
	case contracts.MobileResumeRequest:
		opened = s.onResume(socket, resumption{version: request.Version, oldestComputer: request.OldestComputer, token: request.Token, sessionID: request.SessionID, lastSequence: request.LastSequence}, kind)
	case contracts.ComputerResumeRequest:
		opened = s.onResume(socket, resumption{version: request.Version, token: request.Token, sessionID: request.SessionID, lastSequence: request.LastSequence}, kind)
	}
	if opened == nil {
		return
	}
	named()
	state.session = opened
	s.changed = true
}

// onSessionMessage: a phone re-sends a command whose ack it never saw, so a request ID its device has
// already run is answered from what it decided the first time and never reaches the window twice.
// The memory is the device's rather than the session's, because a phone that falls past the buffer
// resumes into a fresh session with the same outbox.
func (s *Server) onSessionMessage(session *liveSession, message contracts.MobileClientMessage) {
	switch message := message.(type) {
	case contracts.MobileQueryRequest:
		s.onQuery(session, message)
		return
	case contracts.MobileProbeMessage:
		// A phone checking the line after a sleep or a change of network is answered at once.
		s.emit(session, &contracts.MobilePingMessage{At: application.Now()}, nil)
		return
	case contracts.MobilePushTokenMessage:
		s.options.Devices.SetPush(session.DeviceID, message.Push)
		return
	case contracts.MobileCommandRequest:
		s.onCommand(session, message)
	}
}

func (s *Server) onCommand(session *liveSession, message contracts.MobileCommandRequest) {
	requestID := message.RequestID
	handled := s.handled[session.DeviceID]
	if handled == nil {
		return
	}
	_, edit := message.Command.(contracts.TaskCommandTaskEditQueued)
	// An edit's outcome outlives this server, since the phone keeps the words until it hears it.
	if edit && !handled.has(requestID) {
		if stored := s.options.Devices.EditOutcome(session.DeviceID, requestID); stored != nil {
			handled.set(requestID, &outcome{ok: stored.OK, message: stored.Message})
		}
	}
	if handled.has(requestID) {
		if settled := handled.outcomes[requestID]; settled != nil {
			s.ack(session, requestID, *settled)
		}
		return
	}
	if handled.inFlight() >= maxCommandsInFlight {
		s.ack(session, requestID, outcome{message: "This phone has too many commands still waiting on the computer."})
		return
	}
	handled.set(requestID, nil)
	handled.trim()
	deviceID := session.DeviceID
	go func() {
		err := s.options.Command(context.Background(), session.ID, message.Command)
		settled := outcome{ok: err == nil, message: errorMessage(err)}
		s.lock()
		defer s.unlock()
		if edit {
			s.options.Devices.RememberEdit(deviceID, requestID, EditOutcome{OK: settled.ok, Message: settled.message})
		}
		if handled.has(requestID) {
			handled.set(requestID, &settled)
		}
		s.ack(s.answering(session), requestID, settled)
	}()
}

func computerRequestID(message application.ComputerClientMessage) string {
	switch message := message.(type) {
	case application.ComputerInputRequest:
		return message.RequestID
	case contracts.ComputerQueryRequest:
		return message.RequestID
	case application.ComputerClientMessageTransfer:
		return message.RequestID
	}
	return ""
}

func supportsInputs(inputs []application.WorkspaceInput) bool {
	capabilities := contracts.ComputerCapabilitiesComputerCapabilities()
	for _, input := range inputs {
		command, ok := input.(contracts.AppCommand)
		if !ok || !contracts.SupportsComputerCommand(capabilities, command) {
			return false
		}
	}
	return true
}

// encodeResult is a read's answer as it goes on the wire; nothing is no result at all.
func encodeResult(result any) (jsontext.Value, error) {
	switch result := result.(type) {
	case nil:
		return nil, nil
	case jsontext.Value:
		return result, nil
	}
	return application.StringifyWorkspaceJSON(result)
}

// onComputerMessage: another computer's inputs run one after another, so a send lands after the
// recall of what it carries, and are answered once, with the last one's result. A request already
// run is answered from what it decided, the way a phone's is.
func (s *Server) onComputerMessage(session *liveSession, message application.ComputerClientMessage) {
	workspace := s.options.Workspace
	if workspace == nil {
		return
	}
	switch message.(type) {
	case contracts.ComputerPairRequest, contracts.ComputerResumeRequest, application.ComputerClientMessagePong:
		return
	}
	requestID := computerRequestID(message)
	if input, ok := message.(application.ComputerInputRequest); ok && !supportsInputs(input.Inputs) {
		delete(session.transfers, requestID)
		s.emit(session, &contracts.ComputerServerMessageResult{RequestID: requestID, Result: contracts.ComputerServerMessageResultResultFalse{Message: contracts.RemoteUnsupported}}, nil)
		return
	}
	if query, ok := message.(contracts.ComputerQueryRequest); ok && !contracts.SupportsComputerQuery(contracts.ComputerCapabilitiesComputerCapabilities(), query.Query) {
		delete(session.transfers, requestID)
		unsupported := contracts.RemoteUnsupported
		s.emit(session, &contracts.ComputerServerMessageAnswer{RequestID: requestID, Message: &unsupported}, nil)
		return
	}
	if _, ok := message.(application.ComputerClientMessageTransfer); ok {
		if _, held := session.transfers[requestID]; !held && len(session.transfers) < maxCommandsInFlight {
			session.transfers[requestID] = application.Now() + contracts.ComputerTransferTimeoutMs
		}
		return
	}
	transferring := application.IsComputerTransfer(message)
	if !transferring {
		delete(session.transfers, requestID)
	}
	if _, held := session.transfers[requestID]; transferring && !held {
		session.transfers[requestID] = application.Now() + contracts.ComputerTransferTimeoutMs
	}
	sent := func() { delete(session.transfers, requestID) }
	if query, ok := message.(contracts.ComputerQueryRequest); ok {
		if session.queries >= maxQueriesInFlight {
			busy := "That computer is asking for too much at once."
			s.emit(session, &contracts.ComputerServerMessageAnswer{RequestID: requestID, Message: &busy}, sent)
			return
		}
		session.queries++
		go func() {
			result, err := workspace.Query(context.Background(), query.Query)
			var encoded jsontext.Value
			if err == nil {
				encoded, err = encodeResult(result)
			}
			s.lock()
			defer s.unlock()
			if err != nil {
				failure := errorMessage(err)
				s.emit(s.answering(session), &contracts.ComputerServerMessageAnswer{RequestID: requestID, Message: &failure}, sent)
			} else {
				s.emit(s.answering(session), &contracts.ComputerServerMessageAnswer{RequestID: requestID, Ok: true, Result: encoded}, sent)
			}
			session.queries--
		}()
		return
	}
	input, ok := message.(application.ComputerInputRequest)
	if !ok {
		return
	}
	handled := s.handled[session.DeviceID]
	if handled == nil {
		return
	}
	// One still running is answered when it settles, on whichever line the computer has by then.
	if handled.has(requestID) {
		if settled := handled.outcomes[requestID]; settled != nil {
			s.emit(session, &contracts.ComputerServerMessageResult{RequestID: requestID, Result: settled.result(0)}, sent)
		}
		return
	}
	if handled.inFlight() >= maxCommandsInFlight {
		s.emit(session, &contracts.ComputerServerMessageResult{RequestID: requestID, Result: contracts.ComputerServerMessageResultResultFalse{Message: "That computer has too much still waiting."}}, sent)
		return
	}
	handled.set(requestID, nil)
	handled.trim()
	go func() {
		result, err := workspace.Input(context.Background(), input.Inputs)
		s.lock()
		defer s.unlock()
		if err != nil {
			failed := outcome{message: errorMessage(err)}
			if handled.has(requestID) {
				handled.set(requestID, &failed)
			}
			s.emit(s.answering(session), &contracts.ComputerServerMessageResult{RequestID: requestID, Result: failed.result(0)}, sent)
			return
		}
		settled := outcomeOf(result)
		if handled.has(requestID) {
			handled.set(requestID, &settled)
		}
		s.emit(s.answering(session), &contracts.ComputerServerMessageResult{RequestID: requestID, Result: result}, sent)
	}()
}

// answering is where a request's outcome goes: the session that asked while its line is up, else
// whichever session of the same device has one. A device that lost the line may have come back as a
// new session, and asks again for what it is still owed; the answer must find it there.
func (s *Server) answering(session *liveSession) *liveSession {
	if session.socket != nil && s.current(session) {
		return session
	}
	var live *liveSession
	for _, entry := range s.order {
		if entry.DeviceID == session.DeviceID && entry.socket != nil && (live == nil || entry.StartedAt >= live.StartedAt) {
			live = entry
		}
	}
	if live != nil {
		return live
	}
	return session
}

// onQuery: a read is answered once and never remembered: asking twice reads twice, which is harmless,
// and a phone that lost the answer asks again itself. Too many at once are refused rather than queued.
func (s *Server) onQuery(session *liveSession, message contracts.MobileQueryRequest) {
	requestID := message.RequestID
	if session.queries >= maxQueriesInFlight {
		s.emit(session, &contracts.MobileAnswerMessageFalse{RequestID: requestID, Message: "This phone is asking for too much at once."}, nil)
		return
	}
	session.queries++
	go func() {
		result, err := s.options.Query(context.Background(), session.ID, message.Query)
		s.lock()
		defer s.unlock()
		if err != nil {
			s.emit(session, &contracts.MobileAnswerMessageFalse{RequestID: requestID, Message: errorMessage(err)}, nil)
		} else {
			s.emit(session, &contracts.MobileAnswerMessageTrue{RequestID: requestID, Result: result}, nil)
		}
		session.queries--
	}()
}

func (s *Server) ack(session *liveSession, requestID string, settled outcome) {
	if settled.ok {
		s.emit(session, &contracts.MobileAckMessageTrue{RequestID: requestID}, nil)
	} else {
		s.emit(session, &contracts.MobileAckMessageFalse{RequestID: requestID, Message: settled.message}, nil)
	}
}

type pairing struct {
	version        float64
	oldestComputer *float64
	code           string
	deviceName     string
}

func (s *Server) onPair(socket *socket, request pairing, source string, kind domain.PairedDeviceKind) *liveSession {
	if !versionAccepted(request.version, request.oldestComputer, kind, socket) {
		return nil
	}
	// A code buys a device, so it is not spent on a socket that has already gone.
	if !socket.open() {
		return nil
	}
	paired := s.options.Devices.Redeem(request.code, request.deviceName, source, application.Now(), kind)
	if !paired.OK {
		refuse(socket, paired.Code, paired.Message)
		return nil
	}
	session := s.openSession(kind, paired.Device.ID, paired.Device.Name, application.Now())
	s.attach(session, socket)
	s.emit(session, &contracts.MobilePairedMessage{DeviceID: paired.Device.ID, DeviceName: paired.Device.Name, Token: paired.Token}, nil)
	// A token nobody received is a device that will never connect, so it is taken back.
	if !socket.open() {
		s.forget(session)
		s.options.Devices.Revoke(paired.Device.ID)
		return nil
	}
	s.sendCapabilities(session)
	s.sendName(session, s.name())
	s.sendSnapshot(session)
	return session
}

type resumption struct {
	version        float64
	oldestComputer *float64
	token          string
	sessionID      *string
	lastSequence   float64
}

// onResume: device tokens are 256 bits, so guessing one is not a threat worth rate-limiting — and
// counting unknown tokens against the pairing bucket would let any peer that can reach the port lock
// the real phone out, since a reverse proxy makes every phone the same source address.
func (s *Server) onResume(socket *socket, request resumption, kind domain.PairedDeviceKind) *liveSession {
	if !versionAccepted(request.version, request.oldestComputer, kind, socket) {
		return nil
	}
	now := application.Now()
	device := s.options.Devices.Authenticate(request.token, now)
	// A token this computer cannot check yet is not a token it refused: the device keeps it and asks again.
	if device == nil && s.options.Devices.Unreadable() {
		refuse(socket, contracts.MobileErrorCodeInternal, "This computer could not read its paired devices. Try again in a moment.")
		return nil
	}
	if device == nil || device.Kind != kind {
		if kind == domain.PairedDeviceKindComputer {
			refuse(socket, contracts.MobileErrorCodeUnauthorized, "This computer is not paired with the other one.")
		} else {
			refuse(socket, contracts.MobileErrorCodeUnauthorized, "This phone is not paired with this computer.")
		}
		return nil
	}
	s.options.Devices.MarkSeen(device.ID, now)
	var held *liveSession
	if request.sessionID != nil {
		held = s.sessions[*request.sessionID]
	}
	if held != nil && held.DeviceID == device.ID && resumable(held, request.lastSequence) {
		s.attach(held, socket)
		for _, entry := range held.buffer {
			if entry.sequence > request.lastSequence {
				write(socket, entry.text, nil)
			}
		}
		// A phone with nothing to catch up on is still owed a frame, or it waits for the next tick to call itself live.
		s.emit(held, &contracts.MobilePingMessage{At: now}, nil)
		s.sendCapabilities(held)
		s.sendName(held, s.name())
		return held
	}
	session := s.openSession(kind, device.ID, device.Name, now)
	s.attach(session, socket)
	s.sendCapabilities(session)
	s.sendName(session, s.name())
	s.sendSnapshot(session)
	return session
}

// tick pings what is connected, hangs up on what has gone quiet, and forgets what will not come back.
func (s *Server) tick() {
	s.lock()
	defer s.unlock()
	now := application.Now()
	for _, session := range slices.Clone(s.order) {
		if session.socket != nil {
			for id, deadline := range session.transfers {
				if deadline <= now {
					delete(session.transfers, id)
				}
			}
			if len(session.transfers) == 0 && now-session.LastSeenAt > domain.MobileDeadAfterMs {
				session.socket.terminate()
				continue
			}
			s.emit(session, &contracts.MobilePingMessage{At: now}, nil)
			continue
		}
		if session.expiresAt != nil && now >= *session.expiresAt {
			s.forget(session)
			s.changed = true
		}
	}
}

func (s *Server) serve(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		plain(response, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	switch requestPath(request) {
	case socketPath:
		plain(response, http.StatusBadRequest, "This address is a WebSocket.")
	case MobileHealthPath:
		// The instance header tells this process's server from another copy's behind the same name.
		response.Header().Set(MobileInstanceHeader, MobileInstance)
		plain(response, http.StatusOK, MobileHealthResponse)
	default:
		plain(response, http.StatusNotFound, "Not found")
	}
}

// resumable is whether the buffer still reaches back to what the phone says it saw.
func resumable(session *liveSession, lastSequence float64) bool {
	if lastSequence > session.Sequence {
		return false
	}
	if lastSequence == session.Sequence {
		return true
	}
	return len(session.buffer) > 0 && session.buffer[0].sequence <= lastSequence+1
}

// write hands text to a socket; sent runs at once when there is no open socket to hand it to.
func write(socket *socket, text []byte, sent func()) {
	if socket != nil {
		socket.send(text, sent)
	} else if sent != nil {
		sent()
	}
}

func hangUp(socket *socket, code int, reason string) {
	if socket != nil {
		socket.hangUp(code, reason)
	}
}

// refuse is the last thing a socket hears: it carries no session, so it is numbered zero.
func refuse(socket *socket, code contracts.MobileErrorCode, message string) {
	if socket.open() {
		if text, err := jsonx.Marshal(&contracts.MobileErrorMessage{Code: code, Message: message}); err == nil {
			socket.send(text, nil)
		}
	}
	socket.hangUp(4001, string(code))
}

func refuseUpgrade(response http.ResponseWriter, status int) {
	response.Header().Set("Connection", "close")
	response.WriteHeader(status)
}

// congested is whether a phone's socket still holds more unsent than a patch should queue behind.
func congested(session *liveSession) bool {
	return session.socket != nil && session.socket.bufferedAmount() > domain.MobileCongestedBytes
}

// versionAccepted: computers connect best effort across versions; a phone app and this computer must each read the other.
func versionAccepted(version float64, oldestComputer *float64, kind domain.PairedDeviceKind, socket *socket) bool {
	if kind == domain.PairedDeviceKindComputer {
		return true
	}
	if version < contracts.OldestPhoneProtocolVersion {
		refuse(socket, contracts.MobileErrorCodeVersion, "This app is out of date for your computer. Update the app to carry on.")
		return false
	}
	oldest := 0.0
	if oldestComputer != nil {
		oldest = *oldestComputer
	}
	if contracts.MobileProtocolVersion < oldest {
		refuse(socket, contracts.MobileErrorCodeVersion, "AI Coding Tool on your computer is out of date for this app. Update it on your computer to carry on.")
		return false
	}
	return true
}

func plain(response http.ResponseWriter, status int, text string) {
	headers := response.Header()
	headers.Set("content-type", "text/plain; charset=utf-8")
	headers.Set("cache-control", "no-store")
	for name, value := range securityHeaders {
		headers.Set(name, value)
	}
	response.WriteHeader(status)
	_, _ = response.Write([]byte(text))
}
