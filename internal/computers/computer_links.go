package computers

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// ComputersFile is where the links keep the paired computers, in the profile folder.
const ComputersFile = "computers.v1.json"

// storedComputer is a computer this one has paired with, as kept on disk: the token is what gets
// this computer back in, Name is what that computer calls itself, and Label is what the user here
// calls it instead. Node is Tailscale's stable ID for the machine, which finds it again when its
// tailnet name moves.
type storedComputer struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Label    *string `json:"label,omitzero"`
	Host     string  `json:"host"`
	Node     *string `json:"node,omitzero"`
	Token    string  `json:"token"`
	PairedAt float64 `json:"pairedAt"`
}

// stored is the file: Name is what this computer calls itself when the user chose one; absent, it
// goes by the machine's.
type stored struct {
	Version   float64           `json:"version"`
	Name      *string           `json:"name,omitzero"`
	Computers []*storedComputer `json:"computers"`
}

type heldComputer struct {
	storedComputer
	capabilities contracts.ComputerCapabilities
	client       line
	status       domain.ComputerStatus
	err          *string
	// When the line last stopped being connected, so a long silence can ask Tailscale where the
	// machine went. Nil while connected.
	downSince *float64
}

var (
	// How long a pairing may wait on the other computer's answer.
	pairingTimeout = 20 * time.Second
	// How long a computer is unreachable before Tailscale is asked whether it now goes by another name.
	relocateAfter = time.Minute
	// How often Tailscale is asked at most, however many computers are down.
	relocateEvery = 5 * time.Minute
)

// LinksOptions describe the links. The callbacks run one at a time on the links' own goroutine and
// must not wait on the links.
type LinksOptions struct {
	File string
	// What this computer calls itself to the others until the user chooses a name.
	DeviceName string
	// Every change to who is paired or how a line stands, pushed whole with this computer's name.
	OnChanged func(name string, links []*domain.ComputerLink)
	OnState   func(id string, state *application.WorkspaceState)
	OnNotice  func(id string, notice contracts.ThreadNotice)
	// Looks across the tailnet; DiscoverComputers when nil.
	Discover func(ctx context.Context) ([]*domain.DiscoveredComputer, error)
	// The machines on the tailnet; Tailscale's own list when nil.
	Peers func(ctx context.Context) ([]TailnetPeer, error)

	// connect opens a line; the real client when nil.
	connect func(options ClientOptions) line
}

func isStoredComputer(value any) bool {
	record, ok := value.(map[string]any)
	if !ok {
		return false
	}
	isString := func(key string) bool { _, ok := record[key].(string); return ok }
	optionalString := func(key string) bool {
		held, present := record[key]
		if !present {
			return true
		}
		_, ok := held.(string)
		return ok
	}
	_, pairedAt := record["pairedAt"].(float64)
	return isString("id") && isString("name") && optionalString("label") && optionalString("node") && isString("host") && isString("token") && pairedAt
}

// readStored reads the file leniently: anything unreadable is no name and no computers.
func readStored(file string) (name *string, computers []*storedComputer) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	var parsed map[string]any
	if jsonx.Unmarshal(data, &parsed) != nil || parsed == nil {
		return nil, nil
	}
	if held, ok := parsed["name"].(string); ok && held != "" {
		name = &held
	}
	list, _ := parsed["computers"].([]any)
	for _, value := range list {
		if !isStoredComputer(value) {
			continue
		}
		encoded, err := jsonx.Marshal(value)
		if err != nil {
			continue
		}
		var computer storedComputer
		if jsonx.Unmarshal(encoded, &computer) == nil {
			computers = append(computers, &computer)
		}
	}
	return name, computers
}

// StoredComputerName is what this computer calls itself, as kept in the links file, or the
// machine's own name when none was chosen.
func StoredComputerName(file, fallback string) string {
	if name, _ := readStored(file); name != nil {
		return *name
	}
	return fallback
}

