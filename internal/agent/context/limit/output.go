package contextlimit

import (
	"encoding/json"
	"fmt"
	"strings"

	textprune "github.com/felinics/memoh/internal/prune"
)

const (
	toolOutputHeadBytes = 32 * 1024
	toolOutputTailBytes = 8 * 1024
	toolOutputHeadLines = 500
	toolOutputTailLines = 100

	minToolOutputMaxBytes = 256
)

type ToolOutputLimit struct {
	MaxBytes int
	MaxLines int
}

func LimitToolOutput(output any, label string, limit ToolOutputLimit) any {
	cfg := toolOutputPruneConfig(limit)
	limited := limitToolOutputValue(output, label, cfg)
	if !toolOutputExceeds(limited, cfg) {
		return limited
	}
	if fitted, ok := fitToolOutputValue(output, label, cfg); ok {
		return fitted
	}
	if isErrorResultMap(limited) {
		return fallbackErrorToolOutput(limited, label, cfg)
	}
	return fallbackToolOutput(limited, label, cfg)
}

func LimitString(text, label string, limit ToolOutputLimit) string {
	return textprune.PruneWithEdges(text, label, toolOutputPruneConfig(limit))
}

func LimitError(err error, label string, limit ToolOutputLimit) error {
	if err == nil {
		return nil
	}
	limited := LimitString(err.Error(), label, limit)
	if limited == err.Error() {
		return err
	}
	return limitedError{cause: err, message: limited}
}

type limitedError struct {
	cause   error
	message string
}

func (e limitedError) Error() string {
	return e.message
}

func (e limitedError) Unwrap() error {
	return e.cause
}

func EncodedExceeds(value any, limit ToolOutputLimit) bool {
	cfg := toolOutputPruneConfig(limit)
	return toolOutputExceeds(value, cfg)
}

func MarshalString(value any) string {
	raw, err := json.Marshal(value)
	if err == nil {
		return string(raw)
	}
	return fmt.Sprint(value)
}

func NormalizedLimit(limit ToolOutputLimit) ToolOutputLimit {
	cfg := toolOutputPruneConfig(limit)
	return ToolOutputLimit{
		MaxBytes: cfg.MaxBytes,
		MaxLines: cfg.MaxLines,
	}
}

func limitToolOutputValue(value any, label string, cfg textprune.Config) any {
	switch v := value.(type) {
	case string:
		return textprune.PruneWithEdges(v, label, cfg)
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = textprune.PruneWithEdges(item, fmt.Sprintf("%s[%d]", label, i), cfg)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = limitToolOutputValue(item, fmt.Sprintf("%s[%d]", label, i), cfg)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for i, item := range v {
			out[i], _ = limitToolOutputValue(item, fmt.Sprintf("%s[%d]", label, i), cfg).(map[string]any)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, item := range v {
			out[key] = textprune.PruneWithEdges(item, toolOutputChildLabel(label, key), cfg)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = limitToolOutputValue(item, toolOutputChildLabel(label, key), cfg)
		}
		return out
	default:
		return value
	}
}

func toolOutputChildLabel(label, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return label
	}
	return label + "." + key
}

func toolOutputExceeds(value any, cfg textprune.Config) bool {
	return textprune.Exceeds(MarshalString(value), cfg.MaxBytes, cfg.MaxLines)
}

func fitToolOutputValue(value any, label string, cfg textprune.Config) (any, bool) {
	stringLeaves := countStringLeaves(value)
	if stringLeaves == 0 {
		return nil, false
	}
	divisors := []int{stringLeaves + 1, stringLeaves*2 + 2, stringLeaves*4 + 4}
	for _, divisor := range divisors {
		if divisor <= 0 {
			continue
		}
		budget := cfg.MaxBytes / divisor
		if budget < len(textprune.DefaultMarker) {
			continue
		}
		tightCfg := toolOutputPruneConfig(ToolOutputLimit{
			MaxBytes: budget,
			MaxLines: cfg.MaxLines,
		})
		fitted := limitToolOutputValue(value, label, tightCfg)
		if !toolOutputExceeds(fitted, cfg) {
			return fitted, true
		}
	}
	return nil, false
}

func countStringLeaves(value any) int {
	switch v := value.(type) {
	case string:
		return 1
	case []string:
		return len(v)
	case []any:
		count := 0
		for _, item := range v {
			count += countStringLeaves(item)
		}
		return count
	case []map[string]any:
		count := 0
		for _, item := range v {
			count += countStringLeaves(item)
		}
		return count
	case map[string]string:
		return len(v)
	case map[string]any:
		count := 0
		for _, item := range v {
			count += countStringLeaves(item)
		}
		return count
	default:
		return 0
	}
}

func isErrorResultMap(value any) bool {
	result, ok := value.(map[string]any)
	if !ok {
		return false
	}
	isError, _ := result["isError"].(bool)
	return isError
}

func fallbackErrorToolOutput(value any, label string, cfg textprune.Config) any {
	raw := MarshalString(value)
	content := fallbackContentString(raw, label, cfg)
	return map[string]any{
		"isError": true,
		"content": content,
	}
}

func fallbackToolOutput(value any, label string, cfg textprune.Config) any {
	raw := MarshalString(value)
	content := fallbackContentString(raw, label, cfg)
	return map[string]any{
		"_memoh_truncated": true,
		"content":          content,
	}
}

func fallbackContentString(raw, label string, cfg textprune.Config) string {
	contentBudget := cfg.MaxBytes - len(`{"_memoh_truncated":true,"content":""}`)
	if contentBudget <= 0 {
		return textprune.DefaultMarker
	}
	for contentBudget > 0 {
		contentCfg := toolOutputPruneConfig(ToolOutputLimit{
			MaxBytes: contentBudget,
			MaxLines: cfg.MaxLines,
		})
		content := textprune.PruneWithEdges(raw, label, contentCfg)
		if !textprune.Exceeds(MarshalString(map[string]any{
			"_memoh_truncated": true,
			"content":          content,
		}), cfg.MaxBytes, cfg.MaxLines) {
			return content
		}
		contentBudget = contentBudget * 3 / 4
	}
	return textprune.DefaultMarker
}

func toolOutputPruneConfig(limit ToolOutputLimit) textprune.Config {
	maxBytes := limit.MaxBytes
	if maxBytes <= 0 {
		maxBytes = textprune.DefaultMaxBytes
	} else if maxBytes < minToolOutputMaxBytes {
		maxBytes = minToolOutputMaxBytes
	}
	maxLines := limit.MaxLines
	if maxLines <= 0 {
		maxLines = textprune.DefaultMaxLines
	}
	headBytes, tailBytes := fitHeadTail(maxBytes, toolOutputHeadBytes, toolOutputTailBytes)
	headLines, tailLines := fitHeadTail(maxLines, toolOutputHeadLines, toolOutputTailLines)
	return textprune.Config{
		MaxBytes:  maxBytes,
		MaxLines:  maxLines,
		HeadBytes: headBytes,
		TailBytes: tailBytes,
		HeadLines: headLines,
		TailLines: tailLines,
		Marker:    textprune.DefaultMarker,
	}
}

func fitHeadTail(maxValue, head, tail int) (int, int) {
	if maxValue <= 0 {
		return head, tail
	}
	if head+tail <= maxValue {
		return head, tail
	}
	head = maxValue * 3 / 4
	tail = maxValue - head
	if head <= 0 {
		return maxValue, 0
	}
	return head, tail
}
