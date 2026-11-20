package desktop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"

	wails "github.com/wailsapp/wails/v2/pkg/runtime"
	"github.com/zyuapp/ai-coding-tool-go/internal/computers"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
	"github.com/zyuapp/ai-coding-tool-go/internal/store"
	"github.com/zyuapp/ai-coding-tool-go/internal/workspace"
)

// runtimeDesktop is the runtime's desktop, answered in the process that owns every service it names
// (runtime-desktop.ts). Its parts are filled in once the services are open, before the runtime starts.
type runtimeDesktop struct {
	*computers.ServiceDesktop
	contracts.BrowserRuntime
	contracts.SimulatorPortRuntime
	contracts.DictationPortRuntime
	contracts.UpdatePortRuntime
	host.ComputerDesktop
	*mobile.Bridge
	*windowDesktop
}

var _ host.RuntimeDesktop = (*runtimeDesktop)(nil)

// windowDesktop is the window itself, its keys, and the desktop around it.
type windowDesktop struct {
	c       *Controller
	apps    *platform.AppOpener
	editors *platform.EditorOpener
}

func domainPlatform() domain.Platform { return domain.Platform(runtime.GOOS) }

func newWindowDesktop(c *Controller) *windowDesktop {
	osName := domainPlatform()
	var apps []domain.ExternalApp
	for _, app := range domain.ExternalApps(osName) {
		apps = append(apps, *app)
	}
	catalog := platform.AppCatalog{
		Apps: apps,
		Candidates: func(appID, home, folder string) []platform.AppCandidate {
			var candidates []platform.AppCandidate
			for _, candidate := range domain.AppCandidates(appID, osName, home, folder) {
				candidates = append(candidates, platform.AppCandidate{ID: candidate.ID, Command: candidate.Command, Args: candidate.Args, Probe: candidate.Probe, Icon: candidate.Icon})
			}
			return candidates
		},
	}
	line := func(line int) *float64 {
		if line == 0 {
			return nil
		}
		value := float64(line)
		return &value
	}
	launch := func(found *domain.Launch) *platform.EditorLaunch {
		if found == nil {
			return nil
		}
		return &platform.EditorLaunch{Command: found.Command, Args: found.Args}
	}
	editors := platform.EditorCatalog{
		Candidates: func(file string, at int) []platform.EditorLaunch {
			var launches []platform.EditorLaunch
			for _, candidate := range domain.EditorCandidates(osName, file, line(at)) {
				launches = append(launches, platform.EditorLaunch{Command: candidate.Command, Args: candidate.Args})
			}
			return launches
		},
		Launch: func(command, file string, at int) *platform.EditorLaunch {
			return launch(domain.EditorLaunch(osName, command, file, line(at)))
		},
		TextHandler:    func(file string) *platform.EditorLaunch { return launch(domain.TextHandlerLaunch(osName, file)) },
		OpensInBrowser: func(file string, at int) bool { return domain.OpensInBrowser(file, line(at)) },
	}
	return &windowDesktop{c: c, apps: platform.NewAppOpener(catalog), editors: platform.NewEditorOpener(editors)}
}

func (w *windowDesktop) OpenFolder(ctx context.Context) (*domain.WorkspaceRecord, error) {
	root, err := wails.OpenDirectoryDialog(w.c.app.context(), wails.OpenDialogOptions{Title: "Open a project folder", CanCreateDirectories: true})
	if err != nil || root == "" {
		return nil, err
	}
	return w.c.services.Workspaces.RegisterProject(ctx, root)
}

func (w *windowDesktop) RevealWorktree(_ context.Context, root string) error {
	owned, err := w.c.services.Workspaces.OwnedPath(root)
	if err != nil {
		return err
	}
	platform.ShowItemInFolder(owned)
	return nil
}

func (w *windowDesktop) RestartForComputerUse() {
	w.c.requestRestart(nil)
	w.c.quitApp()
}

func (w *windowDesktop) DownloadImage(_ context.Context, source string) error {
	s := w.c.services
	home, _ := os.UserHomeDir()
	return store.DownloadImage(store.ImageDownload{
		Attachments: s.Attachments,
		Images:      s.Images,
		Downloads:   filepath.Join(home, "Downloads"),
		SaveDialog: func(dialog store.SaveImageDialog) (string, error) {
			return wails.SaveFileDialog(w.c.app.context(), wails.SaveDialogOptions{
				Title:            dialog.Title,
				DefaultDirectory: filepath.Dir(dialog.DefaultPath),
				DefaultFilename:  filepath.Base(dialog.DefaultPath),
				Filters:          []wails.FileFilter{{DisplayName: dialog.FilterName, Pattern: patterns(dialog.Extensions)}},
			})
		},
		Fetch: func(source string) ([]byte, error) {
			response := store.AttachmentResponse(source, imageReads{w.c})
			if response.Status != 200 {
				return nil, errors.New(string(response.Body))
			}
			return response.Body, nil
		},
	}, source)
}

