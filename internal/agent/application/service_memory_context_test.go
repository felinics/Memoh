package application

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	memprovider "github.com/felinics/memoh/internal/memory/adapters"
	"github.com/felinics/memoh/internal/settings"
)

func TestLoadMemoryContextMessage_NoProvider(t *testing.T) {
	resolver := &Service{
		logger: slog.Default(),
	}
	msg := resolver.loadMemoryContextMessage(context.Background(), ChatRequest{
		Query:  "hello",
		BotID:  "bot-1",
		ChatID: "chat-1",
	})
	if msg != nil {
		t.Fatalf("expected nil message when no memory provider is configured")
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
	msg := resolver.loadMemoryContextMessage(context.Background(), ChatRequest{
		Query:      "",
		ModelQuery: "The user activated the following skill for this turn without an additional prompt: alpha.",
		BotID:      storeRoundBotID,
		ChatID:     "chat-1",
	})
	if msg != nil {
		t.Fatalf("expected nil message for empty visible query, got %#v", msg)
	}
}

func TestLoadMemoryContextMessageUsesStaleCacheOnTimeout(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	provider := &slowBeforeChatProvider{
		result: &memprovider.BeforeChatResult{
			ContextText:   "<memory-context>cached memory</memory-context>",
			RetrievalMode: "graph",
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

	first := resolver.loadMemoryContextMessage(context.Background(), req)
	if first == nil || !strings.Contains(first.TextContent(), "cached memory") {
		t.Fatalf("expected first memory context, got %#v", first)
	}

	now = now.Add(2 * time.Millisecond)
	provider.waitForContext = true
	second := resolver.loadMemoryContextMessage(context.Background(), req)
	if second == nil || !strings.Contains(second.TextContent(), "cached memory") {
		t.Fatalf("expected stale memory context after timeout, got %#v", second)
	}
	if provider.calls < 2 {
		t.Fatalf("expected provider to be called again after fresh TTL expired, got %d calls", provider.calls)
	}
}

type slowBeforeChatProvider struct {
	memprovider.Provider
	result         *memprovider.BeforeChatResult
	waitForContext bool
	calls          int
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
