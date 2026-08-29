package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
	"github.com/felinics/memoh/internal/botagents"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type sessionUpdateQueries struct {
	dbstore.Queries
	bot          sqlc.GetBotByIDRow
	botAgent     sqlc.BotAgent
	session      sqlc.BotSession
	messageCount int64
	permissions  []byte

	updateCalled bool
	updateParams sqlc.UpdateSessionTypeAndMetadataParams

	titleUpdateCalled bool
}

func (q *sessionUpdateQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *sessionUpdateQueries) GetBotAgentByID(_ context.Context, _ sqlc.GetBotAgentByIDParams) (sqlc.BotAgent, error) {
	return q.botAgent, nil
}

func (q *sessionUpdateQueries) GetSessionByID(_ context.Context, _ pgtype.UUID) (sqlc.BotSession, error) {
	return q.session, nil
}

func (*sessionUpdateQueries) ListSessionsByBot(_ context.Context, _ pgtype.UUID) ([]sqlc.ListSessionsByBotRow, error) {
	return nil, nil
}

func (q *sessionUpdateQueries) CountMessagesBySession(_ context.Context, _ pgtype.UUID) (int64, error) {
	return q.messageCount, nil
}

func (q *sessionUpdateQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	permissions := q.permissions
	if permissions == nil {
		permissions = []byte(`["chat"]`)
	}
	return []sqlc.ListBotUserGrantsForUserRow{{Permissions: permissions}}, nil
}

func (q *sessionUpdateQueries) UpdateSessionTypeAndMetadata(_ context.Context, arg sqlc.UpdateSessionTypeAndMetadataParams) (sqlc.BotSession, error) {
	q.updateCalled = true
	q.updateParams = arg
	updated := q.session
	updated.Type = arg.Type
	updated.SessionMode = arg.SessionMode
	updated.RuntimeType = arg.RuntimeType
	updated.BotAgentID = arg.BotAgentID
	updated.RuntimeMetadata = arg.RuntimeMetadata
	updated.Metadata = arg.Metadata
	return updated, nil
}

func TestUpdateSessionResolvesPersistedBotAgentDescriptor(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	botAgentID := "33333333-3333-3333-3333-333333333333"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": false, "setup_mode": "self"},
				},
			},
		}),
		botAgent: sqlc.BotAgent{
			ID:       testUUID(botAgentID),
			BotID:    testUUID(botID),
			Name:     "Codex",
			Runtime:  botagents.RuntimeACP,
			Enabled:  true,
			Metadata: testJSON(map[string]any{botagents.MetadataProviderKey: acpprofile.AgentCodexID}),
		},
		session: sqlc.BotSession{
			ID:          testUUID(sessionID),
			BotID:       testUUID(botID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
			Metadata:    testJSON(map[string]any{}),
		},
	}
	resetter := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		resetter,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)
	handler.SetBotAgents(botagents.NewService(slog.Default(), queries))

	rec, err := callUpdateSession(handler, botID, sessionID, `{"bot_agent_id":"`+botAgentID+`"}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if queries.updateParams.BotAgentID.String() != botAgentID {
		t.Fatalf("bot_agent_id = %q, want %q", queries.updateParams.BotAgentID.String(), botAgentID)
	}
	if queries.updateParams.RuntimeType != session.RuntimeACPAgent || queries.updateParams.Type != session.TypeACPAgent {
		t.Fatalf("descriptor = %q/%q, want acp_agent/acp_agent", queries.updateParams.Type, queries.updateParams.RuntimeType)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["acp_agent_id"] != acpprofile.AgentCodexID || metadata["project_path"] != session.DefaultACPProjectPath {
		t.Fatalf("metadata = %#v, want resolved Codex descriptor", metadata)
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.updateParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if runtimeMetadata["acp_agent_id"] != acpprofile.AgentCodexID || runtimeMetadata["project_path"] != session.DefaultACPProjectPath {
		t.Fatalf("runtime metadata = %#v, want resolved Codex descriptor", runtimeMetadata)
	}
}

func (q *sessionUpdateQueries) UpdateSessionTitle(_ context.Context, arg sqlc.UpdateSessionTitleParams) (sqlc.BotSession, error) {
	q.titleUpdateCalled = true
	updated := q.session
	updated.Title = arg.Title
	return updated, nil
}

func TestUpdateSessionSwitchesEmptyChatToACPAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "",
			Metadata: testJSON(map[string]any{}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app","runtime_owner_account_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata was not called")
	}
	if queries.updateParams.Type != session.TypeACPAgent {
		t.Fatalf("updated type = %q, want %q", queries.updateParams.Type, session.TypeACPAgent)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["acp_agent_id"] != "codex" || metadata["project_path"] != "/data/app" {
		t.Fatalf("metadata = %#v, want ACP agent metadata", metadata)
	}
	if metadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime owner = %#v, want authenticated user", metadata["runtime_owner_account_id"])
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.updateParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if runtimeMetadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", runtimeMetadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionRejectsConflictingTypeAndRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Metadata: testJSON(map[string]any{}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	// type=acp_agent contradicts runtime_type=model; this must 400, not silently
	// downgrade the session to a plain model chat.
	_, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","runtime_type":"model"}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("UpdateSession() error = %v, want HTTP 400", err)
	}
	// The 400 must come from the conflict guard specifically, not an unrelated
	// validation, so assert its message.
	if msg, _ := httpErr.Message.(string); !strings.Contains(msg, "conflicts with runtime_type") {
		t.Fatalf("error message = %q, want a 'conflicts with runtime_type' conflict error", msg)
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata must not be called for a contradictory type/runtime payload")
	}
}

func TestUpdateSessionRejectsSystemACPRuntimeAsBadRequest(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:          testUUID(sessionID),
			BotID:       testUUID(botID),
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
			Title:       "",
			Metadata:    testJSON(map[string]any{}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	_, err := callUpdateSession(handler, botID, sessionID, `{"session_mode":"schedule","runtime_type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("UpdateSession() error = %v, want HTTP 400", err)
	}
	if msg, _ := httpErr.Message.(string); !strings.Contains(msg, "only supported") {
		t.Fatalf("error message = %q, want an unsupported runtime/mode message", msg)
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata must not be called for an unsupported runtime/mode payload")
	}
}

