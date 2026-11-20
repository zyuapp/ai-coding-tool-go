package speech

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type fakeModel struct {
	mu    sync.Mutex
	heard []int
	text  string
	err   error
}

func (m *fakeModel) Transcribe(samples []float32) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.heard = append(m.heard, len(samples))
	return m.text, m.err
}

// session drives a helper over pipes, reading its lines as they come.
type session struct {
	t      *testing.T
	in     *io.PipeWriter
	lines  chan map[string]any
	served chan struct{}
	helper *DictationHelper
}

func start(t *testing.T, options HelperOptions) *session {
	t.Helper()
	inReader, inWriter := io.Pipe()
	outReader, outWriter := io.Pipe()
	s := &session{t: t, in: inWriter, lines: make(chan map[string]any, 256), served: make(chan struct{})}
	s.helper = NewDictationHelper(outWriter, options)
	go func() {
		s.helper.Serve(inReader)
		close(s.served)
		outWriter.Close()
	}()
	go func() {
		scanner := bufio.NewScanner(outReader)
		for scanner.Scan() {
			var line map[string]any
			if err := jsonx.Unmarshal(scanner.Bytes(), &line); err != nil {
				t.Errorf("unreadable line %q", scanner.Text())
				continue
			}
			s.lines <- line
		}
		close(s.lines)
	}()
	t.Cleanup(func() {
		s.in.Close()
		<-s.served
		s.helper.Quit()
	})
	return s
}

func (s *session) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatal(err)
	}
}

// next is the next line that is not a level.
func (s *session) next() map[string]any {
	s.t.Helper()
	for {
		select {
		case line := <-s.lines:
			if line["type"] != "level" {
				return line
			}
		case <-time.After(10 * time.Second):
			s.t.Fatal("the helper said nothing")
		}
	}
}

