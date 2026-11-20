package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const BrowserServerName = "aicodingtool-browser"

// BrowserBridge is the browser panel, driven as the thread that is running: writes are commands, reads are the page.
type BrowserBridge interface {
	Command(ctx context.Context, write contracts.BrowserWrite) error
	Read(ctx context.Context, read contracts.BrowserRead) (contracts.BrowserReadResult, error)
}

const (
	browserDefaultWaitMs    = 20_000
	browserDefaultTextLimit = 4_000
)

// Every field is held to the limit the window reads it under, so one it would refuse is refused here, on its own field.
var (
	browserTabField = String().Max(contracts.IpcMaxIDLength).Optional().Describe("Which tab to act in. Defaults to the one the panel is showing.")
	browserRefField = String().Min(1).Max(contracts.IpcMaxIDLength).Describe("The ref of the element, from the latest snapshot of that tab.")
)

func browserWaitMs(seconds *float64) float64 {
	wait := float64(browserDefaultWaitMs)
	if seconds != nil {
		wait = max(0, *seconds) * 1_000
	}
	return min(wait, contracts.MaxBrowserWaitMs)
}

func untitled(title string) string {
	if title == "" {
		return "Untitled"
	}
	return title
}

func browserSnapshotText(snapshot *domain.BrowserSnapshot) string {
	elements := make([]string, len(snapshot.Elements))
	for index, element := range snapshot.Elements {
		value := ""
		if element.Value != nil {
			value = " = " + jsonString(*element.Value)
		}
		elements[index] = fmt.Sprintf("[%s] %s %s%s", element.Ref, element.Role, jsonString(element.Name), value)
	}
	loading := ""
	if snapshot.Loading {
		loading = " (still loading)"
	}
	text := snapshot.Text
	if text == "" {
		text = "(the page has no text)"
	}
	acting := "Nothing on this page can be clicked or typed into."
	if len(elements) > 0 {
		acting = "Elements you can act on:\n" + strings.Join(elements, "\n")
	}
	return strings.Join([]string{untitled(snapshot.Title) + " — " + snapshot.URL + loading, "", text, "", acting}, "\n")
}

func browserShotText(shot *domain.BrowserShot) string {
	return strings.Join([]string{
		untitled(shot.Title) + " — " + shot.URL,
		fmt.Sprintf("Picture saved to %s (%sx%s). Open that file to see the page.", shot.Path, jsNumber(shot.Width), jsNumber(shot.Height)),
	}, "\n")
}

func browserConsoleText(snapshot *domain.BrowserConsoleSnapshot) string {
	lines := []string{untitled(snapshot.Title) + " — " + snapshot.URL, "Console cursor: " + jsNumber(snapshot.LatestSequence)}
	if snapshot.Omitted != 0 {
		lines = append(lines, jsNumber(snapshot.Omitted)+" older matching entries omitted.")
	}
	entries := make([]string, len(snapshot.Entries))
	for index, entry := range snapshot.Entries {
		source := ""
		if entry.Source != nil && *entry.Source != "" {
			line := ""
			if entry.Line != nil && *entry.Line != 0 {
				line = ":" + jsNumber(*entry.Line)
			}
			source = " (" + *entry.Source + line + ")"
		}
		entries[index] = fmt.Sprintf("[%s] %s %s%s", jsNumber(entry.Sequence), strings.ToUpper(string(entry.Level)), entry.Message, source)
	}
	if len(entries) > 0 {
		lines = append(lines, strings.Join(entries, "\n"))
	} else {
		lines = append(lines, "No matching console messages.")
	}
	return strings.Join(lines, "\n")
}

