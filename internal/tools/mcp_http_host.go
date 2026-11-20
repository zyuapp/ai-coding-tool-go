package tools

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
)

// ServedTools is what a served set is reachable as: the address, and the bearer token that names the set.
type ServedTools struct {
	URL   string
	Token string
	// Forgets the token and drops every connection made with it.
	Release func()
}

type ToolHost interface {
	Serve(ctx context.Context, tools []BoundTool) (ServedTools, error)
}

const (
	mcpServerName    = "aicodingtool"
	mcpServerVersion = "1.0.0"
	mcpPath          = "/mcp"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// NewMCPServer serves tools as one MCP server, answering as the TypeScript SDK's McpServer does: the
// tools listed in the order given, and a call to a tool it lacks, or one whose handler fails,
// answered as an error result rather than a protocol error.
func NewMCPServer(tools []BoundTool) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: mcpServerName, Version: mcpServerVersion}, &mcp.ServerOptions{
		Logger:       quietLogger,
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	listed := make([]*mcp.Tool, len(tools))
	byName := make(map[string]BoundTool, len(tools))
	for index, tool := range tools {
		listed[index] = &mcp.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.Input}
		byName[tool.Name] = tool
	}
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				return &mcp.ListToolsResult{Tools: listed}, nil
			case "tools/call":
				call, ok := request.(*mcp.CallToolRequest)
				if !ok || call.Params == nil {
					break
				}
				tool, found := byName[call.Params.Name]
				if !found {
					return callResult(errorResult(fmt.Sprintf("MCP error -32602: Tool %s not found", call.Params.Name))), nil
				}
				result, err := tool.Handler(ctx, jsontext.Value(call.Params.Arguments))
				if err != nil {
					return callResult(errorResult(err.Error())), nil
				}
				return callResult(result), nil
			}
			return next(ctx, method, request)
		}
	})
	return server
}

func callResult(result ToolResult) *mcp.CallToolResult {
	content := make([]mcp.Content, len(result.Content))
	for index, block := range result.Content {
		content[index] = &mcp.TextContent{Text: block.Text}
	}
	return &mcp.CallToolResult{Content: content, IsError: result.IsError}
}

type registration struct {
	server  *mcp.Server
	handler *mcp.StreamableHTTPHandler
}

func (r *registration) close() {
	for session := range r.server.Sessions() {
		_ = session.Close()
	}
}

// McpHTTPHost serves the app's tools over streamable HTTP on the loopback interface, to any MCP client
// that can carry a bearer token. Each token names one set of tools, so a caller sees exactly the set
// it was given and nobody else's. The listener opens on the first set served, on a port of the
// system's choosing.
type McpHTTPHost struct {
	mu            sync.Mutex
	registrations map[string]*registration
	server        *http.Server
	url           string
}

func NewMcpHTTPHost() *McpHTTPHost {
	return &McpHTTPHost{registrations: map[string]*registration{}}
}

func (h *McpHTTPHost) Serve(_ context.Context, tools []BoundTool) (ServedTools, error) {
	url, err := h.listen()
	if err != nil {
		return ServedTools{}, err
	}
	token := application.NewID()
	server := NewMCPServer(tools)
	entry := &registration{
		server:  server,
		handler: mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Logger: quietLogger}),
	}
	h.mu.Lock()
	h.registrations[token] = entry
	h.mu.Unlock()
	return ServedTools{
		URL:   url,
		Token: token,
		Release: func() {
			h.mu.Lock()
			current, ok := h.registrations[token]
			if ok && current == entry {
				delete(h.registrations, token)
			}
			h.mu.Unlock()
			if ok && current == entry {
				entry.close()
			}
		},
	}, nil
}

// Close drops every set and connection and stops listening; a later Serve listens afresh.
func (h *McpHTTPHost) Close() error {
	h.mu.Lock()
	registrations := h.registrations
	h.registrations = map[string]*registration{}
	server := h.server
	h.server, h.url = nil, ""
	h.mu.Unlock()
	for _, entry := range registrations {
		entry.close()
	}
	if server == nil {
		return nil
	}
	return server.Close()
}

// listen opens the listener on the first set served. A listener that never opened is forgotten, so
// the next set served tries afresh.
func (h *McpHTTPHost) listen() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.server != nil {
		return h.url, nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		listener.Close()
		return "", errors.New("The tool service has no port.")
	}
	server := &http.Server{Handler: http.HandlerFunc(h.handle), ErrorLog: slog.NewLogLogger(quietLogger.Handler(), slog.LevelError)}
	h.server = server
	h.url = fmt.Sprintf("http://127.0.0.1:%d%s", address.Port, mcpPath)
	go func() { _ = server.Serve(listener) }()
	return h.url, nil
}

func bearerOf(header string) (string, bool) {
	return strings.CutPrefix(header, "Bearer ")
}

func (h *McpHTTPHost) handle(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerOf(r.Header.Get("Authorization"))
	var entry *registration
	if ok {
		h.mu.Lock()
		entry = h.registrations[token]
		h.mu.Unlock()
	}
	if entry == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	// Only a fresh POST may open a session; a stale or foreign session id names nothing here.
	if r.Header.Get("Mcp-Session-Id") == "" && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	entry.handler.ServeHTTP(w, r)
}
