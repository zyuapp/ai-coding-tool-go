package platform

import (
	"net/url"
	"path/filepath"
	"sync"
)

// EditorLaunch is a launcher and its arguments.
type EditorLaunch struct {
	Command string
	Args    []string
}

// EditorCatalog is the domain's catalog of editors for this platform.
type EditorCatalog struct {
	// Candidates are every launcher worth trying, best first. Line 0 means none.
	Candidates func(file string, line int) []EditorLaunch
	// Launch is the launcher a resolved editor uses next time, or nil when it is not an editor's.
	Launch func(command, file string, line int) *EditorLaunch
	// TextHandler opens the file as text rather than by its extension, or nil where there is none.
	TextHandler func(file string) *EditorLaunch
	// OpensInBrowser reports whether the file is a web page to look at rather than source to read.
	OpensInBrowser func(file string, line int) bool
}

// EditorOpener opens source files for reading, remembering the editor that answered last.
type EditorOpener struct {
	catalog  EditorCatalog
	mu       sync.Mutex
	resolved string
}

func NewEditorOpener(catalog EditorCatalog) *EditorOpener {
	return &EditorOpener{catalog: catalog}
}

// fileURL is `pathToFileURL(file).href`.
func fileURL(file string) string {
	absolute, err := filepath.Abs(file)
	if err != nil {
		absolute = file
	}
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
}

// Open opens a source file at its line where the editor it finds takes one. A web page opens in the
// browser. Trying to start a launcher is the probe: a missing one fails before it can do anything.
func (o *EditorOpener) Open(file string, line int) {
	if o.catalog.OpensInBrowser(file, line) && OpenExternal(fileURL(file)) == nil {
		return
	}

	o.mu.Lock()
	resolved := o.resolved
	o.mu.Unlock()
	if resolved != "" {
		if known := o.catalog.Launch(resolved, file, line); known != nil && LaunchDetached(known.Command, known.Args...) {
			return
		}
		o.mu.Lock()
		o.resolved = ""
		o.mu.Unlock()
	}

	for _, candidate := range o.catalog.Candidates(file, line) {
		if !LaunchDetached(candidate.Command, candidate.Args...) {
			continue
		}
		o.mu.Lock()
		o.resolved = candidate.Command
		o.mu.Unlock()
		return
	}

	if handler := o.catalog.TextHandler(file); handler != nil && LaunchDetached(handler.Command, handler.Args...) {
		return
	}
	if OpenPath(file) == "" {
		return
	}
	ShowItemInFolder(file)
}