func TestUpdateSessionAllowsConcordantACPTypeAndRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Metadata: testJSON(map[string]any{}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	// type=acp_agent WITH a concordant runtime_type=acp_agent is NOT a conflict
	// and must be allowed through (locks the guard's RuntimeACPAgent exclusion).
	rec, err := callUpdateSession(handler, botID, sessionID,
		`{"type":"acp_agent","runtime_type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app","runtime_owner_account_id":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v, want success for concordant payload", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata should be called for a concordant acp_agent payload")
	}
	if queries.updateParams.Type != session.TypeACPAgent {
		t.Fatalf("updated type = %q, want %q", queries.updateParams.Type, session.TypeACPAgent)
	}
}

func TestUpdateSessionSwitchToACPDoesNotInheritOwnerFromNonACPMetadata(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "",
			Metadata: testJSON(map[string]any{"runtime_owner_account_id": "stale-metadata-owner"}),
			RuntimeMetadata: testJSON(map[string]any{
				"runtime_owner_account_id": "stale-runtime-owner",
			}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime owner = %#v, want authenticated user", metadata["runtime_owner_account_id"])
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.updateParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if runtimeMetadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", runtimeMetadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionSwitchToACPRequiresWorkspaceExec(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:              testUUID(sessionID),
			BotID:           testUUID(botID),
			Type:            session.TypeChat,
			Title:           "",
			Metadata:        testJSON(map[string]any{}),
			CreatedByUserID: testUUID(userID),
		},
		permissions: []byte(`["chat"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("UpdateSession() error = %v, want HTTP 403", err)
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata should not be called without workspace_exec")
	}
}

func TestUpdateSessionDefaultsACPProjectPath(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "",
			Metadata: testJSON(map[string]any{}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["project_path"] != session.DefaultACPProjectPath || metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("metadata = %#v, want default ACP project", metadata)
	}
}

func TestUpdateSessionDefaultsACPProjectPathBeforeAgentChangeCheck(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "",
			Metadata: testJSON(map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             session.DefaultACPProjectPath,
				"acp_project_mode":         session.DefaultACPProjectMode,
				"runtime_owner_account_id": "original-owner",
			}),
		},
		messageCount: 1,
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["project_path"] != session.DefaultACPProjectPath || metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("metadata = %#v, want default ACP project", metadata)
	}
}

func TestUpdateSessionRejectsAgentChangeAfterFirstMessage(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:       testUUID(sessionID),
			BotID:    testUUID(botID),
			Type:     session.TypeChat,
			Title:    "",
			Metadata: testJSON(map[string]any{}),
		},
		messageCount: 1,
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		&recordingACPSessionCloser{},
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	_, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusConflict {
		t.Fatalf("UpdateSession() error = %v, want HTTP 409", err)
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata should not be called after the session is locked")
	}
}

func TestUpdateSessionRejectsRetagToSubagentForChatUser(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:              testUUID(sessionID),
			BotID:           testUUID(botID),
			Type:            session.TypeChat,
			Title:           "",
			Metadata:        testJSON(map[string]any{}),
			CreatedByUserID: testUUID(userID),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"type":"subagent","metadata":{"agent_id":"direct"}}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("UpdateSession() error = %v, want HTTP 403", err)
	}
	if queries.updateCalled {
		t.Fatal("chat user should not be able to retag sessions as subagent")
	}
}

