package application

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// The phone's view, derived from workspace state alone, and the difference between two of them.
// Nothing here reads state the desktop does not already derive, so a phone and the window in front
// of the user can never disagree about what a thread is doing.

// How many threads the list carries. Far more than a phone scrolls, and still bounded.
const MobileThreadLimit = 100

// How many messages of the open thread travel. Each one is cut to length by the transcript itself.
const MobileTranscriptMessages = 40

// How many older messages one "history" query answers with.
const MobileHistoryPage = 100

// How much of one message travels: a long reply whole, a pasted log not without end.
const MobileMessageTextLimit = 40_000

// How many of the open thread's workflows, processes and subagents travel, live ones first.
const MobileBackgroundLimit = 100

// How much of a background item's line is carried; the phone shows two lines of it.
const MobileBackgroundDetailLimit = 300

// How much of an approval's tool input is carried. Enough to judge it, never a whole file.
const MobileApprovalDetailLimit = 4_000

// How many open decisions the open thread carries; the phone answers them one at a time.
const MobileDecisionLimit = 20

// How much of a suggestion's prompt is carried. The phone shows it only on request.
const MobileSuggestionPromptLimit = 4_000

const mobileUntitled = "Untitled thread"

func EmptyMobileView() *contracts.MobileView {
	return &contracts.MobileView{
		Groups:   []*contracts.MobileProjectGroup{},
		Activity: contracts.MobileActivity{Priority: []*contracts.MobileThreadEntry{}, Running: []*contracts.MobileThreadEntry{}, Threads: []*contracts.MobileThreadEntry{}, Snoozed: []*contracts.MobileThreadEntry{}},
		Theme:    contracts.MobileTheme{Dark: "aicodingtool-dark", Light: "aicodingtool-light", Mode: "dark"},
		Engines:  noEngines,
		Upkeep:   []*contracts.MobileUpkeepItem{},
		Usage:    noUsage,
		Archived: new(contracts.Count(0)),
	}
}

var noEngines = contracts.MobileEngines{}

var noUsage = contracts.MobileUsage{Engines: []*contracts.MobileEngineUsage{}}

// MobileEngines per engine status, by the status's identity.
var projectedEngines struct {
	sync.Mutex
	status  *domain.EngineStatus
	engines contracts.MobileEngines
}

func mobileEngineOf(engines *contracts.MobileEngines, engine domain.AgentEngine) **contracts.MobileEngine {
	switch engine {
	case domain.AgentEngineClaude:
		return &engines.Claude
	case domain.AgentEngineCodex:
		return &engines.Codex
	case domain.AgentEngineOpencode:
		return &engines.Opencode
	}
	return nil
}

func mobileModelOf(spec *domain.ModelSpec) *contracts.MobileModel {
	efforts := make([]domain.AgentEffort, len(spec.Efforts))
	for index, effort := range spec.Efforts {
		efforts[index] = effort.ID
	}
	model := &contracts.MobileModel{ID: spec.ID, Label: spec.Label, Description: spec.Description, Efforts: efforts}
	if spec.Provider != nil && *spec.Provider != "" {
		model.Provider = spec.Provider
	}
	return model
}

// The engines as a phone reads them, kept per status so a view that did not read the engines again carries the same ones.
func ProjectMobileEngines(status *domain.EngineStatus) contracts.MobileEngines {
	if status == nil {
		return noEngines
	}
	projectedEngines.Lock()
	defer projectedEngines.Unlock()
	if projectedEngines.status == status {
		return projectedEngines.engines
	}
	engines := contracts.MobileEngines{}
	for _, engine := range domain.AgentEngines {
		readiness := status.Get(engine)
		if readiness == nil {
			continue
		}
		projected := &contracts.MobileEngine{Access: readiness.Access}
		if readiness.Models != nil {
			projected.Models = slices.Clone(readiness.Models)
		}
		if readiness.Catalog != nil {
			projected.Catalog = make([]*contracts.MobileModel, len(readiness.Catalog))
			for index, spec := range readiness.Catalog {
				projected.Catalog[index] = mobileModelOf(spec)
			}
		}
		*mobileEngineOf(&engines, engine) = projected
	}
	projectedEngines.status, projectedEngines.engines = status, engines
	return engines
}

// What this build calls its engines, modes, models and efforts, as a phone app built apart from it
// reads them. A function, built once on first use.
var MobileVocabulary = sync.OnceValue(func() *contracts.MobileVocabulary {
	vocabulary := &contracts.MobileVocabulary{}
	for _, engine := range domain.AgentEngines {
		terms := &contracts.MobileEngineTerms{ID: engine, Label: domain.EngineLabel(string(engine)), FastMode: domain.CapabilitiesFor(string(engine)).FastMode}
		for _, policy := range domain.AgentEngineOfferedPolicies(string(engine)) {
			text := domain.Policies[policy]
			terms.Policies = append(terms.Policies, &contracts.MobilePolicyTerm{ID: policy, Label: text.Label, Description: text.Description})
		}
		for _, spec := range domain.BuiltInModels.Models.Get(engine) {
			terms.Models = append(terms.Models, mobileModelOf(spec))
		}
		vocabulary.Engines = append(vocabulary.Engines, terms)
	}
	for _, spec := range domain.AgentEngineEfforts(domain.AgentEfforts...) {
		vocabulary.Efforts = append(vocabulary.Efforts, &contracts.MobileEffortTerm{ID: spec.ID, Label: spec.Label, Description: spec.Description, Delegates: spec.Delegates})
	}
	for _, option := range domain.ThreadRoles {
		vocabulary.Roles = append(vocabulary.Roles, &contracts.MobileRoleTerm{ID: option.Role, Label: option.Label})
	}
	return vocabulary
})

// MobileEngineView per sent engines, by their identity.
var engineViews struct {
	sync.Mutex
	engines *contracts.MobileEngines
	view    *MobileEngineView
}

// The other half of ProjectMobileEngines, kept per engines so a picker reads one stable catalogue. Nil engines are none sent.
func MobileProjectionMobileEngineView(engines *contracts.MobileEngines) *MobileEngineView {
	if engines == nil {
		engines = &noEngines
	}
	engineViews.Lock()
	defer engineViews.Unlock()
	if engineViews.view != nil && engineViews.engines == engines {
		return engineViews.view
	}
	// A phone never measures a model's context, so a model it is sent carries none.
	access := domain.ByEngine(func(engine domain.AgentEngine) domain.EngineReadiness {
		readiness := domain.EngineReadiness{Access: domain.EngineAccessReady}
		sent := *mobileEngineOf(engines, engine)
		if sent == nil {
			return readiness
		}
		readiness.Access = sent.Access
		readiness.Models = sent.Models
		if sent.Catalog != nil {
			readiness.Catalog = make([]*domain.ModelSpec, len(sent.Catalog))
			for index, model := range sent.Catalog {
				spec := &domain.ModelSpec{ID: model.ID, Label: model.Label, Description: model.Description, ContextWindow: 0, Efforts: domain.AgentEngineEfforts(model.Efforts...)}
				if model.Provider != nil && *model.Provider != "" {
					spec.Provider = model.Provider
				}
				readiness.Catalog[index] = spec
			}
		}
		return readiness
	})
	claude, codex, opencode := access[domain.AgentEngineClaude], access[domain.AgentEngineCodex], access[domain.AgentEngineOpencode]
	view := &MobileEngineView{
		Catalog: *domain.EngineCatalog(&domain.EngineStatus{Claude: &claude, Codex: &codex, Opencode: &opencode}),
		Access:  MobileEngineViewAccess{Claude: claude, Codex: codex, Opencode: opencode},
	}
	engineViews.engines, engineViews.view = engines, view
	return view
}