func browserNetworkText(snapshot *domain.BrowserNetworkSnapshot) string {
	lines := []string{untitled(snapshot.Title) + " — " + snapshot.URL, "Network cursor: " + jsNumber(snapshot.LatestSequence)}
	if snapshot.Omitted != 0 {
		lines = append(lines, jsNumber(snapshot.Omitted)+" older matching entries omitted.")
	}
	entries := make([]string, len(snapshot.Entries))
	for index, entry := range snapshot.Entries {
		result := "unknown"
		if entry.Error != nil {
			result = *entry.Error
		} else if entry.Status != nil {
			result = jsNumber(*entry.Status)
		}
		cached := ""
		if entry.FromCache != nil && *entry.FromCache {
			cached = " cached"
		}
		entries[index] = fmt.Sprintf("[%s] %s %s %sms %s%s %s", jsNumber(entry.Sequence), entry.Method, result, jsNumber(entry.DurationMs), entry.ResourceType, cached, entry.URL)
	}
	if len(entries) > 0 {
		lines = append(lines, strings.Join(entries, "\n"))
	} else {
		lines = append(lines, "No matching completed requests.")
	}
	return strings.Join(lines, "\n")
}

func browserWaitText(result *domain.BrowserWaitResult) string {
	target := string(result.Condition)
	if result.Value != nil {
		target += " " + jsonString(*result.Value)
	}
	if result.Matched {
		return fmt.Sprintf("Finished waiting for %s after %sms.", target, jsNumber(result.ElapsedMs))
	}
	return fmt.Sprintf("Timed out waiting for %s after %sms. The page is %s.", target, jsNumber(result.ElapsedMs), result.URL)
}

// browserReadText answers every read the same way, so a page nobody has allowed yet reads as the ask it is.
func browserReadText(result contracts.BrowserReadResult) string {
	switch result := result.(type) {
	case contracts.BrowserReadResultSnapshot:
		return browserSnapshotText(&result.Snapshot)
	case contracts.BrowserReadResultShot:
		return browserShotText(&result.Shot)
	case domain.BrowserConsoleSnapshot:
		return browserConsoleText(&result)
	case domain.BrowserNetworkSnapshot:
		return browserNetworkText(&result)
	case domain.BrowserWaitResult:
		return browserWaitText(&result)
	case contracts.BrowserReadResultTabs:
		if len(result.Tabs) == 0 {
			return "The browser panel has no tab open."
		}
		lines := make([]string, len(result.Tabs))
		for index, tab := range result.Tabs {
			lines[index] = domain.DescribeTab(tab)
		}
		return strings.Join(lines, "\n")
	case contracts.BrowserReadResultAwaitingApproval:
		return "AICodingTool is asking the user to allow " + result.URL + ". Nothing loads until they answer, so tell them what you need it for and wait, or ask them to open it themselves."
	}
	return "The browser panel has no tab open."
}

func browserReport(work func() (string, error)) ToolResult { return reportAs("Browser error: ", work) }

func nonEmpty(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}

func browserRead(ctx context.Context, bridge BrowserBridge, read contracts.BrowserRead) (string, error) {
	result, err := bridge.Read(ctx, read)
	if err != nil {
		return "", err
	}
	return browserReadText(result), nil
}

// browserSettledPage reads after a write, which is what the caller wants: the page as it stands once the action settled.
func browserSettledPage(ctx context.Context, bridge BrowserBridge, tabID string, seconds *float64) (string, error) {
	textLimit := float64(browserDefaultTextLimit)
	return browserRead(ctx, bridge, contracts.BrowserReadSnapshot{TabID: nonEmpty(tabID), TimeoutMs: browserWaitMs(seconds), TextLimit: &textLimit})
}

func trueOrNil(value bool) *bool {
	if !value {
		return nil
	}
	return &value
}

func browserOpen(ctx context.Context, bridge BrowserBridge, url string, args Args) (string, error) {
	newTab := args.Bool("newTab")
	if err := bridge.Command(ctx, contracts.BrowserWriteBrowserOpen{URL: url, NewTab: trueOrNil(newTab), TabID: nonEmpty(args.Str("tabId"))}); err != nil {
		return "", err
	}
	tabID := args.Str("tabId")
	if newTab {
		tabID = ""
	}
	return browserSettledPage(ctx, bridge, tabID, args.OptNum("waitSeconds"))
}

