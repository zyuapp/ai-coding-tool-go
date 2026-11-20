package domain

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// DeliveryOption is one way a thread's work lands, and its name.
type DeliveryOption struct {
	ID    Delivery
	Label string
}

var Deliveries = []DeliveryOption{
	{ID: DeliveryPullRequest, Label: "Pull request"},
	{ID: DeliveryCommit, Label: "Commits"},
	{ID: DeliveryReport, Label: "Report"},
}

var CoordinationStates = []CoordinationState{CoordinationStateWorking, CoordinationStateBlocked, CoordinationStateDone, CoordinationStateFailed}

// Limits count UTF-16 code units, as JavaScript's length does.
const (
	MaxBriefField        = 4_000
	MaxSummary           = 500
	MaxQuestion          = 300
	MaxDecisionContext   = 2_000
	MaxOptionLabel       = 120
	MaxOptionDescription = 300
	MaxOptions           = 5
	MaxAnswer            = 4_000
	maxDecisions         = 20
	MaxOpenDecisions     = 10
	maxNotes             = 50
	MaxPurpose           = 200
	MaxMemoryNote        = 500
	MaxMemoryNotes       = 30
	MaxMemoryNoteID      = 200
	MaxProposals         = 12
	MaxProposalTitle     = 120
	MaxProposalProject   = 4_096
	MaxProposalComputer  = 256
	maxRemoteHeard       = 100
	mostRemoteHeard      = 10 * maxRemoteHeard
	MaxMemoryPrompt      = 64_000
)

var OverviewGroups = []OverviewGroup{OverviewGroupNeeds, OverviewGroupReview, OverviewGroupWorking, OverviewGroupDone, OverviewGroupResolved}

var overviewSections = []OverviewSection{OverviewSectionNeeds, OverviewSectionReview, OverviewSectionWorking, OverviewSectionDone, OverviewSectionResolved, OverviewSectionMemory, OverviewSectionUsage}

// IsOverviewSection accepts an OverviewSection or a string.
func IsOverviewSection(value any) bool {
	switch v := value.(type) {
	case OverviewSection:
		return slices.Contains(overviewSections, v)
	case string:
		return slices.Contains(overviewSections, OverviewSection(v))
	}
	return false
}

// NextStepSpec is one next step: its label, the name a button for it is read as, and what the thread is told.
type NextStepSpec struct {
	ID    NextStep
	Label string
	Name  func(title string) string
	Text  string
}

var NextSteps = []NextStepSpec{
	{ID: NextStepResolveConflicts, Label: "Resolve conflicts", Name: func(title string) string { return "Resolve conflicts in " + title + "'s pull request" }, Text: "The pull request has merge conflicts with its base branch. Resolve them and push."},
	{ID: NextStepFixCi, Label: "Fix CI", Name: func(title string) string { return "Fix CI for " + title }, Text: "The pull request's checks are failing. Find out why, fix them, and push."},
	{ID: NextStepAddressComments, Label: "Address comments", Name: func(title string) string { return "Address comments on " + title + "'s pull request" }, Text: "The pull request has review comments asking for changes. Address each one, push, and reply on the pull request."},
	{ID: NextStepMerge, Label: "Merge it", Name: func(title string) string { return "Merge it: " + title + "'s pull request" }, Text: "Merge the pull request."},
	{ID: NextStepCreatePullRequest, Label: "Create PR", Name: func(title string) string { return "Create PR for " + title }, Text: "Open a pull request for this work."},
}

// IsNextStep accepts a NextStep or a string.
func IsNextStep(value any) bool {
	var id NextStep
	switch v := value.(type) {
	case NextStep:
		id = v
	case string:
		id = NextStep(v)
	default:
		return false
	}
	return slices.ContainsFunc(NextSteps, func(step NextStepSpec) bool { return step.ID == id })
}

// NextStepOf is the step with that id; it panics for an id that is none.
func NextStepOf(id NextStep) *NextStepSpec {
	return &NextSteps[slices.IndexFunc(NextSteps, func(step NextStepSpec) bool { return step.ID == id })]
}

// EndsAsStopped takes an empty ending as none.
func EndsAsStopped(ending ThreadOutcome) bool {
	return ending != ThreadOutcomeFailed && ending != ThreadOutcomeFinished
}

// IsCoordinator is false for a nil thread.
func IsCoordinator(thread *Thread) bool {
	return thread != nil && thread.Role != nil && *thread.Role == ThreadRoleCoordinator && thread.ArchivedAt == nil
}

// EffortRoleOf takes a nil thread as none and an empty draftRole as null; it returns "" for none.
func EffortRoleOf(thread *Thread, draftRole ThreadRole) CoordinationRole {
	coordinating := draftRole == ThreadRoleCoordinator
	if thread != nil {
		coordinating = IsCoordinator(thread)
	}
	if coordinating {
		return CoordinationRoleCoordinator
	}
	return ""
}

