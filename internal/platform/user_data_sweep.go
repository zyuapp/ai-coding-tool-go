package platform

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const contextSuffix = ".context.json"

// OrphanAttachmentMinAge is how long an attachment nothing names stays: a composer or a paired device
// may still be about to send it.
const OrphanAttachmentMinAge = 7 * 24 * time.Hour

// SweepResult counts what a sweep removed.
type SweepResult struct {
	Files int
	Bytes int64
}

var savedAttachmentName = regexp.MustCompile(`^[A-Za-z0-9-]+\.png$`)

// SweepOrphanAttachments removes attachments no stored message names, with their context sidecars,
// once they are older than anything still in flight could be. A sidecar whose image is gone goes the
// same way.
func SweepOrphanAttachments(directory string, referenced map[string]bool, now time.Time, minAge time.Duration) (SweepResult, error) {
	var result SweepResult
	entries, err := os.ReadDir(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		image := strings.TrimSuffix(name, contextSuffix)
		if !savedAttachmentName.MatchString(image) || referenced[image] {
			continue
		}
		file := filepath.Join(directory, name)
		info, err := os.Stat(file)
		if err != nil || !info.Mode().IsRegular() || info.ModTime().After(now.Add(-minAge)) {
			continue
		}
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return result, err
		}
		result.Files++
		result.Bytes += info.Size()
	}
	return result, nil
}

// AttachmentNames is what each stored message names, by file name, so the sweep can compare against the directory.
func AttachmentNames(paths []string) map[string]bool {
	names := make(map[string]bool, len(paths))
	for _, file := range paths {
		names[filepath.Base(file)] = true
	}
	return names
}

// RetireLegacyCodexHome hands the Codex home a reverted build once gave Codex under the app's data
// folder to `discard`, such as TrashItem. Nothing has read it since the private home replaced it.
func RetireLegacyCodexHome(userData string, discard func(target string) error) (bool, error) {
	legacy := filepath.Join(userData, "codex")
	info, err := os.Stat(legacy)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	if err := discard(legacy); err != nil {
		return false, err
	}
	return true, nil
}
