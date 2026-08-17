package tools

import (
	"context"
	"errors"
	"strings"
	"time"
)

// SessionContextWindowRequest asks for a composed context window of one
// session: recent history by default, or a window centered on an anchor
// message. Callers must authorize the session before composing.
type SessionContextWindowRequest struct {
	BotID           string
	SessionID       string
	AnchorMessageID string
	WindowMinutes   int
	MaxTokens       int
}

// SessionContextEntry is one rendered element of a composed session window:
// either a history message (Kind "message", MessageID set) or a compaction
// summary covering evicted history (Kind "summary", CompactID set).
type SessionContextEntry struct {
	Kind      string
	Role      string
	Text      string
	MessageID string
	SessionID string
	CompactID string
	CreatedAt time.Time
}

type SessionContextWindowResult struct {
	Entries         []SessionContextEntry
	EstimatedTokens int
	Truncated       bool
}

// SessionContextComposer composes the LLM-facing context window of a session
// (history plus active compaction summaries, trimmed to a token budget).
type SessionContextComposer interface {
	ComposeSessionContextWindow(ctx context.Context, req SessionContextWindowRequest) (SessionContextWindowResult, error)
}

func (p *HistoryProvider) execGetSessionContext(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	if p.composer == nil {
		return nil, errors.New("session context is not available")
	}

	sessionID := strings.TrimSpace(StringArg(args, "session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(sess.SessionID)
	}
	if sessionID == "" {
		return nil, errors.New("session_id is required when there is no current session")
	}
	if sessionID != strings.TrimSpace(sess.SessionID) {
		if err := p.ensureSessionVisible(ctx, sess, sessionID); err != nil {
			return nil, err
		}
	}

	req := SessionContextWindowRequest{
		BotID:           botID,
		SessionID:       sessionID,
		AnchorMessageID: strings.TrimSpace(StringArg(args, "around_message_id")),
	}
	if v, ok, err := IntArg(args, "window_minutes"); err != nil {
		return nil, err
	} else if ok {
		req.WindowMinutes = v
	}
	if v, ok, err := IntArg(args, "max_tokens"); err != nil {
		return nil, err
	} else if ok {
		req.MaxTokens = v
	}

	result, err := p.composer.ComposeSessionContextWindow(ctx, req)
	if err != nil {
		return nil, err
	}

	entries := make([]map[string]any, 0, len(result.Entries))
	for _, entry := range result.Entries {
		item := map[string]any{
			"kind": entry.Kind,
			"role": entry.Role,
			"text": entry.Text,
		}
		if entry.SessionID != "" {
			item["session_id"] = entry.SessionID
		}
		if entry.MessageID != "" {
			item["message_id"] = entry.MessageID
		}
		if entry.CompactID != "" {
			item["compact_id"] = entry.CompactID
		}
		if !entry.CreatedAt.IsZero() {
			item["created_at"] = sess.FormatTime(entry.CreatedAt)
		}
		entries = append(entries, item)
	}

	return map[string]any{
		"ok":               true,
		"bot_id":           botID,
		"session_id":       sessionID,
		"count":            len(entries),
		"estimated_tokens": result.EstimatedTokens,
		"truncated":        result.Truncated,
		"entries":          entries,
	}, nil
}
