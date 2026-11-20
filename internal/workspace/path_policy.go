package workspace

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf16"
)

// Bigger than any path anyone writes, and small enough that nothing chokes on the argument.
const maxFilePath = 4_096

// jsLength is a string's length in UTF-16 code units, as JavaScript counts it.
func jsLength(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func homeRelative(candidate string) string {
	if strings.HasPrefix(candidate, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, candidate[2:])
		}
	}
	return candidate
}

// FileInCheckout is the file a message named, as an absolute path the desktop can open. A message is
// only prose, so the path it wrote has to land inside the checkout the thread works in.
func FileInCheckout(root, candidate string) (string, error) {
	if root == "" {
		return "", errors.New("Invalid folder.")
	}
	if candidate == "" || jsLength(candidate) > maxFilePath {
		return "", errors.New("Invalid file path.")
	}
	named := homeRelative(candidate)
	checkout, err := realpath(root)
	if err != nil {
		return "", missingFile(nodeFSError("realpath", root, err))
	}
	file, err := realpath(absolute(root, named))
	if err != nil {
		return "", missingFile(nodeFSError("realpath", absolute(root, named), err))
	}
	if !IsPathInside(checkout, file) {
		return "", errors.New("That file is outside this thread's folder.")
	}
	return file, nil
}

func missingFile(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("That file is not there any more.")
	}
	return err
}

func IsPathInside(root, candidate string) bool {
	resolvedRoot := absolute(root)
	resolvedCandidate := absolute(candidate)
	return resolvedCandidate == resolvedRoot || strings.HasPrefix(resolvedCandidate, resolvedRoot+string(filepath.Separator))
}

// canonicalWritePath resolves the nearest existing ancestor through symlinks and keeps the rest as written.
func canonicalWritePath(candidate string) (string, bool) {
	pending := absolute(candidate)
	var suffix []string
	for {
		existing, err := realpath(pending)
		if err == nil {
			for index := len(suffix) - 1; index >= 0; index-- {
				existing = filepath.Join(existing, suffix[index])
			}
			return existing, true
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", false
		}
		parent := filepath.Dir(pending)
		if parent == pending {
			return "", false
		}
		suffix = append(suffix, filepath.Base(pending))
		pending = parent
	}
}

func IsWritePathInside(root, candidate string) bool {
	rooted := candidate
	if !filepath.IsAbs(candidate) {
		rooted = absolute(root, candidate)
	}
	canonicalRoot, rootOK := canonicalWritePath(root)
	canonicalCandidate, candidateOK := canonicalWritePath(rooted)
	return rootOK && candidateOK && IsPathInside(canonicalRoot, canonicalCandidate)
}

// How many places one link is allowed to be looked for, so a deep path cannot fan out without end.
const maxFileCandidates = 256

var (
	windowsDrive = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
	pathSplitter = regexp.MustCompile(`[\\/]+`)
)

// fileCandidates are the places a written path might really be, nearest the thread first. A path
// that exists as written is that file; anything else is repaired by re-anchoring its tail on each checkout.
func fileCandidates(roots []string, named string) []string {
	var segments []string
	for _, part := range pathSplitter.Split(named, -1) {
		if part != "" && part != "." {
			segments = append(segments, part)
		}
	}
	var candidates []string
	if filepath.IsAbs(named) || windowsDrive.MatchString(named) {
		candidates = append(candidates, absolute(named))
	} else {
		for _, root := range roots {
			candidates = append(candidates, absolute(root, named))
		}
	}
	for _, root := range roots {
		for index := 1; index < len(segments); index++ {
			candidates = append(candidates, absolute(root, strings.Join(segments[index:], string(filepath.Separator))))
		}
	}
	seen := map[string]bool{}
	unique := []string{}
	for _, candidate := range candidates {
		if !seen[candidate] {
			seen[candidate] = true
			unique = append(unique, candidate)
		}
	}
	return unique[:min(len(unique), maxFileCandidates)]
}

// OpenableFile is the file a link names, as an absolute path the desktop can open. It is looked for in
// the thread's checkout, the project's, and the project's other checkouts, before being taken as
// written. A file outside all of them still opens.
func OpenableFile(roots []string, candidate string) (string, error) {
	for _, root := range roots {
		if root == "" {
			return "", errors.New("Invalid folder.")
		}
	}
	if candidate == "" || jsLength(candidate) > maxFilePath {
		return "", errors.New("Invalid file path.")
	}
	for _, target := range fileCandidates(roots, homeRelative(candidate)) {
		if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
			if resolved, err := realpath(target); err == nil {
				return resolved, nil
			}
			return target, nil
		}
	}
	return "", errors.New("AI Coding Tool could not find " + candidate + " on this machine.")
}
