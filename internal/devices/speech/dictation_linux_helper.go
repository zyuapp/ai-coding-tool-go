package speech

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// The Linux dictation helper: records the microphone through PipeWire, PulseAudio or ALSA, and
// transcribes speech with a Transcriber. It runs in a process of its own and speaks the macOS
// helper's lines (native/dictation), so DictationHost drives both alike.
//
// In:  {"op":"download"} {"op":"start","id"} {"op":"stop","id"} {"op":"cancel","id"} {"op":"transcribe","id","path"}
// Out: {"type":"model","state":"ready"|"downloading"|"failed","message"?,"progress"?}
//      {"type":"started","id"} {"type":"level","id","value"} {"type":"text","id","text"}
//      {"type":"cancelled","id"} {"type":"error","id"?,"message"}

// Transcriber turns 16 kHz mono sound into words.
type Transcriber interface {
	Transcribe(samples []float32) (string, error)
}

// Recorder is a program that writes raw 16 kHz mono 16-bit sound on stdout.
type Recorder struct {
	Command string
	Args    []string
}

// Recorders are tried in this order, so whichever sound system is there records.
var Recorders = []Recorder{
	{"pw-record", []string{"--rate", "16000", "--channels", "1", "--format", "s16", "--raw", "-"}},
	{"parecord", []string{"--raw", "--rate=16000", "--channels=1", "--format=s16le"}},
	{"arecord", []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "16000", "-c", "1"}},
}

const noRecorder = "Dictation needs pw-record, parecord or arecord to hear the microphone."

// ModelDownload is one file of the model, with its size and checksum.
type ModelDownload struct {
	File   string
	Bytes  int64
	SHA256 string
}

// ModelSource is Parakeet TDT 0.6B v2's ONNX export, pinned to one revision.
const ModelSource = "https://huggingface.co/istupakov/parakeet-tdt-0.6b-v2-onnx/resolve/0bbb45a3365852604aef28b538a8f066f4ccaa85"

var ModelDownloads = []ModelDownload{
	{ParakeetPreprocessor, 139_764, "a9fde1486ebfcc08f328d75ad4610c67835fea58c73ba57e3209a6f6cf019e9f"},
	{ParakeetEncoder, 652_184_014, "3e0581fda6ab843888b51e56d7ee78b6d5bc3237ec113af1f732d1d5286aa155"},
	{ParakeetDecoder, 8_998_286, "a449f49acd68979d418651dd2dcb737cc0f1bf0225e009e29ee326354edbf7d3"},
	{ParakeetVocabulary, 9_384, "ec182b70dd42113aff6c5372c75cac58c952443eb22322f57bbd7f53977d497d"},
}

const (
	// maxRecordingBytes is five minutes; anything past it is dropped.
	maxRecordingBytes = ParakeetSampleRate * 2 * 300
	levelInterval     = 50 * time.Millisecond
	// firstSound is how long a recorder has to hear anything before the next one is tried.
	firstSound = 2_500 * time.Millisecond
	stopGrace  = time.Second
	// staging is where a download is put together, inside the model's folder so a cancelled one goes with it.
	staging = ".download"
)

// HelperOptions configure a DictationHelper.
type HelperOptions struct {
	// Folder holds the model.
	Folder string
	// Load reads the model in Folder; it runs once, off the request queue, and again after a failure.
	Load func(folder string, threads int) (Transcriber, error)
	// Recorders replace the default ones.
	Recorders []Recorder
	// Source and Downloads replace where the model comes from.
	Source    string
	Downloads []ModelDownload
	Client    *http.Client
}

// DictationHelper serves the helper's lines.
type DictationHelper struct {
	options HelperOptions

	out   io.Writer
	outMu sync.Mutex

	mu          sync.Mutex
	model       *loadingModel
	recording   *recording
	downloading bool
	// children are recorders a quit must not leave holding the microphone.
	children map[*exec.Cmd]struct{}
}

// NewDictationHelper answers on out.
func NewDictationHelper(out io.Writer, options HelperOptions) *DictationHelper {
	if options.Recorders == nil {
		options.Recorders = Recorders
	}
	if options.Source == "" {
		options.Source = ModelSource
	}
	if options.Downloads == nil {
		options.Downloads = ModelDownloads
	}
	if options.Client == nil {
		options.Client = http.DefaultClient
	}
	return &DictationHelper{options: options, out: out, children: map[*exec.Cmd]struct{}{}}
}

