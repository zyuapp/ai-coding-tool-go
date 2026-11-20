package computers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"rsc.io/qr"
)

// ServeArguments are what `aic serve` and `aic pair` were asked for.
type ServeArguments struct {
	// "serve" or "pair".
	Command string
	// A source checkout's own data folder, apart from the installed app's.
	Dev bool
	// A source checkout serving the installed app's data folder and checkouts, as `aic serve` would.
	Installed bool
	// A data folder named outright, which a test uses so nothing it does lands in the user's.
	UserData *string
	// The port to ask for; 0 takes whatever is free. NaN when what followed --port was no number.
	Port *float64
	// Loopback only: Tailscale is left alone, which a test on a developer's machine must do.
	Local bool
}

func ParseServeArguments(argv []string) ServeArguments {
	parsed := ServeArguments{Command: "serve"}
	next := func(index *int) (string, bool) {
		*index++
		if *index < len(argv) {
			return argv[*index], true
		}
		return "", false
	}
	for index := 0; index < len(argv); index++ {
		switch argument := argv[index]; argument {
		case "serve", "pair":
			parsed.Command = argument
		case "--dev":
			parsed.Dev = true
		case "--installed":
			parsed.Installed = true
		case "--local":
			parsed.Local = true
		case "--user-data":
			if value, ok := next(&index); ok {
				parsed.UserData = &value
			}
		case "--port":
			value, _ := next(&index)
			port := jsNumber(value)
			parsed.Port = &port
		}
	}
	return parsed
}

// jsNumber reads text as Number() does for the decimal forms a port takes.
func jsNumber(text string) float64 {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return math.NaN()
	}
	return value
}

// PairingText is the pairing link drawn as a QR for the phone's camera, above the code to type
// instead. Output that is not a terminal, such as a log, gets the line alone, because the drawing is
// colour codes there.
func PairingText(offer *domain.MobilePairingOffer, terminal bool) string {
	minutes := math.Round((offer.ExpiresAt - application.Now()) / 60_000)
	line := fmt.Sprintf("Pair with code %s (expires in %s minutes) or open %s", offer.Code, strconv.FormatFloat(minutes, 'f', -1, 64), offer.URL)
	if !terminal {
		return line
	}
	code, err := qr.Encode(offer.URL, qr.M)
	if err != nil {
		return line
	}
	return smallTerminalQR(code) + "\n" + line
}

// smallTerminalQR draws two modules per character cell with half blocks, with a one-module quiet
// zone, as the `qrcode` package's small terminal renderer does.
func smallTerminalQR(code *qr.Code) string {
	const (
		backgroundWhite = "\x1b[47m"
		foregroundBlack = "\x1b[30m"
		foregroundWhite = "\x1b[37m"
		reset           = "\x1b[0m"
		lineSetup       = backgroundWhite + foregroundBlack
	)
	palette := map[string]string{
		"00": reset + " " + lineSetup,
		"01": reset + foregroundWhite + "▄" + lineSetup,
		"02": reset + foregroundBlack + "▄" + lineSetup,
		"10": reset + foregroundWhite + "▀" + lineSetup,
		"11": " ",
		"12": "▄",
		"20": reset + foregroundBlack + "▀" + lineSetup,
		"21": "▀",
		"22": "█",
	}
	size := code.Size
	pixel := func(x, y int) string {
		if x >= size+1 || y >= size+1 || y < -1 || x < -1 {
			return "0"
		}
		if x >= size || y >= size || y < 0 || x < 0 {
			return "1"
		}
		if code.Black(x, y) {
			return "2"
		}
		return "1"
	}
	var output strings.Builder
	output.WriteString(lineSetup)
	for y := -1; y < size+1; y += 2 {
		for x := -1; x < size; x++ {
			output.WriteString(palette[pixel(x, y)+pixel(x, y+1)])
		}
		output.WriteString(palette[pixel(size, y)+pixel(size, y+1)] + reset + "\n" + lineSetup)
	}
	output.WriteString(reset)
	return output.String()
}

