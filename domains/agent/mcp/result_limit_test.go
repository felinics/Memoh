package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLimitToolResultCapsFullMCPResult(t *testing.T) {
	t.Parallel()

	result := LimitToolResult(BuildToolSuccessResult(map[string]any{
		"content": "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL",
		"ok":      true,
	}), "test_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	assertJSONBytesAtMost(t, result, 512)
	text := toolResultText(result)
	if !strings.Contains(text, "[memoh pruned]") {
		t.Fatalf("limited MCP result missing prune marker: %#v", result)
	}
	if !strings.Contains(text, "HEAD") || !strings.Contains(text, "TAIL") {
		t.Fatalf("limited MCP result did not preserve head and tail: %#v", result)
	}
}

func TestLimitToolResultPreservesErrorSignal(t *testing.T) {
	t.Parallel()

	result := LimitToolResult(BuildToolErrorResult("HEAD\n"+strings.Repeat("error detail ", 300)+"\nTAIL"), "error_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	assertJSONBytesAtMost(t, result, 512)
	if isError, _ := result["isError"].(bool); !isError {
		t.Fatalf("limited MCP error result lost isError: %#v", result)
	}
	text := toolResultText(result)
	if !strings.Contains(text, "[memoh pruned]") {
		t.Fatalf("limited MCP error missing prune marker: %#v", result)
	}
	if !strings.Contains(text, "HEAD") || !strings.Contains(text, "TAIL") {
		t.Fatalf("limited MCP error did not preserve head and tail: %#v", result)
	}
}

func TestLimitToolResultCapsLineOnlyMCPText(t *testing.T) {
	t.Parallel()

	text := "HEAD\n" + strings.Repeat("x\n", 80) + "TAIL"

	for _, tc := range []struct {
		name   string
		result map[string]any
		error  bool
	}{
		{
			name:   "success",
			result: BuildToolSuccessResult(text),
		},
		{
			name:   "error",
			result: BuildToolErrorResult(text),
			error:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			result := LimitToolResult(tc.result, "line_tool", ToolOutputLimit{MaxBytes: 8192, MaxLines: 20})
			limited := toolResultText(result)
			if countLines(limited) > 20 {
				t.Fatalf("MCP text lines = %d, want <= 20\n%s", countLines(limited), limited)
			}
			if !strings.Contains(limited, "[memoh pruned]") {
				t.Fatalf("line-only MCP result missing prune marker: %#v", result)
			}
			if tc.error {
				if isError, _ := result["isError"].(bool); !isError {
					t.Fatalf("limited MCP error result lost isError: %#v", result)
				}
			}
		})
	}
}

func TestLimitToolResultBoundsOversizedStructuredContent(t *testing.T) {
	t.Parallel()

	result := LimitToolResult(BuildToolSuccessResult(map[string]any{
		"message": "HEAD\n" + strings.Repeat("structured detail ", 300) + "\nTAIL",
		"ok":      true,
	}), "structured_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})

	assertJSONBytesAtMost(t, result, 512)
	text := toolResultText(result)
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("structured result text missing %q:\n%s", want, text)
		}
	}
}

func TestLimitToolResultNormalizesTinyPositiveByteLimit(t *testing.T) {
	t.Parallel()

	result := LimitToolResult(BuildToolSuccessResult(map[string]any{
		"content": strings.Repeat("x", 1024),
	}), "tiny_mcp_tool", ToolOutputLimit{MaxBytes: 1, MaxLines: 80})

	assertJSONBytesAtMost(t, result, 256)
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func assertJSONBytesAtMost(t *testing.T, value any, maxBytes int) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if len(raw) > maxBytes {
		t.Fatalf("JSON bytes = %d, want <= %d\n%s", len(raw), maxBytes, raw)
	}
}
