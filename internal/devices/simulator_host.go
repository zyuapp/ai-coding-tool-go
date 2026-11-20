package devices

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const (
	simulatorRequestTimeout = 15 * time.Second
	simulatorBootTimeout    = 240 * time.Second
	maxSimulatorShots       = 20
)

// actionableRoles are worth listing even with nothing to say, since they are what a tap lands on.
var actionableRoles = jsonx.SetOf("AXButton", "AXTextField", "AXSecureTextField", "AXTextArea", "AXSearchField", "AXSwitch", "AXCheckBox", "AXSlider", "AXLink", "AXCell", "AXMenuItem", "AXPopUpButton", "AXTab", "AXRadioButton", "AXIncrementor", "AXKey")

type simulatorAnswer struct {
	result map[string]any
	err    error
}

type simulatorPending struct {
	answer chan simulatorAnswer
	timer  *time.Timer
}

type simulatorHelper struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	ready   chan struct{}
	settle  sync.Once
	failure error
	// pending is guarded by the host's mutex.
	pending map[int]*simulatorPending
}

func (helper *simulatorHelper) markReady(failure error) {
	helper.settle.Do(func() {
		helper.failure = failure
		close(helper.ready)
	})
}

type simulatorSnapshot struct {
	id            int
	width, height float64
	elements      []*domain.SimulatorElement
}

type simulatorSize struct{ width, height float64 }

// fingers is where a touch is, with a second finger when it is a pinch.
type fingers struct {
	x, y   float64
	x2, y2 *float64
}

type simulatorMoves struct {
	queued bool
	next   *fingers
}

// SimulatorHostOptions configure a SimulatorHost.
type SimulatorHostOptions struct {
	// Helper is the aic-simulator program; NativeDir's when empty.
	Helper string
	// Clipboard writes the Mac's clipboard; pbcopy when nil.
	Clipboard func(text string)
}

// SimulatorHost drives iOS simulators through one aic-simulator helper that serves every device.
// It implements the simulator port's runtime and window calls, and streams frames to one listener.
type SimulatorHost struct {
	helperPath string
	clipboard  func(text string)

	xcodeOnce sync.Once
	xcodeDir  string

	mu           sync.Mutex
	helper       *simulatorHelper
	unavailable  string
	nextRequest  int
	nextSnapshot int
	shotHome     string
	shots        []string
	watchers     map[string]int
	snapshots    map[string]*simulatorSnapshot
	moves        map[string]*simulatorMoves
	// screens is each device's screen in points, learned from its accessibility root.
	screens map[string]simulatorSize
	// orientations is how each device is held, as the last rotation sent to it left it.
	orientations map[string]domain.SimulatorOrientation
	// startedHere are devices this app started, which it also shuts down once nothing shows them.
	startedHere map[string]bool
	// quieted are devices whose input is already kept off the Mac's default microphone.
	quieted map[string]chan struct{}
	// pasteboards is what each device's pasteboard held when this app last read or set it.
	pasteboards   map[string]string
	frameListener *func(domain.SimulatorFrame)

	// inputWork keeps each device's inputs in the order they came, so a key never overtakes a paste still being set up.
	inputWork turns
	// pasteboardWork keeps each device's pasteboard work in order, so a read never overtakes the write before it.
	pasteboardWork turns
}

func NewSimulatorHost(options SimulatorHostOptions) *SimulatorHost {
	host := &SimulatorHost{
		helperPath:   options.Helper,
		clipboard:    options.Clipboard,
		nextRequest:  1,
		nextSnapshot: 1,
		watchers:     map[string]int{},
		snapshots:    map[string]*simulatorSnapshot{},
		moves:        map[string]*simulatorMoves{},
		screens:      map[string]simulatorSize{},
		orientations: map[string]domain.SimulatorOrientation{},
		startedHere:  map[string]bool{},
		quieted:      map[string]chan struct{}{},
		pasteboards:  map[string]string{},
	}
	if host.helperPath == "" {
		host.helperPath = filepath.Join(NativeDir(), "aic-simulator")
	}
	if host.clipboard == nil {
		host.clipboard = writeMacClipboard
	}
	return host
}

var _ contracts.SimulatorPortRuntime = (*SimulatorHost)(nil)
var _ contracts.SimulatorPortWindowCalls = (*SimulatorHost)(nil)
var _ contracts.SimulatorPortWindowOwn = (*SimulatorHost)(nil)

// utf8Env is how simctl reads and writes the pasteboard in UTF-8, which an app opened from the Finder leaves unset.
var utf8Env = []string{"LANG=en_US.UTF-8"}

func writeMacClipboard(text string) {
	_, _, _ = platform.Run(context.Background(), "/usr/bin/pbcopy", nil, platform.RunOptions{Input: []byte(text), Env: utf8Env, Timeout: simulatorRequestTimeout})
}

// xcode is the Xcode the command line tools point at. Command line tools alone carry no simulators.
func (h *SimulatorHost) xcode(ctx context.Context) string {
	h.xcodeOnce.Do(func() {
		stdout, _, err := platform.Run(context.Background(), "xcode-select", []string{"-p"}, platform.RunOptions{})
		if err != nil {
			return
		}
		dir := strings.TrimSpace(string(stdout))
		if dir == "" {
			return
		}
		if _, err := os.Stat(filepath.Join(dir, "Library", "PrivateFrameworks", "SimulatorKit.framework")); err == nil {
			h.xcodeDir = dir
		}
	})
	return h.xcodeDir
}

