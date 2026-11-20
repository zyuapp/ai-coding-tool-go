package tools

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(request)
}

func openHost(t *testing.T) *McpHTTPHost {
	host := NewMcpHTTPHost()
	t.Cleanup(func() { _ = host.Close() })
	return host
}

func connect(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   url,
		HTTPClient: &http.Client{Transport: bearer{token: token, next: http.DefaultTransport}},
		MaxRetries: -1,
	}, nil)
	if err == nil {
		t.Cleanup(func() { _ = session.Close() })
	}
	return session, err
}

func listed(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(result.Tools))
	for index, tool := range result.Tools {
		out[index] = tool.Name
	}
	return out
}

func refused(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized"))
}

var emptyBrowser = &fakeBrowser{read: func(contracts.BrowserRead) (contracts.BrowserReadResult, error) {
	return contracts.BrowserReadResultTabs{}, nil
}}

func TestATokenReachesTheToolsItWasServedAndNothingReachesTheHostWithoutOne(t *testing.T) {
	served, err := openHost(t).Serve(context.Background(), BrowserToolsFor(emptyBrowser, false))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/mcp$`).MatchString(served.URL) {
		t.Fatal(served.URL)
	}
	session, err := connect(t, served.URL, served.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(listed(t, session)); got != "[browser_open browser_search browser_read browser_screenshot browser_console browser_network browser_wait browser_click browser_type browser_back browser_tabs browser_close_tab]" {
		t.Fatalf("listed in another order: %s", got)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "browser_tabs", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if text := result.Content[0].(*mcp.TextContent).Text; text != "The browser panel has no tab open." || result.IsError {
		t.Fatalf("%q %v", text, result.IsError)
	}

	if _, err := connect(t, served.URL, "not-a-token"); !refused(err) {
		t.Fatalf("a foreign token connected: %v", err)
	}
	if _, err := connect(t, served.URL, ""); !refused(err) {
		t.Fatalf("no token connected: %v", err)
	}
}

func TestTheHostAnswersAsTheTypeScriptSDKDoes(t *testing.T) {
	served, err := openHost(t).Serve(context.Background(), BrowserToolsFor(emptyBrowser, false))
	if err != nil {
		t.Fatal(err)
	}
	session, err := connect(t, served.URL, served.Token)
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if text := unknown.Content[0].(*mcp.TextContent).Text; !unknown.IsError || text != "MCP error -32602: Tool no_such_tool not found" {
		t.Fatalf("%q", text)
	}
	invalid, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "browser_click", Arguments: map[string]any{"ref": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if text := invalid.Content[0].(*mcp.TextContent).Text; !invalid.IsError || text != "MCP error -32602: Input validation error: Invalid arguments for tool browser_click: Too small: expected string to have >=1 characters at ref" {
		t.Fatalf("%q", text)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	schema, _ := tools.Tools[0].InputSchema.(map[string]any)
	if schema["$schema"] != "http://json-schema.org/draft-07/schema#" || tools.Tools[0].Description == "" {
		t.Fatalf("%v", tools.Tools[0])
	}
	if caps := session.InitializeResult().Capabilities; caps.Tools == nil || !caps.Tools.ListChanged || caps.Logging != nil {
		t.Fatalf("capabilities %+v", caps)
	}
	if info := session.InitializeResult().ServerInfo; info.Name != "aicodingtool" || info.Version != "1.0.0" {
		t.Fatalf("server info %+v", info)
	}
}

func TestAReleasedTokenIsRefusedWhileOtherTokensGoOn(t *testing.T) {
	shared := openHost(t)
	first, err := shared.Serve(context.Background(), BrowserToolsFor(emptyBrowser, false))
	if err != nil {
		t.Fatal(err)
	}
	second, err := shared.Serve(context.Background(), TerminalToolsFor(&fakeTerminal{}))
	if err != nil {
		t.Fatal(err)
	}
	if first.URL != second.URL || first.Token == second.Token {
		t.Fatalf("one listener serves every set: %v %v", first, second)
	}
	session, err := connect(t, first.URL, first.Token)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	if _, err := session.ListTools(context.Background(), nil); err == nil {
		t.Fatal("an open session outlived its token")
	}
	if _, err := connect(t, first.URL, first.Token); !refused(err) {
		t.Fatalf("a released token connected: %v", err)
	}
	other, err := connect(t, second.URL, second.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(listed(t, other)); got != "[terminal_list terminal_read]" {
		t.Fatal(got)
	}
}

func TestOnlyAFreshPostOpensASession(t *testing.T) {
	served, err := openHost(t).Serve(context.Background(), TerminalToolsFor(&fakeTerminal{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, session string
		want            int
	}{{http.MethodGet, "", http.StatusNotFound}, {http.MethodPost, "stale", http.StatusNotFound}} {
		request, _ := http.NewRequest(c.method, served.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		request.Header.Set("Authorization", "Bearer "+served.Token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if c.session != "" {
			request.Header.Set("Mcp-Session-Id", c.session)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != c.want {
			t.Errorf("%s with session %q: %d, want %d", c.method, c.session, response.StatusCode, c.want)
		}
	}
}

func TestTheSetupToolServedOverHTTPAsksTheAppToOpenComputerUseSettings(t *testing.T) {
	asked := 0
	run := &Run{ComputerUse: domain.ComputerUseRunConfigSetupRequired{}, EmitSetupRequired: func() { asked++ }}
	var tools []BoundTool
	for _, server := range Servers() {
		if served, ok := server.Serve(run); ok {
			tools = append(tools, served...)
		}
	}
	served, err := openHost(t).Serve(context.Background(), tools)
	if err != nil {
		t.Fatal(err)
	}
	session, err := connect(t, served.URL, served.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(listed(t, session)); got != "[request_setup]" {
		t.Fatal(got)
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "request_setup", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "Settings → Computer use") {
		t.Fatalf("%d %v", asked, result.Content)
	}
}

func TestHandlersTakeAbsentAndNullArgumentsAsAnEmptyObject(t *testing.T) {
	tools := TerminalToolsFor(&fakeTerminal{})
	for _, raw := range []string{"", "null", "{}"} {
		result, err := tools[0].Handler(context.Background(), jsontext.Value(raw))
		if err != nil || result.IsError {
			t.Fatalf("%q: %v %+v", raw, err, result)
		}
	}
}
