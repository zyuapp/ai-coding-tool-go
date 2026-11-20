package computers

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestSourceCheckoutCanServeTheInstalledAppsDataAndPairAgainstIt(t *testing.T) {
	zero := 0.0
	cases := []struct {
		argv []string
		want ServeArguments
	}{
		{[]string{"serve", "--dev", "--installed"}, ServeArguments{Command: "serve", Dev: true, Installed: true}},
		{[]string{"pair", "--dev", "--installed"}, ServeArguments{Command: "pair", Dev: true, Installed: true}},
		{[]string{"serve", "--dev", "--local", "--port", "0"}, ServeArguments{Command: "serve", Dev: true, Local: true, Port: &zero}},
	}
	for _, c := range cases {
		if got := ParseServeArguments(c.argv); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%v: %#v", c.argv, got)
		}
	}
	if got := ParseServeArguments([]string{"--user-data", "/tmp/x"}); got.UserData == nil || *got.UserData != "/tmp/x" {
		t.Fatal(got)
	}
}

func TestPairingTextIsTheLineAloneOffATerminal(t *testing.T) {
	now := application.Now
	t.Cleanup(func() { application.Now = now })
	application.Now = func() float64 { return 1_000_000 }
	offer := &domain.MobilePairingOffer{Code: "ATSWG1HE", ExpiresAt: 1_000_000 + 119_000, URL: "aicodingtool://pair?link=http%3A%2F%2F127.0.0.1%3A54270%2F%23pair%3DATSWG1HE"}
	line := "Pair with code ATSWG1HE (expires in 2 minutes) or open aicodingtool://pair?link=http%3A%2F%2F127.0.0.1%3A54270%2F%23pair%3DATSWG1HE"
	if got := PairingText(offer, false); got != line {
		t.Fatal(got)
	}
	drawn := PairingText(offer, true)
	if !strings.HasSuffix(drawn, "\n"+line) || !strings.Contains(drawn, "█") || !strings.HasPrefix(drawn, "\x1b[47m\x1b[30m") {
		t.Fatal(drawn)
	}
}

// electronServer runs the Electron app's own `aic serve` headless on the loopback.
func electronServer(t *testing.T, name string) (host, userData string) {
	t.Helper()
	checkout := electronCheckout(t)
	entry := filepath.Join(checkout, "dist", "main", "main", "serve.js")
	if _, err := os.Stat(entry); err != nil {
		t.Skip("needs ../ai-coding-tool built")
	}
	userData = t.TempDir()
	if err := os.WriteFile(filepath.Join(userData, ComputersFile), []byte(`{"version":1,"name":"`+name+`","computers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("node", "--disable-warning=ExperimentalWarning", entry, "serve", "--dev", "--local", "--user-data", userData, "--port", "0")
	stdout, _ := child.StdoutPipe()
	child.Stderr = child.Stdout
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Signal(syscall.SIGINT)
		done := make(chan struct{})
		go func() { _ = child.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = child.Process.Kill()
		}
	})
	listening := regexp.MustCompile(`Listening on port (\d+)\.`)
	ports := make(chan string, 1)
	var output strings.Builder
	var mu sync.Mutex
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			mu.Lock()
			output.WriteString(lines.Text() + "\n")
			mu.Unlock()
			if match := listening.FindStringSubmatch(lines.Text()); match != nil {
				ports <- match[1]
			}
		}
	}()
	select {
	case port := <-ports:
		return "127.0.0.1:" + port, userData
	case <-time.After(30 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("the served host never listened:\n%s", output.String())
	}
	return "", ""
}

func TestGoComputerPairsWithAHeadlessElectronHostByItsCode(t *testing.T) {
	const servedName = "Build box"
	host, userData := electronServer(t, servedName)
	links := NewComputerLinks(LinksOptions{File: filepath.Join(t.TempDir(), ComputersFile), DeviceName: "This Mac"})
	var mu sync.Mutex
	var states []*application.WorkspaceState
	links.options.OnState = func(_ string, state *application.WorkspaceState) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, state)
	}
	t.Cleanup(links.Stop)
	links.Start()

	err := links.Pair(background(), host, "linux-box", "WRONGCOD")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("a wrong code: %v", err)
	}
	offer, err := RequestPairing(background(), filepath.Join(userData, "serve.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{8}$`).MatchString(offer.Code) || !strings.HasPrefix(offer.URL, "aicodingtool://pair?link=http%3A%2F%2F127.0.0.1%3A") {
		t.Fatalf("offer %#v", offer)
	}
	if err := links.Pair(background(), host, "linux-box", offer.Code); err != nil {
		t.Fatal(err)
	}
	until(t, "the line coming up", func() bool { return links.Links()[0].Status == domain.ComputerStatusConnected })
	until(t, "the served host's own name replacing the one it was paired under", func() bool { return links.Links()[0].Name == servedName })
	until(t, "the served host's workspace arriving", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(states) > 0 && states[len(states)-1].Restored
	})
	mu.Lock()
	if threads := states[len(states)-1].Threads; len(threads) != 0 {
		t.Fatalf("a fresh host has no threads yet: %d", len(threads))
	}
	mu.Unlock()
	if len(links.Links()[0].Capabilities) == 0 {
		t.Fatal("the served host named no capabilities")
	}
	if err := links.Forget(links.Links()[0].ID); err != nil || len(links.Links()) != 0 {
		t.Fatal(err)
	}
}

func TestPairWithNothingServingSaysSo(t *testing.T) {
	_, err := RequestPairing(background(), filepath.Join(t.TempDir(), "serve.sock"))
	if err == nil {
		t.Fatal("answered with nothing serving")
	}
}
