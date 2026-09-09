package builtin

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	adapters "github.com/felinics/memoh/internal/memory/adapters"
)

// fakeLLM implements adapters.LLM for testing the formation pipeline.
type fakeLLM struct {
	extractFacts       []string
	extractFactSources [][]string
	extractErr         error
	decideActions      []adapters.DecisionAction
	decideErr          error
	compactFacts       []string
	compactErr         error
	compactFunc        func(adapters.CompactRequest) adapters.CompactResponse
	extractCalls       int
	decideCalls        int
	compactCalls       int
	compactReqs        []adapters.CompactRequest
}

func (f *fakeLLM) Extract(_ context.Context, _ adapters.ExtractRequest) (adapters.ExtractResponse, error) {
	f.extractCalls++
	return adapters.ExtractResponse{Facts: f.extractFacts, FactSourceMessageIDs: f.extractFactSources}, f.extractErr
}

func (f *fakeLLM) Decide(_ context.Context, _ adapters.DecideRequest) (adapters.DecideResponse, error) {
	f.decideCalls++
	return adapters.DecideResponse{Actions: f.decideActions}, f.decideErr
}

func (f *fakeLLM) Compact(_ context.Context, req adapters.CompactRequest) (adapters.CompactResponse, error) {
	f.compactCalls++
	f.compactReqs = append(f.compactReqs, req)
	if f.compactFunc != nil {
		return f.compactFunc(req), f.compactErr
	}
	return adapters.CompactResponse{Facts: f.compactFacts}, f.compactErr
}

func TestFormationExtractAndAdd(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	llm := &fakeLLM{
		extractFacts: []string{"User likes oolong tea", "User is based in Berlin"},
		decideActions: []adapters.DecisionAction{
			{Event: "ADD", Text: "User likes oolong tea"},
			{Event: "ADD", Text: "User is based in Berlin"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I like oolong tea and I live in Berlin"},
			{Role: "assistant", Content: "Noted!"},
		},
	})

	if result.ExtractedFacts != 2 {
		t.Fatalf("expected 2 extracted facts, got %d", result.ExtractedFacts)
	}
	if result.Added != 2 {
		t.Fatalf("expected 2 adds, got %d", result.Added)
	}
	if result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("expected no updates/deletes, got updated=%d deleted=%d", result.Updated, result.Deleted)
	}
	if len(store.items) != 2 {
		t.Fatalf("expected 2 items in store, got %d", len(store.items))
	}
	if llm.extractCalls != 1 || llm.decideCalls != 1 {
		t.Fatalf("expected 1 extract + 1 decide call, got %d/%d", llm.extractCalls, llm.decideCalls)
	}
}

func TestFormationUpdate(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	addResp, err := runtime.Add(context.Background(), adapters.AddRequest{
		BotID:   "bot-1",
		Message: "User lives in Tokyo",
		Filters: map[string]any{"bot_id": "bot-1"},
	})
	if err != nil {
		t.Fatalf("seed Add failed: %v", err)
	}
	memID := addResp.Results[0].ID

	llm := &fakeLLM{
		extractFacts: []string{"User moved to Berlin"},
		decideActions: []adapters.DecisionAction{
			{Event: "UPDATE", ID: memID, Text: "User is based in Berlin", OldMemory: "User lives in Tokyo"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "Actually, I moved to Berlin"},
		},
	})

	if result.Updated != 1 {
		t.Fatalf("expected 1 update, got %d", result.Updated)
	}
	if result.Added != 0 {
		t.Fatalf("expected 0 adds, got %d", result.Added)
	}

	item, ok := store.items[memID]
	if !ok {
		t.Fatalf("expected memory %q to still exist", memID)
	}
	if !strings.Contains(item.Memory, "Berlin") {
		t.Fatalf("expected updated memory to contain Berlin, got %q", item.Memory)
	}
}

func TestFormationDelete(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	addResp, err := runtime.Add(context.Background(), adapters.AddRequest{
		BotID:   "bot-1",
		Message: "User likes coffee",
		Filters: map[string]any{"bot_id": "bot-1"},
	})
	if err != nil {
		t.Fatalf("seed Add failed: %v", err)
	}
	memID := addResp.Results[0].ID

	llm := &fakeLLM{
		extractFacts: []string{"User no longer drinks coffee"},
		decideActions: []adapters.DecisionAction{
			{Event: "DELETE", ID: memID},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I stopped drinking coffee"},
		},
	})

	if result.Deleted != 1 {
		t.Fatalf("expected 1 delete, got %d", result.Deleted)
	}
	if _, ok := store.items[memID]; ok {
		t.Fatal("expected memory to be deleted from store")
	}
}

func TestFormationNOOP(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	llm := &fakeLLM{
		extractFacts: []string{"User likes tea"},
		decideActions: []adapters.DecisionAction{
			{Event: "NOOP"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I like tea"},
		},
	})

	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", result.Skipped)
	}
	if result.Added != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("expected no mutations, got added=%d updated=%d deleted=%d", result.Added, result.Updated, result.Deleted)
	}
	if len(store.items) != 0 {
		t.Fatalf("expected 0 items in store, got %d", len(store.items))
	}
}

