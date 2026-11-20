package application

import (
	"maps"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

// DiffPatch changes a copy of a dock's review in place (TS: Partial<DiffState>). Clearing an
// optional field is setting it to nil.
type DiffPatch func(next *DiffState)

const DiffModeMenu = "diff:mode"

// Shared by every dock with no review of its own; never written.
var EmptyDiff = &DiffState{
	Mode:             DiffStateModeBranch,
	Range:            domain.DefaultBranchRange,
	WorkspaceID:      nil,
	Result:           nil,
	Loading:          false,
	Collapsed:        []string{},
	Viewed:           map[string]string{},
	Split:            true,
	IgnoreWhitespace: true,
}

func DiffFor(state *WorkspaceState, owner string) *DiffState {
	if diff, ok := state.Diffs[owner]; ok && diff != nil {
		return diff
	}
	return EmptyDiff
}

// `patch` may be nil.
func WithDiff(state *WorkspaceState, owner string, patch DiffPatch) *WorkspaceState {
	diff := *DiffFor(state, owner)
	if patch != nil {
		patch(&diff)
	}
	next := *state
	next.Diffs = maps.Clone(state.Diffs)
	if next.Diffs == nil {
		next.Diffs = map[string]*DiffState{}
	}
	next.Diffs[owner] = &diff
	return &next
}

// Whether a fresh list counts the same files a different way, which is what the whitespace toggle
// does. Nothing changed under the user, so what they had read and folded is still read and folded.
// `previous` may be nil.
func Recounted(previous contracts.DiffSummaryResult, result contracts.DiffSummaryResult) bool {
	before, ok := previous.(contracts.DiffSummaryResultAvailable)
	if !ok {
		return false
	}
	after, ok := result.(contracts.DiffSummaryResultAvailable)
	return ok && before.IgnoreWhitespace != after.IgnoreWhitespace
}

// The ticks that survive a fresh list: a file whose counts moved has changed since it was read, so
// it comes back unread rather than staying ticked against work the user has not seen. A recount is
// the exception, and its ticks are stamped again with the counts the file is now listed at.
// `previous` may be nil.
func RetainedViews(viewed map[string]string, result contracts.DiffSummaryResult, previous contracts.DiffSummaryResult) map[string]string {
	available, ok := result.(contracts.DiffSummaryResultAvailable)
	if !ok {
		return viewed
	}
	fingerprints := fingerprintsOf(available.Files)
	var before map[string]string
	if earlier, ok := previous.(contracts.DiffSummaryResultAvailable); ok && Recounted(previous, result) {
		before = fingerprintsOf(earlier.Files)
	}
	kept := map[string]string{}
	for path, mark := range viewed {
		fingerprint, listed := fingerprints[path]
		if !listed {
			continue
		}
		if was, ok := before[path]; fingerprint == mark || ok && was == mark {
			kept[path] = fingerprint
		}
	}
	return kept
}

// The folds a fresh list lands with. A file the user has already decided about keeps their decision;
// a file that is new, or that changed under them, folds when it is too large to draw. That is how a
// checkout which blows up under an open review folds itself away instead of taking the window with it.
func FoldedOnLoad(diff *DiffState, files []*domain.DiffFileSummary, result contracts.DiffSummaryResult) []string {
	before := map[string]string{}
	if available, ok := diff.Result.(contracts.DiffSummaryResultAvailable); ok {
		before = fingerprintsOf(available.Files)
	}
	counted := Recounted(diff.Result, result)
	oversized := domain.FoldedForSize(files)
	held := jsonx.Set[string]{}
	for _, path := range diff.Collapsed {
		held[path] = struct{}{}
	}
	folded := []string{}
	for _, file := range files {
		fingerprint, seen := before[file.Path]
		known := seen
		if !counted {
			known = seen && fingerprint == domain.FileFingerprint(file)
		}
		var fold bool
		if known {
			_, fold = held[file.Path]
		} else {
			_, fold = oversized[file.Path]
		}
		if fold {
			folded = append(folded, file.Path)
		}
	}
	return folded
}

// Whether a landed list still answers what its dock is asking, which a slow read may not.
func DiffMatches(diff *DiffState, workspaceID string, diffRange domain.DiffRange) bool {
	return diff.WorkspaceID != nil && *diff.WorkspaceID == workspaceID && string(diff.Mode) == domain.ModeForRange(diffRange) && domain.RangeKey(diff.Range) == domain.RangeKey(diffRange)
}

// Each file's fingerprint by path, the last file listed under a path winning as a Map built from entries does.
func fingerprintsOf(files []*domain.DiffFileSummary) map[string]string {
	fingerprints := make(map[string]string, len(files))
	for _, file := range files {
		fingerprints[file.Path] = domain.FileFingerprint(file)
	}
	return fingerprints
}
