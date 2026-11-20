package mobile

import (
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
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

func testView(title string) contracts.MobileView {
	var view contracts.MobileView
	text := `{"groups":[{"projectId":null,"name":"` + title + `","threads":[]}],"activity":{"priority":[],"running":[],"threads":[],"snoozed":[]},"theme":{"dark":"aicodingtool-dark","light":"aicodingtool-light","mode":"dark"},"engines":{},"upkeep":[],"dictation":false,"usage":{"engines":[],"composer":null},"thread":null,"draft":null,"error":null}`
	if err := jsonx.Unmarshal([]byte(text), &view); err != nil {
		panic(err)
	}
	return view
}

func until(t *testing.T, check func() bool, message string) {
	t.Helper()
	for range 300 {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}

type harness struct {
	server   *Server
	devices  *PairingStore
	mu       sync.Mutex
	commands []string
	queries  int
	snapshot contracts.MobileView
	refuse   string
	refuseQ  string
	held     chan struct{}
	changes  int
}

func (h *harness) commandsSeen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.commands...)
}

func (h *harness) set(fn func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn()
}

func (h *harness) origin() string { return fmt.Sprintf("http://127.0.0.1:%d", *h.server.Port()) }
func (h *harness) socketURL() string {
	return fmt.Sprintf("ws://127.0.0.1:%d/m/socket", *h.server.Port())
}
func (h *harness) hold() (release func()) {
	gate := make(chan struct{})
	h.set(func() { h.held = gate })
	return func() {
		h.set(func() { h.held = nil })
		close(gate)
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{devices: NewPairingStore(filepath.Join(t.TempDir(), "mobile-devices.v1.json")), snapshot: testView("first")}
	grace := time.Duration(0)
	h.server = NewServer(ServerOptions{
		Devices:        h.devices,
		Port:           0,
		AllowedOrigins: func() []string { return []string{h.origin()} },
		Snapshot: func(context.Context, string) (*contracts.MobileView, error) {
			h.mu.Lock()
			refuse, held, view := h.refuse, h.held, h.snapshot
			h.mu.Unlock()
			if refuse != "" {
				return nil, errors.New(refuse)
			}
			if held != nil {
				<-held
			}
			return &view, nil
		},
		Command: func(_ context.Context, _ string, command contracts.MobileCommand) error {
			text, _ := jsonx.Marshal(command)
			h.set(func() { h.commands = append(h.commands, string(text)) })
			return nil
		},
		Query: func(context.Context, string, contracts.MobileQuery) (jsontext.Value, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.refuseQ != "" {
				return nil, errors.New(h.refuseQ)
			}
			h.queries++
			return jsontext.Value(`{"status":"available","patch":""}`), nil
		},
		OnChange:     func() { h.set(func() { h.changes++ }) },
		SessionGrace: &grace,
	})
	if err := h.server.Start("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.server.Stop)
	return h
}

type message = map[string]any

// testPhone is a socket with everything it has been told, so a test waits for a message rather than a moment.
type testPhone struct {
	t        *testing.T
	conn     *websocket.Conn
	mu       sync.Mutex
	messages []message
	closes   []int
	done     chan struct{}
}

func dial(t *testing.T, url string, header http.Header) (*testPhone, *http.Response, error) {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, response, err
	}
	phone := &testPhone{t: t, conn: conn, done: make(chan struct{})}
	conn.SetCloseHandler(func(code int, _ string) error {
		phone.mu.Lock()
		phone.closes = append(phone.closes, code)
		phone.mu.Unlock()
		return nil
	})
	go func() {
		defer close(phone.done)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				phone.mu.Lock()
				if len(phone.closes) == 0 {
					phone.closes = append(phone.closes, websocket.CloseAbnormalClosure)
				}
				phone.mu.Unlock()
				return
			}
			var decoded message
			_ = jsonx.Unmarshal(data, &decoded)
			phone.mu.Lock()
			phone.messages = append(phone.messages, decoded)
			phone.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return phone, response, nil
}

func newPhone(t *testing.T, url string, header http.Header) *testPhone {
	t.Helper()
	phone, _, err := dial(t, url, header)
	if err != nil {
		t.Fatal(err)
	}
	return phone
}

func (p *testPhone) send(value any) {
	text, err := jsonx.Marshal(value)
	if err != nil {
		p.t.Fatal(err)
	}
	_ = p.conn.WriteMessage(websocket.TextMessage, text)
}

func (p *testPhone) close() { _ = p.conn.Close() }

func (p *testPhone) all(kind string) []message {
	p.mu.Lock()
	defer p.mu.Unlock()
	found := []message{}
	for _, held := range p.messages {
		if held["kind"] == kind {
			found = append(found, held)
		}
	}
	return found
}

func (p *testPhone) closed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.closes) > 0
}

