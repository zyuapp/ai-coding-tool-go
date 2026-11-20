package mobile

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// HostOptions is what a Host runs on.
type HostOptions struct {
	UserData string
	// Send hands one relayed request to whoever answers phones. False when there is no one to hand it to.
	Send func(request contracts.MobileRequest) bool
	// OnState is what settings should now say. Called after anything at all moves.
	OnState func(state domain.MobileServerState)
	// Workspace is what another computer is handed and may drive. Nil on a host that takes no computers.
	Workspace WorkspaceHooks
	// Tailscale is how Tailscale is asked about and driven; nil drives the machine's own. A test answers
	// for it, because shelling out to whatever Tailscale the machine runs answers differently on two machines.
	Tailscale TailscaleHooks
	// Port is the port to ask for, domain.MobileDefaultPort when nil. A test passes 0 rather than take
	// a well-known port the running app may already serve on.
	Port *int
}

// mobileSettings is what the user chose, kept across launches so a paired phone still reaches a Mac
// that restarted. The tailnet name rides along so a phone that dials in before Tailscale has answered
// is recognised as one of ours rather than turned away for coming from a name not heard yet.
type mobileSettings struct {
	Version      float64 `json:"version"`
	Enabled      bool    `json:"enabled"`
	MagicDNSName *string `json:"magicDnsName"`
}

func defaultSettings() mobileSettings { return mobileSettings{Version: 1} }

var (
	// tailscaleRetry is how long to wait before asking Tailscale again while it is not yet serving: quick at first, then once a minute.
	tailscaleRetry = []time.Duration{5 * time.Second, 15 * time.Second, 60 * time.Second}
	// serverRetry is how long to wait before starting a server that would not start, the same way.
	serverRetry = []time.Duration{5 * time.Second, 15 * time.Second, 60 * time.Second}
)

// serveCheck is how often Serve is looked at once it works. Tailscale can be reset, reinstalled or
// signed into another account, or 443 handed to another copy of this app, and nothing tells us when.
const serveCheck = 60 * time.Second

// backoff is the pause before the next try, which stops growing at the last one.
func backoff(delays []time.Duration, attempt int) time.Duration {
	return delays[max(0, min(attempt, len(delays)-1))]
}

// timerFunc schedules f after d and answers how to cancel it.
type timerFunc func(d time.Duration, f func()) (stop func())

func realTimer(d time.Duration, f func()) func() {
	timer := time.AfterFunc(d, f)
	return func() { timer.Stop() }
}

type scheduled struct{ stop func() }

// Host keeps the phone bridge: its settings, the paired devices, the server, and Tailscale Serve in
// front of it. Starting and stopping run one after another, as do the Tailscale calls on a chain of
// their own, so a stop never waits behind a slow certificate.
type Host struct {
	after timerFunc

	// lifecycle runs starts and stops of the server one at a time: a start that overlapped a stop
	// would build a second server over live sessions.
	lifecycle serial
	// tailscaleWork drives Tailscale, which is slow to answer and slower to provision a certificate.
	// The server turn ends the moment the port is up, and a stop's unserve runs after any serve in flight.
	tailscaleWork serial

	mu        sync.Mutex
	options   *HostOptions
	settings  mobileSettings
	devices   *PairingStore
	relay     *Relay
	server    *Server
	tailscale domain.TailscaleState
	starting  bool
	stopping  bool
	failure   *string

	tailscaleRetry    *scheduled
	tailscaleAttempts int
	// tailscaleCtx is cancelled by every stop, so a turn off or a quit never waits out a slow certificate.
	tailscaleCtx    context.Context
	tailscaleCancel context.CancelFunc
	serverRetry     *scheduled
	serverAttempts  int
}

func NewHost() *Host {
	ctx, cancel := context.WithCancel(context.Background())
	return &Host{after: realTimer, settings: defaultSettings(), tailscale: domain.EmptyTailscaleState(), tailscaleCtx: ctx, tailscaleCancel: cancel}
}

func (h *Host) settingsPath() string {
	return filepath.Join(h.options.UserData, "mobile.v1.json")
}

func readSettings(path string) mobileSettings {
	text, err := os.ReadFile(path)
	if err != nil {
		return defaultSettings()
	}
	var stored map[string]any
	if jsonx.Unmarshal(text, &stored) != nil || stored == nil {
		return defaultSettings()
	}
	settings := mobileSettings{Version: 1, Enabled: stored["enabled"] == true}
	if name, ok := stored["magicDnsName"].(string); ok && name != "" {
		settings.MagicDNSName = &name
	}
	return settings
}

