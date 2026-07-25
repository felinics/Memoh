package tool

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	agentdomain "github.com/memohai/memoh/domains/agent"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
)

const defaultMaxLookbackDays = 7

// SessionLister is the minimal interface for listing sessions.
type SessionLister interface {
	ListByBot(ctx context.Context, botID string) ([]session.Thread, error)
}

// HistoryMessageReader is the minimal interface for reading persisted messages.
type HistoryMessageReader interface {
	ListLatest(ctx context.Context, botID string, limit int32) ([]messagepkg.Message, error)
	ListBefore(ctx context.Context, botID string, before time.Time, limit int32) ([]messagepkg.Message, error)
	ListLatestBySession(ctx context.Context, sessionID string, limit int32) ([]messagepkg.Message, error)
	ListBeforeBySession(ctx context.Context, sessionID string, before time.Time, limit int32) ([]messagepkg.Message, error)
}

// HistorySearchFilter contains persistence-neutral message search criteria.
type HistorySearchFilter struct {
	BotID     string
	SessionID string
	ContactID string
	Role      string
	Keyword   string
	StartTime time.Time
	EndTime   time.Time
	Limit     int32
}

// HistorySearchResult is a persistence-neutral message search result.
type HistorySearchResult struct {
	ID        string
	SessionID string
	ContactID string
	Role      string
	Content   []byte
	CreatedAt time.Time
	Sender    string
	Platform  string
}

// HistorySearcher is the minimal interface for searching persisted messages.
type HistorySearcher interface {
	SearchHistory(ctx context.Context, filter HistorySearchFilter) ([]HistorySearchResult, error)
}

// HistoryProvider exposes list_sessions, get_messages, and search_messages tools.
type HistoryProvider struct {
	sessions SessionLister
	messages HistoryMessageReader
	searcher HistorySearcher
	logger   *slog.Logger
}

func NewHistoryProvider(log *slog.Logger, sessions SessionLister, messages HistoryMessageReader, searcher HistorySearcher) *HistoryProvider {
	if log == nil {
		log = slog.Default()
	}
	return &HistoryProvider{
		sessions: sessions,
		messages: messages,
		searcher: searcher,
		logger:   log.With(slog.String("tool", "history")),
	}
}

func (*HistoryProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	listSessionsRef := ""
	if ref, ok := available.Ref(ToolListSessions()); ok {
		listSessionsRef = ref
		parts = append(parts, ref+": List all chat sessions with their bound contact/route info. Filter by `type` (chat/heartbeat/schedule) or `platform`.")
	}
	if ref, ok := available.Ref(ToolGetMessages()); ok {
		parts = append(parts, ref+": Get recent messages from the current or selected session.")
		if listSessionsRef != "" {
			parts = append(parts, "Use session IDs from "+listSessionsRef+" as `session_id` for "+ref+" when reading a specific conversation.")
		}
	}
	if ref, ok := available.Ref(ToolSearchMessages()); ok {
		parts = append(parts, ref+": Search past message history. All parameters are optional: `start_time` / `end_time`, `keyword`, `session_id`, `contact_id`, and `role`.")
		if listSessionsRef != "" {
			parts = append(parts, "Use session IDs from "+listSessionsRef+" as `session_id` for "+ref+" when searching a specific conversation.")
		}
	}
	return usageSection("Sessions & History", parts)
}

