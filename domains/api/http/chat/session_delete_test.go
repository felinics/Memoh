package chat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	session "github.com/memohai/memoh/domains/agent/chat/thread"
	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/api/http/httpfixture"
)

type sessionDeleteQueries struct {
	stubThreadStore
	bot              bot.Record
	session          session.Thread
	softDeleteCalled bool
	softDeleteID     string
}

func (q *sessionDeleteQueries) InTransaction(_ context.Context, fn func(session.Store) error) error {
	return fn(q)
}

func (q *sessionDeleteQueries) GetBotMetadata(_ context.Context, _ string) (map[string]any, error) {
	return botMetadataMap(q.bot), nil
}

func (q *sessionDeleteQueries) GetThread(_ context.Context, _ string) (session.Thread, error) {
	return q.session, nil
}

func (q *sessionDeleteQueries) SoftDeleteThread(_ context.Context, id string) error {
	q.softDeleteCalled = true
	q.softDeleteID = id
	return nil
}

type recordingACPSessionCloser struct {
	closed []string
	active map[string]bool
}

func (c *recordingACPSessionCloser) CloseSession(sessionID string) error {
	c.closed = append(c.closed, sessionID)
	return nil
}

func (*recordingACPSessionCloser) BindRuntime(_, _, _, _, _, _ string) error {
	return nil
}

func (c *recordingACPSessionCloser) IsSessionActive(sessionID string) bool {
	return c.active != nil && c.active[sessionID]
}

func TestDeleteACPAgentSessionClosesRuntimeBeforeSoftDelete(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionDeleteQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: testThreadFromLegacy(sessionID, botID, session.TypeACPAgent, httpfixture.JSON(map[string]any{
			"acp_agent_id": "codex",
			"project_path": "/data/app",
		})),
	}
	queries.session.Title = "Codex"
	closer := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	rec, err := callDeleteSession(handler, botID, sessionID)
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(closer.closed) != 1 || closer.closed[0] != sessionID {
		t.Fatalf("closed ACP sessions = %#v, want [%s]", closer.closed, sessionID)
	}
	if !queries.softDeleteCalled || queries.softDeleteID != sessionID {
		t.Fatalf("soft delete = %v id=%v, want session %s", queries.softDeleteCalled, queries.softDeleteID, sessionID)
	}
}

func TestDeleteChatSessionDoesNotCloseACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := &sessionDeleteQueries{
		bot:     httpfixture.BotRow(botID, map[string]any{}),
		session: testThreadFromLegacy(sessionID, botID, session.TypeChat, httpfixture.JSON(map[string]any{})),
	}
	queries.session.Title = "Chat"
	closer := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot)),
		httpfixture.NewAdminAccountService("admin"),
	)

	if _, err := callDeleteSession(handler, botID, sessionID); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("chat session closed ACP runtime: %#v", closer.closed)
	}
	if !queries.softDeleteCalled {
		t.Fatal("chat session was not soft-deleted")
	}
}

func TestDeleteSessionRejectsSubagentForChatUser(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionDeleteQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: testThreadFromLegacyFull(
			sessionID, botID, session.TypeSubagent, "", "",
			httpfixture.JSON(map[string]any{"agent_id": "worker"}), nil, userID, "spawned worker",
		),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot)),
		httpfixture.NewAdminAccountService("user"),
	)

	_, err := callDeleteSessionAs(handler, botID, sessionID, userID)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("DeleteSession() error = %v, want HTTP 403", err)
	}
	if queries.softDeleteCalled {
		t.Fatal("chat user should not be able to delete subagent sessions directly")
	}
}

func callDeleteSession(handler *SessionHandler, botID, sessionID string) (*httptest.ResponseRecorder, error) {
	return callDeleteSessionAs(handler, botID, sessionID, "user-1")
}

func callDeleteSessionAs(handler *SessionHandler, botID, sessionID, userID string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/bots/"+botID+"/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.DeleteSession(ctx)
}
