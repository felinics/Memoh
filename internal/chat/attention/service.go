// Package attention records when a user's own session needs them — a run
// finished, failed or is waiting for a decision — and whether they have seen
// it since. It owns the eligibility rule (in its SQL), the per-user read
// cursor, and the per-user events that drive unread dots and notifications.
package attention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Kind is why a session needs attention.
type Kind string

const (
	KindReply      Kind = "reply"
	KindFailed     Kind = "failed"
	KindNeedsInput Kind = "needs_input"
)

// unreadLimit caps the cross-bot unread summary; the inbox shows far fewer.
const unreadLimit = 200

// KindForTerminalState maps a run's durable terminal state to the attention
// it raises. A run the user stopped raises none.
func KindForTerminalState(state string) (Kind, bool) {
	switch state {
	case "completed":
		return KindReply, true
	case "failed", "lost":
		return KindFailed, true
	}
	return "", false
}

// Attention is one session that needs a user, as the unread summary and the
// session_attention event carry it. It never includes message content.
type Attention struct {
	BotID     string    `json:"bot_id"`
	SessionID string    `json:"session_id"`
	Title     string    `json:"title"`
	Kind      Kind      `json:"attention_kind"`
	At        time.Time `json:"attention_at"`
} // @name attention.Attention

// Read is the session_read event payload.
type Read struct {
	SessionID string    `json:"session_id"`
	ReadAt    time.Time `json:"read_at"`
}

// Queries is the slice of the generated store this package uses.
type Queries interface {
	MarkSessionAttention(ctx context.Context, arg dbsqlc.MarkSessionAttentionParams) (dbsqlc.MarkSessionAttentionRow, error)
	MarkSessionRead(ctx context.Context, arg dbsqlc.MarkSessionReadParams) (pgtype.Timestamptz, error)
	ListUnreadSessionsForUser(ctx context.Context, arg dbsqlc.ListUnreadSessionsForUserParams) ([]dbsqlc.ListUnreadSessionsForUserRow, error)
	ListSessionAttentionForUser(ctx context.Context, arg dbsqlc.ListSessionAttentionForUserParams) ([]dbsqlc.ListSessionAttentionForUserRow, error)
}

type Service struct {
	queries   Queries
	publisher event.Publisher
	now       func() time.Time
}

func NewService(queries Queries, publisher event.Publisher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{queries: queries, publisher: publisher, now: now}
}

// Mark records that a session needs its creator's attention now and tells
// that user. changed is false, with no event, when the session is not one
// that raises attention (schedule, discuss, IM, internal, deleted, no
// creator) or was already marked at least as late — which makes replays of
// the same run harmless.
func (s *Service) Mark(ctx context.Context, sessionID string, kind Kind) (bool, error) {
	id, err := db.ParseUUID(sessionID)
	if err != nil {
		return false, fmt.Errorf("parse session id: %w", err)
	}
	row, err := s.queries.MarkSessionAttention(ctx, dbsqlc.MarkSessionAttentionParams{
		ID:   id,
		Kind: string(kind),
		At:   pgtype.Timestamptz{Time: s.now(), Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("mark session attention: %w", err)
	}
	s.publish(row.CreatedByUserID.String(), event.EventTypeSessionAttention, Attention{
		BotID:     row.BotID.String(),
		SessionID: row.ID.String(),
		Title:     row.Title,
		Kind:      Kind(row.AttentionKind.String),
		At:        row.AttentionAt.Time,
	})
	return true, nil
}

// MarkRead moves userID's read cursor for the session to through, never
// backwards, and returns the stored cursor. Callers pass the attention time
// they actually displayed, so attention that arrives while the request is in
// flight stays unread. The caller must already have checked that userID owns
// the session.
func (s *Service) MarkRead(ctx context.Context, userID, sessionID string, through time.Time) (time.Time, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse user id: %w", err)
	}
	sid, err := db.ParseUUID(sessionID)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse session id: %w", err)
	}
	readAt, err := s.queries.MarkSessionRead(ctx, dbsqlc.MarkSessionReadParams{
		UserID:    uid,
		SessionID: sid,
		Through:   pgtype.Timestamptz{Time: through, Valid: true},
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("mark session read: %w", err)
	}
	s.publish(userID, event.EventTypeSessionRead, Read{SessionID: sid.String(), ReadAt: readAt.Time})
	return readAt.Time, nil
}

// ListUnread returns the user's unread sessions across all bots, newest first.
func (s *Service) ListUnread(ctx context.Context, userID string) ([]Attention, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	rows, err := s.queries.ListUnreadSessionsForUser(ctx, dbsqlc.ListUnreadSessionsForUserParams{UserID: uid, LimitCount: unreadLimit})
	if err != nil {
		return nil, fmt.Errorf("list unread sessions: %w", err)
	}
	out := make([]Attention, 0, len(rows))
	for _, r := range rows {
		out = append(out, Attention{
			BotID:     r.BotID.String(),
			SessionID: r.SessionID.String(),
			Title:     r.Title,
			Kind:      Kind(r.AttentionKind.String),
			At:        r.AttentionAt.Time,
		})
	}
	return out, nil
}

// Annotate fills AttentionKind and Unread on the threads userID created.
func (s *Service) Annotate(ctx context.Context, userID string, threads []thread.Thread) ([]thread.Thread, error) {
	if len(threads) == 0 {
		return threads, nil
	}
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	ids := make([]pgtype.UUID, 0, len(threads))
	for _, t := range threads {
		if id, err := db.ParseUUID(t.ID); err == nil {
			ids = append(ids, id)
		}
	}
	rows, err := s.queries.ListSessionAttentionForUser(ctx, dbsqlc.ListSessionAttentionForUserParams{UserID: uid, SessionIds: ids})
	if err != nil {
		return nil, fmt.Errorf("list session attention: %w", err)
	}
	byID := make(map[string]dbsqlc.ListSessionAttentionForUserRow, len(rows))
	for _, r := range rows {
		byID[r.SessionID.String()] = r
	}
	for i := range threads {
		if r, ok := byID[strings.TrimSpace(threads[i].ID)]; ok {
			at := r.AttentionAt.Time
			threads[i].AttentionKind = r.AttentionKind.String
			threads[i].AttentionAt = &at
			threads[i].Unread = r.Unread
		}
	}
	return threads, nil
}

func (s *Service) publish(userID string, typ event.EventType, payload any) {
	if s.publisher == nil || strings.TrimSpace(userID) == "" {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	s.publisher.Publish(event.Event{Type: typ, BotID: event.UserTopic(userID), Data: data})
}
