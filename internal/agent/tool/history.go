package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	contextlimit "github.com/felinics/memoh/internal/agent/context/limit"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
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
	GetByIDBySession(ctx context.Context, sessionID string, messageID string) (messagepkg.Message, error)
}

// HistoryProvider exposes list_sessions, get_messages, and search_messages tools.
type HistoryProvider struct {
	sessions    SessionLister
	messages    HistoryMessageReader
	queries     dbstore.Queries
	outputLimit ToolOutputLimit
	logger      *slog.Logger
}

// NewHistoryProvider takes the tool output limit the runtime wraps every tool
// in, so a page of a stored tool result is cut to fit it rather than elided.
func NewHistoryProvider(log *slog.Logger, sessions SessionLister, messages HistoryMessageReader, queries dbstore.Queries, outputLimit ToolOutputLimit) *HistoryProvider {
	if log == nil {
		log = slog.Default()
	}
	return &HistoryProvider{
		sessions:    sessions,
		messages:    messages,
		queries:     queries,
		outputLimit: outputLimit,
		logger:      log.With(slog.String("tool", "history")),
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
		parts = append(parts, ref+": Get recent messages from the current or selected session, resolve one exact `message_id`, or page through an earlier tool result a summary cites by `ref`.")
		if listSessionsRef != "" {
			parts = append(parts, "Use session IDs from "+listSessionsRef+" as `session_id` for "+ref+" when reading a specific conversation.")
		}
	}
	if ref, ok := available.Ref(ToolSearchMessages()); ok {
		parts = append(parts, ref+": Search past message history. All parameters are optional: `start_time` / `end_time`, `keyword`, `session_id`, `contact_id`, and `role`.")
		parts = append(parts, "When a history notice says earlier messages of this conversation are not in the context, search them with "+ref+" by the time range it gives as `start_time` / `end_time`; without `start_time` it searches only the last 7 days.")
		if listSessionsRef != "" {
			parts = append(parts, "Use session IDs from "+listSessionsRef+" as `session_id` for "+ref+" when searching a specific conversation.")
		}
	}
	return usageSection("Sessions & History", parts)
}

func (p *HistoryProvider) Tools(_ context.Context, sess SessionContext) ([]toolexec.Tool, error) {
	var tools []toolexec.Tool

	if p.sessions != nil {
		s := sess
		tools = append(tools, toolexec.Tool{
			Name:        ToolListSessions().String(),
			Description: "List chat sessions accessible from the current user or channel route, with their bound contact/route information.",
			Parameters:  toolexec.SchemaFor[listSessionsArgs](toolexec.Enum("type", "chat", "schedule")),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args listSessionsArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execListSessions(ctx.Context, s, args))
			}),
		})
	}

	if p.messages != nil {
		s := sess
		tools = append(tools, toolexec.Tool{
			Name:        ToolGetMessages().String(),
			Description: "Get recent messages from a chat session, or resolve one exact message ID. Defaults to the current session. Results are returned oldest-first. When a summary cites an earlier tool result as ref=<message id>:<part index> and you need its original output, pass that ref: the stored result text of the current session comes back in pages with total_bytes; call again with offset set to next_offset until next_offset is absent.",
			Parameters:  toolexec.SchemaFor[getMessagesArgs](toolexec.Minimum("offset", 0), toolexec.Minimum("max_bytes", 1)),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args getMessagesArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execGetMessages(ctx.Context, s, args))
			}),
		})
	}

	if p.queries != nil {
		s := sess
		tools = append(tools, toolexec.Tool{
			Name:        ToolSearchMessages().String(),
			Description: "Search message history across sessions accessible from the current user or channel route. Supports filtering by time range, keyword, session, contact, and role. All parameters are optional. If start_time is not provided, only the last 7 days are searched.",
			Parameters:  toolexec.SchemaFor[searchMessagesArgs](toolexec.Enum("role", "user", "assistant")),
			Execute: toolexec.Typed(func(ctx *toolexec.ToolExecContext, args searchMessagesArgs) (sdk.ToolOutput, error) {
				return toolexec.OutputPair(p.execSearchMessages(ctx.Context, s, args))
			}),
		})
	}

	return tools, nil
}

