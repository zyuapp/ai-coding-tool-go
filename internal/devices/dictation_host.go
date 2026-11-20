package devices

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/devices/speech"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// Holding to talk: a helper records the microphone and runs Parakeet. On macOS it is a native program
// reading the model folder FluidAudio shares between apps, so a model another app downloaded is the
// one used here; on Linux it is a program that runs ONNX Runtime on a model in the user's data folder.

const (
	// dictationIdle is how long the helper, which holds the model in memory, lasts without a hold.
	dictationIdle              = 3 * time.Minute
	dictationStartTimeout      = 10 * time.Second
	dictationTranscribeTimeout = 120 * time.Second
	microphoneDenied           = "AI Coding Tool can't hear you. Allow it in System Settings → Privacy & Security → Microphone."
	unreadableRecording        = "That recording is too long or unreadable."
)

// DictationRuntime runs a helper that is a script.
type DictationRuntime struct {
	Command string
	Env     map[string]string
}

// DictationHelper is the helper's file, and the runtime that runs it when it is a script.
type DictationHelper struct {
	File    string
	Runtime *DictationRuntime
}

// DictationHelperFor is the helper in nativeFolder. On macOS, Core ML keeps one compiled model per
// process name, which a differently signed helper of that name replaces, so a source build's helper
// runs under its own name to stop it and the installed app's recompiling each other's.
func DictationHelperFor(nativeFolder string, packaged bool, goos string) DictationHelper {
	if goos == "linux" {
		return DictationHelper{File: filepath.Join(nativeFolder, "aic-dictation-linux")}
	}
	if packaged {
		return DictationHelper{File: filepath.Join(nativeFolder, "aic-dictation")}
	}
	return DictationHelper{File: filepath.Join(nativeFolder, "aic-dictation-dev")}
}

// DictationHostOptions configure a DictationHost.
type DictationHostOptions struct {
	Helper DictationHelper
	// Microphone asks for the microphone. A host without one can only transcribe recordings it is handed.
	Microphone func(ctx context.Context) bool
	// Models is where the model's files are; the place's folder unless named.
	Models string
	// Place is where this computer keeps the model; this computer's usual place when nil, unless Placeless.
	Place *speech.ModelPlace
	// Placeless says dictation can't run here.
	Placeless bool
}

type dictationHelperMessage struct {
	Type     string   `json:"type"`
	State    string   `json:"state"`
	Message  *string  `json:"message"`
	Progress *float64 `json:"progress"`
	ID       *string  `json:"id"`
	Value    float64  `json:"value"`
	Text     string   `json:"text"`
}

type dictationOutcome struct {
	text string
	err  error
}

type dictationWaiter struct {
	answer chan dictationOutcome
	timer  *time.Timer
}

type dictationChild struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
}

func (child *dictationChild) kill() {
	if child != nil && child.command.Process != nil {
		_ = child.command.Process.Signal(syscall.SIGTERM)
	}
}

type modelListener struct {
	call func(contracts.DictationModelStatus)
}

// DictationHost runs one helper per process, started on first use and stopped after three minutes idle, since it keeps the model in memory.
type DictationHost struct {
	options DictationHostOptions
	place   *speech.ModelPlace

	// emitMu keeps status listeners hearing changes in the order they happened. Listeners must not call back into the host.
	emitMu sync.Mutex

	mu             sync.Mutex
	helper         *dictationChild
	idleTimer      *time.Timer
	recordingID    *string
	starting       chan struct{}
	status         contracts.DictationModelStatus
	waiters        map[string]*dictationWaiter
	modelListeners map[*modelListener]struct{}
	levelListener  *func(level float64)
}

var _ contracts.DictationPortRuntime = (*DictationHost)(nil)
var _ contracts.DictationPortWindowOwn = (*DictationHost)(nil)

