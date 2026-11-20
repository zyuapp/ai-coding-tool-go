package codex

import (
	"context"
	"encoding/json/jsontext"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/engines/codex/protocol"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type childLifecycle string

const (
	lifecycleUnknown childLifecycle = "unknown"
	lifecycleWorking childLifecycle = "working"
	lifecycleIdle    childLifecycle = "idle"
	lifecycleFailed  childLifecycle = "failed"
	lifecycleStopped childLifecycle = "stopped"
)

type codexChild struct {
	id              string
	discovered      bool
	lifecycle       childLifecycle
	active          bool
	prompt          *string
	model           *string
	effort          *string
	metadataReads   int
	metadataPending bool
	preview         *string
	nickname        *string
	role            *string
	path            *string
	totalTokens     float64
	lastToolName    *string
	summary         *string
	statusSummary   *string
	// Never lifecycleUnknown once set.
	reportedLifecycle     childLifecycle
	reportedStatusSummary *string
	progressKey           *string
	bufferedActivity      []domain.SubagentReportSubagentActivity
	seenActivity          map[string]bool
	seenActivityOrder     []string
}

type pendingItem struct {
	threadID string
	item     protocol.ThreadItem
}

type CodexChildTurn struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

const (
	maxBufferedActivity = 100
	maxSeenActivity     = 1_000
)

// A string with something in it besides whitespace, trimmed.
func nonempty(value *string) *string {
	if value == nil {
		return nil
	}
	if trimmed := jsTrim(*value); trimmed != "" {
		return &trimmed
	}
	return nil
}

func nonemptyRaw(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return nonempty(&text)
}

func pathLeaf(value *string) *string {
	if value == nil {
		return nil
	}
	var leaf *string
	for _, part := range strings.Split(*value, "/") {
		if part != "" {
			leaf = &part
		}
	}
	return leaf
}

func subagentFirstLine(value string) string {
	for _, line := range strings.Split(value, "\n") {
		if trimmed := jsTrim(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// value as JSON.stringify(value, null, 2) writes it; "" for nothing at all.
func detail(value any) string {
	var data []byte
	switch value := value.(type) {
	case jsontext.Value:
		if len(value) == 0 {
			return ""
		}
		data = append([]byte(nil), value...)
	default:
		encoded, err := jsonx.Marshal(value)
		if err != nil {
			return ""
		}
		data = encoded
	}
	indented := jsontext.Value(data)
	if indented.Indent(jsontext.WithIndent("  ")) != nil {
		return string(data)
	}
	return string(indented)
}

func rawObject(value jsontext.Value) map[string]any {
	var object map[string]any
	if len(value) == 0 || jsonx.Unmarshal(value, &object) != nil {
		return nil
	}
	return object
}

type spawn struct{ nickname, role, path *string }

// Metadata present only on a child thread's thread/started notification.
func spawnMetadata(thread protocol.Thread) *spawn {
	source := rawObject(thread.Source)
	if source == nil {
		return nil
	}
	subAgent, ok := source["subAgent"].(map[string]any)
	if !ok {
		return nil
	}
	threadSpawn, ok := subAgent["thread_spawn"].(map[string]any)
	if !ok {
		return nil
	}
	return &spawn{nickname: nonemptyRaw(threadSpawn["agent_nickname"]), role: nonemptyRaw(threadSpawn["agent_role"]), path: nonemptyRaw(threadSpawn["agent_path"])}
}

func rawString(value jsontext.Value) (string, bool) {
	var text string
	if len(value) == 0 || jsonx.Unmarshal(value, &text) != nil {
		return "", false
	}
	return text, true
}

// Threads Codex owns for housekeeping must stay out of both the parent conversation and the roster.
func isInternalThread(thread protocol.Thread) bool {
	if source, ok := rawString(thread.ThreadSource); ok && source == "memory_consolidation" {
		return true
	}
	source := rawObject(thread.Source)
	if source == nil {
		return false
	}
	if source["subAgent"] == "memory_consolidation" {
		return true
	}
	return source["internal"] == "memory_consolidation" || source["internal"] == "guardian"
}

type toolActivityOf struct {
	title   string
	text    string
	summary *string
}

type commandDetail struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}

type changesDetail struct {
	Changes []protocol.FileUpdateChange `json:"changes"`
}

type queryDetail struct {
	Query string `json:"query"`
}

type pathDetail struct {
	Path string `json:"path"`
}

type imageDetail struct {
	RevisedPrompt *string `json:"revisedPrompt"`
	SavedPath     *string `json:"savedPath,omitzero"`
}

type sleepDetail struct {
	DurationMs float64 `json:"durationMs"`
}

type collabDetail struct {
	Prompt            *string  `json:"prompt"`
	ReceiverThreadIDs []string `json:"receiverThreadIds"`
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}

func toolActivity(item protocol.ThreadItem) *toolActivityOf {
	switch item.Type {
	case protocol.ItemCommandExecution:
		return &toolActivityOf{"command_execution", detail(commandDetail{deref(item.Command), deref(item.Cwd)}), ptr(deref(item.Command))}
	case protocol.ItemFileChange:
		activity := &toolActivityOf{title: "file_change", text: detail(changesDetail{nonNil(item.Changes)})}
		if len(item.Changes) > 0 && item.Changes[0].Path != "" {
			activity.summary = ptr(item.Changes[0].Path)
		}
		return activity
	case protocol.ItemMcpToolCall, protocol.ItemDynamicToolCall:
		return &toolActivityOf{deref(item.Tool), detail(item.Arguments), ptr(deref(item.Tool))}
	case protocol.ItemWebSearch:
		return &toolActivityOf{"web_search", detail(queryDetail{deref(item.Query)}), ptr(deref(item.Query))}
	case protocol.ItemImageView:
		return &toolActivityOf{"image_view", detail(pathDetail{deref(item.Path)}), ptr(deref(item.Path))}
	case protocol.ItemImageGeneration:
		activity := &toolActivityOf{title: "image_generation", text: detail(imageDetail{item.RevisedPrompt, item.SavedPath})}
		if deref(item.RevisedPrompt) != "" {
			activity.summary = item.RevisedPrompt
		}
		return activity
	case protocol.ItemSleep:
		duration := deref(item.DurationMs)
		return &toolActivityOf{"sleep", detail(sleepDetail{duration}), ptr(jsNumber(duration) + " ms")}
	case protocol.ItemCollabAgentToolCall:
		activity := &toolActivityOf{title: deref(item.Tool), text: detail(collabDetail{item.Prompt, nonNil(item.ReceiverThreadIDs)})}
		if deref(item.Prompt) != "" {
			activity.summary = item.Prompt
		}
		return activity
	}
	return nil
}

// The thread a child's settings are read from: only its id, model and effort count.
type ReadThreadMetadata func(ctx context.Context, threadID string) (protocol.Thread, error)

// Routes Codex multi-agent notifications and folds each child thread into the provider-neutral
// subagent reports. A foreign thread is suppressed immediately, but nothing appears in the roster
// until thread/started names a thread_spawn source or subAgentActivity names the child. Safe for
// concurrent use; reports and busy changes are delivered while it is locked, in order.
type CodexSubagents struct {
	report        func(event domain.SubagentReport)
	onBusyChanged func(busy bool)
	readMetadata  ReadThreadMetadata
	// Where what each child spends is counted. Codex's own housekeeping threads are not the run's work.
	spend *CodexSpend

	mu                sync.Mutex
	rootThreadID      *string
	children          *ordered[string, *codexChild]
	ignoredThreads    map[string]bool
	liveTurnsByThread *ordered[string, string]
	// Children asked to stop before they had a turn to interrupt.
	heldStops       map[string]bool
	pendingItems    *ordered[string, pendingItem]
	metadataQueue   *ordered[*codexChild, struct{}]
	readingMetadata int
	activeChildren  int
	closed          bool
}

// onBusyChanged, readMetadata and spend may be nil.
func NewCodexSubagents(report func(event domain.SubagentReport), onBusyChanged func(busy bool), readMetadata ReadThreadMetadata, spend *CodexSpend) *CodexSubagents {
	if onBusyChanged == nil {
		onBusyChanged = func(bool) {}
	}
	return &CodexSubagents{
		report:            report,
		onBusyChanged:     onBusyChanged,
		readMetadata:      readMetadata,
		spend:             spend,
		children:          newOrdered[string, *codexChild](),
		ignoredThreads:    map[string]bool{},
		liveTurnsByThread: newOrdered[string, string](),
		heldStops:         map[string]bool{},
		pendingItems:      newOrdered[string, pendingItem](),
		metadataQueue:     newOrdered[*codexChild, struct{}](),
	}
}

// Anything closing the app-server process would cut short.
func (subagents *CodexSubagents) Busy() bool {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	return subagents.busy()
}

func (subagents *CodexSubagents) busy() bool {
	return subagents.liveTurnsByThread.len() > 0 || subagents.activeChildren > 0
}

// Child turns currently addressable by turn/interrupt, including child-first provisional ones.
func (subagents *CodexSubagents) LiveTurns() []CodexChildTurn {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	turns := []CodexChildTurn{}
	for _, threadID := range subagents.liveTurnsByThread.keys {
		turnID, _ := subagents.liveTurnsByThread.get(threadID)
		turns = append(turns, CodexChildTurn{threadID, turnID})
	}
	return turns
}

// The turn a stop of this child interrupts now, or held when it has none: a working child's stop
// then waits for the turn it starts next. Neither when no reported child has this id.
func (subagents *CodexSubagents) Stop(threadID string) (turn *CodexChildTurn, held bool) {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	child, ok := subagents.children.get(threadID)
	if !ok || !child.discovered {
		return nil, false
	}
	if turnID, ok := subagents.liveTurnsByThread.get(threadID); ok && turnID != "" {
		return &CodexChildTurn{threadID, turnID}, false
	}
	if child.active {
		subagents.heldStops[threadID] = true
	}
	return nil, true
}

// The turn just started by a child whose stop was held for it.
func (subagents *CodexSubagents) TakeHeldStop(threadID string) *CodexChildTurn {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	turnID, ok := subagents.liveTurnsByThread.get(threadID)
	if !ok || turnID == "" || !subagents.heldStops[threadID] {
		return nil
	}
	delete(subagents.heldStops, threadID)
	return &CodexChildTurn{threadID, turnID}
}

func (subagents *CodexSubagents) SetRootThreadID(threadID string) {
	subagents.withBusy(func() bool {
		subagents.rootThreadID = &threadID
		delete(subagents.ignoredThreads, threadID)
		if child, ok := subagents.children.get(threadID); ok && child.active {
			subagents.activeChildren--
		}
		subagents.children.delete(threadID)
		subagents.liveTurnsByThread.delete(threadID)
		delete(subagents.heldStops, threadID)
		subagents.dropPendingItems(threadID)
		return false
	})
}

// Whether a notification belongs somewhere other than the root conversation. Once the root is
// known, unknown foreign ids are suppressed too: Codex can send child status before discovery.
func (subagents *CodexSubagents) ShouldSuppress(threadID string) bool {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	return subagents.shouldSuppress(threadID)
}

func (subagents *CodexSubagents) shouldSuppress(threadID string) bool {
	if subagents.rootThreadID != nil && threadID == *subagents.rootThreadID {
		return false
	}
	return subagents.children.has(threadID) || subagents.ignoredThreads[threadID] || subagents.rootThreadID != nil
}

// A child item kept for a later approval request, which may carry only the item id.
func (subagents *CodexSubagents) PendingItem(threadID, itemID string) (protocol.ThreadItem, bool) {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	pending, ok := subagents.pendingItems.get(itemID)
	if !ok || pending.threadID != threadID {
		return protocol.ThreadItem{}, false
	}
	return pending.item, true
}

// The newest child call against one MCP server, for an elicitation approval with no item id.
func (subagents *CodexSubagents) PendingMcpCall(threadID, server string) (protocol.ThreadItem, bool) {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	var found protocol.ThreadItem
	ok := false
	for _, itemID := range subagents.pendingItems.keys {
		pending, _ := subagents.pendingItems.get(itemID)
		if pending.threadID == threadID && pending.item.Type == protocol.ItemMcpToolCall && deref(pending.item.Server) == server {
			found, ok = pending.item, true
		}
	}
	return found, ok
}

func (subagents *CodexSubagents) ThreadStarted(params protocol.ThreadStartedNotification) bool {
	return subagents.withBusy(func() bool {
		thread := params.Thread
		if subagents.rootThreadID != nil && thread.ID == *subagents.rootThreadID {
			return false
		}
		if isInternalThread(thread) {
			subagents.ignore(thread.ID)
			return true
		}
		spawned := spawnMetadata(thread)
		if spawned == nil && !subagents.shouldSuppress(thread.ID) {
			return false
		}
		child := subagents.child(thread.ID)
		metadata := childMetadata{
			model:    nonempty(thread.Model),
			effort:   nonempty(thread.ReasoningEffort),
			preview:  nonempty(&thread.Preview),
			nickname: nonempty(thread.AgentNickname),
			role:     nonempty(thread.AgentRole),
		}
		if spawned != nil {
			if spawned.nickname != nil {
				metadata.nickname = spawned.nickname
			}
			if spawned.role != nil {
				metadata.role = spawned.role
			}
			metadata.path = spawned.path
		}
		subagents.mergeMetadata(child, metadata)
		if spawned != nil {
			// A spawn is work beginning. The thread snapshot commonly still says idle before its first turn.
			if !child.discovered && (child.lifecycle == lifecycleUnknown || child.lifecycle == lifecycleIdle) {
				subagents.setLifecycle(child, lifecycleWorking, nil)
			}
			subagents.discover(child)
		}
		return true
	})
}

func (subagents *CodexSubagents) ThreadStatusChanged(params protocol.ThreadStatusChangedNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		if subagents.ignoredThreads[params.ThreadID] {
			if params.Status.Type == "idle" || params.Status.Type == "systemError" {
				subagents.liveTurnsByThread.delete(params.ThreadID)
			}
			return true
		}
		subagents.applyThreadStatus(subagents.child(params.ThreadID), params.Status)
		return true
	})
}

func (subagents *CodexSubagents) TurnStarted(params protocol.TurnNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		if subagents.ignoredThreads[params.ThreadID] {
			return true
		}
		subagents.liveTurnsByThread.set(params.ThreadID, params.Turn.ID)
		child := subagents.child(params.ThreadID)
		subagents.setLifecycle(child, lifecycleWorking, nil)
		subagents.requestMetadata(child)
		return true
	})
}

