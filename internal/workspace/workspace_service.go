package workspace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
)

// Resolver finds a registered workspace by id.
type Resolver interface {
	Resolve(id string) (WorkspaceResolution, error)
}

type UnknownWorkspaceError struct{ WorkspaceID string }

func (e *UnknownWorkspaceError) Error() string { return "Unknown workspace: " + e.WorkspaceID }

type WorkspaceRegistrationError struct {
	Root    string
	Message string
}

func (e *WorkspaceRegistrationError) Error() string { return e.Message }

type registryFile struct {
	Version    float64           `json:"version"`
	Workspaces []WorkspaceRecord `json:"workspaces"`
}

type WorkspaceServiceOptions struct {
	RegistryPath    string
	ProjectlessRoot string
}

// WorkspaceService keeps the registry of folders the app works in, each under an opaque id that
// survives restarts, saved to one JSON file replaced atomically.
type WorkspaceService struct {
	registryPath    string
	projectlessRoot string
	loadErr         error

	registration sync.Mutex
	writes       sync.Mutex
	mu           sync.RWMutex
	order        []string
	records      map[string]WorkspaceRecord
}

func NewWorkspaceService(options WorkspaceServiceOptions) *WorkspaceService {
	service := &WorkspaceService{
		registryPath:    options.RegistryPath,
		projectlessRoot: options.ProjectlessRoot,
		records:         map[string]WorkspaceRecord{},
	}
	service.loadErr = service.load()
	return service
}

func (s *WorkspaceService) RegisterProject(root string) (WorkspaceResolution, error) {
	return s.register(WorkspaceProject, root)
}

func (s *WorkspaceService) RegisterWorktree(root string) (WorkspaceResolution, error) {
	return s.register(WorkspaceWorktree, root)
}

func (s *WorkspaceService) GetProjectless() (WorkspaceResolution, error) {
	if s.loadErr != nil {
		return WorkspaceResolution{}, s.loadErr
	}
	if err := os.MkdirAll(s.projectlessRoot, 0o777); err != nil {
		return WorkspaceResolution{}, nodeFSError("mkdir", s.projectlessRoot, err)
	}
	return s.register(WorkspaceProjectless, s.projectlessRoot)
}

// ForgetWorktree drops a worktree's registration once its directory is gone, so the registry never outgrows the disk.
func (s *WorkspaceService) ForgetWorktree(root string) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	canonical := absolute(root)
	s.mu.Lock()
	removed := false
	for _, id := range slices.Clone(s.order) {
		if record := s.records[id]; record.Kind == WorkspaceWorktree && record.Root == canonical {
			s.remove(id)
			removed = true
		}
	}
	s.mu.Unlock()
	if !removed {
		return nil
	}
	return s.writeRegistry()
}

func (s *WorkspaceService) Resolve(id string) (WorkspaceResolution, error) {
	if s.loadErr != nil {
		return WorkspaceResolution{}, s.loadErr
	}
	s.mu.RLock()
	record, ok := s.records[id]
	s.mu.RUnlock()
	if !ok {
		return WorkspaceResolution{}, &UnknownWorkspaceError{WorkspaceID: id}
	}
	unavailable := func(reason UnavailableReason) (WorkspaceResolution, error) {
		return WorkspaceResolution{Status: ResolutionUnavailable, Workspace: record, Reason: reason}, nil
	}
	actual, err := realpath(record.Root)
	if err != nil {
		return unavailable(unavailableReason(err))
	}
	if actual != record.Root {
		return unavailable(ReasonChanged)
	}
	info, err := os.Stat(actual)
	if err != nil {
		return unavailable(unavailableReason(err))
	}
	if !info.IsDir() {
		return unavailable(ReasonNotDirectory)
	}
	return WorkspaceResolution{Status: ResolutionAvailable, Workspace: record}, nil
}

func (s *WorkspaceService) register(kind WorkspaceKind, root string) (WorkspaceResolution, error) {
	if s.loadErr != nil {
		return WorkspaceResolution{}, s.loadErr
	}
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return WorkspaceResolution{}, err
	}
	s.registration.Lock()
	defer s.registration.Unlock()
	s.mu.Lock()
	for _, id := range s.order {
		if record := s.records[id]; record.Kind == kind && record.Root == canonical {
			s.mu.Unlock()
			return WorkspaceResolution{Status: ResolutionAvailable, Workspace: record}, nil
		}
	}
	record := WorkspaceRecord{ID: randomUUID(), Kind: kind, Root: canonical}
	s.records[record.ID] = record
	s.order = append(s.order, record.ID)
	s.mu.Unlock()
	if err := s.writeRegistry(); err != nil {
		s.mu.Lock()
		s.remove(record.ID)
		s.mu.Unlock()
		return WorkspaceResolution{}, err
	}
	return WorkspaceResolution{Status: ResolutionAvailable, Workspace: record}, nil
}