func (h *DictationHelper) emit(message map[string]any) {
	line, err := jsonx.Marshal(message)
	if err != nil {
		return
	}
	h.outMu.Lock()
	defer h.outMu.Unlock()
	_, _ = h.out.Write(append(line, '\n'))
}

func (h *DictationHelper) fail(id string, message string) {
	if id != "" {
		h.emit(map[string]any{"type": "error", "id": id, "message": message})
	} else {
		h.emit(map[string]any{"type": "error", "message": message})
	}
}

func (h *DictationHelper) modelComplete() bool {
	for _, file := range LinuxModelFiles {
		info, err := os.Stat(filepath.Join(h.options.Folder, file))
		if err != nil || info.Size() <= 0 {
			return false
		}
	}
	return true
}

// loadingModel is the model loading or loaded; transcriptions wait for it.
type loadingModel struct {
	ready       chan struct{}
	transcriber Transcriber
	err         error
}

// loadModel starts loading the model once, so recording goes ahead while it loads.
func (h *DictationHelper) loadModel() (*loadingModel, error) {
	if !h.modelComplete() {
		return nil, errors.New("The speech model is not downloaded.")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.model == nil {
		loading := &loadingModel{ready: make(chan struct{})}
		threads := min(4, max(1, runtime.NumCPU()/2))
		go func() {
			defer close(loading.ready)
			loading.transcriber, loading.err = h.options.Load(h.options.Folder, threads)
		}()
		h.model = loading
	}
	return h.model, nil
}

// dropModel forgets a model that failed, so the next hold loads it afresh.
func (h *DictationHelper) dropModel(model *loadingModel) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.model == model {
		h.model = nil
	}
}

// transcribe treats under a quarter second as nothing said, and pads under a second with the silence Parakeet needs.
func (h *DictationHelper) transcribe(sound []float32, sampleRate int) (string, error) {
	samples := Resampled(sound, float64(sampleRate), ParakeetSampleRate)
	if len(samples) < ParakeetSampleRate/4 {
		return "", nil
	}
	padded := samples
	if len(samples) < ParakeetSampleRate {
		padded = make([]float32, ParakeetSampleRate)
		copy(padded, samples)
	}
	model, err := h.loadModel()
	if err != nil {
		return "", err
	}
	<-model.ready
	if model.err != nil {
		h.dropModel(model)
		return "", fmt.Errorf("The speech model could not run: %s", model.err.Error())
	}
	text, err := model.transcriber.Transcribe(padded)
	if err != nil {
		h.dropModel(model)
		return "", fmt.Errorf("The speech model could not run: %s", err.Error())
	}
	return text, nil
}

func (h *DictationHelper) track(command *exec.Cmd) {
	h.mu.Lock()
	h.children[command] = struct{}{}
	h.mu.Unlock()
}

func (h *DictationHelper) untrack(command *exec.Cmd) {
	h.mu.Lock()
	delete(h.children, command)
	h.mu.Unlock()
}

// recording is one stretch of the microphone: its sound so far, and the levels it reports while it lasts.
type recording struct {
	id      string
	helper  *DictationHelper
	command *exec.Cmd
	closed  chan struct{}

	mu        sync.Mutex
	chunks    []byte
	heard     []byte
	lastLevel time.Time
}

func (r *recording) take(chunk []byte) {
	r.mu.Lock()
	if room := maxRecordingBytes - len(r.chunks); room > 0 {
		r.chunks = append(r.chunks, chunk[:min(len(chunk), room)]...)
	}
	r.heard = append(r.heard, chunk...)
	now := time.Now()
	if now.Sub(r.lastLevel) < levelInterval {
		r.mu.Unlock()
		return
	}
	r.lastLevel = now
	level := AudioLevel(PCM16Samples(r.heard))
	r.heard = nil
	r.mu.Unlock()
	r.helper.emit(map[string]any{"type": "level", "id": r.id, "value": level})
}

