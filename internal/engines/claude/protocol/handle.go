package protocol

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
)

// unansweredSubtypes are requests meant for the machine serving a session's tools; the SDK leaves
// them for that host to answer.
var unansweredSubtypes = map[string]bool{
	"remote_tool_call":        true,
	"remote_plumbing_call":    true,
	"remote_tools_probe":      true,
	"remote_tools_reannounce": true,
}

// errSuppress leaves a control request unanswered.
var errSuppress = errors.New("suppress control response")

type unsupportedError struct{ subtype string }

func (e *unsupportedError) Error() string {
	return "Unsupported control request subtype: " + e.subtype
}

func (q *Query) receiveRequest(raw jsontext.Value) {
	var request incomingControlRequest
	if unmarshal(raw, &request) != nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	if _, duplicate := q.handling[request.RequestID]; duplicate {
		q.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(q.ctx)
	q.handling[request.RequestID] = cancel
	q.mu.Unlock()
	go func() {
		defer cancel()
		body, err := q.process(ctx, request)
		q.mu.Lock()
		closed := q.closed
		if q.handling[request.RequestID] != nil {
			delete(q.handling, request.RequestID)
		}
		q.mu.Unlock()
		if closed || errors.Is(err, errSuppress) {
			return
		}
		response := controlResponseBody{Subtype: "success", RequestID: request.RequestID, Response: body}
		if err != nil {
			message := err.Error()
			response = controlResponseBody{Subtype: "error", RequestID: request.RequestID, Error: &message}
			var unsupported *unsupportedError
			if errors.As(err, &unsupported) {
				response.ErrorCode = ptr("unsupported_request")
			}
		}
		if line, err := marshal(controlResponse{Type: "control_response", Response: response}); err == nil {
			_ = q.writer.write(line)
		}
	}()
}

// process answers one control request Claude Code sent.
func (q *Query) process(ctx context.Context, request incomingControlRequest) (jsontext.Value, error) {
	var head subtypeOnly
	if err := unmarshal(request.Request, &head); err != nil {
		return nil, err
	}
	if unansweredSubtypes[head.Subtype] {
		return nil, errSuppress
	}
	switch head.Subtype {
	case "can_use_tool":
		if q.opts.CanUseTool == nil {
			return nil, errors.New("canUseTool callback is not provided.")
		}
		var permission PermissionRequest
		if err := unmarshal(request.Request, &permission); err != nil {
			return nil, err
		}
		permission.RequestID = request.RequestID
		if len(permission.Input) == 0 {
			permission.Input = jsontext.Value("{}")
		}
		result, err := q.opts.CanUseTool(ctx, permission)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errSuppress
		}
		answer := *result
		answer.ToolUseID = permission.ToolUseID
		return marshal(answer)
	case "hook_callback":
		var hook hookCallbackRequest
		if err := unmarshal(request.Request, &hook); err != nil {
			return nil, err
		}
		callback := q.hooks[hook.CallbackID]
		if callback == nil {
			return nil, fmt.Errorf("No hook callback found for ID: %s", hook.CallbackID)
		}
		var input HookInput
		if len(hook.Input) > 0 {
			_ = unmarshal(hook.Input, &input)
			input.Raw = hook.Input
		}
		output, err := callback(ctx, input, hook.ToolUseID)
		if err != nil {
			return nil, err
		}
		return marshal(output)
	case "mcp_message":
		var message mcpMessageRequest
		if err := unmarshal(request.Request, &message); err != nil {
			return nil, err
		}
		server := q.mcp[message.ServerName]
		if server == nil {
			return nil, fmt.Errorf("SDK MCP server not found: %s", message.ServerName)
		}
		return marshal(mcpMessageResponse{McpResponse: server.Respond(ctx, message.Message)})
	case "elicitation":
		return jsontext.Value(`{"action":"decline"}`), nil
	case "request_user_dialog":
		return nil, errSuppress
	case "oauth_token_refresh":
		return nil, errors.New("getOAuthToken callback is not provided.")
	case "host_auth_token_refresh":
		return nil, errors.New("getHostAuthToken callback is not provided.")
	case "remote_control_work_secret":
		return nil, errors.New("refreshWorkSecret callback is not provided.")
	}
	return nil, &unsupportedError{subtype: head.Subtype}
}
