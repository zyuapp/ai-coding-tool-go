package host

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// ReadHost is what reading threads across computers takes: the state, and the line to paired computers.
type ReadHost struct {
	State   func() *application.WorkspaceState
	Desktop interface {
		QueryComputerThreads(ctx context.Context, id string, query contracts.ComputerThreadQuery) (jsontext.Value, error)
	}
}

// PrepareThreadRequest reads from disk what a request needs before it is answered from state; nil
// when it needed nothing.
type PrepareThreadRequest func(ctx context.Context, request contracts.ThreadRequest) (*PreparedThreadRequest, error)

// LocalThreadReader answers a paired computer's thread query from this runtime, after folding in
// what runs have reported. The answer is a *contracts.ThreadTranscript or []*contracts.ThreadSummary.
func LocalThreadReader(state func() *application.WorkspaceState, prepare PrepareThreadRequest, flush func(), disposed func() bool) func(ctx context.Context, query contracts.ComputerThreadQuery) (any, error) {
	return func(ctx context.Context, query contracts.ComputerThreadQuery) (any, error) {
		if disposed() {
			return nil, errors.New("The workspace runtime has closed.")
		}
		flush()
		return QueryLocalThreads(ctx, state, prepare, query)
	}
}

// QueryLocalThreads answers from this host's own state and disk, without following any paired links.
// The answer is a *contracts.ThreadTranscript or []*contracts.ThreadSummary.
func QueryLocalThreads(ctx context.Context, state func() *application.WorkspaceState, prepare PrepareThreadRequest, query contracts.ComputerThreadQuery) (any, error) {
	invalid := errors.New("Invalid thread query.")
	if !contracts.IsComputerQuery(plain(query)) {
		return nil, invalid
	}
	switch query := query.(type) {
	case contracts.ComputerThreadQueryThreadRead:
		limit := 30.0
		if query.Limit != nil {
			limit = *query.Limit
		}
		if _, err := prepare(ctx, contracts.ThreadRequestRead{RequestID: "paired-read", TaskID: "paired", ThreadID: query.ThreadID, Limit: &limit, Computer: new("this")}); err != nil {
			return nil, err
		}
		transcript := application.ThreadTranscript(state(), query.ThreadID, jsInt(limit), 0, nil)
		if transcript == nil {
			return nil, fmt.Errorf("No thread has the ID %s.", query.ThreadID)
		}
		return transcript, nil
	case contracts.ComputerThreadQueryThreadList:
		local := contracts.ThreadListQuery{Project: query.Project, Archived: query.Archived, IdleForMs: query.IdleForMs, Search: query.Search, Attachments: query.Attachments, Limit: query.Limit}
		if local.Project == nil {
			local.Project = new("all")
		}
		if local.Limit == nil {
			local.Limit = new(20.0)
		}
		prepared, err := prepare(ctx, contracts.ThreadRequestList{
			RequestID: "paired-list", TaskID: "paired", Computer: new("this"),
			Project: local.Project, Archived: local.Archived, IdleForMs: local.IdleForMs, Search: local.Search, Attachments: local.Attachments, Limit: local.Limit,
		})
		if err != nil {
			return nil, err
		}
		var stored jsonx.Set[string]
		if prepared != nil {
			stored = prepared.Stored
		}
		return LocalThreadList(state(), "paired", local, stored)
	}
	return nil, invalid
}

// jsInt is a JavaScript number used as a count, as slice() truncates it.
func jsInt(value float64) int {
	switch {
	case math.IsNaN(value):
		return 0
	case value >= math.MaxInt32:
		return math.MaxInt32
	case value <= math.MinInt32:
		return math.MinInt32
	}
	return int(value)
}

// LocalThreadList lists this state's threads. stored names unloaded threads whose stored messages
// match the search; nil when none was read.
func LocalThreadList(state *application.WorkspaceState, caller string, query contracts.ThreadListQuery, stored jsonx.Set[string]) ([]*contracts.ThreadSummary, error) {
	scope, err := application.ResolveScope(state, caller, query.Project)
	if err != nil {
		return nil, err
	}
	filter := contracts.ThreadFilter{Scope: scope, Archived: query.Archived, IdleForMs: query.IdleForMs, Search: query.Search, Attachments: query.Attachments, Limit: query.Limit, Stored: stored}
	return application.ThreadSummaries(state, filter, application.Now()), nil
}