// remove runs with mu held.
func (s *WorkspaceService) remove(id string) {
	delete(s.records, id)
	s.order = slices.DeleteFunc(slices.Clone(s.order), func(candidate string) bool { return candidate == id })
}

func (s *WorkspaceService) load() error {
	content, err := os.ReadFile(s.registryPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return nodeFSError("open", s.registryPath, err)
	}
	var parsed any
	if err := json.Unmarshal(content, &parsed, jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true)); err != nil {
		return preserveCorruptRegistry(s.registryPath, content)
	}
	records, ok := registryRecords(parsed)
	if !ok {
		return preserveCorruptRegistry(s.registryPath, content)
	}
	for _, record := range records {
		if _, seen := s.records[record.ID]; !seen {
			s.order = append(s.order, record.ID)
		}
		s.records[record.ID] = record
	}
	return nil
}

// writeRegistry writes one at a time, each with the records as they are when it starts, so the last write is the latest.
func (s *WorkspaceService) writeRegistry() error {
	s.writes.Lock()
	defer s.writes.Unlock()
	s.mu.RLock()
	registry := registryFile{Version: 1, Workspaces: make([]WorkspaceRecord, 0, len(s.order))}
	for _, id := range s.order {
		registry.Workspaces = append(registry.Workspaces, s.records[id])
	}
	s.mu.RUnlock()

	directory := filepath.Dir(s.registryPath)
	if err := os.MkdirAll(directory, 0o777); err != nil {
		return nodeFSError("mkdir", directory, err)
	}
	encoded, err := json.Marshal(registry, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	temporary := s.registryPath + "." + randomUUID() + ".tmp"
	if err := os.WriteFile(temporary, append(encoded, '\n'), 0o666); err != nil {
		_ = os.Remove(temporary)
		return nodeFSError("open", temporary, err)
	}
	if err := os.Rename(temporary, s.registryPath); err != nil {
		// Best effort cleanup; the original registry stays if replacement failed.
		_ = os.Remove(temporary)
		return nodeFSError("rename", temporary, err)
	}
	return nil
}

func registryRecords(value any) ([]WorkspaceRecord, bool) {
	registry, ok := value.(map[string]any)
	if !ok || registry["version"] != float64(1) {
		return nil, false
	}
	list, ok := registry["workspaces"].([]any)
	if !ok {
		return nil, false
	}
	records := make([]WorkspaceRecord, 0, len(list))
	for _, item := range list {
		workspace, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		id, _ := workspace["id"].(string)
		root, _ := workspace["root"].(string)
		kind, _ := workspace["kind"].(string)
		if id == "" || root == "" || (kind != "project" && kind != "projectless" && kind != "worktree") {
			return nil, false
		}
		records = append(records, WorkspaceRecord{ID: id, Kind: WorkspaceKind(kind), Root: root})
	}
	return records, true
}

func preserveCorruptRegistry(registryPath string, content []byte) error {
	preserved := registryPath + ".corrupt." + randomUUID() + ".json"
	file, err := os.OpenFile(preserved, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return nodeFSError("open", preserved, err)
	}
	if _, err := file.Write(content); err != nil {
		file.Close()
		return nodeFSError("write", preserved, err)
	}
	return file.Close()
}

func canonicalDirectory(root string) (string, error) {
	canonical, err := realpath(root)
	if err == nil {
		info, statErr := os.Stat(canonical)
		if statErr == nil && !info.IsDir() {
			return "", &WorkspaceRegistrationError{Root: root, Message: "Cannot register workspace (not-directory): " + root}
		}
		err = statErr
	}
	if err != nil {
		return "", &WorkspaceRegistrationError{Root: root, Message: fmt.Sprintf("Cannot register workspace (%s): %s", unavailableReason(err), root)}
	}
	return canonical, nil
}

func unavailableReason(err error) UnavailableReason {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return ReasonMissing
	}
	return ReasonInaccessible
}

// realpath is Node's `fs.realpath`: an absolute path with every symlink resolved.
func realpath(target string) (string, error) {
	resolved, err := filepath.EvalSymlinks(absolute(target))
	if err != nil {
		return "", err
	}
	return absolute(resolved), nil
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