func (p *testPhone) find(match func(message) bool, what string) message {
	p.t.Helper()
	var found message
	until(p.t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, held := range p.messages {
			if match(held) {
				found = held
				return true
			}
		}
		return false
	}, what)
	return found
}

func (p *testPhone) waitFor(kind string) message {
	p.t.Helper()
	return p.find(func(held message) bool { return held["kind"] == kind }, "no "+kind+" message arrived")
}

func pair(code string, name string) message {
	return message{"kind": "pair", "version": contracts.MobileProtocolVersion, "code": code, "deviceName": name}
}

func resume(token string, sessionID any, lastSequence any) message {
	request := message{"kind": "resume", "version": contracts.MobileProtocolVersion, "token": token, "lastSequence": lastSequence}
	if sessionID != nil {
		request["sessionId"] = sessionID
	}
	return request
}

func command(requestID string, body message) message {
	return message{"kind": "command", "requestId": requestID, "command": body}
}

func (h *harness) mint() string { return h.devices.Mint(application.Now()).Code }

func viewJSON(t *testing.T, value any) string {
	t.Helper()
	text, err := jsonx.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var plain any
	_ = jsonx.Unmarshal(text, &plain)
	text, _ = jsonx.Marshal(plain)
	return string(text)
}

func TestTheServerAnswersItsHealthCheckAndNothingElseOverPlainHTTP(t *testing.T) {
	h := newHarness(t)
	response, err := http.Get(h.origin() + "/m/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != MobileHealthResponse {
		t.Fatalf("body %q", body)
	}
	for _, target := range []string{"/", "/m", "/m/", "/m/index.html", "/m/../../package.json"} {
		response, err := http.Get(h.origin() + target)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 404 {
			t.Errorf("%s: %d", target, response.StatusCode)
		}
	}
	response, _ = http.Post(h.origin()+"/m/", "text/plain", nil)
	response.Body.Close()
	if response.StatusCode != 405 {
		t.Errorf("POST: %d", response.StatusCode)
	}
	response, _ = http.Get(h.origin() + "/m/socket")
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Errorf("the socket address is only an upgrade: %d", response.StatusCode)
	}
}

func TestEveryResponseSaysItMayNotBeFramed(t *testing.T) {
	h := newHarness(t)
	for _, target := range []string{"/m/", "/m/health", "/m/socket"} {
		response, err := http.Get(h.origin() + target)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.Header.Get("x-frame-options") != "DENY" || !strings.Contains(response.Header.Get("content-security-policy"), "frame-ancestors 'none'") || response.Header.Get("x-content-type-options") != "nosniff" {
			t.Errorf("%s: %v", target, response.Header)
		}
	}
}