// writeSettings runs with h.mu held.
func (h *Host) writeSettings() {
	text, err := jsonx.Marshal(h.settings)
	if err == nil {
		err = os.WriteFile(h.settingsPath(), text, 0o666)
	}
	if err != nil {
		log.Printf("Could not write the phone bridge settings: %v", err)
	}
}

// servedName is the name a phone can reach, which is only a name while Tailscale is actually serving it.
func (h *Host) servedName() *string {
	if h.tailscale.Serving {
		return h.tailscale.MagicDnsName
	}
	return nil
}

func (h *Host) addresses() []*domain.MobileAddress {
	if h.server == nil {
		return []*domain.MobileAddress{}
	}
	port := h.server.Port()
	if port == nil {
		return []*domain.MobileAddress{}
	}
	return ReachableAddresses(*port, h.servedName())
}

// origins is where a browser may open a socket from. A tailnet name Tailscale has told us about counts
// even while Serve is reported off, because one slow answer from the daemon would otherwise turn every
// phone on the tailnet away at the next reconnection.
func (h *Host) origins() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	addresses := h.addresses()
	if h.tailscale.MagicDnsName != nil && *h.tailscale.MagicDnsName != "" {
		addresses = append(addresses, TailscaleAddress(*h.tailscale.MagicDnsName))
	}
	return AllowedOrigins(addresses)
}

// State is everything settings draws.
func (h *Host) State() domain.MobileServerState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stateLocked()
}

func (h *Host) stateLocked() domain.MobileServerState {
	if h.options == nil {
		return domain.EmptyMobileServerState()
	}
	reachable := h.addresses()
	primary := domain.PreferredAddress(reachable)
	var pending *domain.PairingCode
	if h.devices != nil {
		pending = h.devices.Pending(application.Now())
	}
	state := domain.MobileServerState{
		Enabled:   h.settings.Enabled,
		Status:    domain.MobileServerStatusOff,
		Addresses: reachable,
		Primary:   primary,
		Devices:   []*domain.PairedDeviceView{},
		Sessions:  []*domain.MobileSessionView{},
		Tailscale: h.tailscale,
		Error:     h.failure,
	}
	switch {
	case !h.settings.Enabled:
	case h.starting:
		state.Status = domain.MobileServerStatusStarting
	case h.failure != nil:
		state.Status = domain.MobileServerStatusError
	case h.server != nil:
		state.Status = h.server.Status()
	}
	if h.server != nil {
		if port := h.server.Port(); port != nil {
			value := float64(*port)
			state.Port = &value
		}
		state.Sessions = h.server.SessionViews()
		if state.Error == nil {
			state.Error = h.server.Error()
		}
	}
	if h.devices != nil {
		state.Devices = h.devices.Views()
	}
	if pending != nil && primary != nil {
		state.Pairing = domain.PairingOffer(*primary, *pending)
	}
	return state
}

func (h *Host) announce() {
	h.mu.Lock()
	if h.options == nil {
		h.mu.Unlock()
		return
	}
	state, onState := h.stateLocked(), h.options.OnState
	h.mu.Unlock()
	if onState != nil {
		onState(state)
	}
}

func (h *Host) port() int {
	if h.options.Port != nil {
		return *h.options.Port
	}
	return domain.MobileDefaultPort
}

func (h *Host) makeServer() (*Server, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	store, relay := h.devices, h.relay
	if store == nil || relay == nil {
		return nil, errNotReady
	}
	var made *Server
	made = NewServer(ServerOptions{
		Devices:        store,
		Port:           h.port(),
		AllowedOrigins: h.origins,
		Snapshot:       relay.Snapshot,
		Command:        relay.Command,
		Query:          relay.Query,
		OnChange: func() {
			h.mu.Lock()
			current := h.server == made
			h.mu.Unlock()
			if current && made.Status() == domain.MobileServerStatusError {
				go h.lifecycle.run(func() { h.recover(made) })
			}
			h.announce()
		},
		Workspace: h.options.Workspace,
	})
	return made, nil
}

