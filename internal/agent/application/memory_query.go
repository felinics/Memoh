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

// memoryQueryHistoryTokens bounds the history a memory query reads when no
// prepared history exists to reuse: it only renders a few recent user
// messages.
const memoryQueryHistoryTokens = 4096

// buildMemoryQuery reads the recent history itself, for runs that prepare
// none (external runtimes keep their own transcript). A native turn builds
// its query from the history it loaded instead (memoryQueryFromRecords).
func (s *Service) buildMemoryQuery(ctx context.Context, req ChatRequest) memoryQuery {
	if strings.TrimSpace(req.Query) == "" {
		return memoryQuery{}
	}
	cut, err := s.loadHistoryCut(ctx, req)
	var loaded []historyfrag.HistoryRecord
	if err == nil {
		scope := compactionSummaryScope(req.BotID, req.ChatID, req.ThreadID, req.ConversationType, req.ConversationName, req.ReplyTarget)
		loaded, _, err = s.loadHistoryRecords(ctx, historyScopeFallbackFromChatRequest(req), scope, memoryQueryHistoryTokens, cut)
	}
	if err != nil {
		s.logger.WarnContext(ctx, "memory query history load failed",
			slog.String("bot_id", req.BotID),
			slog.String("chat_id", req.ChatID),
			slog.String("session_id", req.ThreadID),
			slog.Any("error", err),
		)
	}
	return memoryQueryFromRecords(req, loaded)
}

// memoryQueryFromRecords builds the query from loaded records without
// touching them: only a copy of their user messages is deduplicated.
func memoryQueryFromRecords(req ChatRequest, records []historyfrag.HistoryRecord) memoryQuery {
	if strings.TrimSpace(req.Query) == "" {
		return memoryQuery{}
	}
	users := make([]historyfrag.HistoryRecord, 0, len(records))
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.ModelMessage.Role), "user") {
			users = append(users, record)
		}
	}
	users = dedupePersistedCurrentUserMessage(users, req)
	messages := make([]ModelMessage, len(users))
	for i := range users {
		messages[i] = users[i].ModelMessage
	}
	return defaultMemoryQueryBuilder().Build(req, messages)
}

// memoryQueryFromMessages builds the query from composed messages, leaving
// out the current input they already render.
func memoryQueryFromMessages(req ChatRequest, messages []ModelMessage) memoryQuery {
	recent := make([]ModelMessage, 0, len(messages))
	for _, message := range messages {
		if message.ContextSource == nil || !message.ContextSource.Current {
			recent = append(recent, message)
		}
	}
	return defaultMemoryQueryBuilder().Build(req, recent)
}

func (b memoryQueryBuilder) Build(req ChatRequest, history []ModelMessage) memoryQuery {
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

func (b memoryQueryBuilder) recentUserMessages(current string, history []ModelMessage) []string {
	if len(history) == 0 || b.MaxRecentMessages <= 0 {
		return nil
	}
	currentKey := normalizeMemoryQueryText(current)
	seen := map[string]bool{currentKey: true}
	reversed := make([]string, 0, b.MaxRecentMessages)
	for i := len(history) - 1; i >= 0 && len(reversed) < b.MaxRecentMessages; i-- {
		msg := history[i]
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
