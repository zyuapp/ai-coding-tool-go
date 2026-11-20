// Package browser is the browser panel: a native web view per tab placed over the panel's area of
// the app window (WKWebView on macOS, WebKitGTK on Linux), one persistent session shared by every
// tab, the approval a run needs before it reaches a site, the reads and actions the browser tools
// drive pages with, and copying sign-ins from other browsers.
package browser

import (
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	defaultTextLimit = 4_000
	// maxShotPoints: a long page is cut off here rather than refused.
	maxShotPoints = 8_000
	// loadStartGrace is how long a load asked for may take to start before the tab counts as settled without it.
	loadStartGrace = 1_500 * time.Millisecond
	// actionLoadGrace is how long an action's own navigation may take to begin.
	actionLoadGrace = 300 * time.Millisecond

	approvalError       = "This site needs browser approval before the agent can use it."
	scriptApprovalError = "This site needs browser approval."
	pageChangedError    = "The page changed while it was being read. Try again."
)

// parkedViewport: a page nobody is looking at still lays itself out, so it is given a window's worth of room.
var parkedViewport = rect{Width: 1_200, Height: 800}

func nowMs() float64 { return application.Now() }

// owner decides which pages a navigation may reach: the run driving the tab, if one is. A popup
// has its own, copied from its opener's when it opened.
type owner struct {
	tabID  string
	taskID *string
}

type findState struct {
	query   string
	index   float64
	matches float64
}

type documentRequest struct {
	url       string
	startedAt float64
	method    string
}

type tab struct {
	owner
	view handle
	// shown is whether the window is drawing this page. Only the page on screen is ever over it.
	shown bool
	epoch uint64
	// requested is when a load was asked for that the engine has yet to start.
	requested time.Time
	document  *documentRequest
	diagnostics
	find findState
}

// Host holds the browser panel's pages. Its methods are safe from any goroutine.
type Host struct {
	engine    engine
	userAgent string
	onKey     func(KeyEvent) bool
	events    *publisher

	mu      sync.Mutex
	running bool
	tabs    map[string]*tab
	views   map[handle]*tab
	popups  map[handle]*owner
	next    handle
	active  *string
	bounds  *domain.BrowserBounds
	parked  rect
	// origins keeps the order they were granted in, which the in-page guard lists them in.
	origins    []string
	autonomous map[string]bool
	shots      shots
}

// Options configures a Host.
type Options struct {
	// OnKey is offered every key a page is given before the page sees it, and answers whether the
	// app took it. It runs on the UI thread and must answer promptly.
	OnKey func(KeyEvent) bool
	// SessionDir is where Linux keeps the panel's cookies and storage, so sign-ins outlive the app.
	// macOS keeps them in a WebKit data store of the app's own.
	SessionDir string
}

// NewHost makes the panel's host on this platform's web views. Start binds it to the app window.
func NewHost(options Options) *Host {
	return newHost(platformEngine(options), options)
}

func newHost(engine engine, options Options) *Host {
	return &Host{
		engine:     engine,
		userAgent:  SafariUserAgent(),
		onKey:      options.OnKey,
		events:     newPublisher(),
		tabs:       map[string]*tab{},
		views:      map[handle]*tab{},
		popups:     map[handle]*owner{},
		parked:     parkedViewport,
		autonomous: map[string]bool{},
	}
}

// Available reports whether this build can draw pages at all.
func (h *Host) Available() bool { return h.engine != nil }

// Start places the pages over the app window, which must already exist. Call it on startup, after
// the Wails window is up; until then every page call finds no window and does nothing.
func (h *Host) Start() error {
	if h.engine == nil {
		return errors.New("The browser panel is not available on this platform yet.")
	}
	if err := h.engine.start(h, h.userAgent); err != nil {
		return err
	}
	h.mu.Lock()
	h.running = true
	h.mu.Unlock()
	return nil
}

// Stop drops every page, so a closed window leaves no page running, and removes the pictures taken.
func (h *Host) Stop() {
	h.mu.Lock()
	ids := make([]string, 0, len(h.tabs))
	for id := range h.tabs {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		h.CloseTab(id)
	}
	h.mu.Lock()
	wasStarted := h.running
	h.running = false
	h.active = nil
	h.bounds = nil
	h.parked = parkedViewport
	h.popups = map[handle]*owner{}
	h.origins = nil
	h.autonomous = map[string]bool{}
	h.shots.discard()
	h.mu.Unlock()
	if wasStarted {
		h.engine.stop()
	}
}