// stop stops the recorder and waits for the last of its sound.
func (r *recording) stop() []float32 {
	select {
	case <-r.closed:
	default:
		timer := time.AfterFunc(stopGrace, func() { _ = r.command.Process.Kill() })
		_ = r.command.Process.Signal(os.Interrupt)
		<-r.closed
		timer.Stop()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return PCM16Samples(r.chunks)
}

// openRecording tries each recorder until one hears something.
func (h *DictationHelper) openRecording(id string) (*recording, error) {
	refusal := ""
	for _, recorder := range h.options.Recorders {
		opened, problem := h.tryRecorder(id, recorder)
		if opened != nil {
			return opened, nil
		}
		if refusal == "" {
			refusal = problem
		}
	}
	if refusal == "" {
		refusal = noRecorder
	}
	return nil, errors.New(refusal)
}

// tryRecorder answers a recording that hears, or why this recorder did not ("" when it is not installed).
func (h *DictationHelper) tryRecorder(id string, recorder Recorder) (*recording, string) {
	command := platform.Command(context.Background(), recorder.Command, recorder.Args...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, "The microphone could not start: " + err.Error()
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, "The microphone could not start: " + err.Error()
	}
	if err := command.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, syscall.ENOENT) {
			return nil, ""
		}
		return nil, "The microphone could not start: " + err.Error()
	}
	h.track(command)
	opened := &recording{id: id, helper: h, command: command, closed: make(chan struct{})}
	live := make(chan struct{})
	var said strings.Builder
	var saidMu sync.Mutex
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		buffer := make([]byte, 32*1024)
		first := true
		for {
			count, err := stdout.Read(buffer)
			if count > 0 {
				if first {
					first = false
					close(live)
				}
				opened.take(buffer[:count])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer readers.Done()
		buffer := make([]byte, 4096)
		for {
			count, err := stderr.Read(buffer)
			if count > 0 {
				saidMu.Lock()
				text := said.String() + string(buffer[:count])
				said.Reset()
				said.WriteString(lastUnits(text, 400))
				saidMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		readers.Wait()
		_ = command.Wait()
		h.untrack(command)
		close(opened.closed)
	}()
	timer := time.NewTimer(firstSound)
	defer timer.Stop()
	select {
	case <-live:
		return opened, ""
	case <-timer.C:
		_ = command.Process.Kill()
		return nil, "The microphone did not start."
	case <-opened.closed:
		select {
		case <-live:
			return opened, ""
		default:
		}
		saidMu.Lock()
		lines := strings.Split(strings.TrimSpace(said.String()), "\n")
		saidMu.Unlock()
		if last := lines[len(lines)-1]; last != "" {
			return nil, "The microphone could not start: " + last
		}
		return nil, "The microphone could not start."
	}
}

// lastUnits is text.slice(-count), counting bytes.
func lastUnits(text string, count int) string {
	if len(text) <= count {
		return text
	}
	return text[len(text)-count:]
}

func (h *DictationHelper) start(id string) {
	h.mu.Lock()
	busy := h.recording != nil
	h.mu.Unlock()
	if busy {
		h.fail(id, "Dictation is already recording.")
		return
	}
	if !h.modelComplete() {
		h.fail(id, "The speech model is not downloaded.")
		return
	}
	_, _ = h.loadModel()
	opened, err := h.openRecording(id)
	if err != nil {
		h.fail(id, err.Error())
		return
	}
	h.mu.Lock()
	h.recording = opened
	h.mu.Unlock()
	h.emit(map[string]any{"type": "started", "id": id})
}

func (h *DictationHelper) takeRecording(id string) *recording {
	h.mu.Lock()
	defer h.mu.Unlock()
	taken := h.recording
	if taken == nil || taken.id != id {
		return nil
	}
	h.recording = nil
	return taken
}

func (h *DictationHelper) stop(id string) {
	taken := h.takeRecording(id)
	if taken == nil {
		h.fail(id, "Nothing is recording.")
		return
	}
	text, err := h.transcribe(taken.stop(), ParakeetSampleRate)
	if err != nil {
		h.fail(id, err.Error())
		return
	}
	h.emit(map[string]any{"type": "text", "id": id, "text": text})
}

func (h *DictationHelper) cancel(id string) {
	if taken := h.takeRecording(id); taken != nil {
		taken.stop()
	}
	h.emit(map[string]any{"type": "cancelled", "id": id})
}

func (h *DictationHelper) transcribeFile(id string, file string) {
	bytes, err := os.ReadFile(file)
	if err != nil {
		h.fail(id, err.Error())
		return
	}
	samples, sampleRate, err := WAVSamples(bytes)
	if err != nil {
		h.fail(id, err.Error())
		return
	}
	text, err := h.transcribe(samples, sampleRate)
	if err != nil {
		h.fail(id, err.Error())
		return
	}
	h.emit(map[string]any{"type": "text", "id": id, "text": text})
}

// fetchModelFile writes one file into the staging folder, checked against its pinned checksum as it arrives.
func (h *DictationHelper) fetchModelFile(source ModelDownload, staged string, arrived func(int)) error {
	response, err := h.options.Client.Get(h.options.Source + "/" + source.File)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("The download was refused (%d).", response.StatusCode)
	}
	file, err := os.OpenFile(filepath.Join(staged, source.File), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	buffer := make([]byte, 256*1024)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			hash.Write(buffer[:count])
			arrived(count)
			if _, err := file.Write(buffer[:count]); err != nil {
				file.Close()
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			file.Close()
			return readErr
		}
	}
	if err := file.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
		return errors.New("The downloaded speech model didn't match its checksum.")
	}
	return nil
}

