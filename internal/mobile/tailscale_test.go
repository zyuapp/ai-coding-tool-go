package mobile

import (
	"context"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

func TestThePathCommandIsPreferredOverKnownCLIEntryPoints(t *testing.T) {
	want := []string{"/custom/bin/tailscale", "/usr/local/bin/tailscale", "/usr/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/tailscale"}
	if got := TailscaleCommandCandidates("/custom/bin/tailscale"); !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
	if got := TailscaleCommandCandidates("/usr/local/bin/tailscale"); !slices.Equal(got, want[1:]) {
		t.Fatalf("a known command was tried twice: %v", got)
	}
}

func TestTailscaleJSONIsParsedAndPlainTextIsReported(t *testing.T) {
	value, err := ParseTailscaleJSON(`{"BackendState":"Running"}`)
	if err != nil || value.(map[string]any)["BackendState"] != "Running" {
		t.Fatalf("parsed %v, %v", value, err)
	}
	_, err = ParseTailscaleJSON("The Tailscale CLI failed to start: Failed to load preferences.\n")
	if err == nil || err.Error() != "The Tailscale CLI failed to start: Failed to load preferences." {
		t.Fatalf("error %v", err)
	}
}

func answering(body string, instance *string) HealthRequest {
	return func(_ context.Context, url string) (*http.Response, error) {
		header := http.Header{}
		if instance != nil {
			header.Set(MobileInstanceHeader, *instance)
		}
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
}

func TestTheSavedTailnetNameProvesTailscaleStillReachesThisServer(t *testing.T) {
	asked := ""
	request := func(ctx context.Context, url string) (*http.Response, error) {
		asked = url
		return answering(MobileHealthResponse, &MobileInstance)(ctx, url)
	}
	if !ReachesMobileServer(context.Background(), "mac.tail1234.ts.net", request) || asked != "https://mac.tail1234.ts.net/m/health" {
		t.Fatalf("asked %q", asked)
	}
	other := "someone-else"
	if ReachesMobileServer(context.Background(), "mac.tail1234.ts.net", answering(MobileHealthResponse, &other)) {
		t.Fatal("another copy behind the same name is this server")
	}
	if ReachesMobileServer(context.Background(), "mac.tail1234.ts.net", answering(MobileHealthResponse, nil)) {
		t.Fatal("a copy from before the header is this one")
	}
	if ReachesMobileServer(context.Background(), "mac.tail1234.ts.net", answering("not this app", &MobileInstance)) {
		t.Fatal("another service satisfied the probe")
	}
}

func TestOnlyThe443HandlersThatProxyToALocalPortAreRead(t *testing.T) {
	config, _ := ParseTailscaleJSON(`{
		"TCP": {"443": {"HTTPS": true}},
		"Web": {
			"mac.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7737"}, "/docs": {"Path": "/var/www"}}},
			"mac.tail1234.ts.net:8443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}},
			"other.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://localhost:51234/"}}}
		}
	}`)
	if got := ServedPorts(config); !slices.Equal(got, []int{7737, 51234}) {
		t.Fatalf("got %v", got)
	}
	empty, _ := ParseTailscaleJSON(`{}`)
	if len(ServedPorts(empty)) != 0 || len(ServedPorts(nil)) != 0 {
		t.Fatal("nothing served something")
	}
}

func TestServeIsOnlyOursWhenAHandlerPointsAtThisVeryPort(t *testing.T) {
	config, _ := ParseTailscaleJSON(`{"Web": {
		"mac.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:7737"}}},
		"other.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:3000"}}}
	}}`)
	if !ServesPort(config, 7737) || !ServesPort(config, 3000) || ServesPort(config, 9999) {
		t.Fatal("served ports misread")
	}
	empty, _ := ParseTailscaleJSON(`{}`)
	static, _ := ParseTailscaleJSON(`{"Web": {"x:443": {"Handlers": {"/": {"Path": "/var/www"}}}}}`)
	if ServesPort(empty, 7737) || ServesPort(nil, 7737) || ServesPort(static, 7737) {
		t.Fatal("an unused Tailscale serves nothing")
	}
}

// healthServer answers the health probe the way some copy of the app would.
func healthServer(t *testing.T, instance *string, body string) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if instance != nil {
			response.Header().Set(MobileInstanceHeader, *instance)
		}
		_, _ = io.WriteString(response, body)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func TestAPortAnsweringAsAnotherCopyOfTheAppIsThatCopy(t *testing.T) {
	ctx := context.Background()
	other := "someone-else"
	if !AnotherInstanceListens(ctx, healthServer(t, &other, MobileHealthResponse), nil) {
		t.Fatal("another copy was not seen")
	}
	if !AnotherInstanceListens(ctx, healthServer(t, nil, MobileHealthResponse), nil) {
		t.Fatal("an older copy without the header still counts")
	}
	if AnotherInstanceListens(ctx, healthServer(t, &MobileInstance, MobileHealthResponse), nil) {
		t.Fatal("this process is not another copy")
	}
	if AnotherInstanceListens(ctx, healthServer(t, &other, "a web app"), nil) {
		t.Fatal("something else entirely is not a copy")
	}
}

func TestA443HeldByALiveCopyIsLeftAlone(t *testing.T) {
	ctx := context.Background()
	other := "someone-else"
	live := healthServer(t, &other, MobileHealthResponse)
	served := func(port int) any {
		config, _ := ParseTailscaleJSON(`{"Web": {"mac.tail1234.ts.net:443": {"Handlers": {"/": {"Proxy": "http://127.0.0.1:` + strconv.Itoa(port) + `"}}}}}`)
		return config
	}
	if !HeldByAnotherInstance(ctx, served(live), 7737, nil) {
		t.Fatal("a live copy's 443 was taken")
	}
	if HeldByAnotherInstance(ctx, served(7737), 7737, nil) {
		t.Fatal("our own port is ours to keep")
	}
	gone, _ := net.Listen("tcp", "127.0.0.1:0")
	port := gone.Addr().(*net.TCPAddr).Port
	_ = gone.Close()
	if HeldByAnotherInstance(ctx, served(port), 7737, nil) {
		t.Fatal("a handler to a port nothing answers on is free to take")
	}
}

func TestLinuxRefusingANonOperatorSaysWhatToRun(t *testing.T) {
	refused := &platform.RunError{Stderr: []byte("sending serve config: Access denied: serve config denied\n\nTo not require root, use 'sudo tailscale set --operator=$USER' once.\n")}
	if got := ServeFailure(refused); got != OperatorNeeded {
		t.Fatalf("got %q", got)
	}
	if got := ServeFailure(&platform.RunError{Stderr: []byte("Error: timed out\n")}); got != "timed out" {
		t.Fatalf("got %q", got)
	}
}
