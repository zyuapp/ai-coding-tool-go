// Command build builds the native helpers into native/bin, the folder a source build reads them from
// (devices.NativeDir). Run it from anywhere in the checkout: `go run ./native/build`.
//
// On macOS: aic-simulator (Xcode's clang), and on Apple silicon aic-dictation and aic-dictation-dev
// (Swift). On Linux: aic-dictation-linux and the ONNX Runtime library it loads. -onnx also builds the
// Linux helper and fetches ONNX Runtime on macOS, to run it there. A helper newer than its sources is
// left alone; -force rebuilds it.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// onnxRuntime is the NuGet package of ONNX Runtime the Linux helper loads, pinned by checksum. Its
// C API is the version github.com/yalue/onnxruntime_go is built against.
const (
	onnxRuntimeURL    = "https://www.nuget.org/api/v2/package/Microsoft.ML.OnnxRuntime/1.31.1"
	onnxRuntimeSHA256 = "6871ccbc97f3d5bdb4095a1dcec33aa78d56f6ed8de5980ef5fa1d9f2e7c679a"
)

var simulatorFrameworks = []string{"Foundation", "AppKit", "CoreAudio", "CoreImage", "CoreMedia", "CoreVideo", "ImageIO", "IOSurface", "VideoToolbox"}

func main() {
	force := flag.Bool("force", false, "rebuild every helper")
	onnx := flag.Bool("onnx", false, "also build the Linux dictation helper and fetch ONNX Runtime on macOS")
	flag.Parse()
	native := nativeDir()
	out := filepath.Join(native, "bin")
	if err := os.MkdirAll(out, 0o755); err != nil {
		fail(err)
	}
	b := builder{native: native, out: out, force: *force}
	switch runtime.GOOS {
	case "darwin":
		b.simulator()
		b.dictation()
		if *onnx {
			b.linuxDictation()
		}
	case "linux":
		b.linuxDictation()
	default:
		fmt.Println("No native helpers to build on", runtime.GOOS)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// nativeDir is native/ in the module go reports for the working directory.
func nativeDir() string {
	module, err := exec.Command("go", "env", "GOMOD").Output()
	if err == nil && strings.TrimSpace(string(module)) != "" && strings.TrimSpace(string(module)) != os.DevNull {
		return filepath.Join(filepath.Dir(strings.TrimSpace(string(module))), "native")
	}
	fail(fmt.Errorf("run this inside the checkout: go run ./native/build"))
	return ""
}

type builder struct {
	native, out string
	force       bool
}

// newest is the latest change to any file under paths.
func newest(paths ...string) time.Time {
	var latest time.Time
	for _, path := range paths {
		_ = filepath.WalkDir(path, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() && entry.Name() == ".build" {
				return filepath.SkipDir
			}
			if info, err := entry.Info(); err == nil && !entry.IsDir() && info.ModTime().After(latest) {
				latest = info.ModTime()
			}
			return nil
		})
	}
	return latest
}

// fresh is whether output is newer than every source.
func (b builder) fresh(output string, sources ...string) bool {
	if b.force {
		return false
	}
	info, err := os.Stat(output)
	return err == nil && !info.ModTime().Before(newest(sources...))
}

func run(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func xcrun(args ...string) (string, error) {
	output, err := exec.Command("xcrun", append([]string{"--sdk", "macosx"}, args...)...).Output()
	return strings.TrimSpace(string(output)), err
}

// simulator builds the simulator panel's helper. Without Xcode there is nothing to build.
func (b builder) simulator() {
	source := filepath.Join(b.native, "simulator", "aic-simulator.m")
	output := filepath.Join(b.out, "aic-simulator")
	if b.fresh(output, source) {
		return
	}
	clang, err := xcrun("-f", "clang")
	if err != nil {
		fmt.Println("Skipping the simulator helper: Xcode is not installed.")
		return
	}
	sdk, err := xcrun("--show-sdk-path")
	if err != nil {
		fmt.Println("Skipping the simulator helper: Xcode is not installed.")
		return
	}
	args := []string{"-isysroot", sdk, "-arch", "arm64", "-mmacosx-version-min=13.0", "-fobjc-arc", "-O2", "-Wall", "-Werror"}
	for _, framework := range simulatorFrameworks {
		args = append(args, "-framework", framework)
	}
	if err := run(clang, append(args, source, "-o", output)...); err != nil {
		fail(err)
	}
	fmt.Println("Built", output)
}

func copyFile(from, to string, mode os.FileMode) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	staged := to + ".tmp"
	if err := os.WriteFile(staged, data, mode); err != nil {
		return err
	}
	return os.Rename(staged, to)
}

// dictation builds the macOS dictation helper on Apple silicon; the source build runs it as aic-dictation-dev.
func (b builder) dictation() {
	if runtime.GOARCH != "arm64" {
		return
	}
	pkg := filepath.Join(b.native, "dictation")
	output := filepath.Join(b.out, "aic-dictation")
	dev := filepath.Join(b.out, "aic-dictation-dev")
	if b.fresh(output, filepath.Join(pkg, "Sources"), filepath.Join(pkg, "Package.swift")) {
		if _, err := os.Stat(dev); err != nil {
			if err := copyFile(output, dev, 0o755); err != nil {
				fail(err)
			}
		}
		return
	}
	swift, err := exec.Command("xcrun", "-f", "swift").Output()
	if err != nil {
		fmt.Println("Skipping the dictation helper: Xcode is not installed.")
		return
	}
	tool := strings.TrimSpace(string(swift))
	if err := run(tool, "build", "-c", "release", "--arch", "arm64", "--package-path", pkg); err != nil {
		fail(err)
	}
	bin, err := exec.Command(tool, "build", "-c", "release", "--arch", "arm64", "--package-path", pkg, "--show-bin-path").Output()
	if err != nil {
		fail(err)
	}
	if err := copyFile(filepath.Join(strings.TrimSpace(string(bin)), "aic-dictation"), output, 0o755); err != nil {
		fail(err)
	}
	if err := copyFile(output, dev, 0o755); err != nil {
		fail(err)
	}
	fmt.Println("Built", output, "and", dev)
}

// linuxDictation builds the Go dictation helper for this machine and puts ONNX Runtime beside it.
func (b builder) linuxDictation() {
	output := filepath.Join(b.out, "aic-dictation-linux")
	if !b.fresh(output, filepath.Join(b.native, "dictation-linux"), filepath.Join(b.native, "..", "internal", "devices", "speech")) {
		command := exec.Command("go", "build", "-o", output, "./native/dictation-linux")
		command.Dir = filepath.Join(b.native, "..")
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			fail(err)
		}
		fmt.Println("Built", output)
	}
	library, inside := onnxLibrary()
	if inside == "" {
		fmt.Println("No ONNX Runtime library for", runtime.GOOS, runtime.GOARCH)
		return
	}
	target := filepath.Join(b.out, library)
	if _, err := os.Stat(target); err == nil && !b.force {
		return
	}
	if err := fetchOnnxRuntime(inside, target); err != nil {
		fail(err)
	}
	fmt.Println("Fetched", target)
}

