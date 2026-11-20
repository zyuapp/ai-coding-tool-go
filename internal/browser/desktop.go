package browser

import (
	"context"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// Runtime is the browser panel's pages, driven from this process: the port's runtime and the
// window's own calls.
type Runtime struct {
	host               *Host
	openFullDiskAccess func(context.Context) error
}

var (
	_ contracts.BrowserRuntime         = (*Runtime)(nil)
	_ contracts.BrowserPortWindowCalls = (*Runtime)(nil)
	_ contracts.BrowserRuntime         = Headless{}
)

// NewRuntime drives host's pages. openFullDiskAccess opens the setting that lets the app read
// another browser's data; it may be nil.
func NewRuntime(host *Host, openFullDiskAccess func(context.Context) error) *Runtime {
	return &Runtime{host: host, openFullDiskAccess: openFullDiskAccess}
}

// reading opens the setting that allows a read macOS refused.
func (r *Runtime) reading(err error) error {
	var blocked *BrowserDataBlocked
	if errors.As(err, &blocked) && r.openFullDiskAccess != nil {
		go func() { _ = r.openFullDiskAccess(context.Background()) }()
	}
	return err
}

func (r *Runtime) ConfigureBrowserPermissions(_ context.Context, permissions domain.BrowserPermissions) error {
	r.host.ConfigurePermissions(permissions)
	return nil
}

func (r *Runtime) OpenBrowserTab(_ context.Context, tabID string, url, taskID *string) error {
	r.host.OpenTab(tabID, url, taskID)
	return nil
}

func (r *Runtime) NavigateBrowser(_ context.Context, tabID, url string, taskID *string) error {
	r.host.Navigate(tabID, url, taskID)
	return nil
}

func (r *Runtime) BrowserHistory(_ context.Context, tabID string, delta float64, taskID *string) error {
	return r.host.GoHistory(tabID, delta, taskID)
}

func (r *Runtime) ReloadBrowser(_ context.Context, tabID string, taskID *string) error {
	return r.host.Reload(tabID, taskID)
}

func (r *Runtime) CloseBrowserTab(_ context.Context, tabID string) error {
	r.host.CloseTab(tabID)
	return nil
}

func (r *Runtime) ShowBrowserTab(_ context.Context, tabID *string) error {
	r.host.ShowTab(tabID)
	return nil
}

func (r *Runtime) ActInBrowser(ctx context.Context, tabID string, action domain.BrowserAction, taskID *string) (string, error) {
	return r.host.Act(ctx, tabID, action, taskID)
}

func (r *Runtime) ReadBrowserPage(ctx context.Context, tabID string, textLimit, timeoutMs float64, taskID *string) (*domain.BrowserSnapshot, error) {
	return r.host.ReadPage(ctx, tabID, textLimit, timeoutMs, taskID)
}

func (r *Runtime) InspectBrowserPage(ctx context.Context, tabID string, inspection domain.BrowserInspection, taskID *string) (domain.BrowserInspectionResult, error) {
	return r.host.InspectPage(ctx, tabID, inspection, taskID)
}

func (r *Runtime) CaptureBrowserPage(ctx context.Context, tabID string, fullPage bool, timeoutMs float64, taskID *string) (*domain.BrowserShot, error) {
	return r.host.CapturePage(ctx, tabID, fullPage, timeoutMs, taskID)
}

func (r *Runtime) ClearBrowserData(ctx context.Context) error { return r.host.ClearData(ctx) }

func (r *Runtime) ListBrowserImportSources(ctx context.Context) ([]*domain.BrowserImportSource, error) {
	return ListImportSources(ctx)
}

func (r *Runtime) ListBrowserImportSites(ctx context.Context, sourceID string) ([]*domain.BrowserImportSite, error) {
	sites, err := ListImportSites(ctx, sourceID)
	return sites, r.reading(err)
}

func (r *Runtime) ImportBrowserSites(ctx context.Context, sourceID string, sites []string) (*domain.BrowserImportResult, error) {
	result, err := ImportBrowserSites(ctx, sourceID, sites, r.host.ReplaceCookies)
	return result, r.reading(err)
}

func (r *Runtime) OnBrowserEvent(listener func(contracts.BrowserPageEvent)) func() {
	return r.host.OnPage(listener)
}

func (r *Runtime) FindInPage(ctx context.Context, tabID, query string, forward, findNext bool) error {
	r.host.FindInPage(ctx, tabID, query, forward, findNext)
	return nil
}

func (r *Runtime) StopFindInPage(ctx context.Context, tabID string) error {
	r.host.StopFindInPage(ctx, tabID)
	return nil
}

func (r *Runtime) FocusBrowserTab(_ context.Context, tabID string) error {
	r.host.FocusTab(tabID)
	return nil
}

func (r *Runtime) OnBrowserFind(listener func(contracts.BrowserFindEvent)) func() {
	return r.host.OnFind(listener)
}

func (r *Runtime) SetBrowserBounds(_ context.Context, bounds *domain.BrowserBounds) error {
	r.host.SetBounds(bounds)
	return nil
}

var errNeedsDesktop = errors.New("The browser panel needs the desktop app.")

// Headless is the panel where none is drawn: reads find no page, and what would open one is refused.
type Headless struct{}

func (Headless) ConfigureBrowserPermissions(context.Context, domain.BrowserPermissions) error {
	return nil
}
func (Headless) OpenBrowserTab(context.Context, string, *string, *string) error {
	return errNeedsDesktop
}
func (Headless) NavigateBrowser(context.Context, string, string, *string) error {
	return errNeedsDesktop
}
func (Headless) BrowserHistory(context.Context, string, float64, *string) error {
	return errNeedsDesktop
}
func (Headless) ReloadBrowser(context.Context, string, *string) error { return errNeedsDesktop }
func (Headless) CloseBrowserTab(context.Context, string) error        { return nil }
func (Headless) ShowBrowserTab(context.Context, *string) error        { return nil }
func (Headless) ActInBrowser(context.Context, string, domain.BrowserAction, *string) (string, error) {
	return "", errNeedsDesktop
}
func (Headless) ReadBrowserPage(context.Context, string, float64, float64, *string) (*domain.BrowserSnapshot, error) {
	return nil, nil
}
func (Headless) InspectBrowserPage(context.Context, string, domain.BrowserInspection, *string) (domain.BrowserInspectionResult, error) {
	return nil, nil
}
func (Headless) CaptureBrowserPage(context.Context, string, bool, float64, *string) (*domain.BrowserShot, error) {
	return nil, nil
}
func (Headless) ClearBrowserData(context.Context) error { return nil }
func (Headless) ListBrowserImportSources(context.Context) ([]*domain.BrowserImportSource, error) {
	return []*domain.BrowserImportSource{}, nil
}
func (Headless) ListBrowserImportSites(context.Context, string) ([]*domain.BrowserImportSite, error) {
	return []*domain.BrowserImportSite{}, nil
}
func (Headless) ImportBrowserSites(context.Context, string, []string) (*domain.BrowserImportResult, error) {
	return nil, errNeedsDesktop
}
func (Headless) OnBrowserEvent(func(contracts.BrowserPageEvent)) func()       { return func() {} }
func (Headless) FindInPage(context.Context, string, string, bool, bool) error { return nil }
func (Headless) StopFindInPage(context.Context, string) error                 { return nil }
func (Headless) FocusBrowserTab(context.Context, string) error                { return nil }
func (Headless) OnBrowserFind(func(contracts.BrowserFindEvent)) func()        { return func() {} }
