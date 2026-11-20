package protocol

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"sync"
)

// McpProtocolVersion is what an in-process server answers with when the client asks for a version
// it does not know.
const McpProtocolVersion = "2025-11-25"

// McpProtocolVersions are the versions an in-process server accepts, newest first.
var McpProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

const (
	mcpMethodNotFound = -32601
	mcpInvalidParams  = -32602
)

// ToolHandler runs one tool call and answers with its CallToolResult. An error becomes a result
// with isError set, its message as the text.
type ToolHandler func(ctx context.Context, arguments jsontext.Value) (jsontext.Value, error)

// SdkMcpTool is one tool an in-process server offers.
type SdkMcpTool struct {
	Name        string
	Title       *string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema jsontext.Value
	Annotations jsontext.Value
	Meta        map[string]any
	Handler     ToolHandler
}

// SdkMcpServer is an MCP server the app serves to Claude in-process, over control messages.
type SdkMcpServer struct {
	Name         string
	Version      string
	AlwaysLoad   bool
	Instructions *string
	Tools        []SdkMcpTool

	mu       sync.Mutex
	inFlight map[string]context.CancelFunc
}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpToolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type mcpCapabilities struct {
	Tools mcpToolsCapability `json:"tools"`
}

// McpInitializeResult is an in-process server's answer to initialize.
type McpInitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    mcpCapabilities `json:"capabilities"`
	ServerInfo      mcpServerInfo   `json:"serverInfo"`
	Instructions    *string         `json:"instructions,omitzero"`
}

type mcpExecution struct {
	TaskSupport string `json:"taskSupport"`
}

// McpListedTool is one tool as tools/list describes it.
type McpListedTool struct {
	Name        string         `json:"name"`
	Title       *string        `json:"title,omitzero"`
	Description string         `json:"description,omitzero"`
	InputSchema jsontext.Value `json:"inputSchema"`
	Annotations jsontext.Value `json:"annotations,omitzero"`
	Execution   mcpExecution   `json:"execution"`
	Meta        map[string]any `json:"_meta,omitzero"`
}

// McpToolsListResult is an in-process server's answer to tools/list.
type McpToolsListResult struct {
	Tools []McpListedTool `json:"tools"`
}

// McpManifest is what initialize tells Claude of an in-process server up front, so it skips that
// handshake.
type McpManifest struct {
	InitializeResult McpInitializeResult `json:"initializeResult"`
	ToolsListResult  *McpToolsListResult `json:"toolsListResult,omitzero"`
}

var emptyObjectSchema = jsontext.Value(`{"type":"object","properties":{}}`)

func (s *SdkMcpServer) version() string {
	if s.Version == "" {
		return "1.0.0"
	}
	return s.Version
}

func (s *SdkMcpServer) initializeResult(requested string) McpInitializeResult {
	version := McpProtocolVersion
	if slices.Contains(McpProtocolVersions, requested) {
		version = requested
	}
	return McpInitializeResult{
		ProtocolVersion: version,
		Capabilities:    mcpCapabilities{Tools: mcpToolsCapability{ListChanged: true}},
		ServerInfo:      mcpServerInfo{Name: s.Name, Version: s.version()},
		Instructions:    s.Instructions,
	}
}

func (s *SdkMcpServer) toolsList() McpToolsListResult {
	tools := []McpListedTool{}
	for _, tool := range s.Tools {
		meta := tool.Meta
		if s.AlwaysLoad {
			meta = map[string]any{"anthropic/alwaysLoad": true}
			for key, value := range tool.Meta {
				meta[key] = value
			}
		}
		schema := tool.InputSchema
		if len(schema) == 0 {
			schema = emptyObjectSchema
		}
		tools = append(tools, McpListedTool{
			Name: tool.Name, Title: tool.Title, Description: tool.Description, InputSchema: schema,
			Annotations: tool.Annotations, Execution: mcpExecution{TaskSupport: "forbidden"}, Meta: meta,
		})
	}
	return McpToolsListResult{Tools: tools}
}

