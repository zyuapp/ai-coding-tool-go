package mobile

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

func TestTheAddressesAreTheLoopbackBindAndTheTailnetNameWhileServed(t *testing.T) {
	quiet := ReachableAddresses(7737, nil)
	if viewJSON(t, quiet) != `[{"host":"127.0.0.1","kind":"loopback","port":7737}]` {
		t.Fatalf("quiet %s", viewJSON(t, quiet))
	}
	if origins := AllowedOrigins(quiet); !slices.Equal(origins, []string{"http://127.0.0.1:7737", "http://localhost:7737"}) {
		t.Fatalf("origins %v", origins)
	}
	name := "mac.tail1234.ts.net"
	served := ReachableAddresses(7737, &name)
	if viewJSON(t, served) != `[{"host":"mac.tail1234.ts.net","kind":"tailscale-https","port":443},{"host":"127.0.0.1","kind":"loopback","port":7737}]` {
		t.Fatalf("served %s", viewJSON(t, served))
	}
	if !slices.Contains(AllowedOrigins(served), "https://mac.tail1234.ts.net") {
		t.Fatal("the tailnet name is served without a port")
	}
}

// hooksFunc answers for Tailscale in a test.
type hooksFunc struct {
	read  func(ctx context.Context, port *int, knownName *string) domain.TailscaleState
	start func(ctx context.Context, port int) TailscaleAction
	stop  func(ctx context.Context, port int) TailscaleAction
}

func (f hooksFunc) Read(ctx context.Context, port *int, knownName *string) domain.TailscaleState {
	return f.read(ctx, port, knownName)
}
func (f hooksFunc) Start(ctx context.Context, port int) TailscaleAction { return f.start(ctx, port) }
func (f hooksFunc) Stop(ctx context.Context, port int) TailscaleAction  { return f.stop(ctx, port) }

// swappable lets a test change how Tailscale answers while the host runs.
type swappable struct {
	mu    sync.Mutex
	hooks TailscaleHooks
}

func (s *swappable) set(hooks TailscaleHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = hooks
}

func (s *swappable) current() TailscaleHooks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hooks
}

func (s *swappable) Read(ctx context.Context, port *int, knownName *string) domain.TailscaleState {
	return s.current().Read(ctx, port, knownName)
}
func (s *swappable) Start(ctx context.Context, port int) TailscaleAction {
	return s.current().Start(ctx, port)
}
func (s *swappable) Stop(ctx context.Context, port int) TailscaleAction {
	return s.current().Stop(ctx, port)
}

// noTailscale is a machine with no Tailscale, so these tests say the same thing on every machine.
func noTailscale() hooksFunc {
	return hooksFunc{
		read: func(context.Context, *int, *string) domain.TailscaleState {
			return domain.TailscaleState{Status: domain.TailscaleStateStatusMissing}
		},
		start: func(context.Context, int) TailscaleAction {
			return TailscaleAction{Message: "no Tailscale in this test"}
		},
		stop: func(context.Context, int) TailscaleAction { return TailscaleAction{OK: true} },
	}
}

// readyTailscale is a tailnet that is signed in and issues certificates, serving whatever port it was last asked to.
type readyTailscale struct {
	mu     sync.Mutex
	starts []int
	stops  []int
	served *int
}

func (r *readyTailscale) hooks() hooksFunc {
	name := "mac.tail.test"
	return hooksFunc{
		read: func(_ context.Context, port *int, _ *string) domain.TailscaleState {
			r.mu.Lock()
			defer r.mu.Unlock()
			return domain.TailscaleState{Status: domain.TailscaleStateStatusReady, MagicDnsName: &name, Certs: true, Serving: port != nil && r.served != nil && *r.served == *port}
		},
		start: func(_ context.Context, port int) TailscaleAction {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.starts = append(r.starts, port)
			r.served = &port
			return TailscaleAction{OK: true}
		},
		stop: func(_ context.Context, port int) TailscaleAction {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.stops = append(r.stops, port)
			if r.served != nil && *r.served == port {
				r.served = nil
			}
			return TailscaleAction{OK: true}
		},
	}
}