// download reports whole percents while the files arrive; the model is ready once every one is checked and in place.
func (h *DictationHelper) download() {
	h.mu.Lock()
	if h.downloading {
		h.mu.Unlock()
		return
	}
	h.downloading = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.downloading = false
		h.mu.Unlock()
	}()
	h.emit(map[string]any{"type": "model", "state": "downloading", "progress": 0})
	staged := filepath.Join(h.options.Folder, staging)
	var total, received int64
	for _, source := range h.options.Downloads {
		total += source.Bytes
	}
	reported := 0
	arrived := func(bytes int) {
		received += int64(bytes)
		percent := min(99, int(math.Floor(float64(received)/float64(total)*100)))
		if percent == reported {
			return
		}
		reported = percent
		h.emit(map[string]any{"type": "model", "state": "downloading", "progress": float64(percent) / 100})
	}
	err := func() error {
		if err := os.RemoveAll(staged); err != nil {
			return err
		}
		if err := os.MkdirAll(staged, 0o700); err != nil {
			return err
		}
		for _, source := range h.options.Downloads {
			if err := h.fetchModelFile(source, staged, arrived); err != nil {
				return err
			}
		}
		for _, source := range h.options.Downloads {
			if err := os.Rename(filepath.Join(staged, source.File), filepath.Join(h.options.Folder, source.File)); err != nil {
				return err
			}
		}
		return os.RemoveAll(staged)
	}()
	if err != nil {
		_ = os.RemoveAll(staged)
		h.emit(map[string]any{"type": "model", "state": "failed", "message": err.Error()})
		return
	}
	h.emit(map[string]any{"type": "model", "state": "ready"})
}

// helperRequest is one line read, with whether it parsed as an object.
type helperRequest map[string]any

func parseRequest(line string) helperRequest {
	var value any
	if err := jsonx.Unmarshal([]byte(line), &value); err != nil {
		return nil
	}
	switch value := value.(type) {
	case map[string]any:
		return value
	case []any:
		return helperRequest{}
	}
	return nil
}

func (h *DictationHelper) handle(request helperRequest) {
	if request == nil {
		h.fail("", "Unreadable request.")
		return
	}
	id, _ := request["id"].(string)
	op, present := request["op"]
	if id != "" {
		switch op {
		case "start":
			h.start(id)
			return
		case "stop":
			h.stop(id)
			return
		case "cancel":
			h.cancel(id)
			return
		case "transcribe":
			if path, ok := request["path"].(string); ok {
				h.transcribeFile(id, path)
			} else {
				h.fail(id, "No file to transcribe.")
			}
			return
		}
	}
	h.fail(id, "Unknown request "+jsString(op, present)+".")
}

// jsString is String(value) for a value read from JSON.
func jsString(value any, present bool) string {
	switch value := value.(type) {
	case nil:
		if present {
			return "null"
		}
		return "undefined"
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		encoded, _ := jsonx.Marshal(value)
		return string(encoded)
	case []any:
		parts := make([]string, len(value))
		for index, item := range value {
			if item != nil {
				parts[index] = jsString(item, true)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// Serve reads requests from in until it closes: they run one at a time in the order they arrive, and a
// download runs beside them. It returns once the last request is done.
func (h *DictationHelper) Serve(in io.Reader) {
	queue := make(chan helperRequest, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for request := range queue {
			h.handle(request)
		}
	}()
	reader := bufio.NewReaderSize(in, 64*1024)
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" || err == nil {
			request := parseRequest(line)
			if request != nil && request["op"] == "download" {
				go h.download()
			} else {
				queue <- request
			}
		}
		if err != nil {
			break
		}
	}
	close(queue)
	<-done
}

// Quit stops every recorder still running.
func (h *DictationHelper) Quit() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for command := range h.children {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}
}