// OnPage subscribes to what pages do, delivered in order on a goroutine of their own.
func (h *Host) OnPage(listener func(contracts.BrowserPageEvent)) func() {
	return h.events.subscribePage(listener)
}

// OnFind subscribes to how searches of a page went.
func (h *Host) OnFind(listener func(contracts.BrowserFindEvent)) func() {
	return h.events.subscribeFind(listener)
}

func (h *Host) report(event contracts.BrowserPageEvent) { h.events.page(event) }

// ConfigurePermissions sets which origins runs may use, and which runs browse on their own.
func (h *Host) ConfigurePermissions(permissions domain.BrowserPermissions) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.origins = uniqueStrings(permissions.Origins)
	h.autonomous = map[string]bool{}
	for _, id := range permissions.AutonomousTaskIDs {
		h.autonomous[id] = true
	}
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := []string{}
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			unique = append(unique, value)
		}
	}
	return unique
}

func originOf(raw string) string {
	origin, _ := domain.BrowserOrigin(raw)
	return origin
}

// permitted is whether a caller may use url: the user anything, a run only the origins the user
// allowed, unless the run browses on its own. Called with h.mu held.
func (h *Host) permitted(taskID *string, raw string) bool {
	return taskID == nil || h.autonomous[*taskID] || slices.Contains(h.origins, originOf(raw))
}

// scriptOrigins is the origins a script must find itself on, or nil when the caller may run it
// anywhere. Called with h.mu held.
func (h *Host) scriptOrigins(taskID *string) []string {
	if taskID == nil || h.autonomous[*taskID] {
		return nil
	}
	return append([]string{}, h.origins...)
}

func (h *Host) guarded(script string, taskID *string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return guardedScript(script, h.scriptOrigins(taskID))
}

func (h *Host) requirePage(t *tab, taskID *string) error {
	state := h.engine.state(t.view)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.permitted(taskID, state.URL) {
		return errors.New(approvalError)
	}
	return nil
}

func (h *Host) samePage(t *tab, epoch uint64, taskID *string) error {
	if err := h.requirePage(t, taskID); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if t.epoch != epoch || h.tabs[t.tabID] != t {
		return errors.New(pageChangedError)
	}
	return nil
}

func isWebURL(raw string) bool {
	return strings.HasPrefix(raw, "http:") || strings.HasPrefix(raw, "https:")
}

// allowNavigation answers whether a navigation may happen. One a run may not make yet is put to the
// user instead. Called with h.mu held.
func (h *Host) allowNavigation(owner *owner, raw string) bool {
	if !isWebURL(raw) {
		return false
	}
	if h.permitted(owner.taskID, raw) {
		return true
	}
	h.report(contracts.BrowserPageEvent{TabID: owner.tabID, Loading: ptr(false), NavigationRequest: &contracts.BrowserPageEventNavigationRequest{TaskID: *owner.taskID, URL: raw}})
	return false
}

func ptr[T any](value T) *T { return &value }

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	return ptr(*value)
}

func (h *Host) tab(id string) *tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tabs[id]
}

// OpenTab gives a tab its page. Idempotent: a tab that already has one keeps it, so showing a tab
// never reloads its page.
func (h *Host) OpenTab(tabID string, rawURL, taskID *string) {
	h.mu.Lock()
	if h.tabs[tabID] != nil || !h.running {
		h.mu.Unlock()
		return
	}
	h.next++
	t := &tab{owner: owner{tabID: tabID, taskID: cloneString(taskID)}, view: h.next, diagnostics: newDiagnostics(nowMs())}
	parked := h.parked
	h.tabs[tabID] = t
	h.views[t.view] = t
	h.mu.Unlock()
	if err := h.engine.open(t.view, parked.Width, parked.Height); err != nil {
		h.mu.Lock()
		if h.tabs[tabID] == t {
			delete(h.tabs, tabID)
		}
		delete(h.views, t.view)
		h.mu.Unlock()
		h.report(contracts.BrowserPageEvent{TabID: tabID, Loading: ptr(false), Error: ptr(err.Error())})
		return
	}
	if rawURL != nil && *rawURL != "" {
		h.load(t, *rawURL)
	}
	h.layout(t, h.engine.zoom())
}

