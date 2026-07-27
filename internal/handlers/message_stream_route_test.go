package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

// A session's contents are read through the session runtime, over the chat
// WebSocket, and nowhere else. These routes were the two earlier answers to
// "how does a client follow a conversation", and both are gone: the bot-wide
// stream carried every session's messages to every listener, and the
// per-session stream gave each connection its own private projection of one
// session — the divergence the runtime's snapshot and deltas exist to prevent.
//
// Registering either again would let a client re-attach to a view no other
// subscriber shares, silently and without failing anywhere.
func TestRemovedMessageSSERoutesStayRemoved(t *testing.T) {
	t.Parallel()

	e := echo.New()
	(&MessageHandler{}).Register(e)

	for _, path := range []string{
		"/bots/bot-1/messages/events",
		"/bots/bot-1/sessions/session-1/messages/events",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
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
	req := httptest.NewRequest(http.MethodGet, "/bots/bot-1/messages", nil)
	req.Header.Set(echo.HeaderAuthorization, "Bearer test")
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
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