func (subagents *CodexSubagents) TurnCompleted(params protocol.TurnNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		liveTurn, live := subagents.liveTurnsByThread.get(params.ThreadID)
		if !live || liveTurn == "" || liveTurn == params.Turn.ID {
			subagents.liveTurnsByThread.delete(params.ThreadID)
		}
		if subagents.ignoredThreads[params.ThreadID] {
			return true
		}
		// An older completion arriving after a resumed turn says nothing about the newer turn.
		if live && liveTurn != "" && liveTurn != params.Turn.ID {
			return true
		}
		child := subagents.child(params.ThreadID)
		switch params.Turn.Status {
		case protocol.TurnCompleted:
			subagents.setLifecycle(child, lifecycleIdle, nil)
		case protocol.TurnInterrupted:
			subagents.setLifecycle(child, lifecycleStopped, nil)
		case protocol.TurnFailed:
			var message *string
			if params.Turn.Error != nil {
				message = &params.Turn.Error.Message
			}
			subagents.setLifecycle(child, lifecycleFailed, message)
		}
		return true
	})
}

func (subagents *CodexSubagents) TokenUsageUpdated(params protocol.ThreadTokenUsageUpdatedNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		if subagents.ignoredThreads[params.ThreadID] {
			return true
		}
		if subagents.spend != nil {
			subagents.spend.Report(params.ThreadID, params.TokenUsage)
		}
		child := subagents.child(params.ThreadID)
		if total := params.TokenUsage.Total.TotalTokens; finite(total) && total >= 0 {
			child.totalTokens = math.Max(child.totalTokens, total)
		}
		subagents.emitProgress(child)
		return true
	})
}

