package claudecode

import (
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestToolResultTranslationPreservesDisplayContent(t *testing.T) {
	for _, tc := range []struct{ name, tool, structured, text, field, want string }{
		{"bash", "exec", `{"stdout":"out","stderr":"err"}`, "combined", "stdout", "out"},
		{"unknown bash shape", "exec", `{"future_output":"out"}`, "fallback", "stdout", "fallback"},
		{"quiet bash", "exec", `{"stdout":"","stderr":""}`, "summary", "stdout", ""},
		{"read", "read", `{"file":{"content":"file text","totalLines":1}}`, "1→file text", "content", "file text"},
		{"fetch", "web_fetch", `{"result":"page text","code":200}`, "fallback", "content", "page text"},
		{"text-only search", "web_search", `null`, "search output", "content", "search output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content, _ := json.Marshal(tc.text)
			result, failed := translateToolResult(tc.tool, nil, content, json.RawMessage(tc.structured), false)
			if failed || result.(map[string]any)[tc.field] != tc.want {
				t.Fatalf("result = %#v, failed = %v", result, failed)
			}
			if _, invented := result.(map[string]any)["exit_code"]; invented {
				t.Fatal("invented an exit code")
			}
		})
	}
}

func TestFailedToolKeepsDiagnostic(t *testing.T) {
	result, failed := translateToolResult("exec", nil, json.RawMessage(`"Exit code 3: diagnostic"`), nil, true)
	raw, err := json.Marshal(result)
	if err != nil || !failed || string(raw) != `{"content":[{"text":"Exit code 3: diagnostic","type":"text"}],"error":"Exit code 3: diagnostic","isError":true}` {
		t.Fatalf("result = %s, failed = %v, err = %v", raw, failed, err)
	}
	presentation := channel.BuildToolCallEnd(&channel.StreamToolCall{Name: "exec", Result: result})
	if presentation.Status != channel.ToolCallStatusFailed || presentation.Footer != "error: Exit code 3: diagnostic" {
		t.Fatalf("IM lost failure diagnostic: %+v", presentation)
	}
}