// Manifest is the server's initialize and tools/list answers, as initialize carries them.
func (s *SdkMcpServer) Manifest() McpManifest {
	list := s.toolsList()
	return McpManifest{InitializeResult: s.initializeResult(McpProtocolVersion), ToolsListResult: &list}
}

type jsonRPCMessage struct {
	JSONRPC string         `json:"jsonrpc,omitzero"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  *string        `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type jsonRPCResponse struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id"`
	Result  any            `json:"result,omitzero"`
	Error   *jsonRPCError  `json:"error,omitzero"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments jsontext.Value `json:"arguments,omitzero"`
}

type cancelledParams struct {
	RequestID jsontext.Value `json:"requestId"`
}

type toolText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolError struct {
	Content []toolText `json:"content"`
	IsError bool       `json:"isError"`
}

func toolErrorResult(message string) toolError {
	return toolError{Content: []toolText{{Type: "text", Text: message}}, IsError: true}
}

// isRequest reports whether a JSON-RPC message expects an answer: it has a method and an id.
func (m jsonRPCMessage) isRequest() bool {
	return m.Method != nil && len(m.ID) > 0 && m.ID.Kind() != 'n'
}

// Respond answers one JSON-RPC message for this server. A notification or a response gets the
// empty result the SDK sends back for one.
func (s *SdkMcpServer) Respond(ctx context.Context, raw jsontext.Value) jsontext.Value {
	var message jsonRPCMessage
	if err := unmarshal(raw, &message); err != nil || !message.isRequest() {
		if err == nil {
			s.notify(message)
		}
		out, _ := marshal(jsonRPCResponse{JSONRPC: "2.0", ID: jsontext.Value("0"), Result: struct{}{}})
		return out
	}
	response := jsonRPCResponse{JSONRPC: "2.0", ID: message.ID}
	params := message.Params
	switch *message.Method {
	case "initialize":
		var request struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = unmarshal(params, &request)
		response.Result = s.initializeResult(request.ProtocolVersion)
	case "ping":
		response.Result = struct{}{}
	case "tools/list":
		response.Result = s.toolsList()
	case "tools/call":
		response.Result = s.call(ctx, message.ID, params)
	default:
		response.Error = &jsonRPCError{Code: mcpMethodNotFound, Message: "Method not found"}
	}
	out, err := marshal(response)
	if err != nil {
		out, _ = marshal(jsonRPCResponse{JSONRPC: "2.0", ID: message.ID, Result: toolErrorResult(err.Error())})
	}
	return out
}

func (s *SdkMcpServer) call(ctx context.Context, id jsontext.Value, raw jsontext.Value) any {
	var params callToolParams
	if err := unmarshal(raw, &params); err != nil {
		return toolErrorResult(err.Error())
	}
	index := slices.IndexFunc(s.Tools, func(tool SdkMcpTool) bool { return tool.Name == params.Name })
	if index < 0 {
		return toolErrorResult(fmt.Sprintf("MCP error %d: Tool %s not found", mcpInvalidParams, params.Name))
	}
	tool := s.Tools[index]
	if tool.Handler == nil {
		return toolErrorResult(fmt.Sprintf("Tool %s has no handler", params.Name))
	}
	ctx, cancel := context.WithCancel(ctx)
	key := string(id)
	s.mu.Lock()
	if s.inFlight == nil {
		s.inFlight = map[string]context.CancelFunc{}
	}
	s.inFlight[key] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.inFlight, key)
		s.mu.Unlock()
		cancel()
	}()
	result, err := tool.Handler(ctx, params.Arguments)
	if err != nil {
		return toolErrorResult(err.Error())
	}
	if len(result) == 0 {
		return struct{}{}
	}
	return result
}

// notify takes a notification: a cancellation stops the call it names.
func (s *SdkMcpServer) notify(message jsonRPCMessage) {
	if message.Method == nil || *message.Method != "notifications/cancelled" {
		return
	}
	var params cancelledParams
	if unmarshal(message.Params, &params) != nil {
		return
	}
	s.mu.Lock()
	cancel := s.inFlight[string(params.RequestID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