// ---------------------------------------------------------------------------
// list_sessions
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execListSessions(ctx context.Context, sess SessionContext, args listSessionsArgs) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	sessions, _, err := visibleHistorySessions(ctx, p.sessions, sess)
	if err != nil {
		return nil, err
	}

	typeFilter := strings.ToLower(strings.TrimSpace(args.Type))
	platformFilter := strings.ToLower(strings.TrimSpace(args.Platform))

	limit := 50
	if args.Limit != nil && *args.Limit > 0 {
		limit = *args.Limit
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

func (p *HistoryProvider) execGetMessages(ctx context.Context, sess SessionContext, args getMessagesArgs) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}
	if strings.TrimSpace(args.Ref) != "" {
		return p.execGetToolResult(ctx, sess, args)
	}
	if args.Offset != nil || args.MaxBytes != nil {
		return nil, errors.New("offset and max_bytes page a tool result and require ref")
	}

	limit := int32(30)
	if args.Limit != nil && *args.Limit > 0 {
		v := min(*args.Limit, 100)
		limit = int32(v) //nolint:gosec // upper-bounded above
	}

	sessionID := strings.TrimSpace(args.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(sess.SessionID)
	}
	if sessionID == "" {
		return nil, errors.New("session_id is required when there is no current session")
	}
	if sessionID != "" && sessionID != strings.TrimSpace(sess.SessionID) {
		if err := p.ensureSessionVisible(ctx, sess, sessionID); err != nil {
			return nil, err
		}
	}
	messageID := strings.TrimSpace(args.MessageID)
	if messageID != "" && strings.TrimSpace(args.Before) != "" {
		return nil, errors.New("message_id and before cannot be used together")
	}

	var (
		messages []messagepkg.Message
		err      error
		before   time.Time
	)
	if messageID != "" {
		message, loadErr := p.messages.GetByIDBySession(ctx, sessionID, messageID)
		switch {
		case errors.Is(loadErr, pgx.ErrNoRows):
			messages = []messagepkg.Message{}
		case loadErr != nil:
			return nil, loadErr
		default:
			messages = []messagepkg.Message{message}
		}
	} else if rawBefore := strings.TrimSpace(args.Before); rawBefore != "" {
		before, err = parseFlexibleTime(rawBefore)
		if err != nil {
			return nil, err
		}
		messages, err = p.messages.ListBeforeBySession(ctx, sessionID, before, limit)
	} else {
		messages, err = p.messages.ListLatestBySession(ctx, sessionID, limit)
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
	out["session_id"] = sessionID
	if messageID != "" {
		out["message_id"] = messageID
	}
	if !before.IsZero() {
		out["before"] = sess.FormatTime(before)
	}
	return out, nil
}

// execGetToolResult pages through the text a provider receives when history
// replays one stored tool result: the text output itself, or its canonical
// JSON document. A ref names the row and the part's position in the replayed
// message, and resolves only inside the current session.
func (p *HistoryProvider) execGetToolResult(ctx context.Context, sess SessionContext, args getMessagesArgs) (any, error) {
	if strings.TrimSpace(args.MessageID) != "" || strings.TrimSpace(args.Before) != "" || args.Limit != nil {
		return nil, errors.New("ref cannot be combined with message_id, before, or limit; page with offset and max_bytes")
	}
	sessionID := strings.TrimSpace(sess.SessionID)
	if sessionID == "" {
		return nil, errors.New("ref reads the current session, and there is none")
	}
	if requested := strings.TrimSpace(args.SessionID); requested != "" && requested != sessionID {
		return nil, errors.New("ref reads only the current session; omit session_id")
	}
	messageID, partIndex, err := parseToolResultRef(args.Ref)
	if err != nil {
		return nil, err
	}
	offset := 0
	if args.Offset != nil {
		offset = *args.Offset
	}
	if offset < 0 {
		return nil, errors.New("offset must be >= 0")
	}
	if args.MaxBytes != nil && *args.MaxBytes < 1 {
		return nil, errors.New("max_bytes must be >= 1")
	}

	msg, err := p.messages.GetByIDBySession(ctx, sessionID, messageID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("message %s is not in the current session; ref reads only the current session", messageID)
	}
	if err != nil {
		return nil, err
	}
	replayed := historyfrag.StoredModelMessageToSDKMessage(historyfrag.DecodeStoredModelMessage(p.logger, msg.ID, msg.Role, msg.Content))
	if partIndex >= len(replayed.Content) {
		return nil, fmt.Errorf("message %s has no part %d", messageID, partIndex)
	}
	result, ok := replayed.Content[partIndex].(sdk.ToolResultPart)
	if !ok {
		return nil, fmt.Errorf("part %d of message %s is a %s part, not a tool result", partIndex, messageID, replayed.Content[partIndex].PartType())
	}
	text := result.Result.String()
	if offset > len(text) {
		return nil, fmt.Errorf("offset %d is past the end of the result (total_bytes %d)", offset, len(text))
	}
	return p.toolResultPage(msg.ID+":"+strconv.Itoa(partIndex), result, text, offset, args.MaxBytes), nil
}

