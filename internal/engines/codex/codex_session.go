package codex

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/agent"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	signInMessage = "Sign in to Codex to run this thread."
	endedMessage  = "The Codex session ended before the run could start."
)

type goalCommandKind int

const (
	goalGet goalCommandKind = iota + 1
	goalSet
	goalClear
)

type goalCommandOf struct {
	kind      goalCommandKind
	objective string
}

const jsSpaceClass = `\s\x{0B}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}\x{FEFF}`

var goalPattern = regexp.MustCompile(`^/goal(?:[` + jsSpaceClass + `]+((?s:.*)))?$`)

func goalCommand(prompt string) *goalCommandOf {
	match := goalPattern.FindStringSubmatch(jsTrim(prompt))
	if match == nil {
		return nil
	}
	argument := jsTrim(match[1])
	switch {
	case strings.ToLower(argument) == "clear":
		return &goalCommandOf{kind: goalClear}
	case argument != "":
		return &goalCommandOf{kind: goalSet, objective: argument}
	}
	return &goalCommandOf{kind: goalGet}
}

func errorInfoIs(info *protocol.CodexErrorInfo, name string) bool {
	if info == nil {
		return false
	}
	raw, err := protocol.Marshal(info)
	return err == nil && string(raw) == `"`+name+`"`
}

// Why the server is retrying a request, in the user's terms.
func retryReason(turnError protocol.TurnError) string {
	info := turnError.CodexErrorInfo
	switch {
	case errorInfoIs(info, "serverOverloaded"):
		return "Codex is overloaded."
	case errorInfoIs(info, "rateLimitExceeded"):
		return "Codex is rate limited."
	case errorInfoIs(info, "internalServerError"):
		return "Codex API error."
	}
	if info != nil {
		raw, _ := protocol.Marshal(info)
		if object, ok := readObject(raw); ok {
			for _, member := range object {
				if member.key == "httpConnectionFailed" || member.key == "responseStreamConnectionFailed" || member.key == "responseStreamDisconnected" {
					return "Cannot reach Codex."
				}
			}
		}
	}
	return agent.FirstLine(turnError.Message)
}

func describeExit(exit protocol.ExitStatus) string {
	if line := agent.FirstLine(exit.Stderr); line != "" {
		return line
	}
	if exit.Signal != "" {
		return "signal " + exit.Signal
	}
	if exit.Code == nil {
		return "exit code null"
	}
	return fmt.Sprintf("exit code %d", *exit.Code)
}

func startFailure(exit protocol.ExitStatus) string {
	return "Codex could not start: " + describeExit(exit)
}

func openFailure(err error) string {
	var open *agent.OpenFailure
	if errors.As(err, &open) {
		return open.Message
	}
	var exited *protocol.AppServerExited
	if errors.As(err, &exited) {
		return startFailure(exited.Exit)
	}
	return "Codex could not start: " + reasonOf(err)
}

type commandInput struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

type fileChangeEntry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Diff string `json:"diff"`
}

type fileChangeInput struct {
	Path    string            `json:"path"`
	Changes []fileChangeEntry `json:"changes"`
}

func encoded(value any) jsontext.Value {
	data, err := jsonx.Marshal(value)
	if err != nil {
		return jsontext.Value("{}")
	}
	return data
}

// What an item the agent started is about, in the shape the thread records tools in. Nil for the
// kinds it drops.
func IntentOf(item protocol.ThreadItem) *domain.ToolIntent {
	switch item.Type {
	case protocol.ItemCommandExecution:
		return &domain.ToolIntent{ToolID: item.ID, Name: "command_execution", Input: encoded(commandInput{deref(item.Command), deref(item.Cwd)})}
	case protocol.ItemFileChange:
		changes := make([]fileChangeEntry, 0, len(item.Changes))
		for _, change := range item.Changes {
			changes = append(changes, fileChangeEntry{change.Path, change.Kind.Type, change.Diff})
		}
		intent := &domain.ToolIntent{ToolID: item.ID, Name: "file_change"}
		path := ""
		if len(item.Changes) > 0 {
			path = item.Changes[0].Path
			intent.WritePath = &path
		}
		intent.Input = encoded(fileChangeInput{path, changes})
		return intent
	case protocol.ItemMcpToolCall:
		input := item.Arguments
		if input.Kind() != '{' {
			input = jsonObject{}.value()
			if len(item.Arguments) > 0 {
				input = jsonObject{}.with("arguments", item.Arguments).value()
			}
		}
		return &domain.ToolIntent{ToolID: item.ID, Name: deref(item.Tool), Input: input}
	case protocol.ItemWebSearch:
		return &domain.ToolIntent{ToolID: item.ID, Name: "web_search", Input: encoded(queryDetail{deref(item.Query)})}
	case protocol.ItemImageGeneration:
		return &domain.ToolIntent{ToolID: item.ID, Name: "image_generation", Input: jsontext.Value(`{"description":"Generating image"}`)}
	}
	return nil
}

// Grants what was asked for, whole: the question is yes or no, not which part.
func granted(requested protocol.RequestPermissionProfile) protocol.GrantedPermissionProfile {
	var profile protocol.GrantedPermissionProfile
	if truthy(requested.Network) {
		profile.Network = requested.Network
	}
	if truthy(requested.FileSystem) {
		profile.FileSystem = requested.FileSystem
	}
	return profile
}

var toolNamePattern = regexp.MustCompile(`run tool "([^"]+)"`)