// onnxLibrary is the library's name beside the helper, and its path inside the NuGet package.
func onnxLibrary() (string, string) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return "libonnxruntime.so", "runtimes/linux-x64/native/libonnxruntime.so"
	case "linux/arm64":
		return "libonnxruntime.so", "runtimes/linux-arm64/native/libonnxruntime.so"
	case "darwin/arm64":
		return "libonnxruntime.dylib", "runtimes/osx-arm64/native/libonnxruntime.dylib"
	}
	return "", ""
}

// fetchOnnxRuntime downloads the pinned package once into the user's cache, checks it, and copies the library out.
func fetchOnnxRuntime(inside, target string) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	folder := filepath.Join(cache, "ai-coding-tool-go", "native")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		return err
	}
	archive := filepath.Join(folder, "onnxruntime-"+onnxRuntimeSHA256[:12]+".nupkg")
	data, err := os.ReadFile(archive)
	if err != nil || checksum(data) != onnxRuntimeSHA256 {
		fmt.Println("Downloading ONNX Runtime...")
		response, err := http.Get(onnxRuntimeURL)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("ONNX Runtime download refused (%d)", response.StatusCode)
		}
		if data, err = io.ReadAll(response.Body); err != nil {
			return err
		}
		if checksum(data) != onnxRuntimeSHA256 {
			return fmt.Errorf("the ONNX Runtime package didn't match its checksum")
		}
		if err := os.WriteFile(archive, data, 0o600); err != nil {
			return err
		}
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		if file.Name != inside {
			continue
		}
		opened, err := file.Open()
		if err != nil {
			return err
		}
		defer opened.Close()
		library, err := io.ReadAll(opened)
		if err != nil {
			return err
		}
		staged := target + ".tmp"
		if err := os.WriteFile(staged, library, 0o644); err != nil {
			return err
		}
		return os.Rename(staged, target)
	}
	return fmt.Errorf("the ONNX Runtime package has no %s", inside)
}

func checksum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