func parseToolResultRef(raw string) (string, int, error) {
	messageID, part, found := strings.Cut(strings.TrimPrefix(strings.TrimSpace(raw), "ref="), ":")
	index, indexErr := strconv.ParseUint(part, 10, 31)
	if _, idErr := dbpkg.ParseUUID(messageID); !found || indexErr != nil || idErr != nil {
		return "", 0, fmt.Errorf("ref must be <message id>:<part index> as a summary cites it, got %q", raw)
	}
	return messageID, int(index), nil
}

// toolResultPage returns the longest page from offset that the runtime's
// output limit leaves unchanged, so its content is exactly
// text[offset:next_offset]. Pages begin and end on rune boundaries, and while
// text remains a page holds at least one rune, even past max_bytes.
func (p *HistoryProvider) toolResultPage(ref string, result sdk.ToolResultPart, text string, offset int, maxBytes *int) map[string]any {
	limit := contextlimit.NormalizedLimit(p.outputLimit)
	start := offset
	for start > 0 && start < len(text) && !utf8.RuneStart(text[start]) {
		start--
	}
	budget := limit.MaxBytes
	if maxBytes != nil {
		budget = min(budget, *maxBytes)
	}
	end := len(text)
	if budget < end-start {
		end = start + budget
	}
	newlines := 0
	for i := start; i < end; i++ {
		if text[i] != '\n' {
			continue
		}
		if newlines++; newlines == limit.MaxLines {
			end = i
			break
		}
	}
	minEnd := start
	if start < len(text) {
		_, size := utf8.DecodeRuneInString(text[start:])
		minEnd = start + size
	}
	runeEnd := func(end int) int {
		end = max(end, minEnd)
		for end > minEnd && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		return end
	}
	page := func(end int) map[string]any {
		out := map[string]any{"ok": true, "ref": ref, "tool_name": result.ToolName, "offset": start, "total_bytes": len(text), "content": text[start:end]}
		if result.IsError {
			out["is_error"] = true
		}
		if end < len(text) {
			out["next_offset"] = end
		}
		return out
	}
	fits := func(end int) bool { return len(contextlimit.MarshalString(page(end))) <= limit.MaxBytes }
	end = runeEnd(end)
	if !fits(end) {
		// Every shorter page carries next_offset, so its encoded size grows
		// with its end and the longest one that fits can be bisected.
		n := sort.Search(end-minEnd, func(n int) bool { return !fits(runeEnd(minEnd + n)) })
		end = runeEnd(minEnd + n - 1)
	}
	return page(end)
}

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

// ---------------------------------------------------------------------------
// search_messages
// ---------------------------------------------------------------------------

