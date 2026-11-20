package opencode

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/opencode/client"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const sessionEnded = "The OpenCode session ended before the run could start."

// How long a lost feed waits for its server's exit, which says why it went.
const exitWait = 500 * time.Millisecond

type sessionInfo struct {
	ID       string `json:"id"`
	Location *struct {
		Directory *string `json:"directory,omitzero"`
	} `json:"location,omitzero"`
}

type FormOption struct {
	Value       string  `json:"value"`
	Label       string  `json:"label"`
	Description *string `json:"description,omitzero"`
}

type FormField struct {
	Key         string        `json:"key"`
	Type        string        `json:"type"`
	Title       *string       `json:"title,omitzero"`
	Description *string       `json:"description,omitzero"`
	Options     []*FormOption `json:"options,omitzero"`
}

// A form OpenCode puts to the user.
type FormInfo struct {
	ID        string       `json:"id"`
	SessionID string       `json:"sessionID"`
	Title     string       `json:"title"`
	Fields    []*FormField `json:"fields"`
}

// What JavaScript's \s matches, for classes in the patterns below.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	namesSlash   = regexp.MustCompile(`(?:^|[` + jsSpace + `])/[^` + jsSpace + `/]`)
	slashCommand = regexp.MustCompile(`^/([^` + jsSpace + `]+)(?:[` + jsSpace + `]+((?s:.)*))?$`)
	slashSkill   = regexp.MustCompile(`(?:^|[` + jsSpace + `])/([^` + jsSpace + `/]+)`)
	yesOrTrue    = regexp.MustCompile(`(?i)^(yes|true)$`)
	emptySession = regexp.MustCompile(`(?i)empty session`)
)

// Tokens a response read and wrote, as the server counts a step.
func tokensOf(value jsontext.Value) float64 {
	fields, ok := recordOf(value)
	if !ok {
		return 0
	}
	count := func(from record, key string) float64 {
		raw, ok := from[key]
		if !ok || raw.Kind() != '0' {
			return 0
		}
		var number float64
		if jsonx.Unmarshal(raw, &number) != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return 0
		}
		return number
	}
	cache, _ := fields.object("cache")
	return count(fields, "input") + count(fields, "output") + count(fields, "reasoning") + count(cache, "read") + count(cache, "write")
}

// The app's own tools are filed under one server; their bare name is what reads, and what the app knows them by.
func ToolName(name string) string {
	return strings.TrimPrefix(name, AppServerName+"_")
}

var writingTools = []string{"write", "edit", "apply_patch", "patch", "multiedit"}

// What a tool call is about, in the shape the thread records tools in. A write names the file it changes.
func IntentOf(toolID, name string, input jsontext.Value, root string) domain.ToolIntent {
	args, ok := recordOf(input)
	raw := input
	if !ok {
		args, raw = record{}, jsontext.Value("{}")
	}
	intent := domain.ToolIntent{ToolID: toolID, Name: ToolName(name), Input: raw}
	file, ok := args.text("filePath")
	if !ok {
		file, _ = args.text("path")
	}
	if slices.Contains(writingTools, name) && file != "" {
		resolved := file
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, file)
		}
		if absolute, err := filepath.Abs(resolved); err == nil {
			resolved = absolute
		}
		intent.WritePath = &resolved
	}
	return intent
}

// A form OpenCode puts to the user, asked as the composer asks its questions: one per field. Nil
// when no field can be answered in chat.
func QuestionsOf(form *FormInfo) *domain.QuestionRequest {
	var questions []*domain.AgentQuestion
	for _, field := range form.Fields {
		if field == nil || field.Type == "external" {
			continue
		}
		header := form.Title
		if field.Title != nil {
			header = *field.Title
		}
		question := header
		if field.Description != nil {
			question = *field.Description
		}
		options := []*domain.QuestionOption{}
		if field.Type == "boolean" {
			options = append(options, &domain.QuestionOption{Label: "Yes"}, &domain.QuestionOption{Label: "No"})
		} else {
			for _, option := range field.Options {
				options = append(options, &domain.QuestionOption{Label: option.Label, Description: orEmpty(option.Description)})
			}
		}
		questions = append(questions, &domain.AgentQuestion{ID: field.Key, Header: header, Question: question, Options: options})
	}
	if len(questions) == 0 {
		return nil
	}
	return &domain.QuestionRequest{Blocking: true, Questions: questions}
}

func optionValue(field *FormField, label string) (string, bool) {
	for _, option := range field.Options {
		if option.Label == label {
			return option.Value, true
		}
	}
	return "", false
}

var decimalNumber = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