// startServer: a server that would not start is tried again for as long as phone access stays on.
func (h *Host) startServer() {
	h.mu.Lock()
	if h.stopping || h.server != nil || !h.settings.Enabled {
		h.mu.Unlock()
		return
	}
	h.starting = true
	h.failure = nil
	h.mu.Unlock()
	h.announce()
	started, err := h.makeServer()
	if err == nil {
		err = started.Start(BindHost)
	}
	h.mu.Lock()
	if err == nil {
		h.server = started
	} else {
		message := err.Error()
		h.failure = &message
	}
	h.starting = false
	h.mu.Unlock()
	if err == nil {
		h.cancelServerRetry()
	}
	h.announce()
	h.mu.Lock()
	running, enabled, stopping := h.server != nil, h.settings.Enabled, h.stopping
	h.mu.Unlock()
	if running && enabled {
		h.scheduleServe(0)
	} else if !running && enabled && !stopping {
		h.scheduleStart()
	}
}

// recover lets a server that lost its port go and starts it again, the way one that never had a port is.
func (h *Host) recover(broken *Server) {
	h.mu.Lock()
	current := h.server == broken
	h.mu.Unlock()
	if !current {
		return
	}
	h.stopServer(false)
	h.mu.Lock()
	h.failure = broken.Error()
	enabled, stopping := h.settings.Enabled, h.stopping
	h.mu.Unlock()
	h.announce()
	if enabled && !stopping {
		h.scheduleStart()
	}
}

func (h *Host) scheduleStart() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.serverRetry != nil {
		h.serverRetry.stop()
	}
	retry := &scheduled{}
	h.serverRetry = retry
	retry.stop = h.after(backoff(serverRetry, h.serverAttempts), func() {
		h.mu.Lock()
		if h.serverRetry != retry {
			h.mu.Unlock()
			return
		}
		h.serverRetry = nil
		h.mu.Unlock()
		h.lifecycle.run(h.startServer)
	})
	h.serverAttempts++
}

func (h *Host) cancelServerRetry() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.serverRetry != nil {
		h.serverRetry.stop()
	}
	h.serverRetry = nil
	h.serverAttempts = 0
}

// abortTailscale stops whatever Tailscale is still being asked, and hands what comes next a context of its own.
func (h *Host) abortTailscale() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tailscaleCancel()
	h.tailscaleCtx, h.tailscaleCancel = context.WithCancel(context.Background())
}

func (h *Host) hooks() TailscaleHooks {
	if h.options != nil && h.options.Tailscale != nil {
		return h.options.Tailscale
	}
	return RealTailscale{}
}

// stopServer: turning phone access off takes Serve down with it, so Tailscale stops answering for a
// port that no longer listens, but only while Serve still points here: another copy of the app may
// hold it now. Quitting the app leaves Serve in place: Tailscale keeps the config across restarts, and
// the server comes back on launch when the user left it on.
func (h *Host) stopServer(unserve bool) {
	h.cancelServeRetry()
	h.cancelServerRetry()
	h.abortTailscale()
	h.mu.Lock()
	running, relay := h.server, h.relay
	h.server = nil
	h.mu.Unlock()
	var port *int
	if running != nil {
		port = running.Port()
		running.Stop()
	}
	if relay != nil {
		relay.FailAll("The phone bridge was turned off.")
	}
	if !unserve || port == nil {
		return
	}
	h.tailscaleWork.run(func() {
		h.mu.Lock()
		ctx, hooks := h.tailscaleCtx, h.hooks()
		h.mu.Unlock()
		action := hooks.Stop(ctx, *port)
		h.refreshTailscaleState()
		if !action.OK {
			h.mu.Lock()
			message := action.Message
			h.tailscale.Error = &message
			h.mu.Unlock()
		}
	})
}

func (h *Host) cancelServeRetry() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tailscaleRetry != nil {
		h.tailscaleRetry.stop()
	}
	h.tailscaleRetry = nil
	h.tailscaleAttempts = 0
}

// scheduleServe asks Tailscale to serve the bridge after a pause, once at a time, and only while there is a bridge to serve.
func (h *Host) scheduleServe(delay time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tailscaleRetry != nil {
		h.tailscaleRetry.stop()
	}
	retry := &scheduled{}
	h.tailscaleRetry = retry
	retry.stop = h.after(delay, func() {
		h.mu.Lock()
		if h.tailscaleRetry != retry {
			h.mu.Unlock()
			return
		}
		h.tailscaleRetry = nil
		h.mu.Unlock()
		h.tailscaleWork.run(h.serveIfReady)
		h.announce()
	})
}

