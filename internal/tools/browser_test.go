package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func browserSnapshot(edit func(*domain.BrowserSnapshot)) domain.BrowserSnapshot {
	snapshot := domain.BrowserSnapshot{
		TabID: "tab-1", URL: "https://example.com/pulls", Title: "Pull requests", Text: "Two open pull requests",
		Elements: []*domain.BrowserElement{{Ref: "1", Role: "input:text", Name: "Search", Value: ptr("is:open")}, {Ref: "2", Role: "button", Name: "New pull request"}},
	}
	if edit != nil {
		edit(&snapshot)
	}
	return snapshot
}

type fakeBrowser struct {
	calls   []any
	command func(contracts.BrowserWrite) error
	read    func(contracts.BrowserRead) (contracts.BrowserReadResult, error)
}

func (f *fakeBrowser) Command(_ context.Context, write contracts.BrowserWrite) error {
	f.calls = append(f.calls, []any{"command", write})
	if f.command != nil {
		return f.command(write)
	}
	return nil
}

func (f *fakeBrowser) Read(_ context.Context, read contracts.BrowserRead) (contracts.BrowserReadResult, error) {
	f.calls = append(f.calls, []any{"read", read})
	if f.read != nil {
		return f.read(read)
	}
	return contracts.BrowserReadResultSnapshot{Snapshot: browserSnapshot(nil)}, nil
}

func browserCall(t *testing.T, bridge BrowserBridge, name, args string) ToolResult {
	t.Helper()
	return callBound(t, BrowserToolsFor(bridge, false), name, args)
}

func TestOpeningAPageLoadsItAndReadsBackThePage(t *testing.T) {
	bridge := &fakeBrowser{}
	opened := textOf(browserCall(t, bridge, "browser_open", `{"url":"https://example.com/pulls","waitSeconds":5}`))
	assertJSON(t, bridge.calls, `[["command",{"type":"browser.open","url":"https://example.com/pulls"}],["read",{"op":"snapshot","timeoutMs":5000,"textLimit":4000}]]`)
	want := "Pull requests — https://example.com/pulls\n\nTwo open pull requests\n\nElements you can act on:\n[1] input:text \"Search\" = \"is:open\"\n[2] button \"New pull request\""
	if opened != want {
		t.Fatalf("%q", opened)
	}
}

func TestASearchOpensAPageThatAnswersOne(t *testing.T) {
	bridge := &fakeBrowser{}
	searched := browserCall(t, bridge, "browser_search", `{"query":"weather today","newTab":true}`)
	assertJSON(t, bridge.calls, `[["command",{"type":"browser.open","url":"https://duckduckgo.com/?q=weather%20today","newTab":true}],["read",{"op":"snapshot","timeoutMs":20000,"textLimit":4000}]]`)
	assertMatch(t, textOf(searched), `Pull requests — https://example\.com/pulls`)
}

func TestAWaitLongerThanThePanelAllowsIsCapped(t *testing.T) {
	bridge := &fakeBrowser{}
	browserCall(t, bridge, "browser_read", `{"waitSeconds":9000}`)
	assertJSON(t, bridge.calls, `[["read",{"op":"snapshot","timeoutMs":120000,"textLimit":4000}]]`)
}

func TestAPageWaitingOnTheUserReadsAsTheAsk(t *testing.T) {
	bridge := &fakeBrowser{read: func(contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		return contracts.BrowserReadResultAwaitingApproval{URL: "https://dash.example.com"}, nil
	}}
	opened := textOf(browserCall(t, bridge, "browser_open", `{"url":"https://dash.example.com"}`))
	assertMatch(t, opened, `asking the user to allow https://dash\.example\.com`)
	assertMatch(t, opened, `wait, or ask them to open it themselves`)
}

func TestTypingNamesTheFieldTheTextAndSubmitThenReadsThePage(t *testing.T) {
	bridge := &fakeBrowser{}
	browserCall(t, bridge, "browser_type", `{"ref":"1","text":"is:closed","submit":true,"tabId":"tab-2"}`)
	assertJSON(t, bridge.calls, `[["command",{"type":"browser.act","action":{"kind":"type","ref":"1","text":"is:closed","submit":true},"tabId":"tab-2"}],["read",{"op":"snapshot","tabId":"tab-2","timeoutMs":20000,"textLimit":4000}]]`)
}

func TestListingTabsSaysSoWhenThePanelHoldsNothing(t *testing.T) {
	empty := &fakeBrowser{read: func(contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		return contracts.BrowserReadResultTabs{}, nil
	}}
	assertMatch(t, textOf(browserCall(t, empty, "browser_tabs", `{}`)), `no tab open`)
	held := &fakeBrowser{read: func(contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		return contracts.BrowserReadResultTabs{Tabs: []*domain.BrowserTab{{ID: "tab-1", URL: "https://example.com", Title: "Example", Loading: true}}}, nil
	}}
	assertMatch(t, textOf(browserCall(t, held, "browser_tabs", `{}`)), `Example \[tab-1\] · https://example\.com · loading`)
}

func capturingBrowser(edit func(*domain.BrowserShot)) *fakeBrowser {
	return &fakeBrowser{read: func(contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		shot := domain.BrowserShot{TabID: "tab-1", URL: "https://example.com/", Title: "Example", Path: "/tmp/shot.png", Width: 800, Height: 600}
		if edit != nil {
			edit(&shot)
		}
		return contracts.BrowserReadResultShot{Shot: shot}, nil
	}}
}

