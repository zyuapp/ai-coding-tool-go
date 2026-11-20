package browser

import (
	"strconv"
	"unicode/utf16"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const (
	// maxDiagnosticEntries and maxDiagnosticText keep developer diagnostics useful without letting a
	// noisy page retain memory without limit.
	maxDiagnosticEntries = 200
	maxDiagnosticText    = 4_000
	networkIdleMs        = 500
	documentRequestID    = "document"
)

// recorded is an entry and the origin of the page that produced it, which decides who may read it.
type recorded[T any] struct {
	pageOrigin string
	entry      T
}

// diagnostics is a tab's console and network history.
type diagnostics struct {
	console         []recorded[domain.BrowserConsoleEntry]
	network         []recorded[domain.BrowserNetworkEntry]
	consoleSequence float64
	networkSequence float64
	// pending is requests started and not yet finished. A live socket is never one, since it may stay open for hours.
	pending       map[string]bool
	lastNetworkAt float64
}

func newDiagnostics(now float64) diagnostics {
	return diagnostics{pending: map[string]bool{}, lastNetworkAt: now}
}

func boundedPush[T any](entries []T, entry T) []T {
	entries = append(entries, entry)
	if len(entries) > maxDiagnosticEntries {
		entries = append(entries[:0:0], entries[len(entries)-maxDiagnosticEntries:]...)
	}
	return entries
}

// clip is String.prototype.slice(0, limit), which counts UTF-16 code units.
func clip(text string, limit int) string {
	units := 0
	for at, r := range text {
		size := utf16.RuneLen(r)
		if size < 0 {
			size = 1
		}
		if units+size > limit {
			return text[:at]
		}
		units += size
	}
	return text
}

func (d *diagnostics) log(origin string, at float64, level domain.BrowserConsoleLevel, message string, source *string, line *float64) {
	d.consoleSequence++
	entry := domain.BrowserConsoleEntry{Sequence: d.consoleSequence, At: at, Level: level, Message: clip(message, maxDiagnosticText)}
	if source != nil && *source != "" {
		entry.Source = ptr(clip(*source, maxDiagnosticText))
	}
	if line != nil && *line > 0 {
		entry.Line = ptr(*line)
	}
	d.console = boundedPush(d.console, recorded[domain.BrowserConsoleEntry]{origin, entry})
}

func (d *diagnostics) startRequest(id string, now float64) {
	d.pending[id] = true
	d.lastNetworkAt = now
}

type finishedRequest struct {
	id                        string
	startedAt                 float64
	method, url, resourceKind string
	durationMs                float64
	status                    *float64
	error                     string
	fromCache                 *bool
}

func (d *diagnostics) finishRequest(origin string, now float64, request finishedRequest) {
	if request.id != "" {
		delete(d.pending, request.id)
	}
	d.lastNetworkAt = now
	d.networkSequence++
	entry := domain.BrowserNetworkEntry{
		Sequence:     d.networkSequence,
		StartedAt:    request.startedAt,
		Method:       request.method,
		URL:          request.url,
		ResourceType: request.resourceKind,
		DurationMs:   request.durationMs,
		Status:       request.status,
		FromCache:    request.fromCache,
	}
	if request.error != "" {
		entry.Error = ptr(clip(request.error, maxDiagnosticText))
	}
	d.network = boundedPush(d.network, recorded[domain.BrowserNetworkEntry]{origin, entry})
}

// dropPending forgets requests a document left unfinished, which cannot finish once it is gone.
func (d *diagnostics) dropPending() { clear(d.pending) }

func (d *diagnostics) idle(now float64) bool {
	return len(d.pending) == 0 && now-d.lastNetworkAt >= networkIdleMs
}

// newest is the newest limit entries past since that keep accepts, and how many older ones matched too.
func newest[T interface {
	*domain.BrowserConsoleEntry | *domain.BrowserNetworkEntry
}](entries []T, since float64, limit int, keep func(T) bool) ([]T, int) {
	matching := []T{}
	for _, entry := range entries {
		if sequenceOf(entry) > since && keep(entry) {
			matching = append(matching, entry)
		}
	}
	skip := max(0, len(matching)-limit)
	return matching[skip:], skip
}

func sequenceOf(entry any) float64 {
	switch entry := entry.(type) {
	case *domain.BrowserConsoleEntry:
		return entry.Sequence
	case *domain.BrowserNetworkEntry:
		return entry.Sequence
	}
	return 0
}

// finishDocument records the top document's request once the engine has its answer or gives up on it.
func (t *tab) finishDocument(raw string, status *float64, failure string) {
	request := t.document
	if request == nil {
		return
	}
	t.document = nil
	if raw == "" {
		raw = request.url
	}
	now := nowMs()
	t.finishRequest(originOf(raw), now, finishedRequest{
		id: documentRequestID, startedAt: request.startedAt, method: request.method, url: raw, resourceKind: "mainFrame",
		durationMs: max(0, now-request.startedAt), status: status, error: failure,
	})
}

type pageMessage struct {
	Kind     string   `json:"k"`
	Level    string   `json:"level"`
	Message  string   `json:"message"`
	Source   *string  `json:"source"`
	Line     *float64 `json:"line"`
	ID       *float64 `json:"id"`
	Method   *string  `json:"method"`
	URL      string   `json:"url"`
	Type     *string  `json:"type"`
	Started  *float64 `json:"started"`
	Duration *float64 `json:"duration"`
	Status   *float64 `json:"status"`
	Error    string   `json:"error"`
	Cached   *bool    `json:"cached"`
}

// record keeps what the page's hooks posted: a console line, or a request starting or finishing.
func (t *tab) record(origin, body string, now float64) {
	var message pageMessage
	if jsonx.Unmarshal([]byte(body), &message) != nil {
		return
	}
	switch message.Kind {
	case "console":
		level := domain.BrowserConsoleLevel(message.Level)
		if _, ok := consoleRanks[level]; !ok {
			level = domain.BrowserConsoleLevelInfo
		}
		t.log(origin, now, level, message.Message, message.Source, message.Line)
	case "net-start":
		if message.ID != nil {
			t.startRequest(pageRequestID(*message.ID), now)
		}
	case "net":
		request := finishedRequest{startedAt: now, method: "GET", url: message.URL, resourceKind: "other", error: message.Error, fromCache: message.Cached}
		if message.ID != nil {
			request.id = pageRequestID(*message.ID)
		}
		if message.Started != nil {
			request.startedAt = *message.Started
		}
		if message.Method != nil {
			request.method = *message.Method
		}
		if message.Type != nil {
			request.resourceKind = *message.Type
		}
		if message.Duration != nil && *message.Duration > 0 {
			request.durationMs = *message.Duration
		}
		if message.Status != nil && *message.Status > 0 {
			request.status = message.Status
		}
		t.finishRequest(origin, now, request)
	}
}

func pageRequestID(id float64) string { return "page:" + strconv.FormatFloat(id, 'f', -1, 64) }
