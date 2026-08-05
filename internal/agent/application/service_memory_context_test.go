package application

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/hooks"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
	"github.com/memohai/memoh/internal/settings"
)

func TestLoadMemoryContextMessage_NoProvider(t *testing.T) {
	resolver := &Service{
		logger: slog.Default(),
	}
	got := resolver.loadMemoryContext(context.Background(), ChatRequest{
		Query:  "hello",
		BotID:  "bot-1",
		ChatID: "chat-1",
	})
	if got.Message != nil || got.Trace != nil {
		t.Fatalf("expected empty memory load when no memory provider is configured: %#v", got)
	}
}

func TestLoadMemoryContextMessageSkipsEmptyQuery(t *testing.T) {
	t.Parallel()

	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, &storeRoundMemoryProvider{afterChat: make(chan memprovider.AfterChatRequest, 1)})
	resolver := &Service{
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}
	got := resolver.loadMemoryContext(context.Background(), ChatRequest{
		Query:      "",
		ModelQuery: "The user activated the following skill for this turn without an additional prompt: alpha.",
		BotID:      storeRoundBotID,
		ChatID:     "chat-1",
	})
	if got.Message != nil || got.Trace != nil {
		t.Fatalf("expected empty memory load for empty visible query, got %#v", got)
	}
}

func TestLoadMemoryContextMessageUsesStaleCacheOnTimeout(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	provider := &slowBeforeChatProvider{
		result: &memprovider.BeforeChatResult{
			ContextText:   "<memory-context>cached memory</memory-context>",
			RetrievalMode: "graph",
			ResultCount:   2,
			ResultRefs:    []string{"memory-1", "memory-2"},
		},
	}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, provider)
	resolver := &Service{
		memoryRegistry:      registry,
		settingsService:     settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:              slog.New(slog.DiscardHandler),
		memorySearchTimeout: 5 * time.Millisecond,
		memoryContextCache: memprovider.NewMemoryContextCache(memprovider.MemoryContextCacheConfig{
			TTL:      time.Millisecond,
			StaleTTL: time.Minute,
			Now: func() time.Time {
				return now
			},
		}),
	}
	req := ChatRequest{
		Query:  "tea",
		BotID:  storeRoundBotID,
		ChatID: "chat-1",
	}

	first := resolver.loadMemoryContext(context.Background(), req)
	if first.Message == nil || !strings.Contains(first.Message.TextContent(), "cached memory") {
		t.Fatalf("expected first memory context, got %#v", first)
	}
	assertMemoryTrace(t, first.Trace, "miss", "graph", "", "", 2, []string{"memory-1", "memory-2"}, len(provider.result.ContextText))

	fresh := resolver.loadMemoryContext(context.Background(), req)
	if fresh.Message == nil || !strings.Contains(fresh.Message.TextContent(), "cached memory") {
		t.Fatalf("expected fresh cached memory context, got %#v", fresh)
	}
	assertMemoryTrace(t, fresh.Trace, "fresh", "graph", "", "", 2, []string{"memory-1", "memory-2"}, len(provider.result.ContextText))
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want fresh cache hit", provider.calls)
	}

	now = now.Add(2 * time.Millisecond)
	provider.waitForContext = true
	second := resolver.loadMemoryContext(context.Background(), req)
	if second.Message == nil || !strings.Contains(second.Message.TextContent(), "cached memory") {
		t.Fatalf("expected stale memory context after timeout, got %#v", second)
	}
	assertMemoryTrace(t, second.Trace, "stale", "graph", "timeout", "", 2, []string{"memory-1", "memory-2"}, len(provider.result.ContextText))
	if provider.calls < 2 {
		t.Fatalf("expected provider to be called again after fresh TTL expired, got %d calls", provider.calls)
	}

	now = now.Add(2 * time.Minute)
	third := resolver.loadMemoryContext(context.Background(), req)
	if third.Message != nil {
		t.Fatalf("expired stale cache materialized context: %#v", third)
	}
	assertMemoryTrace(t, third.Trace, "miss", "", "timeout", "", 0, nil, 0)
}

func TestLoadMemoryContextMessageClassifiesCacheFilledDuringProviderWait(t *testing.T) {
	t.Parallel()

	provider := &blockingBeforeChatProvider{started: make(chan struct{})}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, provider)
	cache := memprovider.NewMemoryContextCache(memprovider.MemoryContextCacheConfig{})
	resolver := &Service{
		memoryRegistry:      registry,
		settingsService:     settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:              slog.New(slog.DiscardHandler),
		memorySearchTimeout: 100 * time.Millisecond,
		memoryContextCache:  cache,
	}
	req := ChatRequest{Query: "tea", BotID: storeRoundBotID, ChatID: "chat-1"}
	result := make(chan memoryContextLoad, 1)
	go func() {
		result <- resolver.loadMemoryContext(context.Background(), req)
	}()

	select {
	case <-provider.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	cache.Set(memprovider.MemoryContextCacheKey{
		BotID:      storeRoundBotID,
		ChatID:     "chat-1",
		ProviderID: storeRoundMemoryProviderID,
		QueryHash:  memprovider.MemoryContextQueryHash("tea"),
	}, memprovider.MemoryContextCacheValue{
		ContextText:   "<memory-context>concurrent result</memory-context>",
		RetrievalMode: "graph",
		ResultCount:   1,
		ResultRefs:    []string{"memory-1"},
	})

	select {
	case got := <-result:
		if got.Message == nil || !strings.Contains(got.Message.TextContent(), "concurrent result") {
			t.Fatalf("expected concurrently cached memory, got %#v", got)
		}
		assertMemoryTrace(t, got.Trace, "fresh", "graph", "timeout", "", 1, []string{"memory-1"}, len("<memory-context>concurrent result</memory-context>"))
	case <-time.After(time.Second):
		t.Fatal("memory lookup did not finish")
	}
}

