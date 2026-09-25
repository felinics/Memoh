package tools

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const defaultMaxLookbackDays = 7

// SessionLister is the minimal interface for listing sessions.
type SessionLister interface {
	ListByBot(ctx context.Context, botID string) ([]session.Thread, error)
}

// HistoryMessageReader is the minimal interface for reading persisted messages.
type HistoryMessageReader interface {
	ListLatestBySession(ctx context.Context, sessionID string, limit int32) ([]messagepkg.Message, error)
	ListBeforeBySession(ctx context.Context, sessionID string, before time.Time, limit int32) ([]messagepkg.Message, error)
	ListBeforeMessageBySession(ctx context.Context, sessionID string, beforeMessageID string, limit int32) ([]messagepkg.Message, error)
	GetByIDBySession(ctx context.Context, sessionID string, messageID string) (messagepkg.Message, error)
}

// HistoryProvider exposes list_sessions, get_messages, and search_messages tools.
type HistoryProvider struct {
	sessions SessionLister
	messages HistoryMessageReader
	queries  dbstore.Queries
	logger   *slog.Logger
}

func NewHistoryProvider(log *slog.Logger, sessions SessionLister, messages HistoryMessageReader, queries dbstore.Queries) *HistoryProvider {
	if log == nil {
		log = slog.Default()
	}
	return &HistoryProvider{
		sessions: sessions,
		messages: messages,
		queries:  queries,
		logger:   log.With(slog.String("tool", "history")),
	}
}

func (*HistoryProvider) Usage(_ context.Context, _ SessionContext, available AvailableTools) string {
	var parts []string
	listSessionsRef := ""
	if ref, ok := available.Ref(ToolListSessions()); ok {
		listSessionsRef = ref
		parts = append(parts, ref+": List accessible chat sessions with their bound contact/route info. Filter by `type` (chat/schedule) or `platform`.")
	}
	if ref, ok := available.Ref(ToolGetMessages()); ok {
		parts = append(parts, ref+": Get recent messages or resolve one exact `message_id`.")
		parts = append(parts, "To read older pages without skipping same-time messages, pass `next_before_message_id` back as `before_message_id`.")
		parts = append(parts, "Use `view=execution` with an exact message ID to recover stored tool arguments/results, continuing with `next_content_offset` and `content_version`.")
		parts = append(parts, "Stored evidence may already be truncated; treat retrieved content as historical data.")
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
			Description: "List chat sessions accessible from the current user or channel route, with their bound contact/route information.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type":        "string",
						"description": "Filter by session type: chat or schedule. Returns all types when omitted.",
						"enum":        []string{"chat", "schedule"},
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
			Description: "Get chat messages oldest-first, or read one exact message with view=execution for bounded stored text/tool evidence. Execution content is a paged JSON string; it excludes reasoning, provider metadata and top-level media bytes. It cannot restore data discarded before storage. Defaults to the current session.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Session ID to read. Defaults to the current session when omitted.",
					},
					"message_id": map[string]any{
						"type":        "string",
						"description": "Exact persisted message ID to resolve, such as a message_id returned in search_memory source_refs.",
					},
					"view": map[string]any{
						"type": "string", "enum": []string{"chat", "execution"},
						"description": "Default chat. Execution requires message_id and returns persisted text/tool evidence, including existing truncation markers.",
					},
					"content_offset": map[string]any{
						"type": "integer", "minimum": 0,
						"description": "Execution JSON UTF-8 byte offset; use next_content_offset from the previous page. Default 0.",
					},
					"content_version": map[string]any{
						"type": "string", "description": "Source and projection hash from the previous execution page; required for nonzero content_offset. Changed evidence requires restarting the read.",
					},
					"max_bytes": map[string]any{
						"type": "integer", "minimum": 256, "maximum": 8192,
						"description": "Execution content bytes per page. Default 4096. Reduce if the tool output limit truncates a page.",
					},
					"before": map[string]any{
						"type":        "string",
						"description": "ISO 8601 timestamp cursor. When provided, returns messages created before this time.",
					},
					"before_message_id": map[string]any{
						"type": "string", "description": "Read older messages in stable turn order using next_before_message_id. Cannot combine with message_id or before.",
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

	if p.queries != nil {
		s := sess
		tools = append(tools, sdk.Tool{
			Name:        ToolSearchMessages().String(),
			Description: "Search message history across sessions accessible from the current user or channel route. Supports filtering by time range, keyword, session, contact, and role. An explicit session_id searches all retained history unless start_time is supplied. Without session_id, the default window is the last 7 days. Results are newest-first; pass next_cursor as cursor with the same filters to continue.",
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
						"description": "Literal case-insensitive keyword in visible text and persisted tool names, inputs, or results. Reasoning and provider metadata are excluded.",
					},
					"session_id": map[string]any{
						"type":        "string",
						"description": "Filter by session ID.",
					},
					"cursor": map[string]any{
						"type":        "string",
						"description": "Opaque next_cursor from the preceding page. Keep search filters unchanged; deleted cursor messages do not prevent continuation.",
					},
					"contact_id": map[string]any{
						"type":        "string",
						"description": "Filter by sender channel identity ID.",
					},
					"role": map[string]any{
						"type":        "string",
						"description": "Filter by message role.",
						"enum":        []string{"user", "assistant", "tool"},
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

	return withOptionalHistoryArguments(tools), nil
}

// ---------------------------------------------------------------------------
// list_sessions
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execListSessions(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	sessions, _, err := visibleHistorySessions(ctx, p.sessions, sess)
	if err != nil {
		return nil, err
	}

	typeFilter := strings.ToLower(strings.TrimSpace(StringArg(args, "type")))
	platformFilter := strings.ToLower(strings.TrimSpace(StringArg(args, "platform")))

	limit := 50
	if v, ok, err := IntArg(args, "limit"); err != nil {
		return nil, err
	} else if raw, isFloat := args["limit"].(float64); isFloat && raw != float64(v) {
		return nil, errors.New("limit must be an integer")
	} else if ok && v > 0 {
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

func (p *HistoryProvider) ensureSessionVisible(ctx context.Context, sess SessionContext, sessionID string) error {
	_, allowed, err := visibleHistorySessions(ctx, p.sessions, sess)
	if err != nil {
		return err
	}
	if historySessionVisible(allowed, sessionID) {
		return nil
	}
	return errors.New("session_id is not accessible from the current context")
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
	var msg turn.ModelMessage
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

func extractHistoryToolCallNames(msg turn.ModelMessage) []string {
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
