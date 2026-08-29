package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/felinics/memoh/internal/toolcontext"
)

// ToolSessionContext is the MCP gateway's compatibility name for the shared
// tool execution context. The canonical type is owned by internal/toolcontext.
type ToolSessionContext = toolcontext.Session

// ToolDescriptor is the MCP tools/list item shape used by the gateway.
type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolSource represents a tool source surfaced through the MCP gateway.
// Implementations may wrap federated MCP connections or selected native Memoh
// ToolProviders.
type ToolSource interface {
	ListTools(ctx context.Context, session ToolSessionContext) ([]ToolDescriptor, error)
	CallTool(ctx context.Context, session ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error)
}

// ToolCallPayload is the MCP tools/call params payload.
type ToolCallPayload struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ErrToolNotFound indicates the provider does not own the requested tool.
var ErrToolNotFound = errors.New("tool not found")

// BuildToolSuccessResult builds a standard MCP tool success result object.
func BuildToolSuccessResult(structured any) map[string]any {
	result := map[string]any{}
	if structured != nil {
		result["structuredContent"] = structured
		if text := stringifyStructuredContent(structured); text != "" {
			result["content"] = []map[string]any{
				{
					"type": "text",
					"text": text,
				},
			}
		}
	}
	if len(result) == 0 {
		result["content"] = []map[string]any{
			{
				"type": "text",
				"text": "ok",
			},
		}
	}
	return result
}

// BuildToolErrorResult builds a standard MCP tool error result object.
func BuildToolErrorResult(message string) map[string]any {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "tool execution failed"
	}
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{
				"type": "text",
				"text": msg,
			},
		},
	}
}

func stringifyStructuredContent(v any) string {
	if v == nil {
		return ""
	}
	switch value := v.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		payload, err := json.Marshal(value)
		if err != nil {
			return ""
		}
		return string(payload)
	}
}

func StringArg(arguments map[string]any, key string) string {
	if arguments == nil {
		return ""
	}
	raw, ok := arguments[key]
	if !ok {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", raw))
	}
}

func FirstStringArg(arguments map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := StringArg(arguments, key); value != "" {
			return value
		}
	}
	return ""
}

func IntArg(arguments map[string]any, key string) (int, bool, error) {
	if arguments == nil {
		return 0, false, nil
	}
	raw, ok := arguments[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	switch value := raw.(type) {
	case int:
		return value, true, nil
	case int8:
		return int(value), true, nil
	case int16:
		return int(value), true, nil
	case int32:
		return int(value), true, nil
	case int64:
		i, err := int64ToInt(value, key)
		return i, true, err
	case uint:
		i, err := uint64ToInt(uint64(value), key)
		return i, true, err
	case uint8:
		return int(value), true, nil
	case uint16:
		return int(value), true, nil
	case uint32:
		i, err := uint64ToInt(uint64(value), key)
		return i, true, err
	case uint64:
		i, err := uint64ToInt(value, key)
		return i, true, err
	case float32:
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, true, fmt.Errorf("%s must be a valid number", key)
		}
		i, err := float64ToInt(f, key)
		return i, true, err
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, true, fmt.Errorf("%s must be a valid number", key)
		}
		i, err := float64ToInt(value, key)
		return i, true, err
	case json.Number:
		i, err := value.Int64()
		if err != nil {
			return 0, true, fmt.Errorf("%s must be an integer", key)
		}
		n, convErr := int64ToInt(i, key)
		return n, true, convErr
	default:
		return 0, true, fmt.Errorf("%s must be a number", key)
	}
}

func int64ToInt(value int64, key string) (int, error) {
	if value < int64(math.MinInt) || value > int64(math.MaxInt) {
		return 0, fmt.Errorf("%s out of range", key)
	}
	return int(value), nil
}

func uint64ToInt(value uint64, key string) (int, error) {
	const maxIntAsUint = uint64(math.MaxInt)
	if value > maxIntAsUint {
		return 0, fmt.Errorf("%s out of range", key)
	}
	return int(value), nil
}

func float64ToInt(value float64, key string) (int, error) {
	if value < float64(math.MinInt) || value > float64(math.MaxInt) {
		return 0, fmt.Errorf("%s out of range", key)
	}
	return int(value), nil
}

func BoolArg(arguments map[string]any, key string) (bool, bool, error) {
	if arguments == nil {
		return false, false, nil
	}
	raw, ok := arguments[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, true, fmt.Errorf("%s must be a boolean", key)
	}
	return value, true, nil
}
