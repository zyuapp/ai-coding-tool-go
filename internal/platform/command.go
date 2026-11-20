package platform

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// childEnv holds the variables every child gets on top of this process's own, besides the search path.
var childEnv = struct {
	sync.RWMutex
	keys   []string
	values map[string]string
}{values: map[string]string{}}

// SetChildEnv gives every child started from now on `key`, without changing this process's own environment.
func SetChildEnv(key, value string) {
	childEnv.Lock()
	defer childEnv.Unlock()
	if _, ok := childEnv.values[key]; !ok {
		childEnv.keys = append(childEnv.keys, key)
	}
	childEnv.values[key] = value
}

func childVariables() []string {
	childEnv.RLock()
	defer childEnv.RUnlock()
	variables := make([]string, 0, len(childEnv.keys))
	for _, key := range childEnv.keys {
		variables = append(variables, key+"="+childEnv.values[key])
	}
	return variables
}

// ChildEnv is this process's environment as its children see it: its own, then the search path, then
// the app's child variables, each later entry replacing an earlier one with the same key.
func ChildEnv() []string {
	return overlayEnv(os.Environ(), append([]string{"PATH=" + SearchPath()}, childVariables()...))
}

// Environment is ChildEnv as a map.
func Environment() map[string]string {
	environment := map[string]string{}
	for _, entry := range ChildEnv() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			environment[key] = value
		}
	}
	return environment
}

func overlayEnv(base []string, overrides []string) []string {
	replaced := map[string]bool{}
	for _, entry := range overrides {
		key, _, _ := strings.Cut(entry, "=")
		replaced[key] = true
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !replaced[key] {
			result = append(result, entry)
		}
	}
	return append(result, overrides...)
}

// LookPath finds a bare command name on the search path children get, as a shell finds it. A name
// with a separator in it is returned as given.
func LookPath(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return name, nil
	}
	for folder := range strings.SplitSeq(SearchPath(), string(os.PathListSeparator)) {
		if folder == "" {
			continue
		}
		candidate := filepath.Join(folder, name)
		if executable(candidate) {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

// Command is how every child process starts: `name` is found on the search path the user's login
// shell reported, and the child gets that path and the app's child variables. Callers may append to
// Env; a later entry wins over an earlier one with the same key. A login-shell lookup still running
// is waited for first.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	awaitSearchPath(ctx)
	command := exec.CommandContext(ctx, name, args...)
	if found, err := LookPath(name); err == nil {
		command.Path = found
		command.Err = nil
	} else if !strings.ContainsRune(name, filepath.Separator) {
		command.Path = name
		command.Err = err
	}
	command.Args[0] = name
	command.Env = ChildEnv()
	return command
}

// boundedBuffer keeps at most `limit` bytes and reports on `full` once more arrived.
type boundedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	overflow bool
	full     chan struct{}
	once     sync.Once
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	room := b.limit - b.buffer.Len()
	if len(data) <= room {
		b.buffer.Write(data)
		return len(data), nil
	}
	if room > 0 {
		b.buffer.Write(data[:room])
	}
	b.overflow = true
	b.once.Do(func() { close(b.full) })
	return len(data), nil
}

func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.buffer.Bytes())
}

func (b *boundedBuffer) String() string {
	return string(b.Bytes())
}

func (b *boundedBuffer) overflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{limit: limit, full: make(chan struct{})}
}
