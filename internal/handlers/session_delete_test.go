package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type sessionDeleteResetCtxKey struct{}

type sessionDeleteQueries struct {
	dbstore.Queries
	bot              sqlc.GetBotByIDRow
	session          sqlc.BotSession
	softDeleteCalled bool
	softDeleteID     pgtype.UUID
	// events is shared with the fake reset service so the test can assert the
	// soft delete ran inside the begin/release window.
	events              *[]string
	softDeleteSawFenced bool
}

func (q *sessionDeleteQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *sessionDeleteQueries) GetSessionByID(_ context.Context, _ pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (*sessionDeleteQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return []sqlc.ListBotUserGrantsForUserRow{{Permissions: []byte(`["chat"]`)}}, nil
}

func (q *sessionDeleteQueries) SoftDeleteSession(ctx context.Context, id pgtype.UUID) error {
	q.softDeleteCalled = true
	q.softDeleteID = id
	if q.events != nil {
		*q.events = append(*q.events, "soft-delete")
	}
	q.softDeleteSawFenced = ctx.Value(sessionDeleteResetCtxKey{}) != nil
	return nil
}

type recordingACPSessionCloser struct {
	closed []string
	active map[string]bool
	// events shares the ordered call log with sessionDeleteQueries.
	events     *[]string
	resetBots  []string
	resetScope []string
}

func (c *recordingACPSessionCloser) CloseSession(sessionID string) error {
	c.closed = append(c.closed, sessionID)
	return nil
}

func (c *recordingACPSessionCloser) BeginSessionHistoryReset(ctx context.Context, botID, sessionID string) (context.Context, func(), error) {
	if c.events != nil {
		*c.events = append(*c.events, "begin")
	}
	c.resetBots = append(c.resetBots, botID)
	c.resetScope = append(c.resetScope, sessionID)
	release := func() {
		if c.events != nil {
			*c.events = append(*c.events, "release")
		}
	}
	return context.WithValue(ctx, sessionDeleteResetCtxKey{}, true), release, nil
}

func (*recordingACPSessionCloser) BindRuntime(context.Context, string, string, string, string, string, string) error {
	return nil
}

func (c *recordingACPSessionCloser) IsSessionActive(sessionID string) bool {
	return c.active != nil && c.active[sessionID]
}

func TestDeleteACPAgentSessionSoftDeletesInsideResetLease(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	events := []string{}
	queries := &sessionDeleteQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeACPAgent,
			Title:    "Codex",
			Metadata: testJSON(map[string]any{"acp_agent_id": "codex", "project_path": "/data/app"}),
		},
		events: &events,
	}
	closer := &recordingACPSessionCloser{events: &events}
	handler := NewSessionHandler(
		slog.Default(),
		session.NewService(nil, queries, nil),
		closer,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callDeleteSession(handler, botID, sessionID)
	if err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("DeleteSession bypassed reset gate through CloseSession: %#v", closer.closed)
	}
	if !queries.softDeleteCalled || queries.softDeleteID != testUUID(sessionID) {
		t.Fatalf("soft delete = %v id=%v, want session %s", queries.softDeleteCalled, queries.softDeleteID, sessionID)
	}
	// The soft delete must run inside the session reset lease: acquired first,
	// released only after the delete committed.
	if len(events) != 3 || events[0] != "begin" || events[1] != "soft-delete" || events[2] != "release" {
		t.Fatalf("reset lifecycle order = %v, want [begin soft-delete release]", events)
	}
	if len(closer.resetBots) != 1 || closer.resetBots[0] != botID || closer.resetScope[0] != sessionID {
		t.Fatalf("reset scope = (%v, %v), want the deleted session", closer.resetBots, closer.resetScope)
	}
	if !queries.softDeleteSawFenced {
		t.Fatal("soft delete did not run on the reset-fenced context")
	}
}

func TestDeleteChatSessionDoesNotCloseACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := &sessionDeleteQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "Chat",
			Metadata: testJSON(map[string]any{}),
		},
	}
	closer := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		session.NewService(nil, queries, nil),
		closer,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	if _, err := callDeleteSession(handler, botID, sessionID); err != nil {
		t.Fatalf("DeleteSession() error = %v", err)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("chat session closed ACP runtime: %#v", closer.closed)
	}
	if len(closer.resetBots) != 0 {
		t.Fatalf("chat session acquired an ACP reset lease: %#v", closer.resetBots)
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
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:              testUUID(sessionID),
			BotID:           testUUID(botID),
			Type:            session.TypeSubagent,
			Title:           "spawned worker",
			Metadata:        testJSON(map[string]any{"agent_id": "worker"}),
			CreatedByUserID: testUUID(userID),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		session.NewService(nil, queries, nil),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
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
	req := httptest.NewRequest(http.MethodDelete, "/bots/"+botID+"/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.DeleteSession(ctx)
}