func (h *Host) load(t *tab, raw string) {
	h.mu.Lock()
	allowed := h.allowNavigation(&t.owner, raw)
	live := h.tabs[t.tabID] == t
	h.mu.Unlock()
	if !allowed {
		return
	}
	if err := h.engine.load(t.view, raw); err != nil {
		if live && !strings.Contains(err.Error(), "ERR_ABORTED") {
			h.report(contracts.BrowserPageEvent{TabID: t.tabID, Loading: ptr(false), Error: ptr(err.Error())})
		}
		return
	}
	h.mu.Lock()
	t.requested = time.Now()
	h.mu.Unlock()
}

// Navigate loads url in a tab, opening the tab when it has no page yet.
func (h *Host) Navigate(tabID, rawURL string, taskID *string) {
	t := h.tab(tabID)
	if t == nil {
		h.OpenTab(tabID, &rawURL, taskID)
		return
	}
	h.mu.Lock()
	t.taskID = cloneString(taskID)
	h.mu.Unlock()
	h.load(t, rawURL)
}

// GoHistory steps a tab back (-1) or forward (1).
func (h *Host) GoHistory(tabID string, delta float64, taskID *string) error {
	t := h.tab(tabID)
	if t == nil {
		return nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return err
	}
	state := h.engine.state(t.view)
	h.mu.Lock()
	t.taskID = cloneString(taskID)
	back, forward := delta == -1 && state.CanGoBack, delta == 1 && state.CanGoForward
	if back || forward {
		t.requested = time.Now()
	}
	h.mu.Unlock()
	if back {
		h.engine.goBack(t.view)
	}
	if forward {
		h.engine.goForward(t.view)
	}
	return nil
}

// Reload reloads a tab's page.
func (h *Host) Reload(tabID string, taskID *string) error {
	t := h.tab(tabID)
	if t == nil {
		return nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return err
	}
	h.mu.Lock()
	t.taskID = cloneString(taskID)
	t.requested = time.Now()
	h.mu.Unlock()
	h.engine.reload(t.view)
	return nil
}

// FocusTab hands the keyboard to a tab's page.
func (h *Host) FocusTab(tabID string) {
	if t := h.tab(tabID); t != nil {
		h.engine.focus(t.view)
	}
}

// CloseTab drops a tab's page and every window it opened.
func (h *Host) CloseTab(tabID string) {
	h.mu.Lock()
	t := h.tabs[tabID]
	if t == nil {
		h.mu.Unlock()
		return
	}
	delete(h.tabs, tabID)
	delete(h.views, t.view)
	popups := []handle{}
	for id, popup := range h.popups {
		if popup.tabID == tabID {
			popups = append(popups, id)
			delete(h.popups, id)
		}
	}
	h.mu.Unlock()
	for _, popup := range popups {
		h.engine.closePopup(popup)
	}
	h.engine.close(t.view)
}

// ShowTab is which tab the panel shows. The page is only drawn once the panel has also reported
// where it is.
func (h *Host) ShowTab(tabID *string) {
	h.mu.Lock()
	h.active = cloneString(tabID)
	h.mu.Unlock()
	h.layoutAll(h.engine)
}

// SetBounds is where the panel is, in window coordinates. Nil means the panel is not on screen.
func (h *Host) SetBounds(bounds *domain.BrowserBounds) {
	var zoom float64 = 1
	if h.engine != nil {
		zoom = h.engine.zoom()
	}
	h.mu.Lock()
	if bounds == nil {
		h.bounds = nil
		h.parked = parkedViewport
	} else {
		box := *bounds
		h.bounds = &box
		// A page off screen lays out in the panel it would appear in, or a window's worth when there is none.
		frame := inWindow(box, zoom)
		h.parked = rect{Width: frame.Width, Height: frame.Height}
	}
	h.mu.Unlock()
	h.layoutAll(h.engine)
}

