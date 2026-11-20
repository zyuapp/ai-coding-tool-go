package browser

import (
	"context"
	"encoding/binary"
	"encoding/json/jsontext"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

type fakePage struct {
	state  pageState
	shown  bool
	frame  rect
	closed bool
}

type fakeEngine struct {
	mu           sync.Mutex
	signals      signals
	pages        map[handle]*fakePage
	evaluated    int
	onEvaluate   func(view handle, script string) (jsontext.Value, error)
	onDraw       func(view handle) ([]byte, error)
	closedPopups []handle
	zoomFactor   float64
}

func newFakeEngine() *fakeEngine { return &fakeEngine{pages: map[handle]*fakePage{}, zoomFactor: 1} }

func (f *fakeEngine) page(view handle) *fakePage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pages[view]
}

func (f *fakeEngine) start(signals signals, _ string) error { f.signals = signals; return nil }
func (f *fakeEngine) stop()                                 {}
func (f *fakeEngine) open(view handle, width, height float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[view] = &fakePage{frame: rect{Width: width, Height: height}}
	return nil
}
func (f *fakeEngine) load(view handle, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[view].state.URL = url
	return nil
}
func (f *fakeEngine) goBack(handle)    {}
func (f *fakeEngine) goForward(handle) {}
func (f *fakeEngine) reload(handle)    {}
func (f *fakeEngine) close(view handle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page := f.pages[view]; page != nil {
		page.closed = true
	}
}
func (f *fakeEngine) closePopup(popup handle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedPopups = append(f.closedPopups, popup)
}
func (f *fakeEngine) place(view handle, shown bool, frame rect) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages[view].shown, f.pages[view].frame = shown, frame
}
func (f *fakeEngine) wake(handle)   {}
func (f *fakeEngine) zoom() float64 { return f.zoomFactor }
func (f *fakeEngine) state(view handle) pageState {
	f.mu.Lock()
	defer f.mu.Unlock()
	if page := f.pages[view]; page != nil {
		return page.state
	}
	return pageState{}
}
func (f *fakeEngine) evaluate(_ context.Context, view handle, script string) (jsontext.Value, error) {
	f.mu.Lock()
	f.evaluated++
	hook := f.onEvaluate
	f.mu.Unlock()
	if hook == nil {
		return jsontext.Value("null"), nil
	}
	return hook(view, script)
}
func (f *fakeEngine) draw(_ context.Context, view handle, _ bool, _, _ float64) ([]byte, error) {
	return f.onDraw(view)
}
func (f *fakeEngine) find(context.Context, handle, string, bool) bool { return true }
func (f *fakeEngine) focus(handle)                                    {}
func (f *fakeEngine) cookies(context.Context) ([]sessionCookie, error) {
	return nil, nil
}
func (f *fakeEngine) deleteCookie(context.Context, sessionCookie) error { return nil }
func (f *fakeEngine) setCookie(context.Context, CookieDetails) bool     { return true }
func (f *fakeEngine) clearData(context.Context) error                   { return nil }

type harness struct {
	t      *testing.T
	host   *Host
	engine *fakeEngine
	mu     sync.Mutex
	events []contracts.BrowserPageEvent
	finds  []contracts.BrowserFindEvent
	count  int
}

func newHarness(t *testing.T) *harness {
	engine := newFakeEngine()
	h := &harness{t: t, engine: engine}
	h.host = newHost(engine, Options{OnKey: func(input KeyEvent) bool { return input.Key == "Escape" }})
	if err := h.host.Start(); err != nil {
		t.Fatal(err)
	}
	h.host.OnPage(func(event contracts.BrowserPageEvent) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.events = append(h.events, event)
	})
	h.host.OnFind(func(event contracts.BrowserFindEvent) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.finds = append(h.finds, event)
	})
	t.Cleanup(h.host.Stop)
	return h
}

// delivered waits for every event reported so far to reach the listeners.
func (h *harness) delivered() []contracts.BrowserPageEvent {
	done := make(chan struct{})
	h.host.events.enqueue(func() { close(done) })
	<-done
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.events)
}