// JavaScript's Number(text); nil stands for NaN and the infinities, which JSON writes as null.
func jsNumberOf(text string) any {
	trimmed := jsTrim(text)
	if trimmed == "" {
		return 0.0
	}
	if len(trimmed) > 2 && trimmed[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[trimmed[1]]
		if base != 0 {
			value, err := strconv.ParseUint(trimmed[2:], base, 64)
			if err != nil || strings.Contains(trimmed, "_") {
				return nil
			}
			return float64(value)
		}
	}
	if !decimalNumber.MatchString(trimmed) {
		return nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) || math.IsInf(value, 0) {
		return nil
	}
	return value
}

// The answers to a form, each in the type its field takes.
func FormAnswer(form *FormInfo, answers domain.QuestionAnswers) map[string]any {
	answer := map[string]any{}
	for _, field := range form.Fields {
		if field == nil {
			continue
		}
		given, ok := answers[field.Key]
		if !ok {
			continue
		}
		chosen := given
		if value, ok := optionValue(field, given); ok {
			chosen = value
		}
		switch field.Type {
		case "boolean":
			answer[field.Key] = yesOrTrue.MatchString(jsTrim(given))
		case "number", "integer":
			answer[field.Key] = jsNumberOf(given)
		case "multiselect":
			values := []string{}
			for part := range strings.SplitSeq(given, ",") {
				value, ok := optionValue(field, jsTrim(part))
				if !ok {
					value = jsTrim(part)
				}
				if value != "" {
					values = append(values, value)
				}
			}
			answer[field.Key] = values
		default:
			answer[field.Key] = chosen
		}
	}
	return answer
}

func openFailure(err error) string {
	var failure *agent.OpenFailure
	if errors.As(err, &failure) {
		return failure.Message
	}
	return "OpenCode could not start: " + agent.ReasonOf(err)
}

type turn struct {
	*agent.SessionTurn
	// Guarded by compactMu: the turn's start and the event feed both report compaction.
	agent.Compacting
	compactMu sync.Mutex

	// Guarded by the session's mu.
	// Messages this turn put in the session's inbox that it has yet to take, and those it has taken.
	waiting   map[string]bool
	delivered map[string]bool
	// Set once the server has the turn's prompt, so an interrupt has something to stop.
	started         bool
	interruptWanted bool

	// Read and written only by the event feed.
	// Assistant text still buffered per part, so a half-written block never ships; in the order the parts began.
	streamed    map[string]*agent.MarkdownBuffer
	streamOrder []string
	// Tool names by call id, which the call itself does not repeat, and each call as it was shown.
	tools   map[string]string
	intents map[string]domain.ToolIntent
}

type slashNames struct {
	done     chan struct{}
	skills   map[string]bool
	commands map[string]bool
}

// What the server answers a message put in the inbox with.
type inboxAnswer struct {
	ID *string `json:"id,omitzero"`
}

type sessionSettings struct {
	model, variant, agent, rules string
}

// One OpenCode session, kept across turns on a server of its own. The server belongs to the session
// because the app's tools and the run's instructions are given to it as it starts, so a run that
// needs others gets a session of its own.
type Session struct {
	key          string
	connect      Connect
	host         agent.ToolHost
	hooks        agent.SessionHooks
	pluginRoot   string
	loadingRetry time.Duration
	turns        *agent.TurnSlot[*turn]

	mu        sync.Mutex
	client    Client
	served    *agent.ServedTools
	opening   *opening
	ended     bool
	closed    chan struct{}
	outcome   *agent.ProviderResult
	briefFile string
	// Context windows, and the depths each model reasons at, by app model id, read once from the server.
	windows  map[string]float64
	variants map[string]map[string]bool
	// The skills and commands slash names reach, read while the first turn starts and waited on only by one that names any.
	slashes *slashNames
	// The sessions this one's own agents opened, whose questions are this session's to answer.
	children    map[string]bool
	settings    sessionSettings
	lastTokens  float64
	unsubscribe func()
	// What OpenCode calls the session. A later run resumes it by this id.
	sessionID string
}

type opening struct {
	done chan struct{}
	err  error
}

func newSession(key string, connect Connect, host agent.ToolHost, hooks agent.SessionHooks, pluginRoot string, loadingRetry time.Duration) *Session {
	session := &Session{
		key: key, connect: connect, host: host, hooks: hooks, pluginRoot: pluginRoot, loadingRetry: loadingRetry,
		windows: map[string]float64{}, variants: map[string]map[string]bool{}, children: map[string]bool{},
	}
	session.turns = agent.NewTurnSlot[*turn](agent.InterruptGrace, func() { session.Close() })
	return session
}

func (s *Session) Key() string       { return s.key }
func (s *Session) Answering() bool   { return s.turns.Answering() }
func (s *Session) Busy() bool        { return s.Answering() }
func (s *Session) Live() bool        { s.mu.Lock(); defer s.mu.Unlock(); return !s.ended }
func (s *Session) SessionID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.sessionID }

