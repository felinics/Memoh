package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func (p *HistoryProvider) execGetMessages(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	executionPage, err := parseHistoryExecutionPage(args)
	if err != nil {
		return nil, err
	}
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	limit := int32(30)
	if v, ok, err := IntArg(args, "limit"); err != nil {
		return nil, err
	} else if raw, isFloat := args["limit"].(float64); isFloat && raw != float64(v) {
		return nil, errors.New("limit must be an integer")
	} else if ok && v > 0 {
		if v > 100 {
			v = 100
		}
		limit = int32(v) //nolint:gosec // upper-bounded above
	}

	sessionID := strings.TrimSpace(StringArg(args, "session_id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(sess.SessionID)
	}
	if sessionID == "" {
		return nil, errors.New("session_id is required when there is no current session")
	}
	if err := p.ensureSessionVisible(ctx, sess, sessionID); err != nil {
		return nil, err
	}
	messageID := strings.TrimSpace(StringArg(args, "message_id"))
	beforeMessageID := StringArg(args, "before_message_id")
	rawBefore := StringArg(args, "before")
	if (messageID != "" && (rawBefore != "" || beforeMessageID != "")) || (beforeMessageID != "" && rawBefore != "") {
		return nil, errors.New("message_id, before_message_id, and before cannot be used together")
	}

	var (
		messages []messagepkg.Message
		before   time.Time
	)
	switch {
	case messageID != "":
		message, loadErr := p.messages.GetByIDBySession(ctx, sessionID, messageID)
		switch {
		case errors.Is(loadErr, pgx.ErrNoRows):
			messages = []messagepkg.Message{}
		case loadErr != nil:
			return nil, loadErr
		default:
			messages = []messagepkg.Message{message}
		}
	case beforeMessageID != "":
		messages, err = p.messages.ListBeforeMessageBySession(ctx, sessionID, beforeMessageID, limit+1)
	case rawBefore != "":
		before, err = parseFlexibleTime(rawBefore)
		if err != nil {
			return nil, err
		}
		messages, err = p.messages.ListBeforeBySession(ctx, sessionID, before, limit+1)
	default:
		messages, err = p.messages.ListLatestBySession(ctx, sessionID, limit+1)
		reverseHistoryMessages(messages)
	}
	if err != nil {
		return nil, err
	}
	if beforeMessageID != "" && len(messages) == 0 {
		if _, err := p.messages.GetByIDBySession(ctx, sessionID, beforeMessageID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("before_message_id is no longer visible; restart message pagination")
			}
			return nil, err
		}
	}

	more := len(messages) > int(limit)
	if more {
		messages = messages[len(messages)-int(limit):]
	}
	out := map[string]any{
		"ok": true, "bot_id": botID, "session_id": sessionID,
		"count": 0, "messages": []map[string]any{}, "has_more": more,
	}
	if messageID != "" {
		out["message_id"] = messageID
	}
	if !before.IsZero() {
		out["before"] = sess.FormatTime(before)
	}
	if executionPage == nil {
		out["read_hint"] = "Chat text is a preview. Read full stored text/tool evidence with get_messages(message_id=id, view=execution)."
	} else {
		delete(out, "has_more")
	}
	results := make([]map[string]any, 0, len(messages))
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		var entry map[string]any
		if executionPage != nil {
			entry, err = executionPage.format(sess, msg)
			if err != nil {
				return nil, err
			}
		} else {
			entry = formatHistoryMessage(sess, msg)
		}
		candidate := append([]map[string]any{entry}, results...)
		out["count"], out["messages"] = len(candidate), candidate
		if executionPage == nil {
			out["has_more"] = more || i > 0
			if out["has_more"] == true {
				out["next_before_message_id"] = msg.ID
			}
			encoded, _ := json.Marshal(out)
			if len(encoded) > 32*1024 {
				if len(results) == 0 {
					return nil, errors.New("history message metadata exceeds the page budget; use an exact execution read")
				}
				out["count"], out["messages"], out["has_more"] = len(results), results, true
				out["next_before_message_id"] = results[0]["id"]
				break
			}
			if !more && i == 0 {
				delete(out, "next_before_message_id")
			}
		}
		results = candidate
	}
	return out, nil
}

func formatHistoryMessage(sess SessionContext, msg messagepkg.Message) map[string]any {
	text := extractTextContent(msg.Content)
	entry := map[string]any{
		"id":         msg.ID,
		"session_id": msg.SessionID,
		"role":       msg.Role,
		"text":       historySearchPreview(text, 1024),
		"created_at": sess.FormatTime(msg.CreatedAt),
	}
	if len(text) > 1024 {
		entry["text_truncated"] = true
	}
	if strings.TrimSpace(msg.Platform) != "" {
		entry["platform"] = msg.Platform
	}
	if strings.TrimSpace(msg.SenderDisplayName) != "" {
		entry["sender"] = msg.SenderDisplayName
	}
	if strings.TrimSpace(msg.SenderChannelIdentityID) != "" {
		entry["contact_id"] = msg.SenderChannelIdentityID
	}
	if strings.TrimSpace(msg.ExternalMessageID) != "" {
		entry["external_message_id"] = msg.ExternalMessageID
	}
	if strings.TrimSpace(msg.SourceReplyToMessageID) != "" {
		entry["source_reply_to_message_id"] = msg.SourceReplyToMessageID
	}
	if len(msg.Assets) > 0 {
		assets := make([]map[string]any, 0, len(msg.Assets))
		for _, asset := range msg.Assets {
			item := map[string]any{
				"content_hash": asset.ContentHash,
				"role":         asset.Role,
				"ordinal":      asset.Ordinal,
				"mime":         asset.Mime,
				"name":         asset.Name,
			}
			if asset.SizeBytes > 0 {
				item["size_bytes"] = asset.SizeBytes
			}
			assets = append(assets, item)
		}
		entry["assets"] = assets
	}
	if encoded, err := json.Marshal(entry); err == nil && len(encoded) > 8*1024 {
		entry = map[string]any{
			"id": msg.ID, "session_id": msg.SessionID, "role": msg.Role,
			"text": historySearchPreview(text, 1024), "created_at": sess.FormatTime(msg.CreatedAt),
			"metadata_truncated": true,
		}
		if len(text) > 1024 {
			entry["text_truncated"] = true
		}
	}
	return entry
}