func (h *Host) layoutAll(engine engine) {
	if engine == nil {
		return
	}
	zoom := engine.zoom()
	h.mu.Lock()
	tabs := make([]*tab, 0, len(h.tabs))
	for _, t := range h.tabs {
		tabs = append(tabs, t)
	}
	h.mu.Unlock()
	for _, t := range tabs {
		h.layout(t, zoom)
	}
}

// inWindow turns the panel's CSS pixels into window points, which differ by however much the app's
// page is zoomed.
func inWindow(box domain.BrowserBounds, zoom float64) rect {
	return rect{
		X:      jsRound(box.X * zoom),
		Y:      jsRound(box.Y * zoom),
		Width:  math.Max(1, jsRound(box.Width*zoom)),
		Height: math.Max(1, jsRound(box.Height*zoom)),
	}
}

// jsRound is Math.round: halves go up.
func jsRound(value float64) float64 { return math.Floor(value + 0.5) }

// layout puts a tab over the panel when it is the one on screen, else parked out of the app's
// window: a page navigating inside a window takes the keyboard off whatever there had it.
func (h *Host) layout(t *tab, zoom float64) {
	h.mu.Lock()
	if h.tabs[t.tabID] != t {
		h.mu.Unlock()
		return
	}
	shown := h.active != nil && *h.active == t.tabID && h.bounds != nil
	frame := h.parked
	if shown {
		frame = inWindow(*h.bounds, zoom)
	}
	t.shown = shown
	h.mu.Unlock()
	h.engine.place(t.view, shown, frame)
}

