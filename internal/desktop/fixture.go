package desktop

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
)

// Fixture stands in for the workspace runtime with one saved WorkspaceState: a snapshot request
// publishes it, and every other input publishes it again unchanged. Desktop calls answer with the
// empty or unavailable shape their types allow, and attachments are read from a folder.
type Fixture struct {
	state       jsontext.Value
	attachments string

	mu       sync.Mutex
	revision int
	views    map[int]WorkspaceView
	nextView int
}

// NewFixture loads a WorkspaceState saved as JSON. Attachments are read from attachmentsDir, if set.
func NewFixture(stateFile, attachmentsDir string) (*Fixture, error) {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	state := jsontext.Value(data)
	if err := state.Compact(); err != nil {
		return nil, fmt.Errorf("%s is not JSON: %w", stateFile, err)
	}
	return &Fixture{state: state, attachments: attachmentsDir, views: map[int]WorkspaceView{}}, nil
}

func (fixture *Fixture) publish() int {
	fixture.mu.Lock()
	fixture.revision++
	revision := fixture.revision
	views := make([]WorkspaceView, 0, len(fixture.views))
	for _, view := range fixture.views {
		views = append(views, view)
	}
	fixture.mu.Unlock()
	update := make(jsontext.Value, 0, len(fixture.state)+32)
	update = append(update, `{"revision":`...)
	update = strconv.AppendInt(update, int64(revision), 10)
	update = append(update, `,"state":`...)
	update = append(update, fixture.state...)
	update = append(update, '}')
	for _, view := range views {
		view.Update(update)
	}
	return revision
}

func (fixture *Fixture) Request(input jsontext.Value) Reply {
	revision := fixture.publish()
	return Settled(jsontext.Value(`{"ok":true,"revision":`+strconv.Itoa(revision)+`}`), nil)
}

func (fixture *Fixture) Migrate(values map[string]string) Reply { return Settled(nil, nil) }

func (fixture *Fixture) Subscribe(view WorkspaceView) func() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	id := fixture.nextView
	fixture.nextView++
	fixture.views[id] = view
	return func() {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		delete(fixture.views, id)
	}
}

func (fixture *Fixture) SetViewVisible(bool) {}
func (fixture *Fixture) SetViewInUse(bool)   {}

var errFixture = errors.New("Not available in the fixture.")

func fixtureValue(value any) Reply {
	data, err := json.Marshal(value)
	return Settled(data, err)
}

func (fixture *Fixture) Call(ctx context.Context, name string, args []jsontext.Value) Reply {
	switch name {
	case CallOpenFolder, CallTerminalSnapshot, CallReadRemoteTerminal, CallReadAttachmentContext:
		return Settled(jsontext.Value("null"), nil)
	case CallCommands:
		return fixtureValue(map[string]any{"status": "available", "commands": []any{}})
	case CallBranches:
		return fixtureValue(map[string]any{"status": "available", "branches": []string{}, "remotes": []string{}, "current": nil})
	case CallCommits:
		return fixtureValue([]string{})
	case CallDiffPatch:
		return fixtureValue(map[string]any{"status": "error", "message": errFixture.Error()})
	case CallSetBrowserBounds, CallFocusBrowserTab, CallWatchSimulator, CallUnwatchSimulator:
		return Settled(nil, nil)
	}
	return Settled(nil, errFixture)
}

var attachmentName = regexp.MustCompile(`^[A-Za-z0-9-]+\.png$`)

func (fixture *Fixture) Attachment(ctx context.Context, name, thread string) ([]byte, error) {
	if fixture.attachments == "" || !attachmentName.MatchString(name) {
		return nil, errFixture
	}
	return os.ReadFile(filepath.Join(fixture.attachments, name))
}

func (fixture *Fixture) MessageImage(ctx context.Context, query MessageImageQuery, thread string) ([]byte, string, error) {
	return nil, "", errFixture
}