func (s *Session) Continues(continuation *string) bool {
	return continuation == nil || *continuation == s.SessionID()
}

func (s *Session) current(t *turn) bool {
	now, ok := s.turns.Turn()
	return ok && now == t
}

type turnEngine struct{ session *Session }

func (engine turnEngine) Open(base *agent.SessionTurn) *turn {
	return &turn{
		SessionTurn: base,
		waiting:     map[string]bool{}, delivered: map[string]bool{},
		streamed: map[string]*agent.MarkdownBuffer{}, tools: map[string]string{}, intents: map[string]domain.ToolIntent{},
	}
}

func (engine turnEngine) Begin(t *turn)     { go engine.session.begin(t) }
func (engine turnEngine) Interrupt(t *turn) { engine.session.interrupt(t) }

func (s *Session) Run(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	s.mu.Lock()
	if s.ended {
		outcome := s.outcome
		s.mu.Unlock()
		if outcome != nil {
			return *outcome, nil
		}
		return agent.ProviderResult{Status: domain.RunStatusFailed, Message: sessionEnded}, nil
	}
	s.mu.Unlock()
	return s.turns.Take(input, turnEngine{s}), nil
}

func (s *Session) Close() <-chan struct{} {
	s.mu.Lock()
	if s.ended {
		closed := s.closed
		s.mu.Unlock()
		return closed
	}
	s.ended = true
	s.closed = make(chan struct{})
	s.mu.Unlock()
	s.turns.Settle(agent.ProviderResult{Status: domain.RunStatusCancelled})
	s.mu.Lock()
	unsubscribe, c, served, brief, closed := s.unsubscribe, s.client, s.served, s.briefFile, s.closed
	s.unsubscribe, s.client, s.served = nil, nil, nil
	s.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
	go func() {
		if c != nil {
			c.Close()
		}
		if brief != "" {
			_ = os.Remove(brief)
		}
		close(closed)
	}()
	if served != nil && served.Release != nil {
		served.Release()
	}
	if s.hooks.Ended != nil {
		go s.hooks.Ended()
	}
	return closed
}

// Names the session in OpenCode's own history, so it reads there as it reads here.
func (s *Session) label(title string) {
	s.mu.Lock()
	c, id := s.client, s.sessionID
	s.mu.Unlock()
	if c != nil && id != "" {
		go func() {
			_, _ = c.Request(context.Background(), "PATCH", "/api/session/"+id, map[string]any{"title": title}, 0)
		}()
	}
}

// Files a session away in OpenCode's history, or brings it back, on this session's server. False when it has none to ask.
func (s *Session) file(ctx context.Context, sessionID string, archived bool) (bool, error) {
	s.mu.Lock()
	c, ended := s.client, s.ended
	s.mu.Unlock()
	if c == nil || ended {
		return false, nil
	}
	_, err := c.Request(ctx, "PATCH", "/api/session/"+sessionID, map[string]any{"archived": archived}, 0)
	return true, err
}

func (s *Session) begin(t *turn) {
	input := t.Input
	if !s.turns.Opened(t, func() error { return s.openOnce(input) }, openFailure) {
		s.Close()
		return
	}
	s.mu.Lock()
	c, sessionID := s.client, s.sessionID
	s.mu.Unlock()
	if !s.current(t) || c == nil || sessionID == "" {
		return
	}
	s.turns.Announce(domain.Continuation{Provider: string(domain.AgentEngineOpencode), Value: sessionID})
	ctx := context.Background()
	err := s.apply(ctx, c, sessionID, input)
	if err == nil {
		if !s.current(t) {
			return
		}
		s.mu.Lock()
		t.started = true
		s.mu.Unlock()
		if _, compact := input.Operation.(contracts.RunOperationCompact); compact {
			s.setCompacting(t, true, nil)
			if _, err = c.Request(ctx, "POST", "/api/session/"+sessionID+"/compact", map[string]any{}, 0); err == nil {
				return
			}
		} else {
			err = s.send(ctx, c, sessionID, t, input.Prompt, "queue")
		}
	}
	if err != nil {
		if s.current(t) {
			s.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: "OpenCode could not start the turn: " + agent.ReasonOf(err)})
		}
		return
	}
	s.mu.Lock()
	wanted := t.interruptWanted
	s.mu.Unlock()
	if wanted {
		s.interrupt(t)
		return
	}
	s.drainSteering(t)
}