// chosen is a name as it is kept: trimmed and cut to length, so an empty one is no name at all.
func chosen(name string) *string {
	trimmed := jsTrim(name)
	units := utf16.Encode([]rune(trimmed))
	if len(units) > domain.MaxComputerName {
		trimmed = string(utf16.Decode(units[:domain.MaxComputerName]))
	}
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// jsTrim trims what String.prototype.trim does.
func jsTrim(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x00a0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

func sameString(a, b *string) bool {
	return a == b || (a != nil && b != nil && *a == *b)
}

// ComputerLinks are the computers this one is paired with, and a line to each. Every change to who
// is paired or how a line stands is pushed whole, and every state a computer publishes is pushed as
// it arrives.
type ComputerLinks struct {
	options    LinksOptions
	loop       *loop
	connect    func(options ClientOptions) line
	peers      func(ctx context.Context) ([]TailnetPeer, error)
	held       map[string]*heldComputer
	order      []string
	ownName    *string
	relocating *timer
	running    bool
	// The relocator's own state.
	lookedAt float64
	looking  bool
}

// NewComputerLinks reads who is paired; Start opens their lines.
func NewComputerLinks(options LinksOptions) *ComputerLinks {
	links := &ComputerLinks{options: options, loop: newLoop(), held: map[string]*heldComputer{}}
	links.connect = options.connect
	if links.connect == nil {
		links.connect = func(client ClientOptions) line {
			c := newClient(client)
			c.open()
			return c
		}
	}
	links.peers = options.Peers
	if links.peers == nil {
		links.peers = TailnetPeers
	}
	links.ownName, _ = readStored(options.File)
	return links
}

func (l *ComputerLinks) write() error {
	file := stored{Version: 1, Name: l.ownName, Computers: []*storedComputer{}}
	for _, id := range l.order {
		computer := l.held[id].storedComputer
		file.Computers = append(file.Computers, &computer)
	}
	data, err := jsonx.Marshal(file)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.options.File), 0o777); err != nil {
		return err
	}
	staging := l.options.File + ".tmp"
	if err := os.WriteFile(staging, data, 0o600); err != nil {
		return err
	}
	return os.Rename(staging, l.options.File)
}

// writeQuietly writes from a callback, where there is nobody to hand the failure to.
func (l *ComputerLinks) writeQuietly() {
	if err := l.write(); err != nil {
		log.Printf("computers: could not save the paired computers: %v", err)
	}
}

func (l *ComputerLinks) currentName() string {
	if l.ownName != nil {
		return *l.ownName
	}
	return l.options.DeviceName
}

func (l *ComputerLinks) links() []*domain.ComputerLink {
	views := make([]*domain.ComputerLink, 0, len(l.order))
	for _, id := range l.order {
		computer := l.held[id]
		name := computer.Name
		if computer.Label != nil {
			name = *computer.Label
		}
		views = append(views, &domain.ComputerLink{ID: computer.ID, Name: name, Host: computer.Host, Status: computer.status, Error: computer.err, PairedAt: computer.PairedAt, Capabilities: computer.capabilities})
	}
	return views
}

func (l *ComputerLinks) announce() {
	if l.options.OnChanged != nil {
		l.options.OnChanged(l.currentName(), l.links())
	}
}

func (l *ComputerLinks) hold(computer storedComputer) *heldComputer {
	now := application.Now()
	entry := &heldComputer{storedComputer: computer, status: domain.ComputerStatusConnecting, downSince: &now}
	id := computer.ID
	// Set once the line exists, so a status from a line since replaced is not taken for this one's.
	var client line
	current := func() *heldComputer {
		held := l.held[id]
		if held == nil || held.client != client {
			return nil
		}
		return held
	}
	client = l.connect(ClientOptions{
		Host:       computer.Host,
		DeviceName: l.options.DeviceName,
		Credential: ComputerCredential{Token: computer.Token},
		loop:       l.loop,
		OnStatus: func(status domain.ComputerStatus, err *string) {
			held := current()
			if held == nil {
				return
			}
			if status == domain.ComputerStatusConnected {
				held.downSince = nil
			} else if held.downSince == nil {
				now := application.Now()
				held.downSince = &now
			}
			if held.status == status && sameString(held.err, err) {
				return
			}
			held.status = status
			held.err = err
			l.announce()
			if status == domain.ComputerStatusConnected && held.Node == nil {
				l.relocate(true)
			}
		},
		OnCapabilities: func(capabilities contracts.ComputerCapabilities) {
			held := l.held[id]
			if held == nil {
				return
			}
			held.capabilities = capabilities
			l.announce()
		},
		OnState: func(state *application.WorkspaceState) {
			if l.options.OnState != nil {
				l.options.OnState(id, state)
			}
		},
		OnNotice: func(notice contracts.ThreadNotice) {
			if l.options.OnNotice != nil {
				l.options.OnNotice(id, notice)
			}
		},
		// What that computer now calls itself is kept, under whatever the user here calls it.
		OnName: func(announced string) {
			held := l.held[id]
			if held == nil || held.Name == announced {
				return
			}
			held.Name = announced
			l.writeQuietly()
			l.announce()
		},
	})
	entry.client = client
	if _, ok := l.held[id]; !ok {
		l.order = append(l.order, id)
	}
	l.held[id] = entry
	return entry
}

func (l *ComputerLinks) drop(id string) {
	delete(l.held, id)
	l.order = slices.DeleteFunc(l.order, func(held string) bool { return held == id })
}