func simulatorUnsupported(reason string) domain.SimulatorAvailability {
	return domain.SimulatorAvailabilityUnsupported{Reason: reason}
}

func (h *SimulatorHost) Availability(ctx context.Context) domain.SimulatorAvailability {
	if !platform.SimulatorSupported {
		return simulatorUnsupported("The simulator is only on macOS.")
	}
	h.mu.Lock()
	unavailable := h.unavailable
	h.mu.Unlock()
	if unavailable != "" {
		return simulatorUnsupported(unavailable)
	}
	if _, err := os.Stat(h.helperPath); err != nil {
		return simulatorUnsupported("This build of the app has no simulator helper.")
	}
	if h.xcode(ctx) == "" {
		return simulatorUnsupported("Install Xcode to use the simulator.")
	}
	return domain.SimulatorAvailabilityAvailable{}
}

func deviceState(state string) domain.SimulatorDeviceState {
	switch state {
	case "Booted":
		return domain.SimulatorDeviceStateBooted
	case "Booting":
		return domain.SimulatorDeviceStateBooting
	case "Shutdown":
		return domain.SimulatorDeviceStateShutdown
	}
	return domain.SimulatorDeviceStateOther
}

func runtimeTail(identifier string) string {
	return identifier[strings.LastIndex(identifier, ".")+1:]
}

// runtimeName reads "com.apple.CoreSimulator.SimRuntime.iOS-26-4" as "iOS 26.4".
func runtimeName(identifier string) string {
	parts := strings.Split(runtimeTail(identifier), "-")
	return parts[0] + " " + strings.Join(parts[1:], ".")
}

func runtimeVersion(identifier string) []float64 {
	parts := strings.Split(runtimeTail(identifier), "-")[1:]
	version := make([]float64, len(parts))
	for index, part := range parts {
		version[index] = jsNumberText(part)
	}
	return version
}

// jsNumberText is Number(text): NaN for what is not a number, 0 for nothing.
func jsNumberText(text string) float64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return math.NaN()
	}
	return value
}

func newerRuntime(left, right []float64) int {
	for index := range max(len(left), len(right)) {
		at := func(version []float64) float64 {
			if index < len(version) {
				return version[index]
			}
			return 0
		}
		difference := at(right) - at(left)
		if difference != 0 && !math.IsNaN(difference) {
			return cmp.Compare(difference, 0)
		}
	}
	return 0
}

type listedDevice struct {
	UDID        string `json:"udid"`
	Name        string `json:"name"`
	State       string `json:"state"`
	IsAvailable *bool  `json:"isAvailable"`
}

// ParseDeviceList lists iOS devices from the newest runtime down, iPhones before iPads within each.
func ParseDeviceList(text []byte) ([]*domain.SimulatorDevice, error) {
	var listed struct {
		Devices jsontext.Value `json:"devices"`
	}
	if err := jsonx.Unmarshal(text, &listed); err != nil {
		return nil, err
	}
	type runtimeDevices struct {
		runtime string
		devices []*listedDevice
	}
	var runtimes []*runtimeDevices
	if len(listed.Devices) > 0 && listed.Devices.Kind() == '{' {
		decoder := jsontext.NewDecoder(bytes.NewReader(listed.Devices), jsonx.Options())
		if _, err := decoder.ReadToken(); err != nil {
			return nil, err
		}
		seen := map[string]*runtimeDevices{}
		for decoder.PeekKind() != '}' {
			token, err := decoder.ReadToken()
			if err != nil {
				return nil, err
			}
			key := token.String()
			value, err := decoder.ReadValue()
			if err != nil {
				return nil, err
			}
			var devices []*listedDevice
			if value.Kind() != 'n' {
				if err := jsonx.Unmarshal(value, &devices); err != nil {
					return nil, err
				}
			}
			if known := seen[key]; known != nil {
				known.devices = devices
				continue
			}
			entry := &runtimeDevices{runtime: key, devices: devices}
			seen[entry.runtime] = entry
			runtimes = append(runtimes, entry)
		}
	}
	runtimes = slices.DeleteFunc(runtimes, func(entry *runtimeDevices) bool { return !strings.Contains(entry.runtime, ".SimRuntime.iOS-") })
	slices.SortStableFunc(runtimes, func(left, right *runtimeDevices) int {
		return newerRuntime(runtimeVersion(left.runtime), runtimeVersion(right.runtime))
	})
	devices := []*domain.SimulatorDevice{}
	for _, entry := range runtimes {
		available := slices.DeleteFunc(slices.Clone(entry.devices), func(device *listedDevice) bool {
			return device == nil || device.IsAvailable != nil && !*device.IsAvailable
		})
		slices.SortStableFunc(available, func(left, right *listedDevice) int {
			iPhone := func(device *listedDevice) int {
				if strings.HasPrefix(device.Name, "iPhone") {
					return 1
				}
				return 0
			}
			return iPhone(right) - iPhone(left)
		})
		for _, device := range available {
			devices = append(devices, &domain.SimulatorDevice{UDID: device.UDID, Name: device.Name, Runtime: runtimeName(entry.runtime), State: deviceState(device.State)})
		}
	}
	return devices, nil
}