func NewDictationHost(options DictationHostOptions) *DictationHost {
	host := &DictationHost{
		options:        options,
		place:          options.Place,
		status:         contracts.DictationModelStatus{Model: domain.DictationModelUnsupported},
		waiters:        map[string]*dictationWaiter{},
		modelListeners: map[*modelListener]struct{}{},
	}
	if options.Placeless {
		host.place = nil
	} else if host.place == nil {
		host.place = speech.DictationModelPlace(runtime.GOOS, runtime.GOARCH)
	}
	return host
}

func sameStatus(left, right contracts.DictationModelStatus) bool {
	sameMessage := (left.Message == nil) == (right.Message == nil) && (left.Message == nil || *left.Message == *right.Message)
	sameProgress := (left.Progress == nil) == (right.Progress == nil) && (left.Progress == nil || *left.Progress == *right.Progress)
	return left.Model == right.Model && sameMessage && sameProgress
}

func status(model domain.DictationModel) contracts.DictationModelStatus {
	return contracts.DictationModelStatus{Model: model}
}

func statusWith(model domain.DictationModel, message string) contracts.DictationModelStatus {
	return contracts.DictationModelStatus{Model: model, Message: &message}
}

func downloadingStatus(progress float64) contracts.DictationModelStatus {
	return contracts.DictationModelStatus{Model: domain.DictationModelDownloading, Progress: &progress}
}

// OnDictationModel tells listener where the model stands at once, and again whenever it moves.
func (h *DictationHost) OnDictationModel(listener func(status contracts.DictationModelStatus)) func() {
	h.setStatus(h.readStatus())
	held := &modelListener{call: listener}
	h.emitMu.Lock()
	h.mu.Lock()
	h.modelListeners[held] = struct{}{}
	current := h.status
	h.mu.Unlock()
	listener(current)
	h.emitMu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.modelListeners, held)
		h.mu.Unlock()
	}
}

// OnDictationLevel makes listener the one that hears how loud the hold's microphone is.
func (h *DictationHost) OnDictationLevel(listener func(level float64)) func() {
	held := &listener
	h.mu.Lock()
	h.levelListener = held
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		if h.levelListener == held {
			h.levelListener = nil
		}
		h.mu.Unlock()
	}
}

func (h *DictationHost) level(value float64) {
	h.mu.Lock()
	listener := h.levelListener
	h.mu.Unlock()
	if listener != nil {
		(*listener)(value)
	}
}

func (h *DictationHost) StartDictation(ctx context.Context, id string) error {
	if err := h.usable(); err != nil {
		return err
	}
	if h.options.Microphone == nil {
		return errors.New("Holding to talk needs the desktop app.")
	}
	h.mu.Lock()
	if h.recordingID != nil {
		h.mu.Unlock()
		return errors.New("Dictation is already listening.")
	}
	h.recordingID = &id
	started := make(chan struct{})
	h.starting = started
	h.mu.Unlock()
	err := func() error {
		if !h.options.Microphone(ctx) {
			return errors.New(microphoneDenied)
		}
		_, err := h.request("start", id, dictationStartTimeout, nil)
		return err
	}()
	h.mu.Lock()
	if err != nil {
		if h.recordingID != nil && *h.recordingID == id {
			h.recordingID = nil
		}
	}
	child := h.helper
	if h.starting == started {
		h.starting = nil
	}
	h.mu.Unlock()
	close(started)
	if err != nil {
		// A start still queued behind other work would otherwise begin recording after the hold gave up.
		if child != nil {
			child.write(map[string]string{"op": "cancel", "id": id})
		}
		return err
	}
	return nil
}