// The tool an MCP approval prompt names, which the prompt carries only in its wording.
func toolNamed(message string) (string, bool) {
	match := toolNamePattern.FindStringSubmatch(message)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// One turn a run asked for, and everything it builds up as it streams. Fields past the embedded
// ones are the session's, read and written under its lock.
type codexTurn struct {
	*agent.SessionTurn
	agent.Compacting
	// Ends once the run has its answer, which is how long steering is drained for.
	done context.Context
	// Named by the server once turn/start returns; an interrupt asked for before then waits on it.
	turnID          string
	interruptWanted bool
	// Assistant text still buffered per item, so a half-written block never ships.
	streamed map[string]*agent.MarkdownBuffer
	// Tool items started and not yet completed, by id: what an approval for one of them is about.
	items *ordered[string, protocol.ThreadItem]
	// Image persistence may finish after Codex reports the end of the turn.
	images   sync.WaitGroup
	imageIDs map[string]bool
	// The last error the server said it would not retry, kept for the turn's failure.
	failure *protocol.TurnError
	// The final native review report, used to avoid repeating its following assistant message.
	reviewOutput *string
}

type opening struct {
	done chan struct{}
	err  error
}

// One live Codex thread, kept across turns. The app server process belongs to the session, so a
// second turn neither pays for a new process nor resumes the thread from disk. Each turn takes the
// session in turn, and ends on the server's own turn/completed.
type CodexSession struct {
	key         string
	connect     CodexConnect
	host        agent.ToolHost
	hooks       agent.SessionHooks
	imageOutput ImageOutput
	questions   *CodexQuestions
	turns       *agent.TurnSlot[*codexTurn]
	// What this session's threads spend, counted on the run in flight.
	spend *CodexSpend
	// The terminals this thread's app-server session has running.
	background *agent.BackgroundWork
	// What this app leaves in Codex's own record of the thread.
	record    *CodexThreadRecord
	live      atomic.Bool
	subagents atomic.Pointer[CodexSubagents]
	// What Codex calls this thread. A later run resumes it by this id.
	thread atomic.Pointer[string]

	mu     sync.Mutex
	ended  bool
	client CodexClient
	// The app's tools as this session's process reaches them; released with the session.
	served  *agent.ServedTools
	opening *opening
	// Closed once the process this session held has exited.
	closed chan struct{}
	// How the session ended, kept for a run that arrives after it is already over.
	outcome *agent.ProviderResult
	// The context the last request carried, which is what a compaction is measured from.
	lastTokens float64
	goalActive bool
	reportGoal func(report contracts.GoalReport)
	// A newer read supersedes an older one that is still paging through the server.
	backgroundRead int
	skills         *CodexSkills
}

func newCodexSession(key string, connect CodexConnect, host agent.ToolHost, hooks agent.SessionHooks, readOrigin ReadOrigin, imageOutput ImageOutput) *CodexSession {
	session := &CodexSession{
		key:         key,
		connect:     connect,
		host:        host,
		hooks:       hooks,
		imageOutput: imageOutput,
		questions:   NewCodexQuestions(),
		record:      NewCodexThreadRecord(readOrigin),
		reportGoal:  func(contracts.GoalReport) {},
	}
	session.live.Store(true)
	session.turns = agent.NewTurnSlot[*codexTurn](agent.InterruptGrace, func() { session.Close() })
	session.spend = NewCodexSpend(func(event agent.ProviderEvent) {
		if turn, ok := session.turns.Turn(); ok {
			turn.Input.Emit(event)
		}
	})
	session.background = agent.NewBackgroundWork(session.Busy, session.rested)
	return session
}

func (session *CodexSession) rested() {
	if session.hooks.Rested != nil {
		go session.hooks.Rested()
	}
}

func (session *CodexSession) Key() string { return session.key }

func (session *CodexSession) Live() bool { return session.live.Load() }

// A turn is in flight, so the session owes an answer before it can take another.
func (session *CodexSession) Answering() bool { return session.turns.Answering() }

// Closing the app server would stop child turns and background terminals, so either keeps it warm.
func (session *CodexSession) Busy() bool {
	if session.Answering() || session.background.Running() {
		return true
	}
	subagents := session.subagents.Load()
	return subagents != nil && subagents.Busy()
}

// What Codex calls this thread, once it has one.
func (session *CodexSession) ThreadID() string {
	if id := session.thread.Load(); id != nil {
		return *id
	}
	return ""
}

// Whether this session is the one a run means to continue.
func (session *CodexSession) Continues(continuation *string) bool {
	return continuation == nil || *continuation == session.ThreadID()
}

func (session *CodexSession) current(turn *codexTurn) bool {
	held, ok := session.turns.Turn()
	return ok && held == turn
}

func (session *CodexSession) turn() *codexTurn {
	held, _ := session.turns.Turn()
	return held
}

type codexTurns struct{ session *CodexSession }

func (engine codexTurns) Open(base *agent.SessionTurn) *codexTurn {
	return &codexTurn{SessionTurn: base, streamed: map[string]*agent.MarkdownBuffer{}, items: newOrdered[string, protocol.ThreadItem](), imageIDs: map[string]bool{}}
}

func (engine codexTurns) Begin(turn *codexTurn) {
	engine.session.record.Label(turn.Input.Title)
	go engine.session.begin(turn)
}

func (engine codexTurns) Interrupt(turn *codexTurn) { engine.session.interrupt(turn) }

func (session *CodexSession) Run(input *agent.ProviderRunInput) (agent.ProviderResult, error) {
	session.mu.Lock()
	if session.ended {
		outcome := session.outcome
		session.mu.Unlock()
		if outcome != nil {
			return *outcome, nil
		}
		return agent.ProviderResult{Status: domain.RunStatusFailed, Message: endedMessage}, nil
	}
	session.mu.Unlock()
	done, finish := context.WithCancel(context.Background())
	engine := codexTurns{session}
	result := session.turns.Take(input, turnsWithDone{engine, done})
	finish()
	// The questions the server asked are the turn's: nobody is left to answer them once it settles.
	session.questions.Close()
	return result, nil
}

type turnsWithDone struct {
	codexTurns
	done context.Context
}

func (engine turnsWithDone) Open(base *agent.SessionTurn) *codexTurn {
	turn := engine.codexTurns.Open(base)
	turn.done = engine.done
	return turn
}

func (session *CodexSession) Close() <-chan struct{} {
	session.mu.Lock()
	if session.ended {
		closed := session.closed
		session.mu.Unlock()
		if closed == nil {
			return agent.Settled()
		}
		return closed
	}
	session.ended = true
	session.live.Store(false)
	session.backgroundRead++
	client, served, reportGoal := session.client, session.served, session.reportGoal
	session.client, session.served = nil, nil
	closed := make(chan struct{})
	session.closed = closed
	session.mu.Unlock()
	session.background.Clear()
	reportGoal(contracts.GoalReport{})
	session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusCancelled})
	if subagents := session.subagents.Swap(nil); subagents != nil {
		subagents.Close()
	}
	go func() {
		if client != nil {
			client.Close()
		}
		close(closed)
	}()
	if served != nil && served.Release != nil {
		served.Release()
	}
	if session.hooks.Ended != nil {
		go session.hooks.Ended()
	}
	return closed
}