// The approvals, decisions and suggestions a coordination view holds, whichever member it is.
func mobileCoordinationParts(view CoordinationView) (approvals []*CoordinationApprovalView, decisions []*CoordinationDecisionView, proposals []*ProposalView) {
	switch view := view.(type) {
	case CoordinationViewTrue:
		return view.Approvals, view.Decisions, view.Proposals
	case *CoordinationViewTrue:
		return view.Approvals, view.Decisions, view.Proposals
	case CoordinationViewFalse:
		return view.Approvals, view.Decisions, view.Proposals
	case *CoordinationViewFalse:
		return view.Approvals, view.Decisions, view.Proposals
	}
	return nil, nil, nil
}

func mobileTitle(title string) string {
	if title == "" {
		return mobileUntitled
	}
	return title
}

// Everything a phone reads, derived from workspace state alone.
func ProjectMobileView(state *WorkspaceState, at float64) *contracts.MobileView {
	view := DeriveView(state)
	collections := WorkspaceViewCollections.Of(state)
	here := RosterActivity{Working: view.RunningThreadIDs, Blocked: view.BlockedThreadIDs, Ranked: collections.EveryRanked}
	summaries := ThreadSummaries(state, contracts.ThreadFilter{Scope: contracts.ProjectScopeAll{}, Limit: new(float64(MobileThreadLimit))}, at)
	threadsByID := make(map[string]*domain.Thread, len(state.Threads))
	for _, thread := range state.Threads {
		threadsByID[thread.ID] = thread
	}
	projectsByID := make(map[string]*domain.Project, len(state.Projects))
	for _, project := range state.Projects {
		projectsByID[project.ID] = project
	}
	inProject := map[string][]*contracts.MobileThreadEntry{}
	var inNone []*contracts.MobileThreadEntry
	entriesByID := map[string]*contracts.MobileThreadEntry{}
	for _, summary := range summaries {
		thread := threadsByID[summary.ID]
		var project *domain.Project
		if summary.ProjectID != nil && *summary.ProjectID != "" {
			project = projectsByID[*summary.ProjectID]
		}
		// A coordinator's row speaks for the threads under it, which the phone reaches through it, as the
		// desktop's row does: their approvals, their work going on, and what else waits on the user.
		// Each thread under it stands as the computer holding it last said, whichever computers the desktop's sidebar shows.
		var roster *CoordinatorRoster
		var waiting, headline *string
		if thread != nil {
			roster = CoordinationCoordinatorRoster(state, thread, here, nil)
			waiting = WaitingLine(roster.Waiting, "you")
			headline = RowHeadline(thread, waiting)
		}
		blocked := view.BlockedThreadIDs.Has(summary.ID) || (roster != nil && roster.Blocked)
		running := summary.Status == contracts.ThreadSummaryStatusRunning || (roster != nil && roster.Running)
		entry := &contracts.MobileThreadEntry{
			ID:             summary.ID,
			Title:          mobileTitle(summary.Title),
			Status:         contracts.MobileRunStatus(summary.Status),
			LastActivityAt: summary.LastActivityAt,
			Headline:       headline,
			Attention:      roster != nil && roster.Dismissable,
		}
		switch {
		case blocked:
			entry.Status = contracts.MobileRunStatusAwaitingApproval
		case running:
			entry.Status = contracts.MobileRunStatusRunning
		}
		if project != nil {
			entry.ProjectID, entry.ProjectName = &project.ID, new(domain.ProjectName(project))
		}
		if thread != nil {
			entry.Unread = (thread.OutcomeUnread != nil && *thread.OutcomeUnread) || domain.NewestUnreadFinding(thread) != nil || slices.ContainsFunc(roster.Threads, domain.HasUnreadAttention)
			if thread.Outcome != nil && *thread.Outcome != "" {
				entry.Outcome = thread.Outcome
			}
			entry.SnoozedUntil = thread.SnoozedUntil
		}
		if waiting != nil {
			entry.Waiting = new(true)
		}
		if summary.ProjectID != nil {
			inProject[*summary.ProjectID] = append(inProject[*summary.ProjectID], entry)
		} else {
			inNone = append(inNone, entry)
		}
		entriesByID[entry.ID] = entry
	}
	// Every open project is a group even with nothing in it, because a group is how a phone starts one.
	groups := make([]*contracts.MobileProjectGroup, 0, len(view.Projects)+1)
	for _, project := range view.Projects {
		groups = append(groups, &contracts.MobileProjectGroup{ProjectID: &project.ID, Name: domain.ProjectName(project), Threads: RankGroup(inProject[project.ID], threadsByID)})
	}
	// Always last, and always there, because it is the only way to start a thread in no project.
	groups = append(groups, &contracts.MobileProjectGroup{ProjectID: nil, Name: "Recents", Threads: RankGroup(inNone, threadsByID)})
	thread := projectMobileThread(state, view, view.StoppableCoordinatorIDs)
	mobile := &contracts.MobileView{
		Groups:    groups,
		Activity:  projectMobileActivity(state, &ThreadActivity{Working: here.Working, Blocked: here.Blocked, Ranked: here.Ranked}, entriesByID),
		Theme:     ProjectMobileTheme(state),
		Engines:   ProjectMobileEngines(state.EngineStatus),
		Upkeep:    projectMobileUpkeep(state),
		Dictation: state.Dictation.Model == "ready",
		Usage:     projectMobileUsage(state, view, at),
		Archived:  new(contracts.Count(len(collections.ArchivedThreads))),
		Thread:    thread,
		Error:     state.ActionError,
	}
	if thread == nil {
		mobile.Draft = projectMobileDraft(state, view)
	}
	return mobile
}

// JavaScript's Math.round.
func mobileRound(value float64) float64 {
	return math.Floor(value + 0.5)
}

func share(utilization *float64) *float64 {
	if utilization == nil {
		return nil
	}
	return new(mobileRound(domain.BarShare(utilization)))
}

func mobilePlanReport(reports PlanUsageStateReports, engine domain.AgentEngine) domain.PlanUsage {
	switch engine {
	case domain.AgentEngineClaude:
		return reports.Claude
	case domain.AgentEngineCodex:
		return reports.Codex
	case domain.AgentEngineOpencode:
		return reports.Opencode
	}
	return nil
}

func mobileOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// Every engine's plan for the Usage sheet, and the windows the composer's model spends, as the
// desktop composer's meter and warning read them.
func projectMobileUsage(state *WorkspaceState, view *OwnWorkspaceView, at float64) contracts.MobileUsage {
	reports, dismissed := state.PlanUsage.Reports, state.PlanUsage.Dismissed
	drawn := func(item *domain.UsageWindow) *contracts.MobileUsageWindow {
		return &contracts.MobileUsageWindow{ID: item.ID, Label: item.Label, Share: share(item.Utilization), ResetsAt: item.ResetsAt}
	}
	engines := []*contracts.MobileEngineUsage{}
	for _, engine := range domain.AgentEngines {
		switch usage := mobilePlanReport(reports, engine).(type) {
		case domain.PlanUsageAvailable:
			row := &contracts.MobileEngineUsage{Engine: engine, Label: domain.EngineLabel(string(engine)), Windows: []*contracts.MobileUsageWindow{}}
			if plan, ok := domain.PlanLabel(usage.Subscription); ok {
				row.Plan = &plan
			}
			for _, window := range usage.Windows {
				row.Windows = append(row.Windows, drawn(window))
			}
			engines = append(engines, row)
		case domain.ComputerUseRunConfigUnavailable:
			engines = append(engines, &contracts.MobileEngineUsage{Engine: engine, Label: domain.EngineLabel(string(engine)), Windows: []*contracts.MobileUsageWindow{}, Message: &usage.Message})
		}
	}
	if len(engines) == 0 {
		return noUsage
	}
	var spent []*domain.UsageWindow
	if view.Model != nil {
		spent = domain.WindowsFor(mobilePlanReport(reports, view.Engine), domain.ModelLabel(domain.EngineCatalog(state.EngineStatus), string(view.Engine), *view.Model))
	}
	usage := contracts.MobileUsage{Engines: engines}
	tightest := domain.TightestWindow(spent)
	if tightest == nil {
		return usage
	}
	session, week := domain.SessionAndWeek(spent)
	var rows []*domain.UsageWindow
	if session != nil || week != nil {
		for _, row := range []*domain.UsageWindow{session, week} {
			if row != nil {
				rows = append(rows, row)
			}
		}
	} else {
		rows = []*domain.UsageWindow{tightest}
	}
	key := domain.UsageWarningKey(string(view.Engine), tightest)
	warned := view.LimitPause == nil && mobileOrZero(tightest.Utilization) >= domain.NearLimitShare && !domain.UsageWarningDismissed(dismissed, key, at)
	composer := &contracts.MobileComposerUsage{Share: mobileOrZero(share(tightest.Utilization)), Meter: []*contracts.MobileComposerUsageMeter{}, Spends: []string{}}
	for _, row := range rows {
		span, ok := domain.WindowSpan(row)
		if !ok {
			span = row.Label
		}
		composer.Meter = append(composer.Meter, &contracts.MobileComposerUsageMeter{Span: span, Share: mobileOrZero(share(row.Utilization))})
	}
	for _, item := range spent {
		composer.Spends = append(composer.Spends, item.ID)
	}
	if warned {
		composer.Warning = &contracts.MobileComposerUsageWarning{Key: key, Share: mobileOrZero(share(tightest.Utilization)), Window: domain.EngineLabel(string(view.Engine)) + " · " + tightest.Label, ResetsAt: tightest.ResetsAt}
	}
	usage.Composer = composer
	return usage
}

// This computer's update rows as the Computers page draws a paired one's, each with a command a phone may send.
func projectMobileUpkeep(state *WorkspaceState) []*contracts.MobileUpkeepItem {
	_, installing := state.AppUpdateRequest.(domain.AppUpdateRequestInstalling)
	own := OwnUpkeep(state)
	items := make([]*contracts.MobileUpkeepItem, len(own))
	for index, item := range own {
		row := &contracts.MobileUpkeepItem{Key: item.Key, Label: item.Label, Version: item.Version, State: contracts.MobileUpkeepItemState{Text: item.State.Text}}
		if item.State.Tone != nil {
			row.State.Tone = new(contracts.MobileUpkeepItemStateTone(*item.State.Tone))
		}
		if item.Action != nil && contracts.IsPhoneCommand(item.Action.Command) {
			if command, ok := item.Action.Command.(contracts.MobileCommand); ok {
				row.Action = &contracts.MobileUpkeepItemAction{Label: item.Action.Label, Command: command}
			}
		}
		if installing && item.Key == "app" {
			row.Installing = new(true)
		}
		items[index] = row
	}
	return items
}

// The same lists the desktop's activity sidebar draws, ranked by the same rules, a coordinator
// standing for the threads under it, over the threads the list carries. A thread past the list's
// limit is not ranked either: it is too old to be waiting on anyone. `here` is read for its
// working, blocked and ranked sets.
func projectMobileActivity(state *WorkspaceState, here *ThreadActivity, entriesByID map[string]*contracts.MobileThreadEntry) contracts.MobileActivity {
	var listed []*domain.Thread
	for _, thread := range WorkspaceViewCollections.Of(state).VisibleThreads {
		if _, ok := entriesByID[thread.ID]; ok {
			listed = append(listed, thread)
		}
	}
	sections, _ := OwnCoordinationSections(state, listed, RosterActivity{Working: here.Working, Blocked: here.Blocked, Ranked: here.Ranked}, state.Computers.Paired)
	rows := func(threads []*domain.Thread) []*contracts.MobileThreadEntry {
		entries := []*contracts.MobileThreadEntry{}
		for _, thread := range threads {
			if entry, ok := entriesByID[thread.ID]; ok {
				entries = append(entries, entry)
			}
		}
		return entries
	}
	priority := rows(sections[ActivitySectionsKeyPriority])
	for index, entry := range priority {
		snoozable := *entry
		snoozable.Snoozable = new(true)
		priority[index] = &snoozable
	}
	return contracts.MobileActivity{Priority: priority, Running: rows(sections[ActivitySectionsKeyRunning]), Threads: rows(sections[ActivitySectionsKeyThreads]), Snoozed: rows(sections[ActivitySectionsKeySnoozed])}
}

// The desktop's family in both faces, so a phone on "auto" can pick its own without asking.
func ProjectMobileTheme(state *WorkspaceState) contracts.MobileTheme {
	family := domain.ThemeOrDefault(state.Theme).Family
	return contracts.MobileTheme{Dark: domain.ThemeFor(family, domain.ThemeVariantDark).ID, Light: domain.ThemeFor(family, domain.ThemeVariantLight).ID, Mode: state.ThemeMode}
}

// One group's rows: threads waiting on the user first, then the rest in the sidebar's own order.
// A thread holds its slot when it starts, speaks or finishes, so the list never reshuffles under
// a thumb; only waiting on the user lifts a row. `entries` arrive newest first already.
func RankGroup(entries []*contracts.MobileThreadEntry, threadsByID map[string]*domain.Thread) []*contracts.MobileThreadEntry {
	ranked := []*contracts.MobileThreadEntry{}
	byThread := map[string]*contracts.MobileThreadEntry{}
	var threads []*domain.Thread
	for _, entry := range entries {
		if entry.Status == contracts.MobileRunStatusAwaitingApproval {
			ranked = append(ranked, entry)
			continue
		}
		byThread[entry.ID] = entry
		if thread, ok := threadsByID[entry.ID]; ok {
			threads = append(threads, thread)
		}
	}
	for _, thread := range OrderThreads(threads) {
		if entry, ok := byThread[thread.ID]; ok {
			ranked = append(ranked, entry)
		}
	}
	return ranked
}

func mobileSettingsOf(view *OwnWorkspaceView) contracts.MobileThreadSettings {
	settings := contracts.MobileThreadSettings{Engine: string(view.Engine), Model: view.Model, Effort: view.Effort, FastMode: new(view.FastMode), Policy: domain.ExecutionPolicy(view.Policy)}
	if view.Coordinating {
		settings.Coordinating = new(true)
	}
	return settings
}

// What the desktop's empty composer is pointed at, which is all a thread yet to exist amounts to.
func projectMobileDraft(state *WorkspaceState, view *OwnWorkspaceView) *contracts.MobileDraftView {
	project := view.CurrentProject
	draft := &contracts.MobileDraftView{
		Settings:     mobileSettingsOf(view),
		Worktree:     view.DraftWorktree,
		WorktreeName: view.DraftWorktreeName,
		Worktrees:    view.DraftWorktreeChoices,
		CanWorktree:  project != nil && project.WorkspaceID != nil && *project.WorkspaceID != "",
	}
	if project != nil {
		draft.ProjectID, draft.ProjectName = &project.ID, new(domain.ProjectName(project))
	}
	return draft
}

func mobileProjectionLocationOf(view *OwnWorkspaceView) contracts.MobileLocation {
	switch location := view.Location.(type) {
	case ThreadLocationWorktree:
		return contracts.MobileLocationWorktree{Name: domain.WorktreeName(&location.Worktree), Threads: location.Threads}
	case *ThreadLocationWorktree:
		return contracts.MobileLocationWorktree{Name: domain.WorktreeName(&location.Worktree), Threads: location.Threads}
	case contracts.MobileLocation:
		return location
	}
	return domain.WorktreeDestinationLocal{}
}