type testPage struct {
	id   string
	view handle
	h    *harness
}

func (p testPage) move(url string) {
	p.h.engine.mu.Lock()
	p.h.engine.pages[p.view].state.URL = url
	p.h.engine.mu.Unlock()
	p.h.host.committed(p.view)
}

func (h *harness) open(url string, taskID *string) testPage {
	h.count++
	id := "tab-" + string(rune('0'+h.count))
	h.host.ConfigurePermissions(domain.BrowserPermissions{Origins: []string{"https://allowed.example"}, AutonomousTaskIDs: []string{}})
	h.host.OpenTab(id, nil, taskID)
	t := h.host.tab(id)
	if t == nil {
		h.t.Fatal("no tab opened")
	}
	h.engine.mu.Lock()
	h.engine.pages[t.view].state = pageState{URL: url, Title: "Test page"}
	h.engine.mu.Unlock()
	return testPage{id: id, view: t.view, h: h}
}

func task() *string { return ptr("task") }

func expectApproval(t *testing.T, err error) {
	t.Helper()
	if err == nil || !regexp.MustCompile(`approval`).MatchString(err.Error()) {
		t.Fatalf("expected an approval error, got %v", err)
	}
}

func TestEveryAgentReadAndActionChecksTheActualNativeOrigin(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://private.example/", nil)
	ctx := context.Background()
	_, err := h.host.ReadPage(ctx, page.id, 1000, 0, task())
	expectApproval(t, err)
	_, err = h.host.CapturePage(ctx, page.id, false, 0, task())
	expectApproval(t, err)
	for _, inspection := range []domain.BrowserInspection{
		domain.BrowserInspectionConsole{},
		domain.BrowserInspectionNetwork{},
		domain.BrowserInspectionWait{Condition: domain.BrowserWaitConditionURL, Value: ptr("private"), TimeoutMs: 0},
	} {
		_, err := h.host.InspectPage(ctx, page.id, inspection, task())
		expectApproval(t, err)
	}
	_, err = h.host.Act(ctx, page.id, domain.BrowserActionClick{Ref: "1"}, task())
	expectApproval(t, err)
	if h.engine.evaluated != 0 {
		t.Fatalf("scripts ran %d times", h.engine.evaluated)
	}
	h.host.ConfigurePermissions(domain.BrowserPermissions{AutonomousTaskIDs: []string{"task"}})
	if result, err := h.host.InspectPage(ctx, page.id, domain.BrowserInspectionConsole{}, task()); err != nil {
		t.Fatal(err)
	} else if _, ok := result.(domain.BrowserConsoleSnapshot); !ok {
		t.Fatalf("got %#v", result)
	}
	h.host.ConfigurePermissions(domain.BrowserPermissions{})
	_, err = h.host.InspectPage(ctx, page.id, domain.BrowserInspectionConsole{}, task())
	expectApproval(t, err)
	if result, err := h.host.InspectPage(ctx, page.id, domain.BrowserInspectionConsole{}, nil); err != nil {
		t.Fatal(err)
	} else if _, ok := result.(domain.BrowserConsoleSnapshot); !ok {
		t.Fatal("user browsing remains available")
	}
}

