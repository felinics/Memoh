package formation

import (
	"strings"
	"testing"
)

func TestEmbeddedExtractPromptHasTodayPlaceholder(t *testing.T) {
	t.Parallel()
	if !strings.Contains(memoryExtractPrompt, "{{today}}") {
		t.Fatal("extract prompt must contain the {{today}} placeholder")
	}
}

func TestEmbeddedUpdatePromptNotEmpty(t *testing.T) {
	t.Parallel()
	if strings.TrimSpace(memoryUpdatePrompt) == "" {
		t.Fatal("update prompt must not be empty")
	}
}