func (p *HistoryProvider) Tools(_ context.Context, sess SessionContext) ([]sdk.Tool, error) {
	var tools []sdk.Tool

	if p.sessions != nil {
		s := sess
		tools = append(tools, sdk.Tool{
			Name:        ToolListSessions().String(),
			Description: "List all chat sessions for the current bot with their bound contact/route information.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Filter by session type: chat, heartbeat, or schedule. Returns all types when omitted.",
						"enum":        []string{"chat", "heartbeat", "schedule"},
					},
					"platform": map[string]any{
						"type":        "string",
						"description": "Filter by channel platform (e.g. telegram, feishu). Returns all platforms when omitted.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of sessions to return. Default 50.",
					},
				},
				"required": []string{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execListSessions(ctx.Context, s, inputAsMap(input))
			},
		})
	}

	if p.messages != nil {
		s := sess
		tools = append(tools, sdk.Tool{
			Name:        ToolGetMessages().String(),
			Description: "Get recent messages from a chat session. Defaults to the current session. Results are returned oldest-first.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID to read. Defaults to the current session when omitted.",
					},
					"before": map[string]any{
						"type":        "string",
						"description": "ISO 8601 timestamp cursor. When provided, returns messages created before this time.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of messages to return. Default 30, max 100.",
					},
				},
				"required": []string{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execGetMessages(ctx.Context, s, inputAsMap(input))
			},
		})
	}

	if p.searcher != nil {
		s := sess
		tools = append(tools, sdk.Tool{
			Name:        ToolSearchMessages().String(),
			Description: "Search message history across all sessions. Supports filtering by time range, keyword, session, contact, and role. All parameters are optional. If start_time is not provided, only the last 7 days are searched.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"start_time": map[string]any{
						"type":        "string",
						"description": "ISO 8601 timestamp. Only return messages created at or after this time.",
					},
					"end_time": map[string]any{
						"type":        "string",
						"description": "ISO 8601 timestamp. Only return messages created at or before this time.",
					},
					"keyword": map[string]any{
						"type":        "string",
						"description": "Search keyword — matches against the text content of messages (case-insensitive).",
					},
					"session_id": map[string]any{
						"type":        "string",
						"description": "Filter by session ID.",
					},
					"contact_id": map[string]any{
						"type":        "string",
						"description": "Filter by sender channel identity ID.",
					},
					"role": map[string]any{
						"type":        "string",
						"description": "Filter by message role.",
						"enum":        []string{"user", "assistant"},
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of messages to return. Default 50, max 200.",
					},
				},
				"required": []string{},
			},
			Execute: func(ctx *sdk.ToolExecContext, input any) (any, error) {
				return p.execSearchMessages(ctx.Context, s, inputAsMap(input))
			},
		})
	}

	return tools, nil
}

// ---------------------------------------------------------------------------
// list_sessions
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execListSessions(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	sessions, err := p.sessions.ListByBot(ctx, botID)
	if err != nil {
		return nil, err
	}

	typeFilter := strings.ToLower(strings.TrimSpace(StringArg(args, "type")))
	platformFilter := strings.ToLower(strings.TrimSpace(StringArg(args, "platform")))

	limit := 50
	if v, ok, _ := IntArg(args, "limit"); ok && v > 0 {
		limit = v
	}

	results := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		if typeFilter != "" && !strings.EqualFold(s.Type, typeFilter) {
			continue
		}
		if platformFilter != "" && !strings.EqualFold(s.ChannelType, platformFilter) {
			continue
		}

		entry := map[string]any{
			"session_id":        s.ID,
			"type":              s.Type,
			"title":             s.Title,
			"platform":          s.ChannelType,
			"route_id":          s.RouteID,
			"conversation_type": s.RouteConversationType,
			"last_active":       sess.FormatTime(s.UpdatedAt),
			"created_at":        sess.FormatTime(s.CreatedAt),
		}

		if m := s.RouteMetadata; len(m) > 0 {
			if v, _ := m["conversation_name"].(string); v != "" {
				entry["conversation_name"] = v
			}
			if v, _ := m["sender_display_name"].(string); v != "" {
				entry["display_name"] = v
			}
			if v, _ := m["sender_username"].(string); v != "" {
				entry["username"] = v
			}
		}

		results = append(results, entry)
		if len(results) >= limit {
			break
		}
	}

	return map[string]any{
		"ok":       true,
		"bot_id":   botID,
		"count":    len(results),
		"sessions": results,
	}, nil
}

