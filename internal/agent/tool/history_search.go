package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type historySearchCursor struct {
	Version   int        `json:"v"`
	CreatedAt time.Time  `json:"created_at"`
	ID        string     `json:"id"`
	StartTime *time.Time `json:"start_time"`
	EndTime   *time.Time `json:"end_time"`
	TimeMode  string     `json:"time_mode"`
	Filter    string     `json:"filter"`
}

func (p *HistoryProvider) execSearchMessages(ctx context.Context, sess SessionContext, args map[string]any) (any, error) {
	params := sqlc.SearchMessagesParams{MaxCount: 51}
	botID := strings.TrimSpace(sess.BotID)
	var err error
	if params.BotID, err = dbpkg.ParseUUID(botID); err != nil {
		return nil, errors.New("invalid bot_id")
	}
	values := make(map[string]string)
	for _, key := range []string{"session_id", "contact_id", "role", "keyword", "start_time", "end_time", "cursor"} {
		value, present := args[key]
		if !present {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a string", key)
		}
		text = strings.TrimSpace(text)
		if text == "" && key != "keyword" {
			return nil, fmt.Errorf("%s must not be empty", key)
		}
		values[key] = text
	}
	limit := 50
	if raw, present := args["limit"]; present {
		if raw == nil {
			return nil, errors.New("limit must be an integer from 1 to 200")
		}
		n, _, parseErr := IntArg(args, "limit")
		fractional := false
		if value, ok := raw.(float64); ok {
			fractional = value != float64(n)
		}
		if parseErr != nil || fractional || n < 1 || n > 200 {
			return nil, errors.New("limit must be an integer from 1 to 200")
		}
		limit = n
	}
	params.MaxCount = int32(limit + 1)
	for key, target := range map[string]*pgtype.UUID{"session_id": &params.SessionID, "contact_id": &params.ContactID} {
		if value := values[key]; value != "" {
			if *target, err = dbpkg.ParseUUID(value); err != nil {
				return nil, fmt.Errorf("invalid %s", key)
			}
			values[key] = target.String()
		}
	}
	if role := values["role"]; role != "" {
		if role != "user" && role != "assistant" && role != "tool" {
			return nil, errors.New("role must be user, assistant, or tool")
		}
		params.Role = pgtype.Text{String: role, Valid: true}
	}
	if keyword := values["keyword"]; keyword != "" {
		params.Keyword = pgtype.Text{String: keyword, Valid: true}
	}
	var cursor historySearchCursor
	if raw := values["cursor"]; raw != "" {
		if len(raw) > 4096 {
			return nil, errors.New("invalid cursor")
		}
		data, decodeErr := base64.RawURLEncoding.DecodeString(raw)
		if decodeErr != nil || json.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.CreatedAt.IsZero() || cursor.Filter == "" {
			return nil, errors.New("invalid cursor")
		}
		if cursor.TimeMode != "session" && cursor.TimeMode != "recent" && cursor.TimeMode != "bounded" {
			return nil, errors.New("invalid cursor")
		}
		if params.CursorID, err = dbpkg.ParseUUID(cursor.ID); err != nil {
			return nil, errors.New("invalid cursor")
		}
		params.CursorCreatedAt = pgtype.Timestamptz{Time: cursor.CreatedAt, Valid: true}
	}
	timeMode := "session"
	if values["start_time"] != "" || values["end_time"] != "" {
		timeMode = "bounded"
	}
	for key, target := range map[string]*pgtype.Timestamptz{"start_time": &params.StartTime, "end_time": &params.EndTime} {
		if value := values[key]; value != "" {
			parsed, parseErr := parseFlexibleTime(value)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid %s", key)
			}
			*target = pgtype.Timestamptz{Time: parsed.UTC(), Valid: true}
		}
	}
	if values["cursor"] != "" {
		if values["start_time"] == "" && cursor.StartTime != nil {
			params.StartTime = pgtype.Timestamptz{Time: *cursor.StartTime, Valid: true}
		}
		if values["end_time"] == "" && cursor.EndTime != nil {
			params.EndTime = pgtype.Timestamptz{Time: *cursor.EndTime, Valid: true}
		}
		timeMode = cursor.TimeMode
	} else if !params.StartTime.Valid && !params.SessionID.Valid {
		params.StartTime = pgtype.Timestamptz{Time: time.Now().UTC().AddDate(0, 0, -defaultMaxLookbackDays), Valid: true}
		timeMode = "recent"
	}
	if params.StartTime.Valid && params.EndTime.Valid && params.StartTime.Time.After(params.EndTime.Time) {
		return nil, errors.New("start_time must not be after end_time")
	}
	_, allowed, err := visibleHistorySessions(ctx, p.sessions, sess)
	if err != nil {
		return nil, err
	}
	if params.SessionID.Valid && !historySessionVisible(allowed, params.SessionID.String()) {
		return nil, errors.New("session_id is not accessible from the current context")
	}
	if params.SessionID.Valid {
		allowed = map[string]struct{}{params.SessionID.String(): {}}
	}
	ids := make([]string, 0, len(allowed))
	for id := range allowed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		parsed, parseErr := dbpkg.ParseUUID(id)
		if parseErr == nil {
			params.SessionIds = append(params.SessionIds, parsed)
		}
	}
	filterBytes, _ := json.Marshal([]any{botID, sess.SessionID, ids, values["session_id"], values["contact_id"], values["role"], values["keyword"], searchTime(params.StartTime), searchTime(params.EndTime)})
	hash := sha256.Sum256(filterBytes)
	filter := hex.EncodeToString(hash[:])
	if values["cursor"] != "" && cursor.Filter != filter {
		return nil, errors.New("cursor does not match the current search filters or access scope")
	}
	out := map[string]any{
		"ok": true, "bot_id": botID, "count": 0, "messages": []map[string]any{}, "has_more": false,
		"time_scope": map[string]any{"mode": timeMode, "start_time": searchTime(params.StartTime), "end_time": searchTime(params.EndTime)},
		"read_hint":  "Read a hit with get_messages using its session_id and message_id=id, view=execution. Search previews cover persisted text and tool evidence; pre-store truncation cannot be recovered.",
	}
	if len(params.SessionIds) == 0 {
		return out, nil
	}
	rows, err := p.queries.SearchMessages(ctx, params)
	if err != nil {
		return nil, err
	}
	messages := make([]map[string]any, 0, min(limit, len(rows)))
	for i, row := range rows {
		if i >= limit {
			break
		}
		preview := historySearchPreview(row.SearchText, 512)
		entry := map[string]any{"id": row.ID.String(), "session_id": row.SessionID.String(), "role": row.Role, "text": preview, "created_at": sess.FormatTime(row.CreatedAt.Time)}
		if row.TurnID.Valid {
			entry["turn_id"] = row.TurnID.String()
		}
		if row.Platform.Valid {
			entry["platform"] = historySearchPreview(row.Platform.String, 128)
		}
		if row.SenderDisplayName.Valid {
			entry["sender"] = historySearchPreview(row.SenderDisplayName.String, 128)
		}
		if row.SenderChannelIdentityID.Valid {
			entry["contact_id"] = row.SenderChannelIdentityID.String()
		}
		messages = append(messages, entry)
		previousCount, previousMessages, previousMore, previousCursor := out["count"], out["messages"], out["has_more"], out["next_cursor"]
		out["count"], out["messages"], out["has_more"] = len(messages), messages, i+1 < len(rows)
		delete(out, "next_cursor")
		if i+1 < len(rows) {
			cursor = historySearchCursor{Version: 1, CreatedAt: row.CreatedAt.Time.UTC(), ID: row.ID.String(), StartTime: searchTime(params.StartTime), EndTime: searchTime(params.EndTime), TimeMode: timeMode, Filter: filter}
			encoded, _ := json.Marshal(cursor)
			out["next_cursor"] = base64.RawURLEncoding.EncodeToString(encoded)
		}
		encoded, _ := json.Marshal(out)
		if len(encoded) > 32*1024 {
			out["count"], out["messages"], out["has_more"] = previousCount, previousMessages, previousMore
			out["next_cursor"] = previousCursor
			break
		}
	}
	return out, nil
}

func searchTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	utc := value.Time.UTC()
	return &utc
}

func historySearchPreview(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit - len("…")
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}
