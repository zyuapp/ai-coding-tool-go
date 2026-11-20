package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
)

type serviceSetup struct {
	directory string
	options   WorkspaceServiceOptions
}

func setupService(t *testing.T) serviceSetup {
	directory := tempDir(t)
	return serviceSetup{directory, WorkspaceServiceOptions{RegistryPath: filepath.Join(directory, "state", "workspaces.json"), ProjectlessRoot: filepath.Join(directory, "scratch")}}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func isUnknown(err error) bool {
	var unknown *UnknownWorkspaceError
	return errors.As(err, &unknown)
}

func TestRegistersCanonicalProjectsAndReusesTheirID(t *testing.T) {
	s := setupService(t)
	project := filepath.Join(s.directory, "project")
	alias := filepath.Join(s.directory, "project-alias")
	mkdir(t, project)
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	service := NewWorkspaceService(s.options)
	first := must(service.RegisterProject(alias))
	second := must(service.RegisterProject(project))
	if first.Status != ResolutionAvailable || second.Status != ResolutionAvailable || first.Workspace.ID != second.Workspace.ID || first.Workspace.Root != project {
		t.Fatalf("%#v %#v", first, second)
	}
	if !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(first.Workspace.ID) {
		t.Fatal(first.Workspace.ID)
	}
}

func TestReloadsTheRegistryAfterRestart(t *testing.T) {
	s := setupService(t)
	project := filepath.Join(s.directory, "project")
	mkdir(t, project)
	registered := must(NewWorkspaceService(s.options).RegisterProject(project))
	resolved := must(NewWorkspaceService(s.options).Resolve(registered.Workspace.ID))
	if !reflect.DeepEqual(resolved, registered) {
		t.Fatal(resolved, registered)
	}
	content := readFile(t, s.options.RegistryPath)
	want := "{\n  \"version\": 1,\n  \"workspaces\": [\n    {\n      \"id\": \"" + registered.Workspace.ID + "\",\n      \"kind\": \"project\",\n      \"root\": \"" + project + "\"\n    }\n  ]\n}\n"
	if content != want {
		t.Fatalf("written as JSON.stringify(registry, null, 2):\n%s", content)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.options.RegistryPath))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatal("temporary file left behind")
		}
	}
}

func TestSerializesConcurrentRegistrations(t *testing.T) {
	s := setupService(t)
	roots := []string{filepath.Join(s.directory, "first"), filepath.Join(s.directory, "second")}
	service := NewWorkspaceService(s.options)
	registered := make([]WorkspaceResolution, len(roots))
	var wg sync.WaitGroup
	for index, root := range roots {
		mkdir(t, root)
		wg.Go(func() { registered[index] = must(service.RegisterProject(root)) })
	}
	wg.Wait()
	reloaded := NewWorkspaceService(s.options)
	for _, item := range registered {
		if resolved := must(reloaded.Resolve(item.Workspace.ID)); !reflect.DeepEqual(resolved, item) {
			t.Fatal(resolved, item)
		}
	}
}

func TestForgetsAWorktreeButKeepsAnotherKindAtTheSameRoot(t *testing.T) {
	s := setupService(t)
	firstRoot := filepath.Join(s.directory, "first-worktree")
	secondRoot := filepath.Join(s.directory, "second-worktree")
	mkdir(t, firstRoot)
	mkdir(t, secondRoot)
	service := NewWorkspaceService(s.options)
	project := must(service.RegisterProject(firstRoot))
	first := must(service.RegisterWorktree(firstRoot))
	second := must(service.RegisterWorktree(secondRoot))
	if err := service.ForgetWorktree(first.Workspace.Root); err != nil {
		t.Fatal(err)
	}
	if got := must(service.Resolve(project.Workspace.ID)); !reflect.DeepEqual(got, project) {
		t.Fatal("a project at the same root is not a worktree registration")
	}
	if got := must(service.Resolve(second.Workspace.ID)); !reflect.DeepEqual(got, second) {
		t.Fatal(got)
	}
	if _, err := service.Resolve(first.Workspace.ID); !isUnknown(err) {
		t.Fatal(err)
	}
}

