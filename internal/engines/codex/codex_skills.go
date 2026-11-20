package codex

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
)

var appPlugin struct {
	sync.RWMutex
	root string
}

// Where the app's own plugin lives on disk. Empty means no app skills.
func SetAppPluginRoot(directory string) {
	appPlugin.Lock()
	appPlugin.root = directory
	appPlugin.Unlock()
}

// The plugin's skills folder, which Codex reads as an extra skill root.
func appSkillsRoot() (string, bool) {
	appPlugin.RLock()
	defer appPlugin.RUnlock()
	root := agent.AppSkillsRoot(appPlugin.root)
	return root, root != ""
}

// Codex qualifies the app's skills as `aicodingtool:skill`; the app offers and matches them by the short name.
func adoptAppSkills(ctx context.Context, client CodexClient) error {
	root, ok := appSkillsRoot()
	if !ok {
		return nil
	}
	_, err := client.Request(ctx, "skills/extraRoots/set", protocol.SkillsExtraRootsSetParams{ExtraRoots: []string{root}})
	return err
}

// The menu and slash invocation use the same enabled skills and name resolution.
func readSkills(ctx context.Context, client CodexClient, cwd string, forceReload bool) ([]protocol.SkillMetadata, error) {
	result, err := call[protocol.SkillsListResponse](ctx, client, "skills/list", protocol.SkillsListParams{Cwds: []string{cwd}, ForceReload: &forceReload})
	if err != nil {
		return nil, err
	}
	var skills []protocol.SkillMetadata
	place := map[string]int{}
	for _, entry := range result.Data {
		for _, skill := range entry.Skills {
			if !skill.Enabled {
				continue
			}
			if index, seen := place[skill.Name]; seen {
				skills[index] = skill
				continue
			}
			place[skill.Name] = len(skills)
			skills = append(skills, skill)
		}
	}
	return skills, nil
}

type discovery struct {
	done     chan struct{}
	commands []*contracts.AvailableCommand
	err      error
}

var discovering = struct {
	sync.Mutex
	byCwd map[string]*discovery
}{byCwd: map[string]*discovery{}}

const defaultDiscoveryTimeout = 20 * time.Second

// Shares concurrent composer reads; the next refresh asks Codex again for installs and config
// changes. Nil connect is ConnectAppServer; timeout <= 0 is 20 seconds.
func DiscoverCodexCommands(ctx context.Context, cwd string, connect CodexConnect, timeout time.Duration) ([]*contracts.AvailableCommand, error) {
	discovering.Lock()
	if pending := discovering.byCwd[cwd]; pending != nil {
		discovering.Unlock()
		<-pending.done
		return pending.commands, pending.err
	}
	reading := &discovery{done: make(chan struct{})}
	discovering.byCwd[cwd] = reading
	discovering.Unlock()
	reading.commands, reading.err = discover(ctx, cwd, connect, timeout)
	discovering.Lock()
	delete(discovering.byCwd, cwd)
	discovering.Unlock()
	close(reading.done)
	return reading.commands, reading.err
}

var errSkillsLate = errors.New("Codex did not return its skills in time.")

// A short-lived server with the same home and bundled-plugin settings as a run.
func discover(ctx context.Context, cwd string, connect CodexConnect, timeout time.Duration) ([]*contracts.AvailableCommand, error) {
	if connect == nil {
		connect = ConnectAppServer
	}
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	command, err := CodexAppServer(AppFeatures, AppServerOptions{Cwd: cwd})
	if err != nil {
		return nil, err
	}
	client := connect(command)
	defer client.Close()
	return withDeadline(ctx, timeout, errSkillsLate, func(ctx context.Context) ([]*contracts.AvailableCommand, error) {
		if err := initialize(ctx, client); err != nil {
			return nil, err
		}
		if err := adoptAppSkills(ctx, client); err != nil {
			return nil, err
		}
		skills, err := readSkills(ctx, client, cwd, true)
		if err != nil {
			return nil, err
		}
		commands := make([]*contracts.AvailableCommand, 0, len(skills))
		for _, skill := range skills {
			commands = append(commands, &contracts.AvailableCommand{Name: agent.UnqualifiedSkillName(skill.Name), Description: skill.Description})
		}
		return commands, nil
	})
}

// A slash at the start of the prompt or after whitespace, as JavaScript's \s reads it.
var slashSkill = regexp.MustCompile(`(?:^|[\s\x{0B}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}])/([^\s\x{0B}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}/]+)`)

// The native Codex skills available to one workspace.
type CodexSkills struct {
	client CodexClient
	cwd    string

	mu      sync.Mutex
	skills  []protocol.SkillMetadata
	reading chan struct{}
}

func NewCodexSkills(client CodexClient, cwd string) *CodexSkills {
	return &CodexSkills{client: client, cwd: cwd}
}

// Refreshes metadata without making skill discovery a condition for running Codex. The channel
// closes once the read has landed.
func (skills *CodexSkills) Refresh(forceReload bool) <-chan struct{} {
	skills.mu.Lock()
	defer skills.mu.Unlock()
	if skills.reading != nil {
		return skills.reading
	}
	reading := make(chan struct{})
	skills.reading = reading
	go func() {
		read, err := readSkills(context.Background(), skills.client, skills.cwd, forceReload)
		skills.mu.Lock()
		if err == nil {
			skills.skills = read
		}
		if skills.reading == reading {
			skills.reading = nil
		}
		skills.mu.Unlock()
		close(reading)
	}()
	return reading
}

// Waits for an active refresh, then adds every slash skill named in the prompt.
func (skills *CodexSkills) Input(prompt string) []protocol.UserInput {
	skills.mu.Lock()
	reading := skills.reading
	skills.mu.Unlock()
	if reading != nil {
		<-reading
	}
	skills.mu.Lock()
	available := map[string]protocol.SkillMetadata{}
	for _, skill := range skills.skills {
		available[skill.Name] = skill
		available[agent.UnqualifiedSkillName(skill.Name)] = skill
	}
	skills.mu.Unlock()
	requested := []protocol.UserInput{}
	seen := map[string]bool{}
	for _, match := range slashSkill.FindAllStringSubmatch(prompt, -1) {
		skill, ok := available[match[1]]
		if !ok || seen[skill.Name] {
			continue
		}
		seen[skill.Name] = true
		requested = append(requested, protocol.SkillInput{Name: skill.Name, Path: skill.Path})
	}
	return append(requested, protocol.Text(prompt))
}
