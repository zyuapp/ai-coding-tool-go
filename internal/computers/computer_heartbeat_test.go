package computers

import (
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// fakeHost is a bare socket server the test speaks for.
type fakeHost struct {
	server *httptest.Server
	mu     sync.Mutex
	peer   *websocket.Conn
	writes sync.Mutex
	// connected is told every new socket; received every message it sends.
	connected func(*fakeHost)
	received  func(*fakeHost, map[string]any)
}

func newFakeHost(t *testing.T, connected func(*fakeHost), received func(*fakeHost, map[string]any)) *fakeHost {
	t.Helper()
	host := &fakeHost{connected: connected, received: received}
	upgrader := websocket.Upgrader{}
	host.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		host.mu.Lock()
		host.peer = conn
		host.mu.Unlock()
		if host.connected != nil {
			host.connected(host)
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var message map[string]any
			if jsonx.Unmarshal(data, &message) == nil && host.received != nil {
				host.received(host, message)
			}
		}
	}))
	t.Cleanup(func() {
		host.terminate()
		host.server.Close()
	})
	return host
}

func (h *fakeHost) url() string { return "ws" + strings.TrimPrefix(h.server.URL, "http") }

func (h *fakeHost) send(message any) {
	data, err := application.StringifyWorkspaceJSON(message)
	if err != nil {
		panic(err)
	}
	h.mu.Lock()
	peer := h.peer
	h.mu.Unlock()
	h.writes.Lock()
	defer h.writes.Unlock()
	_ = peer.WriteMessage(websocket.TextMessage, data)
}

func (h *fakeHost) raw() net.Conn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.peer.NetConn()
}

func (h *fakeHost) terminate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.peer != nil {
		_ = h.peer.NetConn().Close()
	}
}

func shorten(t *testing.T, silent time.Duration) {
	t.Helper()
	held := silentAfter
	silentAfter = silent
	t.Cleanup(func() { silentAfter = held })
}

func TestClientHeartbeatCountsOnlyWhatArrives(t *testing.T) {
	for _, mode := range []string{"idle", "send", "query", "message-image", "trickle"} {
		t.Run(mode, func(t *testing.T) {
			shorten(t, 400*time.Millisecond)
			var mu sync.Mutex
			requests := 0
			host := newFakeHost(t, nil, func(h *fakeHost, message map[string]any) {
				switch message["kind"] {
				case "resume":
					h.send(contracts.ComputerServerMessageWorkspace{Sequence: 1, Update: contracts.ComputerWorkspaceUpdate{Revision: 0, State: workspaceJSON(t, application.EmptyWorkspaceState(nil))}})
				case "input", "query":
					mu.Lock()
					requests++
					mu.Unlock()
				}
			})
			client := NewComputerClient(ClientOptions{Host: "127.0.0.1", URL: host.url(), Credential: ComputerCredential{Token: "test"}, DeviceName: "Mac"})
			t.Cleanup(client.Stop)
			until(t, "the connection", func() bool { return client.Status() == domain.ComputerStatusConnected })
			switch mode {
			case "trickle":
				// A long answer still arriving: one frame's header, then its body a few bytes at a time.
				raw := host.raw()
				body := []byte(`{"kind":"ping","at":0,"sequence":2,"pad":"` + strings.Repeat("x", 200) + `"}`)
				_, _ = raw.Write([]byte{0x81, 126, byte(len(body) >> 8), byte(len(body))})
				for offset := 0; offset < len(body); offset += 50 {
					time.Sleep(300 * time.Millisecond)
					_, _ = raw.Write(body[offset:min(offset+50, len(body))])
					time.Sleep(20 * time.Millisecond)
					if client.Status() != domain.ComputerStatusConnected {
						t.Fatal("bytes on their way keep the line alive")
					}
				}
			case "send":
				go func() {
					_, _ = client.Send(background(), []application.WorkspaceInput{contracts.TaskCommandAttachmentsSend{Attachments: dataAttachments(1, "AQID")}})
				}()
			case "query":
				go func() { _, _ = client.Query(background(), contracts.ComputerQueryAttachment{Name: "shot.png"}) }()
			case "message-image":
				go func() {
					_, _ = client.Query(background(), contracts.ComputerQueryMessageImage{Path: "/tmp/shot.png", Root: "", Message: "reply"})
				}()
			}
			if mode == "send" || mode == "query" || mode == "message-image" {
				until(t, "the request", func() bool { mu.Lock(); defer mu.Unlock(); return requests == 1 })
			}
			until(t, "the silent line counted as gone", func() bool { return client.Status() == domain.ComputerStatusOffline })
		})
	}
}

func workspaceJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := application.StringifyWorkspaceJSON(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientReadsOldStateAndUsesAdvertisedSupport(t *testing.T) {
	for _, advertise := range []bool{false, true} {
		t.Run(map[bool]string{false: "silent", true: "advertised"}[advertise], func(t *testing.T) {
			all := contracts.ComputerCapabilitiesComputerCapabilities()
			limited := slices.DeleteFunc(slices.Clone(all), func(name string) bool {
				return strings.HasPrefix(name, "command:task.snooze") || strings.HasPrefix(name, "query:directories")
			})
			var mu sync.Mutex
			var received []string
			sequence, generation := 0.0, 0.0
			next := func() float64 { sequence++; return sequence }
			host := newFakeHost(t, nil, func(h *fakeHost, message map[string]any) {
				mu.Lock()
				defer mu.Unlock()
				switch message["kind"] {
				case "pair", "resume":
					generation++
					if advertise {
						capabilities := all
						if generation == 1 {
							capabilities = append(slices.Clone(limited), "future:capability")
						}
						h.send(contracts.ComputerServerMessageCapabilities{Sequence: next(), Capabilities: capabilities})
					}
					old := `{"threads":[{"id":"old","title":"old","engine":"claude","executionPolicy":"confirm","messages":[],"continuationStatus":"none","lastChangeSnapshot":{"files":[],"capturedAt":1},"updatedAt":1}],"currentId":"old","projects":[]}`
					h.send(contracts.ComputerServerMessageWorkspace{Sequence: next(), SessionID: new("session"), Update: contracts.ComputerWorkspaceUpdate{Revision: generation, State: []byte(old)}})
				case "input":
					inputs := message["inputs"].([]any)
					received = append(received, inputs[0].(map[string]any)["type"].(string))
					h.send(contracts.ComputerServerMessageResult{Sequence: next(), RequestID: message["requestId"].(string), Result: contracts.ComputerServerMessageResultResultTrue{Revision: generation}})
				}
			})
			mac := record(ClientOptions{URL: host.url(), Credential: ComputerCredential{Token: "legacy"}})
			t.Cleanup(mac.client.Stop)
			until(t, "the older snapshot", func() bool { return mac.lastState() != nil })
			if first := mac.lastState(); first.SideChats == nil || len(first.SideChats) != 0 {
				t.Fatalf("side chats %#v", first.SideChats)
			}
			snooze := contracts.TaskCommandTaskSnooze{TaskID: "old", Hours: 1}
			if advertise {
				if _, err := mac.client.Send(background(), []application.WorkspaceInput{snooze}); err == nil || !strings.Contains(err.Error(), "unavailable") {
					t.Fatal(err)
				}
				if _, err := mac.client.Query(background(), contracts.ComputerQueryDirectories{Prefix: "/"}); err == nil || !strings.Contains(err.Error(), "unavailable") {
					t.Fatal(err)
				}
				mu.Lock()
				if len(received) != 0 {
					t.Fatal(received)
				}
				mu.Unlock()
			} else {
				if _, err := mac.client.Send(background(), []application.WorkspaceInput{snooze}); err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				if !slices.Equal(received, []string{"task.snooze"}) {
					t.Fatal(received)
				}
				mu.Unlock()
			}
			if _, err := mac.client.Send(background(), []application.WorkspaceInput{rename("old", "works")}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			patch := contracts.ComputerServerMessageWorkspace{Sequence: next(), Update: contracts.ComputerWorkspaceUpdate{Revision: 2, Patches: []*contracts.WorkspacePatch{}}}
			mu.Unlock()
			var patches []*contracts.WorkspacePatch
			if err := jsonx.Unmarshal([]byte(`[{"path":["sideChats"],"value":[]},{"path":["composerFocus"],"value":3}]`), &patches); err != nil {
				t.Fatal(err)
			}
			patch.Update.Patches = patches
			host.send(patch)
			until(t, "patching the original replica", func() bool { return mac.lastState().ComposerFocus == 3 })
			host.terminate()
			until(t, "reconnecting", func() bool {
				mu.Lock()
				defer mu.Unlock()
				return generation == 2 && mac.stateCount() >= 3
			})
			if _, err := mac.client.Send(background(), []application.WorkspaceInput{snooze}); err != nil {
				t.Fatal(err)
			}
			if advertise {
				mac.mu.Lock()
				last := mac.caps[len(mac.caps)-1]
				mac.mu.Unlock()
				if !slices.Equal(last, all) {
					t.Fatal("the capabilities were not refreshed")
				}
			}
		})
	}
}
