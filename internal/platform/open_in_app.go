package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ExternalApp is an application a checkout can be handed to, as the domain catalog lists it.
type ExternalApp struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

// AppCandidate is one place an application might be and the command that opens a folder there. One
// with a Probe exists only if that path does; one without is looked up on the search path.
type AppCandidate struct {
	ID      string
	Command string
	Args    []string
	Probe   *string
	// Icon is the bundle the application's own icon is read from.
	Icon *string
}

// InstalledApp is an application on this machine, with its icon as a data URL or null.
type InstalledApp struct {
	ID    string  `json:"id"`
	Label string  `json:"label"`
	Kind  string  `json:"kind"`
	Icon  *string `json:"icon"`
}

// AppCatalog is the domain's catalog of applications for this platform.
type AppCatalog struct {
	// Apps are the applications this platform can have, in the order the list draws them.
	Apps []ExternalApp
	// Candidates are the places one might be, opening `folder`, in the order worth trying.
	Candidates func(appID, home, folder string) []AppCandidate
}

// scanTTL is long enough that opening the list twice costs one scan, short enough to see a new install.
const scanTTL = 30 * time.Second

// AppOpener lists the applications on this machine and opens checkouts in them.
type AppOpener struct {
	catalog AppCatalog
	mu      sync.Mutex
	scanAt  time.Time
	scan    []InstalledApp
}

func NewAppOpener(catalog AppCatalog) *AppOpener {
	return &AppOpener{catalog: catalog}
}

func candidateExists(candidate AppCandidate) bool {
	if candidate.Probe != nil {
		_, err := os.Stat(*candidate.Probe)
		return err == nil
	}
	return onSearchPath(candidate.Command)
}

// onSearchPath reports whether the launcher is on the search path, found the way a shell finds it.
func onSearchPath(command string) bool {
	for folder := range strings.SplitSeq(SearchPath(), string(os.PathListSeparator)) {
		if folder == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(folder, command)); err == nil {
			return true
		}
	}
	return false
}

func homeDir() string {
	home, _ := os.UserHomeDir()
	return home
}

// installed is where an application really is first, which is also where its icon is read from.
func (o *AppOpener) installed(entry ExternalApp) *InstalledApp {
	for _, candidate := range o.catalog.Candidates(entry.ID, homeDir(), "/") {
		if !candidateExists(candidate) {
			continue
		}
		app := &InstalledApp{ID: entry.ID, Label: entry.Label, Kind: entry.Kind}
		if candidate.Icon != nil {
			app.Icon = BundleIcon(*candidate.Icon)
		}
		return app
	}
	return nil
}

// ListInstalled is every application on this machine that can take a folder, in catalog order.
func (o *AppOpener) ListInstalled() []InstalledApp {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.scan != nil && time.Since(o.scanAt) < scanTTL {
		return o.scan
	}
	found := make([]*InstalledApp, len(o.catalog.Apps))
	var group sync.WaitGroup
	for index, entry := range o.catalog.Apps {
		group.Go(func() { found[index] = o.installed(entry) })
	}
	group.Wait()
	apps := []InstalledApp{}
	for _, app := range found {
		if app != nil {
			apps = append(apps, *app)
		}
	}
	o.scan, o.scanAt = apps, time.Now()
	return apps
}

// OpenFolder opens a checkout in one application, trying each place it might be until one answers.
func (o *AppOpener) OpenFolder(appID, folder string) error {
	name := ""
	for _, entry := range o.catalog.Apps {
		if entry.ID == appID {
			name = entry.Label
			break
		}
	}
	candidates := o.catalog.Candidates(appID, homeDir(), folder)
	if name == "" || len(candidates) == 0 {
		return errors.New("AI Coding Tool does not know that application.")
	}
	for _, candidate := range candidates {
		if candidateExists(candidate) && LaunchDetached(candidate.Command, candidate.Args...) {
			return nil
		}
	}
	return errors.New(name + " is not installed on this machine.")
}