func MayLeadElsewhere(thread *Thread) bool {
	return thread.Role != nil && *thread.Role == ThreadRoleCoordinator || len(thread.RemoteHeard) > 0
}

// ThreadModelOf is what a coordinator's threads run on; nil while nothing names a model.
func ThreadModelOf(catalog *ModelCatalog, lead *Thread) *ThreadModelChoice {
	saved := lead.ThreadModel
	var choice *engineChoice
	if saved != nil {
		engine := AgentEngine("")
		if saved.Engine != nil {
			engine = *saved.Engine
		} else if found, ok := ModelEngine(catalog, saved.Model, lead.Engine); ok {
			engine = found
		}
		choice = &engineChoice{engine: engine, model: saved.Model}
	} else if own, ok := ThreadModel(catalog, lead); ok {
		choice = &engineChoice{engine: lead.Engine, model: own}
	} else if fallback, ok := DefaultModelIn(catalog, lead.Engine); ok {
		choice = &engineChoice{engine: lead.Engine, model: fallback}
	}
	if choice == nil {
		return nil
	}
	if choice.engine == "" || !EngineHasModel(catalog, choice.engine, choice.model) {
		kept := &ThreadModelChoice{Model: choice.model}
		if choice.engine != "" {
			kept.Engine = new(choice.engine)
		}
		if saved != nil && saved.Model == choice.model && saved.Effort != nil && *saved.Effort != "" {
			kept.Effort = saved.Effort
		}
		return kept
	}
	engine, model := choice.engine, choice.model
	if !ModelTakesEffort(catalog, engine, model) {
		return &ThreadModelChoice{Engine: new(engine), Model: model}
	}
	effort := DefaultEffortFor(string(engine))
	if saved != nil && saved.Effort != nil {
		effort = *saved.Effort
	} else if lead.Effort != nil {
		effort = *lead.Effort
	}
	return &ThreadModelChoice{Engine: new(engine), Model: model, Effort: new(EffortForModel(catalog, engine, model, effort, ""))}
}

// engineChoice is TypeScript's EngineChoice.
type engineChoice struct {
	engine AgentEngine
	model  AgentModel
}

func AsCoordinator(catalog *ModelCatalog, thread *Thread) *Thread {
	choice := thread.ThreadModel
	if choice == nil {
		choice = ThreadModelOf(catalog, thread)
	}
	next := *thread
	next.Role = new(ThreadRoleCoordinator)
	if choice != nil {
		next.ThreadModel = choice
	}
	return &next
}

func isCoordinatorRole(thread *Thread) bool {
	return thread.Role != nil && *thread.Role == ThreadRoleCoordinator
}

// withProposal is the coordinator with one suggestion swapped for its changed copy.
func withProposal(lead *Thread, proposal, changed *ThreadProposal) *Thread {
	proposals := slices.Clone(lead.Proposals)
	for i, item := range proposals {
		if item == proposal {
			proposals[i] = changed
		}
	}
	next := *lead
	next.Proposals = proposals
	return &next
}

func coordinator(thread *Thread) *Thread {
	if IsCoordinator(thread) {
		return thread
	}
	return nil
}

// CoordinatorOf is the coordinator a thread works under; nil when none. A nil thread is none.
func CoordinatorOf(threads []*Thread, thread *Thread) *Thread {
	if thread == nil || thread.ParentID == nil || *thread.ParentID == "" || thread.Role != nil && *thread.Role == ThreadRoleCoordinator {
		return nil
	}
	for _, item := range threads {
		if item.ID == *thread.ParentID {
			return coordinator(item)
		}
	}
	return nil
}

// LeadIDOf is the id of the coordinator a thread names as the one it works under.
func LeadIDOf(thread *Thread) (string, bool) {
	if thread.ParentID != nil {
		return *thread.ParentID, true
	}
	if thread.RemoteLead == nil || thread.RemoteLead.Away != nil && *thread.RemoteLead.Away {
		return "", false
	}
	return thread.RemoteLead.ThreadID, true
}

// RemoteLeadOf is nil unless the thread works under a coordinator on another computer. A nil thread is none.
func RemoteLeadOf(thread *Thread) *HeldLead {
	if thread == nil || thread.RemoteLead == nil || thread.RemoteLead.Away != nil && *thread.RemoteLead.Away || thread.ParentID != nil || thread.Role != nil && *thread.Role == ThreadRoleCoordinator {
		return nil
	}
	return thread.RemoteLead
}

// AwayLeadOf is nil unless the thread's coordinator on another computer is away. A nil thread is none.
func AwayLeadOf(thread *Thread) *HeldLead {
	if thread == nil || thread.RemoteLead == nil || thread.RemoteLead.Away == nil || !*thread.RemoteLead.Away || thread.ParentID != nil || thread.Role != nil && *thread.Role == ThreadRoleCoordinator {
		return nil
	}
	return thread.RemoteLead
}