// settled waits for the tab to stop loading, up to timeout. Nil when the tab is gone by then.
func (h *Host) settled(ctx context.Context, tabID string, timeout time.Duration) *tab {
	deadline := time.Now().Add(timeout)
	for {
		t := h.tab(tabID)
		if t == nil {
			return nil
		}
		if !h.loading(t) || !time.Now().Before(deadline) {
			return t
		}
		select {
		case <-ctx.Done():
			return t
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (h *Host) loading(t *tab) bool {
	state := h.engine.state(t.view)
	h.mu.Lock()
	defer h.mu.Unlock()
	return state.Loading || (!t.requested.IsZero() && time.Since(t.requested) < loadStartGrace)
}

func millis(ms float64) time.Duration {
	if math.IsNaN(ms) || ms <= 0 {
		return 0
	}
	return time.Duration(ms * float64(time.Millisecond))
}

type snapshotPage struct {
	URL       string                   `json:"url"`
	Title     string                   `json:"title"`
	Text      string                   `json:"text"`
	Truncated bool                     `json:"truncated"`
	Elements  []*domain.BrowserElement `json:"elements"`
}

// ReadPage waits for the page to settle, then reads it. Nil when that tab is gone by then.
func (h *Host) ReadPage(ctx context.Context, tabID string, textLimit, timeoutMs float64, taskID *string) (*domain.BrowserSnapshot, error) {
	t := h.settled(ctx, tabID, millis(timeoutMs))
	if t == nil {
		return nil, nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return nil, err
	}
	h.mu.Lock()
	epoch := t.epoch
	h.mu.Unlock()
	requested := textLimit
	if requested == 0 || math.IsNaN(requested) {
		requested = defaultTextLimit
	}
	limit := math.Max(200, math.Min(requested, 120_000))
	value, err := h.engine.evaluate(ctx, t.view, h.guarded(snapshotScript(limit), taskID))
	if err != nil {
		return nil, err
	}
	if err := h.samePage(t, epoch, taskID); err != nil {
		return nil, err
	}
	var page snapshotPage
	if err := jsonx.Unmarshal(value, &page); err != nil {
		return nil, err
	}
	h.mu.Lock()
	allowed := h.permitted(taskID, page.URL)
	h.mu.Unlock()
	if !allowed {
		return nil, errors.New(scriptApprovalError)
	}
	text := page.Text
	if page.Truncated {
		text += "\n… (page text truncated)"
	}
	if page.Elements == nil {
		page.Elements = []*domain.BrowserElement{}
	}
	return &domain.BrowserSnapshot{TabID: tabID, URL: page.URL, Title: page.Title, Loading: h.loading(t), Text: text, Elements: page.Elements}, nil
}

var consoleRanks = map[domain.BrowserConsoleLevel]int{
	domain.BrowserConsoleLevelDebug:   0,
	domain.BrowserConsoleLevelInfo:    1,
	domain.BrowserConsoleLevelWarning: 2,
	domain.BrowserConsoleLevelError:   3,
}

func readLimit(limit *float64) int {
	value := 50.0
	if limit != nil {
		value = *limit
	}
	return int(math.Trunc(math.Max(1, math.Min(value, maxDiagnosticEntries))))
}

// InspectPage reads bounded diagnostic history or waits for a condition in one tab. Nil when the
// tab is not open.
func (h *Host) InspectPage(ctx context.Context, tabID string, inspection domain.BrowserInspection, taskID *string) (domain.BrowserInspectionResult, error) {
	t := h.tab(tabID)
	if t == nil {
		return nil, nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return nil, err
	}
	switch inspection := inspection.(type) {
	case domain.BrowserInspectionConsole:
		state := h.engine.state(t.view)
		minimum := consoleRanks[domain.BrowserConsoleLevelDebug]
		if inspection.MinimumLevel != nil {
			minimum = consoleRanks[*inspection.MinimumLevel]
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		visible := []*domain.BrowserConsoleEntry{}
		for _, recorded := range t.console {
			if h.permitted(taskID, recorded.pageOrigin) {
				entry := recorded.entry
				visible = append(visible, &entry)
			}
		}
		entries, omitted := newest(visible, sinceOf(inspection.Since), readLimit(inspection.Limit), func(entry *domain.BrowserConsoleEntry) bool {
			return consoleRanks[entry.Level] >= minimum
		})
		return domain.BrowserConsoleSnapshot{TabID: tabID, URL: state.URL, Title: state.Title, Entries: entries, Omitted: float64(omitted), LatestSequence: t.consoleSequence}, nil
	case domain.BrowserInspectionNetwork:
		state := h.engine.state(t.view)
		failuresOnly := inspection.FailuresOnly != nil && *inspection.FailuresOnly
		h.mu.Lock()
		defer h.mu.Unlock()
		visible := []*domain.BrowserNetworkEntry{}
		for _, recorded := range t.network {
			if h.permitted(taskID, recorded.pageOrigin) {
				entry := recorded.entry
				visible = append(visible, &entry)
			}
		}
		entries, omitted := newest(visible, sinceOf(inspection.Since), readLimit(inspection.Limit), func(entry *domain.BrowserNetworkEntry) bool {
			return !failuresOnly || entry.Error != nil || (entry.Status != nil && *entry.Status >= 400)
		})
		return domain.BrowserNetworkSnapshot{TabID: tabID, URL: state.URL, Title: state.Title, Entries: entries, Omitted: float64(omitted), LatestSequence: t.networkSequence}, nil
	case domain.BrowserInspectionWait:
		return h.waitForPage(ctx, t, inspection, taskID)
	}
	return nil, nil
}

func sinceOf(since *float64) float64 {
	if since == nil {
		return 0
	}
	return *since
}

func (h *Host) waitMatches(ctx context.Context, t *tab, condition domain.BrowserWaitCondition, value string, taskID *string) (bool, error) {
	if err := h.requirePage(t, taskID); err != nil {
		return false, err
	}
	switch condition {
	case domain.BrowserWaitConditionURL:
		return strings.Contains(strings.ToLower(h.engine.state(t.view).URL), strings.ToLower(value)), nil
	case domain.BrowserWaitConditionNetworkIdle:
		h.mu.Lock()
		defer h.mu.Unlock()
		return t.idle(nowMs()), nil
	}
	h.mu.Lock()
	epoch := t.epoch
	h.mu.Unlock()
	result, err := h.engine.evaluate(ctx, t.view, h.guarded(pageConditionScript(condition, value), taskID))
	if err != nil {
		return false, err
	}
	if err := h.samePage(t, epoch, taskID); err != nil {
		return false, err
	}
	return string(result) == "true", nil
}

func (h *Host) waitForPage(ctx context.Context, t *tab, inspection domain.BrowserInspectionWait, taskID *string) (domain.BrowserInspectionResult, error) {
	startedAt := time.Now()
	deadline := startedAt.Add(millis(inspection.TimeoutMs))
	value := ""
	if inspection.Value != nil {
		value = *inspection.Value
	}
	matched := false
	for {
		h.mu.Lock()
		live := h.tabs[t.tabID] == t
		h.mu.Unlock()
		if !live {
			return nil, nil
		}
		var err error
		matched, err = h.waitMatches(ctx, t, inspection.Condition, value, taskID)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		if matched || !now.Before(deadline) {
			break
		}
		pause := min(100*time.Millisecond, max(time.Millisecond, deadline.Sub(now)))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pause):
		}
	}
	if err := h.requirePage(t, taskID); err != nil {
		return nil, err
	}
	state := h.engine.state(t.view)
	return domain.BrowserWaitResult{
		TabID:     t.tabID,
		URL:       state.URL,
		Title:     state.Title,
		Condition: inspection.Condition,
		Value:     cloneString(inspection.Value),
		Matched:   matched,
		ElapsedMs: float64(time.Since(startedAt).Milliseconds()),
	}, nil
}

// CapturePage waits for the page to settle, then writes a picture of it. Nil when that tab is gone by then.
func (h *Host) CapturePage(ctx context.Context, tabID string, fullPage bool, timeoutMs float64, taskID *string) (*domain.BrowserShot, error) {
	t := h.settled(ctx, tabID, millis(timeoutMs))
	if t == nil {
		return nil, nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return nil, err
	}
	h.mu.Lock()
	epoch := t.epoch
	woken := !t.shown
	h.mu.Unlock()
	// A page the window is not drawing is made to draw where it is parked for the capture. That
	// window is never shown, so this puts nothing on screen.
	if woken {
		h.engine.wake(t.view)
	}
	bytes, err := h.drawPage(ctx, t, fullPage)
	if woken {
		h.layout(t, h.engine.zoom())
	}
	if err != nil {
		return nil, err
	}
	if err := h.samePage(t, epoch, taskID); err != nil {
		return nil, err
	}
	width, height := pngSize(bytes)
	h.mu.Lock()
	path, err := h.shots.write(bytes)
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	state := h.engine.state(t.view)
	return &domain.BrowserShot{TabID: tabID, URL: state.URL, Title: state.Title, Path: path, Width: float64(width), Height: float64(height)}, nil
}

func (h *Host) drawPage(ctx context.Context, t *tab, fullPage bool) ([]byte, error) {
	if !fullPage {
		return h.engine.draw(ctx, t.view, false, 0, 0)
	}
	value, err := h.engine.evaluate(ctx, t.view, pageSizeScript)
	if err != nil {
		return nil, err
	}
	var size struct {
		Width  *float64 `json:"width"`
		Height *float64 `json:"height"`
	}
	_ = jsonx.Unmarshal(value, &size)
	clamp := func(value *float64) float64 {
		if value == nil || math.IsNaN(*value) {
			return 1
		}
		return math.Max(1, math.Min(math.Round(*value), maxShotPoints))
	}
	return h.engine.draw(ctx, t.view, true, clamp(size.Width), clamp(size.Height))
}

// Act clicks or types into the page, answering what it did.
func (h *Host) Act(ctx context.Context, tabID string, action domain.BrowserAction, taskID *string) (string, error) {
	t := h.tab(tabID)
	if t == nil {
		return "That tab is no longer open.", nil
	}
	if err := h.requirePage(t, taskID); err != nil {
		return "", err
	}
	h.mu.Lock()
	t.taskID = cloneString(taskID)
	h.mu.Unlock()
	value, err := h.engine.evaluate(ctx, t.view, h.guarded(actionScript(action), taskID))
	if err != nil {
		return "", err
	}
	if err := h.requirePage(t, taskID); err != nil {
		return "", err
	}
	// The engine starts a navigation the action caused a moment later, so a read right after would
	// see the old page.
	navigates := false
	switch action := action.(type) {
	case domain.BrowserActionClick:
		navigates = true
	case domain.BrowserActionType:
		navigates = action.Submit != nil && *action.Submit
	}
	if navigates {
		until := time.Now().Add(actionLoadGrace)
		for !h.engine.state(t.view).Loading && time.Now().Before(until) {
			time.Sleep(25 * time.Millisecond)
		}
	}
	var result string
	_ = jsonx.Unmarshal(value, &result)
	return result, nil
}

// FindInPage searches a page, stepping to the next match when the query is one the page is
// already showing.
func (h *Host) FindInPage(ctx context.Context, tabID, query string, forward, findNext bool) {
	t := h.tab(tabID)
	if t == nil {
		return
	}
	h.mu.Lock()
	previous := t.find
	h.mu.Unlock()
	fresh := !findNext || previous.query != query
	matches := previous.matches
	if fresh {
		matches = 0
		if value, err := h.engine.evaluate(ctx, t.view, countMatchesScript(query)); err == nil {
			var counted float64
			if jsonx.Unmarshal(value, &counted) == nil {
				matches = counted
			}
		}
		_, _ = h.engine.evaluate(ctx, t.view, clearSelectionScript)
	}
	found := query != "" && h.engine.find(ctx, t.view, query, !forward)
	if found {
		matches = math.Max(matches, 1)
	} else {
		matches = 0
	}
	var index float64
	switch {
	case !found:
	case fresh && forward:
	case fresh:
		index = matches - 1
	case forward:
		index = euclid(previous.index+1, matches)
	default:
		index = euclid(previous.index-1, matches)
	}
	h.mu.Lock()
	t.find = findState{query: query, index: index, matches: matches}
	live := h.tabs[tabID] == t
	h.mu.Unlock()
	if live {
		h.events.find(contracts.BrowserFindEvent{TabID: tabID, FindResults: domain.FindResults{Matches: matches, Index: ptr(index)}})
	}
}

func euclid(value, modulus float64) float64 {
	result := math.Mod(value, modulus)
	if result < 0 {
		result += modulus
	}
	return result
}

// StopFindInPage ends a search, clearing what it selected.
func (h *Host) StopFindInPage(ctx context.Context, tabID string) {
	t := h.tab(tabID)
	if t == nil {
		return
	}
	h.mu.Lock()
	t.find = findState{}
	h.mu.Unlock()
	_, _ = h.engine.evaluate(ctx, t.view, clearSelectionScript)
}

func (h *Host) allTabs() []*tab {
	h.mu.Lock()
	defer h.mu.Unlock()
	tabs := make([]*tab, 0, len(h.tabs))
	for _, t := range h.tabs {
		tabs = append(tabs, t)
	}
	return tabs
}

// ClearData signs the whole app out of everything the panel has ever logged into.
func (h *Host) ClearData(ctx context.Context) error {
	h.mu.Lock()
	h.origins = nil
	h.mu.Unlock()
	if h.engine == nil {
		return nil
	}
	if err := h.engine.clearData(ctx); err != nil {
		return err
	}
	for _, t := range h.allTabs() {
		h.engine.reload(t.view)
	}
	return nil
}

// ReplaceCookies replaces sites' cookies in the panel's session with cookies, then reloads the
// pages on those sites. Nothing is removed when there is nothing to set. Answers how many were set.
func (h *Host) ReplaceCookies(ctx context.Context, cookies []CookieDetails, sites []string) (int, error) {
	if h.engine == nil {
		return 0, errors.New("The browser panel needs the desktop app.")
	}
	if len(cookies) > 0 {
		held, err := h.engine.cookies(ctx)
		if err == nil {
			for _, cookie := range held {
				if cookie.Domain != "" && onAnySite(cookie.Domain, sites) {
					_ = h.engine.deleteCookie(ctx, cookie)
				}
			}
		}
	}
	added := 0
	for _, cookie := range cookies {
		if h.engine.setCookie(ctx, cookie) {
			added++
		}
	}
	for _, t := range h.allTabs() {
		parsed, err := url.Parse(h.engine.state(t.view).URL)
		if err == nil && parsed.Hostname() != "" && onAnySite(parsed.Hostname(), sites) {
			h.engine.reload(t.view)
		}
	}
	return added, nil
}

// The engine's signals. They arrive on the UI thread, so none calls back into the engine while
// holding h.mu, and none blocks on anything the UI thread does.

func (h *Host) ownerOf(view handle) (*owner, *tab) {
	if t := h.views[view]; t != nil {
		return &t.owner, t
	}
	return h.popups[view], nil
}

// frameScheme is a scheme frames are built from, which no navigation is refused for where the
// engine cannot tell a page from a frame.
func frameScheme(raw string) bool {
	lower := strings.ToLower(strings.TrimLeft(raw, " \t\n\r"))
	return strings.HasPrefix(lower, "about:") || strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:")
}

func (h *Host) decide(view handle, raw, method string, target frameTarget) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	owner, t := h.ownerOf(view)
	if owner == nil {
		return false
	}
	if target == targetFrame {
		return true
	}
	// A navigation that may be a frame's is let through, and checked once its response shows it is the page's.
	if target == targetUnknown && !(isWebURL(raw) && h.permitted(owner.taskID, raw)) {
		return frameScheme(raw) || isWebURL(raw)
	}
	if !h.allowNavigation(owner, raw) {
		if t != nil {
			t.requested = time.Time{}
		}
		return false
	}
	if t != nil {
		now := nowMs()
		t.startRequest(documentRequestID, now)
		t.document = &documentRequest{url: raw, startedAt: now, method: method}
	}
	return true
}

func (h *Host) started(view handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.views[view]; t != nil {
		t.requested = time.Time{}
	}
}

func (h *Host) response(view handle, raw string, status int, mainFrame bool) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	owner, t := h.ownerOf(view)
	if owner == nil {
		return false
	}
	if !mainFrame {
		return true
	}
	if isWebURL(raw) && !h.permitted(owner.taskID, raw) {
		h.allowNavigation(owner, raw)
		if t != nil {
			t.requested = time.Time{}
			t.document = nil
		}
		return false
	}
	if t != nil {
		var code *float64
		if status > 0 {
			code = ptr(float64(status))
		}
		t.finishDocument(raw, code, "")
	}
	return true
}

