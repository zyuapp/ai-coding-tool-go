package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileEditsStayInsideTheSelectedProject(t *testing.T) {
	cases := map[string]bool{
		"/tmp/project/file.txt":      true,
		"/tmp/project":               true,
		"/tmp/project-copy/file.txt": false,
		"/tmp/elsewhere/file.txt":    false,
	}
	for candidate, want := range cases {
		if IsPathInside("/tmp/project", candidate) != want {
			t.Fatal(candidate)
		}
	}
}

func TestCanonicalWriteTargetsStayInsideTheSelectedProject(t *testing.T) {
	root := tempDir(t)
	outside := tempDir(t)
	outsideFile := filepath.Join(outside, "outside.txt")
	targetLink := filepath.Join(root, "target-link")
	parentLink := filepath.Join(root, "parent-link")
	loopA := filepath.Join(root, "loop-a")
	loopB := filepath.Join(root, "loop-b")
	mkdir(t, filepath.Join(root, "src"))
	writeFile(t, outsideFile, "outside")
	for link, target := range map[string]string{targetLink: outsideFile, parentLink: outside, loopA: loopB, loopB: loopA} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		candidate string
		want      bool
	}{
		{filepath.Join(root, "src", "file.txt"), true},
		{"src/new-file.txt", true},
		{"../outside.txt", false},
		{filepath.Join(filepath.Dir(root), filepath.Base(root)+"-copy", "file.txt"), false},
		{filepath.Join(root, "src") + "/../../outside.txt", false},
		{targetLink, false},
		{filepath.Join(parentLink, "new.txt"), false},
		{filepath.Join(loopA, "new.txt"), false},
	}
	for _, c := range cases {
		if got := IsWritePathInside(root, c.candidate); got != c.want {
			t.Fatal(c.candidate, got)
		}
	}
}

func TestFileAMessageNamedIsLookedForInEveryCheckout(t *testing.T) {
	home := tempDir(t)
	project := filepath.Join(home, "repo")
	worktree := filepath.Join(home, "repo-w1")
	outside := filepath.Join(home, "notes")
	writeFile(t, filepath.Join(project, "src", "App.tsx"), "project")
	writeFile(t, filepath.Join(worktree, "src", "App.tsx"), "worktree")
	writeFile(t, filepath.Join(worktree, "only-here.ts"), "worktree")
	writeFile(t, filepath.Join(outside, "plan.md"), "outside")

	cases := []struct {
		roots     []string
		candidate string
		want      string
	}{
		{[]string{worktree, project}, "src/App.tsx", filepath.Join(worktree, "src", "App.tsx")},
		{[]string{project, worktree}, "only-here.ts", filepath.Join(worktree, "only-here.ts")},
		{[]string{worktree, project}, filepath.Join(project, "src", "App.tsx"), filepath.Join(project, "src", "App.tsx")},
		{[]string{worktree}, filepath.Join(home, "gone", "src", "App.tsx"), filepath.Join(worktree, "src", "App.tsx")},
		{[]string{project}, filepath.Join(outside, "plan.md"), filepath.Join(outside, "plan.md")},
		{[]string{project}, "repo/src/App.tsx", filepath.Join(project, "src", "App.tsx")},
	}
	for _, c := range cases {
		if got := must(OpenableFile(c.roots, c.candidate)); got != c.want {
			t.Fatal(c.candidate, got)
		}
	}
	for candidate, want := range map[string]string{"src/Missing.tsx": "could not find src/Missing.tsx", "src": "could not find src", "": "Invalid file path"} {
		if _, err := OpenableFile([]string{project}, candidate); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatal(candidate, err)
		}
	}
	if _, err := OpenableFile([]string{""}, "src/App.tsx"); err == nil || err.Error() != "Invalid folder." {
		t.Fatal(err)
	}
}

func TestFileInCheckoutStaysInTheCheckout(t *testing.T) {
	root := tempDir(t)
	outside := tempDir(t)
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(outside, "b.txt"), "b")
	if got := must(FileInCheckout(root, "a.txt")); got != filepath.Join(root, "a.txt") {
		t.Fatal(got)
	}
	if got := must(FileInCheckout(root, ".")); got != root {
		t.Fatal(got)
	}
	if _, err := FileInCheckout(root, filepath.Join(outside, "b.txt")); err == nil || err.Error() != "That file is outside this thread's folder." {
		t.Fatal(err)
	}
	if _, err := FileInCheckout(root, "gone.txt"); err == nil || err.Error() != "That file is not there any more." {
		t.Fatal(err)
	}
}

func TestProjectFolderRefusesWhatThePickerWouldNotOffer(t *testing.T) {
	directory := tempDir(t)
	worktrees := filepath.Join(directory, "worktrees")
	mkdir(t, filepath.Join(worktrees, "repo-12345678"))
	writeFile(t, filepath.Join(directory, "file.txt"), "")
	cases := map[string]string{
		"   ":           "Name a folder to open.",
		"relative/path": "Name the folder by its full path.",
		filepath.Join(worktrees, "repo-12345678"): "That folder is a checkout this app made. Name the repository it was cut from.",
		filepath.Join(directory, "nowhere"):       "There is no folder at " + filepath.Join(directory, "nowhere") + ".",
		filepath.Join(directory, "file.txt"):      filepath.Join(directory, "file.txt") + " is a file, not a folder.",
	}
	for root, want := range cases {
		if _, err := ProjectFolder(root, []string{worktrees}); err == nil || err.Error() != want {
			t.Fatal(root, err)
		}
	}
	if got := must(ProjectFolder("  "+directory+"  ", []string{worktrees})); got != directory {
		t.Fatal(got)
	}
	home, _ := os.UserHomeDir()
	if got := must(ProjectFolder("~", nil)); got != home {
		t.Fatal(got)
	}
}
