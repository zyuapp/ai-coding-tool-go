package computers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

const (
	statusTimeout = 10 * time.Second
	// A probe crosses the tailnet once; a peer that takes longer than this is not serving.
	probeTimeout = 3 * time.Second
	// The bridge's private probe, which proves Tailscale Serve reaches this app.
	mobileHealthPath     = domain.MobileAppPath + "/health"
	mobileHealthResponse = "aicodingtool-mobile-v1"
)

// ServesApp is whether a peer answers as this app's own bridge.
func ServesApp(ctx context.Context, host string) bool {
	return servesApp(ctx, http.DefaultClient, host)
}

func servesApp(ctx context.Context, client *http.Client, host string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+mobileHealthPath, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	return err == nil && response.StatusCode >= 200 && response.StatusCode < 300 && string(body) == mobileHealthResponse
}

// TailnetPeer is a machine on the tailnet. ID is Tailscale's stable node ID, which outlives a renamed host.
type TailnetPeer struct {
	ID     string
	Host   string
	Name   string
	OS     string
	Online bool
}

func peersOf(status map[string]any) []TailnetPeer {
	peers, ok := status["Peer"].(map[string]any)
	if !ok {
		return []TailnetPeer{}
	}
	// Object.values follows insertion order; the decoded map has none, so the keys are walked sorted.
	keys := make([]string, 0, len(peers))
	for key := range peers {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	found := []TailnetPeer{}
	for _, key := range keys {
		record, _ := peers[key].(map[string]any)
		dnsName, _ := record["DNSName"].(string)
		host := strings.TrimSuffix(dnsName, ".")
		if host == "" {
			continue
		}
		id, _ := record["ID"].(string)
		name, _ := record["HostName"].(string)
		if name == "" {
			name = host
		}
		system, _ := record["OS"].(string)
		online, _ := record["Online"].(bool)
		found = append(found, TailnetPeer{ID: id, Host: host, Name: name, OS: system, Online: online})
	}
	return found
}

// knownTailscalePaths are CLI entry points used when the search path does not contain Tailscale.
var knownTailscalePaths = []string{
	"/usr/local/bin/tailscale",
	"/usr/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/tailscale",
}

var tailscaleBinary struct {
	sync.Mutex
	path string
}

func executable(file string) bool {
	info, err := os.Stat(file)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// findTailscale is the command, or "" when this machine has no Tailscale. Remembered, because the
// answer rarely moves.
func findTailscale() string {
	tailscaleBinary.Lock()
	defer tailscaleBinary.Unlock()
	if tailscaleBinary.path != "" && executable(tailscaleBinary.path) {
		return tailscaleBinary.path
	}
	tailscaleBinary.path = ""
	candidates := []string{}
	if found, err := platform.LookPath("tailscale"); err == nil && filepath.IsAbs(found) {
		candidates = append(candidates, found)
	}
	for _, known := range knownTailscalePaths {
		if !slices.Contains(candidates, known) {
			candidates = append(candidates, known)
		}
	}
	for _, candidate := range candidates {
		if executable(candidate) {
			tailscaleBinary.path = candidate
			return candidate
		}
	}
	return ""
}

// parseTailscaleJSON reads Tailscale's JSON, which can report a startup failure on stdout with a
// successful exit code.
func parseTailscaleJSON(output []byte) (any, error) {
	var parsed any
	err := jsonx.Unmarshal(output, &parsed)
	if err == nil {
		return parsed, nil
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t\r")
		if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
			return nil, errors.New(strings.TrimSpace(line))
		}
		break
	}
	return nil, err
}

// TailnetPeers is every other machine on this tailnet, as Tailscale reports it right now.
func TailnetPeers(ctx context.Context) ([]TailnetPeer, error) {
	binary := findTailscale()
	if binary == "" {
		return nil, errors.New("Tailscale is not installed on this computer.")
	}
	stdout, _, err := platform.Run(ctx, binary, []string{"status", "--json"}, platform.RunOptions{Timeout: statusTimeout, MaxBuffer: 4 * 1024 * 1024})
	if err != nil {
		return nil, err
	}
	parsed, err := parseTailscaleJSON(stdout)
	if err != nil {
		return nil, err
	}
	status, _ := parsed.(map[string]any)
	return peersOf(status), nil
}

// DiscoverOptions replace how the tailnet is read and how a peer is asked; nil fields keep the real ones.
type DiscoverOptions struct {
	Peers func(ctx context.Context) ([]TailnetPeer, error)
	Probe func(ctx context.Context, host string) bool
}

// DiscoverComputers lists the computers on this tailnet running this app: every peer Tailscale says
// is online, asked in parallel whether it serves the bridge. A peer that is not, or is off, is not offered.
func DiscoverComputers(ctx context.Context, options DiscoverOptions) ([]*domain.DiscoveredComputer, error) {
	peers := options.Peers
	if peers == nil {
		peers = TailnetPeers
	}
	probe := options.Probe
	if probe == nil {
		probe = ServesApp
	}
	all, err := peers(ctx)
	if err != nil {
		return nil, err
	}
	online := slices.DeleteFunc(slices.Clone(all), func(peer TailnetPeer) bool { return !peer.Online })
	serving := make([]bool, len(online))
	var group sync.WaitGroup
	for index, peer := range online {
		group.Go(func() { serving[index] = probe(ctx, peer.Host) })
	}
	group.Wait()
	found := []*domain.DiscoveredComputer{}
	for index, peer := range online {
		if serving[index] {
			found = append(found, &domain.DiscoveredComputer{Host: peer.Host, Name: peer.Name, OS: peer.OS})
		}
	}
	slices.SortStableFunc(found, func(left, right *domain.DiscoveredComputer) int { return localeCompare(left.Name, right.Name) })
	return found, nil
}

var collator = struct {
	sync.Mutex
	*collate.Collator
}{Collator: collate.New(language.Und)}

// localeCompare approximates String.prototype.localeCompare with the root collation.
func localeCompare(left, right string) int {
	collator.Lock()
	defer collator.Unlock()
	return collator.CompareString(left, right)
}