func WorksUnderCoordinator(threads []*Thread, thread *Thread) bool {
	return CoordinatorOf(threads, thread) != nil || RemoteLeadOf(thread) != nil
}

func CoordinatedThreads(threads []*Thread, leadID string) []*Thread {
	var lead *Thread
	for _, thread := range threads {
		if thread.ID == leadID {
			lead = thread
			break
		}
	}
	if !IsCoordinator(lead) {
		return []*Thread{}
	}
	members := []*Thread{}
	for _, thread := range threads {
		if id, ok := LeadIDOf(thread); ok && id == leadID && thread.ArchivedAt == nil && !isCoordinatorRole(thread) {
			members = append(members, thread)
		}
	}
	return members
}

// ThreadsByCoordinator is the threads working under each coordinator, by its id, in list order.
func ThreadsByCoordinator(threads []*Thread) map[string][]*Thread {
	members := CoordinatedThreadIds(threads)
	byLead := map[string][]*Thread{}
	for _, thread := range threads {
		if !members.Has(thread.ID) {
			continue
		}
		leadID, _ := LeadIDOf(thread)
		byLead[leadID] = append(byLead[leadID], thread)
	}
	return byLead
}

func CoordinatedThreadIds(threads []*Thread) jsonx.Set[string] {
	leads := jsonx.Set[string]{}
	for _, thread := range threads {
		if IsCoordinator(thread) {
			leads[thread.ID] = struct{}{}
		}
	}
	members := jsonx.Set[string]{}
	for _, thread := range threads {
		if leadID, ok := LeadIDOf(thread); ok && leadID != "" && leads.Has(leadID) && !isCoordinatorRole(thread) {
			members[thread.ID] = struct{}{}
		}
	}
	return members
}

// WithCoordinated returns ids itself when it names no coordinator.
func WithCoordinated(threads []*Thread, ids jsonx.Set[string]) jsonx.Set[string] {
	leads := jsonx.Set[string]{}
	for _, thread := range threads {
		if ids.Has(thread.ID) && IsCoordinator(thread) {
			leads[thread.ID] = struct{}{}
		}
	}
	if len(leads) == 0 {
		return ids
	}
	all := maps.Clone(ids)
	if all == nil {
		all = jsonx.Set[string]{}
	}
	for _, thread := range threads {
		if leadID, ok := LeadIDOf(thread); ok && leadID != "" && leads.Has(leadID) && !isCoordinatorRole(thread) {
			all[thread.ID] = struct{}{}
		}
	}
	return all
}

// CanJoinCoordinator takes a nil lead as none.
func CanJoinCoordinator(thread *Thread, lead *Thread) bool {
	return lead != nil && IsCoordinator(lead) && lead.ID != thread.ID && (thread.Role == nil || *thread.Role != ThreadRoleCoordinator) && thread.ArchivedAt == nil
}

func OpenDecisions(thread *Thread) []*Decision {
	open := []*Decision{}
	for _, decision := range thread.Decisions {
		if decision.Answer == nil {
			open = append(open, decision)
		}
	}
	return open
}

func DecisionsFull(thread *Thread) bool {
	return len(OpenDecisions(thread)) >= MaxOpenDecisions
}

// CoordinationWaitingOnUser is what waits on the user from a thread and the threads under it.
func CoordinationWaitingOnUser(thread *Thread, members []*Thread, starting []string) WaitingOnUser {
	var proposals []*ThreadProposal
	for _, proposal := range thread.Proposals {
		if !slices.Contains(starting, proposal.ID) {
			proposals = append(proposals, proposal)
		}
	}
	waiting := WaitingOnUser{Requests: []string{}}
	waiting.Decisions = float64(len(OpenDecisions(thread)))
	for _, member := range members {
		waiting.Decisions += float64(len(OpenDecisions(member)))
		if member.ResolvedAt != nil || member.RunOpen != nil && *member.RunOpen || !WantsAttention(member) || len(OpenDecisions(member)) > 0 {
			continue
		}
		if verdict := CoordinationStuckVerdict(member); verdict != nil {
			switch verdict.State {
			case StuckVerdictStateBlocked:
				waiting.Blocked++
			case StuckVerdictStateFailed:
				waiting.Failed++
			}
		}
	}
	for _, proposal := range proposals {
		if proposal.Place.Computer != nil {
			waiting.Requests = append(waiting.Requests, proposal.Place.Computer.Name)
		} else {
			waiting.Suggestions++
		}
	}
	return waiting
}

func IsWaitingOnUser(waiting WaitingOnUser) bool {
	return waiting.Decisions+waiting.Suggestions+float64(len(waiting.Requests))+waiting.Blocked+waiting.Failed > 0
}

