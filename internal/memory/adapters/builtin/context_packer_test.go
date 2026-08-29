package builtin

import (
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

func makeItems(texts ...string) []adapters.MemoryItem {
	items := make([]adapters.MemoryItem, len(texts))
	for i, text := range texts {
		items[i] = adapters.MemoryItem{
			ID:     "id-" + text[:min(len(text), 8)],
			Memory: text,
			Score:  float64(len(texts) - i),
		}
	}
	return items
}

func TestPackContext_BasicPacking(t *testing.T) {
	t.Parallel()
	items := makeItems("alpha", "bravo", "charlie", "delta", "echo", "foxtrot")
	cfg := contextPackerConfig{
		TargetItems:    4,
		MaxTotalChars:  2000,
		MinItemChars:   3,
		MaxItemChars:   100,
		OverfetchRatio: 2,
	}
	result := packContext(items, cfg)
	if len(result.Items) != 4 {
		t.Fatalf("expected 4 packed items, got %d", len(result.Items))
	}
	for _, pi := range result.Items {
		if pi.Snippet == "" {
			t.Fatal("expected non-empty snippet")
		}
	}
}

func TestPackContext_BudgetLimitsItems(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", 500)
	items := makeItems(long, long, long, long, long)
	cfg := contextPackerConfig{
		TargetItems:    5,
		MaxTotalChars:  800,
		MinItemChars:   100,
		MaxItemChars:   500,
		OverfetchRatio: 2,
	}
	result := packContext(items, cfg)
	totalChars := 0
	for _, pi := range result.Items {
		totalChars += len([]rune(pi.Snippet))
	}
	if totalChars > cfg.MaxTotalChars+50 {
		t.Fatalf("total chars %d exceeds budget %d by too much", totalChars, cfg.MaxTotalChars)
	}
}

func TestPackContext_CompressesToFitMore(t *testing.T) {
	t.Parallel()
	medium := strings.Repeat("m", 200)
	items := makeItems(medium, medium, medium, medium, medium, medium)
	cfg := contextPackerConfig{
		TargetItems:    6,
		MaxTotalChars:  600,
		MinItemChars:   50,
		MaxItemChars:   200,
		OverfetchRatio: 2,
	}
	result := packContext(items, cfg)
	if len(result.Items) < 3 {
		t.Fatalf("expected at least 3 items after compression, got %d", len(result.Items))
	}
}

func TestPackContext_ShortItemsNotTruncated(t *testing.T) {
	t.Parallel()
	items := makeItems("hi", "yo", "ok")
	cfg := contextPackerConfig{
		TargetItems:    3,
		MaxTotalChars:  1000,
		MinItemChars:   10,
		MaxItemChars:   200,
		OverfetchRatio: 2,
	}
	result := packContext(items, cfg)
	if len(result.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result.Items))
	}
	for _, pi := range result.Items {
		if strings.HasSuffix(pi.Snippet, "...") {
			t.Fatalf("short item should not be truncated: %q", pi.Snippet)
		}
	}
}

func TestPackContext_EmptyInput(t *testing.T) {
	t.Parallel()
	result := packContext(nil, defaultPackerConfig)
	if len(result.Items) != 0 {
		t.Fatalf("expected 0 items for nil input, got %d", len(result.Items))
	}
}

func TestAntiLostInMiddle_Reordering(t *testing.T) {
	t.Parallel()
	items := []int{1, 2, 3, 4, 5}
	reordered := antiLostInMiddle(items)
	if reordered[0] != 1 {
		t.Fatalf("expected first item to be 1, got %d", reordered[0])
	}
	if reordered[len(reordered)-1] != 2 {
		t.Fatalf("expected last item to be 2, got %d", reordered[len(reordered)-1])
	}
}

func TestAntiLostInMiddle_SmallSlice(t *testing.T) {
	t.Parallel()
	single := antiLostInMiddle([]string{"a"})
	if len(single) != 1 || single[0] != "a" {
		t.Fatalf("unexpected result for single item: %v", single)
	}
	pair := antiLostInMiddle([]string{"a", "b"})
	if len(pair) != 2 {
		t.Fatalf("unexpected result for pair: %v", pair)
	}
}

func TestOverfetchLimit(t *testing.T) {
	t.Parallel()
	cfg := contextPackerConfig{TargetItems: 5, OverfetchRatio: 3}
	if got := overfetchLimit(cfg); got != 15 {
		t.Fatalf("expected 15, got %d", got)
	}
}

func TestDeduplicateAndSort(t *testing.T) {
	t.Parallel()
	items := []adapters.MemoryItem{
		{ID: "a", Score: 1.0, Memory: "first"},
		{ID: "b", Score: 3.0, Memory: "second"},
		{ID: "a", Score: 2.0, Memory: "duplicate"},
		{ID: "c", Score: 2.5, Memory: "third"},
	}
	result := deduplicateAndSort(items)
	if len(result) != 3 {
		t.Fatalf("expected 3 items after dedup, got %d", len(result))
	}
	if result[0].ID != "b" {
		t.Fatalf("expected highest score first, got %q", result[0].ID)
	}
}
