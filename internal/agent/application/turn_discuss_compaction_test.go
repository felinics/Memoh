package application

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/agent/turn"
)

func TestDiscussCompactableTokensIncludesRollingSummary(t *testing.T) {
	t.Parallel()

	messages := []turn.DiscussMessage{
		{Role: "user", Content: "<summary>\ncompacted window\n</summary>", CompactionArtifactID: "a1"},
		{Role: "user", Content: strings.Repeat("a", 400)},
		{Role: "assistant", Content: "[tool call: lookup]", RawContent: json.RawMessage(strings.Repeat("b", 800))},
		{Role: "user", Content: "<summary> pasted by a user " + strings.Repeat("c", 373)},
	}

	got := discussCompactableTokens(messages)
	want := estimateDiscussMessageTokens(messages[0]) + 400/2 + 800/2 + 400/2
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
	if got, want := discussCompactableTokens(summaryOnly), estimateDiscussMessageTokens(summaryOnly[0]); got != want {
		t.Fatalf("summary-only pressure = %d, want %d", got, want)
	}
}

func TestDiscussCompactionArtifactIDsAreStableAndUnique(t *testing.T) {
	t.Parallel()

	messages := []turn.DiscussMessage{
		{CompactionArtifactID: " artifact-1 "},
		{CompactionArtifactID: ""},
		{CompactionArtifactID: "artifact-2"},
		{CompactionArtifactID: "artifact-1"},
	}
	got := discussCompactionArtifactIDs(messages)
	if len(got) != 2 || got[0] != "artifact-1" || got[1] != "artifact-2" {
		t.Fatalf("discussCompactionArtifactIDs = %v, want [artifact-1 artifact-2]", got)
	}
}