func changesOf(view *OwnWorkspaceView) *contracts.MobileChanges {
	environment, ok := view.Environment.(contracts.ChangedFilesResultAvailable)
	if !ok {
		return nil
	}
	return &contracts.MobileChanges{Branch: environment.Branch, Files: contracts.Count(len(environment.Files)), Additions: environment.Additions, Deletions: environment.Deletions}
}

// The threads a phone may name: this computer's own, and those on a paired computer working under a
// coordinator here, which the coordinator's screen lists. Nothing else of another computer is ever on the phone's.
func PhoneReachesThread(state *WorkspaceState, taskID string) bool {
	return threadNamed(state.Threads, taskID) != nil || RemoteMemberHolder(state, taskID) != nil
}

// The paired computer holding a thread that works under a coordinator here, or nil.
func RemoteMemberHolder(state *WorkspaceState, taskID string) *PairedComputer {
	holder := ComputerOfThread(state, taskID)
	if holder == nil || holder.State == nil {
		return nil
	}
	thread := threadNamed(holder.State.Threads, taskID)
	if thread == nil || thread.RemoteLead == nil {
		return nil
	}
	for _, member := range CoordinationMembers(state, thread.RemoteLead.ThreadID) {
		if member.Computer != nil && member.Computer.ID == holder.ID && member.Thread.ID == taskID {
			return holder
		}
	}
	return nil
}

// Whether a paired computer says when a message it was asked to drop had already gone; one too old to is never asked for one back.
func SaysQueuedGone(computer *PairedComputer) bool {
	return computer.Capabilities != nil && contracts.SupportsComputerCommand(computer.Capabilities, contracts.TaskCommandTaskDropQueued{MessageID: "", IfWaiting: new(true)})
}

// Only the user's own words travel to the phone's composer and back: images and files are paths on
// the computer, and pasted text and notes ride beside the words, which a phone's composer cannot hold.
func PhoneEditsQueued(message *QueuedMessage) bool {
	return domain.IsUsersOwn(message.Origin, message.Detail, message.Answers) && (message.Steering == nil || !*message.Steering) &&
		len(message.Attachments) == 0 && len(message.Files) == 0 && len(message.Pastes) == 0 && len(message.Annotations) == 0
}

// The transcript's messages as a phone reads them, before what withDetails adds.
func mobileMessages(messages []*contracts.ThreadMessage) []*contracts.MobileMessage {
	mobile := make([]*contracts.MobileMessage, len(messages))
	for index, message := range messages {
		mobile[index] = &contracts.MobileMessage{Kind: message.Kind, Text: message.Text, Origin: message.Origin, At: message.At}
	}
	return mobile
}

// The thread the desktop has open. One on a paired computer is read from that computer's mirror, and
// only while it works under a coordinator here, so it is answered, approved and messaged through this
// computer; its checkout is that computer's, so it offers no review and no move.
func projectMobileThread(state *WorkspaceState, view *OwnWorkspaceView, stoppable jsonx.Set[string]) *contracts.MobileThreadView {
	thread := view.CurrentThread
	if thread == nil {
		return nil
	}
	elsewhere := SelectedComputer(state)
	shown := state
	if elsewhere != nil && elsewhere.State != nil {
		shown = elsewhere.State
	}
	if elsewhere != nil {
		if holder := RemoteMemberHolder(state, thread.ID); holder == nil || holder.ID != elsewhere.ID {
			return nil
		}
	}
	transcript := ThreadTranscript(shown, thread.ID, MobileTranscriptMessages, MobileMessageTextLimit, nil)
	if transcript == nil {
		return nil
	}
	blocked := view.BlockedThreadIDs
	if elsewhere != nil {
		blocked = ThreadActivityThreadActivity(shown).Blocked
	}
	editable := elsewhere == nil || SaysQueuedGone(elsewhere)
	// A coordinator answers for the threads under it, as its Needs-you card does, its own first.
	approvals, decisions, proposals := mobileCoordinationParts(view.Coordination)
	var member *CoordinationApprovalView
	approval := view.Approval
	if approval == nil && len(approvals) > 0 {
		member = approvals[0]
		approval = &member.Approval
	}
	project := ProjectFor(shown, thread)
	diff := DiffFor(shown, thread.ID)
	location := mobileProjectionLocationOf(view)
	mobile := &contracts.MobileThreadView{
		Loading:     new(thread.HistorySummary != nil),
		ID:          thread.ID,
		Title:       mobileTitle(thread.Title),
		WorktreeID:  thread.WorktreeID,
		Messages:    withDetails(thread, mobileMessages(transcript.Messages), int(transcript.Omitted)),
		Omitted:     transcript.Omitted,
		Status:      contracts.MobileRunStatus(view.Status),
		Queued:      []*contracts.MobileQueuedMessage{},
		Settings:    mobileSettingsOf(view),
		Location:    location,
		Worktrees:   []*contracts.MobileWorktreeChoice{},
		Changes:     changesOf(view),
		Members:     mobileProjectionNoMembers,
		Background:  projectBackground(view.Workflows, view.BackgroundProcesses, view.Subagents),
		Decisions:   projectDecisions(thread, decisions),
		Suggestions: projectSuggestions(proposals),
		Stoppable:   new(stoppable.Has(thread.ID)),
		Question:    jsonx.Null[domain.PendingQuestion](),
		Role:        jsonx.Null[domain.ThreadRole](),
		Limit:       jsonx.Null[contracts.MobileLimitPause](),
	}
	if project != nil {
		mobile.ProjectID = &project.ID
	}
	if view.CurrentProject != nil {
		mobile.ProjectName = new(domain.ProjectName(view.CurrentProject))
	}
	if view.StreamingTail != nil {
		mobile.StreamingTail = &view.StreamingTail.Text
	}
	if blocked.Has(thread.ID) {
		mobile.Status = contracts.MobileRunStatusAwaitingApproval
	}
	if view.Question != nil {
		mobile.Question = jsonx.Some(*view.Question)
	}
	if approval != nil {
		shownApproval := contracts.MobileApproval{
			TaskID: approval.TaskID, ApprovalID: approval.ApprovalID, RunID: approval.RunID, Title: approval.Title,
			Description: approval.Description, ToolName: approval.ToolName, Detail: approvalDetail(approval.Input),
		}
		if member != nil && member.Thread.ID != thread.ID {
			shownApproval.From = new(mobileTitle(member.Thread.Title))
		}
		mobile.Approval = &shownApproval
	}
	for _, message := range view.QueuedMessages {
		queued := &contracts.MobileQueuedMessage{ID: message.ID, Text: message.Text, Origin: LabelOf(message.Origin, message.Detail)}
		if message.Steering != nil && *message.Steering {
			queued.Steering = new(true)
		}
		if editable && PhoneEditsQueued(&message.QueuedMessage) && !DrainingQueued(shown, thread.ID, message.ID) {
			queued.Editable = new(true)
		}
		mobile.Queued = append(mobile.Queued, queued)
	}
	if project != nil && elsewhere == nil {
		mobile.Worktrees = WorktreeChoices(state, project.ID, thread.WorktreeID)
	}
	movable := true
	switch location.(type) {
	case contracts.MobileLocationCreating, contracts.MobileLocationReleasing:
		movable = false
	}
	mobile.CanMove = elsewhere == nil && project != nil && project.WorkspaceID != nil && *project.WorkspaceID != "" && movable && !view.RunningThreadIDs.Has(thread.ID)
	if elsewhere == nil {
		_, mobile.Reviewable = ThreadWorkspaceID(state, thread)
	}
	switch {
	case mobileIsBranches(diff.Range):
		mobile.BranchRange = mobileBranchesOf(diff.Range)
	case diff.BranchRange != nil:
		mobile.BranchRange = *diff.BranchRange
	default:
		mobile.BranchRange = domain.DefaultBranchRange
	}
	if domain.IsCoordinator(thread) {
		mobile.Members = projectMembers(state, thread.ID, view)
	}
	if thread.Role != nil {
		mobile.Role = jsonx.Some(*thread.Role)
	}
	if pause := view.LimitPause; pause != nil {
		mobile.Limit = jsonx.Some(contracts.MobileLimitPause{ResetsAt: pause.ResetsAt, Window: contracts.MobileLimitPauseWindow(pause.Window), Position: pause.Position})
	}
	if elsewhere != nil {
		mobile.Computer = &elsewhere.Name
	}
	return mobile
}

