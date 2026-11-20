package application

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func IsComputerInput(input WorkspaceInput) bool {
	_, ok := input.(ComputerInput)
	return ok
}

// The computers slice with `computers` in place of the whole ComputersState, which callers build by copying state.Computers.
func withComputers(state *WorkspaceState, computers ComputersState, effects ...WorkspaceEffect) SliceTransition {
	return SliceTransition{Patch: func(next *WorkspaceState) { next.Computers = computers }, Effects: effects}
}

// A computer's name as typed: trimmed as JavaScript trims, then cut to MaxComputerName UTF-16 units.
func computersTypedName(name string) string {
	trimmed := strings.TrimFunc(name, func(r rune) bool { return (unicode.IsSpace(r) && r != 0x85) || r == 0xFEFF })
	units := 0
	for index, r := range trimmed {
		units += utf16.RuneLen(r)
		if units > domain.MaxComputerName {
			return trimmed[:index]
		}
	}
	return trimmed
}

// The links as the host reports them, each keeping the state this window already holds for it.
func computerCommandsLinked(state *WorkspaceState, links []*domain.ComputerLink) []*PairedComputer {
	held := make(map[string]*PairedComputer, len(state.Computers.Paired))
	for _, computer := range state.Computers.Paired {
		held[computer.ID] = computer
	}
	// Capacity at least 1: a new list is never the same as the last by jsonx.Same, even when both are empty.
	paired := make([]*PairedComputer, len(links), max(len(links), 1))
	for index, link := range links {
		known := held[link.ID]
		if known != nil && known.Status == link.Status && sameOptionalText(known.Error, link.Error) && known.Name == link.Name && known.Host == link.Host && slices.Equal(known.Capabilities, link.Capabilities) && (known.Capabilities == nil) == (link.Capabilities == nil) {
			paired[index] = known
			continue
		}
		next := &PairedComputer{ComputerLink: *link}
		if known != nil {
			next.State = known.State
		}
		paired[index] = next
	}
	return paired
}

