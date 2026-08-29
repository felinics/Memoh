package memllm

import (
	"strings"
	"testing"
)

func TestEmbeddedExtractPromptHasTodayPlaceholder(t *testing.T) {
	t.Parallel()
	if !strings.Contains(memoryExtractPrompt, "{{today}}") {
		t.Fatal("extract prompt must contain the {{today}} placeholder")
	}
	for _, want := range []string{"message_index", "message_indices", `"text"`} {
		if !strings.Contains(memoryExtractPrompt, want) {
			t.Fatalf("extract prompt must contain provenance contract %q", want)
		}
	}
}

func TestEmbeddedUpdatePromptNotEmpty(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(memoryUpdatePrompt) == "" {
		t.Fatal("update prompt must not be empty")
	}
}