func mobileIsBranches(diffRange domain.DiffRange) bool {
	switch diffRange.(type) {
	case domain.DiffRangeBranches, *domain.DiffRangeBranches:
		return true
	}
	return false
}

func mobileBranchesOf(diffRange domain.DiffRange) domain.DiffRangeBranches {
	switch diffRange := diffRange.(type) {
	case domain.DiffRangeBranches:
		return diffRange
	case *domain.DiffRangeBranches:
		return *diffRange
	}
	return domain.DefaultBranchRange
}

var noDecisions = []*contracts.MobileDecision{}

// The decisions the open thread's Needs-you card answers, naming the thread that asks when it is another.
func projectDecisions(open *domain.Thread, decisions []*CoordinationDecisionView) []*contracts.MobileDecision {
	if len(decisions) == 0 {
		return noDecisions
	}
	if len(decisions) > MobileDecisionLimit {
		decisions = decisions[:MobileDecisionLimit]
	}
	projected := make([]*contracts.MobileDecision, len(decisions))
	for index, item := range decisions {
		decision := &contracts.MobileDecision{TaskID: item.Thread.ID, ID: item.Decision.ID, Question: item.Decision.Question, Options: make([]*contracts.MobileDecisionOptions, len(item.Decision.Options))}
		if item.Thread.ID != open.ID {
			decision.From = new(mobileTitle(item.Thread.Title))
		}
		if item.Decision.Context != nil && *item.Decision.Context != "" {
			decision.Context = item.Decision.Context
		}
		for at, option := range item.Decision.Options {
			shown := &contracts.MobileDecisionOptions{Label: option.Label}
			if option.Description != nil && *option.Description != "" {
				shown.Description = option.Description
			}
			if option.Recommended != nil && *option.Recommended {
				shown.Recommended = new(true)
			}
			decision.Options[at] = shown
		}
		projected[index] = decision
	}
	return projected
}

var noSuggestions = []*contracts.MobileSuggestion{}

// MobileSuggestions per proposal list, by the list's identity.
var projectedSuggestions struct {
	sync.Mutex
	proposals   []*ProposalView
	suggestions []*contracts.MobileSuggestion
}

// The suggestions to start here, kept per list since the view holds the list while no suggestion moves.
func projectSuggestions(proposals []*ProposalView) []*contracts.MobileSuggestion {
	if len(proposals) == 0 {
		return noSuggestions
	}
	projectedSuggestions.Lock()
	defer projectedSuggestions.Unlock()
	if projectedSuggestions.suggestions != nil && jsonx.SameSlice(projectedSuggestions.proposals, proposals) {
		return projectedSuggestions.suggestions
	}
	suggestions := []*contracts.MobileSuggestion{}
	for _, item := range proposals {
		if item.Elsewhere != nil {
			continue
		}
		proposal := item.Proposal
		suggestion := &contracts.MobileSuggestion{ID: proposal.ID, Title: proposal.Title, Prompt: ClipText(proposal.Prompt, MobileSuggestionPromptLimit)}
		if proposal.Brief != nil {
			suggestion.Brief = &contracts.MobileBrief{Intent: proposal.Brief.Intent, DoneWhen: proposal.Brief.DoneWhen, Delivers: domain.DeliveryLabel(string(proposal.Brief.Delivers))}
		}
		if item.Starting {
			suggestion.Starting = new(true)
		}
		if proposal.Error != nil && *proposal.Error != "" && !item.Starting {
			suggestion.Error = proposal.Error
		}
		suggestions = append(suggestions, suggestion)
	}
	projectedSuggestions.proposals, projectedSuggestions.suggestions = proposals, suggestions
	return suggestions
}

// Every archived thread as the phone's Archived list reads it, newest first. Retention bounds the list, and only a query reads it.
func MobileArchivedThreads(state *WorkspaceState) []*contracts.MobileArchivedThread {
	archived := WorkspaceViewCollections.Of(state).ArchivedThreads
	threads := make([]*contracts.MobileArchivedThread, len(archived))
	for index, thread := range archived {
		archivedAt := mobileOrZero(thread.ArchivedAt)
		row := &contracts.MobileArchivedThread{ID: thread.ID, Title: mobileTitle(thread.Title), ArchivedAt: archivedAt, DeletesAt: archivedAt + domain.ArchiveRetentionMs}
		if project := ProjectFor(state, thread); project != nil {
			row.ProjectName = new(domain.ProjectName(project))
		}
		threads[index] = row
	}
	return threads
}

// The last background projection and the lists it was made from.
type mobileHeldBackground struct {
	workflows []*domain.Workflow
	processes []*domain.BackgroundProcess
	subagents []*domain.Subagent
	items     []*contracts.MobileBackgroundItem
}

var heldBackground struct {
	sync.Mutex
	held *mobileHeldBackground
}

// The view hands a fresh empty list whenever a thread has none, so every empty list is the same list.
func sameList[T any](held, next []T) bool {
	return jsonx.Same(held, next) || (len(held) == 0 && len(next) == 0)
}

// The desktop's Processes and Subagents lists as one, kept while its sources are, since a streaming run projects many times a second.
func projectBackground(workflows []*domain.Workflow, processes []*domain.BackgroundProcess, subagents []*domain.Subagent) []*contracts.MobileBackgroundItem {
	heldBackground.Lock()
	defer heldBackground.Unlock()
	if held := heldBackground.held; held != nil && sameList(held.workflows, workflows) && sameList(held.processes, processes) && sameList(held.subagents, subagents) {
		return held.items
	}
	var all []*contracts.MobileBackgroundItem
	for _, workflow := range workflows {
		running := workflow.Status == domain.WorkflowStatusRunning
		status := contracts.MobileBackgroundItemStatus(workflow.Status)
		if running {
			status = contracts.MobileBackgroundItemStatusWorking
		}
		item := &contracts.MobileBackgroundItem{ID: workflow.ID, Kind: contracts.MobileBackgroundItemKindWorkflow, Title: workflow.Name, Detail: domain.WorkflowLabel(workflow), Status: status, Live: running, Stoppable: running}
		if workflow.Stopping != nil && *workflow.Stopping {
			item.Stopping = new(true)
		}
		all = append(all, item)
	}
	for _, process := range processes {
		item := &contracts.MobileBackgroundItem{ID: process.ID, Kind: contracts.MobileBackgroundItemKind(process.Kind), Title: process.Description, Detail: domain.ProcessLabel(process), Status: contracts.MobileBackgroundItemStatusWorking, Live: true, Stoppable: true}
		if process.Stopping != nil && *process.Stopping {
			item.Stopping = new(true)
		}
		all = append(all, item)
	}
	for _, subagent := range domain.OrderSubagents(subagents) {
		item := &contracts.MobileBackgroundItem{
			ID: subagent.ID, Kind: contracts.MobileBackgroundItemKindAgent, Title: subagent.Description, Detail: ClipText(domain.SubagentLine(subagent), MobileBackgroundDetailLimit),
			Status: contracts.MobileBackgroundItemStatus(subagent.Status), Live: subagent.Status == domain.SubagentStatusWorking || subagent.Status == domain.SubagentStatusIdle,
			Stoppable: subagent.Status == domain.SubagentStatusWorking,
		}
		if subagent.Stopping != nil && *subagent.Stopping {
			item.Stopping = new(true)
		}
		all = append(all, item)
	}
	items := []*contracts.MobileBackgroundItem{}
	for _, live := range []bool{true, false} {
		for _, item := range all {
			if item.Live == live && len(items) < MobileBackgroundLimit {
				items = append(items, item)
			}
		}
	}
	heldBackground.held = &mobileHeldBackground{workflows: workflows, processes: processes, subagents: subagents, items: items}
	return items
}