var BrowserTools = []Definition[BrowserBridge]{
	{
		Name:        "browser_open",
		Description: "Open a page in the AICodingTool browser panel and read it back. The panel shares one browser session with the user, so any site they are signed into is already signed in here, and they watch the same tabs you drive: leave the pages they opened alone. A page the user has never visited is theirs to allow first, so say what you need and wait rather than retrying. To search, use browser_search.",
		Input: Shape{
			{"url", String().Min(1).Max(contracts.MaxURLLength).Describe("The page to open. Include the scheme for anything that is not an ordinary domain.")},
			{"newTab", Boolean().Optional().Describe("Open another tab instead of reusing the one on screen.")},
			{"tabId", browserTabField},
			{"waitSeconds", Number().Optional().Describe("How long to wait for the page to finish loading. Defaults to 20.")},
		},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) { return browserOpen(ctx, bridge, args.Str("url"), args) })
		},
	},
	{
		Name:        "browser_search",
		Description: "Search the web in the AICodingTool browser panel and read the results. Use this rather than opening a search engine yourself: Google sometimes answers the panel with its bot page instead of results.",
		Input: Shape{
			{"query", String().Refine(func(query any) bool {
				text, _ := query.(string)
				return utf16Len(domain.BrowserSearchURL(text)) <= contracts.MaxURLLength
			}, "Too long to search for.").Describe("What to search for.")},
			{"newTab", Boolean().Optional().Describe("Search in another tab instead of the one on screen.")},
			{"tabId", browserTabField},
			{"waitSeconds", Number().Optional().Describe("How long to wait for the results to load. Defaults to 20.")},
		},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				return browserOpen(ctx, bridge, domain.BrowserSearchURL(args.Str("query")), args)
			})
		},
	},
	{
		Name:        "browser_read",
		Description: "Read the page a tab is showing: its text, and the elements you can act on. Act on the refs this hands back; they are only valid until the next read of that tab, so read again after anything changes the page.",
		Input: Shape{
			{"tabId", browserTabField},
			{"textLimit", Number().Nonnegative().Optional().Describe("How many characters of page text to return. Defaults to 4000.")},
			{"waitSeconds", Number().Optional().Describe("How long to wait for the page to settle first. Defaults to 20.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				textLimit := float64(browserDefaultTextLimit)
				if limit := args.OptNum("textLimit"); limit != nil {
					textLimit = *limit
				}
				return browserRead(ctx, bridge, contracts.BrowserReadSnapshot{TabID: nonEmpty(args.Str("tabId")), TimeoutMs: browserWaitMs(args.OptNum("waitSeconds")), TextLimit: &textLimit})
			})
		},
	},
	{
		Name:        "browser_screenshot",
		Description: "Take a picture of a tab and read back where it was saved. Use it to check how a page looks, which the page text cannot tell you. The picture is a PNG file on this machine, so open that file to see it. The tab does not have to be the one on screen: a page the panel is not showing is captured just the same, and the user sees nothing move.",
		Input: Shape{
			{"tabId", browserTabField},
			{"fullPage", Boolean().Optional().Describe("Capture the whole page instead of only the part that fits the window.")},
			{"waitSeconds", Number().Optional().Describe("How long to wait for the page to settle first. Defaults to 20.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				return browserRead(ctx, bridge, contracts.BrowserReadScreenshot{TabID: nonEmpty(args.Str("tabId")), FullPage: trueOrNil(args.Bool("fullPage")), TimeoutMs: browserWaitMs(args.OptNum("waitSeconds"))})
			})
		},
	},
	{
		Name:        "browser_console",
		Description: "Read recent console messages from a tab, including runtime errors and rejected promises Chromium reports there. Each answer includes a cursor; pass it as since next time to read only newer messages.",
		Input: Shape{
			{"tabId", browserTabField},
			{"since", Number().Int().Nonnegative().Optional().Describe("Return messages after this console cursor.")},
			{"limit", Number().Int().Min(1).Max(200).Optional().Describe("Maximum messages to return. Defaults to 50.")},
			{"minimumLevel", Enum("debug", "info", "warning", "error").Optional().Describe("Lowest severity to return. Defaults to debug.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				read := contracts.BrowserReadConsole{TabID: nonEmpty(args.Str("tabId")), Since: args.OptNum("since"), Limit: args.OptNum("limit")}
				if level := args.OptStr("minimumLevel"); level != nil {
					minimum := domain.BrowserConsoleLevel(*level)
					read.MinimumLevel = &minimum
				}
				return browserRead(ctx, bridge, read)
			})
		},
	},
	{
		Name:        "browser_network",
		Description: "Read recent completed requests from a tab with method, status or error, resource type, cache use, and duration. Each answer includes a cursor; pass it as since next time to read only newer requests.",
		Input: Shape{
			{"tabId", browserTabField},
			{"since", Number().Int().Nonnegative().Optional().Describe("Return requests after this network cursor.")},
			{"limit", Number().Int().Min(1).Max(200).Optional().Describe("Maximum requests to return. Defaults to 50.")},
			{"failuresOnly", Boolean().Optional().Describe("Return only errors and responses with status 400 or higher.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				return browserRead(ctx, bridge, contracts.BrowserReadNetwork{TabID: nonEmpty(args.Str("tabId")), Since: args.OptNum("since"), Limit: args.OptNum("limit"), FailuresOnly: trueOrNil(args.Bool("failuresOnly"))})
			})
		},
	},
	{
		Name:        "browser_wait",
		Description: "Wait until page text, an element name, or the URL contains a value; until text or an element disappears; or until network activity has stopped for 500ms. Reads the page after a successful wait.",
		Input: Shape{
			{"condition", Enum("text", "text-gone", "element", "element-gone", "url", "network-idle")},
			{"value", String().Max(contracts.MaxSearchText).Optional().Describe("Text to find. Omit only for network-idle.")},
			{"tabId", browserTabField},
			{"timeoutSeconds", Number().Optional().Describe("How long to wait. Defaults to 20.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				condition := args.Str("condition")
				var value *string
				if raw := args.OptStr("value"); raw != nil {
					trimmed := jsTrim(*raw)
					value = &trimmed
				}
				if condition == "network-idle" && value != nil {
					return "", errors.New("network-idle takes no value.")
				}
				if condition != "network-idle" && (value == nil || *value == "") {
					return "", errors.New(condition + " needs a value.")
				}
				result, err := bridge.Read(ctx, contracts.BrowserReadWait{
					Condition: domain.BrowserWaitCondition(condition),
					Value:     value,
					TabID:     nonEmpty(args.Str("tabId")),
					TimeoutMs: browserWaitMs(args.OptNum("timeoutSeconds")),
				})
				if err != nil {
					return "", err
				}
				waited, ok := result.(domain.BrowserWaitResult)
				if !ok || !waited.Matched {
					return browserReadText(result), nil
				}
				zero := 0.0
				page, err := browserSettledPage(ctx, bridge, args.Str("tabId"), &zero)
				if err != nil {
					return "", err
				}
				return browserWaitText(&waited) + "\n\n" + page, nil
			})
		},
	},
	{
		Name:        "browser_click",
		Description: "Click an element in the page and read back what it became.",
		Input: Shape{
			{"ref", browserRefField},
			{"tabId", browserTabField},
			{"waitSeconds", Number().Optional().Describe("How long to wait for the click's page load. Defaults to 20.")},
		},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				if err := bridge.Command(ctx, contracts.BrowserWriteBrowserAct{Action: domain.BrowserActionClick{Ref: args.Str("ref")}, TabID: nonEmpty(args.Str("tabId"))}); err != nil {
					return "", err
				}
				return browserSettledPage(ctx, bridge, args.Str("tabId"), args.OptNum("waitSeconds"))
			})
		},
	},
	{
		Name:        "browser_type",
		Description: "Type into a field in the page, optionally submitting it, then read back what it became.",
		Input: Shape{
			{"ref", browserRefField},
			{"text", String().Max(contracts.IpcMaxPromptLength).Describe("What the field should contain. This replaces whatever it holds.")},
			{"submit", Boolean().Optional().Describe("Submit the field's form afterwards, the way pressing Enter would.")},
			{"tabId", browserTabField},
			{"waitSeconds", Number().Optional().Describe("How long to wait for a page load the submit causes. Defaults to 20.")},
		},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				action := domain.BrowserActionType{Ref: args.Str("ref"), Text: args.Str("text"), Submit: trueOrNil(args.Bool("submit"))}
				if err := bridge.Command(ctx, contracts.BrowserWriteBrowserAct{Action: action, TabID: nonEmpty(args.Str("tabId"))}); err != nil {
					return "", err
				}
				return browserSettledPage(ctx, bridge, args.Str("tabId"), args.OptNum("waitSeconds"))
			})
		},
	},
	{
		Name:        "browser_back",
		Description: "Go back, or forward, in a tab's own history and read the page it lands on.",
		Input: Shape{
			{"forward", Boolean().Optional().Describe("Go forward instead of back.")},
			{"tabId", browserTabField},
		},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				delta := -1.0
				if args.Bool("forward") {
					delta = 1
				}
				if err := bridge.Command(ctx, contracts.BrowserWriteBrowserGo{Delta: delta, TabID: nonEmpty(args.Str("tabId"))}); err != nil {
					return "", err
				}
				return browserSettledPage(ctx, bridge, args.Str("tabId"), nil)
			})
		},
	},
	{
		Name:        "browser_tabs",
		Description: "List the pages the browser panel has open. Use it to find the tab id of a page the user is looking at.",
		Input:       Shape{},
		ReadOnly:    true,
		Run: func(ctx context.Context, bridge BrowserBridge, _ Args) ToolResult {
			return browserReport(func() (string, error) { return browserRead(ctx, bridge, contracts.BrowserReadTabs{}) })
		},
	},
	{
		Name:        "browser_close_tab",
		Description: "Close a tab in the browser panel. Only close tabs you opened; the user's own pages are theirs.",
		Input:       Shape{{"tabId", String().Min(1).Max(contracts.IpcMaxIDLength).Describe("The tab to close, as browser_tabs reports it.")}},
		Run: func(ctx context.Context, bridge BrowserBridge, args Args) ToolResult {
			return browserReport(func() (string, error) {
				if err := bridge.Command(ctx, contracts.BrowserWriteBrowserCloseTab{TabID: args.Str("tabId")}); err != nil {
					return "", err
				}
				return browserRead(ctx, bridge, contracts.BrowserReadTabs{})
			})
		},
	},
}

