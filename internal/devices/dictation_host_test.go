package devices

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/devices/speech"
)

// macModelFiles are handed to every host, so these run on any computer.
var macModelFiles = []string{"Preprocessor.mlmodelc", "Encoder.mlmodelc", "Decoder.mlmodelc", "JointDecision.mlmodelc", "parakeet_vocab.json"}

func completeModel(t *testing.T, models string) {
	t.Helper()
	for _, file := range macModelFiles[:4] {
		if err := os.MkdirAll(filepath.Join(models, file), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(models, "parakeet_vocab.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

type dictationSetup struct {
	noModel    bool
	microphone func(context.Context) bool
	runtime    *DictationRuntime
}

func setupDictation(t *testing.T, options dictationSetup) (*DictationHost, string) {
	t.Helper()
	t.Setenv(testRole, "dictation-helper")
	folder := t.TempDir()
	models := filepath.Join(folder, "models")
	if !options.noModel {
		completeModel(t, models)
	}
	microphone := options.microphone
	if microphone == nil {
		microphone = func(context.Context) bool { return true }
	}
	host := NewDictationHost(DictationHostOptions{
		Helper:     DictationHelper{File: os.Args[0], Runtime: options.runtime},
		Models:     models,
		Place:      &speech.ModelPlace{Folder: models, Files: macModelFiles},
		Microphone: microphone,
	})
	t.Cleanup(host.Stop)
	return host, folder
}

func models(host *DictationHost) []string {
	var seen []string
	host.OnDictationModel(func(status contracts.DictationModelStatus) { seen = append(seen, string(status.Model)) })
	return seen
}

func wavAudio() string {
	bytes := make([]byte, 44)
	copy(bytes, "RIFF")
	copy(bytes[8:], "WAVE")
	return base64.StdEncoding.EncodeToString(bytes)
}

func expectError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("want an error with %q, got %v", text, err)
	}
}

func TestModelReadsReadyOnlyWhenItsFilesAreThere(t *testing.T) {
	ready, _ := setupDictation(t, dictationSetup{})
	missing, _ := setupDictation(t, dictationSetup{noModel: true})
	if got := append(models(ready), models(missing)...); !slices.Equal(got, []string{"ready", "missing"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHoldHearsLevelsAndAnswersWithWords(t *testing.T) {
	ctx := context.Background()
	host, _ := setupDictation(t, dictationSetup{})
	var mu sync.Mutex
	var levels []float64
	host.OnDictationLevel(func(level float64) {
		mu.Lock()
		levels = append(levels, level)
		mu.Unlock()
	})
	if err := host.StartDictation(ctx, "hold-1"); err != nil {
		t.Fatal(err)
	}
	text, err := host.StopDictation(ctx, "hold-1")
	if err != nil || text != "said" {
		t.Fatalf("got %q %v", text, err)
	}
	mu.Lock()
	heardHalf := slices.Contains(levels, 0.5)
	mu.Unlock()
	if !heardHalf {
		t.Fatalf("levels %v", levels)
	}
	_, err = host.StopDictation(ctx, "hold-1")
	expectError(t, err, "Nothing is listening")
}

func TestLettingGoWhileTheMicrophoneIsAskedStopsItOnceItStarts(t *testing.T) {
	ctx := context.Background()
	allow := make(chan bool)
	asked := false
	host, _ := setupDictation(t, dictationSetup{microphone: func(context.Context) bool {
		if asked {
			return true
		}
		asked = true
		return <-allow
	}})
	started := make(chan error, 1)
	go func() { started <- host.StartDictation(ctx, "hold-2") }()
	for range 100 {
		host.mu.Lock()
		waiting := host.starting != nil
		host.mu.Unlock()
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stopped := make(chan string, 1)
	go func() {
		text, _ := host.StopDictation(ctx, "hold-2")
		stopped <- text
	}()
	time.Sleep(20 * time.Millisecond)
	allow <- true
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if text := <-stopped; text != "said" {
		t.Fatalf("got %q", text)
	}
	if err := host.StartDictation(ctx, "hold-3"); err != nil {
		t.Fatal(err)
	}
	if err := host.CancelDictation(ctx, "hold-3"); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedMicrophoneSaysHowToAllowIt(t *testing.T) {
	ctx := context.Background()
	host, _ := setupDictation(t, dictationSetup{microphone: func(context.Context) bool { return false }})
	expectError(t, host.StartDictation(ctx, "hold-4"), "System Settings")
	_, err := host.StopDictation(ctx, "hold-4")
	expectError(t, err, "Nothing is listening")
}

func TestPhoneRecordingMustBeASmallWAV(t *testing.T) {
	ctx := context.Background()
	host, _ := setupDictation(t, dictationSetup{})
	if text, err := host.TranscribeAudio(ctx, wavAudio()); err != nil || text != "from file" {
		t.Fatalf("got %q %v", text, err)
	}
	_, err := host.TranscribeAudio(ctx, base64.StdEncoding.EncodeToString([]byte("not a wav file")))
	expectError(t, err, "unreadable")
}

func TestNothingIsHeardWithoutTheModelAndAHeadlessHostHasNoMicrophone(t *testing.T) {
	ctx := context.Background()
	host, _ := setupDictation(t, dictationSetup{noModel: true})
	_, err := host.TranscribeAudio(ctx, wavAudio())
	expectError(t, err, "not ready")
	headless := NewDictationHost(DictationHostOptions{Helper: DictationHelper{File: "/nonexistent"}, Place: &speech.ModelPlace{Folder: "/nonexistent-models", Files: macModelFiles}})
	expectError(t, headless.StartDictation(ctx, "x"), "no dictation helper")
}

func TestDownloadReportsProgressThenReady(t *testing.T) {
	host, _ := setupDictation(t, dictationSetup{noModel: true})
	var mu sync.Mutex
	var seen []string
	host.OnDictationModel(func(status contracts.DictationModelStatus) {
		mu.Lock()
		defer mu.Unlock()
		if status.Progress == nil {
			seen = append(seen, string(status.Model))
		} else {
			seen = append(seen, fmt.Sprintf("%s %v", status.Model, *status.Progress))
		}
	})
	if err := host.DownloadDictationModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		mu.Lock()
		done := seen[len(seen)-1] == "ready"
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(seen, []string{"missing", "downloading 0", "downloading 0.5", "downloading", "ready"}) {
		t.Fatalf("got %v", seen)
	}
}

func TestCancellingADownloadRemovesThePartialModel(t *testing.T) {
	t.Setenv("SLOW_DOWNLOAD", "1")
	host, folder := setupDictation(t, dictationSetup{noModel: true})
	var seen []string
	host.OnDictationModel(func(status contracts.DictationModelStatus) { seen = append(seen, string(status.Model)) })
	if err := host.DownloadDictationModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(folder, "models")
	if err := os.MkdirAll(filepath.Join(partial, "Encoder.mlmodelc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := host.CancelDictationDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen[len(seen)-1] != "missing" {
		t.Fatalf("got %v", seen)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatal("the partial model stays")
	}
}

func TestModelWithoutItsVocabularyIsNotReady(t *testing.T) {
	host, folder := setupDictation(t, dictationSetup{})
	if err := os.Remove(filepath.Join(folder, "models", "parakeet_vocab.json")); err != nil {
		t.Fatal(err)
	}
	if got := models(host); !slices.Equal(got, []string{"missing"}) {
		t.Fatalf("got %v", got)
	}
}

func TestCancellingKeepsAModelThatBecameCompleteDuringTheDownload(t *testing.T) {
	t.Setenv("SLOW_DOWNLOAD", "1")
	host, folder := setupDictation(t, dictationSetup{noModel: true})
	var seen []string
	host.OnDictationModel(func(status contracts.DictationModelStatus) { seen = append(seen, string(status.Model)) })
	if err := host.DownloadDictationModel(context.Background()); err != nil {
		t.Fatal(err)
	}
	completeModel(t, filepath.Join(folder, "models"))
	if err := host.CancelDictationDownload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if seen[len(seen)-1] != "ready" {
		t.Fatalf("got %v", seen)
	}
	if _, err := os.Stat(filepath.Join(folder, "models", "parakeet_vocab.json")); err != nil {
		t.Fatal("the completed model went")
	}
}

func TestHoldThatFailsToStartTellsTheHelperToDropIt(t *testing.T) {
	ctx := context.Background()
	allowed := true
	host, folder := setupDictation(t, dictationSetup{microphone: func(context.Context) bool { return allowed }})
	if err := host.StartDictation(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.StopDictation(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	allowed = false
	expectError(t, host.StartDictation(ctx, "second"), "System Settings")
	heard := filepath.Join(folder, "heard.log")
	var lines []string
	for range 100 {
		data, _ := os.ReadFile(heard)
		lines = strings.Fields(string(data))
		if slices.Contains(lines, "cancel") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !slices.Equal(lines, []string{"start", "stop", "cancel"}) {
		t.Fatalf("heard %v", lines)
	}
}

func TestComputerWithNoPlaceForTheModelCantDictate(t *testing.T) {
	host := NewDictationHost(DictationHostOptions{Helper: DictationHelper{File: filepath.Join(t.TempDir(), "aic-dictation")}, Placeless: true, Microphone: func(context.Context) bool { return true }})
	var seen []string
	host.OnDictationModel(func(status contracts.DictationModelStatus) {
		seen = append(seen, string(status.Model)+": "+*status.Message)
	})
	if !slices.Equal(seen, []string{"unsupported: Dictation needs Linux or a Mac with Apple silicon."}) {
		t.Fatalf("got %v", seen)
	}
	expectError(t, host.StartDictation(context.Background(), "x"), "Linux or a Mac")
}

func TestHelperThatIsAScriptRunsThroughItsRuntime(t *testing.T) {
	ctx := context.Background()
	host, _ := setupDictation(t, dictationSetup{runtime: &DictationRuntime{Command: os.Args[0], Env: map[string]string{"FAKE_SAYS": "through the runtime"}}})
	if err := host.StartDictation(ctx, "hold-1"); err != nil {
		t.Fatal(err)
	}
	if text, err := host.StopDictation(ctx, "hold-1"); err != nil || text != "through the runtime" {
		t.Fatalf("got %q %v", text, err)
	}
}

func TestHelperThatStopsFailsWhatItHeld(t *testing.T) {
	host, _ := setupDictation(t, dictationSetup{})
	if _, err := host.request("crash", "c", time.Minute, nil); err == nil || err.Error() != "The dictation helper stopped." {
		t.Fatalf("got %v", err)
	}
	missing := NewDictationHost(DictationHostOptions{Helper: DictationHelper{File: "/nonexistent/aic-dictation"}, Placeless: true})
	if _, err := missing.request("stop", "x", time.Minute, nil); err == nil || !strings.HasPrefix(err.Error(), "The dictation helper could not run: ") {
		t.Fatalf("got %v", err)
	}
}

func TestLinuxRunsItsOwnHelperAndMacOSItsBuiltProgram(t *testing.T) {
	if helper := DictationHelperFor("/app/native", true, "linux"); helper.File != "/app/native/aic-dictation-linux" || helper.Runtime != nil {
		t.Fatalf("linux %v", helper)
	}
	if helper := DictationHelperFor("/app/native", true, "darwin"); helper.File != "/app/native/aic-dictation" {
		t.Fatalf("installed %v", helper)
	}
	if helper := DictationHelperFor("/app/native", false, "darwin"); helper.File != "/app/native/aic-dictation-dev" {
		t.Fatalf("source %v", helper)
	}
}