// computers resolves "this", "all", or a paired computer's id or name.
func computers(state *application.WorkspaceState, selected string) (local bool, paired []*application.PairedComputer, err error) {
	if selected == "this" {
		return true, nil, nil
	}
	if selected == "all" {
		return true, state.Computers.Paired, nil
	}
	var found []*application.PairedComputer
	for _, computer := range state.Computers.Paired {
		if computer.ID == selected {
			found = []*application.PairedComputer{computer}
			break
		}
	}
	if found == nil {
		wanted := strings.ToLower(jsTrim(selected))
		for _, computer := range state.Computers.Paired {
			if strings.ToLower(computer.Name) == wanted {
				found = append(found, computer)
			}
		}
	}
	if len(found) != 1 {
		if len(found) > 0 {
			return false, nil, fmt.Errorf("More than one computer is named %s. Use its computer ID.", selected)
		}
		return false, nil, fmt.Errorf("No paired computer is named %s.", selected)
	}
	return false, found, nil
}

// jsTrim is String.prototype.trim: it strips JavaScript's whitespace, which includes the BOM.
func jsTrim(text string) string {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
			return true
		}
		return r >= 0x2000 && r <= 0x200A
	})
}

// NamedComputer is the paired computer a caller names, or nil for this one. A thread starts on one
// computer, so "all" names none and fails.
func NamedComputer(state *application.WorkspaceState, selected string) (*application.PairedComputer, error) {
	if selected == "all" {
		return nil, errors.New(`A thread runs on one computer. Use "this", or a paired computer's name or ID.`)
	}
	_, paired, err := computers(state, selected)
	if err != nil || len(paired) == 0 {
		return nil, err
	}
	return paired[0], nil
}

func Tag(computer *application.PairedComputer) *contracts.ThreadSummaryComputer {
	return &contracts.ThreadSummaryComputer{ID: computer.ID, Name: computer.Name, Offline: computer.Status != domain.ComputerStatusConnected}
}

// Online fails while the computer is not connected; doing is what its threads cannot be ("read" in TS by default).
func Online(computer *application.PairedComputer, doing string) error {
	if computer.Status != domain.ComputerStatusConnected {
		return fmt.Errorf("%s is %s, so its threads cannot be %s right now.", computer.Name, domain.AwayStatus(computer.Status), doing)
	}
	return nil
}

// RemoteSummary is a paired computer's thread as a listing shows it, tagged with the computer, or nil
// while that computer's state does not hold it.
func RemoteSummary(state *application.WorkspaceState, computerID, threadID string) *contracts.ThreadSummary {
	for _, computer := range state.Computers.Paired {
		if computer.ID != computerID {
			continue
		}
		if computer.State == nil {
			return nil
		}
		for _, thread := range computer.State.Threads {
			if thread.ID == threadID {
				summary := *application.ThreadSummary(computer.State, thread, nil)
				summary.Computer = Tag(computer)
				return &summary
			}
		}
		return nil
	}
	return nil
}

