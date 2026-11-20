package platform

import (
	"os"
	"os/exec"
	"testing"
)

// holdSearchPath replaces the held search path for one test.
func holdSearchPath(t *testing.T, value string) {
	t.Helper()
	heldSearchPath()
	searchPath.Lock()
	previous := searchPath.value
	searchPath.value = value
	searchPath.Unlock()
	t.Cleanup(func() {
		searchPath.Lock()
		searchPath.value = previous
		searchPath.Unlock()
	})
}

// bystander is a process that outlives the test unless ended, standing in for an app or server on the same data.
func bystander(t *testing.T) *exec.Cmd {
	t.Helper()
	command := exec.Command("sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _, _ = command.Process.Wait() })
	return command
}

func ended(t *testing.T, command *exec.Cmd) {
	t.Helper()
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}