func (h *SimulatorHost) ListSimulators(ctx context.Context) (*contracts.DesktopAPIListSimulatorsResult, error) {
	availability := h.Availability(ctx)
	if _, ok := availability.(domain.SimulatorAvailabilityAvailable); !ok {
		return &contracts.DesktopAPIListSimulatorsResult{Availability: availability, Devices: []*domain.SimulatorDevice{}}, nil
	}
	stdout, _, err := platform.Run(ctx, "xcrun", []string{"simctl", "list", "devices", "available", "-j"}, platform.RunOptions{MaxBuffer: 16 * 1024 * 1024})
	if err != nil {
		return nil, err
	}
	devices, err := ParseDeviceList(stdout)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	// A device that went down boots again held upright.
	for _, device := range devices {
		if device.State == domain.SimulatorDeviceStateBooted {
			continue
		}
		delete(h.orientations, device.UDID)
		delete(h.quieted, device.UDID)
		delete(h.pasteboards, device.UDID)
	}
	h.mu.Unlock()
	return &contracts.DesktopAPIListSimulatorsResult{Availability: availability, Devices: devices}, nil
}

// BootSimulator boots a device that is down and waits until it is up: `bootstatus -b` covers both.
func (h *SimulatorHost) BootSimulator(ctx context.Context, udid string) error {
	h.mu.Lock()
	h.startedHere[udid] = true
	h.mu.Unlock()
	if _, _, err := platform.Run(ctx, "xcrun", []string{"simctl", "bootstatus", udid, "-b"}, platform.RunOptions{Timeout: simulatorBootTimeout}); err != nil {
		return err
	}
	<-h.quiet(udid)
	return nil
}

// ShutdownSimulator takes down a device this app started; one started elsewhere is left running.
func (h *SimulatorHost) ShutdownSimulator(ctx context.Context, udid string) error {
	h.mu.Lock()
	started := h.startedHere[udid]
	delete(h.startedHere, udid)
	if started {
		delete(h.quieted, udid)
		delete(h.orientations, udid)
		delete(h.pasteboards, udid)
	}
	h.mu.Unlock()
	if !started {
		return nil
	}
	_, _, _ = platform.Run(ctx, "xcrun", []string{"simctl", "shutdown", udid}, platform.RunOptions{})
	return nil
}

// quiet keeps a running device off the headset in use, once per boot. Sound still works if this fails.
func (h *SimulatorHost) quiet(udid string) <-chan struct{} {
	h.mu.Lock()
	if pending, ok := h.quieted[udid]; ok {
		h.mu.Unlock()
		return pending
	}
	pending := make(chan struct{})
	h.quieted[udid] = pending
	h.mu.Unlock()
	go func() {
		defer close(pending)
		if _, err := h.request(context.Background(), map[string]any{"op": "quiet-audio", "udid": udid}, simulatorRequestTimeout); err != nil {
			h.mu.Lock()
			delete(h.quieted, udid)
			h.mu.Unlock()
			log.Printf("[simulator] audio input left on the default device: %s", err.Error())
		}
	}()
	return pending
}

// fail rejects everything the helper held. Called with the host's mutex held.
func (h *SimulatorHost) failPending(current *simulatorHelper, err error) {
	for id, pending := range current.pending {
		pending.timer.Stop()
		pending.answer <- simulatorAnswer{err: err}
		delete(current.pending, id)
	}
}

func (h *SimulatorHost) deliver(current *simulatorHelper, message SimulatorHelperMessage) {
	if message.Frame != nil {
		h.mu.Lock()
		listener := h.frameListener
		h.mu.Unlock()
		if listener != nil {
			(*listener)(message.Frame)
		}
		return
	}
	json := message.JSON
	switch json["event"] {
	case "ready":
		current.markReady(nil)
		return
	case "unavailable":
		reason, ok := json["reason"].(string)
		if !ok {
			reason = "The simulator is unavailable."
		}
		h.mu.Lock()
		h.unavailable = reason
		h.mu.Unlock()
		current.markReady(errors.New(reason))
		return
	}
	number, ok := json["id"].(float64)
	if !ok {
		return
	}
	id := int(number)
	if float64(id) != number {
		return
	}
	h.mu.Lock()
	pending := current.pending[id]
	delete(current.pending, id)
	h.mu.Unlock()
	if pending == nil {
		return
	}
	pending.timer.Stop()
	if json["ok"] == true {
		result, _ := json["result"].(map[string]any)
		if result == nil {
			result = map[string]any{}
		}
		pending.answer <- simulatorAnswer{result: result}
		return
	}
	message2, ok := json["error"].(string)
	if !ok {
		message2 = "The simulator refused that."
	}
	pending.answer <- simulatorAnswer{err: errors.New(message2)}
}

