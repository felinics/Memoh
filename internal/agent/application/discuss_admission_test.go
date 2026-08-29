package application

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestAdmitDiscussMessagesUnderBudgetPassthrough(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}
	admitted, admission := admitDiscussMessages(messages, 1000)
	if len(admitted) != 2 || admission.DroppedMessages != 0 || admission.ProtectedOverflow {
		t.Fatalf("under-budget must pass through: %+v", admission)
	}
}

func TestAdmitDiscussMessagesDropsOldestKeepsPinnedAndNewest(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("a", 4000)},
		{Role: "user", Content: "summary of earlier context", CompactionArtifactID: "a1"},
		{Role: "assistant", Content: strings.Repeat("b", 4000)},
		{Role: "user", Content: strings.Repeat("c", 400)},
	}
	admitted, admission := admitDiscussMessages(messages, 300)
	if admission.ProtectedOverflow {
		t.Fatalf("unexpected protected overflow: %+v", admission)
	}
	if admission.DroppedMessages == 0 {
		t.Fatalf("expected drops: %+v", admission)
	}
	var sawPinned, sawNewest bool
	for _, m := range admitted {
		if m.CompactionArtifactID == "a1" {
			sawPinned = true
		}
		if strings.Contains(m.Content, strings.Repeat("c", 400)) {
			sawNewest = true
		}
		if strings.Contains(m.Content, strings.Repeat("a", 4000)) {
			t.Fatal("oldest raw message must be dropped")
		}
	}
	if !sawPinned || !sawNewest {
		t.Fatalf("pinned summary and newest message must survive: pinned=%v newest=%v", sawPinned, sawNewest)
	}
	if admission.SelectedTokens > 300 {
		t.Fatalf("selected tokens %d exceed budget", admission.SelectedTokens)
	}
}

func TestAdmitDiscussMessagesProtectedOverflow(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "user", Content: strings.Repeat("x", 40000)},
	}
	admitted, admission := admitDiscussMessages(messages, 100)
	if admitted != nil {
		t.Fatal("protected overflow must return nil messages")
	}
	if !admission.ProtectedOverflow {
		t.Fatalf("expected ProtectedOverflow: %+v", admission)
	}
}

func TestAdmitDiscussMessagesDropsLeadingOrphanToolMessage(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("a", 8000)},
		{Role: "tool", Content: strings.Repeat("t", 40)},
		{Role: "assistant", Content: strings.Repeat("b", 40)},
		{Role: "user", Content: strings.Repeat("c", 40)},
	}
	admitted, admission := admitDiscussMessages(messages, 200)
	if admission.ProtectedOverflow {
		t.Fatalf("unexpected protected overflow: %+v", admission)
	}
	for _, m := range admitted {
		if m.Role == "tool" {
			t.Fatal("window must not open on an orphaned tool message")
		}
	}
}

func TestDiscussCompactableTokensUsesSharedEstimator(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "user", Content: strings.Repeat("x", 400)},
		{Role: "user", Content: "pinned", CompactionArtifactID: "a1"},
	}
	if got := discussCompactableTokens(messages); got != 100 {
		t.Fatalf("discussCompactableTokens = %d, want 100", got)
	}
}

func TestAdmitDiscussMessagesNewestOrphanToolFailsClosed(t *testing.T) {
	// When the budget fits only a newest tool response whose call fell
	// outside the window, admission must fail closed instead of handing the
	// provider an empty or summary-only context.
	messages := []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("a", 8000)},
		{Role: "user", Content: "summary", CompactionArtifactID: "a1"},
		{Role: "tool", Content: strings.Repeat("t", 40)},
	}
	admitted, admission := admitDiscussMessages(messages, 50)
	if admitted != nil {
		t.Fatalf("expected nil admitted messages, got %d", len(admitted))
	}
	if !admission.ProtectedOverflow {
		t.Fatalf("expected ProtectedOverflow, admission %+v", admission)
	}
}