// Names this thread in Codex's own history, so it reads there as it reads here.
func (session *CodexSession) Label(title string) { session.record.Label(title) }

// Stops one subagent turn or terminal owned by this thread; a terminal's stop republishes what the
// server still has.
func (session *CodexSession) StopProcess(processID string) {
	session.mu.Lock()
	client := session.client
	session.mu.Unlock()
	threadID := session.ThreadID()
	if client == nil || threadID == "" {
		return
	}
	if subagents := session.subagents.Load(); subagents != nil {
		if child, held := subagents.Stop(processID); child != nil || held {
			if child != nil {
				go func() {
					_, _ = client.Request(context.Background(), "turn/interrupt", protocol.TurnInterruptParams{ThreadID: child.ThreadID, TurnID: child.TurnID})
					session.terminateTerminals(client, processID)
				}()
			}
			return
		}
	}
	go func() {
		_, _ = client.Request(context.Background(), "thread/backgroundTerminals/terminate", protocol.BackgroundTerminalsTerminateParams{ThreadID: threadID, ProcessID: processID})
		session.refreshBackgroundProcesses()
	}()
}

func (session *CodexSession) openingFor(seed *agent.ProviderRunInput) *opening {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.opening == nil {
		started := &opening{done: make(chan struct{})}
		session.opening = started
		go func() {
			started.err = session.open(seed)
			close(started.done)
		}()
	}
	return session.opening
}

func (session *CodexSession) begin(turn *codexTurn) {
	opened := session.openingFor(turn.Input)
	if !session.turns.Opened(turn, func() error { <-opened.done; return opened.err }, openFailure) {
		session.Close()
		return
	}
	session.mu.Lock()
	client := session.client
	session.mu.Unlock()
	threadID := session.ThreadID()
	if !session.current(turn) || client == nil || threadID == "" {
		return
	}
	session.turns.Announce(domain.Continuation{Provider: string(domain.AgentEngineCodex), Value: threadID})
	switch turn.Input.Operation.(type) {
	case contracts.RunOperationCompact:
		session.beginCompaction(turn, client, threadID)
		return
	case contracts.RunOperationReview:
		session.beginReview(turn, client, threadID)
		return
	}
	goal := goalCommand(turn.Input.Prompt)
	if goal != nil && !session.beginGoal(turn, client, threadID, *goal) {
		return
	}
	policy := Policy(turn.Input.Policy, turn.Input.CoordinationRole)
	prompt := turn.Input.Prompt
	if goal != nil && goal.kind == goalSet {
		prompt = goal.objective
	}
	started, err := call[protocol.TurnResponse](context.Background(), client, "turn/start", protocol.TurnStartParams{
		ThreadID:          threadID,
		Input:             session.skills.Input(prompt),
		Model:             ptr(turn.Input.Model),
		Effort:            ptr(string(turn.Input.Effort)),
		ServiceTier:       ptr(serviceTier(turn.Input.FastMode)),
		ApprovalPolicy:    &policy.ApprovalPolicy,
		ApprovalsReviewer: &policy.ApprovalsReviewer,
		SandboxPolicy:     SandboxPolicy(policy.Sandbox, turn.Input.ProjectRoot, nil),
	})
	if err != nil {
		if session.current(turn) {
			session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start the turn: " + reasonOf(err)})
		}
		return
	}
	session.started(turn, started.Turn.ID)
}

// The server named the turn: what was held for that is let go, and steering may begin.
func (session *CodexSession) started(turn *codexTurn, turnID string) {
	session.mu.Lock()
	turn.turnID = turnID
	wanted := turn.interruptWanted
	session.mu.Unlock()
	session.record.Began()
	if !session.current(turn) {
		return
	}
	// A run that went away while the server was starting its turn leaves nothing running behind it.
	if wanted {
		session.interrupt(turn)
		return
	}
	session.drainSteering(turn)
}

func serviceTier(fastMode *bool) string {
	if fastMode != nil && *fastMode {
		return "priority"
	}
	return "default"
}

func (session *CodexSession) beginGoal(turn *codexTurn, client CodexClient, threadID string, command goalCommandOf) bool {
	fail := func(err error) bool {
		if session.current(turn) {
			session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not update the goal: " + reasonOf(err)})
		}
		return false
	}
	switch command.kind {
	case goalClear:
		if _, err := client.Request(context.Background(), "thread/goal/clear", protocol.ThreadIDParams{ThreadID: threadID}); err != nil {
			return fail(err)
		}
		session.mu.Lock()
		session.goalActive = false
		session.mu.Unlock()
		turn.Input.ReportGoal(contracts.GoalReport{})
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusSucceeded})
		return false
	case goalGet:
		result, err := call[protocol.ThreadGoalGetResponse](context.Background(), client, "thread/goal/get", protocol.ThreadIDParams{ThreadID: threadID})
		if err != nil {
			return fail(err)
		}
		session.reportCodexGoal(result.Goal)
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusSucceeded})
		return false
	}
	result, err := call[protocol.ThreadGoalSetResponse](context.Background(), client, "thread/goal/set", protocol.ThreadGoalSetParams{ThreadID: threadID, Objective: &command.objective})
	if err != nil {
		return fail(err)
	}
	session.reportCodexGoal(&result.Goal)
	return true
}

func (session *CodexSession) reportCodexGoal(goal *protocol.ThreadGoal) {
	session.mu.Lock()
	session.goalActive = goal != nil && goal.Status == "active"
	report := session.reportGoal
	session.mu.Unlock()
	var active *domain.ActiveGoal
	if goal != nil && goal.Status != "complete" {
		status := domain.ActiveGoalStatusBlocked
		if goal.Status == "active" {
			status = domain.ActiveGoalStatusActive
		}
		active = &domain.ActiveGoal{Objective: goal.Objective, Status: status}
	}
	report(contracts.GoalReport{Goal: active})
}

// Manual compaction is a thread operation, not an empty model turn.
func (session *CodexSession) beginCompaction(turn *codexTurn, client CodexClient, threadID string) {
	session.setCompacting(turn, true, nil)
	if _, err := client.Request(context.Background(), "thread/compact/start", protocol.ThreadIDParams{ThreadID: threadID}); err != nil {
		if !session.current(turn) {
			return
		}
		session.setCompacting(turn, false, ptr("Could not compact context: "+reasonOf(err)))
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed})
	}
}