// ThreadDecision is one decision and the thread holding it.
type ThreadDecision struct {
	Thread   *Thread
	Decision *Decision
}

// CoordinationDecisions is every decision waiting on the user from a coordinator and its threads, oldest first.
func CoordinationDecisions(lead *Thread, members []*Thread) []ThreadDecision {
	if !IsCoordinator(lead) {
		return []ThreadDecision{}
	}
	decisions := []ThreadDecision{}
	for _, thread := range append([]*Thread{lead}, members...) {
		for _, decision := range OpenDecisions(thread) {
			decisions = append(decisions, ThreadDecision{Thread: thread, Decision: decision})
		}
	}
	slices.SortStableFunc(decisions, func(left, right ThreadDecision) int {
		return cmp.Compare(left.Decision.RaisedAt, right.Decision.RaisedAt)
	})
	return decisions
}

// CoordinationStuckVerdict is nil unless the thread's latest turn left it stuck.
func CoordinationStuckVerdict(thread *Thread) *StuckVerdict {
	if report := thread.Report; report != nil && report.State != CoordinationStateWorking {
		if report.State == CoordinationStateBlocked || report.State == CoordinationStateFailed {
			return &StuckVerdict{State: StuckVerdictState(report.State), Summary: new(report.Summary)}
		}
		return nil
	}
	if outcome, ok := LastRunOutcome(thread); ok && outcome == ThreadOutcomeFailed {
		return &StuckVerdict{State: StuckVerdictStateFailed}
	}
	return nil
}

func ReopenedIfStuck(thread *Thread) *Thread {
	if thread.ResolvedAt == nil || CoordinationStuckVerdict(thread) == nil {
		return thread
	}
	open := *thread
	open.ResolvedAt = nil
	return &open
}

func WithoutReport(thread *Thread) *Thread {
	if thread.Report == nil {
		return thread
	}
	next := *thread
	next.Report = nil
	return &next
}

func WithDecision(thread *Thread, decision *Decision) *Thread {
	kept := append(slices.Clip(thread.Decisions), decision)
	excess := max(0, len(kept)-maxDecisions)
	dropped := map[string]bool{}
	for _, item := range kept {
		if len(dropped) >= excess {
			break
		}
		if item.Answer != nil {
			dropped[item.ID] = true
		}
	}
	decisions := []*Decision{}
	for _, item := range kept {
		if !dropped[item.ID] {
			decisions = append(decisions, item)
		}
	}
	next := *thread
	next.Decisions = decisions
	return &next
}

func WithAnswer(thread *Thread, decisionID string, answer string, at float64) *Thread {
	decisions := make([]*Decision, len(thread.Decisions))
	for i, decision := range thread.Decisions {
		decisions[i] = decision
		if decision.ID == decisionID {
			answered := *decision
			answered.Answer, answered.AnsweredAt = new(answer), new(at)
			decisions[i] = &answered
		}
	}
	next := *thread
	next.Decisions = decisions
	return &next
}

func WithoutAnswer(thread *Thread, decisionID string) *Thread {
	if !slices.ContainsFunc(thread.Decisions, func(decision *Decision) bool { return decision.ID == decisionID && decision.Answer != nil }) {
		return thread
	}
	decisions := make([]*Decision, len(thread.Decisions))
	for i, decision := range thread.Decisions {
		decisions[i] = decision
		if decision.ID == decisionID {
			open := *decision
			open.Answer, open.AnsweredAt = nil, nil
			decisions[i] = &open
		}
	}
	next := *thread
	next.Decisions = decisions
	return &next
}

