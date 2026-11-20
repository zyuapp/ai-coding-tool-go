package workspace

import (
	"encoding/json/v2"
	"fmt"
)

// Local stand-ins for the generated domain and contracts types this package reads and returns.
// Each encodes exactly as its TypeScript counterpart.

// literal names a string constant a Tag encodes as.
type literal interface{ literal() string }

// Tag is a zero-size discriminator field that always encodes as its literal.
type Tag[L literal] struct{}

func (Tag[L]) MarshalJSON() ([]byte, error) {
	var l L
	return json.Marshal(l.literal())
}

func (Tag[L]) UnmarshalJSON(data []byte) error {
	var l L
	var got string
	if err := json.Unmarshal(data, &got); err != nil {
		return err
	}
	if got != l.literal() {
		return fmt.Errorf("expected %q, got %q", l.literal(), got)
	}
	return nil
}

type (
	litAvailable   struct{}
	litUnavailable struct{}
	litUnknown     struct{}
	litError       struct{}
	litTooLarge    struct{}
	litFound       struct{}
	litNone        struct{}
	litGhMissing   struct{}
)

func (litAvailable) literal() string   { return "available" }
func (litUnavailable) literal() string { return "unavailable" }
func (litUnknown) literal() string     { return "unknown" }
func (litError) literal() string       { return "error" }
func (litTooLarge) literal() string    { return "too-large" }
func (litFound) literal() string       { return "found" }
func (litNone) literal() string        { return "none" }
func (litGhMissing) literal() string   { return "gh-missing" }

// domain/workspace.ts

type WorkspaceKind string

const (
	WorkspaceProject     WorkspaceKind = "project"
	WorkspaceProjectless WorkspaceKind = "projectless"
	WorkspaceWorktree    WorkspaceKind = "worktree"
)

type WorkspaceRecord struct {
	ID   string        `json:"id"`
	Kind WorkspaceKind `json:"kind"`
	Root string        `json:"root"`
}

type UnavailableReason string

const (
	ReasonMissing      UnavailableReason = "missing"
	ReasonNotDirectory UnavailableReason = "not-directory"
	ReasonInaccessible UnavailableReason = "inaccessible"
	ReasonChanged      UnavailableReason = "changed"
)

type ResolutionStatus string

const (
	ResolutionAvailable   ResolutionStatus = "available"
	ResolutionUnavailable ResolutionStatus = "unavailable"
)

// WorkspaceResolution is `WorkspaceResolution`: Reason is set only while unavailable.
type WorkspaceResolution struct {
	Status    ResolutionStatus  `json:"status"`
	Workspace WorkspaceRecord   `json:"workspace"`
	Reason    UnavailableReason `json:"reason,omitzero"`
}

// domain/diff.ts

type DiffRangeKind string

const (
	RangeUncommitted DiffRangeKind = "uncommitted"
	RangeCommit      DiffRangeKind = "commit"
	RangeBranches    DiffRangeKind = "branches"
)

// DiffRange is `{kind:"uncommitted"} | {kind:"commit",commit} | {kind:"branches",base,compare:string|null}`.
type DiffRange struct {
	Kind    DiffRangeKind
	Commit  string
	Base    string
	Compare *string
}

func (r DiffRange) MarshalJSON() ([]byte, error) {
	switch r.Kind {
	case RangeCommit:
		return json.Marshal(struct {
			Kind   DiffRangeKind `json:"kind"`
			Commit string        `json:"commit"`
		}{r.Kind, r.Commit})
	case RangeBranches:
		return json.Marshal(struct {
			Kind    DiffRangeKind `json:"kind"`
			Base    string        `json:"base"`
			Compare *string       `json:"compare"`
		}{r.Kind, r.Base, r.Compare})
	default:
		return json.Marshal(struct {
			Kind DiffRangeKind `json:"kind"`
		}{r.Kind})
	}
}

