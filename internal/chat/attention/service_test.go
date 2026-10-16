package attention

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/chat/event"
	"github.com/felinics/memoh/internal/chat/thread"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type fakeQueries struct {
	markRow   dbsqlc.MarkSessionAttentionRow
	markErr   error
	markCalls []dbsqlc.MarkSessionAttentionParams
	readAt    time.Time
	readCalls []dbsqlc.MarkSessionReadParams
	unread    []dbsqlc.ListUnreadSessionsForUserRow
	attention []dbsqlc.ListSessionAttentionForUserRow
}

func (f *fakeQueries) MarkSessionAttention(_ context.Context, arg dbsqlc.MarkSessionAttentionParams) (dbsqlc.MarkSessionAttentionRow, error) {
	f.markCalls = append(f.markCalls, arg)
	return f.markRow, f.markErr
}

func (f *fakeQueries) MarkSessionRead(_ context.Context, arg dbsqlc.MarkSessionReadParams) (pgtype.Timestamptz, error) {
	f.readCalls = append(f.readCalls, arg)
	return pgtype.Timestamptz{Time: f.readAt, Valid: true}, nil
}

func (f *fakeQueries) ListUnreadSessionsForUser(context.Context, dbsqlc.ListUnreadSessionsForUserParams) ([]dbsqlc.ListUnreadSessionsForUserRow, error) {
	return f.unread, nil
}

func (f *fakeQueries) ListSessionAttentionForUser(context.Context, dbsqlc.ListSessionAttentionForUserParams) ([]dbsqlc.ListSessionAttentionForUserRow, error) {
	return f.attention, nil
}

type recordingPublisher struct{ events []event.Event }

func (p *recordingPublisher) Publish(e event.Event) { p.events = append(p.events, e) }

var (
	testNow     = time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	testUser    = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	testBot     = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	testSession = uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
)

func newTestService(q *fakeQueries, p *recordingPublisher) *Service {
	return NewService(q, p, func() time.Time { return testNow })
}

func TestKindForTerminalState(t *testing.T) {
	cases := map[string]struct {
		kind Kind
		ok   bool
	}{
		"completed": {KindReply, true},
		"failed":    {KindFailed, true},
		"lost":      {KindFailed, true},
		"aborted":   {"", false},
		"running":   {"", false},
	}
	for state, want := range cases {
		kind, ok := KindForTerminalState(state)
		if kind != want.kind || ok != want.ok {
			t.Errorf("KindForTerminalState(%q) = (%q, %v), want (%q, %v)", state, kind, ok, want.kind, want.ok)
		}
	}
}