func (h *Host) committed(view handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.views[view]; t != nil {
		t.epoch++
		t.dropPending()
	}
}

func (h *Host) finished(view handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.views[view]; t != nil {
		t.requested = time.Time{}
	}
}

func (h *Host) failed(view handle, provisional bool, description, message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.views[view]
	if t == nil {
		return
	}
	t.requested = time.Time{}
	if provisional {
		t.finishDocument("", nil, description)
	}
	if message != "" {
		h.report(contracts.BrowserPageEvent{TabID: t.tabID, Loading: ptr(false), Error: ptr(message)})
	}
}

func (h *Host) terminated(view handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.views[view]; t != nil {
		t.requested = time.Time{}
		h.report(contracts.BrowserPageEvent{TabID: t.tabID, Loading: ptr(false), Error: ptr("The page stopped responding.")})
	}
}

func (h *Host) changed(view handle, key string, state pageState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := h.views[view]
	if t == nil {
		return
	}
	event := contracts.BrowserPageEvent{TabID: t.tabID}
	switch {
	case key == "loading" && state.Loading:
		event.Loading = ptr(true)
	case key == "title":
		event.Title = ptr(state.Title)
	default:
		if key == "loading" {
			t.requested = time.Time{}
			event.Loading = ptr(false)
		}
		event.URL, event.Title = ptr(state.URL), ptr(state.Title)
		event.CanGoBack, event.CanGoForward = ptr(state.CanGoBack), ptr(state.CanGoForward)
	}
	h.report(event)
}

func (h *Host) message(view handle, origin, body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t := h.views[view]; t != nil {
		t.record(origin, body, nowMs())
	}
}

func (h *Host) pressed(view handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if owner, _ := h.ownerOf(view); owner != nil {
		owner.taskID = nil
	}
}

func (h *Host) key(view handle, input KeyEvent) bool {
	if h.onKey != nil && h.onKey(input) {
		return true
	}
	if input.Type == "keyDown" {
		h.mu.Lock()
		if owner, _ := h.ownerOf(view); owner != nil {
			owner.taskID = nil
		}
		h.mu.Unlock()
	}
	return false
}

func (h *Host) popup(opener handle, raw string) (handle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	parent, _ := h.ownerOf(opener)
	if parent == nil || !h.allowNavigation(parent, raw) {
		return 0, false
	}
	h.next++
	h.popups[h.next] = &owner{tabID: parent.tabID, taskID: cloneString(parent.taskID)}
	return h.next, true
}

func (h *Host) popupClosed(popup handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.popups, popup)
}
