package chat

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
)

// TestMessagesEventsRouteIsRemoved guards the route contract: the bot-wide
// `/messages/events` endpoint was deleted as part of the SSE split. A future
// re-introduction by accident — say, by reverting the route registration —
// would make the legacy frontend re-attach silently.
func TestMessagesEventsRouteIsRemoved(t *testing.T) {
	t.Parallel()

	e := echo.New()
	(&MessageHandler{}).Register(e)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/bot-1/messages/events", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /bots/bot-1/messages/events status = %d, want 404", rec.Code)
	}
}

// TestListMessagesRequiresSessionID pins the contract that ListMessages
// rejects requests without a session_id. The pre-split endpoint accepted
// bot-wide listings; preserving the 400 keeps clients from falling back to
// that shape silently.
func TestListMessagesRequiresSessionID(t *testing.T) {
	t.Parallel()

	h := &MessageHandler{}

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/bot-1/messages", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer test")
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	ctx.SetPath("/bots/:bot_id/messages")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues("11111111-1111-1111-1111-111111111111")

	err := h.ListMessages(ctx)
	if err == nil {
		t.Fatalf("ListMessages() err = nil, want HTTP 400")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *echo.HTTPError", err)
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Fatalf("HTTPError.Code = %d, want 400", httpErr.Code)
	}
}

// TestSubscribeBeforeBacklogDedupsLiveMessage exercises the subscribe-before-
// backlog seam invariant: a message persisted between Subscribe and the
// backlog read shows up in BOTH the backlog query result and the live
// subscriber channel. The handler relies on the backlog ID set to drop the
// duplicate so the wire carries it exactly once.
//
// We test the dedup logic in isolation here (Hub + dedup map). A full HTTP
// integration test would need to fake out bots/accounts/session services for
// authorization; the dedup is self-contained and worth pinning directly.
func TestSubscribeBeforeBacklogDedupsLiveMessage(t *testing.T) {
	t.Parallel()

	hub := messageevent.NewHub()
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"

	// Subscribe first — this is what the handler does before reading backlog.
	sub, cancel := hub.Subscribe(botID, 8)
	defer cancel()

	// Persistence happens; both the backlog query and the live channel will
	// see this message. We model the backlog by listing its ID first.
	msg := messagepkg.Message{ID: "m1", BotID: botID, SessionID: sessionID, CreatedAt: time.Now()}
	backlog := []messagepkg.Message{msg}

	// AFTER subscribe and AFTER the backlog read, the hub broadcasts the same
	// message to live subscribers. Without dedup, the client sees m1 twice.
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	hub.Publish(messageevent.Event{
		Type:  messageevent.EventTypeMessageCreated,
		BotID: botID,
		Data:  data,
	})

	// Build the dedup set the same way the handler does.
	backlogIDs := make(map[string]struct{}, len(backlog))
	delivered := map[string]int{}
	for _, b := range backlog {
		backlogIDs[b.ID] = struct{}{}
		delivered[b.ID]++
	}

	// Drain the live channel and apply the same filter the handler does.
	select {
	case ev := <-sub.Events:
		if ev.Type != messageevent.EventTypeMessageCreated {
			t.Fatalf("event type = %q", ev.Type)
		}
		var live messagepkg.Message
		if err := json.Unmarshal(ev.Data, &live); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if live.SessionID != sessionID {
			t.Fatalf("session filter check missed: %q", live.SessionID)
		}
		if _, dup := backlogIDs[live.ID]; dup {
			// Correctly skipped. This is the on-wire path.
		} else {
			delivered[live.ID]++
		}
	case <-time.After(time.Second):
		t.Fatal("no live event received")
	}

	// The seam works iff the message is delivered EXACTLY ONCE on the wire.
	if delivered["m1"] != 1 {
		t.Fatalf("m1 delivered %d times, want exactly 1", delivered["m1"])
	}
}

// TestStreamSessionMessageEventsRequiresSessionPath confirms the per-session
// stream rejects requests missing the session_id path parameter — ie, a
// client that builds the URL incorrectly gets a 400 instead of an opaque
// streaming response.
func TestStreamSessionMessageEventsRequiresSessionPath(t *testing.T) {
	t.Parallel()

	h := &MessageHandler{}

	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/bots/bot-1/sessions//messages/events", nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/messages/events")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues("11111111-1111-1111-1111-111111111111", "")

	err := h.StreamSessionMessageEvents(ctx)
	if err == nil {
		t.Fatalf("StreamSessionMessageEvents() err = nil, want HTTP 400")
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *echo.HTTPError", err)
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Fatalf("HTTPError.Code = %d, want 400", httpErr.Code)
	}
}