func TestConcurrentForgetsAllReachTheSavedRegistry(t *testing.T) {
	s := setupService(t)
	service := NewWorkspaceService(s.options)
	var registered []WorkspaceResolution
	for index := range 8 {
		root := filepath.Join(s.directory, "worktree-"+string(rune('0'+index)))
		mkdir(t, root)
		registered = append(registered, must(service.RegisterWorktree(root)))
	}
	var wg sync.WaitGroup
	for _, item := range registered {
		wg.Go(func() {
			if err := service.ForgetWorktree(item.Workspace.Root); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	reloaded := NewWorkspaceService(s.options)
	for _, item := range registered {
		if _, err := reloaded.Resolve(item.Workspace.ID); !isUnknown(err) {
			t.Fatal(err)
		}
	}
}

func TestRollsBackARegistrationWhenPersistenceFails(t *testing.T) {
	s := setupService(t)
	project := filepath.Join(s.directory, "project")
	mkdir(t, project)
	service := NewWorkspaceService(s.options)
	if _, err := service.Resolve("probe"); !isUnknown(err) {
		t.Fatal(err)
	}
	mkdir(t, s.options.RegistryPath)
	if _, err := service.RegisterProject(project); err == nil {
		t.Fatal("expected the write to fail")
	}
	if err := os.Remove(s.options.RegistryPath); err != nil {
		t.Fatal(err)
	}
	registered := must(service.RegisterProject(project))
	if got := must(NewWorkspaceService(s.options).Resolve(registered.Workspace.ID)); !reflect.DeepEqual(got, registered) {
		t.Fatal(got)
	}
}

func preservedRegistry(t *testing.T, registryPath string) string {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Dir(registryPath))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "workspaces.json.corrupt.") && strings.HasSuffix(entry.Name(), ".json") {
			return readFile(t, filepath.Join(filepath.Dir(registryPath), entry.Name()))
		}
	}
	t.Fatal("nothing preserved")
	return ""
}

func TestPreservesAMalformedRegistryAndRecovers(t *testing.T) {
	for _, malformed := range []string{"{not-json", `{"version":1,"workspaces":[{"id":"bad","root":"","kind":"project"}]}`} {
		s := setupService(t)
		project := filepath.Join(s.directory, "project")
		mkdir(t, project)
		writeFile(t, s.options.RegistryPath, malformed)
		registered := must(NewWorkspaceService(s.options).RegisterProject(project))
		if registered.Status != ResolutionAvailable {
			t.Fatal(registered)
		}
		if got := preservedRegistry(t, s.options.RegistryPath); got != malformed {
			t.Fatal(got)
		}
		if !strings.Contains(readFile(t, s.options.RegistryPath), registered.Workspace.ID) {
			t.Fatal("new registry not written")
		}
	}
}

func TestCreatesAndReloadsTheProjectlessWorkspace(t *testing.T) {
	s := setupService(t)
	registered := must(NewWorkspaceService(s.options).GetProjectless())
	reloaded := must(NewWorkspaceService(s.options).GetProjectless())
	if registered.Workspace.Kind != WorkspaceProjectless || registered.Workspace.Root != s.options.ProjectlessRoot || reloaded.Workspace.ID != registered.Workspace.ID {
		t.Fatal(registered, reloaded)
	}
}

func TestUnknownIDsAndMissingRoots(t *testing.T) {
	s := setupService(t)
	project := filepath.Join(s.directory, "project")
	mkdir(t, project)
	service := NewWorkspaceService(s.options)
	registered := must(service.RegisterProject(project))
	if err := os.Rename(project, project+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve("unknown-workspace"); !isUnknown(err) {
		t.Fatal(err)
	}
	missing := must(service.Resolve(registered.Workspace.ID))
	if missing.Status != ResolutionUnavailable || missing.Reason != ReasonMissing || missing.Workspace.ID != registered.Workspace.ID {
		t.Fatal(missing)
	}
	if got := encode(t, missing); !strings.HasSuffix(got, `"reason":"missing"}`) || !strings.HasPrefix(got, `{"status":"unavailable","workspace":{`) {
		t.Fatal(got)
	}
}

func TestDetectsRootsReplacedByASymlinkOrAFile(t *testing.T) {
	s := setupService(t)
	linked := filepath.Join(s.directory, "linked-project")
	replacement := filepath.Join(s.directory, "replacement")
	fileProject := filepath.Join(s.directory, "file-project")
	mkdir(t, linked)
	mkdir(t, replacement)
	mkdir(t, fileProject)
	service := NewWorkspaceService(s.options)
	linkedRecord := must(service.RegisterProject(linked))
	fileRecord := must(service.RegisterProject(fileProject))
	if err := os.Rename(linked, linked+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, linked); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(fileProject)
	writeFile(t, fileProject, "not a directory")

	if changed := must(service.Resolve(linkedRecord.Workspace.ID)); changed.Reason != ReasonChanged {
		t.Fatal(changed)
	}
	if notDirectory := must(service.Resolve(fileRecord.Workspace.ID)); notDirectory.Reason != ReasonNotDirectory {
		t.Fatal(notDirectory)
	}
}

func TestRegisteringAMissingFolderNamesWhy(t *testing.T) {
	s := setupService(t)
	missing := filepath.Join(s.directory, "nowhere")
	_, err := NewWorkspaceService(s.options).RegisterProject(missing)
	if err == nil || err.Error() != "Cannot register workspace (missing): "+missing {
		t.Fatal(err)
	}
}