// started is the one helper that serves every device. A crash rejects what it held, and the next request starts another.
func (h *SimulatorHost) started(ctx context.Context) (*simulatorHelper, error) {
	h.mu.Lock()
	if h.helper != nil {
		current := h.helper
		h.mu.Unlock()
		return current, nil
	}
	h.mu.Unlock()
	dir := h.xcode(ctx)
	if dir == "" {
		return nil, errors.New("Install Xcode to use the simulator.")
	}
	h.mu.Lock()
	if h.helper != nil {
		current := h.helper
		h.mu.Unlock()
		return current, nil
	}
	command := platform.Command(context.Background(), h.helperPath, dir)
	stdin, err := command.StdinPipe()
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	if err := command.Start(); err != nil {
		h.mu.Unlock()
		return nil, err
	}
	current := &simulatorHelper{command: command, stdin: stdin, ready: make(chan struct{}), pending: map[int]*simulatorPending{}}
	h.helper = current
	h.mu.Unlock()

	var streams sync.WaitGroup
	streams.Add(2)
	go func() {
		defer streams.Done()
		reader := NewSimulatorFrameReader(func(message SimulatorHelperMessage) { h.deliver(current, message) })
		buffer := make([]byte, 256*1024)
		for {
			count, err := stdout.Read(buffer)
			if count > 0 {
				if pushErr := reader.Push(buffer[:count]); pushErr != nil {
					log.Printf("[simulator] %s", pushErr.Error())
					_ = command.Process.Kill()
					_, _ = io.Copy(io.Discard, stdout)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer streams.Done()
		buffer := make([]byte, 4096)
		for {
			count, err := stderr.Read(buffer)
			if count > 0 {
				log.Printf("[simulator] %s", strings.TrimRight(string(buffer[:count]), " \t\r\n"))
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		streams.Wait()
		_ = command.Wait()
		h.helperStopped(current)
	}()

	select {
	case <-current.ready:
		return current, current.failure
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *SimulatorHost) helperStopped(current *simulatorHelper) {
	failure := errors.New("The simulator helper stopped.")
	current.markReady(failure)
	h.mu.Lock()
	if h.helper == current {
		h.helper = nil
	}
	clear(h.quieted)
	h.failPending(current, failure)
	rewatch := len(h.watchers) > 0
	h.mu.Unlock()
	// Streams a view still watches pick up again in a new helper.
	if rewatch {
		time.AfterFunc(500*time.Millisecond, func() {
			h.mu.Lock()
			watched := map[string]int{}
			for udid, count := range h.watchers {
				watched[udid] = count
			}
			h.mu.Unlock()
			for udid, count := range watched {
				for range count {
					go func() {
						_, _ = h.request(context.Background(), map[string]any{"op": "watch", "udid": udid}, simulatorRequestTimeout)
					}()
				}
			}
		})
	}
}

func (h *SimulatorHost) request(ctx context.Context, body map[string]any, timeout time.Duration) (map[string]any, error) {
	current, err := h.started(ctx)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	id := h.nextRequest
	h.nextRequest++
	pending := &simulatorPending{answer: make(chan simulatorAnswer, 1)}
	pending.timer = time.AfterFunc(timeout, func() {
		h.mu.Lock()
		owned := current.pending[id] == pending
		delete(current.pending, id)
		h.mu.Unlock()
		if owned {
			pending.answer <- simulatorAnswer{err: errors.New("The simulator did not answer in time.")}
		}
	})
	current.pending[id] = pending
	h.mu.Unlock()

	message := map[string]any{"id": id}
	for key, value := range body {
		message[key] = value
	}
	line, err := jsonx.Marshal(message)
	if err == nil {
		current.writeMu.Lock()
		_, _ = current.stdin.Write(append(line, '\n'))
		current.writeMu.Unlock()
	}
	select {
	case answer := <-pending.answer:
		return answer.result, answer.err
	case <-ctx.Done():
		h.mu.Lock()
		if current.pending[id] == pending {
			delete(current.pending, id)
			pending.timer.Stop()
		}
		h.mu.Unlock()
		return nil, ctx.Err()
	}
}

func ratio(value float64) float64 {
	return math.Min(1, math.Max(0, value))
}

// elementCenter is the middle of an element from the latest read of this device; a ref from any older read is refused.
func (h *SimulatorHost) elementCenter(udid, ref string) (float64, float64, error) {
	h.mu.Lock()
	snapshot := h.snapshots[udid]
	h.mu.Unlock()
	parts := strings.Split(ref, ".")
	readID := jsNumberText(parts[0])
	if snapshot == nil || readID != float64(snapshot.id) {
		return 0, 0, fmt.Errorf("The ref %s is from an older read of the screen. Read it again, then tap.", ref)
	}
	index := slices.IndexFunc(snapshot.elements, func(item *domain.SimulatorElement) bool { return item.Ref == ref })
	if index < 0 || len(parts) < 2 {
		return 0, 0, fmt.Errorf("The latest read has no element %s.", ref)
	}
	frame := snapshot.elements[index].Frame
	x, y := h.onDevice(udid, ratio((frame[0]+frame[2]/2)/snapshot.width), ratio((frame[1]+frame[3]/2)/snapshot.height), simulatorSize{snapshot.width, snapshot.height})
	return x, y, nil
}

func (h *SimulatorHost) screenPoints(ctx context.Context, udid string) (simulatorSize, error) {
	h.mu.Lock()
	known, ok := h.screens[udid]
	h.mu.Unlock()
	if ok {
		return known, nil
	}
	result, err := h.request(ctx, map[string]any{"op": "describe", "udid": udid}, simulatorRequestTimeout)
	if err != nil {
		return simulatorSize{}, err
	}
	var frame []float64
	if elements, ok := result["elements"].([]any); ok && len(elements) > 0 {
		frame = rawElementOf(elements[0]).frame
	}
	if len(frame) < 4 || frame[2] == 0 || frame[3] == 0 || math.IsNaN(frame[2]) || math.IsNaN(frame[3]) {
		return simulatorSize{}, errors.New("Could not measure the simulator's screen.")
	}
	size := simulatorSize{frame[2], frame[3]}
	h.mu.Lock()
	h.screens[udid] = size
	h.mu.Unlock()
	return size, nil
}

// onDevice moves a point as the app on screen measures it onto the device's portrait screen. An app
// drawn sideways measures from the turned screen's corner; one that stayed upright, such as the home
// screen on a turned phone, measures from the device's own.
func (h *SimulatorHost) onDevice(udid string, u, v float64, screen simulatorSize) (float64, float64) {
	h.mu.Lock()
	orientation, ok := h.orientations[udid]
	h.mu.Unlock()
	if !ok {
		orientation = domain.SimulatorOrientationPortrait
	}
	if screen.width > screen.height {
		return domain.Untilted(u, v, orientation)
	}
	return u, v
}

// fractions is a position as fractions of the screen, from points when it was given in points.
func (h *SimulatorHost) fractions(ctx context.Context, udid string, x, y float64, points *bool) (float64, float64, error) {
	if points == nil || !*points {
		return x, y, nil
	}
	screen, err := h.screenPoints(ctx, udid)
	if err != nil {
		return 0, 0, err
	}
	u, v := h.onDevice(udid, ratio(x/screen.width), ratio(y/screen.height), screen)
	return u, v, nil
}

func (f fingers) into(body map[string]any) map[string]any {
	body["x"] = f.x
	body["y"] = f.y
	if f.x2 != nil {
		body["x2"] = *f.x2
		if f.y2 != nil {
			body["y2"] = *f.y2
		}
	}
	return body
}

func fingersOf(input domain.SimulatorInputTouch) fingers {
	return fingers{x: input.X, y: input.Y, x2: input.X2, y2: input.Y2}
}

// move: a drag streams moves faster than the device takes them, so a move waiting its turn only ever sends the newest position.
func (h *SimulatorHost) move(ctx context.Context, udid string, next fingers) error {
	h.mu.Lock()
	state := h.moves[udid]
	if state == nil {
		state = &simulatorMoves{}
	}
	h.moves[udid] = state
	state.next = &next
	if state.queued {
		h.mu.Unlock()
		return nil
	}
	state.queued = true
	h.mu.Unlock()
	return inTurn(&h.inputWork, udid, func() error {
		h.mu.Lock()
		state.queued = false
		waiting := state.next
		state.next = nil
		h.mu.Unlock()
		if waiting == nil {
			return nil
		}
		_, err := h.request(ctx, waiting.into(map[string]any{"op": "touch", "udid": udid, "phase": "move"}), simulatorRequestTimeout)
		return err
	})
}

const (
	commandKey = 227
	keyC       = 6
	keyV       = 25
	keyX       = 27
	// pasteSettle is how long the device takes to handle a paste; a key that reaches it sooner loses the paste or meets a paste prompt.
	pasteSettle = 300 * time.Millisecond
	// copyWait is how long a copy on the device may take to reach its pasteboard.
	copyWait = 1_500 * time.Millisecond
	copyPoll = 100 * time.Millisecond
)

func (h *SimulatorHost) readPasteboard(ctx context.Context, udid string) (string, error) {
	stdout, _, err := platform.Run(ctx, "xcrun", []string{"simctl", "pbpaste", udid}, platform.RunOptions{Env: utf8Env, Timeout: simulatorRequestTimeout, MaxBuffer: 16 * 1024 * 1024})
	return string(stdout), err
}

func (h *SimulatorHost) writePasteboard(ctx context.Context, udid, text string) error {
	if _, _, err := platform.Run(ctx, "xcrun", []string{"simctl", "pbcopy", udid}, platform.RunOptions{Input: []byte(text), Env: utf8Env, Timeout: simulatorRequestTimeout}); err != nil {
		return err
	}
	h.mu.Lock()
	h.pasteboards[udid] = text
	h.mu.Unlock()
	return nil
}

// shortcut presses ⌘ around one key, letting go of it even when the device refuses the key. Other modifiers are the keyboard's own, already held.
func (h *SimulatorHost) shortcut(ctx context.Context, udid string, usage float64) error {
	if _, err := h.request(ctx, map[string]any{"op": "key", "udid": udid, "usage": commandKey, "down": true}, simulatorRequestTimeout); err != nil {
		return err
	}
	defer func() {
		_, _ = h.request(ctx, map[string]any{"op": "key", "udid": udid, "usage": commandKey, "down": false}, simulatorRequestTimeout)
	}()
	if _, err := h.request(ctx, map[string]any{"op": "key", "udid": udid, "usage": usage, "down": true}, simulatorRequestTimeout); err != nil {
		return err
	}
	_, err := h.request(ctx, map[string]any{"op": "key", "udid": udid, "usage": usage, "down": false}, simulatorRequestTimeout)
	return err
}

// paste puts text on the device's pasteboard and pastes it into whatever has the keyboard, holding the device's next input until the paste lands.
func (h *SimulatorHost) paste(ctx context.Context, udid, text string) error {
	if err := inTurn(&h.pasteboardWork, udid, func() error {
		if err := h.writePasteboard(ctx, udid, text); err != nil {
			return err
		}
		return h.shortcut(ctx, udid, keyV)
	}); err != nil {
		return err
	}
	time.Sleep(pasteSettle)
	return nil
}

// copyOut presses ⌘C or ⌘X on the device, then puts what it copied on the Mac's clipboard once it
// reaches the device's pasteboard. A copy that changes nothing, such as one with nothing selected,
// leaves the Mac's clipboard alone; so does copying the very text the device's pasteboard already holds.
func (h *SimulatorHost) copyOut(ctx context.Context, udid string, usage float64) error {
	before, err := runInTurn(&h.inputWork, udid, func() (string, error) {
		current, err := runInTurn(&h.pasteboardWork, udid, func() (string, error) { return h.readPasteboard(ctx, udid) })
		if err != nil {
			return "", err
		}
		return current, h.shortcut(ctx, udid, usage)
	})
	if err != nil {
		return err
	}
	return inTurn(&h.pasteboardWork, udid, func() error {
		for until := time.Now().Add(copyWait); time.Now().Before(until); {
			time.Sleep(copyPoll)
			text, err := h.readPasteboard(ctx, udid)
			if err != nil {
				return err
			}
			if text == before {
				continue
			}
			h.mu.Lock()
			h.pasteboards[udid] = text
			h.mu.Unlock()
			if text != "" {
				h.clipboard(text)
			}
			return nil
		}
		return nil
	})
}

// syncPasteboard notes what the device's pasteboard holds, and on leaving brings anything copied on the device since to the Mac.
func (h *SimulatorHost) syncPasteboard(ctx context.Context, udid string, phase domain.SimulatorInputPasteboardPhase) error {
	return inTurn(&h.pasteboardWork, udid, func() error {
		text, err := h.readPasteboard(ctx, udid)
		if err != nil {
			return err
		}
		h.mu.Lock()
		known, wasKnown := h.pasteboards[udid]
		h.pasteboards[udid] = text
		h.mu.Unlock()
		if phase == domain.SimulatorInputPasteboardPhaseLeave && wasKnown && text != "" && text != known {
			h.clipboard(text)
		}
		return nil
	})
}

var usKeyboardText = regexp.MustCompile("^[\x20-\x7e\n\t]*$")
var trailingReturns = regexp.MustCompile("\n+$")

// PastedText splits text a US keyboard cannot type, which is pasted whole instead: a paste among
// typed keys lands before iOS has taken them and is lost or met with a paste prompt. Returns at its
// end are still pressed, so a submit still submits. False for text the keyboard types.
func PastedText(text string) (paste string, returns string, ok bool) {
	if usKeyboardText.MatchString(text) {
		return "", "", false
	}
	paste = trailingReturns.ReplaceAllString(text, "")
	return paste, text[len(paste):], true
}

func (h *SimulatorHost) typeText(ctx context.Context, udid, text string) error {
	paste, returns, pasted := PastedText(text)
	typed := text
	if pasted {
		if err := h.paste(ctx, udid, paste); err != nil {
			return err
		}
		if returns == "" {
			return nil
		}
		typed = returns
	}
	_, err := h.request(ctx, map[string]any{"op": "type", "udid": udid, "text": typed}, simulatorRequestTimeout+time.Duration(utf16Length(typed))*40*time.Millisecond)
	return err
}

// upright turns a device upright the first time this app uses it after starting, so the picture and
// every tap agree, even over a device someone left sideways in another tool. A device keeps the way
// it was last turned, but this app only knows the turns it made itself. A device that will not turn
// is taken as upright, so its taps still land.
func (h *SimulatorHost) upright(ctx context.Context, udid string) {
	h.mu.Lock()
	if _, ok := h.orientations[udid]; ok {
		h.mu.Unlock()
		return
	}
	h.orientations[udid] = domain.SimulatorOrientationPortrait
	h.mu.Unlock()
	_, _ = h.request(ctx, map[string]any{"op": "rotate", "udid": udid, "orientation": domain.OrientationCodes[domain.SimulatorOrientationPortrait]}, simulatorRequestTimeout)
}

// simulatorInputValue reads a pointer member of the input union as its value.
func simulatorInputValue(input domain.SimulatorInput) domain.SimulatorInput {
	switch input := input.(type) {
	case *domain.SimulatorInputTouch:
		return *input
	case *domain.SimulatorInputTap:
		return *input
	case *domain.SimulatorInputTapElement:
		return *input
	case *domain.SimulatorInputSwipe:
		return *input
	case *domain.SimulatorInputButton:
		return *input
	case *domain.SimulatorInputType:
		return *input
	case *domain.SimulatorInputKey:
		return *input
	case *domain.SimulatorInputShortcut:
		return *input
	case *domain.SimulatorInputPaste:
		return *input
	case *domain.SimulatorInputPasteboard:
		return *input
	case *domain.SimulatorInputRotate:
		return *input
	}
	return input
}

func (h *SimulatorHost) SimulatorInput(ctx context.Context, udid string, input domain.SimulatorInput) error {
	input = simulatorInputValue(input)
	switch input := input.(type) {
	case domain.SimulatorInputPasteboard:
		return h.syncPasteboard(ctx, udid, input.Phase)
	case domain.SimulatorInputShortcut:
		if input.Usage == keyC || input.Usage == keyX {
			return h.copyOut(ctx, udid, input.Usage)
		}
	case domain.SimulatorInputTouch:
		if input.Phase == domain.SimulatorInputTouchPhaseMove {
			return h.move(ctx, udid, fingersOf(input))
		}
		// A finger landing or lifting ends what a waiting move may still be folded into, so the next drag's moves queue after it.
		h.mu.Lock()
		delete(h.moves, udid)
		h.mu.Unlock()
	}
	return inTurn(&h.inputWork, udid, func() error { return h.perform(ctx, udid, input) })
}

func (h *SimulatorHost) perform(ctx context.Context, udid string, input domain.SimulatorInput) error {
	if _, rotate := input.(domain.SimulatorInputRotate); !rotate {
		h.upright(ctx, udid)
	}
	send := func(body map[string]any) error {
		_, err := h.request(ctx, body, simulatorRequestTimeout)
		return err
	}
	switch input := input.(type) {
	case domain.SimulatorInputTouch:
		return send(fingersOf(input).into(map[string]any{"op": "touch", "udid": udid, "phase": string(input.Phase)}))
	case domain.SimulatorInputTap:
		x, y, err := h.fractions(ctx, udid, input.X, input.Y, input.Points)
		if err != nil {
			return err
		}
		return send(map[string]any{"op": "tap", "udid": udid, "x": x, "y": y})
	case domain.SimulatorInputTapElement:
		x, y, err := h.elementCenter(udid, input.Ref)
		if err != nil {
			return err
		}
		return send(map[string]any{"op": "tap", "udid": udid, "x": x, "y": y})
	case domain.SimulatorInputSwipe:
		x, y, err := h.fractions(ctx, udid, input.X, input.Y, input.Points)
		if err != nil {
			return err
		}
		toX, toY, err := h.fractions(ctx, udid, input.ToX, input.ToY, input.Points)
		if err != nil {
			return err
		}
		duration, extra := 300.0, 0.0
		if input.DurationMs != nil {
			duration, extra = *input.DurationMs, *input.DurationMs
		}
		_, err = h.request(ctx, map[string]any{"op": "swipe", "udid": udid, "x": x, "y": y, "toX": toX, "toY": toY, "durationMs": duration}, simulatorRequestTimeout+time.Duration(extra*float64(time.Millisecond)))
		return err
	case domain.SimulatorInputButton:
		return send(map[string]any{"op": "button", "udid": udid, "button": string(input.Button)})
	case domain.SimulatorInputType:
		return h.typeText(ctx, udid, input.Text)
	case domain.SimulatorInputKey:
		return send(map[string]any{"op": "key", "udid": udid, "usage": input.Usage, "down": input.Down})
	case domain.SimulatorInputShortcut:
		return h.shortcut(ctx, udid, input.Usage)
	case domain.SimulatorInputPaste:
		return h.paste(ctx, udid, input.Text)
	case domain.SimulatorInputRotate:
		if err := send(map[string]any{"op": "rotate", "udid": udid, "orientation": domain.OrientationCodes[input.Orientation]}); err != nil {
			return err
		}
		h.mu.Lock()
		h.orientations[udid] = input.Orientation
		// The app redraws for the new way up, so what was read of it before no longer holds.
		delete(h.screens, udid)
		delete(h.snapshots, udid)
		h.mu.Unlock()
		return nil
	}
	return nil
}

type rawElement struct {
	role, label, value, identifier string
	hasRole                        bool
	frame                          []float64
	hasFrame                       bool
	depth                          float64
	hasDepth                       bool
}

func rawElementOf(value any) rawElement {
	object, _ := value.(map[string]any)
	var element rawElement
	element.role, element.hasRole = object["role"].(string)
	element.label, _ = object["label"].(string)
	element.value, _ = object["value"].(string)
	element.identifier, _ = object["identifier"].(string)
	if frame, ok := object["frame"].([]any); ok {
		element.hasFrame = true
		for _, part := range frame {
			number, _ := part.(float64)
			element.frame = append(element.frame, number)
		}
	}
	element.depth, element.hasDepth = object["depth"].(float64)
	return element
}

// listed is what a read lists: what has something to say or can be tapped, and is on the screen at all.
func listed(element rawElement, width, height float64) bool {
	part := func(index int) float64 {
		if index < len(element.frame) {
			return element.frame[index]
		}
		return 0
	}
	x, y, w, h := part(0), part(1), part(2), part(3)
	if w <= 0 || h <= 0 || x+w <= 0 || y+h <= 0 || x >= width || y >= height {
		return false
	}
	return element.label != "" || element.value != "" || element.identifier != "" || actionableRoles.Has(element.role)
}

func (h *SimulatorHost) DescribeSimulator(ctx context.Context, udid string, device string) (*domain.SimulatorSnapshot, error) {
	h.upright(ctx, udid)
	result, err := h.request(ctx, map[string]any{"op": "describe", "udid": udid}, simulatorRequestTimeout)
	if err != nil {
		return nil, err
	}
	rawList, _ := result["elements"].([]any)
	var width, height float64
	if len(rawList) > 0 {
		root := rawElementOf(rawList[0])
		if len(root.frame) > 2 {
			width = root.frame[2]
		}
		if len(root.frame) > 3 {
			height = root.frame[3]
		}
	}
	h.mu.Lock()
	id := h.nextSnapshot
	h.nextSnapshot++
	h.mu.Unlock()
	elements := []*domain.SimulatorElement{}
	if len(rawList) > 1 {
		for _, item := range rawList[1:] {
			raw := rawElementOf(item)
			if !listed(raw, width, height) {
				continue
			}
			role := "AXUnknown"
			if raw.hasRole {
				role = raw.role
			}
			element := &domain.SimulatorElement{Ref: fmt.Sprintf("%d.%d", id, len(elements)+1), Role: strings.TrimPrefix(role, "AX")}
			if raw.label != "" {
				element.Label = &raw.label
			}
			if raw.value != "" {
				element.Value = &raw.value
			}
			if raw.identifier != "" {
				element.Identifier = &raw.identifier
			}
			if raw.hasFrame {
				for index := range min(4, len(raw.frame)) {
					element.Frame[index] = jsRound(raw.frame[index])
				}
			}
			depth := 1.0
			if raw.hasDepth {
				depth = raw.depth
			}
			element.Depth = math.Max(0, depth-1)
			elements = append(elements, element)
		}
	}
	h.mu.Lock()
	h.snapshots[udid] = &simulatorSnapshot{id: id, width: width, height: height, elements: elements}
	if width != 0 && height != 0 {
		h.screens[udid] = simulatorSize{width, height}
	}
	h.mu.Unlock()
	snapshot := &domain.SimulatorSnapshot{UDID: udid, Device: device, Width: jsRound(width), Height: jsRound(height), Elements: elements}
	if application, ok := result["application"].(string); ok {
		snapshot.Application = &application
	}
	return snapshot, nil
}

func (h *SimulatorHost) CaptureSimulator(ctx context.Context, udid string) (*domain.SimulatorShot, error) {
	h.upright(ctx, udid)
	h.mu.Lock()
	if h.shotHome == "" {
		home, err := os.MkdirTemp("", "aicodingtool-simulator-")
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		h.shotHome = home
	}
	file := filepath.Join(h.shotHome, randomUUID()+".png")
	h.mu.Unlock()
	result, err := h.request(ctx, map[string]any{"op": "screenshot", "udid": udid, "path": file}, simulatorRequestTimeout)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.shots = append(h.shots, file)
	var stale []string
	for len(h.shots) > maxSimulatorShots {
		stale = append(stale, h.shots[0])
		h.shots = h.shots[1:]
	}
	h.mu.Unlock()
	for _, oldest := range stale {
		_ = os.Remove(oldest)
	}
	return &domain.SimulatorShot{Path: file, Width: jsNumber(result["width"]), Height: jsNumber(result["height"])}, nil
}

// WatchSimulator streams a device's screen to the frame listener while at least one view watches it.
func (h *SimulatorHost) WatchSimulator(ctx context.Context, udid string) error {
	h.mu.Lock()
	h.watchers[udid]++
	h.mu.Unlock()
	h.upright(ctx, udid)
	h.quiet(udid)
	if _, err := h.request(ctx, map[string]any{"op": "watch", "udid": udid}, simulatorRequestTimeout); err != nil {
		_ = h.UnwatchSimulator(ctx, udid)
		return err
	}
	return nil
}

func (h *SimulatorHost) UnwatchSimulator(ctx context.Context, udid string) error {
	h.mu.Lock()
	if count := h.watchers[udid] - 1; count > 0 {
		h.watchers[udid] = count
	} else {
		delete(h.watchers, udid)
	}
	running := h.helper != nil
	h.mu.Unlock()
	if running {
		_, _ = h.request(ctx, map[string]any{"op": "unwatch", "udid": udid}, simulatorRequestTimeout)
	}
	return nil
}

// OnSimulatorFrame makes listener the one that hears every frame, until the returned func is called.
func (h *SimulatorHost) OnSimulatorFrame(listener func(frame domain.SimulatorFrame)) func() {
	held := &listener
	h.mu.Lock()
	h.frameListener = held
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		if h.frameListener == held {
			h.frameListener = nil
		}
		h.mu.Unlock()
	}
}

// Stop forgets the window's devices and stops the helper. A new window starts with every device
// upright in its record, so the devices are turned to match when next used.
func (h *SimulatorHost) Stop() {
	h.mu.Lock()
	h.frameListener = nil
	clear(h.watchers)
	clear(h.orientations)
	clear(h.quieted)
	clear(h.screens)
	clear(h.snapshots)
	current := h.helper
	h.helper = nil
	home := h.shotHome
	h.shotHome = ""
	h.shots = nil
	h.mu.Unlock()
	if current != nil {
		_ = current.command.Process.Kill()
	}
	if home != "" {
		go func() { _ = os.RemoveAll(home) }()
	}
}

// shutdownStarted takes down the devices this app started, finishing after the app has gone.
func (h *SimulatorHost) shutdownStarted() {
	h.mu.Lock()
	started := make([]string, 0, len(h.startedHere))
	for udid := range h.startedHere {
		started = append(started, udid)
	}
	clear(h.startedHere)
	h.mu.Unlock()
	for _, udid := range started {
		platform.LaunchDetached("xcrun", "simctl", "shutdown", udid)
	}
}

// StopWithWindow is what closing the window does to the simulator.
func (h *SimulatorHost) StopWithWindow() {
	h.Stop()
	h.shutdownStarted()
}

// StopAtQuit is what quitting does to the simulator.
func (h *SimulatorHost) StopAtQuit() {
	h.Stop()
	h.shutdownStarted()
}

// jsRound is Math.round: halves go up, toward positive infinity.
func jsRound(value float64) float64 {
	return math.Floor(value + 0.5)
}

// utf16Length is a string's JavaScript length.
func utf16Length(text string) int {
	length := 0
	for _, r := range text {
		if r >= 0x10000 {
			length += 2
		} else {
			length++
		}
	}
	return length
}
