package mcp

import (
	"strings"

	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	textprune "github.com/felinics/memoh/internal/prune"
)

type ToolOutputLimit = contextlimit.ToolOutputLimit

func LimitToolResult(result map[string]any, label string, limit ToolOutputLimit) map[string]any {
	if result == nil {
		result = BuildToolSuccessResult(map[string]any{"ok": true})
	}
	if !contextlimit.EncodedExceeds(result, limit) && !toolResultTextExceeds(result, limit) {
		return result
	}
	if isToolErrorResult(result) {
		return limitToolErrorResult(result, label, limit)
	}
	if structured, ok := result["structuredContent"]; ok && structured != nil {
		if limited := limitStructuredMCPResult(structured, label, limit); limited != nil {
			return limited
		}
	}
	return limitToolTextResult(false, toolResultText(result), label, limit)
}

func toolResultTextExceeds(result map[string]any, limit ToolOutputLimit) bool {
	normalized := contextlimit.NormalizedLimit(limit)
	if text := toolResultText(result); text != "" && textprune.Exceeds(text, normalized.MaxBytes, normalized.MaxLines) {
		return true
	}
	return stringLeafExceeds(result["structuredContent"], normalized)
}

func isToolErrorResult(result map[string]any) bool {
	isError, _ := result["isError"].(bool)
	return isError
}

func limitStructuredMCPResult(structured any, label string, limit ToolOutputLimit) map[string]any {
	normalized := contextlimit.NormalizedLimit(limit)
	budget := normalized.MaxBytes / 3
	if budget <= 0 {
		return nil
	}
	for budget > 0 {
		limitedStructured := contextlimit.LimitToolOutput(structured, label+".structuredContent", ToolOutputLimit{
			MaxBytes: budget,
			MaxLines: normalized.MaxLines,
		})
		if isTruncatedFallback(limitedStructured) {
			return nil
		}
		result := BuildToolSuccessResult(limitedStructured)
		if !contextlimit.EncodedExceeds(result, limit) {
			return result
		}
		budget = budget * 3 / 4
	}
	return nil
}

func isTruncatedFallback(value any) bool {
	result, ok := value.(map[string]any)
	if !ok {
		return false
	}
	truncated, _ := result["_memoh_truncated"].(bool)
	return truncated
}

func limitToolErrorResult(result map[string]any, label string, limit ToolOutputLimit) map[string]any {
	text := toolResultText(result)
	if text == "" {
		text = contextlimit.MarshalString(result)
	}
	return limitToolTextResult(true, text, label, limit)
}

func limitToolTextResult(isError bool, text, label string, limit ToolOutputLimit) map[string]any {
	normalized := contextlimit.NormalizedLimit(limit)
	budget := normalized.MaxBytes - 128
	if budget <= 0 {
		budget = normalized.MaxBytes / 2
	}
	if budget <= 0 {
		budget = len(textprune.DefaultMarker)
	}
	for budget > 0 {
		limitedText := contextlimit.LimitString(text, label, ToolOutputLimit{
			MaxBytes: budget,
			MaxLines: normalized.MaxLines,
		})
		result := toolTextResult(isError, limitedText)
		if !contextlimit.EncodedExceeds(result, limit) {
			return result
		}
		budget = budget * 3 / 4
	}
	return toolTextResult(isError, textprune.DefaultMarker)
}

func toolTextResult(isError bool, text string) map[string]any {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "ok"
	}
	result := map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": text,
			},
		},
	}
	if isError {
		result["isError"] = true
	}
	return result
}

func toolResultText(result map[string]any) string {
	if result == nil {
		return ""
	}
	var parts []string
	appendText := func(value any) {
		text, ok := value.(string)
		if !ok {
			return
		}
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	switch content := result["content"].(type) {
	case []map[string]any:
		for _, item := range content {
			appendText(item["text"])
		}
	case []any:
		for _, raw := range content {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			appendText(item["text"])
		}
	case string:
		appendText(content)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	if structured := result["structuredContent"]; structured != nil {
		return contextlimit.MarshalString(structured)
	}
	return contextlimit.MarshalString(result)
}

func stringLeafExceeds(value any, limit ToolOutputLimit) bool {
	switch v := value.(type) {
	case string:
		return textprune.Exceeds(v, limit.MaxBytes, limit.MaxLines)
	case []string:
		for _, item := range v {
			if stringLeafExceeds(item, limit) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if stringLeafExceeds(item, limit) {
				return true
			}
		}
	case []map[string]any:
		for _, item := range v {
			if stringLeafExceeds(item, limit) {
				return true
			}
		}
	case map[string]string:
		for _, item := range v {
			if stringLeafExceeds(item, limit) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if stringLeafExceeds(item, limit) {
				return true
			}
		}
	}
	return false
}