func (subagents *CodexSubagents) Error(params protocol.ErrorNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		if params.WillRetry {
			return true
		}
		// An error from an older turn cannot fail a child that has already resumed.
		if liveTurn, live := subagents.liveTurnsByThread.get(params.ThreadID); live && liveTurn != "" && liveTurn != params.TurnID {
			return true
		}
		subagents.liveTurnsByThread.delete(params.ThreadID)
		if !subagents.ignoredThreads[params.ThreadID] {
			subagents.setLifecycle(subagents.child(params.ThreadID), lifecycleFailed, &params.Error.Message)
		}
		return true
	})
}

func (subagents *CodexSubagents) ThreadClosed(params protocol.ThreadNotification) bool {
	return subagents.withBusy(func() bool {
		if !subagents.shouldSuppress(params.ThreadID) {
			return false
		}
		subagents.liveTurnsByThread.delete(params.ThreadID)
		if subagents.ignoredThreads[params.ThreadID] {
			delete(subagents.ignoredThreads, params.ThreadID)
			return true
		}
		if child, ok := subagents.children.get(params.ThreadID); ok {
			subagents.dropPendingItems(params.ThreadID)
			if child.lifecycle != lifecycleFailed {
				subagents.setLifecycle(child, lifecycleStopped, nil)
			}
		}
		return true
	})
}

