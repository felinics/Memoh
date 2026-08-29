package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestDiscussCompactableTokensExcludesSummaries(t *testing.T) {
	t.Parallel()

	messages := []turn.DiscussMessage{
		{Role: "user", Content: "<summary>\ncompacted window\n</summary>", CompactionArtifactID: "a1"},
		{Role: "user", Content: strings.Repeat("a", 400)},
		{Role: "assistant", Content: "[tool call: lookup]", RawContent: json.RawMessage(strings.Repeat("b", 800))},
		{Role: "user", Content: "<summary> pasted by a user " + strings.Repeat("c", 373)},
	}

	got := discussCompactableTokens(messages)
	want := 400/4 + 800/4 + 400/4
	if got != want {
		t.Fatalf("discussCompactableTokens = %d, want %d", got, want)
	}
}

func TestDiscussCompactableTokensEmpty(t *testing.T) {
	t.Parallel()

	if got := discussCompactableTokens(nil); got != 0 {
		t.Fatalf("expected 0 for empty messages, got %d", got)
	}
	summaryOnly := []turn.DiscussMessage{{Role: "user", Content: "<summary>\nx\n</summary>", CompactionArtifactID: "a1"}}
	if got := discussCompactableTokens(summaryOnly); got != 0 {
		t.Fatalf("expected 0 for summary-only messages, got %d", got)
	}
}