func (r *readyTailscale) startCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.starts)
}

// fakeClock runs a host's timers only when a test moves time on.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Duration
	f       func()
	stopped bool
}

func (c *fakeClock) after(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now + d, f: f}
	c.timers = append(c.timers, timer)
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		timer.stopped = true
	}
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	target := c.now + d
	for {
		var next *fakeTimer
		for _, timer := range c.timers {
			if !timer.stopped && timer.at <= target && (next == nil || timer.at < next.at) {
				next = timer
			}
		}
		if next == nil {
			break
		}
		next.stopped = true
		c.now = next.at
		c.mu.Unlock()
		next.f()
		c.mu.Lock()
	}
	c.now = target
	c.mu.Unlock()
}

type hostHarness struct {
	host      *Host
	folder    string
	options   *HostOptions
	tailscale *swappable
	mu        sync.Mutex
	requests  []contracts.MobileRequest
}

func (h *hostHarness) snapshotAsked() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, request := range h.requests {
		if _, ok := request.(contracts.MobileRequestSnapshot); ok {
			return true
		}
	}
	return false
}

func newHostHarness(t *testing.T, clock *fakeClock) *hostHarness {
	t.Helper()
	h := &hostHarness{host: NewHost(), folder: t.TempDir(), tailscale: &swappable{hooks: noTailscale()}}
	if clock != nil {
		h.host.after = clock.after
	}
	port := 0
	h.options = &HostOptions{
		UserData:  h.folder,
		Tailscale: h.tailscale,
		Port:      &port,
		Send: func(request contracts.MobileRequest) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.requests = append(h.requests, request)
			return true
		},
		OnState: func(domain.MobileServerState) {},
	}
	h.host.Start(h.options)
	t.Cleanup(h.host.Stop)
	return h
}

func (h *hostHarness) savedEnabled(t *testing.T) bool {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(h.folder, "mobile.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := jsonx.Unmarshal(text, &saved); err != nil {
		t.Fatal(err)
	}
	return saved["enabled"] == true
}

func healthOf(port float64) (*http.Response, error) {
	return http.Get(fmt.Sprintf("http://127.0.0.1:%d/m/health", int(port)))
}

func enable(t *testing.T, host *Host, enabled bool) domain.MobileServerState {
	t.Helper()
	state, err := host.SetEnabled(enabled)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestTheBridgeStartsOffComesUpOnTheLoopbackAndGoesBackDown(t *testing.T) {
	h := newHostHarness(t, nil)
	if state := h.host.State(); state.Enabled || state.Port != nil {
		t.Fatalf("state %+v", state)
	}
	on := enable(t, h.host, true)
	if !on.Enabled || on.Status != domain.MobileServerStatusListening || on.Primary == nil || on.Primary.Kind != domain.MobileAddressKindLoopback || on.Primary.Host != "127.0.0.1" || on.Primary.Port != *on.Port {
		t.Fatalf("on %+v", on)
	}
	if !h.savedEnabled(t) {
		t.Fatal("the switch was not saved")
	}
	response, err := healthOf(*on.Port)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("health %v", err)
	}
	response.Body.Close()

	started := time.Now()
	off := enable(t, h.host, false)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("turning it off took %v", elapsed)
	}
	if off.Status != domain.MobileServerStatusOff || off.Port != nil {
		t.Fatalf("off %+v", off)
	}
	if _, err := healthOf(*on.Port); err == nil {
		t.Fatal("the port was not let go")
	}
}