func (subagents *CodexSubagents) ItemStarted(params protocol.ItemStartedNotification) bool {
	return subagents.item(params.ThreadID, params.Item, true)
}

func (subagents *CodexSubagents) ItemCompleted(params protocol.ItemCompletedNotification) bool {
	return subagents.item(params.ThreadID, params.Item, false)
}

// Marks unfinished visible children stopped, then releases every session-only reference.
func (subagents *CodexSubagents) Close() {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	if subagents.closed {
		return
	}
	wasBusy := subagents.busy()
	for _, id := range subagents.children.orderedKeys() {
		child, _ := subagents.children.get(id)
		if child.discovered && child.lifecycle != lifecycleIdle && child.lifecycle != lifecycleFailed && child.lifecycle != lifecycleStopped {
			subagents.setLifecycle(child, lifecycleStopped, nil)
		}
	}
	subagents.closed = true
	subagents.liveTurnsByThread.clear()
	subagents.pendingItems.clear()
	subagents.ignoredThreads = map[string]bool{}
	subagents.children.clear()
	subagents.metadataQueue.clear()
	subagents.activeChildren = 0
	subagents.changedBusy(wasBusy)
}

func (subagents *CodexSubagents) item(threadID string, item protocol.ThreadItem, started bool) bool {
	return subagents.withBusy(func() bool {
		if item.Type == protocol.ItemSubAgentActivity {
			subagents.receiveSubagentActivity(item)
			return true
		}
		if item.Type == protocol.ItemCollabAgentToolCall {
			subagents.rememberCollaboration(item)
		}
		if !subagents.shouldSuppress(threadID) {
			return false
		}
		if subagents.ignoredThreads[threadID] {
			return true
		}
		child := subagents.child(threadID)
		if started {
			subagents.pendingItems.set(item.ID, pendingItem{threadID, item})
		}
		subagents.receiveChildItem(child, item, started)
		subagents.requestMetadata(child)
		if !started {
			subagents.pendingItems.delete(item.ID)
		}
		return true
	})
}