func reviewTarget(target domain.ReviewTarget) protocol.ReviewTarget {
	switch target := target.(type) {
	case domain.ReviewTargetBaseBranch:
		return protocol.BaseBranchTarget{Branch: target.Branch}
	case domain.ReviewTargetCommit:
		return protocol.CommitTarget{Sha: target.Sha, Title: target.Title}
	case domain.ReviewTargetCustom:
		return protocol.CustomTarget{Instructions: target.Instructions}
	}
	return protocol.UncommittedChangesTarget{}
}

// Inline delivery keeps the isolated native reviewer compatible with paginated history.
func (session *CodexSession) beginReview(turn *codexTurn, client CodexClient, threadID string) {
	operation, ok := turn.Input.Operation.(contracts.RunOperationReview)
	if !ok {
		return
	}
	started, err := call[protocol.ReviewStartResponse](context.Background(), client, "review/start", protocol.ReviewStartParams{ThreadID: threadID, Target: reviewTarget(operation.Target), Delivery: ptr("inline")})
	if err != nil {
		if session.current(turn) {
			session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not start the review: " + reasonOf(err)})
		}
		return
	}
	session.started(turn, started.Turn.ID)
}

func (session *CodexSession) isEnded() bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.ended
}

type injectedContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type injectedMessage struct {
	Type    string            `json:"type"`
	Role    string            `json:"role"`
	Content []injectedContent `json:"content"`
}

// Spawns the server and puts the thread on it: a new one, the one the run continues, or a fork of
// it. The app's tools are served first, since the process connects to them as the thread starts.
func (session *CodexSession) open(seed *agent.ProviderRunInput) error {
	ctx := context.Background()
	reportBackground, reportGoal, reportSubagent := seed.ReportBackground, seed.ReportGoal, seed.ReportSubagent
	if reportBackground == nil {
		reportBackground = func(contracts.BackgroundReport) {}
	}
	if reportGoal == nil {
		reportGoal = func(contracts.GoalReport) {}
	}
	if reportSubagent == nil {
		reportSubagent = func(domain.SubagentReport) {}
	}
	session.background.OpenWith(reportBackground)
	session.mu.Lock()
	session.reportGoal = reportGoal
	session.mu.Unlock()
	reportGoal(contracts.GoalReport{})
	brief := CodexBrief(seed)
	served, err := agent.ServeTools(ctx, session.host, brief.Tools, session.isEnded, endedMessage)
	if err != nil {
		return err
	}
	session.mu.Lock()
	session.served = served
	session.mu.Unlock()
	options := AppServerOptions{Cwd: seed.WorkspaceRoot}
	if served != nil {
		options.Env = childEnvironment()
		options.Env[ToolTokenEnv] = served.Token
	}
	command, err := CodexAppServer(CodexConfig(configSources(seed), served, brief.Tools), options)
	if err != nil {
		return err
	}
	command.OnSpawn = session.hooks.Resources.RegisterProcess
	session.mu.Lock()
	if session.ended {
		session.mu.Unlock()
		return &agent.OpenFailure{Message: endedMessage}
	}
	client := session.connect(command)
	session.client = client
	skills := NewCodexSkills(client, seed.WorkspaceRoot)
	session.skills = skills
	session.mu.Unlock()
	subagents := NewCodexSubagents(reportSubagent, func(busy bool) {
		if !busy && !session.Answering() {
			session.rested()
		}
	}, func(ctx context.Context, threadID string) (protocol.Thread, error) {
		read, err := call[protocol.ThreadResponse](ctx, client, "thread/read", protocol.ThreadReadParams{ThreadID: threadID, IncludeTurns: ptr(false)})
		return read.Thread, err
	}, session.spend)
	session.subagents.Store(subagents)
	session.listen(client, subagents, skills)
	go func() {
		<-client.Exited()
		if exit := client.Exit(); exit != nil {
			session.exited(*exit)
		} else {
			session.exited(protocol.ExitStatus{})
		}
	}()
	if err := initialize(ctx, client); err != nil {
		return err
	}
	if err := adoptAppSkills(ctx, client); err != nil {
		return err
	}
	<-skills.Refresh(true)
	account, err := call[protocol.GetAccountResponse](ctx, client, "account/read", protocol.GetAccountParams{RefreshToken: ptr(false)})
	if err != nil {
		return err
	}
	if account.Account == nil {
		return &agent.OpenFailure{Message: signInMessage}
	}
	policy := Policy(seed.Policy, seed.CoordinationRole)
	effort := encoded(string(seed.Effort))
	instructions := strings.Join(brief.Instructions, "\n\n")
	tier := serviceTier(seed.FastMode)
	config := map[string]jsontext.Value{"model_reasoning_effort": effort}
	var started protocol.ThreadSessionResponse
	continuation, continues := agent.ContinuationOf(seed)
	switch {
	case !continues:
		started, err = call[protocol.ThreadSessionResponse](ctx, client, "thread/start", protocol.ThreadStartParams{
			Cwd: &seed.WorkspaceRoot, Model: ptr(seed.Model), ServiceTier: &tier, ApprovalPolicy: &policy.ApprovalPolicy, Sandbox: &policy.Sandbox,
			ApprovalsReviewer: &policy.ApprovalsReviewer, Config: config, DeveloperInstructions: &instructions,
		})
	case seed.ForkContinuation:
		started, err = call[protocol.ThreadSessionResponse](ctx, client, "thread/fork", protocol.ThreadForkParams{
			ThreadID: continuation, Cwd: &seed.WorkspaceRoot, Model: ptr(seed.Model), ServiceTier: &tier, ApprovalPolicy: &policy.ApprovalPolicy, Sandbox: &policy.Sandbox,
			ApprovalsReviewer: &policy.ApprovalsReviewer, Config: config, DeveloperInstructions: &instructions, ExcludeTurns: ptr(true),
		})
	default:
		started, err = resumeThread(ctx, client, protocol.ThreadResumeParams{
			ThreadID: continuation, Cwd: &seed.WorkspaceRoot, Model: ptr(seed.Model), ServiceTier: &tier, ApprovalPolicy: &policy.ApprovalPolicy, Sandbox: &policy.Sandbox,
			ApprovalsReviewer: &policy.ApprovalsReviewer, Config: config, DeveloperInstructions: &instructions,
		})
		// Only the server's own refusal says the thread is gone; a server that died may still have it.
		if err != nil && isAppServerError(err) {
			return &agent.OpenFailure{Message: fmt.Sprintf("Codex could not continue this thread (%s). Start a new thread to keep going.", reasonOf(err)), Lost: true}
		}
	}
	if err != nil {
		return err
	}
	if agent.NeedsSideChatBoundary(seed) {
		boundary := injectedMessage{Type: "message", Role: "user", Content: []injectedContent{{Type: "input_text", Text: agent.SideChatBoundary}}}
		if _, err := client.Request(ctx, "thread/inject_items", protocol.ThreadInjectItemsParams{ThreadID: started.Thread.ID, Items: []any{boundary}}); err != nil {
			return err
		}
	}
	threadID := started.Thread.ID
	session.thread.Store(&threadID)
	session.record.Opened(client, threadID, seed.WorkspaceRoot)
	// A thread that came back from disk already has the rollout the record is written against.
	if continues {
		session.record.Began()
	}
	subagents.SetRootThreadID(threadID)
	return nil
}

