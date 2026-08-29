package channel

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/felinics/memoh/internal/textutil"
)

const (
	toolCallSummaryMaxRunes  = 200
	toolCallSummaryTruncMark = "…"
)

// SummarizeToolInput returns a short human-readable representation of a
// tool call's input payload, prioritizing known key fields (path, command,
// query, url, target, to, id, cron, action) before falling back to a compact
// JSON projection.
func SummarizeToolInput(_ string, input any) string {
	if input == nil {
		return ""
	}
	m, ok := normalizeToMap(input)
	if ok {
		if s := pickStringField(m, "path", "file_path", "filepath"); s != "" {
			return truncateSummary(s)
		}
		if s := pickStringField(m, "command", "cmd"); s != "" {
			return truncateSummary(firstLine(s))
		}
		if s := pickStringField(m, "query"); s != "" {
			return truncateSummary(s)
		}
		if s := pickStringField(m, "url"); s != "" {
			return truncateSummary(s)
		}
		if s := combineTargetAndBody(m); s != "" {
			return truncateSummary(s)
		}
		if s := pickStringField(m, "id"); s != "" {
			if cron := strings.TrimSpace(fmt.Sprint(m["cron"])); cron != "" && cron != "<nil>" {
				return truncateSummary(fmt.Sprintf("%s · %s", s, cron))
			}
			if action := strings.TrimSpace(fmt.Sprint(m["action"])); action != "" && action != "<nil>" {
				return truncateSummary(fmt.Sprintf("%s · %s", s, action))
			}
			return truncateSummary(s)
		}
		if s := pickStringField(m, "cron"); s != "" {
			return truncateSummary(s)
		}
		if s := pickStringField(m, "action"); s != "" {
			return truncateSummary(s)
		}
	}
	return compactJSONSummary(input)
}

// SummarizeToolResult returns a short representation of a tool call's result,
// surfacing status/error/count signals when present and otherwise falling
// back to trimmed text or a compact JSON projection.
func SummarizeToolResult(_ string, result any) string {
	if result == nil {
		return ""
	}
	if s, ok := result.(string); ok {
		return truncateSummary(strings.TrimSpace(s))
	}
	m, ok := normalizeToMap(result)
	if ok {
		parts := make([]string, 0, 4)
		if errStr := pickStringField(m, "error"); errStr != "" {
			return truncateSummary("error: " + errStr)
		}
		if okVal, okFound := m["ok"]; okFound {
			parts = append(parts, fmt.Sprintf("ok=%v", okVal))
		}
		if status := pickStringField(m, "status"); status != "" {
			parts = append(parts, "status="+status)
		}
		if code, ok := numericField(m, "exit_code"); ok {
			parts = append(parts, fmt.Sprintf("exit=%v", code))
		}
		if count, ok := numericField(m, "count"); ok {
			parts = append(parts, fmt.Sprintf("count=%v", count))
		}
		if msg := pickStringField(m, "message"); msg != "" {
			parts = append(parts, msg)
		}
		if stdout := pickStringField(m, "stdout"); stdout != "" {
			parts = append(parts, "stdout: "+firstLine(stdout))
		} else if stderr := pickStringField(m, "stderr"); stderr != "" {
			parts = append(parts, "stderr: "+firstLine(stderr))
		}
		if len(parts) > 0 {
			return truncateSummary(strings.Join(parts, " · "))
		}
	}
	return compactJSONSummary(result)
}

// isToolResultFailure inspects a tool result payload and reports whether it
// represents a failure (ok=false, non-empty error, non-zero exit_code).
func isToolResultFailure(result any) bool {
	if result == nil {
		return false
	}
	m, ok := normalizeToMap(result)
	if !ok {
		return false
	}
	if errStr := pickStringField(m, "error"); errStr != "" {
		return true
	}
	if okVal, okFound := m["ok"]; okFound {
		if b, ok := okVal.(bool); ok && !b {
			return true
		}
	}
	if code, ok := numericField(m, "exit_code"); ok {
		if code != 0 {
			return true
		}
	}
	return false
}

func normalizeToMap(v any) (map[string]any, bool) {
	switch val := v.(type) {
	case map[string]any:
		return val, true
	case json.RawMessage:
		if len(val) == 0 {
			return nil, false
		}
		var m map[string]any
		if err := json.Unmarshal(val, &m); err == nil {
			return m, true
		}
	case []byte:
		if len(val) == 0 {
			return nil, false
		}
		var m map[string]any
		if err := json.Unmarshal(val, &m); err == nil {
			return m, true
		}
	}
	return nil, false
}

func pickStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch val := v.(type) {
			case string:
				if s := strings.TrimSpace(val); s != "" {
					return s
				}
			case fmt.Stringer:
				if s := strings.TrimSpace(val.String()); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func numericField(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case json.Number:
		if f, err := val.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func combineTargetAndBody(m map[string]any) string {
	target := pickStringField(m, "target", "to", "recipient")
	body := pickStringField(m, "body", "content", "message", "text", "subject")
	if target != "" && body != "" {
		return fmt.Sprintf("→ %s: %s", target, body)
	}
	if target != "" {
		return "→ " + target
	}
	return ""
}

func truncateSummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return textutil.TruncateRunesWithSuffix(s, toolCallSummaryMaxRunes, toolCallSummaryTruncMark)
}

// compactJSONSummary is a last-resort projection for values where we cannot
// extract known key fields. It omits binary / base64 content and large arrays.
func compactJSONSummary(v any) string {
	if v == nil {
		return ""
	}
	if raw, ok := v.(json.RawMessage); ok {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err == nil {
			v = decoded
		} else {
			return truncateSummary(string(raw))
		}
	}
	projected := projectForSummary(v)
	bytes, err := json.Marshal(projected)
	if err != nil {
		return truncateSummary(fmt.Sprint(v))
	}
	return truncateSummary(string(bytes))
}

// projectForSummary reduces large / binary values before serialization so
// the summary stays short. It replaces base64-looking strings, truncates
// slices, and sorts map keys for stability.
func projectForSummary(v any) any {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(keys))
		for _, k := range keys {
			out[k] = projectForSummary(val[k])
		}
		return out
	case []any:
		if len(val) == 0 {
			return val
		}
		preview := 3
		if len(val) < preview {
			preview = len(val)
		}
		head := make([]any, 0, preview)
		for i := 0; i < preview; i++ {
			head = append(head, projectForSummary(val[i]))
		}
		if len(val) > preview {
			return map[string]any{
				"count":   len(val),
				"preview": head,
			}
		}
		return head
	case string:
		if isLikelyBase64(val) {
			return fmt.Sprintf("<binary %d bytes>", len(val))
		}
		if len(val) > 120 {
			return textutil.TruncateRunesWithSuffix(val, 120, toolCallSummaryTruncMark)
		}
		return val
	default:
		return val
	}
}

func isLikelyBase64(s string) bool {
	if len(s) < 200 {
		return false
	}
	if strings.ContainsAny(s, " \n\t") {
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(s); err == nil {
		return true
	}
	return false
}