func TestAgentNavigationAndPopupsCannotCarryAGrantToAnotherSite(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", task())
	if h.host.decide(page.view, "https://private.example/", "GET", targetPage) {
		t.Fatal("a run reached a site it was not granted")
	}
	events := h.delivered()
	last := events[len(events)-1]
	if last.NavigationRequest == nil || *last.NavigationRequest != (contracts.BrowserPageEventNavigationRequest{TaskID: "task", URL: "https://private.example/"}) {
		t.Fatalf("no navigation request: %#v", last)
	}
	if _, ok := h.host.popup(page.view, "https://private.example/popup"); ok {
		t.Fatal("a popup carried the grant")
	}
	if _, ok := h.host.popup(page.view, "https://allowed.example/popup"); !ok {
		t.Fatal("an allowed popup was refused")
	}
	if !h.host.key(page.view, KeyEvent{Type: "keyDown", KeyInput: domain.KeyInput{Key: "Escape"}}) {
		t.Fatal("the app's own key reached the page")
	}
	if h.host.decide(page.view, "https://private.example/", "GET", targetPage) {
		t.Fatal("a key the app took handed the page to the user")
	}
	if !h.host.decide(page.view, "https://allowed.example/next", "GET", targetPage) {
		t.Fatal("an allowed navigation was refused")
	}
	h.host.pressed(page.view)
	if _, ok := h.host.popup(page.view, "https://allowed.example/popup"); !ok {
		t.Fatal("an allowed popup was refused")
	}
	if !h.host.decide(page.view, "https://private.example/", "GET", targetPage) {
		t.Fatal("native user navigation retains its behavior")
	}
	if h.host.decide(page.view, "file:///etc/passwd", "GET", targetPage) {
		t.Fatal("a page left the web")
	}
	h.host.CloseTab(page.id)
}

func TestReadsDiscardADocumentChangedOrAGrantRevokedWhileAwaitingExecution(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	ctx := context.Background()
	h.engine.onEvaluate = func(handle, string) (jsontext.Value, error) {
		page.move("https://private.example/")
		return jsontext.Value(`{"url":"https://allowed.example/","title":"Old","text":"private text","elements":[]}`), nil
	}
	_, err := h.host.ReadPage(ctx, page.id, 1000, 0, task())
	expectApproval(t, err)
	page.move("https://allowed.example/")
	h.engine.onEvaluate = func(handle, string) (jsontext.Value, error) {
		h.host.ConfigurePermissions(domain.BrowserPermissions{})
		return jsontext.Value("true"), nil
	}
	_, err = h.host.InspectPage(ctx, page.id, domain.BrowserInspectionWait{Condition: domain.BrowserWaitConditionElement, Value: ptr("secret")}, task())
	expectApproval(t, err)
}