// Routes the server's notifications and requests: a child's to the subagents, the thread's to the turn in flight.
func (session *CodexSession) listen(client CodexClient, subagents *CodexSubagents, skills *CodexSkills) {
	ours := func(threadID string) bool { return threadID == session.ThreadID() }
	onNotification(client, protocol.NotifyThreadStarted, func(params protocol.ThreadStartedNotification) { subagents.ThreadStarted(params) })
	onNotification(client, protocol.NotifyThreadStatusChanged, func(params protocol.ThreadStatusChangedNotification) { subagents.ThreadStatusChanged(params) })
	onNotification(client, protocol.NotifyThreadClosed, func(params protocol.ThreadNotification) { subagents.ThreadClosed(params) })
	onNotification(client, protocol.NotifyTurnStarted, func(params protocol.TurnNotification) {
		if subagents.TurnStarted(params) {
			if held := subagents.TakeHeldStop(params.ThreadID); held != nil {
				go client.Request(context.Background(), "turn/interrupt", protocol.TurnInterruptParams{ThreadID: held.ThreadID, TurnID: held.TurnID})
			}
			return
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		if turn := session.turn(); ours(params.ThreadID) && turn != nil && session.goalActive {
			turn.turnID = params.Turn.ID
		}
	})
	onNotification(client, protocol.NotifyThreadGoalUpdated, func(params protocol.ThreadGoalUpdatedNotification) {
		if ours(params.ThreadID) {
			session.reportCodexGoal(&params.Goal)
		}
	})
	onNotification(client, protocol.NotifyThreadGoalCleared, func(params protocol.ThreadNotification) {
		if !ours(params.ThreadID) {
			return
		}
		session.mu.Lock()
		session.goalActive = false
		report := session.reportGoal
		session.mu.Unlock()
		report(contracts.GoalReport{})
	})
	client.On(protocol.NotifySkillsChanged, func(jsontext.Value) { skills.Refresh(true) })
	onNotification(client, protocol.NotifyItemStarted, func(params protocol.ItemStartedNotification) {
		if !subagents.ItemStarted(params) && ours(params.ThreadID) {
			session.receiveStarted(params.Item)
		}
	})
	onNotification(client, protocol.NotifyAgentMessageDelta, func(params protocol.AgentMessageDeltaNotification) {
		if ours(params.ThreadID) {
			session.receiveDelta(params.ItemID, params.Delta)
		}
	})
	onNotification(client, protocol.NotifyItemCompleted, func(params protocol.ItemCompletedNotification) {
		child := subagents.ItemCompleted(params)
		if !child && ours(params.ThreadID) {
			session.receiveCompleted(params.Item)
		}
		if ours(params.ThreadID) && params.Item.Type == protocol.ItemCommandExecution && session.background.Running() {
			go session.refreshBackgroundProcesses()
		}
	})
	onNotification(client, protocol.NotifyThreadTokenUsage, func(params protocol.ThreadTokenUsageUpdatedNotification) {
		if subagents.TokenUsageUpdated(params) || !ours(params.ThreadID) {
			return
		}
		session.spend.Report(params.ThreadID, params.TokenUsage)
		session.receiveUsage(params.TokenUsage.Last.TotalTokens, params.TokenUsage.ModelContextWindow)
	})
	onNotification(client, protocol.NotifyError, func(params protocol.ErrorNotification) {
		if subagents.Error(params) || !ours(params.ThreadID) {
			return
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		turn := session.turn()
		if turn == nil {
			return
		}
		if params.WillRetry {
			turn.Input.Emit(agent.EventRetry{RetryNotice: domain.RetryNotice{Message: retryReason(params.Error)}})
		} else {
			failure := params.Error
			turn.failure = &failure
		}
	})
	onNotification(client, protocol.NotifyTurnCompleted, func(params protocol.TurnNotification) {
		if !subagents.TurnCompleted(params) && ours(params.ThreadID) {
			session.receiveTurnCompleted(params.Turn)
		}
	})
	onNotification(client, protocol.NotifyServerRequestResolved, func(params protocol.ServerRequestResolvedNotification) {
		session.questions.Withdraw(params.RequestID)
	})
	client.OnRequest(session.answer)
}

func (session *CodexSession) interrupt(turn *codexTurn) {
	session.mu.Lock()
	if turn.turnID == "" {
		turn.interruptWanted = true
		session.mu.Unlock()
		return
	}
	client, turnID := session.client, turn.turnID
	session.mu.Unlock()
	threadID := session.ThreadID()
	if client == nil || threadID == "" {
		return
	}
	var children []CodexChildTurn
	if subagents := session.subagents.Load(); subagents != nil {
		children = subagents.LiveTurns()
	}
	for _, child := range children {
		go client.Request(context.Background(), "turn/interrupt", protocol.TurnInterruptParams{ThreadID: child.ThreadID, TurnID: child.TurnID})
	}
	go client.Request(context.Background(), "turn/interrupt", protocol.TurnInterruptParams{ThreadID: threadID, TurnID: turnID})
}

type steerTarget struct {
	client   CodexClient
	threadID string
	turnID   string
}

// Feeds one run's steered messages into the turn already going. One the server refuses stays queued
// on the thread, which sends it as the next turn once this run settles.
func (session *CodexSession) drainSteering(turn *codexTurn) {
	if turn.Input.Steering == nil {
		return
	}
	threadID := session.ThreadID()
	target := func() (steerTarget, bool) {
		session.mu.Lock()
		defer session.mu.Unlock()
		if !session.current(turn) || session.client == nil || threadID == "" || turn.turnID == "" {
			return steerTarget{}, false
		}
		return steerTarget{session.client, threadID, turn.turnID}, true
	}
	agent.DrainSteering(turn.done, turn.Input, target, func(into steerTarget, prompt string) error {
		if goal := goalCommand(prompt); goal != nil && goal.kind == goalClear {
			if _, err := into.client.Request(context.Background(), "thread/goal/clear", protocol.ThreadIDParams{ThreadID: into.threadID}); err != nil {
				return err
			}
			session.mu.Lock()
			session.goalActive = false
			session.mu.Unlock()
			turn.Input.ReportGoal(contracts.GoalReport{})
			return nil
		}
		_, err := into.client.Request(context.Background(), "turn/steer", protocol.TurnSteerParams{ThreadID: into.threadID, Input: session.skills.Input(prompt), ExpectedTurnID: into.turnID})
		return err
	}, reasonOf)
}

func (session *CodexSession) exited(exit protocol.ExitStatus) {
	session.mu.Lock()
	if session.ended {
		session.mu.Unlock()
		return
	}
	outcome := agent.ProviderResult{Status: domain.RunStatusFailed, Message: startFailure(exit)}
	if session.ThreadID() != "" {
		outcome.Message = "Codex stopped: " + describeExit(exit)
	}
	session.outcome = &outcome
	session.mu.Unlock()
	session.turns.Settle(outcome)
	session.Close()
}

func (session *CodexSession) receiveStarted(item protocol.ThreadItem) {
	session.mu.Lock()
	defer session.mu.Unlock()
	turn := session.turn()
	if turn == nil {
		return
	}
	if item.Type == protocol.ItemContextCompaction {
		session.setCompactingLocked(turn, true, nil)
		return
	}
	intent := IntentOf(item)
	if intent == nil {
		return
	}
	turn.items.set(item.ID, item)
	turn.Input.Emit(agent.EventTool{Intent: *intent})
}

func (session *CodexSession) receiveDelta(itemID, delta string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	turn := session.turn()
	if turn == nil || turn.reviewOutput != nil {
		return
	}
	agent.StreamMarkdown(turn.streamed, agent.EmitFunc(turn.Input.Emit), itemID, delta)
}

func (session *CodexSession) receiveCompleted(item protocol.ThreadItem) {
	session.mu.Lock()
	defer session.mu.Unlock()
	turn := session.turn()
	if turn == nil {
		return
	}
	switch {
	case item.Type == protocol.ItemAgentMessage:
		if turn.reviewOutput != nil {
			return
		}
		if client, threadID := session.client, session.ThreadID(); deref(item.Delivery) == "async" && len(item.Questions) > 0 && client != nil && threadID != "" {
			go func() {
				err := session.questions.AnswerAsync(item.Questions, turn.Input, client, threadID, func() (string, bool) {
					session.mu.Lock()
					defer session.mu.Unlock()
					if !session.current(turn) || turn.turnID == "" {
						return "", false
					}
					return turn.turnID, true
				})
				if err != nil && session.current(turn) {
					session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: "Codex could not receive your answer: " + reasonOf(err)})
				}
			}()
		}
		agent.EndMarkdown(turn.streamed, agent.EmitFunc(turn.Input.Emit), item.ID, deref(item.Text))
	case item.Type == protocol.ItemExitedReviewMode:
		if _, review := turn.Input.Operation.(contracts.RunOperationReview); !review {
			turn.items.delete(item.ID)
			return
		}
		if output := jsTrim(deref(item.Review)); output != "" && turn.reviewOutput == nil {
			turn.reviewOutput = &output
			turn.Input.Emit(agent.EventAssistant{MessageID: item.ID, Text: output})
		}
	case item.Type == protocol.ItemImageGeneration:
		turn.items.delete(item.ID)
		if turn.imageIDs[item.ID] {
			return
		}
		turn.imageIDs[item.ID] = true
		root := turn.Input.WorkspaceRoot
		turn.images.Add(1)
		go func() {
			defer turn.images.Done()
			text, err := session.imageOutput(context.Background(), item, root)
			if err != nil {
				text = "The generated image could not be saved or displayed: " + reasonOf(err)
			}
			if session.current(turn) && !turn.Input.Aborted() {
				turn.Input.Emit(agent.EventAssistant{MessageID: item.ID, Text: text, Artifact: true})
			}
		}()
	case item.Type == protocol.ItemContextCompaction:
		_, manual := turn.Input.Operation.(contracts.RunOperationCompact)
		trigger := contracts.RunEventContextCompactedTriggerAuto
		if manual {
			trigger = contracts.RunEventContextCompactedTriggerManual
		}
		pre := session.lastTokens
		if turn.CompactionPreTokens != nil {
			pre = *turn.CompactionPreTokens
		}
		turn.Input.Emit(agent.EventCompaction{Trigger: trigger, PreTokens: pre})
		session.setCompactingLocked(turn, false, nil)
		if manual {
			session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusSucceeded})
		}
	default:
		turn.items.delete(item.ID)
	}
}

