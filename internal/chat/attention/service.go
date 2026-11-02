// Package attention records when a user's own session needs them — a run
// finished, failed or is waiting for a decision — and whether they have seen
// it since. It owns the eligibility rule (in its SQL), each user's read state
// per session (read and unread are two forward-only marks; the later wins),
// and the per-user events that drive unread dots and notifications.
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
	// KindMarked is a session the user marked unread that never needed
	// attention on its own.
	KindMarked Kind = "marked"
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
	BotID     string `json:"bot_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Kind      Kind   `json:"attention_kind"`
	// At is when the session last needed attention, or when it was marked
	// unread if it never did; it orders the inbox.
	At time.Time `json:"attention_at"`
	// ReadThrough is the through a client sends to read it: it covers both the
	// attention and a manual unread mark.
	ReadThrough time.Time `json:"read_through"`
	// Marked is true when the user marked the session unread themselves; such
	// a session is not read just because it is already on screen.
	Marked bool `json:"marked"`
} // @name attention.Attention

// ReadStateChange is one session's desired read state. Through is required
// to read (the attention time the client displayed, so attention that lands
// meanwhile stays unread); marking unread ignores it.
type ReadStateChange struct {
	SessionID string    `json:"session_id"`
	Unread    bool      `json:"unread"`
	Through   time.Time `json:"through,omitzero"`
} // @name attention.ReadStateChange

// Read is the session_read event payload.
type Read struct {
	SessionID string    `json:"session_id"`
	ReadAt    time.Time `json:"read_at"`
}

// Queries is the slice of the generated store this package uses.
type Queries interface {
	MarkSessionAttention(ctx context.Context, arg dbsqlc.MarkSessionAttentionParams) (dbsqlc.MarkSessionAttentionRow, error)
	MarkSessionRead(ctx context.Context, arg dbsqlc.MarkSessionReadParams) (pgtype.Timestamptz, error)
	MarkSessionsUnread(ctx context.Context, arg dbsqlc.MarkSessionsUnreadParams) ([]dbsqlc.MarkSessionsUnreadRow, error)
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
		BotID:       row.BotID.String(),
		SessionID:   row.ID.String(),
		Title:       row.Title,
		Kind:        Kind(row.AttentionKind.String),
		At:          row.AttentionAt.Time,
		ReadThrough: row.AttentionAt.Time,
	})
	return true, nil
}

// ErrThroughRequired rejects a read without the attention time it covers.
var ErrThroughRequired = errors.New("reading a session requires through")

// SetReadState writes each change as the user's read or unread mark and
// returns the sessions that are unread afterwards because this call marked
// them. Sessions the user did not create, or that cannot carry unread state,
// are skipped; nothing is rejected for them, so a stale client cannot wedge a
// batch. Reads publish session_read and unread marks publish session_unread,
// never session_attention, so marking something unread never notifies.
func (s *Service) SetReadState(ctx context.Context, userID string, changes []ReadStateChange) ([]Attention, error) {
	uid, err := db.ParseUUID(userID)
	if err != nil {
		return nil, fmt.Errorf("parse user id: %w", err)
	}
	unread := make([]pgtype.UUID, 0, len(changes))
	for _, change := range changes {
		sid, err := db.ParseUUID(change.SessionID)
		if err != nil {
			return nil, fmt.Errorf("parse session id: %w", err)
		}
		if change.Unread {
			unread = append(unread, sid)
			continue
		}
		if change.Through.IsZero() {
			return nil, ErrThroughRequired
		}
		readAt, err := s.queries.MarkSessionRead(ctx, dbsqlc.MarkSessionReadParams{
			UserID:    uid,
			SessionID: sid,
			Through:   pgtype.Timestamptz{Time: change.Through, Valid: true},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("mark session read: %w", err)
		}
		s.publish(userID, event.EventTypeSessionRead, Read{SessionID: sid.String(), ReadAt: readAt.Time})
	}
	if len(unread) == 0 {
		return []Attention{}, nil
	}
	rows, err := s.queries.MarkSessionsUnread(ctx, dbsqlc.MarkSessionsUnreadParams{
		UserID:     uid,
		SessionIds: unread,
		At:         pgtype.Timestamptz{Time: s.now(), Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("mark sessions unread: %w", err)
	}
	out := make([]Attention, 0, len(rows))
	for _, r := range rows {
		item := Attention{
			BotID:       r.BotID.String(),
			SessionID:   r.SessionID.String(),
			Title:       r.Title,
			Kind:        Kind(r.AttentionKind),
			At:          r.AttentionAt.Time,
			ReadThrough: r.ReadThrough.Time,
			Marked:      r.Marked,
		}
		out = append(out, item)
		s.publish(userID, event.EventTypeSessionUnread, item)
	}
	return out, nil
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
			BotID:       r.BotID.String(),
			SessionID:   r.SessionID.String(),
			Title:       r.Title,
			Kind:        Kind(r.AttentionKind),
			At:          r.AttentionAt.Time,
			ReadThrough: r.ReadThrough.Time,
			Marked:      r.Marked,
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
			threads[i].AttentionKind = r.AttentionKind
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
