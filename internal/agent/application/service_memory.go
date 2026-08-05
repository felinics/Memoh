package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	"github.com/memohai/memoh/internal/hooks"
	memprovider "github.com/memohai/memoh/internal/memory/adapters"
)

const defaultMemorySearchTimeout = 1200 * time.Millisecond

type memoryContextLoad struct {
	MemoryText string
	HookText   string
	Message    *ModelMessage
	Trace      *contextfrag.MemoryRecallTrace
}

func (s *Service) resolveMemoryProvider(ctx context.Context, botID string) memprovider.Provider {
	_, p := s.resolveMemoryProviderWithID(ctx, botID)
	return p
}

func (s *Service) resolveMemoryProviderWithID(ctx context.Context, botID string) (string, memprovider.Provider) {
	if s.memoryRegistry == nil {
		return "", nil
	}
	if s.settingsService == nil {
		return "", nil
	}
	botSettings, err := s.settingsService.GetBot(ctx, botID)
	if err != nil {
		return "", nil
	}
	providerID := strings.TrimSpace(botSettings.MemoryProviderID)
	if providerID == "" {
		return "", nil
	}
	p, err := s.memoryRegistry.Get(ctx, providerID)
	if err != nil {
		s.logger.Warn("memory provider lookup failed", slog.String("provider_id", providerID), slog.Any("error", err))
		return "", nil
	}
	return providerID, p
}

func (s *Service) loadMemoryContext(ctx context.Context, req ChatRequest) memoryContextLoad {
	builtQuery := s.buildMemoryQuery(ctx, req)
	if strings.TrimSpace(builtQuery.Query) == "" {
		return memoryContextLoad{}
	}
	providerID, p := s.resolveMemoryProviderWithID(ctx, req.BotID)
	if p == nil {
		return memoryContextLoad{}
	}
	cacheKey := s.memoryContextCacheKey(ctx, req, providerID, p, builtQuery.Query)

	before, err := s.runChatHook(ctx, req, hooks.EventBeforeMemorySearch, func(hreq *hooks.Request) {
		hreq.Memory = map[string]any{
			"scope":                 "before_chat",
			"query":                 builtQuery.Query,
			"visible_query":         strings.TrimSpace(req.Query),
			"query_source":          builtQuery.Source,
			"query_recent_messages": builtQuery.RecentMessages,
			"query_truncated":       builtQuery.Truncated,
		}
	})
	if err != nil {
		s.logHookWarn(hooks.EventBeforeMemorySearch, req.BotID, req.ThreadID, err)
		if before.Decision == hooks.DecisionDeny {
			return memoryContextLoad{Trace: memoryRecallTrace(cacheKey, builtQuery, nil, "bypass", "hook_denied")}
		}
	}

	if cached, ok := s.getMemoryContextCache().Get(cacheKey); ok {
		result := &memprovider.BeforeChatResult{
			ContextText:    cached.ContextText,
			RetrievalMode:  cached.RetrievalMode,
			FallbackReason: cached.FallbackReason,
			ResultCount:    cached.ResultCount,
			ResultRefs:     cached.ResultRefs,
		}
		return s.memoryContextFromResult(ctx, req, builtQuery, cacheKey, result, "fresh", "")
	}

	searchCtx, cancel := context.WithTimeout(ctx, s.effectiveMemorySearchTimeout())
	result, err := p.OnBeforeChat(searchCtx, memprovider.BeforeChatRequest{
		Query:  builtQuery.Query,
		BotID:  req.BotID,
		ChatID: req.ChatID,
	})
	cancel()

	if err != nil {
		fallbackReason := "provider_error"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
			fallbackReason = "timeout"
		}
		s.logger.Warn("memory provider OnBeforeChat failed",
			slog.String("bot_id", req.BotID),
			slog.String("fallback_reason", fallbackReason),
			slog.Any("error", err),
		)
		if cached, cacheState, ok := s.getMemoryContextCache().GetFreshOrStale(cacheKey); ok {
			result := &memprovider.BeforeChatResult{
				ContextText:    cached.ContextText,
				RetrievalMode:  cached.RetrievalMode,
				FallbackReason: firstNonEmpty(fallbackReason, cached.FallbackReason),
				ResultCount:    cached.ResultCount,
				ResultRefs:     cached.ResultRefs,
			}
			return s.memoryContextFromResult(ctx, req, builtQuery, cacheKey, result, string(cacheState), fallbackReason)
		}
		return s.memoryContextFromResult(ctx, req, builtQuery, cacheKey, nil, "miss", fallbackReason)
	}

	if result == nil || strings.TrimSpace(result.ContextText) == "" {
		return s.memoryContextFromResult(ctx, req, builtQuery, cacheKey, nil, "miss", "empty_result")
	}

	s.getMemoryContextCache().Set(cacheKey, memprovider.MemoryContextCacheValue{
		ContextText:    result.ContextText,
		RetrievalMode:  result.RetrievalMode,
		FallbackReason: result.FallbackReason,
		ResultCount:    result.ResultCount,
		ResultRefs:     result.ResultRefs,
	})
	return s.memoryContextFromResult(ctx, req, builtQuery, cacheKey, result, "miss", strings.TrimSpace(result.FallbackReason))
}