func (session *CodexSession) setCompacting(turn *codexTurn, compacting bool, err *string) {
	session.mu.Lock()
	defer session.mu.Unlock()
	session.setCompactingLocked(turn, compacting, err)
}

func (session *CodexSession) setCompactingLocked(turn *codexTurn, compacting bool, err *string) {
	if session.current(turn) {
		agent.ReportCompacting(turn.Input, &turn.Compacting, compacting, session.lastTokens, err)
	}
}

// The window the server reports is the model's own; the catalogue's stands in while it reports none.
func (session *CodexSession) receiveUsage(tokens float64, contextWindow *float64) {
	session.mu.Lock()
	defer session.mu.Unlock()
	turn := session.turn()
	if turn == nil {
		return
	}
	session.lastTokens = tokens
	limit := 0.0
	if contextWindow != nil {
		limit = *contextWindow
	} else {
		limit = domain.ContextWindowLimit(domain.BuiltInModels, domain.AgentEngineCodex, turn.Input.Model)
	}
	turn.Input.Emit(agent.EventUsage{Tokens: tokens, Limit: limit, Model: turn.Input.Model})
}

func (session *CodexSession) receiveTurnCompleted(completed protocol.Turn) {
	session.mu.Lock()
	turn := session.turn()
	mine := turn != nil && (turn.turnID == "" || turn.turnID == completed.ID)
	session.mu.Unlock()
	if mine {
		go session.finishTurn(turn, completed)
	}
}