var trailingSlashes = regexp.MustCompile(`/+$`)

func (subagents *CodexSubagents) receiveSubagentActivity(item protocol.ThreadItem) {
	path := trailingSlashes.ReplaceAllString(deref(item.AgentPath), "")
	if path == "" {
		path = "/"
	}
	agentThreadID := deref(item.AgentThreadID)
	if (subagents.rootThreadID != nil && agentThreadID == *subagents.rootThreadID) || path == "/root" || path == "/" {
		return
	}
	if subagents.ignoredThreads[agentThreadID] {
		return
	}
	child := subagents.child(agentThreadID)
	subagents.mergeMetadata(child, childMetadata{path: nonempty(&path)})
	switch deref(item.Kind) {
	case "started":
		subagents.setLifecycle(child, lifecycleWorking, nil)
	case "completed":
		subagents.setLifecycle(child, lifecycleIdle, nil)
	case "interrupted":
		subagents.setLifecycle(child, lifecycleStopped, nil)
	default:
		if child.lifecycle == lifecycleUnknown {
			subagents.setLifecycle(child, lifecycleIdle, nil)
		}
	}
	// `interacted` is deliberately not working: Codex also emits it after a child becomes idle.
	subagents.discover(child)
}

func (subagents *CodexSubagents) rememberCollaboration(item protocol.ThreadItem) {
	for _, receiver := range item.ReceiverThreadIDs {
		if (subagents.rootThreadID != nil && receiver == *subagents.rootThreadID) || subagents.ignoredThreads[receiver] {
			continue
		}
		child := subagents.child(receiver)
		if deref(item.Tool) == "spawnAgent" {
			metadata := childMetadata{prompt: item.Prompt, model: child.model, effort: child.effort}
			if metadata.model == nil {
				metadata.model = nonempty(item.Model)
			}
			if metadata.effort == nil {
				metadata.effort = nonempty(item.ReasoningEffort)
			}
			subagents.mergeMetadata(child, metadata)
		}
		state, ok := item.AgentsStates[receiver]
		if !ok {
			continue
		}
		switch state.Status {
		case "pendingInit", "running":
			subagents.setLifecycle(child, lifecycleWorking, state.Message)
		case "completed":
			subagents.setLifecycle(child, lifecycleIdle, state.Message)
		case "errored":
			subagents.setLifecycle(child, lifecycleFailed, state.Message)
		case "interrupted", "shutdown", "notFound":
			subagents.setLifecycle(child, lifecycleStopped, state.Message)
		}
	}
}