func TestMarkPublishesToCreatorTopic(t *testing.T) {
	q := &fakeQueries{markRow: dbsqlc.MarkSessionAttentionRow{
		ID:              pgtype.UUID{Bytes: testSession, Valid: true},
		BotID:           pgtype.UUID{Bytes: testBot, Valid: true},
		CreatedByUserID: pgtype.UUID{Bytes: testUser, Valid: true},
		Title:           "Refactor login",
		AttentionKind:   pgtype.Text{String: "reply", Valid: true},
		AttentionAt:     pgtype.Timestamptz{Time: testNow, Valid: true},
	}}
	p := &recordingPublisher{}

	changed, err := newTestService(q, p).Mark(context.Background(), testSession.String(), KindReply)
	if err != nil || !changed {
		t.Fatalf("Mark = (%v, %v), want (true, nil)", changed, err)
	}
	if got := q.markCalls[0]; got.Kind != "reply" || !got.At.Time.Equal(testNow) {
		t.Fatalf("mark params = %+v, want kind reply at %v", got, testNow)
	}
	if len(p.events) != 1 {
		t.Fatalf("published %d events, want 1", len(p.events))
	}
	e := p.events[0]
	if e.BotID != event.UserTopic(testUser.String()) || e.Type != event.EventTypeSessionAttention {
		t.Fatalf("event = %s on %q, want %s on the creator topic", e.Type, e.BotID, event.EventTypeSessionAttention)
	}
	var payload map[string]any
	if err := json.Unmarshal(e.Data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	want := map[string]any{
		"bot_id": testBot.String(), "session_id": testSession.String(), "title": "Refactor login",
		"attention_kind": "reply", "attention_at": testNow.Format(time.RFC3339Nano),
	}
	for k, v := range want {
		if payload[k] != v {
			t.Errorf("payload[%q] = %v, want %v", k, payload[k], v)
		}
	}
	if len(payload) != len(want) {
		t.Errorf("payload has extra keys: %v", payload)
	}
}

func TestMarkDoesNotPublishWhenUnchanged(t *testing.T) {
	p := &recordingPublisher{}
	changed, err := newTestService(&fakeQueries{markErr: pgx.ErrNoRows}, p).Mark(context.Background(), testSession.String(), KindReply)
	if err != nil || changed {
		t.Fatalf("Mark = (%v, %v), want (false, nil)", changed, err)
	}
	if len(p.events) != 0 {
		t.Fatalf("published %d events for an unchanged session", len(p.events))
	}
}

func TestMarkReturnsDatabaseErrors(t *testing.T) {
	boom := errors.New("boom")
	if _, err := newTestService(&fakeQueries{markErr: boom}, &recordingPublisher{}).Mark(context.Background(), testSession.String(), KindReply); !errors.Is(err, boom) {
		t.Fatalf("Mark err = %v, want %v", err, boom)
	}
}

func TestMarkReadPublishesSessionRead(t *testing.T) {
	readAt := testNow.Add(-time.Minute)
	q := &fakeQueries{readAt: readAt}
	p := &recordingPublisher{}

	got, err := newTestService(q, p).MarkRead(context.Background(), testUser.String(), testSession.String(), readAt)
	if err != nil || !got.Equal(readAt) {
		t.Fatalf("MarkRead = (%v, %v), want (%v, nil)", got, err, readAt)
	}
	if !q.readCalls[0].Through.Time.Equal(readAt) {
		t.Fatalf("through = %v, want %v", q.readCalls[0].Through.Time, readAt)
	}
	if len(p.events) != 1 || p.events[0].Type != event.EventTypeSessionRead || p.events[0].BotID != event.UserTopic(testUser.String()) {
		t.Fatalf("events = %+v, want one session_read on the user topic", p.events)
	}
}

func TestAnnotateSetsUnreadOnlyForReturnedRows(t *testing.T) {
	other := uuid.New()
	q := &fakeQueries{attention: []dbsqlc.ListSessionAttentionForUserRow{{
		SessionID:     pgtype.UUID{Bytes: testSession, Valid: true},
		AttentionKind: pgtype.Text{String: "needs_input", Valid: true},
		Unread:        true,
	}}}
	threads := []thread.Thread{{ID: testSession.String()}, {ID: other.String()}}

	got, err := newTestService(q, &recordingPublisher{}).Annotate(context.Background(), testUser.String(), threads)
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	if !got[0].Unread || got[0].AttentionKind != "needs_input" {
		t.Fatalf("annotated = %+v, want unread needs_input", got[0])
	}
	if got[1].Unread || got[1].AttentionKind != "" {
		t.Fatalf("unmarked thread = %+v, want untouched", got[1])
	}
}

func TestListUnreadMapsRows(t *testing.T) {
	q := &fakeQueries{unread: []dbsqlc.ListUnreadSessionsForUserRow{{
		SessionID:     pgtype.UUID{Bytes: testSession, Valid: true},
		BotID:         pgtype.UUID{Bytes: testBot, Valid: true},
		Title:         "Refactor login",
		AttentionKind: pgtype.Text{String: "failed", Valid: true},
		AttentionAt:   pgtype.Timestamptz{Time: testNow, Valid: true},
	}}}
	got, err := newTestService(q, &recordingPublisher{}).ListUnread(context.Background(), testUser.String())
	if err != nil {
		t.Fatalf("ListUnread: %v", err)
	}
	want := Attention{BotID: testBot.String(), SessionID: testSession.String(), Title: "Refactor login", Kind: KindFailed, At: testNow}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("ListUnread = %+v, want [%+v]", got, want)
	}
}