// relocate: a machine re-registered or renamed on the tailnet keeps its node ID but answers to a new
// name, and the old name is dialled in vain. Computers down long enough are looked up by that ID,
// and dialled again at the name Tailscale now gives; one paired before IDs were kept learns its ID
// by name. learn looks up only for that, and is how a computer that just connected learns its ID.
func (l *ComputerLinks) relocate(learn bool) {
	now := application.Now()
	var lost []*heldComputer
	for _, id := range l.order {
		computer := l.held[id]
		if learn {
			if computer.Node == nil {
				lost = append(lost, computer)
			}
		} else if computer.status != domain.ComputerStatusConnected && computer.downSince != nil && now-*computer.downSince >= float64(relocateAfter/time.Millisecond) {
			lost = append(lost, computer)
		}
	}
	every := float64(relocateEvery / time.Millisecond)
	if learn {
		every = 0
	}
	if len(lost) == 0 || l.looking || now-l.lookedAt < every {
		return
	}
	l.looking = true
	l.lookedAt = now
	go func() {
		found, err := l.peers(context.Background())
		l.loop.post(func() {
			l.looking = false
			if err != nil {
				return
			}
			l.relocated(lost, found)
		})
	}()
}

func (l *ComputerLinks) relocated(lost []*heldComputer, found []TailnetPeer) {
	change := false
	for _, computer := range lost {
		if l.held[computer.ID] != computer {
			continue
		}
		at := slices.IndexFunc(found, func(peer TailnetPeer) bool {
			if computer.Node != nil {
				return peer.ID == *computer.Node
			}
			return peer.Host == computer.Host
		})
		if at < 0 || found[at].ID == "" {
			continue
		}
		peer := found[at]
		if computer.Node == nil {
			node := peer.ID
			computer.Node = &node
			change = true
		}
		taken := false
		for _, id := range l.order {
			if other := l.held[id]; other != computer && other.Host == peer.Host {
				taken = true
			}
		}
		if peer.Host == computer.Host || taken {
			continue
		}
		computer.client.stop()
		moved := computer.storedComputer
		moved.Host = peer.Host
		l.hold(moved)
		change = true
	}
	if change {
		l.writeQuietly()
		l.announce()
	}
}

// Start opens a line to every paired computer, and keeps looking for the ones that moved.
func (l *ComputerLinks) Start() {
	l.loop.call(func() {
		_, computers := readStored(l.options.File)
		for _, computer := range computers {
			if _, ok := l.held[computer.ID]; !ok {
				l.hold(*computer)
			}
		}
		if !l.running {
			l.running = true
			l.every()
		}
		l.announce()
	})
}

func (l *ComputerLinks) every() {
	l.relocating = l.loop.after(relocateAfter, func() {
		if !l.running {
			return
		}
		l.relocate(false)
		l.every()
	})
}

// Reconnect dials now: every computer, or the one named, whatever its backoff or a refusal had it
// waiting on. What wakes the machine or the user's own asking comes through here.
func (l *ComputerLinks) Reconnect(id *string) {
	l.loop.call(func() {
		for _, held := range l.order {
			if id == nil || *id == "" || held == *id {
				l.held[held].client.reconnect()
			}
		}
		l.lookedAt = 0
		l.relocate(false)
	})
}

// Name is what this computer calls itself.
func (l *ComputerLinks) Name() string {
	var name string
	l.loop.call(func() { name = l.currentName() })
	return name
}

// Links lists the paired computers and where each line stands.
func (l *ComputerLinks) Links() []*domain.ComputerLink {
	var links []*domain.ComputerLink
	l.loop.call(func() { links = l.links() })
	return links
}

// Rename sets what this computer calls itself from now on. Empty goes back to the machine's own name.
func (l *ComputerLinks) Rename(next string) error {
	var err error
	l.loop.call(func() {
		name := chosen(next)
		if sameString(name, l.ownName) {
			return
		}
		l.ownName = name
		if err = l.write(); err != nil {
			return
		}
		l.announce()
	})
	return err
}

// Label sets what the user here calls a paired computer. Empty, or its own name, goes back to what
// it announces.
func (l *ComputerLinks) Label(id, next string) error {
	var err error
	l.loop.call(func() {
		computer := l.held[id]
		if computer == nil {
			return
		}
		label := chosen(next)
		if label != nil && *label == computer.Name {
			label = nil
		}
		if sameString(label, computer.Label) {
			return
		}
		computer.Label = label
		if err = l.write(); err != nil {
			return
		}
		l.announce()
	})
	return err
}

