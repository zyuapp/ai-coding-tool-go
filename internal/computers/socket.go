package computers

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// maxPayload is what the `ws` client takes in one message by default.
const maxPayload = 100 * 1024 * 1024

// writeBuffer is how much of a message goes in one frame. `ws` refuses a message in more than 16384
// fragments, so a large message must not be cut into small ones; the buffer is pooled, held only
// while a message is written.
const writeBuffer = 1 << 20

var writeBuffers = &sync.Pool{}

// Close codes as `ws` reports them.
const (
	closeNormal   = 1000
	closeAbnormal = 1006
)

// socketHandlers are told, in the loop, what one dial does. close is told once.
type socketHandlers struct {
	open    func()
	message func(data []byte)
	close   func(code int, reason string)
}

// socket is one dial of a WebSocket. Its methods run in the loop; the dial, the reads and the
// writes run on goroutines of their own and report back through the loop.
type socket struct {
	loop     *loop
	handlers socketHandlers
	cancel   context.CancelFunc
	conn     *websocket.Conn
	open     bool
	ended    bool
	// terminated is set once the socket is cut, so a dial still finishing is closed at once.
	terminated bool
	// heardAt is when the last raw bytes arrived, in application.Now milliseconds.
	heardAt atomic.Int64
	out     *writer
}

// dial opens a socket to url, telling handlers in the loop as it opens, speaks and closes.
func dial(l *loop, url string, handshake time.Duration, handlers socketHandlers) *socket {
	ctx, cancel := context.WithCancel(context.Background())
	s := &socket{loop: l, handlers: handlers, cancel: cancel}
	go s.run(ctx, url, handshake)
	return s
}

func (s *socket) run(ctx context.Context, url string, handshake time.Duration) {
	dialer := websocket.Dialer{
		HandshakeTimeout: handshake,
		WriteBufferSize:  writeBuffer,
		WriteBufferPool:  writeBuffers,
		NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return &heardConn{Conn: conn, socket: s}, nil
		},
	}
	conn, _, err := dialer.DialContext(ctx, url, nil)
	if err != nil {
		s.loop.post(func() { s.end(closeAbnormal, "") })
		return
	}
	conn.SetReadLimit(maxPayload)
	out := newWriter(conn)
	s.loop.post(func() {
		if s.terminated || s.ended {
			_ = conn.NetConn().Close()
			return
		}
		s.conn = conn
		s.out = out
		s.open = true
		s.handlers.open()
	})
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			code, reason := closeAbnormal, ""
			var closed *websocket.CloseError
			if errors.As(err, &closed) {
				code, reason = closed.Code, closed.Text
			}
			out.stop()
			_ = conn.NetConn().Close()
			s.loop.post(func() { s.end(code, reason) })
			return
		}
		s.loop.post(func() {
			if s.open {
				s.handlers.message(data)
			}
		})
	}
}

func (s *socket) end(code int, reason string) {
	if s.ended {
		return
	}
	s.ended = true
	s.open = false
	s.handlers.close(code, reason)
}

// send writes one text frame, in order after every earlier one, when the socket is open.
func (s *socket) send(data []byte) {
	if s.open {
		s.out.push(frame{data: data})
	}
}

// terminate cuts the connection without a closing handshake.
func (s *socket) terminate() {
	s.terminated = true
	s.cancel()
	if s.conn != nil {
		s.out.stop()
		_ = s.conn.NetConn().Close()
	}
}

// close says goodbye with code and reason after whatever is still queued, then hangs up.
func (s *socket) close(code int, reason string) {
	if !s.open {
		s.terminate()
		return
	}
	s.terminated = true
	s.cancel()
	s.out.push(frame{close: true, code: code, reason: reason})
}

// heardConn notes when bytes arrive, so a long message still on its way counts as the line being alive.
type heardConn struct {
	net.Conn
	socket *socket
}

func (c *heardConn) Read(buffer []byte) (int, error) {
	read, err := c.Conn.Read(buffer)
	if read > 0 {
		c.socket.heardAt.Store(int64(application.Now()))
	}
	return read, err
}

type frame struct {
	data   []byte
	close  bool
	code   int
	reason string
}

// writer sends frames from a goroutine of its own, so a large one never holds up the loop.
type writer struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	wake    *sync.Cond
	queue   []frame
	stopped bool
}

func newWriter(conn *websocket.Conn) *writer {
	w := &writer{conn: conn}
	w.wake = sync.NewCond(&w.mu)
	go w.run()
	return w
}

func (w *writer) push(f frame) {
	w.mu.Lock()
	if !w.stopped {
		w.queue = append(w.queue, f)
		w.wake.Signal()
	}
	w.mu.Unlock()
}

func (w *writer) stop() {
	w.mu.Lock()
	w.stopped = true
	w.queue = nil
	w.wake.Signal()
	w.mu.Unlock()
}

func (w *writer) run() {
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.stopped {
			w.wake.Wait()
		}
		if w.stopped {
			w.mu.Unlock()
			return
		}
		next := w.queue[0]
		w.queue[0] = frame{}
		w.queue = w.queue[1:]
		w.mu.Unlock()
		if next.close {
			message := websocket.FormatCloseMessage(next.code, next.reason)
			_ = w.conn.WriteControl(websocket.CloseMessage, message, time.Now().Add(time.Second))
			w.stop()
			_ = w.conn.NetConn().Close()
			return
		}
		if err := w.conn.WriteMessage(websocket.TextMessage, next.data); err != nil {
			w.stop()
			_ = w.conn.NetConn().Close()
			return
		}
	}
}