// BrowserToolsFor binds the browser tools; readOnly keeps only the tools that change nothing the user
// can see: reading the panel's tabs, never driving them.
func BrowserToolsFor(bridge BrowserBridge, readOnly bool) []BoundTool {
	if readOnly {
		return Bind(bridge, filterDefinitions(BrowserTools, func(tool Definition[BrowserBridge]) bool { return tool.ReadOnly }))
	}
	return Bind(bridge, BrowserTools)
}

const browserInstructions = `The AICodingTool browser panel is a real browser sharing one session with the user, so every site they have signed into is signed in for you: use the browser panel tools, such as browser_open, browser_read and browser_screenshot, rather than curl or Bash for anything behind a login, and rather than guessing at a page you can read.`
const browserReadingInstructions = `The AICodingTool browser panel is a real browser sharing one session with the user: read the pages they have open there with browser_tabs, browser_read and browser_screenshot rather than guessing at them. Opening, clicking and typing in it are work for a thread.`

var BrowserServer = Server{
	Name:        BrowserServerName,
	Definitions: infos(BrowserTools),
	// Reading a page the panel already holds changes nothing; opening one and acting in it does.
	Grants: GrantsReadOnly,
	// A coordinator changes nothing itself, so it reads the browser panel but never drives it.
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Browser == nil {
			return nil, false
		}
		return BrowserToolsFor(run.Browser, run.role() == domain.CoordinationRoleCoordinator), true
	},
	Brief: func(offers Offers, _ BriefDialect, _ BriefRun) []string {
		if !offers("") {
			return nil
		}
		if offers("browser_open") {
			return []string{browserInstructions}
		}
		return []string{browserReadingInstructions}
	},
}