func TestASnapshotNamesThisBuildsVocabularyAndTakesAPushToken(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	opening := client.waitFor("snapshot")
	if viewJSON(t, opening["vocabulary"]) != viewJSON(t, application.MobileVocabulary()) {
		t.Fatal("the snapshot's vocabulary is not this build's")
	}
	vocabulary := application.MobileVocabulary()
	var claude *contracts.MobileEngineTerms
	for _, engine := range vocabulary.Engines {
		if engine.ID == "claude" {
			claude = engine
		}
	}
	if claude == nil || claude.Label != "Claude" {
		t.Fatal("no Claude")
	}
	policies := []string{}
	for _, policy := range claude.Policies {
		policies = append(policies, string(policy.ID))
	}
	if strings.Join(policies, ",") != "autonomous,bypass,allow-edits,confirm" {
		t.Fatalf("policies %v", policies)
	}
	if vocabulary.Efforts[0].ID != "ultra" || vocabulary.Efforts[0].Delegates == nil || !*vocabulary.Efforts[0].Delegates {
		t.Fatal("efforts are not deepest first")
	}
	if opening["push"] != true {
		t.Fatal("the snapshot does not say the computer takes a push token")
	}
	push := message{"token": strings.Repeat("cd", 32), "environment": "production"}
	client.send(message{"kind": "push-token", "push": push})
	until(t, func() bool { return len(h.devices.PushTargets()) == 1 }, "the token was never kept")
	if held := h.devices.PushTargets()[0].Push; held.Token != push["token"] || held.Environment != "production" {
		t.Fatalf("push %+v", held)
	}
	if client.closed() {
		t.Fatal("the phone was hung up on")
	}
}

func TestAPhoneTradesItsCodeForATokenAndIsHandedTheView(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	paired := client.waitFor("paired")
	if paired["sequence"] != 1.0 || len(paired["token"].(string)) != 64 {
		t.Fatalf("paired %v", paired)
	}
	if device := h.devices.Authenticate(paired["token"].(string), application.Now()); device == nil || device.ID != paired["deviceId"] {
		t.Fatal("the token does not name the device")
	}
	snapshot := client.waitFor("snapshot")
	if snapshot["sequence"] != 2.0 || viewJSON(t, snapshot["view"]) != viewJSON(t, testView("first")) {
		t.Fatalf("snapshot %v", snapshot)
	}
	sessions := h.server.SessionViews()
	if len(sessions) != 1 || sessions[0].DeviceName != "iPhone" || sessions[0].Connection != domain.MobileConnectionStateLive {
		t.Fatalf("sessions %+v", sessions)
	}

	client.send(command("request-1", message{"type": "task.select", "taskId": "task-1"}))
	if ack := client.waitFor("ack"); ack["ok"] != true {
		t.Fatalf("ack %v", ack)
	}
	if commands := h.commandsSeen(); len(commands) != 1 || !strings.Contains(commands[0], `"task.select"`) {
		t.Fatalf("commands %v", commands)
	}

	client.send(command("request-2", message{"type": "task.select", "taskId": ""}))
	refused := client.find(func(held message) bool { return held["kind"] == "ack" && held["requestId"] == "request-2" }, "a refusal for the command the guard turned away")
	if refused["ok"] != false {
		t.Fatal("a command that fails the guard is refused rather than hung up on")
	}
	if len(h.commandsSeen()) != 1 {
		t.Fatal("a refused command reached the window")
	}
	if client.closed() {
		t.Fatal("the phone was hung up on")
	}
}

func patchUpdate(t *testing.T, patch string) contracts.MobileViewUpdate {
	t.Helper()
	var update contracts.MobileViewUpdatePatch
	if err := jsonx.Unmarshal([]byte(`{"kind":"patch","patch":`+patch+`}`), &update); err != nil {
		t.Fatal(err)
	}
	return update
}

func offline(h *harness) bool {
	sessions := h.server.SessionViews()
	return len(sessions) > 0 && sessions[0].Connection == domain.MobileConnectionStateOffline
}