// WithMemoryChange makes one change to a coordinator's memory. It returns the thread (itself when nothing
// changed), the note the change named ("" for none), or an error whose text is the refusal the user reads.
func WithMemoryChange(thread *Thread, change MemoryChange, at float64, heard bool) (*Thread, string, error) {
	memory := thread.Memory
	if memory == nil {
		memory = &CoordinatorMemory{Notes: []*MemoryNote{}, Revision: 0, Heard: new(0.0)}
	}
	revision := memory.Revision + 1
	purpose, purposeCleared, notes := memory.Purpose, memory.PurposeCleared, memory.Notes
	noteID := ""
	switch change := change.(type) {
	case MemoryChangePurpose:
		asked := ""
		if change.Purpose != nil {
			asked = jsTrim(*change.Purpose)
		}
		if utf16Length(asked) > MaxPurpose {
			return nil, "", fmt.Errorf("A purpose can be at most %d characters.", MaxPurpose)
		}
		if purpose == nil && asked == "" || purpose != nil && asked == *purpose {
			return thread, "", nil
		}
		purpose, purposeCleared = nil, new(revision)
		if asked != "" {
			purpose, purposeCleared = new(asked), nil
		}
	case MemoryChangeRemember:
		text, err := noteText(change.Text)
		if err != nil {
			return nil, "", err
		}
		if len(notes) >= MaxMemoryNotes {
			return nil, "", fmt.Errorf("Memory holds at most %d notes. Forget one first.", MaxMemoryNotes)
		}
		noteID = "n" + jsNumber(revision)
		notes = append(slices.Clip(notes), &MemoryNote{ID: noteID, Text: text, At: at})
	case MemoryChangeEdit:
		index := slices.IndexFunc(notes, func(item *MemoryNote) bool { return item.ID == change.NoteID })
		if index < 0 {
			return nil, "", errors.New("No note has the ID " + change.NoteID + ".")
		}
		text, err := noteText(change.Text)
		if err != nil {
			return nil, "", err
		}
		if text == notes[index].Text {
			return thread, "", nil
		}
		noteID = notes[index].ID
		edited := *notes[index]
		edited.Text, edited.At = text, at
		notes = slices.Clone(notes)
		notes[index] = &edited
	case MemoryChangeForget:
		if !slices.ContainsFunc(notes, func(item *MemoryNote) bool { return item.ID == change.NoteID }) {
			return nil, "", errors.New("No note has the ID " + change.NoteID + ".")
		}
		noteID = change.NoteID
		kept := []*MemoryNote{}
		for _, item := range notes {
			if item.ID != change.NoteID {
				kept = append(kept, item)
			}
		}
		notes = kept
	default:
		return thread, "", nil
	}
	read := memory.Heard
	if heard && memory.Heard != nil && *memory.Heard == memory.Revision {
		read = new(revision)
	}
	if notes == nil {
		notes = []*MemoryNote{}
	}
	next := *thread
	next.Memory = &CoordinatorMemory{Purpose: purpose, PurposeCleared: purposeCleared, Notes: notes, Revision: revision, Heard: read}
	return &next, noteID, nil
}

// noteText is a note's trimmed text, or the refusal for it.
func noteText(value string) (string, error) {
	text := jsTrim(value)
	if text == "" {
		return "", errors.New("A note needs some text.")
	}
	if utf16Length(text) > MaxMemoryNote {
		return "", fmt.Errorf("A note can be at most %d characters.", MaxMemoryNote)
	}
	return text, nil
}

func WithMemoryHeard(lead *Thread) *Thread {
	memory := lead.Memory
	if memory == nil || memory.Heard != nil && *memory.Heard == memory.Revision {
		return lead
	}
	heard := *memory
	heard.Heard = new(memory.Revision)
	next := *lead
	next.Memory = &heard
	return &next
}

func WithMemoryUnheard(thread *Thread) *Thread {
	if thread.Memory == nil || thread.Memory.Heard == nil {
		return thread
	}
	memory := *thread.Memory
	memory.Heard = nil
	next := *thread
	next.Memory = &memory
	return &next
}

func WithProposals(lead *Thread, proposals []*ThreadProposal) *Thread {
	held := lead.Proposals
	added := proposals[:min(len(proposals), max(0, MaxProposals-len(held)))]
	if len(added) == 0 {
		return lead
	}
	next := *lead
	next.Proposals = append(slices.Clip(held), added...)
	return &next
}

func WithRemoteHeard(lead *Thread, threadID string, at float64, led jsonx.Set[string]) *Thread {
	if held, ok := lead.RemoteHeard[threadID]; ok && held == at {
		return lead
	}
	entries := maps.Clone(lead.RemoteHeard)
	if entries == nil {
		entries = map[string]float64{}
	}
	entries[threadID] = at
	next := *lead
	next.RemoteHeard = entries
	if len(entries) <= maxRemoteHeard {
		return &next
	}
	oldest := func(kept func(id string) bool) []string {
		ids := []string{}
		for id := range entries {
			if id != threadID && kept(id) {
				ids = append(ids, id)
			}
		}
		slices.SortFunc(ids, func(left, right string) int {
			return cmp.Or(cmp.Compare(entries[left], entries[right]), strings.Compare(left, right))
		})
		return ids
	}
	byAge := oldest(func(id string) bool { return !led.Has(id) })
	dropped := jsonx.SetOf(byAge[:min(len(byAge), len(entries)-maxRemoteHeard)]...)
	if left := len(entries) - len(dropped); left > mostRemoteHeard {
		rest := oldest(func(id string) bool { return !dropped.Has(id) })
		for _, id := range rest[:min(len(rest), left-mostRemoteHeard)] {
			dropped[id] = struct{}{}
		}
	}
	for id := range dropped {
		delete(entries, id)
	}
	return &next
}

