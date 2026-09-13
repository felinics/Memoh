// Package claudecode implements the direct Claude Code runtime driver: it
// drives a Claude Code CLI inside the bot workspace over the
// stream-json wire protocol (NDJSON stdio plus the bidirectional control
// channel), with no ACP adapter and no Node sidecar in between.
//
// The wire contract is checked against @anthropic-ai/claude-agent-sdk
// 0.3.269 and CLI 2.1.269 (see protocolref/VERSION.json). Only the protocol
// surface used by this driver is modeled; optional fields and unconsumed
// events do not require version-specific adapters. Unsupported inbound
// control requests receive an error response.
//
// One process serves one Memoh run and resumes its durable native session.
// Steering may start several native turns before stdin closes. Token usage
// belongs to each native result and is summed across distinct result UUIDs.
package claudecode

import (
	"encoding/json"
	"strings"
)

// PinnedCLIVersion is the CLI version verified by the protocol fixtures.
// A different installed version logs a warning. The workspace dependency
// manager owns CLI installation; changing this baseline does not install it.
const PinnedCLIVersion = "2.1.269"

const noResponseRequested = "No response requested."

// Stream message types (CLI → Memoh).
const (
	messageTypeSystem          = "system"
	messageTypeAssistant       = "assistant"
	messageTypeUser            = "user"
	messageTypeStreamEvent     = "stream_event"
	messageTypeResult          = "result"
	messageTypeControlRequest  = "control_request"
	messageTypeControlResponse = "control_response"
	messageTypeControlCancel   = "control_cancel_request"
)

// inboundMessage is one decoded NDJSON line from the CLI, probed just far
// enough to route it; payloads stay raw until the consumer needs them.
type inboundMessage struct {
	UUID            string `json:"uuid,omitempty"`
	CommandUUID     string `json:"command_uuid,omitempty"`
	State           string `json:"state,omitempty"`
	QueuedTurnCount int    `json:"queued_turn_count,omitempty"`
	Type            string `json:"type"`
	Subtype         string `json:"subtype,omitempty"`
	SessionID       string `json:"session_id,omitempty"`

	// system/init fields.
	Model              string              `json:"model,omitempty"`
	ClaudeCodeVersion  string              `json:"claude_code_version,omitempty"`
	Capabilities       []string            `json:"capabilities,omitempty"`
	PermissionMode     string              `json:"permissionMode,omitempty"`
	Skills             []string            `json:"skills,omitempty"`
	Commands           []initializeCommand `json:"commands,omitempty"`
	Content            string              `json:"content,omitempty"`
	LocalCommandSource string              `json:"local_command_source,omitempty"`
	MCPServers         []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers,omitempty"`
	CompactResult      string  `json:"compact_result,omitempty"`
	Status             *string `json:"status,omitempty"`
	Attempt            int     `json:"attempt,omitempty"`
	MaxRetries         int     `json:"max_retries,omitempty"`
	RetryDelayMS       int     `json:"retry_delay_ms,omitempty"`
	ToolUseID          string  `json:"tool_use_id,omitempty"`
	ToolName           string  `json:"tool_name,omitempty"`
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds,omitempty"`
	CompactMetadata    *struct {
		Trigger string `json:"trigger"`
	} `json:"compact_metadata,omitempty"`

	// assistant / user replay messages.
	Message         json.RawMessage `json:"message,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`
	Error           string          `json:"error,omitempty"`

	// stream_event payload.
	Event json.RawMessage `json:"event,omitempty"`

	// result fields.
	IsError   bool            `json:"is_error,omitempty"`
	Errors    []string        `json:"errors,omitempty"`
	Result    string          `json:"result,omitempty"`
	Usage     *resultUsage    `json:"usage,omitempty"`
	Request   json.RawMessage `json:"request,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	Response  json.RawMessage `json:"response,omitempty"`
}

type initializeResponse struct {
	Models   []initializeModel   `json:"models"`
	Commands []initializeCommand `json:"commands"`
}

type initializeCommand struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	ArgumentHint string   `json:"argumentHint"`
	Aliases      []string `json:"aliases,omitempty"`
}

type initializeModel struct {
	Value                 string   `json:"value"`
	ResolvedModel         string   `json:"resolvedModel"`
	DisplayName           string   `json:"displayName"`
	Description           string   `json:"description"`
	SupportsEffort        bool     `json:"supportsEffort"`
	SupportedEffortLevels []string `json:"supportedEffortLevels"`
	SupportsAutoMode      bool     `json:"supportsAutoMode,omitempty"`
}

type settingsResponse struct {
	Effective struct {
		Permissions struct {
			DefaultMode string `json:"defaultMode"`
		} `json:"permissions"`
	} `json:"effective"`
	Applied struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	} `json:"applied"`
}

func decodeInbound(line []byte) (*inboundMessage, error) {
	var msg inboundMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// resultUsage is the Anthropic usage block on the result message.
type resultUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// contentBlock is one Anthropic message content block; only the fields the
// mapping consumes are typed.
type contentBlock struct {
	Type string `json:"type"`

	Text string `json:"text,omitempty"`

	// tool_use fields.
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result fields.
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// chatMessage is the Anthropic message envelope inside assistant/user lines.
type chatMessage struct {
	Content []contentBlock `json:"content"`
}

func decodeChatMessage(raw json.RawMessage) (*chatMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, false
	}
	return &msg, true
}

// streamEvent is the raw Anthropic streaming event inside a stream_event line.
type streamEvent struct {
	Type  string `json:"type"`
	Delta struct {
		Type     string `json:"type"`
		Text     string `json:"text,omitempty"`
		Thinking string `json:"thinking,omitempty"`
	} `json:"delta"`
}

// controlRequestPayload is the `request` object of a control_request line.
type controlRequestPayload struct {
	Subtype string `json:"subtype"`

	// can_use_tool fields.
	ToolName  string         `json:"tool_name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
}

// Outbound message builders.

// userMessageLine builds one user turn input line.
func userMessageLine(sessionID string, content []map[string]any) ([]byte, error) {
	line := map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
		"parent_tool_use_id": nil,
	}
	if strings.TrimSpace(sessionID) != "" {
		line["session_id"] = sessionID
	}
	return json.Marshal(line)
}

// controlRequestLine builds one Memoh → CLI control request.
func controlRequestLine(requestID, subtype string, extra map[string]any) ([]byte, error) {
	request := map[string]any{"subtype": subtype}
	for key, value := range extra {
		request[key] = value
	}
	return json.Marshal(map[string]any{
		"type":       messageTypeControlRequest,
		"request_id": requestID,
		"request":    request,
	})
}

// permissionAllowResponse answers can_use_tool with an allow decision.
func permissionAllowResponse(requestID string, updatedInput map[string]any, toolUseID string) ([]byte, error) {
	response := map[string]any{
		"behavior":     "allow",
		"updatedInput": updatedInput,
	}
	if strings.TrimSpace(toolUseID) != "" {
		response["toolUseID"] = toolUseID
	}
	return controlSuccessResponse(requestID, response)
}

// permissionDenyResponse answers can_use_tool with a deny decision.
func permissionDenyResponse(requestID, message string) ([]byte, error) {
	if strings.TrimSpace(message) == "" {
		message = "denied"
	}
	return controlSuccessResponse(requestID, map[string]any{
		"behavior": "deny",
		"message":  message,
	})
}

func controlSuccessResponse(requestID string, response map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": messageTypeControlResponse,
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   response,
		},
	})
}

// controlErrorResponse rejects a control request Memoh cannot serve.
func controlErrorResponse(requestID, message string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type": messageTypeControlResponse,
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      message,
		},
	})
}