// ListAcrossComputers lists threads here and on the paired computers the query names. Ordinary
// listings use the already mirrored index, including cached rows when a host is offline. A listing
// across every computer leaves out one it cannot answer for: one that has sent no list yet, or, for a
// search, one whose line is down or that fails to answer. Naming that computer says why.
func ListAcrossComputers(ctx context.Context, host ReadHost, caller string, query contracts.ThreadListQuery, stored jsonx.Set[string]) ([]*contracts.ThreadSummary, error) {
	state := host.State()
	selectedName := "this"
	if query.Computer != nil {
		selectedName = *query.Computer
	}
	local, paired, err := computers(state, selectedName)
	if err != nil {
		return nil, err
	}
	filter := query
	filter.Computer = nil
	if query.Computer != nil && *query.Computer != "this" && filter.Project == nil {
		filter.Project = new("all")
	}
	if len(paired) > 0 && filter.Project != nil && *filter.Project == "current" {
		return nil, errors.New(`Project "current" belongs to this computer. Use computer "this", or name a remote project.`)
	}
	every := query.Computer != nil && *query.Computer == "all"
	covers := func(state *application.WorkspaceState) bool {
		project := filter.Project
		return !every || project == nil || *project == "" || *project == "all" || *project == "current" || len(domain.MatchingProjects(state.Projects, *project)) > 0
	}
	rows := []*contracts.ThreadSummary{}
	if local && covers(state) {
		if rows, err = LocalThreadList(state, caller, filter, stored); err != nil {
			return nil, err
		}
	}
	remote, err := all(paired, func(computer *application.PairedComputer) ([]*contracts.ThreadSummary, error) {
		if computer.State == nil {
			if every {
				return nil, nil
			}
			return nil, fmt.Errorf("%s has not sent its thread list yet. Try again once it is connected.", computer.Name)
		}
		if !covers(computer.State) {
			return nil, nil
		}
		var summaries []*contracts.ThreadSummary
		if filter.Search != nil && jsTrim(*filter.Search) != "" {
			if every && computer.Status != domain.ComputerStatusConnected {
				return nil, nil
			}
			if err := Online(computer, "read"); err != nil {
				return nil, err
			}
			listed, err := remoteList(ctx, host, computer, filter)
			if err != nil {
				if every {
					return nil, nil
				}
				return nil, err
			}
			summaries = listed
		} else {
			listed, err := LocalThreadList(computer.State, "", filter, nil)
			if err != nil {
				return nil, err
			}
			summaries = listed
		}
		tagged := make([]*contracts.ThreadSummary, len(summaries))
		for index, thread := range summaries {
			summary := *thread
			summary.Computer = Tag(computer)
			tagged[index] = &summary
		}
		return tagged, nil
	})
	if err != nil {
		return nil, err
	}
	combined := slices.Concat(append([][]*contracts.ThreadSummary{rows}, remote...)...)
	slices.SortStableFunc(combined, func(a, b *contracts.ThreadSummary) int {
		switch {
		case b.LastActivityAt > a.LastActivityAt:
			return 1
		case b.LastActivityAt < a.LastActivityAt:
			return -1
		}
		return 0
	})
	if combined == nil {
		combined = []*contracts.ThreadSummary{}
	}
	if query.Limit == nil {
		return combined, nil
	}
	return combined[:min(len(combined), max(0, jsInt(*query.Limit)))], nil
}

// remoteList asks a paired computer to search its own threads, checking what it answers.
func remoteList(ctx context.Context, host ReadHost, computer *application.PairedComputer, filter contracts.ThreadListQuery) ([]*contracts.ThreadSummary, error) {
	limit := 20.0
	if filter.Limit != nil {
		limit = *filter.Limit
	}
	raw, err := host.Desktop.QueryComputerThreads(ctx, computer.ID, contracts.ComputerThreadQueryThreadList{
		Project: filter.Project, Search: filter.Search, Attachments: filter.Attachments, Limit: new(math.Min(limit, 200)), Archived: filter.Archived, IdleForMs: filter.IdleForMs,
	})
	if err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("Invalid thread list from %s.", computer.Name)
	var listed []any
	if jsonx.Unmarshal(raw, &listed) != nil || listed == nil || len(listed) > 200 {
		return nil, invalid
	}
	for _, item := range listed {
		if !contracts.IsThreadSummary(item) {
			return nil, invalid
		}
	}
	var summaries []*contracts.ThreadSummary
	if jsonx.Unmarshal(raw, &summaries) != nil {
		return nil, invalid
	}
	return summaries, nil
}

// namedCandidates is as many of the threads a loose reference matches as a refusal names.
const namedCandidates = 5

// ThreadReadMatch is the one thread a reference resolved to, and the state and computer (nil for this
// one) that hold it.
type ThreadReadMatch struct {
	State    *application.WorkspaceState
	Computer *application.PairedComputer
	Thread   *domain.Thread
}