// ---------------------------------------------------------------------------
// get_messages
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execGetMessages(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	limit := int32(30)
	if v, ok, err := IntArg(args, "limit"); err != nil {
		return nil, err
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
	if sessionID != "" && sessionID != strings.TrimSpace(sess.SessionID) {
		if err := p.ensureSessionBelongsToBot(ctx, botID, sessionID); err != nil {
			return nil, err
		}
	}

	var (
		messages []messagepkg.Message
		err      error
		before   time.Time
	)
	if rawBefore := StringArg(args, "before"); rawBefore != "" {
		before, err = parseFlexibleTime(rawBefore)
		if err != nil {
			return nil, err
		}
		if sessionID != "" {
			messages, err = p.messages.ListBeforeBySession(ctx, sessionID, before, limit)
		} else {
			messages, err = p.messages.ListBefore(ctx, botID, before, limit)
		}
	} else if sessionID != "" {
		messages, err = p.messages.ListLatestBySession(ctx, sessionID, limit)
		reverseHistoryMessages(messages)
	} else {
		messages, err = p.messages.ListLatest(ctx, botID, limit)
		reverseHistoryMessages(messages)
	}
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(messages))
	for _, msg := range messages {
		results = append(results, formatHistoryMessage(sess, msg))
	}

	out := map[string]any{
		"ok":       true,
		"bot_id":   botID,
		"count":    len(results),
		"messages": results,
	}
	if sessionID != "" {
		out["session_id"] = sessionID
	}
	if !before.IsZero() {
		out["before"] = sess.FormatTime(before)
	}
	return out, nil
}

func (p *HistoryProvider) ensureSessionBelongsToBot(ctx context.Context, botID, sessionID string) error {
	if p.sessions == nil {
		return errors.New("session lookup is not configured")
	}
	sessions, err := p.sessions.ListByBot(ctx, botID)
	if err != nil {
		return err
	}
	for _, item := range sessions {
		if item.ID == sessionID {
			return nil
		}
	}
	return errors.New("session_id does not belong to the current bot")
}

// ---------------------------------------------------------------------------
// search_messages
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execSearchMessages(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	limit := int32(50)
	if v, ok, _ := IntArg(args, "limit"); ok && v > 0 && v <= 200 {
		limit = int32(v) //nolint:gosec // bounds-checked above
	}

	filter := HistorySearchFilter{
		BotID:     botID,
		SessionID: StringArg(args, "session_id"),
		ContactID: StringArg(args, "contact_id"),
		Role:      StringArg(args, "role"),
		Keyword:   StringArg(args, "keyword"),
		Limit:     limit,
	}
	if v := StringArg(args, "start_time"); v != "" {
		if t, parseErr := parseFlexibleTime(v); parseErr == nil {
			filter.StartTime = t
		}
	} else {
		filter.StartTime = time.Now().UTC().AddDate(0, 0, -defaultMaxLookbackDays)
	}
	if v := StringArg(args, "end_time"); v != "" {
		if t, parseErr := parseFlexibleTime(v); parseErr == nil {
			filter.EndTime = t
		}
	}

	results, err := p.searcher.SearchHistory(ctx, filter)
	if err != nil {
		return nil, err
	}

	messages := make([]map[string]any, 0, len(results))
	for _, result := range results {
		entry := map[string]any{
			"id":         result.ID,
			"session_id": result.SessionID,
			"role":       result.Role,
			"text":       extractTextContent(result.Content),
			"created_at": sess.FormatTime(result.CreatedAt),
		}
		if result.Platform != "" {
			entry["platform"] = result.Platform
		}
		if result.Sender != "" {
			entry["sender"] = result.Sender
		}
		if result.ContactID != "" {
			entry["contact_id"] = result.ContactID
		}

		messages = append(messages, entry)
	}

	return map[string]any{
		"ok":       true,
		"bot_id":   botID,
		"count":    len(messages),
		"messages": messages,
	}, nil
}