func modelFolder(t *testing.T) string {
	folder := t.TempDir()
	for _, file := range LinuxModelFiles {
		if err := os.WriteFile(filepath.Join(folder, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

// soundRecorder writes a second of noise, then waits to be stopped.
var soundRecorder = Recorder{"/bin/sh", []string{"-c", "head -c 32000 /dev/urandom; exec sleep 30"}}

func TestHoldRecordsAndTranscribes(t *testing.T) {
	model := &fakeModel{text: "said"}
	var loads int
	s := start(t, HelperOptions{Folder: modelFolder(t), Recorders: []Recorder{soundRecorder}, Load: func(string, int) (Transcriber, error) {
		loads++
		return model, nil
	}})
	s.send(`{"op":"start","id":"hold"}`)
	if line := s.next(); line["type"] != "started" || line["id"] != "hold" {
		t.Fatalf("start: %v", line)
	}
	s.send(`{"op":"start","id":"other"}`)
	if line := s.next(); line["type"] != "error" || line["message"] != "Dictation is already recording." {
		t.Fatalf("second start: %v", line)
	}
	time.Sleep(200 * time.Millisecond)
	s.send(`{"op":"stop","id":"hold"}`)
	if line := s.next(); line["type"] != "text" || line["text"] != "said" {
		t.Fatalf("stop: %v", line)
	}
	if len(model.heard) != 1 || model.heard[0] != ParakeetSampleRate || loads != 1 {
		t.Fatalf("model heard %v after %d loads", model.heard, loads)
	}
	s.send(`{"op":"stop","id":"hold"}`)
	if line := s.next(); line["message"] != "Nothing is recording." {
		t.Fatalf("second stop: %v", line)
	}
}

func TestLevelsArriveWhileRecording(t *testing.T) {
	s := start(t, HelperOptions{Folder: modelFolder(t), Recorders: []Recorder{soundRecorder}, Load: func(string, int) (Transcriber, error) { return &fakeModel{}, nil }})
	s.send(`{"op":"start","id":"hold"}`)
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line := <-s.lines:
			if line["type"] == "level" {
				if value, ok := line["value"].(float64); !ok || value <= 0 || value > 1 || line["id"] != "hold" {
					t.Fatalf("level %v", line)
				}
				s.send(`{"op":"cancel","id":"hold"}`)
				for line := range s.lines {
					if line["type"] == "cancelled" {
						return
					}
				}
				return
			}
		case <-deadline:
			t.Fatal("no level")
		}
	}
}

func TestStartNeedsTheModelAndARecorder(t *testing.T) {
	s := start(t, HelperOptions{Folder: t.TempDir(), Recorders: []Recorder{soundRecorder}})
	s.send(`{"op":"start","id":"a"}`)
	if line := s.next(); line["message"] != "The speech model is not downloaded." {
		t.Fatalf("no model: %v", line)
	}
	load := func(string, int) (Transcriber, error) { return &fakeModel{}, nil }
	missing := start(t, HelperOptions{Folder: modelFolder(t), Load: load, Recorders: []Recorder{{"aic-no-such-recorder", nil}}})
	missing.send(`{"op":"start","id":"b"}`)
	if line := missing.next(); line["message"] != noRecorder {
		t.Fatalf("no recorder: %v", line)
	}
	refusing := start(t, HelperOptions{Folder: modelFolder(t), Load: load, Recorders: []Recorder{
		{"aic-no-such-recorder", nil},
		{"/bin/sh", []string{"-c", "echo first >&2; echo 'no such device' >&2; exit 1"}},
		{"/bin/sh", []string{"-c", "exit 1"}},
	}})
	refusing.send(`{"op":"start","id":"c"}`)
	if line := refusing.next(); line["message"] != "The microphone could not start: no such device" {
		t.Fatalf("refusing recorder: %v", line)
	}
}

func TestTranscribingAFileAndAModelThatFails(t *testing.T) {
	folder := modelFolder(t)
	recording := filepath.Join(t.TempDir(), "speech.wav")
	if err := os.WriteFile(recording, wav(wavOptions{channels: 1, rate: 8_000, bits: 16, data: make([]byte, 8_000)}), 0o600); err != nil {
		t.Fatal(err)
	}
	failures := 1
	model := &fakeModel{text: "from file"}
	s := start(t, HelperOptions{Folder: folder, Load: func(string, int) (Transcriber, error) {
		if failures > 0 {
			failures--
			return nil, errors.New("boom")
		}
		return model, nil
	}})
	s.send(`{"op":"transcribe","id":"1","path":"` + recording + `"}`)
	if line := s.next(); line["message"] != "The speech model could not run: boom" {
		t.Fatalf("failed load: %v", line)
	}
	s.send(`{"op":"transcribe","id":"2","path":"` + recording + `"}`)
	if line := s.next(); line["text"] != "from file" {
		t.Fatalf("reloaded: %v", line)
	}
	s.send(`{"op":"transcribe","id":"3"}`)
	if line := s.next(); line["message"] != "No file to transcribe." {
		t.Fatalf("no path: %v", line)
	}
	short := filepath.Join(t.TempDir(), "short.wav")
	if err := os.WriteFile(short, wav(wavOptions{channels: 1, rate: 16_000, bits: 16, data: make([]byte, 100)}), 0o600); err != nil {
		t.Fatal(err)
	}
	s.send(`{"op":"transcribe","id":"4","path":"` + short + `"}`)
	if line := s.next(); line["type"] != "text" || line["text"] != "" {
		t.Fatalf("under a quarter second: %v", line)
	}
}

func TestUnreadableAndUnknownRequests(t *testing.T) {
	s := start(t, HelperOptions{Folder: t.TempDir()})
	s.send(`not json`)
	if line := s.next(); line["message"] != "Unreadable request." || line["id"] != nil {
		t.Fatalf("unreadable: %v", line)
	}
	s.send(`{"op":"fly","id":"x"}`)
	if line := s.next(); line["message"] != "Unknown request fly." || line["id"] != "x" {
		t.Fatalf("unknown: %v", line)
	}
	s.send(`{"id":"y"}`)
	if line := s.next(); line["message"] != "Unknown request undefined." {
		t.Fatalf("no op: %v", line)
	}
	s.send(`{"op":"start"}`)
	if line := s.next(); line["message"] != "Unknown request start." {
		t.Fatalf("no id: %v", line)
	}
}

func TestDownloadChecksEachFileAndReportsProgress(t *testing.T) {
	files := map[string]string{"one.onnx": strings.Repeat("a", 300), "two.txt": strings.Repeat("b", 100)}
	var downloads []ModelDownload
	for _, name := range []string{"one.onnx", "two.txt"} {
		sum := sha256.Sum256([]byte(files[name]))
		downloads = append(downloads, ModelDownload{name, int64(len(files[name])), hex.EncodeToString(sum[:])})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/model/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	folder := filepath.Join(t.TempDir(), "models")
	s := start(t, HelperOptions{Folder: folder, Source: server.URL + "/model", Downloads: downloads})
	s.send(`{"op":"download"}`)
	var progress []float64
	for {
		line := s.next()
		if line["state"] == "downloading" {
			progress = append(progress, line["progress"].(float64))
			continue
		}
		if line["state"] != "ready" {
			t.Fatalf("download: %v", line)
		}
		break
	}
	if progress[0] != 0 || progress[len(progress)-1] != 0.99 {
		t.Fatalf("progress %v", progress)
	}
	for name, body := range files {
		if data, err := os.ReadFile(filepath.Join(folder, name)); err != nil || string(data) != body {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(folder, staging)); !os.IsNotExist(err) {
		t.Fatal("the staging folder stays behind")
	}

	files["two.txt"] = "tampered"
	again := filepath.Join(t.TempDir(), "models")
	broken := start(t, HelperOptions{Folder: again, Source: server.URL + "/model", Downloads: downloads})
	broken.send(`{"op":"download"}`)
	for {
		line := broken.next()
		if line["state"] == "downloading" {
			continue
		}
		if line["state"] != "failed" || line["message"] != "The downloaded speech model didn't match its checksum." {
			t.Fatalf("tampered: %v", line)
		}
		break
	}
	if _, err := os.Stat(filepath.Join(again, staging)); !os.IsNotExist(err) {
		t.Fatal("a failed download leaves its staging folder")
	}
	if _, err := os.Stat(filepath.Join(again, "one.onnx")); !os.IsNotExist(err) {
		t.Fatal("a failed download leaves part of the model in place")
	}
}
