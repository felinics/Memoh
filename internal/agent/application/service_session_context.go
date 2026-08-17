package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
	tools "github.com/memohai/memoh/internal/agent/tool"
	messagepkg "github.com/memohai/memoh/internal/chat/message"
	session "github.com/memohai/memoh/internal/chat/thread"
)

const (
	defaultSessionContextMaxTokens = 6000
	minSessionContextMaxTokens     = 256
	maxSessionContextMaxTokens     = 16000
	maxSessionContextWindowMinutes = 7 * 24 * 60
	maxSessionContextRows          = 1000
)

// ComposeSessionContextWindow builds the read-only, LLM-facing context window
// of one session. Overview windows (no anchor) fold history with the active
// compaction summaries like the live turn path; anchored windows serve raw
// detail only, since their purpose is drilling past summaries. Both load at
// most maxSessionContextRows newest rows and trim to a token budget.
//
// Authorization contract: this method re-checks bot ownership only.
// Route/user visibility (visibleHistorySessions) is enforced at the tool
// boundary; every new caller must apply an equivalent session-visibility
// check before composing, or it will cross the DM/group isolation line.
func (s *Service) ComposeSessionContextWindow(ctx context.Context, req tools.SessionContextWindowRequest) (tools.SessionContextWindowResult, error) {
	botID := strings.TrimSpace(req.BotID)
	sessionID := strings.TrimSpace(req.SessionID)
	if botID == "" || sessionID == "" {
		return tools.SessionContextWindowResult{}, errors.New("bot_id and session_id are required")
	}
	if s == nil || s.sessionService == nil || s.messageService == nil {
		return tools.SessionContextWindowResult{}, errors.New("session context is not available")
	}

	thread, err := s.sessionService.Get(ctx, sessionID)
	if err != nil {
		return tools.SessionContextWindowResult{}, err
	}
	if strings.TrimSpace(thread.BotID) != botID {
		return tools.SessionContextWindowResult{}, errors.New("session_id does not belong to the current bot")
	}

	windowMinutes := req.WindowMinutes
	if windowMinutes <= 0 {
		windowMinutes = defaultMaxContextMinutes
	}
	if windowMinutes > maxSessionContextWindowMinutes {
		windowMinutes = maxSessionContextWindowMinutes
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultSessionContextMaxTokens
	}
	if maxTokens < minSessionContextMaxTokens {
		maxTokens = minSessionContextMaxTokens
	}
	if maxTokens > maxSessionContextMaxTokens {
		maxTokens = maxSessionContextMaxTokens
	}

	fallback := historyfrag.ScopeFallback{
		ConversationType: strings.TrimSpace(thread.RouteConversationType),
		ConversationName: sessionConversationName(thread),
	}

	var loaded []historyfrag.HistoryRecord
	var rowCapped bool
	anchorID := strings.TrimSpace(req.AnchorMessageID)
	if anchorID != "" {
		anchor, anchorErr := s.messageService.GetByIDBySession(ctx, sessionID, anchorID)
		if anchorErr != nil {
			if errors.Is(anchorErr, pgx.ErrNoRows) {
				return tools.SessionContextWindowResult{}, errors.New("around_message_id not found in the session")
			}
			return tools.SessionContextWindowResult{}, anchorErr
		}
		half := time.Duration(windowMinutes) * time.Minute / 2
		loaded, rowCapped, err = s.loadHistoryRecordsBetween(ctx, fallback, sessionID, anchor.CreatedAt.Add(-half), anchor.CreatedAt.Add(half))
	} else {
		end := time.Now().UTC()
		loaded, rowCapped, err = s.loadHistoryRecordsBetween(ctx, fallback, sessionID, end.Add(-time.Duration(windowMinutes)*time.Minute), end)
	}
	if err != nil {
		return tools.SessionContextWindowResult{}, err
	}

	loaded = pruneHistoryForGateway(loaded)
	if anchorID == "" {
		boundary := s.loadCompactionArtifactBoundary(ctx, loaded, sessionID, "")
		scope := compactionSummaryScope(botID, "", sessionID, fallback.ConversationType, fallback.ConversationName, "")
		loaded, err = s.replaceCompactedMessages(ctx, sessionID, scope, loaded, boundary)
		if err != nil {
			return tools.SessionContextWindowResult{}, err
		}
	}

	total := len(loaded)
	log := s.logger
	if log == nil {
		log = slog.Default()
	}
	_, records, estimatedTokens := trimMessagesAndRecordsByTokens(log, loaded, maxTokens)
	retained := 0
	for _, record := range records {
		if !record.Synthetic {
			retained++
		}
	}
	return tools.SessionContextWindowResult{
		Entries:         sessionContextEntriesFromRecords(records),
		EstimatedTokens: estimatedTokens,
		Truncated:       retained < total || rowCapped,
	}, nil
}

func (s *Service) loadHistoryRecordsBetween(ctx context.Context, fallback historyfrag.ScopeFallback, sessionID string, start, end time.Time) ([]historyfrag.HistoryRecord, bool, error) {
	if s.messageService == nil {
		return nil, false, nil
	}
	lister, ok := s.messageService.(messagepkg.ActiveWindowLister)
	if !ok {
		return nil, false, errors.New("windowed history listing is not supported")
	}
	msgs, err := lister.ListActiveBetweenBySession(ctx, sessionID, start, end, maxSessionContextRows)
	if err != nil {
		return nil, false, err
	}
	records, err := s.historyRecordsFromMessages(msgs, fallback)
	if err != nil {
		return nil, false, err
	}
	return records, len(msgs) >= maxSessionContextRows, nil
}

func sessionConversationName(thread session.Thread) string {
	if len(thread.RouteMetadata) == 0 {
		return ""
	}
	name, _ := thread.RouteMetadata["conversation_name"].(string)
	return strings.TrimSpace(name)
}

func sessionContextEntriesFromRecords(records []historyfrag.HistoryRecord) []tools.SessionContextEntry {
	entries := make([]tools.SessionContextEntry, 0, len(records))
	for _, record := range records {
		if record.Synthetic {
			continue
		}
		text := sessionContextEntryText(record.ModelMessage)
		if text == "" {
			continue
		}
		entry := tools.SessionContextEntry{
			Kind:      "message",
			Role:      strings.TrimSpace(record.ModelMessage.Role),
			Text:      text,
			SessionID: strings.TrimSpace(record.SessionID),
			CreatedAt: record.CreatedAt,
		}
		if record.SourceKind == historyfrag.SourceCompactionLog {
			entry.Kind = "summary"
			entry.CompactID = firstNonEmpty(strings.TrimSpace(record.CompactID), strings.TrimSpace(record.Ref.ID))
		} else {
			entry.MessageID = strings.TrimSpace(record.DBMessageID)
		}
		entries = append(entries, entry)
	}
	return entries
}

func sessionContextEntryText(msg ModelMessage) string {
	if text := tools.HistoryMessageDisplayText(msg); text != "" {
		return text
	}
	if strings.EqualFold(strings.TrimSpace(msg.Role), "tool") {
		return "[tool_result]"
	}
	return ""
}
