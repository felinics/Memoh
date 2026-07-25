package mcp

import (
	"encoding/json"
	"errors"
	"strconv"
)

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewToolCallRequest(id string, toolName string, args map[string]any) (JSONRPCRequest, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return JSONRPCRequest{}, err
	}
	return JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      RawStringID(id),
		Method:  "tools/call",
		Params:  rawParams,
	}, nil
}

func RawStringID(id string) json.RawMessage {
	return json.RawMessage([]byte(strconv.Quote(id)))
}

func PayloadError(payload map[string]any) error {
	if payload == nil {
		return errors.New("empty payload")
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg, ok := errObj["message"].(string); ok && msg != "" {
			return errors.New(msg)
		}
		return errors.New("mcp error")
	}
	return nil
}

func ResultError(payload map[string]any) error {
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return nil
	}
	if isErr, ok := result["isError"].(bool); ok && isErr {
		msg := ContentText(result)
		if msg == "" {
			msg = "mcp tool error"
		}
		return errors.New(msg)
	}
	return nil
}

func StructuredContent(payload map[string]any) (map[string]any, error) {
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return nil, errors.New("missing result")
	}
	if structured, ok := result["structuredContent"].(map[string]any); ok {
		return structured, nil
	}
	if content := ContentText(result); content != "" {
		var out map[string]any
		if err := json.Unmarshal([]byte(content), &out); err == nil {
			return out, nil
		}
	}
	return nil, errors.New("missing structured content")
}

func ContentText(result map[string]any) string {
	rawContent, ok := result["content"].([]any)
	if !ok || len(rawContent) == 0 {
		return ""
	}
	first, ok := rawContent[0].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := first["text"].(string)
	return text
}