func WithoutProposals(lead *Thread, ids jsonx.Set[string]) *Thread {
	rest := []*ThreadProposal{}
	for _, proposal := range lead.Proposals {
		if !ids.Has(proposal.ID) {
			rest = append(rest, proposal)
		}
	}
	if len(rest) == len(lead.Proposals) {
		return lead
	}
	next := *lead
	next.Proposals = nil
	if len(rest) > 0 {
		next.Proposals = rest
	}
	return &next
}

// WithProposalError sets why a suggestion's last start failed; a nil error clears it.
func WithProposalError(lead *Thread, id string, error *string) *Thread {
	index := slices.IndexFunc(lead.Proposals, func(item *ThreadProposal) bool { return item.ID == id })
	if index < 0 {
		return lead
	}
	proposal := lead.Proposals[index]
	if proposal.Error == nil && error == nil || proposal.Error != nil && error != nil && *proposal.Error == *error {
		return lead
	}
	marked := *proposal
	marked.Error = error
	return withProposal(lead, proposal, &marked)
}

func WithProposalCarried(lead *Thread, id string) *Thread {
	index := slices.IndexFunc(lead.Proposals, func(item *ThreadProposal) bool { return item.ID == id })
	if index < 0 || lead.Proposals[index].Carried != nil && *lead.Proposals[index].Carried {
		return lead
	}
	proposal := lead.Proposals[index]
	carried := *proposal
	carried.Carried = new(true)
	return withProposal(lead, proposal, &carried)
}

func WithCoordinationNote(lead *Thread, note *CoordinationNote) *Thread {
	notes := append(slices.Clone(lead.CoordinationNotes), note)
	for len(notes) > maxNotes {
		index := slices.Index(notes, leastNews(notes))
		notes = slices.Delete(notes, index, index+1)
	}
	next := *lead
	next.CoordinationNotes = notes
	return &next
}

func leastNews(notes []*CoordinationNote) *CoordinationNote {
	counts := map[string]int{}
	most := 0
	for _, note := range notes {
		counts[note.ThreadID]++
		most = max(most, counts[note.ThreadID])
	}
	var busiest string
	for _, note := range notes {
		if counts[note.ThreadID] == most {
			busiest = note.ThreadID
			break
		}
	}
	var first *CoordinationNote
	for _, note := range notes {
		if note.ThreadID != busiest {
			continue
		}
		if note.Urgent == nil || !*note.Urgent {
			return note
		}
		if first == nil {
			first = note
		}
	}
	return first
}

func WithoutCoordinationNotes(lead *Thread, heard jsonx.Set[string]) *Thread {
	rest := []*CoordinationNote{}
	for _, note := range lead.CoordinationNotes {
		if !heard.Has(note.ID) {
			rest = append(rest, note)
		}
	}
	next := *lead
	next.CoordinationNotes = nil
	if len(rest) > 0 {
		next.CoordinationNotes = rest
	}
	return &next
}

// DeliveryLabel is what a delivery is called, or its own name for one this build does not know.
func DeliveryLabel(delivers string) string {
	for _, delivery := range Deliveries {
		if string(delivery.ID) == delivers {
			return delivery.Label
		}
	}
	return delivers
}

// IsDelivery accepts a Delivery or a string.
func IsDelivery(value any) bool {
	var id Delivery
	switch v := value.(type) {
	case Delivery:
		id = v
	case string:
		id = Delivery(v)
	default:
		return false
	}
	return slices.ContainsFunc(Deliveries, func(delivery DeliveryOption) bool { return delivery.ID == id })
}

// IsCoordinationState accepts a CoordinationState or a string.
func IsCoordinationState(value any) bool {
	switch v := value.(type) {
	case CoordinationState:
		return slices.Contains(CoordinationStates, v)
	case string:
		return slices.Contains(CoordinationStates, CoordinationState(v))
	}
	return false
}

func coordinationIsRecord(value any) (map[string]any, bool) {
	return jsRecord(value)
}

// coordinationText is a non-blank string of at most max UTF-16 code units.
func coordinationText(value any, max int) bool {
	text, ok := value.(string)
	return ok && !jsBlank(text) && utf16Length(text) <= max
}

// utf16Length is JavaScript's length of a string.
func utf16Length(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	return units
}

func finite(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsInf(number, 0) && !math.IsNaN(number)
}

// The validators below check values decoded from JSON into any (map[string]any, []any, float64, string, bool, nil).

func IsThreadBrief(value any) bool {
	brief, ok := coordinationIsRecord(value)
	return ok && coordinationText(brief["intent"], MaxBriefField) && coordinationText(brief["doneWhen"], MaxBriefField) && IsDelivery(brief["delivers"])
}

func IsThreadModelChoice(value any) bool {
	choice, ok := coordinationIsRecord(value)
	return ok && coordAbsentOr(choice, "engine", IsAgentEngine) && IsModelID(choice["model"]) && coordAbsentOr(choice, "effort", IsAgentEffort)
}