func TestAPhoneThatDropsResumesWhereItWas(t *testing.T) {
	h := newHarness(t)
	first := newPhone(t, h.socketURL(), nil)
	first.send(pair(h.mint(), "iPhone"))
	paired := first.waitFor("paired")
	opening := first.waitFor("snapshot")
	sessionID := opening["sessionId"]

	first.close()
	until(t, func() bool { return offline(h) }, "the session never went offline")
	h.server.Publish(patchUpdate(t, `{"groups":[{"projectId":null,"name":"while away","threads":[]}]}`))

	back := newPhone(t, h.socketURL(), nil)
	back.send(resume(paired["token"].(string), sessionID, opening["sequence"]))
	missed := back.waitFor("patch")
	if missed["sequence"] != opening["sequence"].(float64)+1 {
		t.Fatalf("missed %v", missed)
	}
	if missed["patch"].(message)["groups"].([]any)[0].(message)["name"] != "while away" {
		t.Fatalf("patch %v", missed)
	}
	if len(back.all("snapshot")) != 0 || len(h.server.SessionViews()) != 1 {
		t.Fatal("a resume that worked costs no snapshot and keeps the session")
	}

	back.close()
	until(t, func() bool { return offline(h) }, "the session never went offline")
	h.set(func() { h.snapshot = testView("second") })
	for range domain.MobileEventBuffer + 1 {
		h.server.Publish(patchUpdate(t, `{"groups":[]}`))
	}
	behind := newPhone(t, h.socketURL(), nil)
	behind.send(resume(paired["token"].(string), sessionID, missed["sequence"]))
	snapshot := behind.waitFor("snapshot")
	want := testView("first")
	want.Groups = []*contracts.MobileProjectGroup{}
	if viewJSON(t, snapshot["view"]) != viewJSON(t, want) {
		t.Fatalf("the view the published patches built, which nothing published can fall outside of: %v", snapshot["view"])
	}
	if snapshot["sessionId"] == sessionID {
		t.Fatal("a phone too far behind is given a session of its own")
	}
}

func TestASocketIsRefusedWithoutATokenOrAnOriginItServed(t *testing.T) {
	h := newHarness(t)
	stranger := newPhone(t, h.socketURL(), nil)
	stranger.send(resume(strings.Repeat("0", 64), nil, 0))
	if refused := stranger.waitFor("error"); refused["code"] != "unauthorized" || refused["sequence"] != 0.0 {
		t.Fatalf("refused %v", refused)
	}
	until(t, stranger.closed, "the refused socket stayed open")
	if len(h.server.SessionViews()) != 0 {
		t.Fatal("a stranger got a session")
	}

	outdated := newPhone(t, h.socketURL(), nil)
	old := pair(h.mint(), "iPhone")
	old["version"] = contracts.OldestPhoneProtocolVersion - 1
	outdated.send(old)
	if tooOld := outdated.waitFor("error"); tooOld["code"] != "version" || !strings.Contains(tooOld["message"].(string), "Update the app") {
		t.Fatalf("too old %v", tooOld)
	}

	ahead := newPhone(t, h.socketURL(), nil)
	future := pair(h.mint(), "iPhone")
	future["version"], future["oldestComputer"] = contracts.MobileProtocolVersion+2, contracts.MobileProtocolVersion+1
	ahead.send(future)
	if tooNew := ahead.waitFor("error"); tooNew["code"] != "version" || !strings.Contains(tooNew["message"].(string), "Update it on your computer") {
		t.Fatalf("too new %v", tooNew)
	}

	newer := newPhone(t, h.socketURL(), nil)
	readable := pair(h.mint(), "iPhone")
	readable["version"], readable["oldestComputer"] = contracts.MobileProtocolVersion+1, contracts.MobileProtocolVersion
	newer.send(readable)
	newer.waitFor("paired")

	newPhone(t, h.socketURL(), http.Header{"Origin": {h.origin()}})
	_, response, err := dial(t, h.socketURL(), http.Header{"Origin": {"https://evil.example"}})
	if err == nil || response == nil || response.StatusCode != 403 {
		t.Fatalf("a foreign origin was let in: %v", err)
	}
}

func TestACommandResentAfterALostAckIsAnsweredNotRunAgain(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	client.waitFor("snapshot")
	send := command("request-1", message{"type": "task.send", "taskId": "task-1", "text": "ship it"})
	client.send(send)
	if first := client.waitFor("ack"); first["ok"] != true {
		t.Fatal(first)
	}
	client.send(send)
	until(t, func() bool { return len(client.all("ack")) == 2 }, "the resend went unanswered")
	if len(h.commandsSeen()) != 1 {
		t.Fatal("the window was asked twice")
	}
}