// Reads terminals before settling, so the session cannot be reclaimed while new work is running.
func (session *CodexSession) finishTurn(turn *codexTurn, completed protocol.Turn) {
	turn.images.Wait()
	session.refreshBackgroundProcesses()
	if !session.current(turn) {
		return
	}
	session.mu.Lock()
	goalActive, failure, client := session.goalActive, turn.failure, session.client
	session.mu.Unlock()
	_, review := turn.Input.Operation.(contracts.RunOperationReview)
	switch completed.Status {
	case protocol.TurnCompleted:
		if goalActive && !review {
			return
		}
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusSucceeded})
	case protocol.TurnInterrupted:
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusCancelled})
	case protocol.TurnFailed:
		limited := (completed.Error != nil && errorInfoIs(completed.Error.CodexErrorInfo, "usageLimitExceeded")) ||
			(failure != nil && errorInfoIs(failure.CodexErrorInfo, "usageLimitExceeded"))
		var limit *domain.UsageLimit
		if limited && client != nil {
			limit = ReadCodexUsageLimit(context.Background(), client)
		}
		if !session.current(turn) {
			return
		}
		message := "Codex could not finish the turn."
		if completed.Error != nil {
			message = completed.Error.Message
		} else if failure != nil {
			message = failure.Message
		}
		session.turns.Settle(agent.ProviderResult{Status: domain.RunStatusFailed, Message: message, Limit: limit})
	}
}

// Every page of a thread's background terminals; false when one could not be read.
func backgroundTerminals(client CodexClient, threadID string, still func() bool) ([]protocol.BackgroundTerminal, bool) {
	var terminals []protocol.BackgroundTerminal
	cursors := map[string]bool{}
	var cursor *string
	for {
		page, err := call[protocol.BackgroundTerminalsListResponse](context.Background(), client, "thread/backgroundTerminals/list", protocol.BackgroundTerminalsListParams{ThreadID: threadID, Cursor: cursor, Limit: ptr(100.0)})
		if err != nil || !still() {
			return nil, false
		}
		terminals = append(terminals, page.Data...)
		cursor = page.NextCursor
		if cursor == nil || *cursor == "" {
			return terminals, true
		}
		if cursors[*cursor] {
			return terminals, false
		}
		cursors[*cursor] = true
	}
}

// Interrupting a child's turn leaves the commands it started running, so its stop ends them too.
func (session *CodexSession) terminateTerminals(client CodexClient, threadID string) {
	terminals, ok := backgroundTerminals(client, threadID, func() bool { return true })
	if !ok && terminals == nil {
		return
	}
	var waiting sync.WaitGroup
	for _, terminal := range terminals {
		waiting.Go(func() {
			_, _ = client.Request(context.Background(), "thread/backgroundTerminals/terminate", protocol.BackgroundTerminalsTerminateParams{ThreadID: threadID, ProcessID: terminal.ProcessID})
		})
	}
	waiting.Wait()
}

// Reads every page because the Session Panel treats each provider report as the complete set.
func (session *CodexSession) refreshBackgroundProcesses() {
	session.mu.Lock()
	client, ended := session.client, session.ended
	threadID := session.ThreadID()
	if client == nil || threadID == "" || ended {
		session.mu.Unlock()
		return
	}
	session.backgroundRead++
	read := session.backgroundRead
	session.mu.Unlock()
	still := func() bool {
		session.mu.Lock()
		defer session.mu.Unlock()
		return read == session.backgroundRead && session.client == client && session.ThreadID() == threadID && !session.ended
	}
	terminals, ok := backgroundTerminals(client, threadID, still)
	if !ok {
		return
	}
	found := make([]*domain.BackgroundProcess, 0, len(terminals))
	for _, terminal := range terminals {
		description := terminal.Command
		if description == "" {
			description = "Background shell"
		}
		found = append(found, &domain.BackgroundProcess{ID: terminal.ProcessID, Kind: domain.BackgroundProcessKindShell, Description: description})
	}
	session.background.Replace(found, nil)
}

// Whether the run allows what the server is asking. Nothing is allowed on a session with no run to ask.
func (session *CodexSession) allowed(intent domain.ToolIntent, workspace bool) bool {
	turn := session.turn()
	if turn == nil {
		return false
	}
	if workspace && turn.Input.CoordinationRole != nil && *turn.Input.CoordinationRole == domain.CoordinationRoleCoordinator {
		return false
	}
	if agent.GrantsTool(agent.ReachWorkspace, &agent.ApprovalScope{Policy: turn.Input.Policy, Channel: turn.Input.Channel}) {
		return true
	}
	decision := turn.Input.Authorize(context.Background(), intent)
	if !decision.Allowed && decision.Reason != "" {
		session.explainDenial(turn, decision.Reason)
	}
	return decision.Allowed
}

// A decline carries no wording on the wire, so a denial that says why is steered into the turn it stopped.
func (session *CodexSession) explainDenial(turn *codexTurn, reason string) {
	session.mu.Lock()
	client, turnID := session.client, turn.turnID
	session.mu.Unlock()
	threadID := session.ThreadID()
	if !session.current(turn) || client == nil || threadID == "" || turnID == "" {
		return
	}
	go client.Request(context.Background(), "turn/steer", protocol.TurnSteerParams{ThreadID: threadID, Input: []protocol.UserInput{protocol.Text(reason)}, ExpectedTurnID: turnID})
}