// Puts a message in the session's inbox: a slash command as the command it names, a skill as that skill.
func (s *Session) send(ctx context.Context, c Client, sessionID string, t *turn, prompt, delivery string) error {
	var known, commands map[string]bool
	if namesSlash.MatchString(prompt) {
		s.mu.Lock()
		slashes := s.slashes
		s.mu.Unlock()
		if slashes != nil {
			<-slashes.done
			known, commands = slashes.skills, slashes.commands
		}
	}
	if command := slashCommand.FindStringSubmatch(jsTrim(prompt)); command != nil && commands[command[1]] {
		answer, err := requestAs[*inboxAnswer](ctx, c, "POST", "/api/session/"+sessionID+"/command", map[string]any{"name": command[1], "text": command[2], "delivery": delivery}, 0)
		if err != nil {
			return err
		}
		s.awaitDelivery(t, answer)
		return nil
	}
	var skills []map[string]string
	seen := map[string]bool{}
	for _, match := range slashSkill.FindAllStringSubmatchIndex(prompt, -1) {
		if end := match[1]; end < len(prompt) {
			next, _ := utf8.DecodeRuneInString(prompt[end:])
			if !isJSSpace(next) {
				continue
			}
		}
		name := prompt[match[2]:match[3]]
		if known[name] && !seen[name] {
			seen[name] = true
			skills = append(skills, map[string]string{"id": name})
		}
	}
	body := map[string]any{"text": prompt, "delivery": delivery}
	if len(skills) > 0 {
		body["skills"] = skills
	}
	answer, err := requestAs[*inboxAnswer](ctx, c, "POST", "/api/session/"+sessionID+"/prompt", body, 0)
	if err != nil {
		return err
	}
	s.awaitDelivery(t, answer)
	return nil
}

// The server may take a message before saying it has it, so one already taken is not waited on.
func (s *Session) awaitDelivery(t *turn, answer *inboxAnswer) {
	if answer == nil || answer.ID == nil || *answer.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !t.delivered[*answer.ID] {
		t.waiting[*answer.ID] = true
	}
}

// Brings the session to the run's model, effort, mode and tools, saying only what changed.
func (s *Session) apply(ctx context.Context, c Client, sessionID string, input *agent.ProviderRunInput) error {
	ref := ModelRefOf(input.Model, s.variantOf(input))
	if ref == nil {
		return &agent.OpenFailure{Message: fmt.Sprintf("OpenCode cannot run the %s model.", input.Model)}
	}
	s.mu.Lock()
	settings := s.settings
	s.mu.Unlock()
	if ref.ProviderID+ref.ID != settings.model || ref.Variant != settings.variant {
		if _, err := c.Request(ctx, "POST", "/api/session/"+sessionID+"/model", map[string]any{"model": ref}, 0); err != nil {
			return err
		}
		s.mu.Lock()
		s.settings.model, s.settings.variant = ref.ProviderID+ref.ID, ref.Variant
		s.mu.Unlock()
	}
	if name := AgentFor(input.Policy); name != settings.agent {
		if _, err := c.Request(ctx, "POST", "/api/session/"+sessionID+"/agent", map[string]any{"agent": name}, 0); err != nil {
			return err
		}
		s.mu.Lock()
		s.settings.agent = name
		s.mu.Unlock()
	}
	rules := s.rules(input)
	written, err := jsonx.Marshal(rules)
	if err != nil {
		return err
	}
	if string(written) != settings.rules {
		if _, err := c.Request(ctx, "PATCH", "/api/session/"+sessionID, map[string]any{"permissions": rules}, 0); err != nil {
			return err
		}
		s.mu.Lock()
		s.settings.rules = string(written)
		s.mu.Unlock()
	}
	return nil
}

// The effort is passed on only as a depth the model offers; one that offers none takes none.
func (s *Session) variantOf(input *agent.ProviderRunInput) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.variants[input.Model][string(input.Effort)] {
		return string(input.Effort)
	}
	return ""
}

func computerUseOf(input *agent.ProviderRunInput) (domain.ComputerUseRunConfigAvailable, bool) {
	config, ok := input.ComputerUse.(domain.ComputerUseRunConfigAvailable)
	return config, ok
}

func (s *Session) rules(input *agent.ProviderRunInput) []PermissionRule {
	_, computerUse := computerUseOf(input)
	sources := PolicySources{Policy: input.Policy, Channel: input.Channel, CoordinationRole: input.CoordinationRole}
	return PermissionRules(sources, Brief(input).Tools, computerUse)
}

// Opens the session for the first turn; every later turn shares that opening.
func (s *Session) openOnce(seed *agent.ProviderRunInput) error {
	s.mu.Lock()
	current := s.opening
	first := current == nil
	if first {
		current = &opening{done: make(chan struct{})}
		s.opening = current
	}
	s.mu.Unlock()
	if first {
		current.err = s.open(seed)
		close(current.done)
	}
	<-current.done
	return current.err
}

