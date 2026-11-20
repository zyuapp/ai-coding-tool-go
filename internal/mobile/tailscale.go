package mobile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// tailscaleTimeout is long enough for a cold daemon to answer, short enough that settings never look frozen.
const tailscaleTimeout = 10 * time.Second

// tailscaleServeTimeout: Serve provisions a certificate on its first call, which is slow the way any certificate is.
const tailscaleServeTimeout = 90 * time.Second

// tailscaleHealthTimeout: the fallback is only a local trip through the tailnet, so a slow answer is not a healthy route.
const tailscaleHealthTimeout = 3 * time.Second

// localHealthTimeout: a port on this machine answers at once or not at all.
const localHealthTimeout = 1500 * time.Millisecond

// knownCLIPaths are the CLI entry points tried when the search path does not contain Tailscale.
var knownCLIPaths = []string{
	"/usr/local/bin/tailscale",
	"/usr/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/tailscale",
}

var cachedBinary struct {
	sync.Mutex
	path string
}

func executable(candidate string) bool {
	info, err := os.Stat(candidate)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

// TailscaleCommandCandidates is the search path's result first, followed by known CLI entry points without duplicates.
func TailscaleCommandCandidates(onPath string) []string {
	candidates := []string{}
	if onPath != "" {
		candidates = append(candidates, onPath)
	}
	for _, candidate := range knownCLIPaths {
		if !slices.Contains(candidates, candidate) {
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

// tailscaleOnPath is searched here rather than by `which`, which not every Linux installs.
func tailscaleOnPath() string {
	for _, directory := range filepath.SplitList(platform.SearchPath()) {
		if !filepath.IsAbs(directory) {
			continue
		}
		if candidate := filepath.Join(directory, "tailscale"); executable(candidate) {
			return candidate
		}
	}
	return ""
}

// FindTailscale is the command, or "" when this machine has no Tailscale. Remembered, because the answer rarely moves.
func FindTailscale() string {
	cachedBinary.Lock()
	defer cachedBinary.Unlock()
	if cachedBinary.path != "" && executable(cachedBinary.path) {
		return cachedBinary.path
	}
	cachedBinary.path = ""
	for _, candidate := range TailscaleCommandCandidates(tailscaleOnPath()) {
		if executable(candidate) {
			cachedBinary.path = candidate
			return candidate
		}
	}
	return ""
}

func stderrOf(err error) string {
	var failed *platform.RunError
	if errors.As(err, &failed) {
		return string(failed.Stderr)
	}
	return ""
}

func readable(err error) string {
	message := errorMessage(err)
	text := message
	if stderr := strings.TrimSpace(stderrOf(err)); stderr != "" {
		text = stderr
	}
	line := message
	for candidate := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = candidate
			break
		}
	}
	if line = errorPrefix.ReplaceAllString(strings.TrimSpace(line), ""); line != "" {
		return line
	}
	return "Tailscale could not be reached."
}

var errorPrefix = regexp.MustCompile(`(?i)^Error:\s*`)

// OperatorNeeded is the one refusal a user fixes with a command rather than a setting, so the command is what it says.
const OperatorNeeded = "Tailscale needs permission. Run: sudo tailscale set --operator=$USER"

var operatorRefusal = regexp.MustCompile(`(?i)access denied|--operator`)

// ServeFailure is what a failed serve says, with Linux's refusal of a user who is not Tailscale's operator named as such.
func ServeFailure(err error) string {
	if operatorRefusal.MatchString(stderrOf(err) + "\n" + errorMessage(err)) {
		return OperatorNeeded
	}
	return readable(err)
}

// tailscale runs one command. A stop cancels ctx, so a slow certificate never holds the app open.
func tailscale(ctx context.Context, binary string, args []string, timeout time.Duration) (string, error) {
	stdout, _, err := platform.Run(ctx, binary, args, platform.RunOptions{Timeout: timeout, MaxBuffer: 4 * 1024 * 1024})
	return string(stdout), err
}

// serveConfig is what Serve is set up to do. No config at all is what an unused Tailscale answers.
func serveConfig(ctx context.Context, binary string) (any, error) {
	output, err := tailscale(ctx, binary, []string{"serve", "status", "--json"}, tailscaleTimeout)
	if err != nil {
		return nil, err
	}
	if output == "" {
		output = "{}"
	}
	return ParseTailscaleJSON(output)
}

func record(value any) map[string]any {
	held, _ := value.(map[string]any)
	return held
}

// certDomains is whether the tailnet will issue this machine a certificate. A tailnet with HTTPS
// turned off answers with no domains at all, and `serve --https` against it hangs rather than
// refusing, so this is checked before serve is ever asked.
func certDomains(status any) []string {
	domains, _ := record(status)["CertDomains"].([]any)
	names := []string{}
	for _, domain := range domains {
		if name, ok := domain.(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// trimDNSName: MagicDNS names come back with the root dot the DNS protocol writes; a URL wants it gone.
func trimDNSName(name any) *string {
	text, ok := name.(string)
	if !ok || utf16Length(text) <= 1 {
		return nil
	}
	trimmed := strings.TrimSuffix(text, ".")
	return &trimmed
}

func backendStatus(state any) domain.TailscaleStateStatus {
	if state == "Running" {
		return domain.TailscaleStateStatusReady
	}
	return domain.TailscaleStateStatusLoggedOut
}

// ParseTailscaleJSON reads Tailscale's JSON. Tailscale can report a startup failure on stdout with a
// successful exit code, which is reported as its own words rather than as a parse failure.
func ParseTailscaleJSON(output string) (any, error) {
	var value any
	err := jsonx.Unmarshal([]byte(output), &value)
	if err == nil {
		return value, nil
	}
	for line := range strings.SplitSeq(strings.TrimSpace(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		start := strings.TrimLeft(line, " \t\r")
		if !strings.HasPrefix(start, "{") && !strings.HasPrefix(start, "[") {
			return nil, errors.New(strings.TrimSpace(line))
		}
		break
	}
	return nil, err
}

// HealthRequest is a GET of a health probe; tests answer it themselves.
type HealthRequest func(ctx context.Context, url string) (*http.Response, error)

var healthClient = &http.Client{}

func fetchHealth(ctx context.Context, url string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("cache-control", "no-store")
	return healthClient.Do(request)
}

// healthInstance is the instance a health probe reached; false when what answered is not this app at all.
func healthInstance(ctx context.Context, url string, request HealthRequest) (string, bool) {
	if request == nil {
		request = fetchHealth
	}
	response, err := request(ctx, url)
	if err != nil {
		return "", false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil || response.StatusCode < 200 || response.StatusCode > 299 || string(body) != MobileHealthResponse {
		return "", false
	}
	// A copy from before the header still answers as this app, just not as this process.
	return response.Header.Get(MobileInstanceHeader), true
}

// ReachesMobileServer proves that HTTPS for the saved tailnet name still terminates at this process,
// not merely at this app: a dev build beside the installed one answers the same body from a server of its own.
func ReachesMobileServer(ctx context.Context, magicDNSName string, request HealthRequest) bool {
	within, cancel := context.WithTimeout(ctx, tailscaleHealthTimeout)
	defer cancel()
	instance, ok := healthInstance(within, "https://"+magicDNSName+MobileHealthPath, request)
	return ok && instance == MobileInstance
}

// AnotherInstanceListens is whether another running copy of this app listens on a local port. Nothing answering is no copy at all.
func AnotherInstanceListens(ctx context.Context, port int, request HealthRequest) bool {
	within, cancel := context.WithTimeout(ctx, localHealthTimeout)
	defer cancel()
	instance, ok := healthInstance(within, fmt.Sprintf("http://%s:%d%s", BindHost, port, MobileHealthPath), request)
	return ok && instance != MobileInstance
}

func handlersOf(host any) map[string]any {
	return record(record(host)["Handlers"])
}

// ServesPort is whether Serve is pointed at our port. The config names every handler it proxies, so
// the answer is whether any of them is this server rather than something else the user set up.
func ServesPort(config any, port int) bool {
	web := record(record(config)["Web"])
	target := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, host := range web {
		for _, handler := range handlersOf(host) {
			if record(handler)["Proxy"] == target {
				return true
			}
		}
	}
	return false
}

var localProxy = regexp.MustCompile(`^http://(?:127\.0\.0\.1|localhost):(\d+)/?$`)

// ServedPorts are the local ports Serve hands HTTPS on 443 to, which is where anything dialling this machine's name lands.
func ServedPorts(config any) []int {
	web := record(record(config)["Web"])
	addresses := make([]string, 0, len(web))
	for address := range web {
		addresses = append(addresses, address)
	}
	slices.Sort(addresses)
	ports := []int{}
	for _, address := range addresses {
		if !strings.HasSuffix(address, ":443") {
			continue
		}
		handlers := handlersOf(web[address])
		paths := make([]string, 0, len(handlers))
		for path := range handlers {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			proxy, _ := record(handlers[path])["Proxy"].(string)
			match := localProxy.FindStringSubmatch(proxy)
			if match == nil {
				continue
			}
			if port, err := strconv.Atoi(match[1]); err == nil && !slices.Contains(ports, port) {
				ports = append(ports, port)
			}
		}
	}
	return ports
}

// HeldByAnotherInstance is whether 443 belongs to another running copy of this app: a dev build
// beside the installed one, or `aic serve` on a data folder of its own. Taking it would hand that
// copy's paired devices to a server that has never heard of them, so it is left alone. A port that no
// longer answers is a copy that has gone, and its handler is free to take.
func HeldByAnotherInstance(ctx context.Context, config any, port int, request HealthRequest) bool {
	for _, served := range ServedPorts(config) {
		if served != port && AnotherInstanceListens(ctx, served, request) {
			return true
		}
	}
	return false
}

// ReadTailscale never fails: a machine without Tailscale, or one that will not answer, is a state
// rather than a failure. A name this machine was already known by survives a daemon that would not
// answer, because the name is what the origin check and the QR are built from and a slow answer is
// not a rename. A nil port reads no Serve config.
func ReadTailscale(ctx context.Context, port *int, knownName *string) domain.TailscaleState {
	binary := FindTailscale()
	if binary == "" {
		state := domain.EmptyTailscaleState()
		state.Status = domain.TailscaleStateStatusMissing
		return state
	}
	// Without peers, because this is asked once a minute and a large tailnet makes the answer large.
	output, err := tailscale(ctx, binary, []string{"status", "--json", "--peers=false"}, tailscaleTimeout)
	var status any
	if err == nil {
		status, err = ParseTailscaleJSON(output)
	}
	if err != nil {
		if port != nil && knownName != nil && *knownName != "" && ReachesMobileServer(ctx, *knownName, nil) {
			return domain.TailscaleState{Status: domain.TailscaleStateStatusReady, MagicDnsName: knownName, Certs: true, Serving: true}
		}
		message := readable(err)
		state := domain.EmptyTailscaleState()
		state.Status, state.MagicDnsName, state.Error = domain.TailscaleStateStatusUnavailable, knownName, &message
		return state
	}
	name := trimDNSName(record(record(status)["Self"])["DNSName"])
	if name == nil {
		name = knownName
	}
	state := domain.TailscaleState{
		Status:       backendStatus(record(status)["BackendState"]),
		MagicDnsName: name,
		Certs:        len(certDomains(status)) > 0,
	}
	if state.Status != domain.TailscaleStateStatusReady || port == nil {
		return state
	}
	config, err := serveConfig(ctx, binary)
	if err == nil {
		state.Serving = ServesPort(config, *port)
		return state
	}
	if state.MagicDnsName != nil && *state.MagicDnsName != "" && ReachesMobileServer(ctx, *state.MagicDnsName, nil) {
		state.Certs, state.Serving = true, true
	}
	// No serve config at all is what an unused Tailscale answers, and it is not an error to report.
	return state
}

// TailscaleAction is done (OK), or why not.
type TailscaleAction struct {
	OK      bool
	Message string
}

// CertsOff is the one failure a user must go elsewhere to fix, so it names where rather than what went wrong.
const CertsOff = "This tailnet does not issue HTTPS certificates yet. Turn on HTTPS in the Tailscale admin console, under DNS, then check again."

// HeldElsewhere is said instead of fighting another running copy of this app for the one HTTPS port a name has.
const HeldElsewhere = "Another AI Coding Tool on this computer is using Tailscale. Quit it, then check again."

// StartTailscaleServe puts HTTPS on 443 in front of the local port. Tailscale holds this itself,
// across restarts of this app. A config that cannot be read is served over, as it always was: only a
// copy that answers is left alone.
func StartTailscaleServe(ctx context.Context, port int) TailscaleAction {
	binary := FindTailscale()
	if binary == "" {
		return TailscaleAction{Message: "Tailscale is not installed on this computer."}
	}
	if ready := ReadTailscale(ctx, nil, nil); ready.Status == domain.TailscaleStateStatusReady && !ready.Certs {
		return TailscaleAction{Message: CertsOff}
	}
	config, err := serveConfig(ctx, binary)
	if err != nil {
		config = nil
	}
	if HeldByAnotherInstance(ctx, config, port, nil) {
		return TailscaleAction{Message: HeldElsewhere}
	}
	if _, err := tailscale(ctx, binary, []string{"serve", "--bg", "--https=443", fmt.Sprintf("http://%s:%d", BindHost, port)}, tailscaleServeTimeout); err != nil {
		return TailscaleAction{Message: ServeFailure(err)}
	}
	return TailscaleAction{OK: true}
}

// StopTailscaleServe takes 443 down only while it still points at this port, so another copy's handler is never removed.
func StopTailscaleServe(ctx context.Context, port int) TailscaleAction {
	binary := FindTailscale()
	if binary == "" {
		return TailscaleAction{OK: true}
	}
	config, err := serveConfig(ctx, binary)
	if err != nil {
		return TailscaleAction{Message: ServeFailure(err)}
	}
	if !slices.Contains(ServedPorts(config), port) {
		return TailscaleAction{OK: true}
	}
	if _, err := tailscale(ctx, binary, []string{"serve", "--https=443", "off"}, tailscaleTimeout); err != nil {
		return TailscaleAction{Message: ServeFailure(err)}
	}
	return TailscaleAction{OK: true}
}

// TailscaleHooks is how Tailscale is asked about and driven. Each takes a context a stop cancels, so
// a slow answer never holds the app open.
type TailscaleHooks interface {
	Read(ctx context.Context, port *int, knownName *string) domain.TailscaleState
	Start(ctx context.Context, port int) TailscaleAction
	// Stop takes Serve down only while it still points at this port.
	Stop(ctx context.Context, port int) TailscaleAction
}

// RealTailscale drives whatever Tailscale this machine runs.
type RealTailscale struct{}

func (RealTailscale) Read(ctx context.Context, port *int, knownName *string) domain.TailscaleState {
	return ReadTailscale(ctx, port, knownName)
}

func (RealTailscale) Start(ctx context.Context, port int) TailscaleAction {
	return StartTailscaleServe(ctx, port)
}

func (RealTailscale) Stop(ctx context.Context, port int) TailscaleAction {
	return StopTailscaleServe(ctx, port)
}
