package mobile

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// closeGrace is how long a hung-up socket is given to answer the close handshake. A client that never
// answers would otherwise keep delivering messages, so it is cut instead.
const closeGrace = 250 * time.Millisecond

const controlTimeout = 5 * time.Second

type socketState int

const (
	socketOpen socketState = iota
	socketClosing
	socketClosed
)

type outbound struct {
	text []byte
	// sent runs once the text is written or will never be, through the socket's locked runner.
	sent func()
	// closing frames end the queue.
	closing bool
	code    int
	reason  string
}

// socket is one accepted WebSocket: frames queue for a writer of their own so a sender never waits on
// the network, and what is queued but unwritten is the socket's buffered amount.
type socket struct {
	conn *websocket.Conn
	// locked runs a sent callback with the server's lock held.
	locked func(func())

	mu       sync.Mutex
	state    socketState
	queue    []outbound
	buffered int
	wake     chan struct{}
	closed   chan struct{}
	once     sync.Once
}

func newSocket(conn *websocket.Conn, locked func(func())) *socket {
	s := &socket{conn: conn, locked: locked, wake: make(chan struct{}, 1), closed: make(chan struct{})}
	go s.write()
	return s
}

// open is the socket's readyState being OPEN.
func (s *socket) open() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == socketOpen
}

// bufferedAmount is what has been handed to the socket and not yet written.
func (s *socket) bufferedAmount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffered
}

func (s *socket) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// send queues one text frame. Whoever calls it holds the lock sent runs under, so a socket that is
// not open runs sent at once.
func (s *socket) send(text []byte, sent func()) {
	s.mu.Lock()
	if s.state != socketOpen {
		s.mu.Unlock()
		if sent != nil {
			sent()
		}
		return
	}
	s.queue = append(s.queue, outbound{text: text, sent: sent})
	s.buffered += len(text)
	s.mu.Unlock()
	s.signal()
}

// hangUp closes with a code, and cuts the socket shortly after: a close is a handshake a client is
// free to ignore, and one that does would keep delivering messages.
func (s *socket) hangUp(code int, reason string) {
	s.mu.Lock()
	if s.state == socketOpen {
		s.state = socketClosing
		s.queue = append(s.queue, outbound{closing: true, code: code, reason: closeReason(reason)})
	}
	s.mu.Unlock()
	s.signal()
	time.AfterFunc(closeGrace, s.terminate)
}

// terminate cuts the connection at once.
func (s *socket) terminate() {
	s.once.Do(func() {
		s.mu.Lock()
		s.state = socketClosed
		s.mu.Unlock()
		close(s.closed)
		_ = s.conn.NetConn().Close()
		s.signal()
	})
}

func (s *socket) write() {
	for {
		select {
		case <-s.wake:
		case <-s.closed:
			s.drain()
			return
		}
		for {
			s.mu.Lock()
			if len(s.queue) == 0 {
				s.mu.Unlock()
				break
			}
			next := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			if next.closing {
				_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(next.code, next.reason), time.Now().Add(controlTimeout))
				continue
			}
			err := s.conn.WriteMessage(websocket.TextMessage, next.text)
			s.mu.Lock()
			s.buffered -= len(next.text)
			s.mu.Unlock()
			if next.sent != nil {
				s.locked(next.sent)
			}
			if err != nil {
				s.terminate()
			}
		}
	}
}

// drain runs what never went out, as ws calls back a write the socket dropped.
func (s *socket) drain() {
	s.mu.Lock()
	left := s.queue
	s.queue = nil
	s.buffered = 0
	s.mu.Unlock()
	for _, item := range left {
		if item.sent != nil {
			s.locked(item.sent)
		}
	}
}

// read delivers each text or binary message until the connection ends, then reports the close once.
func (s *socket) read(message func([]byte), closed func()) {
	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			break
		}
		message(data)
	}
	s.terminate()
	closed()
}
