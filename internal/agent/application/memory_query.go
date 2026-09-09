package application

import (
	"context"
	"log/slog"
	"strings"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/prune"
	"github.com/felinics/memoh/internal/textutil"
)

const (
	defaultMemoryQueryRecentMessages = 4
	defaultMemoryQueryMaxBytes       = 1600
	defaultMemoryQueryMaxLines       = 40
	defaultMemoryQueryMessageRunes   = 280
)

type memoryQuery struct {
	Query          string
	Source         string
	RecentMessages int
	Truncated      bool
}

type memoryQueryBuilder struct {
	MaxRecentMessages int
	MaxBytes          int
	MaxLines          int
	MaxMessageRunes   int
}

func defaultMemoryQueryBuilder() memoryQueryBuilder {
	return memoryQueryBuilder{
		MaxRecentMessages: defaultMemoryQueryRecentMessages,
		MaxBytes:          defaultMemoryQueryMaxBytes,
		MaxLines:          defaultMemoryQueryMaxLines,
		MaxMessageRunes:   defaultMemoryQueryMessageRunes,
	}
}

func (s *Service) buildMemoryQuery(ctx context.Context, req ChatRequest) memoryQuery {
	builder := defaultMemoryQueryBuilder()
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return memoryQuery{}
	}
	if s == nil || s.messageService == nil {
		return builder.Build(req, nil)
	}
	loaded, err := s.loadHistoryRecords(ctx, historyScopeFallbackFromChatRequest(req), req.ThreadID, defaultMaxContextMinutes, 0)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("memory query history load failed",
				slog.String("bot_id", req.BotID),
				slog.String("chat_id", req.ChatID),
				slog.String("session_id", req.ThreadID),
				slog.Any("error", err),
			)
		}
		return builder.Build(req, nil)
	}
	loaded = pruneHistoryForGateway(loaded)
	artifactBoundary := s.loadCompactionArtifactBoundary(ctx, loaded, req.ThreadID, req.HistoryCutoffBeforeMessageID)
	loaded = filterMessagesBeforeID(loaded, req.HistoryCutoffBeforeMessageID)
	loaded, err = s.replaceCompactedMessages(ctx, req.ThreadID, compactionSummaryScope(req.BotID, req.ChatID, req.ThreadID, req.ConversationType, req.ConversationName, req.ReplyTarget), loaded, artifactBoundary)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("memory query compaction frontier load failed", slog.String("session_id", req.ThreadID), slog.Any("error", err))
		}
		return builder.Build(req, nil)
	}
	loaded = dedupePersistedCurrentUserMessage(loaded, req)
	return builder.Build(req, loaded)
}

func (b memoryQueryBuilder) Build(req ChatRequest, history []historyfrag.HistoryRecord) memoryQuery {
	current := strings.TrimSpace(req.Query)
	if current == "" {
		return memoryQuery{}
	}
	b = b.withDefaults()
	recent := b.recentUserMessages(current, history)
	if len(recent) == 0 {
		return memoryQuery{Query: b.prune(current).text, Source: "current_query"}
	}

	var sb strings.Builder
	sb.WriteString("Current user request:\n")
	sb.WriteString(current)
	sb.WriteString("\n\nRecent user context:\n")
	for _, msg := range recent {
		sb.WriteString("- ")
		sb.WriteString(msg)
		sb.WriteString("\n")
	}
	pruned := b.prune(strings.TrimSpace(sb.String()))
	return memoryQuery{
		Query:          pruned.text,
		Source:         "current_query_with_history",
		RecentMessages: len(recent),
		Truncated:      pruned.truncated,
	}
}

func (b memoryQueryBuilder) recentUserMessages(current string, history []historyfrag.HistoryRecord) []string {
	if len(history) == 0 || b.MaxRecentMessages <= 0 {
		return nil
	}
	currentKey := normalizeMemoryQueryText(current)
	seen := map[string]bool{currentKey: true}
	reversed := make([]string, 0, b.MaxRecentMessages)
	for i := len(history) - 1; i >= 0 && len(reversed) < b.MaxRecentMessages; i-- {
		msg := history[i].ModelMessage
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		text := strings.TrimSpace(msg.TextContent())
		if text == "" {
			continue
		}
		key := normalizeMemoryQueryText(text)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		reversed = append(reversed, textutil.TruncateRunesWithSuffix(text, b.MaxMessageRunes, "..."))
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed
}

type prunedMemoryQuery struct {
	text      string
	truncated bool
}

func (b memoryQueryBuilder) prune(text string) prunedMemoryQuery {
	text = strings.TrimSpace(text)
	if text == "" {
		return prunedMemoryQuery{}
	}
	pruned := prune.PruneWithEdges(text, "memory query", prune.Config{
		MaxBytes:  b.MaxBytes,
		MaxLines:  b.MaxLines,
		HeadBytes: max(1, b.MaxBytes*2/3),
		TailBytes: max(1, b.MaxBytes/3),
		HeadLines: max(1, b.MaxLines*2/3),
		TailLines: max(1, b.MaxLines/3),
		Marker:    "[memory query pruned]",
	})
	return prunedMemoryQuery{text: pruned, truncated: pruned != text}
}

func (b memoryQueryBuilder) withDefaults() memoryQueryBuilder {
	if b.MaxRecentMessages <= 0 {
		b.MaxRecentMessages = defaultMemoryQueryRecentMessages
	}
	if b.MaxBytes <= 0 {
		b.MaxBytes = defaultMemoryQueryMaxBytes
	}
	if b.MaxLines <= 0 {
		b.MaxLines = defaultMemoryQueryMaxLines
	}
	if b.MaxMessageRunes <= 0 {
		b.MaxMessageRunes = defaultMemoryQueryMessageRunes
	}
	return b
}

func normalizeMemoryQueryText(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(text)), " "))
}
