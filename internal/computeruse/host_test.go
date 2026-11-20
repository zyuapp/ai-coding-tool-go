package computeruse

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// The test binary stands in for cua-driver when FAKE_CUA_DRIVER names what it should do.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_CUA_DRIVER") {
	case "serve":
		fakeServe()
		return
	case "fail":
		fmt.Fprintln(os.Stderr, "Cua Driver could not open the display.")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func fakeServe() {
	args := os.Args
	index := slices.Index(args, "--socket")
	if index < 0 || !slices.Contains(args, "--parent-liveness-stdio") || os.Getenv("CUA_DRIVER_EMBEDDED_HOST_PID") == "" {
		os.Exit(2)
	}
	listener, err := net.Listen("unix", args[index+1])
	if err != nil {
		os.Exit(4)
	}
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			connection.Close()
		}
	}()
	_, _ = io.Copy(io.Discard, os.Stdin)
	listener.Close()
}

// fakeDriver is a script that runs this test binary as the driver.
func fakeDriver(t *testing.T, mode string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "cua-driver")
	body := fmt.Sprintf("#!/bin/sh\nFAKE_CUA_DRIVER=%s exec '%s' \"$@\"\n", mode, executable)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestLinuxPermissionsDescribeTheSession(t *testing.T) {
	env := func(values map[string]string) platform.Env { return func(key string) string { return values[key] } }
	x11 := linuxPermissions(platform.ComputerUseCapability(env(map[string]string{"DISPLAY": ":99"})), true)
	if platform.OS != "linux" {
		x11 = linuxPermissions(platform.DesktopCapability{Status: "available", Display: platform.DisplayX11}, true)
	}
	want := &domain.ComputerUsePermissions{Accessibility: true, ScreenRecording: true, LinuxRuntime: &domain.ComputerUsePermissionsLinuxRuntime{
		Status: "available", Display: "x11", Message: "Computer use is ready for this X11 session.",
	}}
	if !domain.SameComputerUsePermissions(*x11, *want) {
		t.Fatalf("%+v", x11.LinuxRuntime)
	}
	xwayland := linuxPermissions(platform.DesktopCapability{Status: "available", Display: platform.DisplayXWayland}, true)
	if xwayland.LinuxRuntime.Status != "limited" || !strings.HasPrefix(xwayland.LinuxRuntime.Message, "X11 and XWayland apps are available.") {
		t.Fatalf("%+v", xwayland.LinuxRuntime)
	}
	missing := linuxPermissions(platform.DesktopCapability{Status: "available", Display: platform.DisplayX11}, false)
	if missing.Accessibility || missing.LinuxRuntime.Status != "unavailable" || missing.LinuxRuntime.Message != missingDriver {
		t.Fatalf("%+v", missing.LinuxRuntime)
	}
	none := linuxPermissions(platform.DesktopCapability{Status: "unsupported", Display: platform.DisplayNone, Message: "no display"}, true)
	if none.ScreenRecording || none.LinuxRuntime.Display != "none" || none.LinuxRuntime.Message != "no display" {
		t.Fatalf("%+v", none.LinuxRuntime)
	}
}

func TestTheDaemonStartsOnceAndStopsWithTheApp(t *testing.T) {
	computerUse := NewHost(fakeDriver(t, "serve"))
	ctx := context.Background()
	socket, err := computerUse.ensureDaemon(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(socket), fmt.Sprintf("cua-%d-", os.Getpid())) {
		t.Fatal(socket)
	}
	again, err := computerUse.ensureDaemon(ctx)
	if err != nil || again != socket {
		t.Fatal("a running daemon is reused")
	}
	running := computerUse.daemon
	if err := computerUse.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the liveness pipe ends the daemon")
	}
	if config, _ := computerUse.ForRun(ctx); config != (domain.ComputerUseRunConfigUnavailable{Message: "AI Coding Tool is quitting."}) {
		t.Fatalf("%+v", config)
	}
	computerUse.Resume()
	if _, err := computerUse.ensureDaemon(ctx); err != nil {
		t.Fatal("a cancelled quit lets the next run start a fresh daemon")
	}
	_ = computerUse.Stop(ctx)
}