func textActivity(child *codexChild, activityID, text string) domain.SubagentReportSubagentActivity {
	return domain.SubagentReportSubagentActivity{ID: child.id, ActivityID: activityID, Kind: domain.SubagentReportSubagentActivityKindText, Text: text}
}

func (subagents *CodexSubagents) receiveChildItem(child *codexChild, item protocol.ThreadItem, started bool) {
	switch item.Type {
	// Older protocols expose plain input. MultiAgentV2's encrypted NEW_TASK messages do not expose a prompt.
	case protocol.ItemUserMessage:
		var texts []string
		for _, part := range item.UserInputs() {
			if part.Type == "text" {
				texts = append(texts, deref(part.Text))
			}
		}
		if text := strings.Join(texts, "\n"); text != "" && child.prompt == nil {
			subagents.mergeMetadata(child, childMetadata{prompt: &text})
		}
		return
	case protocol.ItemExitedReviewMode:
		if started {
			return
		}
		text := jsTrim(deref(item.Review))
		if text == "" {
			return
		}
		child.summary = ptr(subagentFirstLine(text))
		subagents.queueActivity(child, textActivity(child, item.ID+":review", text))
		subagents.emitProgress(child)
		return
	case protocol.ItemAgentMessage:
		if started {
			return
		}
		text := jsTrim(deref(item.Text))
		if text == "" {
			return
		}
		child.summary = ptr(subagentFirstLine(text))
		subagents.queueActivity(child, textActivity(child, item.ID+":text", text))
		subagents.emitProgress(child)
		return
	}
	activity := toolActivity(item)
	if activity == nil {
		return
	}
	if started {
		subagents.setLifecycle(child, lifecycleWorking, nil)
	}
	child.lastToolName = &activity.title
	if activity.summary != nil {
		child.summary = ptr(subagentFirstLine(*activity.summary))
	}
	subagents.queueActivity(child, domain.SubagentReportSubagentActivity{ID: child.id, ActivityID: item.ID, Kind: domain.SubagentReportSubagentActivityKindTool, Title: &activity.title, Text: activity.text})
	subagents.emitProgress(child)
}

func (subagents *CodexSubagents) applyThreadStatus(child *codexChild, status protocol.ThreadStatus) {
	switch status.Type {
	case "active":
		var waiting *string
		if slices.Contains(status.ActiveFlags, "waitingOnApproval") {
			waiting = ptr("Waiting for approval")
		} else if slices.Contains(status.ActiveFlags, "waitingOnUserInput") {
			waiting = ptr("Waiting for input")
		}
		subagents.setLifecycle(child, lifecycleWorking, waiting)
	case "idle", "notLoaded":
		subagents.setLifecycle(child, lifecycleIdle, nil)
	case "systemError":
		subagents.setLifecycle(child, lifecycleFailed, nil)
	}
}

func (subagents *CodexSubagents) child(id string) *codexChild {
	child, ok := subagents.children.get(id)
	if !ok {
		child = &codexChild{id: id, lifecycle: lifecycleUnknown, seenActivity: map[string]bool{}}
		subagents.children.set(id, child)
	}
	return child
}

// At most limit UTF-16 code units of text, as JavaScript's slice(0, limit) cuts it.
func utf16Prefix(text string, limit int) string {
	units := 0
	for index, r := range text {
		width := len(utf16.Encode([]rune{r}))
		if units+width > limit {
			return text[:index]
		}
		units += width
	}
	return text
}

func (subagents *CodexSubagents) description(child *codexChild) string {
	// The roster's existing title stays searchable; its transport limit must not truncate the separate prompt.
	for _, candidate := range []*string{nonempty(child.prompt), child.preview, child.nickname, pathLeaf(child.path), child.role} {
		if candidate != nil {
			return utf16Prefix(*candidate, 100_000)
		}
	}
	return "Subagent"
}

type childMetadata struct {
	prompt, model, effort, preview, nickname, role, path *string
}