// A server's MCP entry, as OpenCode's configuration reads it.
type mcpServer struct {
	Type        string            `json:"type"`
	URL         string            `json:"url,omitzero"`
	Headers     map[string]string `json:"headers,omitzero"`
	OAuth       *bool             `json:"oauth,omitzero"`
	Command     []string          `json:"command,omitzero"`
	Environment map[string]string `json:"environment,omitzero"`
	Codemode    bool              `json:"codemode"`
}

type sessionConfig struct {
	MCP          map[string]mcpServer `json:"mcp"`
	Instructions []string             `json:"instructions"`
	Skills       []string             `json:"skills,omitzero"`
}

func (s *Session) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// Starts the server with the app's tools and instructions, and puts the run's session on it.
func (s *Session) open(seed *agent.ProviderRunInput) error {
	ctx := context.Background()
	brief := Brief(seed)
	served, err := agent.ServeTools(ctx, s.host, brief.Tools, s.isEnded, sessionEnded)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.served = served
	s.mu.Unlock()
	folder := filepath.Join(os.TempDir(), "aicodingtool-opencode")
	if err := os.MkdirAll(folder, 0o777); err != nil {
		return err
	}
	briefFile := filepath.Join(folder, application.NewID()+".md")
	s.mu.Lock()
	s.briefFile = briefFile
	s.mu.Unlock()
	if err := os.WriteFile(briefFile, []byte(strings.Join(brief.Instructions, "\n\n")), 0o600); err != nil {
		return err
	}
	config := sessionConfig{MCP: map[string]mcpServer{}, Instructions: []string{briefFile}}
	if served != nil {
		config.MCP[AppServerName] = mcpServer{Type: "remote", URL: served.URL, Headers: map[string]string{"Authorization": "Bearer " + served.Token}, OAuth: new(false)}
	}
	if computerUse, ok := computerUseOf(seed); ok {
		command := append([]string{computerUse.MCP.Command}, computerUse.MCP.Args...)
		config.MCP[ComputerUseServerName] = mcpServer{Type: "local", Command: command, Environment: computerUse.MCP.Env}
	}
	if skills := agent.AppSkillsRoot(s.pluginRoot); skills != "" {
		config.Skills = []string{skills}
	}
	c, err := s.connect(ctx, ServerOptions{Cwd: seed.WorkspaceRoot, Config: config, OnSpawn: s.hooks.Resources.RegisterProcess})
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		c.Close()
		return &agent.OpenFailure{Message: sessionEnded}
	}
	s.client = c
	s.unsubscribe = c.Subscribe(s.receive)
	slashes := &slashNames{done: make(chan struct{}), skills: map[string]bool{}, commands: map[string]bool{}}
	s.slashes = slashes
	s.mu.Unlock()
	go func() {
		<-c.Exited()
		s.lost("OpenCode stopped: " + exitMessage(c.Exit()))
	}()
	location := locationQuery(seed.WorkspaceRoot)
	go func() {
		var wait sync.WaitGroup
		wait.Go(func() {
			skills, _ := client.ReadLoaded[struct {
				ID string `json:"id"`
			}](ctx, c, "/api/skill?"+location, s.loadingRetry)
			for _, skill := range skills {
				slashes.skills[skill.ID] = true
			}
		})
		wait.Go(func() {
			commands, _ := client.ReadLoaded[struct {
				Name string `json:"name"`
			}](ctx, c, "/api/command?"+location, s.loadingRetry)
			for _, command := range commands {
				slashes.commands[command.Name] = true
			}
		})
		wait.Wait()
		close(slashes.done)
	}()
	models, _ := requestAs[[]*Model](ctx, c, "GET", "/api/model?"+location, nil, 0)
	s.mu.Lock()
	for _, model := range models {
		if model == nil {
			continue
		}
		id := AppModelID(model.ProviderID, model.ID)
		if model.Limit != nil && model.Limit.Context != nil && *model.Limit.Context != 0 && !math.IsNaN(*model.Limit.Context) {
			s.windows[id] = *model.Limit.Context
		}
		offered := map[string]bool{}
		for _, variant := range model.Variants {
			if variant != nil {
				offered[variant.ID] = true
			}
		}
		s.variants[id] = offered
	}
	s.mu.Unlock()
	continuation, continues := agent.ContinuationOf(seed)
	ref := ModelRefOf(seed.Model, s.variantOf(seed))
	if ref == nil {
		return &agent.OpenFailure{Message: fmt.Sprintf("OpenCode cannot run the %s model.", seed.Model)}
	}
	create := func() (*sessionInfo, error) {
		return requestAs[*sessionInfo](ctx, c, "POST", "/api/session", map[string]any{
			"location":    map[string]string{"directory": seed.WorkspaceRoot},
			"model":       ref,
			"agent":       AgentFor(seed.Policy),
			"permissions": s.rules(seed),
			"metadata":    map[string]string{"aicodingtool": seed.TaskID},
		}, 0)
	}
	var session *sessionInfo
	if !continues {
		if session, err = create(); err != nil {
			return err
		}
	} else {
		held, err := requestAs[*sessionInfo](ctx, c, "GET", "/api/session/"+continuation, nil, 0)
		if err != nil {
			var failure *client.Error
			if errors.As(err, &failure) && failure.Status == 404 {
				return &agent.OpenFailure{Message: fmt.Sprintf("OpenCode could not continue this session (%s). Start a new thread to keep going.", failure.Message), Lost: true}
			}
			return err
		}
		session = held
		// A session with nothing in it has nothing to copy, so a fork of one starts afresh.
		if seed.ForkContinuation {
			session, err = requestAs[*sessionInfo](ctx, c, "POST", "/api/session/"+continuation+"/fork", map[string]any{}, 0)
			var failure *client.Error
			if err != nil && errors.As(err, &failure) && failure.Status == 400 && emptySession.MatchString(failure.Message) {
				session, err = create()
			}
			if err != nil {
				return err
			}
		}
		if session == nil {
			session = &sessionInfo{}
		}
		// A session works where it was opened, and a fork where its parent was, so one taken into another checkout is moved there.
		if session.Location == nil || session.Location.Directory == nil || *session.Location.Directory != seed.WorkspaceRoot {
			if _, err := c.Request(ctx, "POST", "/api/session/"+session.ID+"/move", map[string]string{"directory": seed.WorkspaceRoot}, 0); err != nil {
				return err
			}
		}
	}
	if session == nil {
		session = &sessionInfo{}
	}
	s.mu.Lock()
	s.sessionID = session.ID
	s.mu.Unlock()
	if agent.NeedsSideChatBoundary(seed) {
		if _, err := c.Request(ctx, "POST", "/api/session/"+session.ID+"/synthetic", map[string]any{"text": agent.SideChatBoundary, "resume": false}, 0); err != nil {
			return err
		}
	}
	return nil
}