func TestGetSessionAllowsChatUserToReadOwnSubagent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
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
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	rec, err := callGetSession(handler, botID, sessionID, userID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestUpdateSessionRejectsSubagentTitleUpdateForChatUser(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
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
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"title":"renamed directly"}`)
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusForbidden {
		t.Fatalf("UpdateSession() error = %v, want HTTP 403", err)
	}
	if queries.titleUpdateCalled {
		t.Fatal("chat user should not be able to title-update subagent sessions directly")
	}
}

func TestUpdateSessionAllowsEmptyACPAgentChangeAndClosesRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "55555555-5555-5555-5555-555555555555"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "",
			Metadata: testJSON(map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"runtime_owner_account_id": "original-owner",
			}),
		},
		messageCount: 0,
	}
	closer := &recordingACPSessionCloser{active: map[string]bool{sessionID: true}}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/other"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata was not called")
	}
	if len(closer.closed) != 0 {
		t.Fatalf("UpdateSession bypassed the reset gate through CloseSession: %#v", closer.closed)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["runtime_owner_account_id"] != "original-owner" {
		t.Fatalf("runtime owner = %#v, want original owner", metadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionMetadataPatchPreservesDiscussACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "66666666-6666-6666-6666-666666666666"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: sqlc.BotSession{
			ID:          testUUID(sessionID),
			BotID:       testUUID(botID),
			Type:        session.TypeDiscuss,
			SessionMode: session.TypeDiscuss,
			RuntimeType: session.RuntimeACPAgent,
			Title:       "Discuss Codex",
			Metadata: testJSON(map[string]any{
				"acp_agent_id":     "codex",
				"project_path":     "/data/app",
				"acp_project_mode": "project",
				"topic":            "old",
			}),
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"acp_project_mode":         "project",
				"runtime_owner_account_id": "original-owner",
			}),
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"metadata":{"topic":"new"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if queries.updateParams.Type != session.TypeDiscuss || queries.updateParams.SessionMode != session.TypeDiscuss || queries.updateParams.RuntimeType != session.RuntimeACPAgent {
		t.Fatalf("descriptor = %q/%q/%q, want discuss/discuss/acp_agent", queries.updateParams.Type, queries.updateParams.SessionMode, queries.updateParams.RuntimeType)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	if metadata["topic"] != "new" || metadata["acp_agent_id"] != "codex" || metadata["project_path"] != "/data/app" {
		t.Fatalf("metadata = %#v, want patched topic with ACP metadata preserved", metadata)
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.updateParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if runtimeMetadata["runtime_owner_account_id"] != "original-owner" || runtimeMetadata["acp_agent_id"] != "codex" {
		t.Fatalf("runtime metadata = %#v, want ACP runtime metadata preserved", runtimeMetadata)
	}
}

func TestUpdateSessionSwitchesACPAgentToChatClearsMetadataAndClosesRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "44444444-4444-4444-4444-444444444444"
	queries := &sessionUpdateQueries{
		bot: testBotRow(botID, map[string]any{}),
		session: sqlc.BotSession{
			ID:    testUUID(sessionID),
			BotID: testUUID(botID),
			Type:  session.TypeACPAgent,
			Title: "Codex",
			Metadata: testJSON(map[string]any{
				"acp_agent_id":     "codex",
				"project_path":     "/data/app",
				"acp_project_mode": "project",
				"acp_session_id":   "runtime-1",
				"acp_status":       "active",
			}),
			RuntimeMetadata: testJSON(map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"acp_project_mode":         "project",
				"runtime_owner_account_id": "original-owner",
			}),
		},
	}
	closer := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"chat"}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if queries.updateParams.Type != session.TypeChat {
		t.Fatalf("updated type = %q, want %q", queries.updateParams.Type, session.TypeChat)
	}
	var metadata map[string]any
	if err := json.Unmarshal(queries.updateParams.Metadata, &metadata); err != nil {
		t.Fatalf("metadata json = %v", err)
	}
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "acp_session_id", "acp_status"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("metadata key %q was not cleared: %#v", key, metadata)
		}
	}
	var runtimeMetadata map[string]any
	if err := json.Unmarshal(queries.updateParams.RuntimeMetadata, &runtimeMetadata); err != nil {
		t.Fatalf("runtime metadata json = %v", err)
	}
	if len(runtimeMetadata) != 0 {
		t.Fatalf("runtime metadata = %#v, want cleared", runtimeMetadata)
	}
	if len(closer.closed) != 0 {
		t.Fatalf("UpdateSession bypassed the reset gate through CloseSession: %#v", closer.closed)
	}
}

func callUpdateSession(handler *SessionHandler, botID, sessionID, body string) (*httptest.ResponseRecorder, error) {
	return callUpdateSessionAs(handler, botID, sessionID, "user-1", body)
}

func callUpdateSessionAs(handler *SessionHandler, botID, sessionID, userID, body string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPatch, "/bots/"+botID+"/sessions/"+sessionID, bytes.NewBufferString(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.UpdateSession(ctx)
}

func callGetSession(handler *SessionHandler, botID, sessionID, userID string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.GetSession(ctx)
}