func (r *DiffRange) UnmarshalJSON(data []byte) error {
	var raw struct {
		Kind    DiffRangeKind `json:"kind"`
		Commit  string        `json:"commit"`
		Base    string        `json:"base"`
		Compare *string       `json:"compare"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = DiffRange{Kind: raw.Kind, Commit: raw.Commit, Base: raw.Base, Compare: raw.Compare}
	return nil
}

// rangeKey is `rangeKey` from domain/diff.ts.
func rangeKey(r DiffRange) string {
	switch r.Kind {
	case RangeCommit:
		return "commit:" + r.Commit
	case RangeUncommitted:
		return "uncommitted"
	}
	compare := ""
	if r.Compare != nil {
		compare = *r.Compare
	}
	return "branches:" + r.Base + ":" + compare
}

type DiffFileStatus string

const (
	FileAdded     DiffFileStatus = "added"
	FileModified  DiffFileStatus = "modified"
	FileDeleted   DiffFileStatus = "deleted"
	FileRenamed   DiffFileStatus = "renamed"
	FileUntracked DiffFileStatus = "untracked"
)

type DiffFileSummary struct {
	Path         string         `json:"path"`
	PreviousPath *string        `json:"previousPath,omitzero"`
	Status       DiffFileStatus `json:"status"`
	Additions    float64        `json:"additions"`
	Deletions    float64        `json:"deletions"`
	Binary       bool           `json:"binary"`
}

// contracts/ipc.ts results

type ChangedFilesResult interface{ changedFilesResult() }
type DiffSummaryResult interface{ diffSummaryResult() }
type DiffPatchResult interface{ diffPatchResult() }

type ChangedFilesAvailable struct {
	Status    Tag[litAvailable] `json:"status"`
	Files     []string          `json:"files"`
	Branch    *string           `json:"branch"`
	Baseline  *string           `json:"baseline"`
	Additions float64           `json:"additions"`
	Deletions float64           `json:"deletions"`
}

type DiffSummaryAvailable struct {
	Status           Tag[litAvailable] `json:"status"`
	Range            DiffRange         `json:"range"`
	IgnoreWhitespace bool              `json:"ignoreWhitespace"`
	Files            []DiffFileSummary `json:"files"`
	Additions        float64           `json:"additions"`
	Deletions        float64           `json:"deletions"`
}

type DiffPatchAvailable struct {
	Status Tag[litAvailable] `json:"status"`
	Patch  string            `json:"patch"`
}

type DiffPatchTooLarge struct {
	Status Tag[litTooLarge] `json:"status"`
	Limit  float64          `json:"limit"`
}

// WorkspaceUnavailable is the `unavailable` member of the changed-files and diff-summary results.
type WorkspaceUnavailable struct {
	Status Tag[litUnavailable] `json:"status"`
	Reason UnavailableReason   `json:"reason"`
}

// WorkspaceUnknown is the `unknown` member of the changed-files and diff-summary results.
type WorkspaceUnknown struct {
	Status      Tag[litUnknown] `json:"status"`
	WorkspaceID string          `json:"workspaceId"`
}

// Failure is the `{status:"error",message}` member every result here shares.
type Failure struct {
	Status  Tag[litError] `json:"status"`
	Message string        `json:"message"`
}

func (ChangedFilesAvailable) changedFilesResult() {}
func (WorkspaceUnavailable) changedFilesResult()  {}
func (WorkspaceUnknown) changedFilesResult()      {}
func (Failure) changedFilesResult()               {}

func (DiffSummaryAvailable) diffSummaryResult() {}
func (WorkspaceUnavailable) diffSummaryResult() {}
func (WorkspaceUnknown) diffSummaryResult()     {}
func (Failure) diffSummaryResult()              {}

func (DiffPatchAvailable) diffPatchResult() {}
func (DiffPatchTooLarge) diffPatchResult()  {}
func (Failure) diffPatchResult()            {}

// contracts/git.ts

type BranchesResult interface{ branchesResult() }

// Branches is what `listBranches` answers: local branches newest first, remote ones apart, and the one checked out.
type Branches struct {
	Branches []string `json:"branches"`
	Remotes  []string `json:"remotes"`
	Current  *string  `json:"current"`
}

type BranchesAvailable struct {
	Status   Tag[litAvailable] `json:"status"`
	Branches `json:",inline"`
}

func (BranchesAvailable) branchesResult() {}
func (Failure) branchesResult()           {}

// domain/pull-request.ts

type PullRequestState string

const (
	PullRequestDraft  PullRequestState = "draft"
	PullRequestOpen   PullRequestState = "open"
	PullRequestMerged PullRequestState = "merged"
	PullRequestClosed PullRequestState = "closed"
)

type PullRequestChecks string

const (
	ChecksPending PullRequestChecks = "pending"
	ChecksPassing PullRequestChecks = "passing"
	ChecksFailing PullRequestChecks = "failing"
)

type PullRequestReview string

const (
	ReviewApproved         PullRequestReview = "approved"
	ReviewChangesRequested PullRequestReview = "changes-requested"
	ReviewRequired         PullRequestReview = "required"
)

// PullRequestNeeds leaves out each part GitHub says nothing of. Conflicts is the literal `true` or absent.
type PullRequestNeeds struct {
	Checks    PullRequestChecks `json:"checks,omitzero"`
	Review    PullRequestReview `json:"review,omitzero"`
	Conflicts bool              `json:"conflicts,omitzero"`
}

type PullRequestRef struct {
	Number           float64          `json:"number"`
	Title            string           `json:"title"`
	URL              string           `json:"url"`
	State            PullRequestState `json:"state"`
	PullRequestNeeds `json:",inline"`
}

type PullRequestAnswer interface{ pullRequestAnswer() }

type PullRequestFound struct {
	Status      Tag[litFound]  `json:"status"`
	PullRequest PullRequestRef `json:"pullRequest"`
}

type PullRequestNone struct {
	Status Tag[litNone] `json:"status"`
}

type PullRequestGhMissing struct {
	Status Tag[litGhMissing] `json:"status"`
}

func (PullRequestFound) pullRequestAnswer()     {}
func (PullRequestNone) pullRequestAnswer()      {}
func (PullRequestGhMissing) pullRequestAnswer() {}

// domain/worktree.ts and the worktree requests in contracts/ipc.ts

type WorktreeComparison struct {
	Branch string  `json:"branch"`
	Ahead  float64 `json:"ahead"`
}

type WorktreeStatus struct {
	ChangedFiles *float64            `json:"changedFiles"`
	Comparison   *WorktreeComparison `json:"comparison"`
}

type ManagedWorktree struct {
	ID         string         `json:"id"`
	Root       string         `json:"root"`
	Repository *string        `json:"repository"`
	Branch     *string        `json:"branch"`
	Status     WorktreeStatus `json:"status"`
}

// CreatedWorktree is `Omit<Worktree, "projectId">`.
type CreatedWorktree struct {
	ID          string  `json:"id"`
	Name        *string `json:"name,omitzero"`
	Root        string  `json:"root"`
	WorkspaceID string  `json:"workspaceId"`
	BaseCommit  string  `json:"baseCommit"`
	CreatedAt   float64 `json:"createdAt"`
	LastUsedAt  float64 `json:"lastUsedAt"`
}

type CreateWorktreeRequest struct {
	ProjectRoot  string  `json:"projectRoot"`
	CarryChanges bool    `json:"carryChanges"`
	Branch       *string `json:"branch,omitzero"`
}

type WorktreeRelease string

const (
	ReleaseReturnedToLocal WorktreeRelease = "returned-to-local"
	ReleaseDeleted         WorktreeRelease = "deleted"
)

type ReleaseWorktreeRequest struct {
	WorktreeID  string          `json:"worktreeId"`
	Root        string          `json:"root"`
	TaskID      *string         `json:"taskId"`
	Title       string          `json:"title"`
	Release     WorktreeRelease `json:"release"`
	MissingOnly *bool           `json:"missingOnly,omitzero"`
}

// WorktreeSnapshot is `WorktreeSnapshotResult`.
type WorktreeSnapshot struct {
	Commit      *string `json:"commit"`
	ShortCommit *string `json:"shortCommit"`
	Ref         *string `json:"ref"`
}