func (s *Service) memoryContextFromResult(ctx context.Context, req ChatRequest, builtQuery memoryQuery, cacheKey memprovider.MemoryContextCacheKey, result *memprovider.BeforeChatResult, cacheState, fallbackReason string) memoryContextLoad {
	contextText := ""
	if result != nil {
		contextText = strings.TrimSpace(result.ContextText)
	}
	trace := memoryRecallTrace(cacheKey, builtQuery, result, cacheState, fallbackReason)

	after, err := s.runChatHook(ctx, req, hooks.EventAfterMemorySearch, func(hreq *hooks.Request) {
		hreq.Memory = map[string]any{
			"scope":                 "before_chat",
			"query":                 builtQuery.Query,
			"visible_query":         strings.TrimSpace(req.Query),
			"query_source":          builtQuery.Source,
			"query_recent_messages": builtQuery.RecentMessages,
			"query_truncated":       builtQuery.Truncated,
			"result_count":          trace.Result.Count,
			"context_bytes":         trace.Result.ContextBytes,
			"cache_hit":             cacheState == "fresh" || cacheState == "stale",
			"cache_state":           cacheState,
			"retrieval_mode":        trace.RetrievalMode,
			"fallback_reason":       trace.FallbackReason,
		}
	})
	if err != nil {
		s.logHookWarn(hooks.EventAfterMemorySearch, req.BotID, req.ThreadID, err)
	}

	load := materializeMemoryContext(contextText, after.AppendContext)
	load.Trace = trace
	return load
}

func materializeMemoryContext(memoryText, hookText string) memoryContextLoad {
	memoryText = strings.TrimSpace(memoryText)
	hookText = formatServiceHookContext(hooks.EventAfterMemorySearch, hookText)
	combined := memoryText
	if hookText != "" {
		if combined != "" {
			combined += "\n\n"
		}
		combined += hookText
	}
	load := memoryContextLoad{MemoryText: memoryText, HookText: hookText}
	if combined != "" {
		load.Message = &ModelMessage{Role: "user", Content: newTextContent(combined)}
	}
	return load
}

func memoryRecallTrace(cacheKey memprovider.MemoryContextCacheKey, builtQuery memoryQuery, result *memprovider.BeforeChatResult, cacheState, fallbackReason string) *contextfrag.MemoryRecallTrace {
	contextText := ""
	resultCount := 0
	retrievalMode := ""
	var resultRefs []string
	if result != nil {
		contextText = strings.TrimSpace(result.ContextText)
		resultCount = max(result.ResultCount, 0)
		resultRefs = append([]string(nil), result.ResultRefs...)
		retrievalMode = strings.TrimSpace(result.RetrievalMode)
		if fallbackReason == "" {
			fallbackReason = strings.TrimSpace(result.FallbackReason)
		}
	}
	return &contextfrag.MemoryRecallTrace{
		ProviderID:     strings.TrimSpace(cacheKey.ProviderID),
		MemoryVersion:  strings.TrimSpace(cacheKey.MemoryVersion),
		CacheState:     strings.TrimSpace(cacheState),
		RetrievalMode:  retrievalMode,
		FallbackReason: strings.TrimSpace(fallbackReason),
		Query: contextfrag.MemoryRecallQueryTrace{
			Source:         builtQuery.Source,
			RecentMessages: builtQuery.RecentMessages,
			Truncated:      builtQuery.Truncated,
		},
		Result: contextfrag.MemoryRecallResultTrace{
			Count:        resultCount,
			Refs:         resultRefs,
			ContextBytes: len(contextText),
		},
	}
}

func (s *Service) getMemoryContextCache() *memprovider.MemoryContextCache {
	if s == nil {
		return nil
	}
	s.memoryContextMu.Lock()
	defer s.memoryContextMu.Unlock()
	if s.memoryContextCache == nil {
		s.memoryContextCache = memprovider.NewMemoryContextCache(memprovider.MemoryContextCacheConfig{
			TTL:        time.Minute,
			StaleTTL:   5 * time.Minute,
			MaxEntries: 256,
		})
	}
	return s.memoryContextCache
}