var mobileProjectionNoMembers = []*contracts.MobileMember{}

// What was derived from stored messages, by the message's identity: the last few thousand
// messages read, so a transcript projected many times a second reads each message once.
type mobileMessageMemo[V any] struct {
	sync.Mutex
	prev, cur map[*domain.ConversationMessage]V
}

const mobileMessageMemoSize = 4096

func (m *mobileMessageMemo[V]) of(message *domain.ConversationMessage, read func() V) V {
	m.Lock()
	defer m.Unlock()
	if value, ok := m.cur[message]; ok {
		return value
	}
	value, ok := m.prev[message]
	if !ok {
		value = read()
	}
	if m.cur == nil || len(m.cur) >= mobileMessageMemoSize {
		m.prev, m.cur = m.cur, map[*domain.ConversationMessage]V{}
	}
	m.cur[message] = value
	return value
}

// Tool calls per stored message.
var toolCalls mobileMessageMemo[*contracts.MobileToolCall]

// A tool call read once per stored message: its input is parsed here rather than on every change to the view.
func toolCallOf(engine domain.AgentEngine, message *domain.ConversationMessage) *contracts.MobileToolCall {
	return toolCalls.of(message, func() *contracts.MobileToolCall {
		described := domain.DescribeToolCall(string(engine), message.Text, message.Detail)
		call := &contracts.MobileToolCall{Family: described.Family, Label: described.Label, Argument: described.Argument}
		if described.Sigil != nil && *described.Sigil != "" {
			call.Sigil = described.Sigil
		}
		if described.Verb != nil && *described.Verb {
			call.Verb = new(true)
		}
		return call
	})
}

// Whether a stored message may link images.
var imageLinking mobileMessageMemo[bool]

func linksImages(message *domain.ConversationMessage) bool {
	return imageLinking.of(message, func() bool { return domain.MayLinkImages(message.Text) })
}

// The transcript with what a phone reads beside each message's words: what each tool call did, and
// how many images a message carried, and the ID of a reply that may link images. The transcript is
// the thread's messages from index `start`, in order.
func withDetails(thread *domain.Thread, messages []*contracts.MobileMessage, start int) []*contracts.MobileMessage {
	detailed := make([]*contracts.MobileMessage, len(messages))
	for index, message := range messages {
		detailed[index] = message
		at := start + index
		if at < 0 || at >= len(thread.Messages) {
			continue
		}
		stored := thread.Messages[at]
		switch {
		case message.Kind == domain.ConversationMessageKindTool && stored.Kind == domain.ConversationMessageKindTool:
			copied := *message
			copied.Call = toolCallOf(thread.Engine, stored)
			detailed[index] = &copied
		case stored.Kind == domain.ConversationMessageKindAssistant && linksImages(stored):
			copied := *message
			copied.ID = &stored.ID
			detailed[index] = &copied
		case len(stored.Attachments) > 0:
			copied := *message
			copied.Images = new(contracts.Count(len(stored.Attachments)))
			detailed[index] = &copied
		}
	}
	return detailed
}

// A thread on this computer as its own screen reads it, without opening it. Nil for one this computer does not hold.
func MobileThreadRead(state *WorkspaceState, taskID string) *contracts.MobileThreadRead {
	thread := threadNamed(state.Threads, taskID)
	if thread == nil {
		return nil
	}
	transcript := ThreadTranscript(state, thread.ID, MobileTranscriptMessages, MobileMessageTextLimit, nil)
	if transcript == nil {
		return nil
	}
	activity := ThreadActivityThreadActivity(state)
	status := contracts.MobileRunStatusIdle
	switch {
	case activity.Blocked.Has(thread.ID):
		status = contracts.MobileRunStatusAwaitingApproval
	case activity.Working.Has(thread.ID):
		status = contracts.MobileRunStatusRunning
	}
	// A conversation not loaded yet counts as left out, as on a paired computer that has yet to open it.
	omitted := transcript.Omitted
	if thread.HistorySummary != nil {
		omitted = max(omitted, thread.HistorySummary.MessageCount-float64(len(transcript.Messages)))
	}
	read := &contracts.MobileThreadRead{Title: mobileTitle(thread.Title), Status: status, Messages: withDetails(thread, mobileMessages(transcript.Messages), int(transcript.Omitted)), Omitted: omitted}
	if tail := state.StreamingTails[thread.ID]; tail != nil {
		read.StreamingTail = &tail.Text
	}
	return read
}

// The page of a thread's messages before index `before`, which the phone shows above its live window.
func MobileThreadHistory(state *WorkspaceState, taskID string, before int) *contracts.MobileThreadHistory {
	thread := threadNamed(state.Threads, taskID)
	if thread == nil {
		return nil
	}
	transcript := ThreadTranscript(state, thread.ID, MobileHistoryPage, MobileMessageTextLimit, &before)
	if transcript == nil {
		return nil
	}
	return &contracts.MobileThreadHistory{Start: transcript.Omitted, Messages: withDetails(thread, mobileMessages(transcript.Messages), int(transcript.Omitted))}
}

// The threads working under a coordinator, here and on paired computers, as its desktop roster lists them.
func projectMembers(state *WorkspaceState, leadID string, view *OwnWorkspaceView) []*contracts.MobileMember {
	coordinated := CoordinationMembers(state, leadID)
	members := make([]*contracts.MobileMember, len(coordinated))
	for index, member := range coordinated {
		working, blocked := MemberActivity(member, view.RunningThreadIDs, view.BlockedThreadIDs)
		status := contracts.MobileRunStatusIdle
		switch {
		case blocked.Has(member.Thread.ID):
			status = contracts.MobileRunStatusAwaitingApproval
		case working.Has(member.Thread.ID):
			status = contracts.MobileRunStatusRunning
		}
		shown := &contracts.MobileMember{ID: member.Thread.ID, Title: mobileTitle(member.Thread.Title), Status: status}
		if member.Computer != nil {
			shown.Computer, shown.Online = &member.Computer.Name, new(member.Computer.Status == domain.ComputerStatusConnected)
		}
		members[index] = shown
	}
	return members
}

// The input as JSON.stringify(input, null, 2) writes it, keys in sorted order, cut to length.
func approvalDetail(input map[string]jsontext.Value) string {
	data, err := json.Marshal(input, jsonx.Options(), jsontext.WithIndent("  "))
	if err != nil {
		return ""
	}
	return ClipText(string(data), MobileApprovalDetailLimit)
}