func sameString(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func (subagents *CodexSubagents) mergeMetadata(child *codexChild, metadata childMetadata) {
	before := subagents.description(child)
	role := child.role
	changed := (child.prompt == nil && metadata.prompt != nil) ||
		(metadata.model != nil && !sameString(child.model, metadata.model)) ||
		(metadata.effort != nil && !sameString(child.effort, metadata.effort))
	if child.prompt == nil {
		child.prompt = metadata.prompt
	}
	if metadata.model != nil {
		child.model = metadata.model
	}
	if metadata.effort != nil {
		child.effort = metadata.effort
	}
	if child.preview == nil {
		child.preview = metadata.preview
	}
	if child.nickname == nil {
		child.nickname = metadata.nickname
	}
	if child.role == nil {
		child.role = metadata.role
	}
	if child.path == nil {
		child.path = metadata.path
	}
	if child.discovered && (subagents.description(child) != before || !sameString(child.role, role)) {
		subagents.emitProgress(child)
	}
	if child.discovered && changed {
		subagents.report(domain.SubagentReportSubagentMetadata{SubagentMetadata: subagents.metadata(child), ID: child.id})
	}
}

func (subagents *CodexSubagents) metadata(child *codexChild) domain.SubagentMetadata {
	return domain.SubagentMetadata{Prompt: child.prompt, Model: child.model, Effort: child.effort}
}

func (subagents *CodexSubagents) discover(child *codexChild) {
	if subagents.closed {
		return
	}
	if child.discovered {
		subagents.requestMetadata(child)
		return
	}
	child.discovered = true
	subagents.report(domain.SubagentReportSubagentStarted{
		SubagentMetadata: subagents.metadata(child),
		ID:               child.id,
		Description:      subagents.description(child),
		AgentType:        nonEmptyPointer(child.role),
		SessionScoped:    ptr(true),
	})
	// `started` implies working; a buffered child-first state corrects it immediately when needed.
	child.reportedLifecycle = lifecycleWorking
	if child.lifecycle != lifecycleUnknown && child.lifecycle != lifecycleWorking {
		subagents.emitLifecycle(child)
	} else if child.lifecycle == lifecycleWorking && deref(child.statusSummary) != "" {
		subagents.emitLifecycle(child)
	}
	if child.totalTokens > 0 || deref(child.lastToolName) != "" || deref(child.summary) != "" {
		subagents.emitProgress(child)
	}
	for _, activity := range child.bufferedActivity {
		subagents.report(activity)
	}
	child.bufferedActivity = nil
	subagents.requestMetadata(child)
}

func nonEmptyPointer(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

// V2 can announce only subAgentActivity. Read the child's settings without loading its transcript.
func (subagents *CodexSubagents) requestMetadata(child *codexChild) {
	if subagents.readMetadata == nil || subagents.closed || !child.discovered || child.metadataPending ||
		(child.model != nil && child.effort != nil) || child.metadataReads >= 3 {
		return
	}
	child.metadataPending = true
	subagents.metadataQueue.set(child, struct{}{})
	subagents.drainMetadata()
}

// A large spawn batch must not flood the app-server with simultaneous reads.
func (subagents *CodexSubagents) drainMetadata() {
	for !subagents.closed && subagents.readingMetadata < 4 && subagents.metadataQueue.len() > 0 {
		child := subagents.metadataQueue.keys[0]
		subagents.metadataQueue.delete(child)
		if held, ok := subagents.children.get(child.id); !ok || held != child {
			continue
		}
		subagents.readingMetadata++
		child.metadataReads++
		go subagents.loadMetadata(child)
	}
}

func (subagents *CodexSubagents) loadMetadata(child *codexChild) {
	thread, err := subagents.readMetadata(context.Background(), child.id)
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	wasBusy := subagents.busy()
	// Discovery can beat persistence. Later child activity retries, at most three times per child.
	if held, ok := subagents.children.get(child.id); err == nil && !subagents.closed && thread.ID == child.id && ok && held == child {
		// A thread/started notification received during the read has fresher settings.
		metadata := childMetadata{model: child.model, effort: child.effort}
		if metadata.model == nil {
			metadata.model = nonempty(thread.Model)
		}
		if metadata.effort == nil {
			metadata.effort = nonempty(thread.ReasoningEffort)
		}
		subagents.mergeMetadata(child, metadata)
	}
	child.metadataPending = false
	subagents.readingMetadata--
	subagents.drainMetadata()
	subagents.changedBusy(wasBusy)
}

func (subagents *CodexSubagents) setLifecycle(child *codexChild, lifecycle childLifecycle, summary *string) {
	wasActive := child.active
	if lifecycle == lifecycleWorking && child.lifecycle != lifecycleWorking {
		child.lastToolName = nil
	}
	child.lifecycle = lifecycle
	child.active = lifecycle == lifecycleWorking
	if child.active != wasActive {
		if child.active {
			subagents.activeChildren++
		} else {
			subagents.activeChildren--
		}
	}
	if lifecycle != lifecycleWorking {
		subagents.liveTurnsByThread.delete(child.id)
		delete(subagents.heldStops, child.id)
		subagents.dropPendingItems(child.id)
	}
	if lifecycle == lifecycleWorking {
		child.statusSummary = summary
	} else {
		child.statusSummary = nil
	}
	if deref(summary) != "" && lifecycle != lifecycleWorking {
		child.summary = ptr(subagentFirstLine(*summary))
	}
	subagents.emitLifecycle(child)
}

func (subagents *CodexSubagents) emitLifecycle(child *codexChild) {
	if !child.discovered || subagents.closed {
		return
	}
	lifecycle := child.lifecycle
	switch lifecycle {
	case lifecycleUnknown:
		return
	case lifecycleWorking, lifecycleIdle:
		statusSummary := child.summary
		if lifecycle == lifecycleWorking {
			statusSummary = child.statusSummary
		}
		if child.reportedLifecycle == lifecycle && sameString(child.reportedStatusSummary, statusSummary) {
			return
		}
		child.reportedLifecycle = lifecycle
		child.reportedStatusSummary = statusSummary
		subagents.report(domain.SubagentReportSubagentStatus{ID: child.id, Status: domain.SubagentLiveStatus(lifecycle), Summary: nonEmptyPointer(statusSummary)})
		return
	}
	terminalSummary := deref(child.summary)
	if child.reportedLifecycle == lifecycle && sameString(child.reportedStatusSummary, &terminalSummary) {
		return
	}
	child.reportedLifecycle = lifecycle
	child.reportedStatusSummary = &terminalSummary
	subagents.report(domain.SubagentReportSubagentFinished{ID: child.id, Status: domain.SubagentTerminalStatus(lifecycle), Summary: terminalSummary})
}

func (subagents *CodexSubagents) emitProgress(child *codexChild) {
	if !child.discovered || subagents.closed {
		return
	}
	description := subagents.description(child)
	key, _ := jsonx.Marshal([]any{description, child.lastToolName, child.summary, child.totalTokens})
	if child.progressKey != nil && *child.progressKey == string(key) {
		return
	}
	child.progressKey = ptr(string(key))
	subagents.report(domain.SubagentReportSubagentProgress{
		ID:           child.id,
		Description:  description,
		AgentType:    nonEmptyPointer(child.role),
		LastToolName: nonEmptyPointer(child.lastToolName),
		Summary:      nonEmptyPointer(child.summary),
		TotalTokens:  child.totalTokens,
	})
}

func (subagents *CodexSubagents) queueActivity(child *codexChild, activity domain.SubagentReportSubagentActivity) {
	if child.seenActivity[activity.ActivityID] {
		return
	}
	child.seenActivity[activity.ActivityID] = true
	child.seenActivityOrder = append(child.seenActivityOrder, activity.ActivityID)
	if len(child.seenActivityOrder) > maxSeenActivity {
		oldest := child.seenActivityOrder[0]
		child.seenActivityOrder = child.seenActivityOrder[1:]
		delete(child.seenActivity, oldest)
	}
	if child.discovered && !subagents.closed {
		subagents.report(activity)
		return
	}
	child.bufferedActivity = append(child.bufferedActivity, activity)
	if len(child.bufferedActivity) > maxBufferedActivity {
		child.bufferedActivity = child.bufferedActivity[1:]
	}
}

func (subagents *CodexSubagents) ignore(threadID string) {
	subagents.ignoredThreads[threadID] = true
	subagents.liveTurnsByThread.delete(threadID)
	if child, ok := subagents.children.get(threadID); ok && child.active {
		subagents.activeChildren--
	}
	subagents.children.delete(threadID)
	subagents.dropPendingItems(threadID)
}

func (subagents *CodexSubagents) dropPendingItems(threadID string) {
	for _, itemID := range subagents.pendingItems.orderedKeys() {
		if pending, _ := subagents.pendingItems.get(itemID); pending.threadID == threadID {
			subagents.pendingItems.delete(itemID)
		}
	}
}

func (subagents *CodexSubagents) withBusy(change func() bool) bool {
	subagents.mu.Lock()
	defer subagents.mu.Unlock()
	wasBusy := subagents.busy()
	result := change()
	subagents.changedBusy(wasBusy)
	return result
}

func (subagents *CodexSubagents) changedBusy(wasBusy bool) {
	if busy := subagents.busy(); busy != wasBusy {
		subagents.onBusyChanged(busy)
	}
}