func (p *HistoryProvider) execSearchMessages(ctx context.Context, sess SessionContext, args searchMessagesArgs) (any, error) {
	botID := strings.TrimSpace(sess.BotID)
	if botID == "" {
		return nil, errors.New("bot_id is required")
	}

	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		return nil, errors.New("invalid bot_id")
	}

	limit := int32(50)
	if args.Limit != nil && *args.Limit > 0 && *args.Limit <= 200 {
		limit = int32(*args.Limit) //nolint:gosec // bounds-checked above
	}

	_, allowed, err := visibleHistorySessions(ctx, p.sessions, sess)
	if err != nil {
		return nil, err
	}
	allowedSessionIDs := make([]pgtype.UUID, 0, len(allowed))
	for sessionID := range allowed {
		parsed, parseErr := dbpkg.ParseUUID(sessionID)
		if parseErr == nil {
			allowedSessionIDs = append(allowedSessionIDs, parsed)
		}
	}
	if len(allowedSessionIDs) == 0 {
		return map[string]any{
			"ok": true, "bot_id": botID, "count": 0, "messages": []map[string]any{},
		}, nil
	}

	params := sqlc.SearchMessagesParams{
		BotID:      pgBotID,
		SessionIds: allowedSessionIDs,
		MaxCount:   limit,
	}

	if v := strings.TrimSpace(args.SessionID); v != "" {
		if !historySessionVisible(allowed, v) {
			return nil, errors.New("session_id is not accessible from the current context")
		}
		parsed, parseErr := dbpkg.ParseUUID(v)
		if parseErr != nil {
			return nil, errors.New("invalid session_id")
		}
		params.SessionID = parsed
	}
	if v := args.ContactID; v != "" {
		params.ContactID = dbpkg.ParseUUIDOrEmpty(v)
	}
	if v := strings.TrimSpace(args.Role); v != "" {
		params.Role = pgtype.Text{String: v, Valid: true}
	}
	if v := strings.TrimSpace(args.Keyword); v != "" {
		params.Keyword = pgtype.Text{String: v, Valid: true}
	}
	if v := strings.TrimSpace(args.StartTime); v != "" {
		if t, parseErr := parseFlexibleTime(v); parseErr == nil {
			params.StartTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	} else {
		defaultLookback := time.Now().UTC().AddDate(0, 0, -defaultMaxLookbackDays)
		params.StartTime = pgtype.Timestamptz{Time: defaultLookback, Valid: true}
	}
	if v := strings.TrimSpace(args.EndTime); v != "" {
		if t, parseErr := parseFlexibleTime(v); parseErr == nil {
			params.EndTime = pgtype.Timestamptz{Time: t, Valid: true}
		}
	}

	rows, err := p.queries.SearchMessages(ctx, params)
	if err != nil {
		return nil, err
	}

	messages := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		text := extractTextContent(row.Content)

		entry := map[string]any{
			"id":         row.ID.String(),
			"session_id": row.SessionID.String(),
			"role":       row.Role,
			"text":       text,
			"created_at": sess.FormatTime(row.CreatedAt.Time),
		}
		if dbpkg.TextToString(row.Platform) != "" {
			entry["platform"] = dbpkg.TextToString(row.Platform)
		}
		if dbpkg.TextToString(row.SenderDisplayName) != "" {
			entry["sender"] = dbpkg.TextToString(row.SenderDisplayName)
		}
		if row.SenderChannelIdentityID.Valid {
			entry["contact_id"] = row.SenderChannelIdentityID.String()
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

type listSessionsArgs struct {
	Limit    *int   `json:"limit,omitempty" jsonschema:"Maximum number of sessions to return. Default 50."`
	Platform string `json:"platform,omitempty" jsonschema:"Filter by channel platform (e.g. telegram, feishu). Returns all platforms when omitted."`
	Type     string `json:"type,omitempty" jsonschema:"Filter by session type: chat or schedule. Returns all types when omitted."`
}

type getMessagesArgs struct {
	Before    string `json:"before,omitempty" jsonschema:"ISO 8601 timestamp cursor. When provided, returns messages created before this time."`
	Limit     *int   `json:"limit,omitempty" jsonschema:"Maximum number of messages to return. Default 30, max 100."`
	MaxBytes  *int   `json:"max_bytes,omitempty" jsonschema:"With ref: the most bytes of the result to return in this page. Default: as many as one tool result holds."`
	MessageID string `json:"message_id,omitempty" jsonschema:"Exact persisted message ID to resolve, such as a message_id returned in search_memory source_refs."`
	Offset    *int   `json:"offset,omitempty" jsonschema:"With ref: byte offset in the result to start from; pass the previous page's next_offset. Default 0."`
	Ref       string `json:"ref,omitempty" jsonschema:"Tool result reference <message id>:<part index>, as cited by a summary (ref=...). Reads that earlier tool result's original text from the current session, one page at a time."`
	SessionID string `json:"session_id,omitempty" jsonschema:"Session ID to read. Defaults to the current session when omitted."`
}

type searchMessagesArgs struct {
	ContactID string `json:"contact_id,omitempty" jsonschema:"Filter by sender channel identity ID."`
	EndTime   string `json:"end_time,omitempty" jsonschema:"ISO 8601 timestamp. Only return messages created at or before this time."`
	Keyword   string `json:"keyword,omitempty" jsonschema:"Search keyword — matches against the text content of messages (case-insensitive)."`
	Limit     *int   `json:"limit,omitempty" jsonschema:"Maximum number of messages to return. Default 50, max 200."`
	Role      string `json:"role,omitempty" jsonschema:"Filter by message role."`
	SessionID string `json:"session_id,omitempty" jsonschema:"Filter by session ID."`
	StartTime string `json:"start_time,omitempty" jsonschema:"ISO 8601 timestamp. Only return messages created at or after this time."`
}
