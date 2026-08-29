package builtin

import (
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
)

func TestRuntimeHash(t *testing.T) {
	t.Parallel()
	h1 := runtimeHash("hello")
	h2 := runtimeHash("  hello  ")
	if h1 != h2 {
		t.Fatalf("expected trimmed strings to produce same hash, got %q vs %q", h1, h2)
	}
	h3 := runtimeHash("world")
	if h1 == h3 {
		t.Fatal("expected different hashes for different inputs")
	}
}

func TestRuntimeBotID(t *testing.T) {
	t.Parallel()
	id, err := runtimeBotID("bot-1", nil)
	if err != nil || id != "bot-1" {
		t.Fatalf("expected bot-1, got %q, err=%v", id, err)
	}

	id, err = runtimeBotID("", map[string]any{"bot_id": "bot-2"})
	if err != nil || id != "bot-2" {
		t.Fatalf("expected bot-2 from filter, got %q, err=%v", id, err)
	}

	_, err = runtimeBotID("", nil)
	if err == nil {
		t.Fatal("expected error for empty bot_id")
	}
}

func TestRuntimeBotIDFromMemoryID(t *testing.T) {
	t.Parallel()
	if got := runtimeBotIDFromMemoryID("bot-1:mem_123"); got != "bot-1" {
		t.Fatalf("expected bot-1, got %q", got)
	}
	if got := runtimeBotIDFromMemoryID("invalid"); got != "" {
		t.Fatalf("expected empty for invalid format, got %q", got)
	}
}

func TestRuntimeLocalMemoryID(t *testing.T) {
	t.Parallel()
	if got := runtimeLocalMemoryID("bot-1:mem_123"); got != "mem_123" {
		t.Fatalf("expected mem_123, got %q", got)
	}
	if got := runtimeLocalMemoryID("mem_123"); got != "mem_123" {
		t.Fatalf("expected bare id to pass through, got %q", got)
	}
}

func TestCanonicalStoreItem(t *testing.T) {
	t.Parallel()
	item := storefs.MemoryItem{
		ID:     "  id-1  ",
		Memory: "  hello world  ",
	}
	c := canonicalStoreItem(item)
	if c.ID != "id-1" {
		t.Fatalf("expected trimmed ID, got %q", c.ID)
	}
	if c.Memory != "hello world" {
		t.Fatalf("expected trimmed memory, got %q", c.Memory)
	}
	if c.Hash == "" {
		t.Fatal("expected hash to be populated")
	}
}

func TestStoreItemRoundTrip(t *testing.T) {
	t.Parallel()
	original := adapters.MemoryItem{
		ID:     "id-1",
		Memory: "hello world",
		Hash:   "abc",
		BotID:  "bot-1",
	}
	store := storeItemFromMemoryItem(original)
	back := memoryItemFromStore(store)
	if back.ID != original.ID || back.Memory != original.Memory || back.BotID != original.BotID {
		t.Fatalf("round-trip failed: got %+v", back)
	}
}

func TestRuntimeText_SingleMessage(t *testing.T) {
	t.Parallel()
	text := runtimeText("hello", nil)
	if text != "hello" {
		t.Fatalf("expected 'hello', got %q", text)
	}
}

func TestRuntimeText_MultipleMessages(t *testing.T) {
	t.Parallel()
	msgs := []adapters.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
	}
	text := runtimeText("", msgs)
	if text == "" {
		t.Fatal("expected non-empty text from messages")
	}
	if !strings.Contains(text, "[USER] hi") || !strings.Contains(text, "[ASSISTANT] hello") {
		t.Fatalf("unexpected text format: %q", text)
	}
}
