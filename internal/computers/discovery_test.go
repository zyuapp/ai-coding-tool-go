package computers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func TestDiscoveryOffersOnlyOnlinePeersServingTheApp(t *testing.T) {
	parsed, err := parseTailscaleJSON([]byte(`{"Peer":{"k1":{"ID":"n1","DNSName":"zed.tail.ts.net.","HostName":"zed","OS":"linux","Online":true},"k2":{"ID":"n2","DNSName":"mini.tail.ts.net.","HostName":"","OS":"macOS","Online":true},"k3":{"DNSName":"off.tail.ts.net.","Online":false},"k4":{"HostName":"nameless"},"k5":{"ID":"n5","DNSName":"Alpha.tail.ts.net.","HostName":"alpha","Online":true},"k6":{"ID":"n6","DNSName":"ghost.tail.ts.net.","HostName":"ghost","Online":true}}}`))
	if err != nil {
		t.Fatal(err)
	}
	peers := peersOf(parsed.(map[string]any))
	if len(peers) != 5 || peers[1].Name != "mini.tail.ts.net" {
		t.Fatalf("%#v", peers)
	}
	found, err := DiscoverComputers(context.Background(), DiscoverOptions{
		Peers: func(context.Context) ([]TailnetPeer, error) { return peers, nil },
		Probe: func(_ context.Context, host string) bool { return host != "ghost.tail.ts.net" },
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []*domain.DiscoveredComputer{{Host: "Alpha.tail.ts.net", Name: "alpha", OS: ""}, {Host: "mini.tail.ts.net", Name: "mini.tail.ts.net", OS: "macOS"}, {Host: "zed.tail.ts.net", Name: "zed", OS: "linux"}}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("%v", found)
	}
	if _, err := DiscoverComputers(context.Background(), DiscoverOptions{Peers: func(context.Context) ([]TailnetPeer, error) {
		return nil, errors.New("Tailscale is not installed on this computer.")
	}}); err == nil {
		t.Fatal("no Tailscale is an error")
	}
	if _, err := parseTailscaleJSON([]byte("Tailscale is stopped.\n")); err == nil || err.Error() != "Tailscale is stopped." {
		t.Fatal(err)
	}
}

func TestAPeerServesTheAppOnlyWhenItsHealthCheckSaysSo(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == mobileHealthPath {
			_, _ = w.Write([]byte(mobileHealthResponse))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "https://")
	if !servesApp(context.Background(), server.Client(), host) {
		t.Fatal("the app's own bridge")
	}
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }))
	defer other.Close()
	if servesApp(context.Background(), other.Client(), strings.TrimPrefix(other.URL, "https://")) {
		t.Fatal("some other server")
	}
}