// ResolveThreadRead resolves a reference across the computers selected ("all" in TS by default). The
// same resolution runs before loading history, so a remote read never loads local messages. A
// reference matching more than one thread, here or across computers, is refused rather than guessed.
func ResolveThreadRead(state *application.WorkspaceState, reference string, selected string) (ThreadReadMatch, error) {
	local, paired, err := computers(state, selected)
	if err != nil {
		return ThreadReadMatch{}, err
	}
	var sources []ThreadReadMatch
	if local {
		sources = append(sources, ThreadReadMatch{State: state})
	}
	for _, computer := range paired {
		if computer.State != nil {
			sources = append(sources, ThreadReadMatch{State: computer.State, Computer: computer})
		}
	}
	// Each computer's closest matches, kept only where none is closer on another computer.
	type ranked struct {
		source  ThreadReadMatch
		rank    int
		threads []*domain.Thread
	}
	rankings := make([]ranked, len(sources))
	closest := math.MaxInt
	for index, source := range sources {
		rank, threads := application.ThreadMatches(source.State, reference)
		rankings[index] = ranked{source, rank, threads}
		closest = min(closest, rank)
	}
	var candidates []ThreadReadMatch
	for _, ranking := range rankings {
		if ranking.rank != closest {
			continue
		}
		for _, thread := range ranking.threads {
			candidates = append(candidates, ThreadReadMatch{State: ranking.source.State, Computer: ranking.source.Computer, Thread: thread})
		}
	}
	if len(candidates) > 1 {
		return ThreadReadMatch{}, errors.New(ambiguous(reference, candidates))
	}
	if len(candidates) == 0 {
		var missing []string
		for _, computer := range paired {
			if computer.State == nil || computer.Status != domain.ComputerStatusConnected {
				missing = append(missing, computer.Name)
			}
		}
		suffix := ""
		if len(missing) > 0 {
			verb := "are"
			if len(missing) == 1 {
				verb = "is"
			}
			suffix = fmt.Sprintf(" %s %s not connected, so newer threads there may be missing.", strings.Join(missing, ", "), verb)
		}
		return ThreadReadMatch{}, fmt.Errorf("No thread has the ID %s.%s", reference, suffix)
	}
	return candidates[0], nil
}

func ambiguous(reference string, candidates []ThreadReadMatch) string {
	places := map[string]bool{}
	for _, candidate := range candidates {
		places[placeOf(candidate.Computer)] = true
	}
	var only *application.PairedComputer
	if len(places) == 1 {
		only = candidates[0].Computer
	}
	named := make([]string, 0, namedCandidates)
	for _, candidate := range candidates[:min(len(candidates), namedCandidates)] {
		item := candidate.Thread.ID + ` "` + candidate.Thread.Title + `"`
		if len(places) > 1 {
			if candidate.Computer != nil {
				item += fmt.Sprintf(" on %s [%s]", candidate.Computer.Name, candidate.Computer.ID)
			} else {
				item += " on this computer"
			}
		}
		named = append(named, item)
	}
	more := len(candidates) - len(named)
	var which string
	if len(places) > 1 {
		which = "More than one computer has a thread matching " + reference
	} else if only != nil {
		which = fmt.Sprintf("More than one thread on %s matches %s", only.Name, reference)
	} else {
		which = "More than one thread matches " + reference
	}
	tail := ""
	if more > 0 {
		tail = fmt.Sprintf(", and %d more", more)
	}
	return fmt.Sprintf("%s: %s%s. Use the full thread ID.", which, strings.Join(named, ", "), tail)
}

// placeOf names where a candidate is, "" for this computer.
func placeOf(computer *application.PairedComputer) string {
	if computer == nil {
		return ""
	}
	return "computer:" + computer.ID
}

// ReadAcrossComputers resolves across the paired indexes, then asks only the owning computer for the
// transcript. selected is "all" in TS by default.
func ReadAcrossComputers(ctx context.Context, host ReadHost, reference string, limit *float64, selected string) (*contracts.ThreadTranscript, error) {
	state := host.State()
	match, err := ResolveThreadRead(state, reference, selected)
	if err != nil {
		return nil, err
	}
	if match.Computer == nil {
		count := 30
		if limit != nil {
			count = jsInt(*limit)
		}
		return application.ThreadTranscript(state, match.Thread.ID, count, 0, nil), nil
	}
	computer := match.Computer
	if err := Online(computer, "read"); err != nil {
		return nil, err
	}
	wanted := 30.0
	if limit != nil {
		wanted = *limit
	}
	raw, err := host.Desktop.QueryComputerThreads(ctx, computer.ID, contracts.ComputerThreadQueryThreadRead{ThreadID: match.Thread.ID, Limit: new(math.Min(wanted, 200))})
	if err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("Invalid thread transcript from %s.", computer.Name)
	var decoded any
	if jsonx.Unmarshal(raw, &decoded) != nil || !contracts.IsThreadTranscript(decoded) {
		return nil, invalid
	}
	var transcript contracts.ThreadTranscript
	if jsonx.Unmarshal(raw, &transcript) != nil || transcript.Thread.ID != match.Thread.ID {
		return nil, invalid
	}
	transcript.Thread.Computer = Tag(computer)
	return &transcript, nil
}
