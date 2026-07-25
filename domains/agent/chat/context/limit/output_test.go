package limit

import (
	"strings"
	"testing"
)

func TestLimitStringPreservesHeadAndTail(t *testing.T) {
	t.Parallel()

	large := "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL"

	got := LimitString(large, "test_tool", ToolOutputLimit{MaxBytes: 512, MaxLines: 80})
	if len(got) > 512 {
		t.Fatalf("limited text bytes = %d, want <= 512\n%s", len(got), got)
	}
	if countLines(got) > 80 {
		t.Fatalf("limited text lines = %d, want <= 80\n%s", countLines(got), got)
	}
	for _, want := range []string{"[memoh pruned]", "HEAD", "TAIL"} {
		if !strings.Contains(got, want) {
			t.Fatalf("limited text missing %q:\n%s", want, got)
		}
	}
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}