func TestAPairingCodeNeedsAListeningServerAndCarriesTheAddress(t *testing.T) {
	h := newHostHarness(t, nil)
	if _, err := h.host.CreatePairingCode(); err == nil || !strings.Contains(err.Error(), "Turn the phone bridge on") {
		t.Fatal(err)
	}
	on := enable(t, h.host, true)
	offer, err := h.host.CreatePairingCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(offer.Code) != 8 {
		t.Fatalf("code %q", offer.Code)
	}
	link := fmt.Sprintf("http://127.0.0.1:%d/#pair=%s", int(*on.Port), offer.Code)
	if want := "aicodingtool://pair?link=" + strings.NewReplacer(":", "%3A", "/", "%2F", "#", "%23", "=", "%3D").Replace(link); offer.URL != want {
		t.Fatalf("url %q, want %q", offer.URL, want)
	}
	if state := h.host.State(); state.Pairing == nil || state.Pairing.Code != offer.Code {
		t.Fatal("settings draws the code that was minted")
	}
	second, _ := h.host.CreatePairingCode()
	if second.Code == offer.Code {
		t.Fatal("a fresh code replaces the one on screen")
	}
	enable(t, h.host, false)
	if h.host.State().Pairing != nil {
		t.Fatal("turning the bridge off throws away the code with it")
	}
}

func TestAPhonePairedThroughTheHostReachesTheWindowAndRevokingDropsIt(t *testing.T) {
	h := newHostHarness(t, nil)
	on := enable(t, h.host, true)
	offer, _ := h.host.CreatePairingCode()
	client := newPhone(t, fmt.Sprintf("ws://127.0.0.1:%d/m/socket", int(*on.Port)), nil)
	client.send(pair(offer.Code, "iPhone"))
	client.waitFor("paired")
	until(t, h.snapshotAsked, "the window was never asked for a view")
	devices := h.host.State().Devices
	if len(devices) != 1 || devices[0].Name != "iPhone" {
		t.Fatalf("devices %+v", devices)
	}
	if strings.Contains(viewJSON(t, devices), "tokenHash") {
		t.Fatal("the hash never leaves the main process")
	}
	until(t, func() bool { return len(h.host.State().Sessions) == 1 }, "the session was never reported to settings")
	after := h.host.RevokeDevice(devices[0].ID)
	if len(after.Devices) != 0 || len(after.Sessions) != 0 {
		t.Fatalf("after %+v", after)
	}
}

func TestStoppingDisconnectsThePhoneAndReopeningKeepsItsPairing(t *testing.T) {
	h := newHostHarness(t, nil)
	on := enable(t, h.host, true)
	offer, _ := h.host.CreatePairingCode()
	client := newPhone(t, fmt.Sprintf("ws://127.0.0.1:%d/m/socket", int(*on.Port)), nil)
	client.send(pair(offer.Code, "Phone"))
	paired := client.waitFor("paired")

	h.host.Stop()
	until(t, client.closed, "the phone stayed connected after shutdown")
	if _, err := healthOf(*on.Port); err == nil {
		t.Fatal("the port is still served")
	}
	if !h.savedEnabled(t) {
		t.Fatal("stopping forgot the switch")
	}

	h.host.Start(h.options)
	reopened := h.host.State()
	if reopened.Status != domain.MobileServerStatusListening || !reopened.Enabled || len(reopened.Devices) != 1 || reopened.Devices[0].ID != paired["deviceId"] {
		t.Fatalf("reopened %+v", reopened)
	}
	returning := newPhone(t, fmt.Sprintf("ws://127.0.0.1:%d/m/socket", int(*reopened.Port)), nil)
	returning.send(resume(paired["token"].(string), nil, 0))
	until(t, func() bool { return len(h.host.State().Sessions) == 1 }, "the paired phone could not reconnect")
	if h.host.State().Sessions[0].DeviceID != paired["deviceId"] {
		t.Fatal("another device reconnected")
	}
}

func TestALateTailscaleResultDuringShutdownKeepsTheSavedSetting(t *testing.T) {
	h := newHostHarness(t, nil)
	enable(t, h.host, true)
	asked := make(chan struct{})
	finish := make(chan struct{})
	var once sync.Once
	blocking := noTailscale()
	blocking.read = func(context.Context, *int, *string) domain.TailscaleState {
		once.Do(func() { close(asked) })
		<-finish
		name := "computer.tail.test"
		return domain.TailscaleState{Status: domain.TailscaleStateStatusMissing, MagicDnsName: &name}
	}
	h.tailscale.set(blocking)
	refreshed := make(chan struct{})
	go func() {
		h.host.RefreshTailscale()
		close(refreshed)
	}()
	<-asked
	stopped := make(chan struct{})
	go func() {
		h.host.Stop()
		close(stopped)
	}()
	close(finish)
	<-refreshed
	<-stopped
	if !h.savedEnabled(t) {
		t.Fatal("a late answer overwrote the saved setting")
	}
}