// The pending item addressed by a server request, kept separate across concurrent threads.
func (session *CodexSession) pendingItem(threadID, itemID string) (protocol.ThreadItem, bool) {
	if threadID == session.ThreadID() {
		session.mu.Lock()
		defer session.mu.Unlock()
		if turn := session.turn(); turn != nil {
			return turn.items.get(itemID)
		}
		return protocol.ThreadItem{}, false
	}
	if subagents := session.subagents.Load(); subagents != nil {
		return subagents.PendingItem(threadID, itemID)
	}
	return protocol.ThreadItem{}, false
}

// The newest call one thread has going against one MCP server, which is what its prompt is about.
func (session *CodexSession) pendingMcpCall(threadID, server string) (protocol.ThreadItem, bool) {
	if threadID == session.ThreadID() {
		session.mu.Lock()
		defer session.mu.Unlock()
		var found protocol.ThreadItem
		ok := false
		if turn := session.turn(); turn != nil {
			for _, id := range turn.items.keys {
				item, _ := turn.items.get(id)
				if item.Type == protocol.ItemMcpToolCall && deref(item.Server) == server {
					found, ok = item, true
				}
			}
		}
		return found, ok
	}
	if subagents := session.subagents.Load(); subagents != nil {
		return subagents.PendingMcpCall(threadID, server)
	}
	return protocol.ThreadItem{}, false
}

type approvalInput struct {
	Command string  `json:"command"`
	Cwd     *string `json:"cwd,omitzero"`
	Reason  *string `json:"reason,omitzero"`
}

type permissionsInput struct {
	Cwd         string                            `json:"cwd"`
	Reason      *string                           `json:"reason,omitzero"`
	Permissions protocol.RequestPermissionProfile `json:"permissions"`
}

func decision(allow bool) protocol.ApprovalResponse {
	if allow {
		return protocol.ApprovalResponse{Decision: protocol.DecisionAccept}
	}
	return protocol.ApprovalResponse{Decision: protocol.DecisionDecline}
}

// Every question the server asks goes to the run's user; a denial declines rather than cancels, so the turn goes on.
func (session *CodexSession) answer(request *ServerRequest) {
	switch request.Method {
	case protocol.RequestCommandApproval:
		var params protocol.CommandExecutionRequestApprovalParams
		_ = request.decode(&params)
		input := approvalInput{Command: deref(params.Command), Cwd: nonEmptyPointer(params.Cwd), Reason: nonEmptyPointer(params.Reason)}
		if params.Command == nil {
			if started, ok := session.pendingItem(params.ThreadID, params.ItemID); ok && started.Type == protocol.ItemCommandExecution {
				input.Command = deref(started.Command)
			}
		}
		go func() {
			request.Respond(decision(session.allowed(domain.ToolIntent{ToolID: params.ItemID, Name: "command_execution", Input: encoded(input)}, true)))
		}()
	case protocol.RequestFileChangeApproval:
		var params protocol.FileChangeRequestApprovalParams
		_ = request.decode(&params)
		var intent *domain.ToolIntent
		if started, ok := session.pendingItem(params.ThreadID, params.ItemID); ok {
			intent = IntentOf(started)
		}
		if intent == nil {
			intent = &domain.ToolIntent{ToolID: params.ItemID, Name: "file_change", Input: jsontext.Value("{}")}
		}
		object, _ := readObject(intent.Input)
		if reason := deref(params.Reason); reason != "" {
			object = object.withValue("reason", reason)
		}
		ask := *intent
		ask.Input = object.value()
		go func() { request.Respond(decision(session.allowed(ask, true))) }()
	case protocol.RequestPermissionsApproval:
		var params protocol.PermissionsRequestApprovalParams
		_ = request.decode(&params)
		input := permissionsInput{Cwd: params.Cwd, Reason: nonEmptyPointer(params.Reason), Permissions: params.Permissions}
		go func() {
			allow := session.allowed(domain.ToolIntent{ToolID: params.ItemID, Name: "permissions", Input: encoded(input)}, true)
			response := protocol.PermissionsRequestApprovalResponse{Scope: "turn"}
			if allow {
				response.Permissions = granted(params.Permissions)
			}
			request.Respond(response)
		}()
	case protocol.RequestToolUserInput:
		var params protocol.ToolRequestUserInputParams
		_ = request.decode(&params)
		session.mu.Lock()
		turn := session.turn()
		stale := turn != nil && params.ThreadID == session.ThreadID() && turn.turnID != "" && params.TurnID != turn.turnID
		session.mu.Unlock()
		secret := slices.ContainsFunc(params.Questions, func(question protocol.ToolRequestUserInputQuestion) bool { return question.IsSecret })
		if turn == nil || stale || secret {
			request.Fail(protocol.JSONRPCError{Code: -32000, Message: "This question cannot be answered in chat."})
			return
		}
		session.questions.Answer(request, params, turn.Input)
	case protocol.RequestMcpElicitation:
		var params protocol.McpServerElicitationRequestParams
		_ = request.decode(&params)
		meta, _ := readObject(params.Meta)
		member := func(key string) jsontext.Value {
			for _, entry := range meta {
				if entry.key == key {
					return entry.value
				}
			}
			return nil
		}
		// Only a tool-call approval is a yes-or-no question; a form or a link needs an answer nobody here can give.
		if kind, _ := rawString(member("codex_approval_kind")); params.Meta.Kind() != '{' || kind != "mcp_tool_call" {
			request.Respond(protocol.DeclineElicitation())
			return
		}
		var intent *domain.ToolIntent
		if started, ok := session.pendingMcpCall(params.ThreadID, params.ServerName); ok {
			intent = IntentOf(started)
		}
		if intent == nil {
			name, ok := toolNamed(params.Message)
			if !ok {
				name = "mcp_tool_call"
			}
			input := member("tool_params")
			if input.Kind() != '{' {
				input = jsontext.Value("{}")
			}
			intent = &domain.ToolIntent{ToolID: params.ServerName + ":" + request.ID.String(), Name: name, Input: input}
		}
		ask := *intent
		go func() {
			if session.allowed(ask, false) {
				request.Respond(protocol.McpServerElicitationRequestResponse{Action: "accept", Content: jsontext.Value("{}"), Meta: jsontext.Value("null")})
			} else {
				request.Respond(protocol.DeclineElicitation())
			}
		}()
	default:
		request.Fail(protocol.JSONRPCError{Code: -32601, Message: request.Method + " is not supported."})
	}
}