func isMemoryNote(value any) bool {
	note, ok := coordinationIsRecord(value)
	return ok && coordinationText(note["id"], 200) && coordIsString(note["text"]) && finite(note["at"])
}

func IsCoordinatorMemory(value any) bool {
	memory, ok := coordinationIsRecord(value)
	if !ok {
		return false
	}
	notes, listed := memory["notes"].([]any)
	revision, counted := memory["revision"].(float64)
	return coordAbsentOr(memory, "purpose", coordIsString) && coordAbsentOr(memory, "purposeCleared", jsSafeInteger) &&
		listed && coordEvery(notes, isMemoryNote) &&
		counted && jsSafeInteger(revision) && revision >= 0 && coordAbsentOr(memory, "heard", finite)
}

func IsMemoryChange(value any) bool {
	change, ok := coordinationIsRecord(value)
	if !ok {
		return false
	}
	switch change["kind"] {
	case "purpose":
		purpose, has := change["purpose"]
		text, isText := purpose.(string)
		return has && purpose == nil || isText && utf16Length(text) <= MaxPurpose
	case "remember":
		return coordinationText(change["text"], MaxMemoryNote)
	case "edit":
		return coordinationText(change["noteId"], MaxMemoryNoteID) && coordinationText(change["text"], MaxMemoryNote)
	case "forget":
		return coordinationText(change["noteId"], MaxMemoryNoteID)
	}
	return false
}

// optionalText is a field that is absent or holds coordinationText.
func optionalText(record map[string]any, key string, max int) bool {
	return coordAbsentOr(record, key, func(value any) bool { return coordinationText(value, max) })
}

// coordAbsentOr is record.key === undefined || check(record.key). A JSON null is present.
func coordAbsentOr(record map[string]any, key string, check func(value any) bool) bool {
	value, has := record[key]
	return !has || check(value)
}

func coordIsString(value any) bool {
	_, ok := value.(string)
	return ok
}

func coordIsBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

func coordIsTrue(value any) bool {
	return value == true
}

func coordEvery(items []any, check func(value any) bool) bool {
	for _, item := range items {
		if !check(item) {
			return false
		}
	}
	return true
}

func coordEveryValue(record map[string]any, check func(value any) bool) bool {
	for _, value := range record {
		if !check(value) {
			return false
		}
	}
	return true
}

// distinctLabelTexts is whether no two labels match, ignoring case and outer space.
func distinctLabelTexts(labels []string) bool {
	seen := make(map[string]bool, len(labels))
	for _, label := range labels {
		seen[strings.ToLower(jsTrim(label))] = true
	}
	return len(seen) == len(labels)
}

func IsProposalRequest(value any) bool {
	request, ok := coordinationIsRecord(value)
	if !ok {
		return false
	}
	prompt, prompted := request["prompt"].(string)
	return coordinationText(request["title"], MaxProposalTitle) && prompted && !jsBlank(prompt) &&
		coordAbsentOr(request, "brief", IsThreadBrief) &&
		optionalText(request, "project", MaxProposalProject) &&
		coordAbsentOr(request, "worktree", coordIsBool) &&
		coordAbsentOr(request, "worktreeId", func(value any) bool { id, ok := value.(string); return ok && !jsBlank(id) }) &&
		coordAbsentOr(request, "engine", IsAgentEngine) &&
		coordAbsentOr(request, "model", IsModelID) &&
		coordAbsentOr(request, "effort", IsAgentEffort) &&
		coordAbsentOr(request, "role", IsThreadRole) &&
		optionalText(request, "computer", MaxProposalComputer)
}

func isProposalPlace(value any) bool {
	place, ok := coordinationIsRecord(value)
	return ok && coordAbsentOr(place, "project", coordIsString) && coordAbsentOr(place, "worktree", coordIsBool) && coordAbsentOr(place, "worktreeId", coordIsString) &&
		coordAbsentOr(place, "computer", func(value any) bool {
			computer, ok := coordinationIsRecord(value)
			return ok && coordinationText(computer["id"], 256) && coordIsString(computer["name"])
		})
}

func IsThreadProposal(value any) bool {
	proposal, ok := coordinationIsRecord(value)
	return ok && coordinationText(proposal["id"], 200) && coordIsString(proposal["title"]) && coordAbsentOr(proposal, "named", coordIsTrue) &&
		coordIsString(proposal["prompt"]) &&
		coordAbsentOr(proposal, "brief", IsThreadBrief) && isProposalPlace(proposal["place"]) &&
		coordAbsentOr(proposal, "engine", IsAgentEngine) &&
		coordAbsentOr(proposal, "model", IsModelID) &&
		coordAbsentOr(proposal, "effort", IsAgentEffort) &&
		coordAbsentOr(proposal, "role", IsThreadRole) &&
		coordAbsentOr(proposal, "from", func(value any) bool { return coordinationText(value, 200) }) &&
		finite(proposal["proposedAt"]) &&
		coordAbsentOr(proposal, "error", coordIsString) &&
		coordAbsentOr(proposal, "carried", coordIsTrue)
}

