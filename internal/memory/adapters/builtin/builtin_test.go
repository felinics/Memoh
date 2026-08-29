package builtin

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

func TestBuiltinProviderNilService(t *testing.T) {
	t.Parallel()
	p := NewBuiltinProvider(slog.Default(), nil)
	if p.Type() != BuiltinType {
		t.Fatalf("expected type %q, got %q", BuiltinType, p.Type())
	}

	result, err := p.OnBeforeChat(context.Background(), adapters.BeforeChatRequest{
		BotID: "bot-1",
		Query: "hello",
	})
	if err != nil {
		t.Fatalf("OnBeforeChat error: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil result for nil service, got %+v", result)
	}
}

func TestBuiltinProviderFileRuntimeDoesNotAdvertiseSemanticCompact(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	p := NewBuiltinProvider(slog.Default(), runtime)

	withoutLLM := p.SemanticCompactCapability()
	if withoutLLM.Semantic {
		t.Fatal("semantic compact should be unavailable without an LLM")
	}
	if withoutLLM.Reason == "" {
		t.Fatal("expected unavailable semantic compact to explain why")
	}

	p.SetLLM(&fakeLLM{})
	withLLM := p.SemanticCompactCapability()
	if withLLM.Semantic {
		t.Fatalf("file runtime should not advertise semantic compact: %+v", withLLM)
	}
	if !strings.Contains(withLLM.Reason, "does not support semantic compact") {
		t.Fatalf("file runtime compact reason = %q", withLLM.Reason)
	}
}

func TestBuiltinProviderSemanticCompactCapabilityWithGraphRuntime(t *testing.T) {
	t.Parallel()
	provider := NewBuiltinProvider(slog.Default(), NewGraphRuntime(nil, newFakeWikiStore(), newFakeStore()))
	provider.SetLLM(&fakeLLM{})

	capability := provider.SemanticCompactCapability()
	if !capability.Semantic {
		t.Fatalf("graph semantic compact should be available with LLM: %+v", capability)
	}
	if capability.Archive {
		t.Fatalf("graph compact should not advertise archive support without a graph archive store: %+v", capability)
	}
	if !capability.RebuildIndex {
		t.Fatalf("graph compact should advertise index rebuild support: %+v", capability)
	}
}

func TestBuiltinProviderOnBeforeChatEmptyQuery(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	p := NewBuiltinProvider(slog.Default(), runtime)

	result, err := p.OnBeforeChat(context.Background(), adapters.BeforeChatRequest{
		BotID: "bot-1",
		Query: "",
	})
	if err != nil {
		t.Fatalf("OnBeforeChat error: %v", err)
	}
	if result != nil {
		t.Fatal("expected nil result for empty query")
	}
}

