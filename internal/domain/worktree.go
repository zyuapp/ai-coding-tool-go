package domain

import (
	"math"
	"regexp"
	"strings"
	"unicode/utf16"
)

const SnapshotRefNamespace = "refs/aicodingtool"

func WorktreeRef(worktreeID string) string {
	return SnapshotRefNamespace + "/" + worktreeID
}

var releaseReasons = map[WorktreeRelease]string{
	WorktreeReleaseReturnedToLocal: "returned to local",
	WorktreeReleaseDeleted:         "deleted manually",
}

func ReleaseReason(release WorktreeRelease) string {
	return releaseReasons[release]
}

// SnapshotMessage is the commit message of a worktree snapshot. An empty taskID or ref is null.
func SnapshotMessage(title string, taskID string, release WorktreeRelease, ref string) string {
	thread := "No thread"
	if taskID != "" {
		thread = "Thread " + taskID
	}
	lines := []string{"aicodingtool: snapshot \"" + title + "\"", "", thread + " · " + ReleaseReason(release)}
	if ref != "" {
		lines = append(lines, "Ref "+ref)
	}
	return strings.Join(lines, "\n")
}

const slugLimit = 24

var worktreeIDPattern = regexp.MustCompile(`-([0-9a-f]{8})$`)

var nonSlugChars = regexp.MustCompile(`[^a-z0-9]+`)

var nonIDChars = regexp.MustCompile(`[^A-Za-z0-9-]+`)

// nonEmptySegments is path.split("/").filter(Boolean).
func nonEmptySegments(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' })
}

// WorktreeDirectoryName is the project's name, slugged, plus the worktree's id.
func WorktreeDirectoryName(projectRoot string, worktreeID string) string {
	name := "project"
	if segments := nonEmptySegments(projectRoot); len(segments) > 0 {
		name = segments[len(segments)-1]
	}
	slug := strings.TrimSuffix(strings.TrimPrefix(nonSlugChars.ReplaceAllString(strings.ToLower(name), "-"), "-"), "-")
	slug = slug[:min(len(slug), slugLimit)]
	if slug == "" {
		slug = "project"
	}
	return slug + "-" + worktreeID
}

func WorktreeIDFromDirectoryName(name string) string {
	if match := worktreeIDPattern.FindStringSubmatch(name); match != nil {
		return match[1]
	}
	return nonIDChars.ReplaceAllString(name, "-")
}

// IsWorktree checks a value decoded from JSON.
func IsWorktree(value any) bool {
	worktree, ok := jsRecord(value)
	if !ok {
		return false
	}
	name, named := worktree["name"]
	return worktreeNonEmptyString(worktree["id"]) &&
		(!named || worktreeNonEmptyString(name)) &&
		worktreeNonEmptyString(worktree["projectId"]) &&
		worktreeNonEmptyString(worktree["root"]) &&
		worktreeNonEmptyString(worktree["workspaceId"]) &&
		worktreeNonEmptyString(worktree["baseCommit"]) &&
		worktreeFiniteNumber(worktree["createdAt"]) &&
		worktreeFiniteNumber(worktree["lastUsedAt"])
}

// WorktreeName is a saved display name, with the directory name as the fallback for older records.
func WorktreeName(worktree *Worktree) string {
	if worktree.Name != nil {
		return *worktree.Name
	}
	if segments := nonEmptySegments(worktree.Root); len(segments) > 0 {
		return segments[len(segments)-1]
	}
	return worktree.ID
}

func worktreeNonEmptyString(value any) bool {
	return projectNonEmptyString(value)
}

func worktreeFiniteNumber(value any) bool {
	return projectFiniteNumber(value)
}

const WorktreeHues = 6

// WorktreeHue is the colour a checkout's mark carries, from its id.
func WorktreeHue(worktreeID string) int {
	var hash int32
	for _, unit := range utf16.Encode([]rune(worktreeID)) {
		hash = hash*31 + int32(unit)
	}
	return int(math.Abs(float64(hash))) % WorktreeHues
}
