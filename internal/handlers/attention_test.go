package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/chat/attention"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	attentionOwnerID    = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	attentionOutsiderID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

type attentionTestQueries struct {
	*sessionDeleteQueries
	listed []sqlc.ListSessionsByBotPagedRow
}

func (*attentionTestQueries) ListBotUserGrantsForUser(context.Context, sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return nil, nil
}

func (q *attentionTestQueries) ListSessionsByBotPaged(context.Context, sqlc.ListSessionsByBotPagedParams) ([]sqlc.ListSessionsByBotPagedRow, error) {
	return q.listed, nil
}

type fakeAttentionService struct {
	readCalls []string
	readAt    time.Time
	unread    []attention.Attention
	annotate  map[string]bool
}

func (f *fakeAttentionService) MarkRead(_ context.Context, userID, sessionID string, through time.Time) (time.Time, error) {
	f.readCalls = append(f.readCalls, userID+"/"+sessionID+"@"+through.UTC().Format(time.RFC3339))
	return f.readAt, nil
}

func (f *fakeAttentionService) ListUnread(context.Context, string) ([]attention.Attention, error) {
	return f.unread, nil
}

func (f *fakeAttentionService) Annotate(_ context.Context, _ string, threads []session.Thread) ([]session.Thread, error) {
	for i := range threads {
		if f.annotate[threads[i].ID] {
			threads[i].Unread = true
			threads[i].AttentionKind = "reply"
		}
	}
	return threads, nil
}

func attentionTestQueriesFor(creator string) *attentionTestQueries {
	return &attentionTestQueries{sessionDeleteQueries: &sessionDeleteQueries{
		bot: testBotRow(activityTestBotID, nil),
		session: sqlc.BotSession{
			ID: testUUID(activityTestSessionID), BotID: testUUID(activityTestBotID), Type: session.TypeChat,
			SessionMode: session.TypeChat, Visibility: string(session.VisibilityUser),
			CreatedByUserID: testUUID(creator),
		},
	}}
}

func attentionTestHandler(creator string) (*MessageHandler, *messageevent.Hub, *fakeAttentionService) {
	hub := messageevent.NewHub()
	queries := attentionTestQueriesFor(creator)
	h := NewMessageHandler(slog.Default(), nil, session.NewService(nil, queries, hub),
		bots.NewService(nil, queries), newTestAdminAccountService("member"), hub)
	svc := &fakeAttentionService{readAt: time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)}
	h.SetAttentionService(svc)
	return h, hub, svc
}

func markReadRequest(userID, body string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPut, "/bots/"+activityTestBotID+"/sessions/"+activityTestSessionID+"/read", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := testAuthContext(echo.New(), req, rec, userID)
	c.SetParamNames("bot_id", "session_id")
	c.SetParamValues(activityTestBotID, activityTestSessionID)
	return c, rec
}

func TestMarkReadStoresThroughForCreator(t *testing.T) {
	h, _, svc := attentionTestHandler(attentionOwnerID)
	c, rec := markReadRequest(attentionOwnerID, `{"through":"2026-10-16T08:59:00Z"}`)

	if err := h.MarkSessionRead(c); err != nil {
		t.Fatalf("MarkSessionRead: %v", err)
	}
	if want := attentionOwnerID + "/" + activityTestSessionID + "@2026-10-16T08:59:00Z"; len(svc.readCalls) != 1 || svc.readCalls[0] != want {
		t.Fatalf("read calls = %v, want [%s]", svc.readCalls, want)
	}
	if !strings.Contains(rec.Body.String(), `"read_at":"2026-10-16T09:00:00Z"`) {
		t.Fatalf("response = %s, want read_at", rec.Body.String())
	}
}

func TestMarkReadRejectsNonCreator(t *testing.T) {
	h, _, svc := attentionTestHandler("cccccccc-cccc-cccc-cccc-cccccccccccc")
	c, _ := markReadRequest(attentionOwnerID, `{"through":"2026-10-16T08:59:00Z"}`)

	err := h.MarkSessionRead(c)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("MarkSessionRead err = %v, want 403", err)
	}
	if len(svc.readCalls) != 0 {
		t.Fatalf("read stored for a non-creator: %v", svc.readCalls)
	}
}

func TestMarkReadRequiresThrough(t *testing.T) {
	h, _, _ := attentionTestHandler(attentionOwnerID)
	c, _ := markReadRequest(attentionOwnerID, `{}`)
	if err := h.MarkSessionRead(c); err == nil {
		t.Fatalf("MarkSessionRead without through succeeded")
	}
}

