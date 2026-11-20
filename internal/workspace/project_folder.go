package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ProjectFolder checks a folder the user typed rather than picked: that it is a directory, and that
// it is theirs rather than a checkout the app made.
func ProjectFolder(root string, worktreesRoots []string) (string, error) {
	named := strings.TrimSpace(root)
	if named == "" {
		return "", errors.New("Name a folder to open.")
	}
	expanded := named
	if named == "~" || strings.HasPrefix(named, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, named[1:])
		}
	}
	if !filepath.IsAbs(expanded) {
		return "", errors.New("Name the folder by its full path.")
	}
	if !projectRootsAreOwn([]string{expanded}, worktreesRoots) {
		return "", errors.New("That folder is a checkout this app made. Name the repository it was cut from.")
	}
	info, err := os.Stat(expanded)
	if err != nil {
		return "", errors.New("There is no folder at " + expanded + ".")
	}
	if !info.IsDir() {
		return "", errors.New(expanded + " is a file, not a folder.")
	}
	return expanded, nil
}

// projectRootsAreOwn ports task-database's check: no root sits inside a worktrees root.
func projectRootsAreOwn(roots []string, worktreesRoots []string) bool {
	for _, root := range roots {
		resolved := absolute(root)
		for _, owned := range worktreesRoots {
			if strings.HasPrefix(resolved, absolute(owned)+string(filepath.Separator)) {
				return false
			}
		}
	}
	return true
}
