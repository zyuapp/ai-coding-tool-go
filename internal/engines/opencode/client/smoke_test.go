package client

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestSmokeRealOpenCode starts the installed OpenCode when OPENCODE_SMOKE=1, reads its models and
// first event, and stops it.
func TestSmokeRealOpenCode(t *testing.T) {
	if os.Getenv("OPENCODE_SMOKE") == "" {
		t.Skip("set OPENCODE_SMOKE=1 to run against the installed opencode")
	}
	executable, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server, err := Start(ctx, Options{Cwd: t.TempDir(), Executable: executable})
	if err != nil {
		t.Fatal(err)
	}
	pid := server.cmd.Process.Pid
	events := make(chan Event, 16)
	server.Subscribe(func(event Event) { events <- event })
	models, err := RequestAs[[]map[string]jsontext.Value](ctx, server, "GET", "/api/model", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %d models", server.URL(), len(models))
	select {
	case event := <-events:
		t.Logf("first event: %s", event.Type)
	case <-time.After(10 * time.Second):
		t.Error("no event")
	}
	server.Close()
	t.Logf("exit: %+v (shell %d)", server.Exit(), pid)
}