func TestARunReadsOnlyTheDocumentItWasGranted(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	h.engine.onEvaluate = func(_ handle, script string) (jsontext.Value, error) {
		if !strings.Contains(script, `["https://allowed.example"].includes(location.origin)`) {
			t.Fatal("the script does not check its own document")
		}
		return jsontext.Value(`{"url":"https://allowed.example/","title":"Page","text":"` + strings.Repeat("a", 10) + `","truncated":true,"elements":[{"ref":"1","role":"button","name":"Go"}]}`), nil
	}
	snapshot, err := h.host.ReadPage(context.Background(), page.id, 1000, 0, task())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Text != "aaaaaaaaaa\n… (page text truncated)" || len(snapshot.Elements) != 1 || snapshot.Elements[0].Name != "Go" {
		t.Fatalf("got %#v", snapshot)
	}
	h.engine.onEvaluate = func(_ handle, script string) (jsontext.Value, error) {
		if strings.Contains(script, "browser approval") {
			t.Fatal("the user's own read was guarded")
		}
		return jsontext.Value(`{"url":"https://private.example/","title":"","text":"","truncated":false,"elements":[]}`), nil
	}
	if _, err := h.host.ReadPage(context.Background(), page.id, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedDiagnosticsAreFilteredByTheDocumentThatProducedThem(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://private.example/", nil)
	h.host.message(page.view, "https://private.example", `{"k":"console","level":"info","message":"private token"}`)
	h.host.message(page.view, "https://private.example", `{"k":"net-start","id":42}`)
	page.move("https://allowed.example/")
	h.host.message(page.view, "https://private.example", `{"k":"net","id":42,"url":"https://private.example/token","status":200}`)
	h.host.message(page.view, "https://allowed.example", `{"k":"console","level":"info","message":"allowed log"}`)
	ctx := context.Background()
	console, err := h.host.InspectPage(ctx, page.id, domain.BrowserInspectionConsole{}, task())
	if err != nil {
		t.Fatal(err)
	}
	entries := console.(domain.BrowserConsoleSnapshot).Entries
	if len(entries) != 1 || entries[0].Message != "allowed log" {
		t.Fatalf("got %#v", entries)
	}
	network, err := h.host.InspectPage(ctx, page.id, domain.BrowserInspectionNetwork{}, task())
	if err != nil {
		t.Fatal(err)
	}
	if got := network.(domain.BrowserNetworkSnapshot).Entries; len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestScreenshotCaptureDiscardsChangedDocumentsAndPreservesAuthorizedCaptures(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	bytes := make([]byte, 24)
	binary.BigEndian.PutUint32(bytes[16:], 100)
	binary.BigEndian.PutUint32(bytes[20:], 50)
	change := false
	h.engine.onDraw = func(handle) ([]byte, error) {
		if change {
			page.move("https://private.example/")
		}
		return bytes, nil
	}
	shot, err := h.host.CapturePage(context.Background(), page.id, false, 0, task())
	if err != nil {
		t.Fatal(err)
	}
	if shot.Width != 100 || shot.Height != 50 {
		t.Fatalf("got %#v", shot)
	}
	written, err := os.ReadFile(shot.Path)
	if err != nil || string(written) != string(bytes) {
		t.Fatalf("the picture was not written: %v", err)
	}
	change = true
	_, err = h.host.CapturePage(context.Background(), page.id, false, 0, task())
	expectApproval(t, err)
}

func TestApprovedAgentPopupsRetainNavigationEnforcement(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", task())
	popup, ok := h.host.popup(page.view, "https://allowed.example/login")
	if !ok {
		t.Fatal("the allowed popup was refused")
	}
	if h.host.decide(popup, "https://private.example/", "GET", targetPage) {
		t.Fatal("the popup reached a site its run was not granted")
	}
	h.host.ConfigurePermissions(domain.BrowserPermissions{AutonomousTaskIDs: []string{"task"}})
	if _, ok := h.host.popup(popup, "https://private.example/nested"); !ok {
		t.Fatal("an autonomous popup was refused")
	}
	h.host.ConfigurePermissions(domain.BrowserPermissions{})
	if _, ok := h.host.popup(popup, "https://private.example/nested"); ok {
		t.Fatal("a revoked popup was allowed")
	}
	h.host.CloseTab(page.id)
	if !slices.Contains(h.engine.closedPopups, popup) {
		t.Fatal("the popup outlived its tab")
	}
}

func TestAClosedPagesLateEventsDoNotReachTheTabThatReopensUnderItsID(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	h.host.CloseTab(page.id)
	h.host.OpenTab(page.id, nil, nil)
	reopened := h.host.tab(page.id).view
	before := len(h.delivered())
	stale := page.view
	h.host.changed(stale, "loading", pageState{})
	h.host.committed(stale)
	h.host.changed(stale, "URL", pageState{URL: "https://allowed.example/"})
	h.host.changed(stale, "title", pageState{Title: "Stale"})
	h.host.failed(stale, false, "Failed", "Failed (https://allowed.example/)")
	h.host.terminated(stale)
	h.host.message(stale, "https://allowed.example", `{"k":"console","level":"info","message":"stale log"}`)
	if events := h.delivered(); len(events) != before {
		t.Fatalf("stale events arrived: %#v", events[before:])
	}
	console, err := h.host.InspectPage(context.Background(), page.id, domain.BrowserInspectionConsole{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if entries := console.(domain.BrowserConsoleSnapshot).Entries; len(entries) != 0 {
		t.Fatalf("got %#v", entries)
	}
	h.host.changed(reopened, "loading", pageState{Loading: true})
	events := h.delivered()[before:]
	if len(events) != 1 || events[0].TabID != page.id || events[0].Loading == nil || !*events[0].Loading || events[0].URL != nil {
		t.Fatalf("got %#v", events)
	}
}

func TestPagesReportTheirStateAsElectronDid(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	before := len(h.delivered())
	state := pageState{URL: "https://allowed.example/a", Title: "A", CanGoBack: true}
	h.host.changed(page.view, "loading", state)
	h.host.changed(page.view, "title", state)
	h.host.failed(page.view, true, "Not found", "Not found (https://allowed.example/a)")
	h.host.failed(page.view, true, "Cancelled", "")
	h.host.terminated(page.view)
	events := h.delivered()[before:]
	want := []contracts.BrowserPageEvent{
		{TabID: page.id, Loading: ptr(false), URL: ptr("https://allowed.example/a"), Title: ptr("A"), CanGoBack: ptr(true), CanGoForward: ptr(false)},
		{TabID: page.id, Title: ptr("A")},
		{TabID: page.id, Loading: ptr(false), Error: ptr("Not found (https://allowed.example/a)")},
		{TabID: page.id, Loading: ptr(false), Error: ptr("The page stopped responding.")},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events", len(events))
	}
	for index := range want {
		got, _ := jsonMarshal(events[index])
		expected, _ := jsonMarshal(want[index])
		if got != expected {
			t.Fatalf("event %d: got %s want %s", index, got, expected)
		}
	}
}

func TestANavigationTheEngineCannotPlaceIsCheckedAtItsResponse(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", task())
	if !h.host.decide(page.view, "https://private.example/frame", "GET", targetUnknown) {
		t.Fatal("a possible frame was refused before its response")
	}
	if h.host.decide(page.view, "file:///etc/passwd", "GET", targetUnknown) {
		t.Fatal("a page left the web")
	}
	if !h.host.decide(page.view, "data:text/html,x", "GET", targetUnknown) {
		t.Fatal("a frame's data document was refused")
	}
	if h.host.response(page.view, "https://private.example/frame", 200, true) {
		t.Fatal("the page reached a site its run was not granted")
	}
	events := h.delivered()
	if last := events[len(events)-1]; last.NavigationRequest == nil || last.NavigationRequest.URL != "https://private.example/frame" {
		t.Fatalf("got %#v", last)
	}
	if !h.host.response(page.view, "https://private.example/frame", 200, false) {
		t.Fatal("a frame's response was refused")
	}
}

func TestTheMainDocumentIsRecordedAsANetworkEntry(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	h.host.decide(page.view, "https://allowed.example/next", "GET", targetPage)
	h.host.response(page.view, "https://allowed.example/next", 404, true)
	h.host.decide(page.view, "https://allowed.example/gone", "POST", targetPage)
	h.host.failed(page.view, true, "Could not connect", "Could not connect (https://allowed.example/gone)")
	result, err := h.host.InspectPage(context.Background(), page.id, domain.BrowserInspectionNetwork{FailuresOnly: ptr(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries := result.(domain.BrowserNetworkSnapshot).Entries
	if len(entries) != 2 || *entries[0].Status != 404 || entries[0].ResourceType != "mainFrame" || entries[1].Method != "POST" || *entries[1].Error != "Could not connect" {
		t.Fatalf("got %#v", entries)
	}
}

func TestOnlyTheShownTabIsPlacedOverThePanel(t *testing.T) {
	h := newHarness(t)
	h.engine.zoomFactor = 1.25
	first := h.open("https://allowed.example/", nil)
	second := h.open("https://allowed.example/", nil)
	h.host.ShowTab(&first.id)
	if h.engine.page(first.view).shown {
		t.Fatal("a tab was drawn before the panel said where it is")
	}
	h.host.SetBounds(&domain.BrowserBounds{X: 10, Y: 20, Width: 301, Height: 401})
	if got := h.engine.page(first.view); !got.shown || got.frame != (rect{X: 13, Y: 25, Width: 376, Height: 501}) {
		t.Fatalf("got %#v", got)
	}
	if got := h.engine.page(second.view); got.shown || got.frame != (rect{Width: 376, Height: 501}) {
		t.Fatalf("got %#v", got)
	}
	h.host.SetBounds(nil)
	if got := h.engine.page(first.view); got.shown || got.frame != parkedViewport {
		t.Fatalf("got %#v", got)
	}
}

func TestFindStepsThroughTheMatchesItCounted(t *testing.T) {
	h := newHarness(t)
	page := h.open("https://allowed.example/", nil)
	h.engine.onEvaluate = func(_ handle, script string) (jsontext.Value, error) {
		if strings.Contains(script, "indexOf") {
			return jsontext.Value("3"), nil
		}
		return jsontext.Value("null"), nil
	}
	ctx := context.Background()
	h.host.FindInPage(ctx, page.id, "word", true, false)
	h.host.FindInPage(ctx, page.id, "word", true, true)
	h.host.FindInPage(ctx, page.id, "word", false, true)
	h.host.FindInPage(ctx, page.id, "word", false, true)
	h.host.FindInPage(ctx, page.id, "other", false, true)
	h.delivered()
	h.mu.Lock()
	defer h.mu.Unlock()
	indices := []float64{}
	for _, find := range h.finds {
		indices = append(indices, *find.Index)
		if find.Matches != 3 {
			t.Fatalf("got %#v", find)
		}
	}
	if !slices.Equal(indices, []float64{0, 1, 0, 2, 2}) {
		t.Fatalf("got %v", indices)
	}
}

func TestDiagnosticsKeepTheNewestAndCountTheRest(t *testing.T) {
	entries := []*domain.BrowserConsoleEntry{}
	for sequence := 1.0; sequence <= 10; sequence++ {
		entries = append(entries, &domain.BrowserConsoleEntry{Sequence: sequence})
	}
	kept, omitted := newest(entries, 2, 3, func(entry *domain.BrowserConsoleEntry) bool { return int(entry.Sequence)%2 == 0 })
	sequences := []float64{}
	for _, entry := range kept {
		sequences = append(sequences, entry.Sequence)
	}
	if !slices.Equal(sequences, []float64{6, 8, 10}) || omitted != 1 {
		t.Fatalf("got %v, %d", sequences, omitted)
	}
	history := newDiagnostics(0)
	for index := range maxDiagnosticEntries + 5 {
		history.log("https://a.com", float64(index), domain.BrowserConsoleLevelInfo, "x", nil, nil)
	}
	if len(history.console) != maxDiagnosticEntries || history.console[0].entry.Sequence != 6 {
		t.Fatalf("history is not bounded: %d", len(history.console))
	}
	if clip("a😀b", 2) != "a" || clip("a😀b", 3) != "a😀" {
		t.Fatal("clip counts UTF-16 units")
	}
}

func TestTheShotFolderKeepsOnlyTheNewestPictures(t *testing.T) {
	var held shots
	first, err := held.write([]byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	for range maxShots {
		if _, err := held.write([]byte("b")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("the oldest picture was kept")
	}
	files, _ := os.ReadDir(held.home)
	if len(files) != maxShots {
		t.Fatalf("got %d files", len(files))
	}
	home := held.home
	held.discard()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("the folder outlived the host")
	}
}

func TestScriptsMatchTheElectronApp(t *testing.T) {
	click := actionScript(domain.BrowserActionClick{Ref: "4"})
	if !strings.Contains(click, `document.querySelector('[data-aicodingtool-ref=' + JSON.stringify("4") + ']')`) || !strings.Contains(click, "node.click();") {
		t.Fatal(click)
	}
	typed := actionScript(domain.BrowserActionType{Ref: "2", Text: `a"b`, Submit: ptr(true)})
	if !strings.Contains(typed, `setter.call(node, "a\"b")`) || !strings.Contains(typed, "requestSubmit") {
		t.Fatal(typed)
	}
	if got := pageConditionScript(domain.BrowserWaitConditionTextGone, "Hello"); got != `!(document.body?.innerText || '').toLocaleLowerCase().includes("hello")` {
		t.Fatal(got)
	}
	if !strings.HasPrefix(pageConditionScript(domain.BrowserWaitConditionElementGone, "x"), "!(Array.from(") {
		t.Fatal("element-gone negates the search")
	}
	if guardedScript("1", nil) != "1" {
		t.Fatal("an unguarded script changed")
	}
	if !strings.Contains(snapshotScript(250), "const limit = 250;") {
		t.Fatal("the limit is not in the snapshot")
	}
}

func jsonMarshal(value any) (string, error) {
	data, err := jsonx.Marshal(value)
	return string(data), err
}