func TestCapturingATabReportsTheFileItWentTo(t *testing.T) {
	bridge := capturingBrowser(func(s *domain.BrowserShot) {
		s.URL, s.Title, s.Path, s.Width, s.Height = "https://example.com/pulls", "Pull requests", "/tmp/aicodingtool-shots-a1/b2.png", 1_200, 2_400
	})
	captured := textOf(browserCall(t, bridge, "browser_screenshot", `{"tabId":"tab-1","fullPage":true,"waitSeconds":5}`))
	assertJSON(t, bridge.calls, `[["read",{"op":"screenshot","tabId":"tab-1","fullPage":true,"timeoutMs":5000}]]`)
	if captured != "Pull requests — https://example.com/pulls\nPicture saved to /tmp/aicodingtool-shots-a1/b2.png (1200x2400). Open that file to see the page." {
		t.Fatal(captured)
	}
	plain := capturingBrowser(nil)
	browserCall(t, plain, "browser_screenshot", `{}`)
	assertJSON(t, plain.calls, `[["read",{"op":"screenshot","timeoutMs":20000}]]`)
}

func TestConsoleAndNetworkReadsPassCursorsAndFormatDiagnostics(t *testing.T) {
	bridge := &fakeBrowser{read: func(read contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		if _, console := read.(contracts.BrowserReadConsole); console {
			return domain.BrowserConsoleSnapshot{TabID: "tab-1", URL: "https://example.com/app", Title: "App", LatestSequence: 9,
				Entries: []*domain.BrowserConsoleEntry{{Sequence: 9, At: 1, Level: domain.BrowserConsoleLevelError, Message: "render failed", Source: ptr("app.js"), Line: ptr(42.0)}}}, nil
		}
		return domain.BrowserNetworkSnapshot{TabID: "tab-1", URL: "https://example.com/app", Title: "App", LatestSequence: 5, Omitted: 2,
			Entries: []*domain.BrowserNetworkEntry{{Sequence: 5, StartedAt: 1, Method: "GET", URL: "https://example.com/api", ResourceType: "xhr", DurationMs: 83, Status: ptr(503.0)}}}, nil
	}}
	console := textOf(browserCall(t, bridge, "browser_console", `{"tabId":"tab-1","since":4,"minimumLevel":"warning"}`))
	network := textOf(browserCall(t, bridge, "browser_network", `{"tabId":"tab-1","since":2,"failuresOnly":true}`))
	assertJSON(t, bridge.calls, `[["read",{"op":"console","tabId":"tab-1","since":4,"minimumLevel":"warning"}],["read",{"op":"network","tabId":"tab-1","since":2,"failuresOnly":true}]]`)
	if console != "App — https://example.com/app\nConsole cursor: 9\n[9] ERROR render failed (app.js:42)" {
		t.Fatal(console)
	}
	if network != "App — https://example.com/app\nNetwork cursor: 5\n2 older matching entries omitted.\n[5] GET 503 83ms xhr https://example.com/api" {
		t.Fatal(network)
	}
}

func TestWaitingForAPageConditionReadsTheActionablePageAfterItMatches(t *testing.T) {
	bridge := &fakeBrowser{}
	bridge.read = func(read contracts.BrowserRead) (contracts.BrowserReadResult, error) {
		if _, wait := read.(contracts.BrowserReadWait); wait {
			return domain.BrowserWaitResult{TabID: "tab-1", URL: "https://example.com/app", Title: "App", Condition: "text", Value: ptr("Ready"), Matched: true, ElapsedMs: 120}, nil
		}
		return contracts.BrowserReadResultSnapshot{Snapshot: browserSnapshot(func(s *domain.BrowserSnapshot) { s.Text = "Ready" })}, nil
	}
	result := textOf(browserCall(t, bridge, "browser_wait", `{"condition":"text","value":" Ready ","tabId":"tab-1","timeoutSeconds":5}`))
	assertJSON(t, bridge.calls, `[["read",{"op":"wait","condition":"text","value":"Ready","tabId":"tab-1","timeoutMs":5000}],["read",{"op":"snapshot","tabId":"tab-1","timeoutMs":0,"textLimit":4000}]]`)
	assertMatch(t, result, `^Finished waiting for text "Ready" after 120ms\.\n\nPull requests[\s\S]*Ready`)
}

func TestAWaitRejectsAMissingTargetBeforeReachingTheBrowser(t *testing.T) {
	bridge := &fakeBrowser{}
	result := browserCall(t, bridge, "browser_wait", `{"condition":"element"}`)
	if !result.IsError || textOf(result) != "Browser error: element needs a value." || len(bridge.calls) != 0 {
		t.Fatalf("%+v %v", result, bridge.calls)
	}
	idle := browserCall(t, bridge, "browser_wait", `{"condition":"network-idle","value":" "}`)
	if textOf(idle) != "Browser error: network-idle takes no value." {
		t.Fatal(textOf(idle))
	}
}

func TestABrowserToolReportsWhatWentWrong(t *testing.T) {
	bridge := &fakeBrowser{command: func(contracts.BrowserWrite) error { return errors.New("The AICodingTool window is not open.") }}
	failed := browserCall(t, bridge, "browser_open", `{"url":"https://example.com"}`)
	if !failed.IsError || textOf(failed) != "Browser error: The AICodingTool window is not open." {
		t.Fatalf("%+v", failed)
	}
}

func TestACoordinatorReadsTheBrowserButNeverDrivesIt(t *testing.T) {
	role := domain.CoordinationRoleCoordinator
	tools, offered := BrowserServer.Serve(&Run{Browser: &fakeBrowser{}, CoordinationRole: &role})
	if !offered {
		t.Fatal("not offered")
	}
	for _, tool := range tools {
		if tool.Name == "browser_open" || tool.Name == "browser_click" {
			t.Fatalf("a coordinator drives the browser with %s", tool.Name)
		}
	}
	if len(tools) != 6 {
		t.Fatal(names(tools))
	}
}