func patterns(extensions []string) string {
	pattern := ""
	for i, extension := range extensions {
		if i > 0 {
			pattern += ";"
		}
		pattern += "*." + extension
	}
	return pattern
}

// imageReads answers the store's image reads from whichever computer holds the thread.
type imageReads struct{ c *Controller }

func (r imageReads) ReadAttachment(name string, thread *string) ([]byte, error) {
	return r.c.services.Reads.Attachment(context.Background(), name, thread)
}

func (r imageReads) ReadMessageImage(query contracts.ComputerQueryMessageImage, thread *string) (store.MessageImage, error) {
	answer, err := r.c.services.Reads.MessageImage(context.Background(), query.Path, query.Root, query.Message, query.Thumbnail != nil && *query.Thumbnail, thread)
	return store.MessageImage{Bytes: answer.Bytes, ContentType: answer.ContentType}, err
}

// OpenSourceLicenses shows the notices the app ships in the app's own reader.
func (w *windowDesktop) OpenSourceLicenses(context.Context) error {
	return w.c.app.openSourceLicenses(w.c.options.Packaged, w.c.options.Resources, w.c.options.AppPath)
}

func (w *windowDesktop) OpenFile(_ context.Context, roots []string, path string, line *float64) error {
	file, err := workspace.OpenableFile(roots, path)
	if err != nil {
		return err
	}
	at := 0
	if line != nil {
		at = int(*line)
	}
	go w.editors.Open(file, at)
	return nil
}

func (w *windowDesktop) ListApps(context.Context) ([]*contracts.InstalledApp, error) {
	installed := w.apps.ListInstalled()
	apps := make([]*contracts.InstalledApp, len(installed))
	for i := range installed {
		apps[i] = &installed[i]
	}
	return apps, nil
}

func (w *windowDesktop) OpenFolderInApp(_ context.Context, appID string, root string) error {
	folder, err := workspace.FileInCheckout(root, ".")
	if err != nil {
		return err
	}
	return w.apps.OpenFolder(appID, folder)
}

func (w *windowDesktop) SetCaptureOptions(options domain.CaptureOptions) {
	if keyboard := w.c.keyboard; keyboard != nil {
		keyboard.SetCaptureOptions(options)
	}
}

func (w *windowDesktop) SetShortcuts(overrides domain.ShortcutOverrides) {
	if data, err := jsonx.Marshal(overrides); err == nil {
		w.c.app.SetShortcuts(data)
	}
	if keyboard := w.c.keyboard; keyboard != nil {
		keyboard.SetShortcuts(overrides)
		keyboard.ClaimDesktopShortcut()
	}
}

func (w *windowDesktop) SetShortcutCapture(capturing bool) {
	w.c.app.SetShortcutCapture(capturing)
	keyboard := w.c.keyboard
	if keyboard == nil {
		return
	}
	keyboard.SetShortcutCapture(capturing)
	// A keystroke the desktop is holding never reaches the window, so settings could not read it back.
	if capturing {
		keyboard.ReleaseDesktopShortcut()
	} else {
		keyboard.ClaimDesktopShortcut()
	}
}

func (w *windowDesktop) CloseWindow() { w.c.app.closeWindow() }

func (w *windowDesktop) FocusWindow() { w.c.app.focusPage() }

// AnnounceThread: the phone is told only when the user has stepped away, where the desktop already says it.
func (w *windowDesktop) AnnounceThread(notice contracts.ThreadNotice) {
	platform.AnnounceThread(noticeHost{w.c.app}, notice)
	if platform.DesktopAway() {
		go w.c.mobile.NotifyPhones(context.Background(), notice)
	}
}

func (w *windowDesktop) SetBadgeCount(count float64) { w.c.app.SetBadgeCount(int(count)) }

// noticeHost is where a thread's notice goes when the window is not where the user is looking.
type noticeHost struct{ app *App }

func (h noticeHost) Focused() bool {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return !h.app.closed && h.app.active != nil && *h.app.active
}

func (h noticeHost) HasWindow() bool {
	h.app.mu.Lock()
	defer h.app.mu.Unlock()
	return !h.app.closed
}

func (h noticeHost) Reveal() { h.app.Reveal() }

func (h noticeHost) OpenThread(taskID string) { h.app.SendValue(ChannelOpenThread, taskID) }