func TestADaemonThatCannotStartSaysWhy(t *testing.T) {
	computerUse := NewHost(fakeDriver(t, "fail"))
	_, err := computerUse.ensureDaemon(context.Background())
	if err == nil || err.Error() != "Cua Driver could not open the display." {
		t.Fatal(err)
	}
}

func TestLinuxComputerUseNeedsNoMacPermissionsAndStartsTheBundledHost(t *testing.T) {
	if platform.OS != "linux" {
		t.Skip("Linux runs need no grants")
	}
	binary := fakeDriver(t, "serve")
	computerUse := NewHost(binary)
	computerUse.env = func(key string) string {
		if key == "DISPLAY" {
			return ":99"
		}
		return ""
	}
	ctx := context.Background()
	defer computerUse.Stop(ctx)
	config, err := computerUse.ForRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	available, ok := config.(domain.ComputerUseRunConfigAvailable)
	if !ok || available.MCP.Command != binary || available.MCP.Args[0] != "mcp" || len(available.MCP.Env) != 0 {
		t.Fatalf("%+v", config)
	}
}

type fakePublisher struct {
	revision float64
	answers  []application.WorkspaceResponseResult
}

func (p *fakePublisher) Revision() float64                       { return p.revision }
func (p *fakePublisher) Snapshot() application.WorkspaceUpdate   { return application.WorkspaceUpdate{} }
func (p *fakePublisher) Subscribe(host.RuntimeSubscriber) func() { return func() {} }
func (p *fakePublisher) Request(context.Context, application.WorkspaceInput) application.WorkspaceResponseResult {
	answer := p.answers[0]
	p.answers = p.answers[1:]
	return answer
}

func TestTheBridgeRunsInputsInOrderAndStopsAtTheFirstRefusal(t *testing.T) {
	task := "task-1"
	publisher := &fakePublisher{revision: 3, answers: []application.WorkspaceResponseResult{
		contracts.ComputerServerMessageResultResultTrue{TaskID: &task, Revision: 4},
		contracts.ComputerServerMessageResultResultTrue{Revision: 5},
	}}
	bridge := CreateComputerBridge(BridgeHost{Publisher: publisher, Name: func() string { return "desk" }})
	if empty := bridge.Input(context.Background(), nil); empty != (contracts.ComputerServerMessageResultResultTrue{Revision: 3}) {
		t.Fatalf("%+v", empty)
	}
	got := bridge.Input(context.Background(), []application.WorkspaceInput{nil, nil}).(contracts.ComputerServerMessageResultResultTrue)
	if got.Revision != 5 || got.TaskID == nil || *got.TaskID != task {
		t.Fatalf("%+v", got)
	}
	refused := contracts.ComputerServerMessageResultResultFalse{Message: "no", Revision: 6}
	publisher.answers = []application.WorkspaceResponseResult{refused, contracts.ComputerServerMessageResultResultTrue{Revision: 7}}
	if answer := bridge.Input(context.Background(), []application.WorkspaceInput{nil, nil}); answer != refused || len(publisher.answers) != 1 {
		t.Fatalf("%+v", answer)
	}
}

// CUA_DRIVER_BINARY names a real cua-driver to start, e.g. ../ai-coding-tool/vendor/cua-driver/cua-driver.
func TestTheRealDriverStartsAndStops(t *testing.T) {
	binary := os.Getenv("CUA_DRIVER_BINARY")
	if binary == "" {
		t.Skip("CUA_DRIVER_BINARY is not set")
	}
	computerUse := NewHost(binary)
	ctx := context.Background()
	socket, err := computerUse.ensureDaemon(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proxy := exec.Command(binary, "mcp", "--embedded", "--socket", socket, "--host-bundle-id", BundleID)
	proxy.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}` + "\n")
	output, _ := proxy.Output()
	if !strings.Contains(string(output), `"id":1`) || !strings.Contains(string(output), `"result"`) {
		t.Fatalf("the MCP proxy answers through the daemon: %s", output)
	}
	running := computerUse.daemon
	if err := computerUse.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running.exited:
	default:
		t.Fatal("the daemon outlived Stop")
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("the socket is removed")
	}
}
