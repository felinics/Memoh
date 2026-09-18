package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type historyExecutionPage struct {
	offset   int
	maxBytes int
	version  string
}

func parseHistoryExecutionPage(args map[string]any) (*historyExecutionPage, error) {
	for _, key := range []string{"view", "content_version"} {
		if raw, present := args[key]; present {
			if _, ok := raw.(string); !ok {
				return nil, fmt.Errorf("%s must be a string", key)
			}
		}
	}
	view := StringArg(args, "view")
	if view == "" || view == "chat" {
		for _, key := range []string{"content_offset", "content_version", "max_bytes"} {
			if _, ok := args[key]; ok {
				return nil, fmt.Errorf("%s requires view=execution", key)
			}
		}
		return nil, nil
	}
	if view != "execution" || strings.TrimSpace(StringArg(args, "message_id")) == "" {
		return nil, errors.New("view=execution requires an exact message_id; supported views are chat and execution")
	}
	page := &historyExecutionPage{maxBytes: 4096, version: StringArg(args, "content_version")}
	for _, key := range []string{"content_offset", "max_bytes"} {
		if raw, present := args[key]; present && raw == nil {
			return nil, fmt.Errorf("%s must be an integer", key)
		}
		if v, ok := args[key].(float64); ok && math.Trunc(v) != v {
			return nil, fmt.Errorf("%s must be an integer", key)
		}
		value, present, err := IntArg(args, key)
		if err != nil {
			return nil, err
		}
		if present {
			if key == "content_offset" {
				page.offset = value
			} else {
				page.maxBytes = value
			}
		}
	}
	if page.offset < 0 || page.maxBytes < 256 || page.maxBytes > 8192 {
		return nil, errors.New("content_offset must be nonnegative and max_bytes must be between 256 and 8192")
	}
	if page.offset > 0 && page.version == "" {
		return nil, errors.New("content_version is required when continuing an execution content page")
	}
	return page, nil
}

func (page historyExecutionPage) format(sess SessionContext, msg messagepkg.Message) (map[string]any, error) {
	content, err := historyExecutionContent(msg)
	if err != nil {
		return nil, errors.New("persisted execution content is unavailable")
	}
	digest := sha256.New()
	digest.Write(msg.Content)
	digest.Write([]byte{0})
	digest.Write(content)
	version := hex.EncodeToString(digest.Sum(nil))
	if page.version != "" && page.version != version {
		return nil, errors.New("content_version changed; restart the execution read at content_offset=0 without content_version")
	}
	if page.offset > len(content) || (page.offset < len(content) && !utf8.RuneStart(content[page.offset])) {
		return nil, errors.New("content_offset must be a UTF-8 byte boundary within the execution content")
	}
	end := page.offset + min(page.maxBytes, len(content)-page.offset)
	for end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	entry := map[string]any{
		"id": msg.ID, "session_id": msg.SessionID, "role": msg.Role,
		"created_at": sess.FormatTime(msg.CreatedAt), "source": "persisted_message",
		"content": string(content[page.offset:end]), "content_format": "json",
		"content_version": version, "content_offset": page.offset,
		"total_bytes": len(content), "has_more": end < len(content),
	}
	if msg.TurnID != "" {
		entry["turn_id"] = msg.TurnID
	}
	if end < len(content) {
		entry["next_content_offset"] = end
	}
	return entry, nil
}

func historyExecutionContent(source messagepkg.Message) ([]byte, error) {
	if !json.Valid(source.Content) {
		return nil, errors.New("invalid stored message")
	}
	msg := historyfrag.DecodeStoredModelMessage(nil, source.ID, source.Role, source.Content)
	legacyResult := msg.Role == "tool" && strings.Trim(msg.ToolCallID, " \t\n\r\f\v") != ""
	var plain string
	if len(msg.Content) > 0 && json.Unmarshal(msg.Content, &plain) != nil {
		var rawParts []json.RawMessage
		if err := json.Unmarshal(msg.Content, &rawParts); err != nil {
			var single struct {
				Type string `json:"type"`
			}
			switch {
			case json.Unmarshal(msg.Content, &single) == nil && single.Type == "tool-result":
				rawParts = []json.RawMessage{msg.Content}
			case legacyResult:
				return json.Marshal(msg)
			default:
				return nil, err
			}
		}
		parts := make([]map[string]json.RawMessage, len(rawParts))
		hasResultPart := false
		for i, raw := range rawParts {
			_ = json.Unmarshal(raw, &parts[i])
			var kind string
			_ = json.Unmarshal(parts[i]["type"], &kind)
			hasResultPart = hasResultPart || kind == "tool-result"
		}
		if legacyResult && !hasResultPart {
			return json.Marshal(msg)
		}
		projected := make([]map[string]json.RawMessage, 0, len(parts))
		for _, part := range parts {
			var kind string
			if json.Unmarshal(part["type"], &kind) != nil {
				return nil, errors.New("invalid content part")
			}
			var keys []string
			switch kind {
			case "text":
				keys = []string{"text"}
			case "tool-call":
				keys = []string{"toolCallId", "toolName", "input", "args"}
			case "tool-result":
				keys = []string{"toolCallId", "toolName", "result", "output", "isError"}
			case "image", "file":
				keys = []string{"mediaType", "filename"}
			case "link", "emoji":
				keys = []string{"url", "emoji"}
			default:
				continue
			}
			item := map[string]json.RawMessage{"type": part["type"]}
			for _, key := range keys {
				if value, ok := part[key]; ok {
					item[key] = value
				}
			}
			projected = append(projected, item)
		}
		content, err := json.Marshal(projected)
		if err != nil {
			return nil, err
		}
		msg.Content = content
	}
	return json.Marshal(msg)
}