// Nil when nothing moved, so a server with nothing to say sends nothing at all.
func DiffMobileView(previous, next *contracts.MobileView) *contracts.MobilePatch {
	patch := &contracts.MobilePatch{}
	moved := false
	if !sameGroups(previous.Groups, next.Groups) {
		patch.Groups, moved = next.Groups, true
	}
	if !sameActivity(&previous.Activity, &next.Activity) {
		patch.Activity, moved = &next.Activity, true
	}
	if !same(previous.Theme, next.Theme) {
		patch.Theme, moved = &next.Theme, true
	}
	if !jsonx.Same(previous.Engines, next.Engines) && !same(previous.Engines, next.Engines) {
		patch.Engines, moved = &next.Engines, true
	}
	if !same(previous.Upkeep, next.Upkeep) {
		patch.Upkeep, moved = mobileList(next.Upkeep), true
	}
	if previous.Dictation != next.Dictation {
		patch.Dictation, moved = new(next.Dictation), true
	}
	if !same(previous.Usage, next.Usage) {
		patch.Usage, moved = &next.Usage, true
	}
	if !mobileSameCount(previous.Archived, next.Archived) {
		patch.Archived, moved = next.Archived, true
	}
	if thread := diffMobileThread(previous.Thread, next.Thread); thread != nil {
		patch.Thread, moved = thread, true
	}
	if !same(previous.Draft, next.Draft) {
		patch.Draft, moved = mobileOpt(next.Draft), true
	}
	if !sameOptionalText(previous.Error, next.Error) {
		patch.Error, moved = mobileOpt(next.Error), true
	}
	if !moved {
		return nil
	}
	return patch
}