// Two optional strings that are both absent or both the same text.
func sameOptionalText(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Which paired computer the conversation shows as the lines move. One whose line drops leaves the
// screen, except while its terminal is in front, which waits to restore the same shell; it comes
// back on screen when its line does, unless the window has moved on in the meantime.
func onScreen(computers *ComputersState, paired []*PairedComputer) (active, dropped *string) {
	if selected := pairedNamed(paired, computers.Active); selected != nil {
		if selected.Status == domain.ComputerStatusConnected || terminalInFront(selected.State) {
			return &selected.ID, nil
		}
		return nil, &selected.ID
	}
	if computers.Active != nil {
		return nil, nil
	}
	returning := pairedNamed(paired, computers.Dropped)
	if returning == nil {
		return nil, nil
	}
	if returning.Status == domain.ComputerStatusConnected {
		return &returning.ID, nil
	}
	return nil, &returning.ID
}

// A line that comes back up to the computer on screen is told again whether anyone here is looking,
// since that computer forgot it when the line dropped. The others already take it that nobody is.
func lookingAgain(state *WorkspaceState, paired []*PairedComputer, active *string) []WorkspaceEffect {
	shown := pairedNamed(paired, active)
	if shown == nil || shown.Status != domain.ComputerStatusConnected {
		return nil
	}
	before := pairedNamed(state.Computers.Paired, &shown.ID)
	if before != nil && before.Status == domain.ComputerStatusConnected && state.Computers.Active != nil && *state.Computers.Active == shown.ID {
		return nil
	}
	return []WorkspaceEffect{WorkspaceEffectComputerForward{ID: shown.ID, Inputs: []WorkspaceInput{contracts.ViewCommandViewSetFocused{Focused: state.Focused}}}}
}

func ReduceComputers(state *WorkspaceState, input ComputerInput) SliceTransition {
	computers := state.Computers
	switch input := input.(type) {
	case contracts.ComputerCommandComputersDiscover:
		computers.Searching, computers.SearchError = true, nil
		return withComputers(state, computers, WorkspaceEffectComputerDiscover{})
	case ComputerEventComputersFound:
		computers.Found, computers.Searching, computers.SearchError = input.Found, false, nil
		return withComputers(state, computers)
	case ComputerEventComputersSearchFailed:
		computers.Searching, computers.SearchError = false, &input.Message
		return withComputers(state, computers)
	// A pair with no code yet opens the card the code is typed into; one with a code sends it.
	case contracts.ComputerCommandComputersPair:
		computers.Pairing = &domain.ComputerPairing{Host: input.Host, Name: input.Name, Busy: input.Code != ""}
		if input.Code == "" {
			return withComputers(state, computers)
		}
		return withComputers(state, computers, WorkspaceEffectComputerPair{Host: input.Host, Name: input.Name, Code: input.Code})
	case ComputerEventComputersPairFailed:
		if computers.Pairing != nil {
			pairing := *computers.Pairing
			pairing.Busy, pairing.Error = false, &input.Message
			computers.Pairing = &pairing
		}
		return withComputers(state, computers)
	case contracts.ComputerCommandComputersCancelPairing:
		computers.Pairing = nil
		return withComputers(state, computers)
	case contracts.ComputerCommandComputersForget:
		if computers.Active != nil && *computers.Active == input.ID {
			computers.Active = nil
		}
		if computers.Dropped != nil && *computers.Dropped == input.ID {
			computers.Dropped = nil
		}
		if computers.Filter == input.ID {
			computers.Filter = "all"
		}
		return withComputers(state, computers, WorkspaceEffectComputerForget{ID: input.ID})
	case contracts.ComputerCommandComputersReconnect:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectComputerReconnect{ID: input.ID}}}
	// The name shows at once when there is one; an empty one is the host's to fill in and announce.
	case contracts.ComputerCommandComputersRename:
		name := computersTypedName(input.Name)
		if name == "" {
			return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectComputerRename{Name: name}}}
		}
		computers.Name = name
		return withComputers(state, computers, WorkspaceEffectComputerRename{Name: name})
	case contracts.ComputerCommandComputersLabel:
		if pairedNamed(computers.Paired, &input.ID) == nil {
			return SliceTransition{}
		}
		name := computersTypedName(input.Name)
		if name != "" {
			paired := slices.Clone(computers.Paired)
			for index, computer := range paired {
				if computer.ID == input.ID {
					renamed := *computer
					renamed.Name = name
					paired[index] = &renamed
				}
			}
			computers.Paired = paired
		}
		return withComputers(state, computers, WorkspaceEffectComputerLabel{ID: input.ID, Name: name})
	case contracts.ComputerCommandComputersFilter:
		computers.Filter = input.Filter
		return withComputers(state, computers)
	case ComputerEventComputersForwarded:
		return SliceTransition{Edits: []OwnerEdit{SentElsewhereIn(ComposerOf(state), &input.Draft)}}
	case ComputerEventComputersAnswered:
		return SliceTransition{Result: contracts.WorkspaceCommandResultTrue{TaskID: &input.TaskID}}
	case ComputerEventComputersChanged:
		paired := computerCommandsLinked(state, input.Links)
		// A pairing that now shows up as a link is done; one that went away, or whose line dropped, takes the screen with it.
		pairing := computers.Pairing
		if pairing != nil && slices.ContainsFunc(paired, func(computer *PairedComputer) bool { return computer.Host == pairing.Host }) {
			pairing = nil
		}
		active, dropped := onScreen(&computers, paired)
		filter := computers.Filter
		if filter != "all" && filter != "this" && pairedNamed(paired, &filter) == nil {
			filter = "all"
		}
		effects := lookingAgain(state, paired, active)
		computers.Name, computers.Paired, computers.Pairing, computers.Active, computers.Dropped, computers.Filter = input.Name, paired, pairing, active, dropped, filter
		return withComputers(state, computers, effects...)
	case ComputerEventComputerState:
		computer := pairedNamed(computers.Paired, &input.ID)
		if computer == nil {
			return SliceTransition{}
		}
		paired := slices.Clone(computers.Paired)
		for index, item := range paired {
			if item == computer {
				next := *item
				next.State = &input.State
				paired[index] = &next
			}
		}
		computers.Paired = paired
		return withComputers(state, computers)
	// A thread there working under a coordinator here is announced here, under that coordinator's
	// notices, so what that computer carries of the same thread is dropped.
	case ComputerEventComputerNotice:
		computer := pairedNamed(computers.Paired, &input.ID)
		if computer == nil {
			return SliceTransition{}
		}
		var held *domain.Thread
		if computer.State != nil {
			held = threadNamed(computer.State.Threads, input.Notice.TaskID)
		}
		if lead := domain.RemoteLeadOf(held); lead != nil && domain.IsCoordinator(threadNamed(state.Threads, lead.ThreadID)) {
			return SliceTransition{}
		}
		return SliceTransition{Effects: AnnouncedNotice(state, input.Notice)}
	}
	return SliceTransition{}
}

// The thread with `id` in a list, or nil.
func threadNamed(threads []*domain.Thread, id string) *domain.Thread {
	for _, thread := range threads {
		if thread.ID == id {
			return thread
		}
	}
	return nil
}