// Start reads the settings, and starts the server when the user left it on. Tailscale is asked about
// in the background, because a machine without it must not slow the app's own launch.
func (h *Host) Start(options *HostOptions) {
	devices := NewPairingStore(filepath.Join(options.UserData, "mobile-devices.v1.json"))
	settings := readSettings(filepath.Join(options.UserData, "mobile.v1.json"))
	h.mu.Lock()
	h.stopping = false
	h.tailscaleCtx, h.tailscaleCancel = context.WithCancel(context.Background())
	h.options = options
	h.settings = settings
	h.tailscale = domain.EmptyTailscaleState()
	h.tailscale.MagicDnsName = settings.MagicDNSName
	h.devices = devices
	h.relay = NewRelay(options.Send)
	h.mu.Unlock()
	if settings.Enabled {
		h.lifecycle.run(h.startServer)
		return
	}
	go func() {
		h.tailscaleWork.run(func() { h.refreshTailscaleState() })
		h.announce()
	}()
}

// Stop never waits on Tailscale: whatever it is still being asked is cancelled.
func (h *Host) Stop() {
	h.mu.Lock()
	h.stopping = true
	h.mu.Unlock()
	h.cancelServeRetry()
	h.cancelServerRetry()
	h.abortTailscale()
	h.lifecycle.run(func() { h.stopServer(false) })
	h.tailscaleWork.wait()
	h.mu.Lock()
	h.options = nil
	h.devices = nil
	h.relay = nil
	h.tailscale = domain.EmptyTailscaleState()
	h.mu.Unlock()
}

// SetEnabled turns phone access on or off. On but not running is a server that failed, which asking
// again starts rather than shrugs at.
func (h *Host) SetEnabled(enabled bool) (domain.MobileServerState, error) {
	h.mu.Lock()
	if h.settings.Enabled == enabled && (!enabled || h.server != nil) {
		state := h.stateLocked()
		h.mu.Unlock()
		return state, nil
	}
	if h.options == nil {
		h.mu.Unlock()
		return domain.MobileServerState{}, errNotReady
	}
	if h.settings.Enabled != enabled {
		h.settings.Enabled = enabled
		h.writeSettings()
	}
	h.mu.Unlock()
	// The switch answers at once; the server follows behind whatever turn is still running.
	h.announce()
	if enabled {
		h.lifecycle.run(func() {
			h.mu.Lock()
			running := h.server != nil
			h.mu.Unlock()
			if running {
				h.scheduleServe(0)
			} else {
				h.startServer()
			}
		})
	} else {
		h.lifecycle.run(func() { h.stopServer(true) })
		h.mu.Lock()
		if h.devices != nil {
			h.devices.DiscardCode()
		}
		h.failure = nil
		h.mu.Unlock()
	}
	h.announce()
	return h.State(), nil
}

func (h *Host) CreatePairingCode() (*domain.MobilePairingOffer, error) {
	h.mu.Lock()
	store := h.devices
	if store == nil {
		h.mu.Unlock()
		return nil, errNotReady
	}
	primary := domain.PreferredAddress(h.addresses())
	h.mu.Unlock()
	if primary == nil {
		return nil, errors.New("Turn the phone bridge on before pairing a phone.")
	}
	code := store.Mint(application.Now())
	h.announce()
	return domain.PairingOffer(*primary, code), nil
}

func (h *Host) RevokeDevice(deviceID string) domain.MobileServerState {
	h.mu.Lock()
	store, server := h.devices, h.server
	h.mu.Unlock()
	if store != nil && store.Revoke(deviceID) && server != nil {
		server.DropDevice(deviceID)
	}
	h.announce()
	return h.State()
}

func (h *Host) refreshTailscaleState() {
	h.mu.Lock()
	ctx, hooks, known := h.tailscaleCtx, h.hooks(), h.tailscale.MagicDnsName
	var port *int
	if h.server != nil {
		port = h.server.Port()
	}
	h.mu.Unlock()
	read := hooks.Read(ctx, port, known)
	// An answer cut short by a stop says nothing about Tailscale.
	if ctx.Err() != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.tailscale = read
	name := read.MagicDnsName
	if name != nil && *name != "" && (h.settings.MagicDNSName == nil || *h.settings.MagicDNSName != *name) {
		held := *name
		h.settings.MagicDNSName = &held
		if h.options != nil {
			h.writeSettings()
		}
	}
}