func TestAnEditsOutcomeOutlivesTheServer(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	paired := client.waitFor("paired")
	client.waitFor("snapshot")
	edit := command("edit-1", message{"type": "task.edit-queued", "taskId": "task-1", "messageId": "q1"})
	client.send(edit)
	client.waitFor("ack")
	client.close()

	h.server.Stop()
	if err := h.server.Start("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	again := newPhone(t, h.socketURL(), http.Header{"Origin": {h.origin()}})
	again.send(resume(paired["token"].(string), nil, 0))
	again.waitFor("snapshot")
	again.send(edit)
	if ack := again.waitFor("ack"); ack["ok"] != true {
		t.Fatal(ack)
	}
	if len(h.commandsSeen()) != 1 {
		t.Fatal("the window was asked twice")
	}
}

func TestAPhoneWhosePairingIsRevokedLosesTheSession(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	paired := client.waitFor("paired")
	client.waitFor("snapshot")
	h.devices.Revoke(paired["deviceId"].(string))
	h.server.DropDevice(paired["deviceId"].(string))
	until(t, client.closed, "the revoked phone was left connected")
	if len(h.server.SessionViews()) != 0 {
		t.Fatal("the session outlived the device")
	}
}

func TestAViewTheWindowWillNotGiveUpIsReported(t *testing.T) {
	h := newHarness(t)
	h.set(func() { h.refuse = "The AI Coding Tool window is not open." })
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	if failed := client.waitFor("error"); failed["code"] != "internal" || !strings.Contains(failed["message"].(string), "window is not open") {
		t.Fatal(failed)
	}
	until(t, client.closed, "the phone was left on a session with no view")
	if len(h.server.SessionViews()) != 0 {
		t.Fatal("a session with no view was kept")
	}
}

func TestACommandWhoseAckWasLostBeforeTheSessionExpiredRunsOnce(t *testing.T) {
	h := newHarness(t)
	first := newPhone(t, h.socketURL(), nil)
	first.send(pair(h.mint(), "iPhone"))
	paired := first.waitFor("paired")
	opening := first.waitFor("snapshot")
	send := command("request-1", message{"type": "task.send", "taskId": "task-1", "text": "ship it"})
	first.send(send)
	first.waitFor("ack")

	first.close()
	until(t, func() bool { return offline(h) }, "the session never went offline")
	h.server.tick()
	if len(h.server.SessionViews()) != 0 {
		t.Fatal("the session outlived its grace")
	}
	back := newPhone(t, h.socketURL(), nil)
	back.send(resume(paired["token"].(string), opening["sessionId"], opening["sequence"].(float64)+1))
	back.waitFor("snapshot")
	back.send(send)
	if again := back.waitFor("ack"); again["ok"] != true {
		t.Fatal(again)
	}
	if len(h.commandsSeen()) != 1 {
		t.Fatal("the window ran the resent command a second time")
	}
}

func TestAResumeWithNothingToReplayIsStillHandedAFrame(t *testing.T) {
	h := newHarness(t)
	first := newPhone(t, h.socketURL(), nil)
	first.send(pair(h.mint(), "iPhone"))
	paired := first.waitFor("paired")
	opening := first.waitFor("snapshot")
	first.close()

	back := newPhone(t, h.socketURL(), nil)
	back.send(resume(paired["token"].(string), opening["sessionId"], opening["sequence"]))
	if ping := back.waitFor("ping"); ping["sequence"] != opening["sequence"].(float64)+1 {
		t.Fatalf("ping %v", ping)
	}
	if len(back.all("snapshot")) != 0 {
		t.Fatal("a resume with nothing to replay cost a snapshot")
	}
}

func TestAPageServedThroughAProxyIsLetInByItsHost(t *testing.T) {
	h := newHarness(t)
	proxied := func(origin, forwardedFor string) http.Header {
		return http.Header{"Origin": {origin}, "X-Forwarded-Host": {"mac.tail1234.ts.net"}, "X-Forwarded-Proto": {"https"}, "X-Forwarded-For": {forwardedFor}}
	}
	newPhone(t, h.socketURL(), proxied("https://mac.tail1234.ts.net", "100.64.0.7"))
	if _, response, err := dial(t, h.socketURL(), proxied("https://evil.example", "100.64.0.7")); err == nil || response.StatusCode != 403 {
		t.Fatal("a foreign origin behind the proxy was let in")
	}

	h.mint()
	for range 5 {
		guess := newPhone(t, h.socketURL(), proxied("https://mac.tail1234.ts.net", "100.64.0.7"))
		guess.send(pair("WRONGONE", "iPhone"))
		guess.waitFor("error")
	}
	other := newPhone(t, h.socketURL(), proxied("https://mac.tail1234.ts.net", "100.64.0.8"))
	other.send(pair(h.mint(), "iPad"))
	other.waitFor("paired")
}

func TestAPhonesRequestIsAnsweredByTheWindowOrByTheWaitRunningOut(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	sent := []contracts.MobileRequest{}
	record := func(request contracts.MobileRequest) bool {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, request)
		return true
	}
	nth := func(index int) contracts.MobileRequest {
		var request contracts.MobileRequest
		until(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			if len(sent) > index {
				request = sent[index]
				return true
			}
			return false
		}, "the request never reached the window")
		return request
	}
	relay := NewRelayWithPatience(record, 40*time.Millisecond, MobileTranscribeTimeout, MobileCommandsTimeout)
	viewText, _ := jsonx.Marshal(testView("first"))

	answered := make(chan *contracts.MobileView, 1)
	go func() {
		view, _ := relay.Snapshot(ctx, "session-1")
		answered <- view
	}()
	first := nth(0).(contracts.MobileRequestSnapshot)
	relay.Answer(contracts.MobileResponseTrue{RequestID: first.RequestID, Result: viewText})
	if view := <-answered; view == nil || viewJSON(t, view) != viewJSON(t, testView("first")) {
		t.Fatal("the window's view was not handed back")
	}
	relay.Answer(contracts.MobileResponseTrue{RequestID: first.RequestID, Result: viewText})

	before := application.Now()
	refused := make(chan error, 1)
	go func() { refused <- relay.Command(ctx, "session-1", contracts.TaskCommandTaskSelect{TaskID: "task-1"}) }()
	second := nth(1).(contracts.MobileRequestCommand)
	if second.AnswerBy == nil || *second.AnswerBy < before+40 {
		t.Fatal("the window is told when the relay stops waiting")
	}
	relay.Answer(contracts.MobileResponseFalse{RequestID: second.RequestID, Message: "No such thread."})
	if err := <-refused; err == nil || err.Error() != "No such thread." {
		t.Fatal(err)
	}

	if _, err := relay.Snapshot(ctx, "session-1"); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatal(err)
	}
	closed := NewRelay(func(contracts.MobileRequest) bool { return false })
	if _, err := closed.Snapshot(ctx, "session-1"); err == nil || !strings.Contains(err.Error(), "window is not open") {
		t.Fatal(err)
	}

	slow := NewRelayWithPatience(record, 40*time.Millisecond, 400*time.Millisecond, 40*time.Millisecond)
	heard := make(chan jsontext.Value, 1)
	go func() {
		result, _ := slow.Query(ctx, "session-1", contracts.MobileQueryTranscribe{Audio: "UklGRg=="})
		heard <- result
	}()
	transcribing := nth(3).(contracts.MobileRequestQuery)
	time.Sleep(120 * time.Millisecond)
	slow.Answer(contracts.MobileResponseTrue{RequestID: transcribing.RequestID, Result: jsontext.Value(`{"text":"words"}`)})
	if result := <-heard; string(result) != `{"text":"words"}` {
		t.Fatalf("a recording waits longer than any other request: %s", result)
	}
	if _, err := slow.Query(ctx, "session-1", contracts.ComputerQueryDirectories{Prefix: "/"}); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatal(err)
	}
	discovering := NewRelayWithPatience(record, 40*time.Millisecond, 40*time.Millisecond, 400*time.Millisecond)
	listed := make(chan jsontext.Value, 1)
	go func() {
		taskID := "t1"
		result, _ := discovering.Query(ctx, "session-1", contracts.MobileQueryCommands{TaskID: &taskID})
		listed <- result
	}()
	commands := nth(5).(contracts.MobileRequestQuery)
	time.Sleep(120 * time.Millisecond)
	discovering.Answer(contracts.MobileResponseTrue{RequestID: commands.RequestID, Result: jsontext.Value(`{"status":"available","commands":[]}`)})
	if result := <-listed; string(result) != `{"status":"available","commands":[]}` {
		t.Fatalf("a command list waits out an engine starting to list its skills: %s", result)
	}

	abandoned := NewRelay(func(contracts.MobileRequest) bool { return true })
	waiting := make(chan error, 1)
	go func() {
		_, err := abandoned.Snapshot(ctx, "session-1")
		waiting <- err
	}()
	until(t, func() bool {
		abandoned.mu.Lock()
		defer abandoned.mu.Unlock()
		return len(abandoned.pending) == 1
	}, "the request never waited")
	abandoned.FailAll("The phone bridge was turned off.")
	if err := <-waiting; err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatal(err)
	}
}