// stopping takes the hold id off the microphone once its start has settled; false when nothing holds it.
func (h *DictationHost) stopping(id string) bool {
	h.mu.Lock()
	if h.recordingID == nil || *h.recordingID != id {
		h.mu.Unlock()
		return false
	}
	starting := h.starting
	h.mu.Unlock()
	if starting != nil {
		<-starting
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.recordingID == nil || *h.recordingID != id {
		return false
	}
	h.recordingID = nil
	return true
}

func (h *DictationHost) StopDictation(ctx context.Context, id string) (string, error) {
	if !h.stopping(id) {
		return "", errors.New("Nothing is listening.")
	}
	h.level(0)
	return h.request("stop", id, dictationTranscribeTimeout, nil)
}

func (h *DictationHost) CancelDictation(ctx context.Context, id string) error {
	if !h.stopping(id) {
		return nil
	}
	h.level(0)
	_, _ = h.request("cancel", id, dictationStartTimeout, nil)
	return nil
}

func (h *DictationHost) DownloadDictationModel(ctx context.Context) error {
	if h.readStatus().Model == domain.DictationModelUnsupported {
		return errors.New(domain.DictationUnsupported)
	}
	h.setStatus(downloadingStatus(0))
	h.keepAlive()
	h.send(map[string]string{"op": "download"})
	return nil
}

// CancelDictationDownload stops the helper, the only way to stop FluidAudio's download. Only an
// incomplete folder is removed, so a model another app finished meanwhile is kept.
func (h *DictationHost) CancelDictationDownload(ctx context.Context) error {
	h.mu.Lock()
	if h.status.Model != domain.DictationModelDownloading {
		h.mu.Unlock()
		return nil
	}
	child := h.helper
	h.helper = nil
	h.mu.Unlock()
	child.kill()
	complete := h.modelComplete()
	if !complete {
		_ = os.RemoveAll(h.modelFolder())
	}
	if complete {
		h.setStatus(status(domain.DictationModelReady))
	} else {
		h.setStatus(status(domain.DictationModelMissing))
	}
	return nil
}

// wavBytes checks a phone's recording is the small WAV it says it is before it touches the disk.
func wavBytes(audio string) ([]byte, error) {
	if !domain.IsDictationAudio(audio) {
		return nil, errors.New(unreadableRecording)
	}
	bytes, err := base64.StdEncoding.DecodeString(audio)
	if err != nil || len(bytes) > domain.MaxDictationAudioBytes || len(bytes) < 12 || string(bytes[0:4]) != "RIFF" || string(bytes[8:12]) != "WAVE" {
		return nil, errors.New(unreadableRecording)
	}
	return bytes, nil
}

func (h *DictationHost) TranscribeAudio(ctx context.Context, audio string) (string, error) {
	if err := h.usable(); err != nil {
		return "", err
	}
	bytes, err := wavBytes(audio)
	if err != nil {
		return "", err
	}
	folder, err := os.MkdirTemp("", "aic-dictation-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(folder)
	file := filepath.Join(folder, "speech.wav")
	if err := os.WriteFile(file, bytes, 0o600); err != nil {
		return "", err
	}
	return h.request("transcribe", randomUUID(), dictationTranscribeTimeout, map[string]string{"path": file})
}

// Stop stops the helper, if one was ever started.
func (h *DictationHost) Stop() {
	h.mu.Lock()
	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	child := h.helper
	h.helper = nil
	h.mu.Unlock()
	child.kill()
}

func (h *DictationHost) setStatus(next contracts.DictationModelStatus) {
	h.emitMu.Lock()
	defer h.emitMu.Unlock()
	h.mu.Lock()
	if sameStatus(next, h.status) {
		h.mu.Unlock()
		return
	}
	h.status = next
	listeners := make([]*modelListener, 0, len(h.modelListeners))
	for listener := range h.modelListeners {
		listeners = append(listeners, listener)
	}
	h.mu.Unlock()
	for _, listener := range listeners {
		listener.call(next)
	}
}

// readStatus is what the disk says, without starting the helper.
func (h *DictationHost) readStatus() contracts.DictationModelStatus {
	if h.place == nil {
		return statusWith(domain.DictationModelUnsupported, domain.DictationUnsupported)
	}
	if _, err := os.Stat(h.options.Helper.File); err != nil {
		return statusWith(domain.DictationModelUnsupported, "This build of the app has no dictation helper.")
	}
	h.mu.Lock()
	current := h.status
	h.mu.Unlock()
	if current.Model == domain.DictationModelDownloading {
		return current
	}
	if h.modelComplete() {
		return status(domain.DictationModelReady)
	}
	return status(domain.DictationModelMissing)
}

func (h *DictationHost) modelFolder() string {
	if h.options.Models != "" {
		return h.options.Models
	}
	if h.place != nil {
		return h.place.Folder
	}
	return ""
}

func (h *DictationHost) modelComplete() bool {
	if h.place == nil {
		return false
	}
	folder := h.modelFolder()
	for _, file := range h.place.Files {
		if _, err := os.Stat(filepath.Join(folder, file)); err != nil {
			return false
		}
	}
	return true
}

func (h *DictationHost) usable() error {
	current := h.readStatus()
	h.setStatus(current)
	if current.Model != domain.DictationModelReady {
		if current.Message != nil {
			return errors.New(*current.Message)
		}
		return errors.New("The speech model is not ready.")
	}
	return nil
}

func (h *DictationHost) settle(id string, outcome dictationOutcome) {
	h.mu.Lock()
	waiter := h.waiters[id]
	delete(h.waiters, id)
	h.mu.Unlock()
	if waiter == nil {
		return
	}
	waiter.timer.Stop()
	waiter.answer <- outcome
}

func (h *DictationHost) heard(message dictationHelperMessage) {
	id := ""
	if message.ID != nil {
		id = *message.ID
	}
	switch message.Type {
	case "model":
		switch {
		case message.State == "failed":
			reason := "The speech model could not be downloaded."
			if message.Message != nil {
				reason = *message.Message
			}
			h.setStatus(statusWith(domain.DictationModelFailed, reason))
		case message.State == "downloading" && message.Progress != nil:
			h.setStatus(downloadingStatus(min(1, max(0, *message.Progress))))
		default:
			h.setStatus(status(domain.DictationModel(message.State)))
		}
	case "level":
		h.mu.Lock()
		current := h.recordingID != nil && message.ID != nil && *h.recordingID == id
		h.mu.Unlock()
		if current {
			h.level(message.Value)
		}
	case "started", "cancelled":
		h.settle(id, dictationOutcome{})
	case "text":
		h.settle(id, dictationOutcome{text: message.Text})
	case "error":
		if id != "" {
			reason := ""
			if message.Message != nil {
				reason = *message.Message
			}
			h.settle(id, dictationOutcome{err: errors.New(reason)})
		} else if message.Message != nil {
			log.Printf("Dictation helper: %s", *message.Message)
		}
	}
}

// gone forgets a helper that stopped, failing what it held.
func (h *DictationHost) gone(child *dictationChild, reason string) {
	h.mu.Lock()
	if h.helper != child {
		h.mu.Unlock()
		return
	}
	h.helper = nil
	h.recordingID = nil
	downloading := h.status.Model == domain.DictationModelDownloading
	ids := make([]string, 0, len(h.waiters))
	for id := range h.waiters {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	if downloading {
		h.setStatus(statusWith(domain.DictationModelFailed, reason))
	}
	for _, id := range ids {
		h.settle(id, dictationOutcome{err: errors.New(reason)})
	}
}

func (h *DictationHost) ensureHelper() *dictationChild {
	h.mu.Lock()
	if h.helper != nil {
		child := h.helper
		h.mu.Unlock()
		return child
	}
	file, helperRuntime := h.options.Helper.File, h.options.Helper.Runtime
	var models []string
	if h.options.Models != "" {
		models = []string{"--models", h.options.Models}
	}
	var command *exec.Cmd
	if helperRuntime != nil {
		command = platform.Command(context.Background(), helperRuntime.Command, append([]string{file}, models...)...)
		for key, value := range helperRuntime.Env {
			command.Env = append(command.Env, key+"="+value)
		}
	} else {
		command = platform.Command(context.Background(), file, models...)
	}
	child := &dictationChild{command: command}
	h.helper = child
	h.mu.Unlock()

	stdin, stdinErr := command.StdinPipe()
	stdout, stdoutErr := command.StdoutPipe()
	command.Stderr = io.Discard
	if err := errors.Join(stdinErr, stdoutErr); err != nil {
		go h.gone(child, "The dictation helper could not run: "+err.Error())
		return child
	}
	child.stdin = stdin
	if err := command.Start(); err != nil {
		reason := err.Error()
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			reason = "spawn " + command.Path + " ENOENT"
		}
		go h.gone(child, "The dictation helper could not run: "+reason)
		return child
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for scanner.Scan() {
			var message dictationHelperMessage
			if err := jsonx.Unmarshal(scanner.Bytes(), &message); err != nil {
				log.Print("Dictation helper said something unreadable.")
				continue
			}
			h.heard(message)
		}
		_, _ = io.Copy(io.Discard, stdout)
		_ = command.Wait()
		h.gone(child, "The dictation helper stopped.")
	}()
	return child
}

func (child *dictationChild) write(request map[string]string) {
	line, err := jsonx.Marshal(request)
	if err != nil || child.stdin == nil {
		return
	}
	child.writeMu.Lock()
	defer child.writeMu.Unlock()
	_, _ = child.stdin.Write(append(line, '\n'))
}

func (h *DictationHost) send(request map[string]string) {
	h.ensureHelper().write(request)
}

func (h *DictationHost) keepAlive() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(dictationIdle, func() {
		h.mu.Lock()
		if h.idleTimer != timer {
			h.mu.Unlock()
			return
		}
		h.idleTimer = nil
		busy := h.recordingID != nil || len(h.waiters) > 0 || h.status.Model == domain.DictationModelDownloading
		var child *dictationChild
		if !busy {
			child = h.helper
			h.helper = nil
		}
		h.mu.Unlock()
		if busy {
			h.keepAlive()
			return
		}
		child.kill()
	})
	h.idleTimer = timer
}

func (h *DictationHost) request(op, id string, timeout time.Duration, extra map[string]string) (string, error) {
	h.keepAlive()
	waiter := &dictationWaiter{answer: make(chan dictationOutcome, 1)}
	waiter.timer = time.AfterFunc(timeout, func() { h.settle(id, dictationOutcome{err: errors.New("Dictation took too long.")}) })
	h.mu.Lock()
	h.waiters[id] = waiter
	h.mu.Unlock()
	request := map[string]string{"op": op, "id": id}
	for key, value := range extra {
		request[key] = value
	}
	h.send(request)
	outcome := <-waiter.answer
	return outcome.text, outcome.err
}

// DictationDesktop is what a runtime asks of dictation, from the host `host` answers with when first asked.
func DictationDesktop(host func() *DictationHost) contracts.DictationPortRuntime {
	return lazyDictation(host)
}

type lazyDictation func() *DictationHost

func (lazy lazyDictation) OnDictationModel(listener func(status contracts.DictationModelStatus)) func() {
	return lazy().OnDictationModel(listener)
}

func (lazy lazyDictation) StartDictation(ctx context.Context, id string) error {
	return lazy().StartDictation(ctx, id)
}

func (lazy lazyDictation) StopDictation(ctx context.Context, id string) (string, error) {
	return lazy().StopDictation(ctx, id)
}

func (lazy lazyDictation) CancelDictation(ctx context.Context, id string) error {
	return lazy().CancelDictation(ctx, id)
}

func (lazy lazyDictation) DownloadDictationModel(ctx context.Context) error {
	return lazy().DownloadDictationModel(ctx)
}

func (lazy lazyDictation) CancelDictationDownload(ctx context.Context) error {
	return lazy().CancelDictationDownload(ctx)
}

func (lazy lazyDictation) TranscribeAudio(ctx context.Context, audio string) (string, error) {
	return lazy().TranscribeAudio(ctx, audio)
}