func TestBuiltinProviderContextPackingProducesMemoryContextTags(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	p := NewBuiltinProvider(slog.Default(), runtime)

	_ = p.OnAfterChat(context.Background(), adapters.AfterChatRequest{
		BotID:    "bot-1",
		Messages: []adapters.Message{{Role: "user", Content: "I like green tea"}},
	})
	_ = p.OnAfterChat(context.Background(), adapters.AfterChatRequest{
		BotID:    "bot-1",
		Messages: []adapters.Message{{Role: "user", Content: "I work in Tokyo"}},
	})

	result, err := p.OnBeforeChat(context.Background(), adapters.BeforeChatRequest{
		BotID: "bot-1",
		Query: "tea",
	})
	if err != nil {
		t.Fatalf("OnBeforeChat error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if !strings.Contains(result.ContextText, "<memory-context>") {
		t.Fatalf("expected memory-context tags, got: %s", result.ContextText)
	}
	if !strings.Contains(result.ContextText, "</memory-context>") {
		t.Fatalf("expected closing memory-context tag, got: %s", result.ContextText)
	}
}

func TestBuiltinProviderApplyProviderConfig(t *testing.T) {
	t.Parallel()
	p := NewBuiltinProvider(slog.Default(), nil)

	p.ApplyProviderConfig(map[string]any{
		"context_target_items":    float64(10),
		"context_max_total_chars": float64(3000),
	})

	if p.packer.TargetItems != 10 {
		t.Fatalf("expected TargetItems=10, got %d", p.packer.TargetItems)
	}
	if p.packer.MaxTotalChars != 3000 {
		t.Fatalf("expected MaxTotalChars=3000, got %d", p.packer.MaxTotalChars)
	}
	if p.packer.MinItemChars != defaultPackerConfig.MinItemChars {
		t.Fatalf("expected MinItemChars to remain default, got %d", p.packer.MinItemChars)
	}
}

func TestBuiltinProviderApplyProviderConfigNil(t *testing.T) {
	t.Parallel()
	p := NewBuiltinProvider(slog.Default(), nil)
	p.ApplyProviderConfig(nil)
	if p.packer.TargetItems != defaultPackerConfig.TargetItems {
		t.Fatalf("expected default TargetItems, got %d", p.packer.TargetItems)
	}
}

func TestBuiltinProviderCompactUsesLLMResults(t *testing.T) {
	t.Parallel()
	store := newFakeWikiStore()
	runtime := NewGraphRuntime(nil, store, newFakeStore())
	llm := &fakeLLM{
		compactFacts: []string{"Ran likes tea, especially black tea and oolong."},
	}
	provider := NewBuiltinProvider(slog.Default(), runtime)
	provider.SetLLM(llm)
	ctx := context.Background()
	for _, memory := range []string{"Ran likes black tea", "Ran likes oolong tea"} {
		if _, err := provider.Add(ctx, adapters.AddRequest{
			BotID:    "bot-1",
			Message:  memory,
			Metadata: map[string]any{"subject": "Ran tea"},
		}); err != nil {
			t.Fatalf("seed graph memory: %v", err)
		}
	}

	result, err := provider.Compact(ctx, map[string]any{"bot_id": "bot-1"}, 1, 30)
	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if llm.compactCalls != 1 {
		t.Fatalf("expected LLM compact to be called once, got %d", llm.compactCalls)
	}
	if len(llm.compactReqs) != 1 {
		t.Fatalf("expected one compact request, got %d", len(llm.compactReqs))
	}
	req := llm.compactReqs[0]
	if req.TargetCount != 1 {
		t.Fatalf("expected target_count=1, got %d", req.TargetCount)
	}
	if req.DecayDays != 30 {
		t.Fatalf("expected decay_days=30, got %d", req.DecayDays)
	}
	if len(req.Memories) != 2 {
		t.Fatalf("expected 2 candidate memories, got %d", len(req.Memories))
	}
	if result.BeforeCount != 2 || result.AfterCount != 1 {
		t.Fatalf("unexpected counts: before=%d after=%d", result.BeforeCount, result.AfterCount)
	}
	all, err := provider.GetAll(ctx, adapters.GetAllRequest{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("GetAll after compact: %v", err)
	}
	if len(all.Results) != 1 || all.Results[0].Memory != llm.compactFacts[0] {
		t.Fatalf("graph compact results = %#v", all.Results)
	}
}

func TestBuiltinProviderCompactRequiresSemanticCompactCapability(t *testing.T) {
	t.Parallel()
	runtime := newFileRuntime(newFakeStore())
	provider := NewBuiltinProvider(slog.Default(), runtime)

	if _, err := provider.Compact(context.Background(), map[string]any{"bot_id": "bot-1"}, 0.5, 0); err == nil {
		t.Fatal("expected file runtime compact to fail instead of truncating memories")
	}
}

func TestIntFromConfig(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		m        map[string]any
		key      string
		expected int
	}{
		{"float64", map[string]any{"k": float64(42)}, "k", 42},
		{"int", map[string]any{"k": 10}, "k", 10},
		{"missing", map[string]any{}, "k", 0},
		{"nil_map", nil, "k", 0},
		{"string_value", map[string]any{"k": "abc"}, "k", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := intFromConfig(tc.m, tc.key)
			if got != tc.expected {
				t.Fatalf("expected %d, got %d", tc.expected, got)
			}
		})
	}
}

func TestBuiltinProviderCRUDErrorsWithNilService(t *testing.T) {
	t.Parallel()
	p := NewBuiltinProvider(slog.Default(), nil)
	if _, err := p.Add(context.Background(), adapters.AddRequest{}); err == nil {
		t.Fatal("expected Add error")
	}
	if _, err := p.GetAll(context.Background(), adapters.GetAllRequest{}); err == nil {
		t.Fatal("expected GetAll error")
	}
	if _, err := p.Update(context.Background(), adapters.UpdateRequest{}); err == nil {
		t.Fatal("expected Update error")
	}
	if _, err := p.Delete(context.Background(), "x"); err == nil {
		t.Fatal("expected Delete error")
	}
	if _, err := p.DeleteBatch(context.Background(), []string{"x"}); err == nil {
		t.Fatal("expected DeleteBatch error")
	}
	if _, err := p.DeleteAll(context.Background(), adapters.DeleteAllRequest{}); err == nil {
		t.Fatal("expected DeleteAll error")
	}
	if _, err := p.Compact(context.Background(), nil, 0.5, 0); err == nil {
		t.Fatal("expected Compact error")
	}
	if _, err := p.Usage(context.Background(), nil); err == nil {
		t.Fatal("expected Usage error")
	}
	if _, err := p.Status(context.Background(), "b"); err == nil {
		t.Fatal("expected Status error")
	}
	if _, err := p.Rebuild(context.Background(), "b"); err == nil {
		t.Fatal("expected Rebuild error")
	}
}

func TestNewBuiltinRuntimeFromConfig_DefaultIsGraphRequiringWikiStore(t *testing.T) {
	t.Parallel()
	// Default mode is now graph, so without a wiki store it must error.
	if _, err := NewBuiltinRuntimeFromConfig(nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error for default (graph) mode without wiki store")
	}
}

func TestNewBuiltinRuntimeFromConfig_LegacyDenseConfigUsesGraphWithoutAuxIndex(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"memory_mode": "dense"}
	rt, err := NewBuiltinRuntimeFromConfig(nil, cfg, nil, nil, nil, newFakeWikiStore())
	if err != nil {
		t.Fatalf("legacy dense config should fall back to graph without auxiliary index: %v", err)
	}
	if rt.Mode() != ModeGraph {
		t.Fatalf("Mode = %q, want %q", rt.Mode(), ModeGraph)
	}
}

func TestNewBuiltinRuntimeFromConfig_GraphRequiresWikiStore(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"memory_mode": "graph"}
	// No wiki store -> error.
	if _, err := NewBuiltinRuntimeFromConfig(nil, cfg, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error for graph mode without wiki store")
	}
}