func mobileList[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

func mobileSameCount(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// A nullable value as a present field: null for nil.
func mobileOpt[T any](value *T) jsonx.Opt[T] {
	if value == nil {
		return jsonx.Null[T]()
	}
	return jsonx.Some(*value)
}

// How far a row's last activity may drift before the list is worth sending again. The list never
// draws a time finer than a minute, and a streaming run moves its row with every block it commits,
// which would otherwise resend the heaviest part of the view many times a minute. Whoever diffs
// keeps the view the phone holds rather than the newest one, so the drift never passes this.
const activityDriftMs = 60_000

func sameGroups(previous, next []*contracts.MobileProjectGroup) bool {
	if len(previous) != len(next) {
		return false
	}
	for index, group := range previous {
		other := next[index]
		if !sameOptionalText(group.ProjectID, other.ProjectID) || group.Name != other.Name || !sameRows(group.Threads, other.Threads) {
			return false
		}
	}
	return true
}

func sameActivity(previous, next *contracts.MobileActivity) bool {
	return sameRows(previous.Priority, next.Priority) && sameRows(previous.Running, next.Running) && sameRows(previous.Threads, next.Threads) &&
		sameRows(previous.Snoozed, next.Snoozed)
}

// Rows in the same order that differ only in when they last moved, and by less than the drift, are the same rows.
func sameRows(previous, next []*contracts.MobileThreadEntry) bool {
	if len(previous) != len(next) {
		return false
	}
	for index, before := range previous {
		after := next[index]
		if math.Abs(before.LastActivityAt-after.LastActivityAt) >= activityDriftMs {
			return false
		}
		left, right := *before, *after
		left.LastActivityAt, right.LastActivityAt = 0, 0
		if !same(left, right) {
			return false
		}
	}
	return true
}

// TypeScript's `a !== b && !same(a, b)`: whether a field of the open thread moved.
func mobileMoved(a, b any) bool {
	return !jsonx.Same(a, b) && !same(a, b)
}

// Nil when the open thread did not move.
func diffMobileThread(previous, next *contracts.MobileThreadView) contracts.MobileThreadPatch {
	if next == nil {
		if previous != nil {
			return contracts.MobileThreadPatchClosed{}
		}
		return nil
	}
	if previous == nil || previous.ID != next.ID {
		return contracts.MobileThreadPatchOpened{Thread: *next}
	}
	delta := contracts.MobileThreadDelta{}
	moved := false
	mark := func(changed bool, write func()) {
		if changed {
			write()
			moved = true
		}
	}
	mark(!mobileSameFlag(previous.Loading, next.Loading), func() { delta.Loading = next.Loading })
	mark(previous.Title != next.Title, func() { delta.Title = &next.Title })
	mark(!sameOptionalText(previous.ProjectID, next.ProjectID), func() { delta.ProjectID = mobileOpt(next.ProjectID) })
	mark(!sameOptionalText(previous.ProjectName, next.ProjectName), func() { delta.ProjectName = mobileOpt(next.ProjectName) })
	mark(!sameOptionalText(previous.WorktreeID, next.WorktreeID), func() { delta.WorktreeID = mobileOpt(next.WorktreeID) })
	mark(previous.Omitted != next.Omitted, func() { delta.Omitted = &next.Omitted })
	mark(previous.Status != next.Status, func() { delta.Status = &next.Status })
	mark(mobileMoved(previous.Question, next.Question), func() { delta.Question = next.Question })
	mark(mobileMoved(previous.Approval, next.Approval), func() { delta.Approval = mobileOpt(next.Approval) })
	mark(mobileMoved(previous.Queued, next.Queued), func() { delta.Queued = mobileList(next.Queued) })
	mark(mobileMoved(previous.Settings, next.Settings), func() { delta.Settings = &next.Settings })
	mark(mobileMoved(previous.Location, next.Location), func() { delta.Location = next.Location })
	mark(mobileMoved(previous.Worktrees, next.Worktrees), func() { delta.Worktrees = mobileList(next.Worktrees) })
	mark(previous.CanMove != next.CanMove, func() { delta.CanMove = &next.CanMove })
	mark(mobileMoved(previous.Changes, next.Changes), func() { delta.Changes = mobileOpt(next.Changes) })
	mark(previous.Reviewable != next.Reviewable, func() { delta.Reviewable = &next.Reviewable })
	mark(mobileMoved(previous.BranchRange, next.BranchRange), func() { delta.BranchRange = &next.BranchRange })
	mark(mobileMoved(previous.Members, next.Members), func() { delta.Members = next.Members })
	mark(mobileMoved(previous.Role, next.Role), func() { delta.Role = mobileDeltaRole(next.Role) })
	mark(mobileMoved(previous.Background, next.Background), func() { delta.Background = next.Background })
	mark(mobileMoved(previous.Decisions, next.Decisions), func() { delta.Decisions = next.Decisions })
	mark(mobileMoved(previous.Suggestions, next.Suggestions), func() { delta.Suggestions = next.Suggestions })
	mark(mobileMoved(previous.Limit, next.Limit), func() { delta.Limit = next.Limit })
	mark(!sameOptionalText(previous.Computer, next.Computer), func() { delta.Computer = next.Computer })
	mark(!mobileSameFlag(previous.Stoppable, next.Stoppable), func() { delta.Stoppable = next.Stoppable })
	if tail := previous.StreamingTail; !sameOptionalText(tail, next.StreamingTail) {
		moved = true
		if tail != nil && next.StreamingTail != nil && strings.HasPrefix(*next.StreamingTail, *tail) {
			delta.Tail = &contracts.MobileThreadDeltaTail{From: contracts.Count(mobileUTF16Len(*tail)), Text: (*next.StreamingTail)[len(*tail):]}
		} else {
			delta.StreamingTail = mobileOpt(next.StreamingTail)
		}
	}
	spliced, messages := transcriptDelta(previous.Messages, next.Messages)
	if spliced != nil || messages != nil {
		delta.Spliced, delta.Messages, moved = spliced, messages, true
	}
	if !moved {
		return nil
	}
	return contracts.MobileThreadPatchChanged{ID: next.ID, Delta: delta}
}

// JavaScript's length of a string.
func mobileUTF16Len(text string) int {
	units := 0
	for _, r := range text {
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
	}
	return units
}

func mobileSameFlag(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// A thread's role as a delta carries it.
func mobileDeltaRole(role jsonx.Opt[domain.ThreadRole]) jsonx.Opt[contracts.MobileThreadDeltaRole] {
	if value, ok := role.Get(); ok {
		return jsonx.Some(contracts.MobileThreadDeltaRole(value))
	}
	if role.IsNull() {
		return jsonx.Null[contracts.MobileThreadDeltaRole]()
	}
	return jsonx.Opt[contracts.MobileThreadDeltaRole]{}
}

// Every field of the open thread a delta carries as it stands, which is all of them but its id, its
// transcript and its streaming tail: those two grow at their end, and travel as what they grew by.
var movingKeys = []string{
	"loading", "title", "projectId", "projectName", "worktreeId", "omitted", "status", "question", "approval",
	"queued", "settings", "location", "worktrees", "canMove", "changes", "reviewable", "branchRange", "members", "role", "background", "decisions", "suggestions", "limit", "computer", "stoppable",
}

// How the transcript moved: not at all (both nil), at its end (spliced), or some other way (the
// whole transcript in messages). At its end means the window may have slid past older messages, and
// the newest message the phone holds may have grown, while everything between stayed as it was.
func transcriptDelta(previous, next []*contracts.MobileMessage) (spliced *contracts.MobileThreadDeltaSpliced, messages []*contracts.MobileMessage) {
	encode := func(list []*contracts.MobileMessage) []string {
		encoded := make([]string, len(list))
		for index, message := range list {
			data, _ := jsonx.Marshal(message)
			encoded[index] = string(data)
		}
		return encoded
	}
	held, wanted := encode(previous), encode(next)
	if len(held) == 0 {
		if len(wanted) == 0 {
			return nil, nil
		}
		return &contracts.MobileThreadDeltaSpliced{Dropped: 0, Kept: 0, Messages: next}, nil
	}
	dropped := -1
	if len(wanted) > 0 {
		dropped = slices.Index(held, wanted[0])
	}
	if dropped < 0 {
		return nil, mobileList(next)
	}
	kept := 0
	for dropped+kept < len(held) && kept < len(wanted) && held[dropped+kept] == wanted[kept] {
		kept++
	}
	rest := len(held) - dropped
	if kept < rest-1 {
		return nil, mobileList(next)
	}
	if dropped == 0 && kept == rest && kept == len(wanted) {
		return nil, nil
	}
	return &contracts.MobileThreadDeltaSpliced{Dropped: contracts.Count(dropped), Kept: contracts.Count(kept), Messages: mobileList(next[kept:])}, nil
}

// Puts a patch back on the view the phone holds, which is the other half of DiffMobileView.
// Nil when it does not fit: a transcript or tail that grew from something other than what is held
// would be drawn wrong in silence, so whoever holds the view fetches it whole instead.
func ApplyMobilePatch(view *contracts.MobileView, patch *contracts.MobilePatch) *contracts.MobileView {
	next := *view
	if patch.Groups != nil {
		next.Groups = patch.Groups
	}
	if patch.Activity != nil {
		next.Activity = *patch.Activity
	}
	if patch.Theme != nil {
		next.Theme = *patch.Theme
	}
	if patch.Engines != nil {
		next.Engines = *patch.Engines
	}
	if patch.Upkeep != nil {
		next.Upkeep = patch.Upkeep
	}
	if patch.Dictation != nil {
		next.Dictation = *patch.Dictation
	}
	if patch.Usage != nil {
		next.Usage = *patch.Usage
	}
	if patch.Archived != nil {
		next.Archived = patch.Archived
	}
	if !patch.Draft.IsAbsent() {
		next.Draft = nil
		if draft, ok := patch.Draft.Get(); ok {
			next.Draft = &draft
		}
	}
	if !patch.Error.IsAbsent() {
		next.Error = nil
		if message, ok := patch.Error.Get(); ok {
			next.Error = &message
		}
	}
	switch thread := patch.Thread.(type) {
	case nil:
		return &next
	case contracts.MobileThreadPatchClosed:
		next.Thread = nil
		return &next
	case contracts.MobileThreadPatchOpened:
		next.Thread = &thread.Thread
		return &next
	case contracts.MobileThreadPatchChanged:
		current := view.Thread
		if current == nil || current.ID != thread.ID {
			return &next
		}
		moved := applyThreadDelta(current, &thread.Delta)
		if moved == nil {
			return nil
		}
		next.Thread = moved
	}
	return &next
}

// The open thread with a delta's fields in place, nil when its transcript or tail does not fit.
func applyThreadDelta(current *contracts.MobileThreadView, delta *contracts.MobileThreadDelta) *contracts.MobileThreadView {
	thread := *current
	if delta.Loading != nil {
		thread.Loading = delta.Loading
	}
	if delta.Title != nil {
		thread.Title = *delta.Title
	}
	mobileSetOpt(&thread.ProjectID, delta.ProjectID)
	mobileSetOpt(&thread.ProjectName, delta.ProjectName)
	mobileSetOpt(&thread.WorktreeID, delta.WorktreeID)
	if delta.Omitted != nil {
		thread.Omitted = *delta.Omitted
	}
	if delta.Status != nil {
		thread.Status = *delta.Status
	}
	if !delta.Question.IsAbsent() {
		thread.Question = delta.Question
	}
	mobileSetOpt(&thread.Approval, delta.Approval)
	if delta.Queued != nil {
		thread.Queued = delta.Queued
	}
	if delta.Settings != nil {
		thread.Settings = *delta.Settings
	}
	if delta.Location != nil {
		thread.Location = delta.Location
	}
	if delta.Worktrees != nil {
		thread.Worktrees = delta.Worktrees
	}
	if delta.CanMove != nil {
		thread.CanMove = *delta.CanMove
	}
	mobileSetOpt(&thread.Changes, delta.Changes)
	if delta.Reviewable != nil {
		thread.Reviewable = *delta.Reviewable
	}
	if delta.BranchRange != nil {
		thread.BranchRange = *delta.BranchRange
	}
	if delta.Members != nil {
		thread.Members = delta.Members
	}
	if value, ok := delta.Role.Get(); ok {
		thread.Role = jsonx.Some(domain.ThreadRole(value))
	} else if delta.Role.IsNull() {
		thread.Role = jsonx.Null[domain.ThreadRole]()
	}
	if delta.Background != nil {
		thread.Background = delta.Background
	}
	if delta.Decisions != nil {
		thread.Decisions = delta.Decisions
	}
	if delta.Suggestions != nil {
		thread.Suggestions = delta.Suggestions
	}
	if !delta.Limit.IsAbsent() {
		thread.Limit = delta.Limit
	}
	if delta.Computer != nil {
		thread.Computer = delta.Computer
	}
	if delta.Stoppable != nil {
		thread.Stoppable = delta.Stoppable
	}
	transcript := current.Messages
	if delta.Messages != nil {
		transcript = delta.Messages
	}
	if spliced := delta.Spliced; spliced != nil {
		dropped, kept := int(spliced.Dropped), int(spliced.Kept)
		if len(current.Messages) < dropped+kept {
			return nil
		}
		transcript = slices.Concat(current.Messages[dropped:dropped+kept], spliced.Messages)
	}
	thread.Messages = transcript
	if !delta.StreamingTail.IsAbsent() {
		thread.StreamingTail = nil
		if tail, ok := delta.StreamingTail.Get(); ok {
			thread.StreamingTail = &tail
		}
	} else {
		thread.StreamingTail = current.StreamingTail
	}
	if tail := delta.Tail; tail != nil {
		if current.StreamingTail == nil || mobileUTF16Len(*current.StreamingTail) != int(tail.From) {
			return nil
		}
		thread.StreamingTail = new(*current.StreamingTail + tail.Text)
	}
	return &thread
}

// Writes a present Opt into a nullable field: nil for null, the value otherwise.
func mobileSetOpt[T any](field **T, value jsonx.Opt[T]) {
	if value.IsAbsent() {
		return
	}
	*field = nil
	if held, ok := value.Get(); ok {
		*field = &held
	}
}

func same(left, right any) bool {
	a, errA := jsonx.Marshal(left)
	b, errB := jsonx.Marshal(right)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}