func IsRemoteLead(value any) bool {
	if !IsRemoteSender(value) {
		return false
	}
	lead, _ := coordinationIsRecord(value)
	return coordAbsentOr(lead, "proposalId", func(value any) bool { return coordinationText(value, 200) })
}

func IsHeldLead(value any) bool {
	if !IsRemoteLead(value) {
		return false
	}
	lead, _ := coordinationIsRecord(value)
	return coordAbsentOr(lead, "away", coordIsTrue) && coordAbsentOr(lead, "backAt", finite)
}

func IsRemoteHeard(value any) bool {
	heard, ok := coordinationIsRecord(value)
	return ok && coordEveryValue(heard, finite)
}

func IsRemoteStops(value any) bool {
	stops, ok := coordinationIsRecord(value)
	return ok && coordEveryValue(stops, func(value any) bool {
		stop, ok := coordinationIsRecord(value)
		return ok && coordIsString(stop["computer"]) && coordAbsentOr(stop, "runId", coordIsString) &&
			coordAbsentOr(stop, "answer", func(value any) bool {
				answer, ok := coordinationIsRecord(value)
				return ok && finite(answer["at"]) && coordAbsentOr(answer, "runId", coordIsString)
			})
	})
}

func IsCaughtStarts(value any) bool {
	starts, ok := coordinationIsRecord(value)
	return ok && coordEveryValue(starts, func(value any) bool {
		start, ok := coordinationIsRecord(value)
		return ok && coordIsString(start["computer"]) && coordAbsentOr(start, "threadId", coordIsString)
	})
}

func IsRemoteWorking(value any) bool {
	working, ok := coordinationIsRecord(value)
	return ok && coordEveryValue(working, func(value any) bool {
		work, ok := coordinationIsRecord(value)
		return ok && coordIsString(work["computer"]) && coordIsString(work["title"])
	})
}

func IsReportSummary(value any) bool {
	return coordinationText(value, MaxSummary)
}

func IsCoordinationReport(value any) bool {
	report, ok := coordinationIsRecord(value)
	return ok && IsCoordinationState(report["state"]) && IsReportSummary(report["summary"]) && finite(report["at"])
}

func IsDecisionOption(value any) bool {
	option, ok := coordinationIsRecord(value)
	return ok && coordinationText(option["label"], MaxOptionLabel) &&
		coordAbsentOr(option, "description", func(value any) bool { return coordinationText(value, MaxOptionDescription) }) &&
		coordAbsentOr(option, "recommended", coordIsTrue)
}

// DistinctLabels is whether every option's label differs from the others', ignoring case and outer space.
func DistinctLabels(options []*DecisionOption) bool {
	labels := make([]string, len(options))
	for i, option := range options {
		labels[i] = option.Label
	}
	return distinctLabelTexts(labels)
}

func isDecisionShape(value any) bool {
	decision, ok := coordinationIsRecord(value)
	if !ok {
		return false
	}
	options, listed := decision["options"].([]any)
	return coordinationText(decision["question"], MaxQuestion) &&
		coordAbsentOr(decision, "context", func(value any) bool { return coordinationText(value, MaxDecisionContext) }) &&
		listed && len(options) <= MaxOptions && coordEvery(options, IsDecisionOption)
}

func IsDecisionRequest(value any) bool {
	if !isDecisionShape(value) {
		return false
	}
	request, _ := coordinationIsRecord(value)
	options := request["options"].([]any)
	labels := make([]string, len(options))
	for i, option := range options {
		labels[i] = option.(map[string]any)["label"].(string)
	}
	return distinctLabelTexts(labels)
}

func IsDecision(value any) bool {
	if !isDecisionShape(value) {
		return false
	}
	decision, _ := coordinationIsRecord(value)
	return coordinationText(decision["id"], 200) && finite(decision["raisedAt"]) &&
		coordAbsentOr(decision, "answer", func(value any) bool { return coordinationText(value, MaxAnswer) }) &&
		coordAbsentOr(decision, "answeredAt", finite)
}

func IsCoordinationNote(value any) bool {
	note, ok := coordinationIsRecord(value)
	return ok && coordinationText(note["id"], 200) && coordinationText(note["threadId"], 200) && coordIsString(note["text"]) && finite(note["at"]) &&
		coordAbsentOr(note, "urgent", coordIsTrue) &&
		coordAbsentOr(note, "runId", func(value any) bool { return coordinationText(value, 200) }) &&
		coordAbsentOr(note, "quiet", coordIsTrue)
}