// Discover looks across the tailnet for computers running this app.
func (l *ComputerLinks) Discover(ctx context.Context) ([]*domain.DiscoveredComputer, error) {
	if l.options.Discover != nil {
		return l.options.Discover(ctx)
	}
	return DiscoverComputers(ctx, DiscoverOptions{})
}

// Pair trades the code for a token over a line of its own, then opens the computer's line with the token.
func (l *ComputerLinks) Pair(ctx context.Context, host, name, code string) error {
	var refused error
	var deviceName string
	l.loop.call(func() {
		for _, id := range l.order {
			if l.held[id].Host == host {
				refused = errors.New(name + " is already paired.")
				return
			}
		}
		deviceName = l.currentName()
	})
	if refused != nil {
		return refused
	}
	deviceID, token, err := l.trade(ctx, host, deviceName, code)
	if err != nil {
		return err
	}
	l.loop.call(func() {
		l.hold(storedComputer{ID: deviceID, Name: name, Host: host, Token: token, PairedAt: application.Now()})
		if err = l.write(); err != nil {
			return
		}
		l.announce()
	})
	return err
}

type traded struct {
	deviceID, token string
	err             error
}

// trade swaps the code for a token over a line of its own, which is hung up once the trade is done.
func (l *ComputerLinks) trade(ctx context.Context, host, deviceName, code string) (string, string, error) {
	settle := make(chan traded, 1)
	offer := func(outcome traded) {
		select {
		case settle <- outcome:
		default:
		}
	}
	var client line
	l.loop.call(func() {
		client = l.connect(ClientOptions{
			Host:       host,
			DeviceName: deviceName,
			Credential: ComputerCredential{Code: code},
			loop:       l.loop,
			OnStatus: func(status domain.ComputerStatus, err *string) {
				if status != domain.ComputerStatusOffline {
					return
				}
				message := "The other computer could not be reached."
				if err != nil {
					message = *err
				}
				offer(traded{err: errors.New(message)})
			},
			OnPaired: func(deviceID, _ string, token string) { offer(traded{deviceID: deviceID, token: token}) },
		})
	})
	defer l.loop.call(func() { client.stop() })
	deadline := time.NewTimer(pairingTimeout)
	defer deadline.Stop()
	select {
	case outcome := <-settle:
		return outcome.deviceID, outcome.token, outcome.err
	case <-deadline.C:
		return "", "", errors.New("The other computer did not answer the code in time.")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

// Forget hangs up on a paired computer and forgets its token.
func (l *ComputerLinks) Forget(id string) error {
	var err error
	l.loop.call(func() {
		computer := l.held[id]
		if computer == nil {
			return
		}
		computer.client.stop()
		l.drop(id)
		if err = l.write(); err != nil {
			return
		}
		l.announce()
	})
	return err
}

const noLongerPaired = "That computer is no longer paired."

// Send carries inputs to a paired computer's reducer, answering with what it said.
func (l *ComputerLinks) Send(ctx context.Context, id string, inputs []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
	var done <-chan outcome
	l.loop.call(func() {
		computer := l.held[id]
		if computer == nil {
			done = refusal(noLongerPaired)
			return
		}
		done = computer.client.send(inputs)
	})
	result, err := await(ctx, done)
	if err != nil {
		return nil, err
	}
	return CommandResult(result.result), nil
}

// Query asks a paired computer for a read, answering with its raw result.
func (l *ComputerLinks) Query(ctx context.Context, id string, query contracts.ComputerQuery) (jsontext.Value, error) {
	var done <-chan outcome
	l.loop.call(func() {
		computer := l.held[id]
		if computer == nil {
			done = refusal(noLongerPaired)
			return
		}
		done = computer.client.query(query)
	})
	result, err := await(ctx, done)
	return result.answer, err
}

// Stop hangs up on every computer. They are no longer held, so nothing is shown as connected; Start
// brings them back.
func (l *ComputerLinks) Stop() {
	l.loop.call(func() {
		l.running = false
		l.relocating.clear()
		l.relocating = nil
		for _, id := range l.order {
			l.held[id].client.stop()
		}
		l.held = map[string]*heldComputer{}
		l.order = nil
		l.announce()
	})
}

// CommandResult is the wire result of an input without the revision it carries.
func CommandResult(result contracts.ComputerServerMessageResultResult) contracts.WorkspaceCommandResult {
	switch result := result.(type) {
	case contracts.ComputerServerMessageResultResultTrue:
		return contracts.WorkspaceCommandResultTrue{TaskID: result.TaskID, Answer: result.Answer}
	case contracts.ComputerServerMessageResultResultFalse:
		return contracts.WorkspaceCommandResultFalse{Message: result.Message}
	}
	return contracts.WorkspaceCommandResultFalse{Message: "That computer sent an unreadable result."}
}