func formatHistoryMessage(sess SessionContext, msg messagepkg.Message) map[string]any {
	entry := map[string]any{
		"id":         msg.ID,
		"session_id": msg.SessionID,
		"role":       msg.Role,
		"text":       extractTextContent(msg.Content),
		"created_at": sess.FormatTime(msg.CreatedAt),
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
	return entry
}

func reverseHistoryMessages(messages []messagepkg.Message) {
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}
}

// extractTextContent deserialises the JSONB content column (a ModelMessage)
// and returns a human-readable text summary.
func extractTextContent(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var msg agentdomain.ModelMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}

	if text := extractVisibleHistoryText(msg.Content); text != "" {
		return text
	}

	if names := extractHistoryToolCallNames(msg); len(names) > 0 {
		return "[tool_call: " + strings.Join(names, ", ") + "]"
	}

	if names := extractHistoryToolResultNames(msg.Content); len(names) > 0 {
		return "[tool_result: " + strings.Join(names, ", ") + "]"
	}

	return ""
}

type historyContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	URL      string          `json:"url,omitempty"`
	Emoji    string          `json:"emoji,omitempty"`
	ToolName string          `json:"toolName,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}

func extractVisibleHistoryText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return ""
		}
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			if nested := extractVisibleHistoryText(json.RawMessage(trimmed)); nested != "" {
				return nested
			}
		}
		return trimmed
	}

	parts := extractHistoryContentParts(raw)
	if len(parts) > 0 {
		lines := make([]string, 0, len(parts))
		for _, part := range parts {
			partType := strings.ToLower(strings.TrimSpace(part.Type))
			switch {
			case partType == "reasoning", partType == "tool-call", partType == "tool-result":
				continue
			case partType == "text" && strings.TrimSpace(part.Text) != "":
				lines = append(lines, strings.TrimSpace(part.Text))
			case partType == "link" && strings.TrimSpace(part.URL) != "":
				lines = append(lines, strings.TrimSpace(part.URL))
			case partType == "emoji" && strings.TrimSpace(part.Emoji) != "":
				lines = append(lines, strings.TrimSpace(part.Emoji))
			case strings.TrimSpace(part.Text) != "":
				lines = append(lines, strings.TrimSpace(part.Text))
			}
		}
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err == nil {
		if value, ok := object["text"].(string); ok {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func extractHistoryToolCallNames(msg agentdomain.ModelMessage) []string {
	names := make([]string, 0, len(msg.ToolCalls))
	for _, part := range extractHistoryContentParts(msg.Content) {
		if strings.ToLower(strings.TrimSpace(part.Type)) != "tool-call" {
			continue
		}
		if name := strings.TrimSpace(part.ToolName); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		return dedupeHistoryNames(names)
	}

	for _, tc := range msg.ToolCalls {
		if name := strings.TrimSpace(tc.Function.Name); name != "" {
			names = append(names, name)
		}
	}
	return dedupeHistoryNames(names)
}

func extractHistoryToolResultNames(raw json.RawMessage) []string {
	parts := extractHistoryContentParts(raw)
	if len(parts) == 0 {
		return nil
	}

	names := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ToLower(strings.TrimSpace(part.Type)) != "tool-result" {
			continue
		}
		if name := strings.TrimSpace(part.ToolName); name != "" {
			names = append(names, name)
		}
	}
	return dedupeHistoryNames(names)
}

func extractHistoryContentParts(raw json.RawMessage) []historyContentPart {
	if len(raw) == 0 {
		return nil
	}

	var parts []historyContentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		return parts
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		trimmed := strings.TrimSpace(encoded)
		if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			return extractHistoryContentParts(json.RawMessage(trimmed))
		}
	}

	var object struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && len(object.Content) > 0 {
		return extractHistoryContentParts(object.Content)
	}

	return nil
}

func dedupeHistoryNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

var timeFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseFlexibleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range timeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("unsupported time format")
}