// rudePhone speaks the protocol but never answers a close frame, which is what a patched client would do.
type rudePhone struct {
	conn net.Conn
	gone chan struct{}
}

func newRudePhone(t *testing.T, port int) *rudePhone {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, _ = io.WriteString(conn, strings.Join([]string{
		"GET /m/socket HTTP/1.1",
		fmt.Sprintf("Host: 127.0.0.1:%d", port),
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Key: AAAAAAAAAAAAAAAAAAAAAA==",
		"Sec-WebSocket-Version: 13",
		"", "",
	}, "\r\n"))
	buffer := make([]byte, 1024)
	if _, err := conn.Read(buffer); err != nil {
		t.Fatal(err)
	}
	rude := &rudePhone{conn: conn, gone: make(chan struct{})}
	go func() {
		defer close(rude.gone)
		_, _ = io.Copy(io.Discard, conn)
	}()
	return rude
}

// send writes one masked text frame, the only shape of frame these tests send.
func (r *rudePhone) send(value any) {
	body, _ := jsonx.Marshal(value)
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	frame := []byte{0x81}
	if len(body) < 126 {
		frame = append(frame, 0x80|byte(len(body)))
	} else {
		frame = append(frame, 0x80|126, byte(len(body)>>8), byte(len(body)))
	}
	frame = append(frame, mask[:]...)
	for index, b := range body {
		frame = append(frame, b^mask[index%4])
	}
	_, _ = r.conn.Write(frame)
}

