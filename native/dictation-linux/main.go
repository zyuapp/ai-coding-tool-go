// Command aic-dictation-linux is holding to talk on Linux: it records the microphone and runs Parakeet
// through ONNX Runtime, whose shared library sits beside it. It speaks the macOS helper's lines on
// stdin and stdout (see speech.DictationHelper).
//
// Usage: aic-dictation-linux [--models folder] [--onnxruntime library]
package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/zyuapp/ai-coding-tool-go/internal/devices/speech"
	"github.com/zyuapp/ai-coding-tool-go/native/dictation-linux/parakeet"
)

func argument(name string) string {
	for index, value := range os.Args[1:] {
		if value == name && index+2 < len(os.Args) {
			return os.Args[index+2]
		}
	}
	return ""
}

// library is ONNX Runtime beside this program, unless named.
func library() string {
	if named := argument("--onnxruntime"); named != "" {
		return named
	}
	name := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		name = "libonnxruntime.dylib"
	}
	executable, err := os.Executable()
	if err != nil {
		return name
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	return filepath.Join(filepath.Dir(executable), name)
}

func main() {
	folder := argument("--models")
	if folder != "" {
		folder, _ = filepath.Abs(folder)
	} else if place := speech.DictationModelPlace(runtime.GOOS, runtime.GOARCH); place != nil {
		folder = place.Folder
	} else {
		fmt.Fprintln(os.Stderr, "This computer has no place for the speech model.")
		os.Exit(1)
	}
	lib := library()
	helper := speech.NewDictationHelper(os.Stdout, speech.HelperOptions{
		Folder: folder,
		Load: func(folder string, threads int) (speech.Transcriber, error) {
			if err := parakeet.Init(lib); err != nil {
				return nil, err
			}
			return parakeet.Load(folder, threads)
		},
	})
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-signals
		helper.Quit()
		os.Exit(0)
	}()
	helper.Serve(os.Stdin)
	helper.Quit()
}