func TestUnreadListDropsBotsWithoutAccess(t *testing.T) {
	h, _, svc := attentionTestHandler(attentionOutsiderID)
	svc.unread = []attention.Attention{{BotID: activityTestBotID, SessionID: activityTestSessionID, Title: "t", Kind: attention.KindReply}}
	req := httptest.NewRequest(http.MethodGet, "/users/me/unread", nil)
	rec := httptest.NewRecorder()

	if err := h.ListUnreadSessions(testAuthContext(echo.New(), req, rec, attentionOutsiderID)); err != nil {
		t.Fatalf("ListUnreadSessions: %v", err)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"items":[]}` {
		t.Fatalf("unread for outsider = %s, want empty items", got)
	}

	h, _, svc = attentionTestHandler(attentionOwnerID)
	svc.unread = []attention.Attention{{BotID: activityTestBotID, SessionID: activityTestSessionID, Title: "t", Kind: attention.KindReply}}
	rec = httptest.NewRecorder()
	if err := h.ListUnreadSessions(testAuthContext(echo.New(), req, rec, attentionOwnerID)); err != nil {
		t.Fatalf("ListUnreadSessions: %v", err)
	}
	if !strings.Contains(rec.Body.String(), activityTestSessionID) {
		t.Fatalf("unread for owner = %s, want the session", rec.Body.String())
	}
}

// streamAttention runs the user stream until the recorder has seen `frames`
// flushes, publishing `publish` once the subscription is live.
func streamAttention(t *testing.T, h *MessageHandler, hub *messageevent.Hub, userID string, frames int, publish func()) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rec := &countingFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, stopAfter: frames}
	h.messageEvents = publishOnSubscribe{hub: hub, publish: publish}
	req := httptest.NewRequest(http.MethodGet, "/users/me/attention/events", nil).WithContext(ctx)
	if err := h.StreamAttentionEvents(testAuthContext(echo.New(), req, rec, userID)); err != nil {
		t.Fatalf("StreamAttentionEvents: %v", err)
	}
	var out []map[string]any
	for _, frame := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &payload); err != nil {
			t.Fatalf("decode frame %q: %v", frame, err)
		}
		out = append(out, payload)
	}
	return out
}

type countingFlushRecorder struct {
	*httptest.ResponseRecorder
	flushes   int
	stopAfter int
	cancel    context.CancelFunc
}

func (r *countingFlushRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.flushes++
	if r.flushes >= r.stopAfter {
		r.cancel()
	}
}

type publishOnSubscribe struct {
	hub     *messageevent.Hub
	publish func()
}

func (s publishOnSubscribe) Subscribe(topic string, buffer int) (*messageevent.Subscription, func()) {
	sub, cancel := s.hub.Subscribe(topic, buffer)
	if s.publish != nil {
		s.publish()
	}
	return sub, cancel
}

func publishAttention(hub *messageevent.Hub, userID string) {
	data, _ := json.Marshal(attention.Attention{BotID: activityTestBotID, SessionID: activityTestSessionID, Title: "t", Kind: attention.KindReply, At: time.Now()})
	hub.Publish(messageevent.Event{Type: messageevent.EventTypeSessionAttention, BotID: messageevent.UserTopic(userID), Data: data})
}

func TestAttentionStreamDeliversOnlyOwnTopic(t *testing.T) {
	h, hub, _ := attentionTestHandler(attentionOwnerID)
	frames := streamAttention(t, h, hub, attentionOwnerID, 2, func() {
		publishAttention(hub, attentionOutsiderID)
		publishAttention(hub, attentionOwnerID)
	})
	if len(frames) != 2 || frames[0]["type"] != "attention_ready" {
		t.Fatalf("frames = %+v, want ready then one attention", frames)
	}
	if frames[1]["type"] != "session_attention" || frames[1]["session_id"] != activityTestSessionID {
		t.Fatalf("attention frame = %+v", frames[1])
	}
}

func TestAttentionStreamSkipsBotsWithoutAccess(t *testing.T) {
	h, hub, _ := attentionTestHandler(attentionOutsiderID)
	frames := streamAttention(t, h, hub, attentionOutsiderID, 2, func() {
		publishAttention(hub, attentionOutsiderID)
		data, _ := json.Marshal(attention.Read{SessionID: activityTestSessionID, ReadAt: time.Now()})
		hub.Publish(messageevent.Event{Type: messageevent.EventTypeSessionRead, BotID: messageevent.UserTopic(attentionOutsiderID), Data: data})
	})
	if len(frames) != 2 || frames[1]["type"] != "session_read" {
		t.Fatalf("frames = %+v, want ready then session_read only (attention for an inaccessible bot dropped)", frames)
	}
}

func TestListSessionsIncludesUnread(t *testing.T) {
	queries := attentionTestQueriesFor(attentionOwnerID)
	queries.listed = []sqlc.ListSessionsByBotPagedRow{{
		ID: testUUID(activityTestSessionID), BotID: testUUID(activityTestBotID), Type: session.TypeChat,
		SessionMode: session.TypeChat, Visibility: string(session.VisibilityUser), Title: "t",
		CreatedByUserID: testUUID(attentionOwnerID), UpdatedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}}
	h := NewSessionHandler(slog.Default(), session.NewService(nil, queries, nil), nil, bots.NewService(nil, queries), newTestAdminAccountService("member"))
	h.SetAttentionService(&fakeAttentionService{annotate: map[string]bool{activityTestSessionID: true}})
	req := httptest.NewRequest(http.MethodGet, "/bots/"+activityTestBotID+"/sessions", nil)
	rec := httptest.NewRecorder()
	c := testAuthContext(echo.New(), req, rec, attentionOwnerID)
	c.SetParamNames("bot_id")
	c.SetParamValues(activityTestBotID)

	if err := h.ListSessions(c); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `"unread":true`) || !strings.Contains(rec.Body.String(), `"attention_kind":"reply"`) {
		t.Fatalf("list response = %s, want unread attention", rec.Body.String())
	}
}