func (r *rudePhone) destroyed() bool {
	select {
	case <-r.gone:
		return true
	default:
		return false
	}
}

func TestARevokedPhoneThatWillNotHangUpIsCutOff(t *testing.T) {
	h := newHarness(t)
	rude := newRudePhone(t, *h.server.Port())
	rude.send(pair(h.mint(), "Stolen"))
	until(t, func() bool { return len(h.server.SessionViews()) == 1 }, "the rude phone never paired")
	device := h.devices.List()[0]

	rude.send(command("before", message{"type": "task.select", "taskId": "task-1"}))
	until(t, func() bool { return len(h.commandsSeen()) == 1 }, "the paired phone's command never arrived")

	h.devices.Revoke(device.ID)
	h.server.DropDevice(device.ID)
	rude.send(command("after", message{"type": "task.send", "taskId": "task-1", "text": "rm -rf ~"}))
	time.Sleep(200 * time.Millisecond)
	if len(h.commandsSeen()) != 1 || len(h.server.SessionViews()) != 0 {
		t.Fatal("a revoked phone reached the window again")
	}
	until(t, rude.destroyed, "the revoked socket was left alive")
}

func TestTurningTheBridgeOffFinishesWhileAPhoneRefusesToHangUp(t *testing.T) {
	h := newHarness(t)
	rude := newRudePhone(t, *h.server.Port())
	rude.send(pair(h.mint(), "Stolen"))
	until(t, func() bool { return len(h.server.SessionViews()) == 1 }, "the rude phone never paired")
	started := time.Now()
	h.server.Stop()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("stop took %v", elapsed)
	}
	if h.server.Port() != nil {
		t.Fatal("the port is still held")
	}
	until(t, rude.destroyed, "the socket outlived the server")
}