var pairingCode = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]+$`)

// dialUnix dials a socket at the path libuv binds it to: cut to what sockaddr_un holds, so a socket
// a Node server bound under a long path is reached at the same place. A path Go cannot name itself
// is reached through a short link to it.
func dialUnix(ctx context.Context, socketPath string) (net.Conn, error) {
	if len(socketPath) > sunPathSize {
		socketPath = socketPath[:sunPathSize]
	}
	if dialable(socketPath) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}
	folder, err := os.MkdirTemp("", "aic-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(folder)
	link := filepath.Join(folder, "s")
	if err := os.Symlink(socketPath, link); err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, "unix", link)
}

// RequestPairing asks the process serving on the socket for a pairing code.
func RequestPairing(ctx context.Context, socketPath string) (*domain.MobilePairingOffer, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialUnix(ctx, socketPath)
		}},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/pair", nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		var timeout interface{ Timeout() bool }
		if errors.As(err, &timeout) && timeout.Timeout() {
			return nil, errors.New("The bridge did not answer.")
		}
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 16_385))
	if err != nil {
		return nil, err
	}
	if len(body) > 16_384 {
		return nil, errors.New("Invalid pairing response.")
	}
	if response.StatusCode != http.StatusOK {
		if len(body) == 0 {
			return nil, errors.New("The bridge is not ready.")
		}
		return nil, errors.New(string(body))
	}
	var offer domain.MobilePairingOffer
	if err := jsonx.Unmarshal(body, &offer); err != nil {
		return nil, err
	}
	if !pairingCode.MatchString(offer.Code) {
		return nil, errors.New("Invalid pairing response.")
	}
	return &offer, nil
}

// TailscaleOffForServer is what a loopback-only server's Tailscale answers a start with.
const TailscaleOffForServer = "Tailscale is off for this server."

// ServedName is what a headless host calls itself: the name chosen in the app that shares its data
// folder, or the machine's own.
func ServedName(userData string) func() string {
	return func() string {
		host, _ := os.Hostname()
		return StoredComputerName(filepath.Join(userData, ComputersFile), strings.TrimSuffix(host, ".local"))
	}
}

// RestartForUpdate writes down the threads the restart cuts off, to carry on once the new build is up.
// record is how update-relaunch keeps them.
func RestartForUpdate(state func() *application.WorkspaceState, record func(version string, threads []string), restart func()) func(installed string) {
	return func(installed string) {
		record(installed, application.ResumableAfterUpdate(state()))
		restart()
	}
}

// ServeStateReporter is what a headless host says as its bridge moves: the name it serves as, with a
// pairing code under it, and whatever the bridge or Tailscale reports wrong, each said once as it changes.
func ServeStateReporter(say func(string), pair func(ctx context.Context) (*domain.MobilePairingOffer, error), terminal bool) func(state domain.MobileServerState) {
	var mu sync.Mutex
	var served, trouble *string
	return func(state domain.MobileServerState) {
		mu.Lock()
		defer mu.Unlock()
		var name *string
		if state.Tailscale.Serving {
			name = state.Tailscale.MagicDnsName
		}
		if name != nil && *name != "" && !sameString(name, served) {
			served = name
			say("Serving as https://" + *name)
			go func() {
				offer, err := pair(context.Background())
				if err == nil {
					say(PairingText(offer, terminal))
				}
			}()
		}
		if state.Error != nil && *state.Error != "" {
			say("Bridge: " + *state.Error)
		}
		if state.Tailscale.Error != nil && *state.Tailscale.Error != "" && !sameString(state.Tailscale.Error, trouble) {
			say("Tailscale: " + *state.Tailscale.Error)
		}
		trouble = state.Tailscale.Error
	}
}

// ServeOptions describe one headless server.
type ServeOptions struct {
	UserData  string
	Packaged  bool
	Installed bool
	// The packaged app's resources folder, or "" from source.
	Resources string
	Port      *int
	Local     bool
	Say       func(line string)
	// Given when whatever started this server starts it again after it stops for an update.
	Restart func()
	// Terminal says whether output is a terminal, where a pairing link is drawn as a QR.
	Terminal bool
	// Version is this build's, or "" when it cannot say.
	Version string
	// RecordUpdateRelaunch writes down the threads an update's restart cuts off (update-relaunch).
	RecordUpdateRelaunch func(userData, version string, threads []string)
	// Build opens the services on the claimed data folder.
	Build func(ctx context.Context, environment ServeEnvironment) (*ServeParts, error)
}

// ServeEnvironment is what the services of a headless server are opened with.
type ServeEnvironment struct {
	UserData      string
	WorktreesRoot string
	// Older checkout roots under the data folder, still the app's own.
	LegacyRoots []string
	// Nil where this server cannot replace its own app.
	Updates *ServeUpdater
	Say     func(line string)
}

// ServeRuntime is the workspace runtime as a server drives it.
type ServeRuntime interface {
	Start(ctx context.Context) error
	Dispatch(ctx context.Context, input application.WorkspaceInput) error
	State() *application.WorkspaceState
	Dispose()
}

// ServeMobileHost is the bridge a server runs: started, served on Tailscale unless Local, and stopped.
type ServeMobileHost interface {
	// Start listens on port (nil for the default), loopback only when local, telling onState as it moves.
	Start(ctx context.Context, port *int, local bool, onState func(domain.MobileServerState)) error
	SetMobileEnabled(ctx context.Context, enabled bool) (*domain.MobileServerState, error)
	MobileState() domain.MobileServerState
	CreateMobilePairingCode(ctx context.Context) (*domain.MobilePairingOffer, error)
	Stop(ctx context.Context) error
}

// ServeParts are the services Build opened, as StartServe drives them.
type ServeParts struct {
	Runtime ServeRuntime
	// Flush saves whatever the runtime has not yet written.
	Flush  func(ctx context.Context) error
	Mobile ServeMobileHost
	// ServePairingSocket answers `aic pair` on a private socket until the returned stop.
	ServePairingSocket func(ctx context.Context, socketPath string, pair func(ctx context.Context) (*domain.MobilePairingOffer, error)) (func(ctx context.Context) error, error)
	// SweepAttachments removes attachment files no thread names any more.
	SweepAttachments func(ctx context.Context) (files int, bytes int64, err error)
	// Stop puts the services down once the runtime is flushed: runs, agents, engines, the
	// scheduler, terminals and the task database.
	Stop func(ctx context.Context)
}

// Served is a running headless server.
type Served struct {
	Parts *ServeParts
	stop  func(ctx context.Context) error
	once  sync.Once
	err   error
}

// Port is the port the bridge listens on, or nil before it does.
func (s *Served) Port() *float64 { return s.Parts.Mobile.MobileState().Port }

// Stop puts the server down; later calls wait on the first.
func (s *Served) Stop(ctx context.Context) error {
	s.once.Do(func() { s.err = s.stop(ctx) })
	return s.err
}

// StartServe runs the application headless: every service the desktop app has, reachable only
// through the bridge.
func StartServe(ctx context.Context, options ServeOptions) (*Served, error) {
	say := options.Say
	if say == nil {
		say = func(string) {}
	}
	if options.Build == nil {
		return nil, errors.New("Serving needs its services.")
	}
	if err := os.MkdirAll(options.UserData, 0o777); err != nil {
		return nil, err
	}
	release, err := platform.ClaimServeLock(options.UserData)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	worktreesRoot := platform.AppProfile(platform.AppDataDir(), home, options.Packaged || options.Installed).WorktreesRoot
	var legacyRoots []string
	if legacy := filepath.Join(options.UserData, "worktrees"); legacy != worktreesRoot {
		legacyRoots = append(legacyRoots, legacy)
	}
	var workspace ServeRuntime
	var updates *ServeUpdater
	if options.Resources != "" && options.Restart != nil && options.Version != "" {
		if target := UpdateTargetFor(runtime.GOOS, executablePath(), os.Getenv); target != nil {
			updates = NewServeUpdater(ServeUpdaterOptions{
				Version: options.Version,
				Target:  *target,
				Restart: RestartForUpdate(func() *application.WorkspaceState { return workspace.State() }, func(version string, threads []string) {
					if options.RecordUpdateRelaunch != nil {
						options.RecordUpdateRelaunch(options.UserData, version, threads)
					}
				}, options.Restart),
				Say: say,
			})
		}
	}
	parts, err := options.Build(ctx, ServeEnvironment{UserData: options.UserData, WorktreesRoot: worktreesRoot, LegacyRoots: legacyRoots, Updates: updates, Say: say})
	if err != nil {
		release()
		return nil, err
	}
	workspace = parts.Runtime
	if err := workspace.Start(ctx); err != nil {
		release()
		return nil, err
	}
	var stopUpdateChecks func()
	if updates != nil {
		stopUpdateChecks = updates.Start()
	}
	// Browser and computer-use surfaces need a desktop; shells can be viewed from a paired computer.
	_ = workspace.Dispatch(ctx, contracts.ViewCommandViewSetBrowserTools{Enabled: false})
	_ = workspace.Dispatch(ctx, contracts.ViewCommandViewSetComputerUse{Enabled: false})

	report := ServeStateReporter(say, parts.Mobile.CreateMobilePairingCode, options.Terminal)
	if err := parts.Mobile.Start(ctx, options.Port, options.Local, report); err != nil {
		release()
		return nil, err
	}
	if parts.SweepAttachments != nil {
		go func() {
			files, bytes, err := parts.SweepAttachments(context.Background())
			if err != nil {
				fmt.Fprintln(os.Stderr, "Could not sweep unused attachments:", err)
				return
			}
			if files > 0 {
				say(fmt.Sprintf("Removed %d unused attachment file(s), %s MB.", files, strconv.FormatFloat(math.Round(float64(bytes)/1024/1024), 'f', -1, 64)))
			}
		}()
	}
	stopPairingSocket, err := parts.ServePairingSocket(ctx, filepath.Join(options.UserData, "serve.sock"), parts.Mobile.CreateMobilePairingCode)
	if err != nil {
		release()
		return nil, err
	}
	if _, err := parts.Mobile.SetMobileEnabled(ctx, true); err != nil {
		say("Bridge: " + err.Error())
	}
	state := parts.Mobile.MobileState()
	if state.Status == domain.MobileServerStatusListening && state.Port != nil {
		suffix := ". Waiting for Tailscale to serve it."
		if options.Local {
			suffix = "."
		}
		say("Listening on port " + strconv.FormatFloat(*state.Port, 'f', -1, 64) + suffix)
	}
	if state.Tailscale.Status == domain.TailscaleStateStatusMissing && !options.Local {
		say("Tailscale is not installed on this computer, so nothing can reach the bridge.")
	}
	served := &Served{Parts: parts}
	served.stop = func(ctx context.Context) error {
		if stopUpdateChecks != nil {
			stopUpdateChecks()
		}
		if updates != nil {
			updates.Dispose()
		}
		if err := parts.Flush(ctx); err != nil {
			say("Could not save the workspace: " + err.Error())
		}
		if parts.Stop != nil {
			parts.Stop(ctx)
		}
		_ = stopPairingSocket(ctx)
		_ = parts.Mobile.Stop(ctx)
		_ = parts.Flush(ctx)
		workspace.Dispose()
		release()
		return nil
	}
	return served, nil
}

// ServeMain runs `aic serve` and `aic pair` as a process: what stops it is said plainly and becomes
// the exit code. start is StartServe with the services filled in.
func ServeMain(argv []string, version string, start func(ctx context.Context, options ServeOptions) (*Served, error)) int {
	arguments := ParseServeArguments(argv)
	packaged := !arguments.Dev
	userData := ""
	if arguments.UserData != nil {
		userData = *arguments.UserData
	} else {
		home, _ := os.UserHomeDir()
		userData = platform.AppProfile(platform.AppDataDir(), home, packaged || arguments.Installed).UserData
	}
	terminal := isTerminal(os.Stdout)
	if arguments.Command == "pair" {
		offer, err := RequestPairing(context.Background(), filepath.Join(userData, "serve.sock"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "No AI Coding Tool is serving here. Start one with `aic serve`. (%s)\n", err.Error())
			return 1
		}
		fmt.Println(PairingText(offer, terminal))
		return 0
	}
	resources := packagedResources()
	restarts := os.Getenv(domain.ServeRestartsEnv) == "1"
	// Kept from the agents and shells this server starts.
	_ = os.Unsetenv(domain.ServeRestartsEnv)
	exit := make(chan int, 1)
	stopping := make(chan string, 1)
	options := ServeOptions{UserData: userData, Packaged: packaged, Installed: arguments.Installed, Resources: resources, Local: arguments.Local, Say: func(line string) { fmt.Println(line) }, Terminal: terminal, Version: version}
	if arguments.Port != nil {
		port := int(*arguments.Port)
		options.Port = &port
	}
	if restarts {
		options.Restart = func() {
			select {
			case stopping <- "update":
			default:
			}
		}
	}
	served, err := start(context.Background(), options)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Printf("AI Coding Tool is serving from %s. Press Ctrl+C to stop.\n", userData)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		code := 0
		select {
		case received := <-signals:
			fmt.Printf("Stopping (%s)…\n", signalName(received))
		case reason := <-stopping:
			fmt.Printf("Stopping (%s)…\n", reason)
			code = domain.ServeRestartExit
		}
		if err := served.Stop(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			exit <- 1
			return
		}
		exit <- code
	}()
	return <-exit
}

func signalName(received os.Signal) string {
	switch received {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return received.String()
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func executablePath() string {
	path, _ := os.Executable()
	return path
}

// packagedResources is the packaged app's resources folder beside this process's binary, on either
// platform's layout, or "" from source.
func packagedResources() string {
	binary := filepath.Dir(executablePath())
	for _, candidate := range []string{filepath.Join(binary, "resources"), filepath.Join(binary, "..", "Resources")} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() && platform.Packaged() {
			return filepath.Clean(candidate)
		}
	}
	return ""
}