// RefreshTailscale asks Tailscale again, and finishes the setup if it now can: a user who installs or
// signs into Tailscale after turning phone access on presses "Check again" and is served without
// another switch. A server that would not start is tried again first, since there is nothing to serve without one.
func (h *Host) RefreshTailscale() domain.MobileServerState {
	h.cancelServeRetry()
	h.mu.Lock()
	retry := h.settings.Enabled && h.server == nil
	h.mu.Unlock()
	if retry {
		h.lifecycle.run(h.startServer)
		// This check is about to serve it, so the one a fresh server schedules would only repeat it.
		h.cancelServeRetry()
	}
	h.tailscaleWork.run(func() {
		h.mu.Lock()
		running := h.server != nil
		h.mu.Unlock()
		if running {
			h.serveIfReady()
		} else {
			h.refreshTailscaleState()
		}
	})
	h.announce()
	return h.State()
}

// Recheck looks again now rather than at the next check: a machine that slept may wake to a Tailscale
// that was reset or handed 443 elsewhere meanwhile, and to a server that can start where it could not.
func (h *Host) Recheck() {
	h.mu.Lock()
	if h.options == nil || h.stopping || !h.settings.Enabled {
		h.mu.Unlock()
		return
	}
	running, starting := h.server != nil, h.starting
	h.mu.Unlock()
	if running {
		h.cancelServeRetry()
		h.scheduleServe(0)
	} else if !starting {
		go h.lifecycle.run(h.startServer)
	}
}

// serveIfReady keeps Tailscale Serve in front of the listening server for as long as it listens. A
// tailnet that is not signed in or issues no certificate is reported as it stands rather than asked
// and left to hang, and asked again later: Tailscale often comes up after the app does. Once served it
// is still looked at once a minute, and served again the moment it points anywhere else. Whatever goes
// wrong, the next look is booked.
func (h *Host) serveIfReady() {
	h.mu.Lock()
	listening, stopping, enabled := h.server, h.stopping, h.settings.Enabled
	h.mu.Unlock()
	if stopping || listening == nil || !enabled {
		return
	}
	port := listening.Port()
	if port == nil {
		return
	}
	serving := h.serveListening(listening, *port)
	h.mu.Lock()
	if h.stopping || h.server != listening || !h.settings.Enabled {
		h.mu.Unlock()
		return
	}
	delay := serveCheck
	if serving {
		h.tailscaleAttempts = 0
	} else {
		h.tailscaleAttempts++
		delay = backoff(tailscaleRetry, h.tailscaleAttempts-1)
	}
	h.mu.Unlock()
	h.scheduleServe(delay)
}

// serveListening is whether Serve now fronts this server, having asked it to when Tailscale is able to.
func (h *Host) serveListening(listening *Server, port int) bool {
	h.refreshTailscaleState()
	h.mu.Lock()
	current, state, ctx, hooks := h.server == listening, h.tailscale, h.tailscaleCtx, h.hooks()
	h.mu.Unlock()
	if !current {
		return false
	}
	if state.Serving {
		return true
	}
	if state.Status != domain.TailscaleStateStatusReady || !state.Certs {
		return false
	}
	action := hooks.Start(ctx, port)
	h.mu.Lock()
	current = h.server == listening
	h.mu.Unlock()
	if !current {
		return false
	}
	h.refreshTailscaleState()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !action.OK {
		message := action.Message
		h.tailscale.Error = &message
	} else if h.devices != nil {
		h.devices.DiscardCode()
	}
	return h.tailscale.Serving
}

// NoticeComputers hands a notice to the computers on the line; with no server up there is no one to hand it to.
func (h *Host) NoticeComputers(notice contracts.ThreadNotice) {
	h.mu.Lock()
	server := h.server
	h.mu.Unlock()
	if server != nil {
		server.Notice(notice)
	}
}

// NoticePhones hands a notice to the phones that take them while closed, unless the phone bridge is off.
func (h *Host) NoticePhones(ctx context.Context, notice contracts.ThreadNotice) {
	h.mu.Lock()
	enabled, devices := h.settings.Enabled, h.devices
	h.mu.Unlock()
	if !enabled || devices == nil {
		return
	}
	PushToPhones(ctx, devices, notice, nil)
}

// AnnounceComputerName tells the computers on the line what this one now calls itself.
func (h *Host) AnnounceComputerName() {
	h.mu.Lock()
	server := h.server
	h.mu.Unlock()
	if server != nil {
		server.AnnounceName()
	}
}

func (h *Host) PublishView(update contracts.MobileViewUpdate) {
	h.mu.Lock()
	server := h.server
	h.mu.Unlock()
	if server != nil {
		server.Publish(update)
	}
}

func (h *Host) AnswerRequest(response contracts.MobileResponse) {
	h.mu.Lock()
	relay := h.relay
	h.mu.Unlock()
	if relay != nil {
		relay.Answer(response)
	}
}