func (*Service) memoryContextCacheKey(ctx context.Context, req ChatRequest, providerID string, p memprovider.Provider, query string) memprovider.MemoryContextCacheKey {
	memoryVersion := ""
	if versioned, ok := p.(memprovider.MemoryVersionProvider); ok {
		memoryVersion = versioned.MemoryVersion(ctx, req.BotID)
	}
	return memprovider.MemoryContextCacheKey{
		BotID:         strings.TrimSpace(req.BotID),
		ChatID:        strings.TrimSpace(req.ChatID),
		ProviderID:    strings.TrimSpace(providerID),
		QueryHash:     memprovider.MemoryContextQueryHash(query),
		MemoryVersion: strings.TrimSpace(memoryVersion),
	}
}

func (s *Service) effectiveMemorySearchTimeout() time.Duration {
	if s == nil || s.memorySearchTimeout <= 0 {
		return defaultMemorySearchTimeout
	}
	return s.memorySearchTimeout
}

func (s *Service) storeMemory(ctx context.Context, req ChatRequest, persisted []messagepkg.Message) {
	botID := strings.TrimSpace(req.BotID)
	if botID == "" {
		return
	}
	if req.UserMessagePersisted || req.ReusePersistedUserMessage {
		userMessage, err := s.messageService.GetByIDBySession(ctx, req.ThreadID, req.PersistedUserMessageID)
		if err != nil {
			s.logger.Warn("load persisted user message for memory failed",
				slog.String("session_id", req.ThreadID),
				slog.String("message_id", req.PersistedUserMessageID),
				slog.Any("error", err),
			)
		} else {
			persisted = append([]messagepkg.Message{userMessage}, persisted...)
		}
	}
	memMsgs := toProviderMessages(persisted)
	if len(memMsgs) == 0 {
		return
	}

	p := s.resolveMemoryProvider(ctx, botID)
	if p == nil {
		return
	}
	before, err := s.runChatHook(ctx, req, hooks.EventBeforeMemoryWrite, func(hreq *hooks.Request) {
		hreq.Memory = map[string]any{
			"scope":         "after_chat",
			"message_count": len(memMsgs),
		}
	})
	if err != nil {
		s.logHookWarn(hooks.EventBeforeMemoryWrite, botID, req.ThreadID, err)
		if before.Decision == hooks.DecisionDeny {
			return
		}
	}
	_, tzLoc := s.resolveTimezone(ctx, req.BotID, req.UserID)
	if err := p.OnAfterChat(ctx, memprovider.AfterChatRequest{
		BotID:             botID,
		Messages:          memMsgs,
		UserID:            strings.TrimSpace(req.UserID),
		ChannelIdentityID: strings.TrimSpace(req.SourceChannelIdentityID),
		DisplayName:       s.resolveDisplayName(ctx, req),
		TimezoneLocation:  tzLoc,
	}); err != nil {
		s.logger.Warn("memory provider OnAfterChat failed", slog.String("bot_id", botID), slog.Any("error", err))
		return
	}
	_, _ = s.runChatHook(ctx, req, hooks.EventMemoryExtracted, func(hreq *hooks.Request) {
		hreq.Memory = map[string]any{
			"scope":         "after_chat",
			"message_count": len(memMsgs),
		}
	})
	if _, err := s.runChatHook(ctx, req, hooks.EventAfterMemoryWrite, func(hreq *hooks.Request) {
		hreq.Memory = map[string]any{
			"scope":         "after_chat",
			"message_count": len(memMsgs),
		}
	}); err != nil {
		s.logHookWarn(hooks.EventAfterMemoryWrite, botID, req.ThreadID, err)
	}
}

func toProviderMessages(persisted []messagepkg.Message) []memprovider.Message {
	out := make([]memprovider.Message, 0, len(persisted))
	for _, stored := range persisted {
		var msg ModelMessage
		if err := json.Unmarshal(stored.Content, &msg); err != nil {
			continue
		}
		text := strings.TrimSpace(msg.TextContent())
		if text == "" {
			continue
		}
		sessionID := strings.TrimSpace(stored.SessionID)
		ref := memprovider.EncodeSourceRef(sessionID, stored.ID)
		if _, _, ok := memprovider.ParseScopedSourceRef(ref); !ok {
			continue
		}
		role := strings.TrimSpace(stored.Role)
		if role == "" {
			role = strings.TrimSpace(msg.Role)
		}
		out = append(out, memprovider.Message{Role: role, Content: text, SourceMessageID: ref})
	}
	return out
}
