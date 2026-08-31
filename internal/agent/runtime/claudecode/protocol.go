// Package claudecode implements the direct Claude Code runtime driver: it
// drives a pinned Claude Code CLI inside the bot workspace over the
// stream-json wire protocol (NDJSON stdio plus the bidirectional control
// channel), with no ACP adapter and no Node sidecar in between.
//
// The wire contract is not officially documented; it is pinned against the
// dissected @anthropic-ai/claude-agent-sdk 0.3.250 ↔ CLI 2.1.250 pair (see
// PinnedCLIVersion) and defended by tolerant decoding: unknown message types,
// control subtypes, and fields never fail the stream.
//
// One process serves one turn: the CLI is spawned per turn with `--resume`
// carrying the durable session id from Memoh session runtime metadata, and
// exits when stdin closes after the result. This makes the cumulative
// cost/usage fields in `result` equal to the turn's own usage.
package claudecode

import (
	"encoding/json"
	"strings"
)

// PinnedCLIVersion is the Claude Code CLI version the wire contract was
// dissected from. A different runtime version logs a warning; the toolkit pin
// and this constant must move together.
const PinnedCLIVersion = "2.1.250"

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
	Type      string `json:"type"`
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`

	// system/init fields.
	Model             string   `json:"model,omitempty"`
	ClaudeCodeVersion string   `json:"claude_code_version,omitempty"`
	Capabilities      []string `json:"capabilities,omitempty"`

	// assistant / user replay messages.
	Message         json.RawMessage `json:"message,omitempty"`
	ParentToolUseID *string         `json:"parent_tool_use_id,omitempty"`

	// stream_event payload.
	Event json.RawMessage `json:"event,omitempty"`

	// result fields.
	IsError      bool            `json:"is_error,omitempty"`
	Result       string          `json:"result,omitempty"`
	Usage        *resultUsage    `json:"usage,omitempty"`
	TotalCostUSD *float64        `json:"total_cost_usd,omitempty"`
	Request      json.RawMessage `json:"request,omitempty"`
	RequestID    string          `json:"request_id,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`

	Raw json.RawMessage `json:"-"`
}

type initializeResponse struct {
	Models []initializeModel `json:"models"`
}

type initializeModel struct {
	Value                 string   `json:"value"`
	ResolvedModel         string   `json:"resolvedModel"`
	DisplayName           string   `json:"displayName"`
	Description           string   `json:"description"`
	SupportsEffort        bool     `json:"supportsEffort"`
	SupportedEffortLevels []string `json:"supportedEffortLevels"`
}

func decodeInbound(line []byte) (*inboundMessage, error) {
	var msg inboundMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, err
	}
	msg.Raw = append(json.RawMessage(nil), line...)
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

	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`

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
	Role    string         `json:"role"`
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
	ToolName              string          `json:"tool_name,omitempty"`
	Input                 map[string]any  `json:"input,omitempty"`
	ToolUseID             string          `json:"tool_use_id,omitempty"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions,omitempty"`
	BlockedPath           *string         `json:"blocked_path,omitempty"`
	DecisionReason        json.RawMessage `json:"decision_reason,omitempty"`
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
