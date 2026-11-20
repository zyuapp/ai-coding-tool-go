package workspace

import (
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitT(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func initRepository(t *testing.T, root string) {
	t.Helper()
	gitT(t, root, "init", "-b", "main")
	gitT(t, root, "config", "user.email", "tests@example.com")
	gitT(t, root, "config", "user.name", "AI Coding Tool Tests")
	gitT(t, root, "config", "commit.gpgsign", "false")
}

// repository is a fresh repository with one commit holding tracked.txt.
func repository(t *testing.T, tracked string) string {
	t.Helper()
	root := tempDir(t)
	initRepository(t, root)
	writeFile(t, filepath.Join(root, "tracked.txt"), tracked)
	gitT(t, root, "add", "tracked.txt")
	gitT(t, root, "commit", "-m", "initial")
	return root
}

type resolverFunc func(id string) (WorkspaceResolution, error)

func (f resolverFunc) Resolve(id string) (WorkspaceResolution, error) { return f(id) }

func fixture(root string) Resolver {
	return resolverFunc(func(string) (WorkspaceResolution, error) {
		return WorkspaceResolution{Status: ResolutionAvailable, Workspace: WorkspaceRecord{ID: "fixture", Kind: WorkspaceProject, Root: root}}, nil
	})
}

func encode(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var bg = context.Background()

func ptr[T any](value T) *T { return &value }
