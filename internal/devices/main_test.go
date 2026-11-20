package devices

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// testRole makes the test binary stand in for a helper program when a test starts it.
const testRole = "AIC_DEVICES_TEST_ROLE"

func TestMain(m *testing.M) {
	switch os.Getenv(testRole) {
	case "dictation-helper":
		fakeDictationHelper()
		os.Exit(0)
	case "mcp-driver":
		fakeMCPDriver()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func say(message map[string]any) {
	line, _ := jsonx.Marshal(message)
	fmt.Println(string(line))
}

// fakeDictationHelper answers like the real one: it hears "said" on start and writes it down on stop.
// Each request is noted in heard.log beside the models folder.
func fakeDictationHelper() {
	models := ""
	for index, arg := range os.Args {
		if arg == "--models" && index+1 < len(os.Args) {
			models = os.Args[index+1]
		}
	}
	heard := filepath.Join(filepath.Dir(models), "heard.log")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request map[string]string
		if jsonx.Unmarshal(scanner.Bytes(), &request) != nil {
			continue
		}
		file, err := os.OpenFile(heard, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintln(file, request["op"])
			file.Close()
		}
		id := request["id"]
		switch request["op"] {
		case "start":
			say(map[string]any{"type": "level", "id": id, "value": 0.5})
			say(map[string]any{"type": "started", "id": id})
		case "stop":
			text, ok := os.LookupEnv("FAKE_SAYS")
			if !ok {
				text = "said"
			}
			say(map[string]any{"type": "text", "id": id, "text": text})
		case "cancel":
			say(map[string]any{"type": "cancelled", "id": id})
		case "transcribe":
			text := ""
			if _, err := os.Stat(request["path"]); err == nil {
				text = "from file"
			}
			say(map[string]any{"type": "text", "id": id, "text": text})
		case "download":
			if os.Getenv("SLOW_DOWNLOAD") != "" {
				say(map[string]any{"type": "model", "state": "downloading", "progress": 0.1})
			} else {
				say(map[string]any{"type": "model", "state": "downloading", "progress": 0.5})
				say(map[string]any{"type": "model", "state": "downloading"})
				say(map[string]any{"type": "model", "state": "ready"})
			}
		case "crash":
			os.Exit(1)
		}
	}
}

// fakeMCPDriver serves `get_window_state` from FAKE_SNAPSHOTS, one per call (the last repeats), after
// FAKE_DELAY_MS each, and writes its environment's Wayland variables to FAKE_ENV_FILE.
func fakeMCPDriver() {
	if file := os.Getenv("FAKE_ENV_FILE"); file != "" {
		nest, hasNest := os.LookupEnv("CUA_WAYLAND_NEST")
		_ = os.WriteFile(file, []byte(fmt.Sprintf("enable=%s nest=%s/%v", os.Getenv("CUA_DRIVER_RS_ENABLE_WAYLAND"), nest, hasNest)), 0o600)
	}
	if os.Getenv("FAKE_EXIT") != "" {
		return
	}
	var snapshots []string
	_ = jsonx.Unmarshal([]byte(os.Getenv("FAKE_SNAPSHOTS")), &snapshots)
	delay, _ := time.ParseDuration(os.Getenv("FAKE_DELAY"))
	calls := 0
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if jsonx.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		switch request.Method {
		case "initialize":
			say(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-06-18"}})
		case "tools/call":
			time.Sleep(delay)
			snapshot := ""
			if len(snapshots) > 0 {
				snapshot = snapshots[min(calls, len(snapshots)-1)]
			}
			calls++
			if strings.HasPrefix(snapshot, "!") {
				fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(request.ID) + `,"result":{"isError":true}}`)
				continue
			}
			fmt.Println(`{"jsonrpc":"2.0","id":` + fmt.Sprint(request.ID) + `,"result":{"isError":false,"structuredContent":` + snapshot + `}}`)
		}
	}
}