func TestTheHealthProbeNamesThisProcess(t *testing.T) {
	h := newHostHarness(t, nil)
	on := enable(t, h.host, true)
	response, err := healthOf(*on.Port)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := make([]byte, 64)
	n, _ := response.Body.Read(body)
	if string(body[:n]) != MobileHealthResponse || response.Header.Get(MobileInstanceHeader) != MobileInstance {
		t.Fatalf("health %q %v", body[:n], response.Header)
	}
}

func TestServeIsLookedAtAgainOnceAMinute(t *testing.T) {
	clock := &fakeClock{}
	h := newHostHarness(t, clock)
	ready := &readyTailscale{}
	h.tailscale.set(ready.hooks())
	on := enable(t, h.host, true)
	clock.advance(0)
	if ready.startCount() != 1 || ready.starts[0] != int(*on.Port) || !h.host.State().Tailscale.Serving {
		t.Fatalf("starts %v", ready.starts)
	}

	// `tailscale serve reset`, or another copy taking 443 and quitting.
	ready.mu.Lock()
	ready.served = nil
	ready.mu.Unlock()
	clock.advance(59 * time.Second)
	if ready.startCount() != 1 {
		t.Fatal("nothing is asked again before the minute is up")
	}
	clock.advance(time.Second)
	if ready.startCount() != 2 || !h.host.State().Tailscale.Serving {
		t.Fatalf("starts %v", ready.starts)
	}

	// Waking from sleep looks at once rather than at the next minute.
	ready.mu.Lock()
	ready.served = nil
	ready.mu.Unlock()
	h.host.Recheck()
	clock.advance(0)
	if ready.startCount() != 3 {
		t.Fatalf("starts %v", ready.starts)
	}
}

func TestTurningOffTakesDownOnlyAServeThatPointsHere(t *testing.T) {
	h := newHostHarness(t, nil)
	ready := &readyTailscale{}
	h.tailscale.set(ready.hooks())
	on := enable(t, h.host, true)
	h.host.RefreshTailscale()
	enable(t, h.host, false)
	ready.mu.Lock()
	defer ready.mu.Unlock()
	if !slices.Equal(ready.stops, []int{int(*on.Port)}) {
		t.Fatalf("stops %v", ready.stops)
	}
}

func TestAServerThatWouldNotStartIsTriedAgain(t *testing.T) {
	h := newHostHarness(t, nil)
	*h.options.Port = 70_000
	if failed := enable(t, h.host, true); failed.Status != domain.MobileServerStatusError || failed.Port != nil {
		t.Fatalf("failed %+v", failed)
	}
	if still := enable(t, h.host, true); still.Status != domain.MobileServerStatusError {
		t.Fatal("the switch shrugged rather than trying again")
	}
	*h.options.Port = 0
	checked := h.host.RefreshTailscale()
	if checked.Status != domain.MobileServerStatusListening || checked.Error != nil {
		t.Fatalf("checked %+v", checked)
	}
}

func TestStoppingNeverWaitsOnATailscaleCall(t *testing.T) {
	h := newHostHarness(t, nil)
	asked := make(chan struct{})
	var aborted bool
	var mu sync.Mutex
	var once sync.Once
	ready := (&readyTailscale{}).hooks()
	// A first certificate can take a minute and a half.
	ready.start = func(ctx context.Context, _ int) TailscaleAction {
		once.Do(func() { close(asked) })
		<-ctx.Done()
		mu.Lock()
		aborted = true
		mu.Unlock()
		return TailscaleAction{Message: "aborted"}
	}
	h.tailscale.set(ready)
	enable(t, h.host, true)
	<-asked
	started := time.Now()
	h.host.Stop()
	mu.Lock()
	defer mu.Unlock()
	if !aborted || time.Since(started) > 2*time.Second {
		t.Fatalf("aborted %v after %v", aborted, time.Since(started))
	}
}