func TestACommandResentAfterFallingPastTheBufferRunsOnce(t *testing.T) {
	h := newHarness(t)
	first := newPhone(t, h.socketURL(), nil)
	first.send(pair(h.mint(), "iPhone"))
	paired := first.waitFor("paired")
	opening := first.waitFor("snapshot")
	send := command("request-1", message{"type": "task.send", "taskId": "task-1", "text": "deploy to prod"})
	first.send(send)
	until(t, func() bool { return len(h.commandsSeen()) == 1 }, "the command never reached the window")

	first.close()
	until(t, func() bool { return offline(h) }, "the session never went offline")
	for range domain.MobileEventBuffer + 1 {
		h.server.Publish(patchUpdate(t, `{"groups":[]}`))
	}
	behind := newPhone(t, h.socketURL(), nil)
	behind.send(resume(paired["token"].(string), opening["sessionId"], opening["sequence"]))
	if fresh := behind.waitFor("snapshot"); fresh["sessionId"] == opening["sessionId"] {
		t.Fatal("the phone fell too far behind to resume")
	}
	behind.send(send)
	behind.waitFor("ack")
	if len(h.commandsSeen()) != 1 {
		t.Fatal("the window was asked twice across two sessions")
	}
}

func TestWhatAPhoneMissedBeforeItsFirstSnapshotIsNotSentOutOfOrder(t *testing.T) {
	h := newHarness(t)
	release := h.hold()
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	client.waitFor("paired")

	h.server.Publish(patchUpdate(t, `{"groups":[{"projectId":null,"name":"too early","threads":[]}]}`))
	time.Sleep(50 * time.Millisecond)
	if len(client.all("patch")) != 0 {
		t.Fatal("a patch outran the snapshot it belongs after")
	}
	release()
	snapshot := client.waitFor("snapshot")
	h.server.Publish(patchUpdate(t, `{"groups":[{"projectId":null,"name":"in order","threads":[]}]}`))
	if patch := client.waitFor("patch"); patch["sequence"] != snapshot["sequence"].(float64)+1 {
		t.Fatalf("patch %v after snapshot %v", patch["sequence"], snapshot["sequence"])
	}
}

func TestSocketsThatNeverSayWhoTheyAreCannotPileUp(t *testing.T) {
	h := newHarness(t)
	for range domain.MaxPendingSockets {
		newPhone(t, h.socketURL(), nil)
	}
	until(t, func() bool {
		h.server.lock()
		defer h.server.unlock()
		return h.server.pending == domain.MaxPendingSockets
	}, "the sockets were never counted")
	if _, response, err := dial(t, h.socketURL(), nil); err == nil || response.StatusCode != 503 {
		t.Fatal("one socket too many was let in")
	}
}

func TestAReadIsAnsweredRefusedAndNeverRemembered(t *testing.T) {
	h := newHarness(t)
	client := newPhone(t, h.socketURL(), nil)
	client.send(pair(h.mint(), "iPhone"))
	client.waitFor("snapshot")

	query := message{"kind": "diff-patch", "taskId": "task-1", "range": message{"kind": "uncommitted"}, "path": "a.ts"}
	client.send(message{"kind": "query", "requestId": "q1", "query": query})
	answer := client.waitFor("answer")
	if viewJSON(t, answer) != viewJSON(t, message{"kind": "answer", "sequence": answer["sequence"], "requestId": "q1", "ok": true, "result": message{"status": "available", "patch": ""}}) {
		t.Fatalf("answer %v", answer)
	}
	client.send(message{"kind": "query", "requestId": "q1", "query": query})
	until(t, func() bool { return len(client.all("answer")) == 2 }, "the same read asked again is read again")

	h.set(func() { h.refuseQ = "The checkout is gone." })
	client.send(message{"kind": "query", "requestId": "q2", "query": query})
	refused := client.find(func(held message) bool { return held["kind"] == "answer" && held["requestId"] == "q2" }, "a refusal")
	if viewJSON(t, refused) != viewJSON(t, message{"kind": "answer", "sequence": refused["sequence"], "requestId": "q2", "ok": false, "message": "The checkout is gone."}) {
		t.Fatalf("refused %v", refused)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.queries != 2 {
		t.Fatalf("queries %d", h.queries)
	}
}