func exitMessage(exit *client.ExitStatus) string {
	if exit == nil {
		return (&client.Exited{Exit: client.ExitStatus{}}).Error()
	}
	return (&client.Exited{Exit: *exit}).Error()
}

func (s *Session) interrupt(t *turn) {
	s.mu.Lock()
	if !t.started {
		t.interruptWanted = true
		s.mu.Unlock()
		return
	}
	c, id := s.client, s.sessionID
	s.mu.Unlock()
	if c != nil && id != "" {
		go func() { _, _ = c.Request(context.Background(), "POST", "/api/session/"+id+"/interrupt", nil, 0) }()
	}
}

type steerTarget struct {
	client    Client
	sessionID string
}

// Feeds one run's steered messages into the session as it works, each taken at its next step.
func (s *Session) drainSteering(t *turn) {
	if t.Input.Steering == nil {
		return
	}
	target := func() (steerTarget, bool) {
		if !s.current(t) {
			return steerTarget{}, false
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.client == nil || s.sessionID == "" {
			return steerTarget{}, false
		}
		return steerTarget{s.client, s.sessionID}, true
	}
	agent.DrainSteering(context.Background(), t.Input, target, func(into steerTarget, prompt string) error {
		return s.send(context.Background(), into.client, into.sessionID, t, prompt, "steer")
	}, nil)
}

// A feed usually ends because its server died, whose own exit says why, so that is waited on briefly before the bare loss is reported.
func (s *Session) feedEnded() {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	go func() {
		var exited <-chan struct{}
		if c != nil {
			exited = c.Exited()
		}
		timer := time.NewTimer(exitWait)
		defer timer.Stop()
		select {
		case <-exited:
			s.lost("OpenCode stopped: " + exitMessage(c.Exit()))
		case <-timer.C:
			s.lost("OpenCode stopped sending events")
		}
	}()
}

// A server the session can no longer hear is given up on: its turn fails, and the next run opens a fresh one.
func (s *Session) lost(message string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	outcome := agent.ProviderResult{Status: domain.RunStatusFailed, Message: message}
	s.outcome = &outcome
	s.mu.Unlock()
	s.turns.Settle(outcome)
	s.Close()
}

// Whether an event belongs to this session or to one its own agents opened. Called with mu held.
func (s *Session) ownsLocked(sessionID string, ok bool) bool {
	return ok && (sessionID == s.sessionID || s.children[sessionID])
}

func (s *Session) owns(data record, key string) bool {
	id, ok := data.text(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ownsLocked(id, ok)
}

func (s *Session) receive(event client.Event) {
	if event.Type == client.FeedEnded {
		s.feedEnded()
		return
	}
	data, _ := recordOf(event.Data)
	if data == nil {
		data = record{}
	}
	switch event.Type {
	case "session.created":
		parent, hasParent := data.text("parentID")
		child, hasChild := data.text("sessionID")
		s.mu.Lock()
		if hasParent && s.ownsLocked(parent, true) && hasChild {
			s.children[child] = true
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
	case "permission.asked":
		if s.owns(data, "sessionID") {
			s.decide(data)
		}
		return
	case "form.created":
		raw, ok := data["form"]
		if !ok || raw.Kind() != '{' {
			return
		}
		var form FormInfo
		if jsonx.Unmarshal(raw, &form) != nil {
			return
		}
		formData, _ := recordOf(raw)
		if s.owns(formData, "sessionID") {
			s.ask(&form)
		}
		return
	}
	sessionID, hasSession := data.text("sessionID")
	s.mu.Lock()
	ours := s.sessionID
	lastTokens := s.lastTokens
	s.mu.Unlock()
	if _, present := data["sessionID"]; ours == "" && present || ours != "" && (!hasSession || sessionID != ours) {
		return
	}
	t, ok := s.turns.Turn()
	if !ok {
		return
	}
	input := t.Input
	switch event.Type {
	case "session.inbox.delivered":
		if inbox, ok := data.text("inboxID"); ok {
			s.mu.Lock()
			t.delivered[inbox] = true
			delete(t.waiting, inbox)
			s.mu.Unlock()
		}
	case "session.text.delta":
		key := data.jsString("assistantMessageID") + ":" + data.jsString("ordinal")
		if _, ok := t.streamed[key]; !ok {
			t.streamOrder = append(t.streamOrder, key)
		}
		delta, _ := data.text("delta")
		agent.StreamMarkdown(t.streamed, agent.EmitFunc(input.Emit), key, delta)
	case "session.text.ended":
		whole, _ := data.text("text")
		agent.EndMarkdown(t.streamed, agent.EmitFunc(input.Emit), data.jsString("assistantMessageID")+":"+data.jsString("ordinal"), whole)
	case "session.tool.input.started":
		id, hasID := data.text("id")
		name, hasName := data.text("name")
		if hasID && hasName {
			t.tools[id] = name
		}
	case "session.tool.called":
		id, ok := data.text("id")
		if !ok {
			return
		}
		name, ok := t.tools[id]
		if !ok {
			name = "tool"
		}
		intent := IntentOf(id, name, data["input"], input.WorkspaceRoot)
		t.intents[id] = intent
		input.Emit(agent.EventTool{Intent: intent})
	case "session.step.ended":
		tokens := tokensOf(data["tokens"])
		if tokens == 0 {
			return
		}
		s.mu.Lock()
		s.lastTokens = tokens
		limit := s.windows[input.Model]
		s.mu.Unlock()
		input.Emit(agent.EventSpend{Tokens: tokens})
		if limit != 0 {
			input.Emit(agent.EventUsage{Tokens: tokens, Limit: limit, Model: input.Model})
		}
	case "session.retry.scheduled":
		input.Emit(agent.EventRetry{RetryNotice: domain.RetryNotice{Message: messageOf(data, "OpenCode is retrying the request.")}})
	case "session.compaction.started":
		s.setCompacting(t, true, nil)
	case "session.compaction.ended":
		_, manual := input.Operation.(contracts.RunOperationCompact)
		trigger := contracts.RunEventContextCompactedTriggerAuto
		if manual {
			trigger = contracts.RunEventContextCompactedTriggerManual
		}
		t.compactMu.Lock()
		pre := lastTokens
		if t.CompactionPreTokens != nil {
			pre = *t.CompactionPreTokens
		}
		t.compactMu.Unlock()
		input.Emit(agent.EventCompaction{Trigger: trigger, PreTokens: pre})
		s.setCompacting(t, false, nil)
		if manual {
			s.turns.Settle(agent.ProviderResult{Status: domain.RunStatusSucceeded})
		}
	case "session.compaction.failed":
		reason := "OpenCode could not compact this session."
		if message, ok := data.text("message"); ok {
			reason = message
		}
		failure := "Could not compact context: " + reason
		s.setCompacting(t, false, &failure)
		if _, manual := input.Operation.(contracts.RunOperationCompact); manual {
			s.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed})
		}
	case "session.execution.failed":
		s.finish(t, agent.ProviderResult{Status: domain.RunStatusFailed, Message: messageOf(data, "OpenCode could not finish the turn.")})
	case "session.execution.interrupted":
		s.finish(t, agent.ProviderResult{Status: domain.RunStatusCancelled})
	case "session.execution.succeeded":
		s.finish(t, agent.ProviderResult{Status: domain.RunStatusSucceeded})
	}
}

// The event's own message, or its error's, or fallback.
func messageOf(data record, fallback string) string {
	if message, ok := data.text("message"); ok {
		return message
	}
	if failure, ok := data.object("error"); ok {
		if message, ok := failure.text("message"); ok {
			return message
		}
	}
	return fallback
}

// A turn ends with the execution that took its last message; one still waiting in the inbox runs next.
func (s *Session) finish(t *turn, result agent.ProviderResult) {
	if !s.current(t) {
		return
	}
	if _, compact := t.Input.Operation.(contracts.RunOperationCompact); compact {
		return
	}
	s.mu.Lock()
	started, waiting := t.started, len(t.waiting)
	s.mu.Unlock()
	if !started || result.Status == domain.RunStatusSucceeded && waiting > 0 {
		return
	}
	for _, key := range t.streamOrder {
		if buffer := t.streamed[key]; buffer != nil && buffer.Text != "" {
			t.Input.Emit(agent.EventAssistant{MessageID: key, Text: buffer.Text, Append: true})
		}
	}
	clear(t.streamed)
	t.streamOrder = nil
	s.turns.Settle(result)
	if s.hooks.Rested != nil {
		s.hooks.Rested()
	}
}

func (s *Session) setCompacting(t *turn, compacting bool, err *string) {
	if !s.current(t) {
		return
	}
	s.mu.Lock()
	lastTokens := s.lastTokens
	s.mu.Unlock()
	t.compactMu.Lock()
	defer t.compactMu.Unlock()
	agent.ReportCompacting(t.Input, &t.Compacting, compacting, lastTokens, err)
}

func signalOf(input *agent.ProviderRunInput) context.Context {
	if input.Signal != nil {
		return input.Signal
	}
	return context.Background()
}

// Puts a tool the rules left to ask about to the run's user. Nothing is allowed on a session with no run to ask.
func (s *Session) decide(request record) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	t, hasTurn := s.turns.Turn()
	id, hasID := request.text("id")
	sessionID, hasSession := request.text("sessionID")
	if c == nil || !hasID || !hasSession {
		return
	}
	// A rejection with a message is told to the model, which goes on past it; one without stops the turn.
	reply := func(decision, message string) {
		body := map[string]string{"decision": decision}
		if message != "" {
			body["message"] = message
		}
		_, _ = c.Request(context.Background(), "POST", "/api/session/"+sessionID+"/permission/"+id+"/reply", body, 0)
	}
	if !hasTurn {
		go reply("reject", "")
		return
	}
	source, _ := request.object("source")
	metadata, _ := request.object("metadata")
	action, ok := request.text("action")
	if !ok {
		action = "tool"
	}
	input, ok := metadata["input"]
	if !ok || input.Kind() != '{' {
		input = jsontext.Value("{}")
		if resources, ok := request["resources"]; ok {
			input, _ = jsonx.Marshal(map[string]jsontext.Value{"resources": resources})
		}
	}
	// The call the rules asked about, as it was shown, says more than the rule's own name for it.
	sourceID, hasSourceID := source.text("id")
	intent, shown := t.intents[sourceID]
	if !shown {
		toolID := id
		if hasSourceID {
			toolID = sourceID
		}
		intent = IntentOf(toolID, action, input, t.Input.WorkspaceRoot)
	}
	run := t.Input
	go func() {
		if run.Authorize == nil {
			reply("reject", "")
			return
		}
		decision := run.Authorize(signalOf(run), intent)
		if decision.Allowed {
			reply("once", "")
			return
		}
		reply("reject", agent.DenialReason(decision))
	}()
}

// A form goes to the user as the composer's questions; one nobody answers is withdrawn.
func (s *Session) ask(form *FormInfo) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	t, hasTurn := s.turns.Turn()
	request := QuestionsOf(form)
	path := "/api/session/" + form.SessionID + "/form/" + form.ID
	cancel := func() {
		if c != nil {
			_, _ = c.Request(context.Background(), "DELETE", path, nil, 0)
		}
	}
	if c == nil || !hasTurn || request == nil || t.Input.AskQuestion == nil {
		go cancel()
		return
	}
	run := t.Input
	go func() {
		answers, ok := run.AskQuestion(signalOf(run), *request)
		if !ok || answers == nil {
			cancel()
			return
		}
		if _, err := c.Request(context.Background(), "POST", path+"/reply", map[string]any{"answer": FormAnswer(form, answers)}, 0); err != nil {
			cancel()
		}
	}()
}