func TestFormationNoFacts(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	llm := &fakeLLM{
		extractFacts: []string{},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there!"},
		},
	})

	if result.ExtractedFacts != 0 {
		t.Fatalf("expected 0 extracted facts, got %d", result.ExtractedFacts)
	}
	if llm.decideCalls != 0 {
		t.Fatal("expected Decide to NOT be called when no facts extracted")
	}
}

func TestFormationMixedActions(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	addResp, _ := runtime.Add(context.Background(), adapters.AddRequest{
		BotID:   "bot-1",
		Message: "User lives in Tokyo",
		Filters: map[string]any{"bot_id": "bot-1"},
	})
	existingID := addResp.Results[0].ID

	llm := &fakeLLM{
		extractFacts: []string{"User moved to Berlin", "User prefers dark mode"},
		decideActions: []adapters.DecisionAction{
			{Event: "UPDATE", ID: existingID, Text: "User lives in Berlin"},
			{Event: "ADD", Text: "User prefers dark mode"},
			{Event: "NOOP"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I moved to Berlin and I like dark mode"},
		},
	})

	if result.Added != 1 {
		t.Fatalf("expected 1 add, got %d", result.Added)
	}
	if result.Updated != 1 {
		t.Fatalf("expected 1 update, got %d", result.Updated)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped, got %d", result.Skipped)
	}
	if len(store.items) != 2 {
		t.Fatalf("expected 2 items in store, got %d", len(store.items))
	}
}

func TestFormationInvalidActionsSkipped(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	llm := &fakeLLM{
		extractFacts: []string{"User likes cats"},
		decideActions: []adapters.DecisionAction{
			{Event: "ADD", Text: ""},
			{Event: "UPDATE", ID: "", Text: "something"},
			{Event: "DELETE", ID: ""},
			{Event: "UNKNOWN_EVENT", Text: "foo"},
			{Event: "ADD", Text: "User likes cats"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I like cats"},
		},
	})

	if result.Added != 1 {
		t.Fatalf("expected 1 valid add, got %d", result.Added)
	}
	if result.Skipped != 4 {
		t.Fatalf("expected 4 skipped (3 invalid + 1 unknown), got %d", result.Skipped)
	}
}

func TestFormationDuplicateActionsSameID(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	addResp, _ := runtime.Add(context.Background(), adapters.AddRequest{
		BotID:   "bot-1",
		Message: "User likes tea",
		Filters: map[string]any{"bot_id": "bot-1"},
	})
	memID := addResp.Results[0].ID

	llm := &fakeLLM{
		extractFacts: []string{"Updated fact"},
		decideActions: []adapters.DecisionAction{
			{Event: "UPDATE", ID: memID, Text: "User prefers coffee"},
			{Event: "UPDATE", ID: memID, Text: "User prefers juice"},
		},
	}

	result := runFormation(context.Background(), slog.Default(), llm, runtime, adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I changed my mind"},
		},
	})

	if result.Updated != 1 {
		t.Fatalf("expected 1 update (second should be deduped), got %d", result.Updated)
	}
	if result.Skipped != 1 {
		t.Fatalf("expected 1 skipped (duplicate), got %d", result.Skipped)
	}
}

func TestOnAfterChatWithLLM(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	llm := &fakeLLM{
		extractFacts: []string{"User prefers dark mode"},
		decideActions: []adapters.DecisionAction{
			{Event: "ADD", Text: "User prefers dark mode"},
		},
	}

	p := NewBuiltinProvider(slog.Default(), runtime)
	p.SetLLM(llm)

	err := p.OnAfterChat(context.Background(), adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I prefer dark mode"},
			{Role: "assistant", Content: "Got it!"},
		},
	})
	if err != nil {
		t.Fatalf("OnAfterChat error: %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("expected 1 fact stored, got %d", len(store.items))
	}
	for _, item := range store.items {
		if !strings.Contains(item.Memory, "dark mode") {
			t.Fatalf("expected stored fact to mention dark mode, got %q", item.Memory)
		}
	}
}

func TestOnAfterChatFallbackWithoutLLM(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)

	p := NewBuiltinProvider(slog.Default(), runtime)

	err := p.OnAfterChat(context.Background(), adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "Hello world"},
		},
	})
	if err != nil {
		t.Fatalf("OnAfterChat error: %v", err)
	}
	if len(store.items) != 1 {
		t.Fatalf("expected 1 item in store (legacy fallback), got %d", len(store.items))
	}
}

func TestOnBeforeChatRecallsFactMemory(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	runtime := newFileRuntime(store)
	llm := &fakeLLM{
		extractFacts: []string{"User prefers oolong tea"},
		decideActions: []adapters.DecisionAction{
			{Event: "ADD", Text: "User prefers oolong tea"},
		},
	}

	p := NewBuiltinProvider(slog.Default(), runtime)
	p.SetLLM(llm)

	_ = p.OnAfterChat(context.Background(), adapters.AfterChatRequest{
		BotID: "bot-1",
		Messages: []adapters.Message{
			{Role: "user", Content: "I prefer oolong tea"},
		},
	})

	result, err := p.OnBeforeChat(context.Background(), adapters.BeforeChatRequest{
		BotID: "bot-1",
		Query: "tea",
	})
	if err != nil {
		t.Fatalf("OnBeforeChat error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil context result")
		return
	}
	lower := strings.ToLower(result.ContextText)
	if !strings.Contains(lower, "oolong tea") {
		t.Fatalf("expected recalled context to mention oolong tea, got %q", result.ContextText)
	}
}