func TestLoadMemoryContextMessageInvalidatesCacheWhenMemoryVersionChanges(t *testing.T) {
	t.Parallel()

	provider := &versionedBeforeChatProvider{
		slowBeforeChatProvider: slowBeforeChatProvider{result: &memprovider.BeforeChatResult{ContextText: "memory v1"}},
		version:                "v1",
	}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, provider)
	resolver := &Service{
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}
	req := ChatRequest{Query: "tea", BotID: storeRoundBotID, ChatID: "chat-1"}

	first := resolver.loadMemoryContext(context.Background(), req)
	if first.Message == nil || first.Message.TextContent() != "memory v1" {
		t.Fatalf("first memory context = %#v, want v1", first)
	}
	assertMemoryTrace(t, first.Trace, "miss", "", "", "v1", 0, nil, len("memory v1"))
	provider.version = "v2"
	provider.result = &memprovider.BeforeChatResult{ContextText: "memory v2"}
	second := resolver.loadMemoryContext(context.Background(), req)
	if second.Message == nil || second.Message.TextContent() != "memory v2" {
		t.Fatalf("second memory context = %#v, want v2", second)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want cache miss after version change", provider.calls)
	}
	assertMemoryTrace(t, second.Trace, "miss", "", "", "v2", 0, nil, len("memory v2"))
}

func TestLoadMemoryContextTracesEmptyResult(t *testing.T) {
	t.Parallel()

	provider := &slowBeforeChatProvider{}
	registry := memprovider.NewRegistry(slog.New(slog.DiscardHandler))
	registry.Register(storeRoundMemoryProviderID, provider)
	resolver := &Service{
		memoryRegistry:  registry,
		settingsService: settings.NewService(slog.New(slog.DiscardHandler), &storeRoundSettingsQueries{}, nil, nil),
		logger:          slog.New(slog.DiscardHandler),
	}

	got := resolver.loadMemoryContext(context.Background(), ChatRequest{
		Query: "tea", BotID: storeRoundBotID, ChatID: "chat-1",
	})
	if got.Message != nil {
		t.Fatalf("empty provider result materialized context: %#v", got)
	}
	assertMemoryTrace(t, got.Trace, "miss", "", "empty_result", "", 0, nil, 0)
}

func TestMaterializeMemoryContextSeparatesHookOutputWithoutChangingMessageBytes(t *testing.T) {
	t.Parallel()

	got := materializeMemoryContext(" remembered fact ", " plugin guidance ")
	if got.MemoryText != "remembered fact" {
		t.Fatalf("memory text = %q, want provider result only", got.MemoryText)
	}
	wantHook := "[Hook Context: " + hooks.EventAfterMemorySearch + "]\nplugin guidance"
	if got.HookText != wantHook {
		t.Fatalf("hook text = %q, want separately attributed hook context", got.HookText)
	}
	if got.Message == nil || got.Message.TextContent() != "remembered fact\n\n"+wantHook {
		t.Fatalf("legacy combined message = %#v", got.Message)
	}
}

type slowBeforeChatProvider struct {
	memprovider.Provider
	result         *memprovider.BeforeChatResult
	waitForContext bool
	calls          int
}

type blockingBeforeChatProvider struct {
	memprovider.Provider
	started chan struct{}
}

func (*blockingBeforeChatProvider) Type() string {
	return "test"
}

func (p *blockingBeforeChatProvider) OnBeforeChat(ctx context.Context, _ memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*slowBeforeChatProvider) Type() string {
	return "test"
}

func (p *slowBeforeChatProvider) OnBeforeChat(ctx context.Context, _ memprovider.BeforeChatRequest) (*memprovider.BeforeChatResult, error) {
	p.calls++
	if p.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return p.result, nil
}

type versionedBeforeChatProvider struct {
	slowBeforeChatProvider
	version string
}

func (p *versionedBeforeChatProvider) MemoryVersion(context.Context, string) string {
	return p.version
}

func assertMemoryTrace(t *testing.T, trace *contextfrag.MemoryRecallTrace, cacheState, retrievalMode, fallbackReason, memoryVersion string, count int, refs []string, contextBytes int) {
	t.Helper()
	if trace == nil {
		t.Fatal("memory trace is nil")
	}
	if trace.ProviderID != storeRoundMemoryProviderID || trace.MemoryVersion != memoryVersion || trace.CacheState != cacheState ||
		trace.RetrievalMode != retrievalMode || trace.FallbackReason != fallbackReason {
		t.Fatalf("memory trace = %#v", trace)
	}
	if trace.Query.Source != "current_query" || trace.Query.RecentMessages != 0 || trace.Query.Truncated {
		t.Fatalf("query provenance = %#v", trace.Query)
	}
	if trace.Result.Count != count || trace.Result.ContextBytes != contextBytes || !slices.Equal(trace.Result.Refs, refs) {
		t.Fatalf("result trace = %#v, want count=%d refs=%#v bytes=%d", trace.Result, count, refs, contextBytes)
	}
}
