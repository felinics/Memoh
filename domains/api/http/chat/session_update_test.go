package chat

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/memohai/memoh/domains/agent/acp/profile"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
	botpersistence "github.com/memohai/memoh/domains/api/bot/persistence"
	httpfixture "github.com/memohai/memoh/domains/api/http/internal/test"
	"github.com/memohai/memoh/internal/apperror"
)

type sessionUpdateQueries struct {
	stubThreadStore
	bot          botpersistence.Record
	session      session.Thread
	messageCount int64
	permissions  []byte

	updateCalled bool
	updateParams session.DescriptorRecord

	titleUpdateCalled bool
}

func (q *sessionUpdateQueries) InTransaction(_ context.Context, fn func(session.Store) error) error {
	return fn(q)
}

func (q *sessionUpdateQueries) GetBotMetadata(_ context.Context, _ string) (map[string]any, error) {
	return botMetadataMap(q.bot), nil
}

func (q *sessionUpdateQueries) GetThread(_ context.Context, _ string) (session.Thread, error) {
	return q.session, nil
}

func (q *sessionUpdateQueries) CountThreadMessages(_ context.Context, _ string) (int64, error) {
	return q.messageCount, nil
}

func (q *sessionUpdateQueries) UpdateThreadDescriptor(_ context.Context, record session.DescriptorRecord) (session.Thread, error) {
	q.updateCalled = true
	q.updateParams = record
	updated := q.session
	updated.Type = record.Type
	updated.SessionMode = record.SessionMode
	updated.RuntimeType = record.RuntimeType
	updated.RuntimeMetadata = record.RuntimeMetadata
	updated.Metadata = record.Metadata
	return updated, nil
}

func (q *sessionUpdateQueries) UpdateThreadTitle(_ context.Context, _, title string, _ *session.RuntimeFence) (session.Thread, error) {
	q.titleUpdateCalled = true
	updated := q.session
	updated.Title = title
	return updated, nil
}

func TestUpdateSessionSwitchesEmptyChatToACPAgent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Title:    "",
			Metadata: map[string]any{},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
	metadata := queries.updateParams.Metadata
	if metadata["acp_agent_id"] != "codex" || metadata["project_path"] != "/data/app" {
		t.Fatalf("metadata = %#v, want ACP agent metadata", metadata)
	}
	if metadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime owner = %#v, want authenticated user", metadata["runtime_owner_account_id"])
	}
	runtimeMetadata := queries.updateParams.RuntimeMetadata
	if runtimeMetadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", runtimeMetadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionRejectsConflictingTypeAndRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Metadata: map[string]any{},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	// type=acp_agent contradicts runtime_type=model; this must 400, not silently
	// downgrade the session to a plain model chat.
	_, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","runtime_type":"model"}`)
	requireHTTPStatus(t, err, http.StatusBadRequest)
	// The 400 must come from the conflict guard specifically, not an unrelated
	// validation path.
	if op := apperror.OpOf(err); op != "resolve session type conflict" {
		t.Fatalf("op = %q, want resolve session type conflict", op)
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata must not be called for a contradictory type/runtime payload")
	}
}

func TestUpdateSessionRejectsSystemACPRuntimeAsBadRequest(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:          sessionID,
			BotID:       botID,
			Type:        session.TypeChat,
			SessionMode: session.TypeChat,
			RuntimeType: session.RuntimeModel,
			Title:       "",
			Metadata:    map[string]any{},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	_, err := callUpdateSession(handler, botID, sessionID, `{"session_mode":"schedule","runtime_type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	requireHTTPStatus(t, err, http.StatusBadRequest)
	if apperror.KindOf(err) != apperror.KindInvalid {
		t.Fatalf("kind = %q, want KindInvalid", apperror.KindOf(err))
	}
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata must not be called for an unsupported runtime/mode payload")
	}
}

func TestUpdateSessionAllowsConcordantACPTypeAndRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Metadata: map[string]any{},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Title:    "",
			Metadata: map[string]any{"runtime_owner_account_id": "stale-metadata-owner"},
			RuntimeMetadata: map[string]any{
				"runtime_owner_account_id": "stale-runtime-owner",
			},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	metadata := queries.updateParams.Metadata
	if metadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime owner = %#v, want authenticated user", metadata["runtime_owner_account_id"])
	}
	runtimeMetadata := queries.updateParams.RuntimeMetadata
	if runtimeMetadata["runtime_owner_account_id"] != "user-1" {
		t.Fatalf("runtime metadata owner = %#v, want authenticated user", runtimeMetadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionSwitchToACPRequiresWorkspaceExec(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            session.TypeChat,
			Title:           "",
			Metadata:        map[string]any{},
			CreatedByUserID: userID,
		},
		permissions: []byte(`["chat"]`),
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	requireHTTPStatus(t, err, http.StatusForbidden)
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata should not be called without workspace_exec")
	}
}

func TestUpdateSessionDefaultsACPProjectPath(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Title:    "",
			Metadata: map[string]any{},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	metadata := queries.updateParams.Metadata
	if metadata["project_path"] != session.DefaultACPProjectPath || metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("metadata = %#v, want default ACP project", metadata)
	}
}

func TestUpdateSessionDefaultsACPProjectPathBeforeAgentChangeCheck(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "",
			Metadata: map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             session.DefaultACPProjectPath,
				"acp_project_mode":         session.DefaultACPProjectMode,
				"runtime_owner_account_id": "original-owner",
			},
		},
		messageCount: 1,
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	rec, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex"}}`)
	if err != nil {
		t.Fatalf("UpdateSession() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	metadata := queries.updateParams.Metadata
	if metadata["project_path"] != session.DefaultACPProjectPath || metadata["acp_project_mode"] != session.DefaultACPProjectMode {
		t.Fatalf("metadata = %#v, want default ACP project", metadata)
	}
}

func TestUpdateSessionRejectsAgentChangeAfterFirstMessage(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:       sessionID,
			BotID:    botID,
			Type:     session.TypeChat,
			Title:    "",
			Metadata: map[string]any{},
		},
		messageCount: 1,
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
	)

	_, err := callUpdateSession(handler, botID, sessionID, `{"type":"acp_agent","metadata":{"acp_agent_id":"codex","project_path":"/data/app"}}`)
	requireHTTPStatus(t, err, http.StatusConflict)
	if queries.updateCalled {
		t.Fatal("UpdateSessionTypeAndMetadata should not be called after the session is locked")
	}
}

func TestUpdateSessionRejectsRetagToSubagentForChatUser(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            session.TypeChat,
			Title:           "",
			Metadata:        map[string]any{},
			CreatedByUserID: userID,
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"type":"subagent","metadata":{"agent_id":"direct"}}`)
	requireHTTPStatus(t, err, http.StatusForbidden)
	if queries.updateCalled {
		t.Fatal("chat user should not be able to retag sessions as subagent")
	}
}

func TestGetSessionAllowsChatUserToReadOwnSubagent(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "33333333-3333-3333-3333-333333333333"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            session.TypeSubagent,
			Title:           "spawned worker",
			Metadata:        map[string]any{"agent_id": "worker"},
			CreatedByUserID: userID,
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
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
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:              sessionID,
			BotID:           botID,
			Type:            session.TypeSubagent,
			Title:           "spawned worker",
			Metadata:        map[string]any{"agent_id": "worker"},
			CreatedByUserID: userID,
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("user"),
	)

	_, err := callUpdateSessionAs(handler, botID, sessionID, userID, `{"title":"renamed directly"}`)
	requireHTTPStatus(t, err, http.StatusForbidden)
	if queries.titleUpdateCalled {
		t.Fatal("chat user should not be able to title-update subagent sessions directly")
	}
}

func TestUpdateSessionAllowsEmptyACPAgentChangeAndClosesRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "55555555-5555-5555-5555-555555555555"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "",
			Metadata: map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"runtime_owner_account_id": "original-owner",
			},
		},
		messageCount: 0,
	}
	closer := &recordingACPSessionCloser{active: map[string]bool{sessionID: true}}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
	if len(closer.closed) != 1 || closer.closed[0] != sessionID {
		t.Fatalf("closed ACP sessions = %#v, want [%s]", closer.closed, sessionID)
	}
	metadata := queries.updateParams.Metadata
	if metadata["runtime_owner_account_id"] != "original-owner" {
		t.Fatalf("runtime owner = %#v, want original owner", metadata["runtime_owner_account_id"])
	}
}

func TestUpdateSessionMetadataPatchPreservesDiscussACPRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "66666666-6666-6666-6666-666666666666"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{
			acpprofile.MetadataKeyACP: map[string]any{
				"agents": map[string]any{
					acpprofile.AgentCodexID: map[string]any{"enabled": true, "setup_mode": "self"},
				},
			},
		}),
		session: session.Thread{
			ID:          sessionID,
			BotID:       botID,
			Type:        session.TypeDiscuss,
			SessionMode: session.TypeDiscuss,
			RuntimeType: session.RuntimeACPAgent,
			Title:       "Discuss Codex",
			Metadata: map[string]any{
				"acp_agent_id":     "codex",
				"project_path":     "/data/app",
				"acp_project_mode": "project",
				"topic":            "old",
			},
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"acp_project_mode":         "project",
				"runtime_owner_account_id": "original-owner",
			},
		},
	}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
	metadata := queries.updateParams.Metadata
	if metadata["topic"] != "new" || metadata["acp_agent_id"] != "codex" || metadata["project_path"] != "/data/app" {
		t.Fatalf("metadata = %#v, want patched topic with ACP metadata preserved", metadata)
	}
	runtimeMetadata := queries.updateParams.RuntimeMetadata
	if runtimeMetadata["runtime_owner_account_id"] != "original-owner" || runtimeMetadata["acp_agent_id"] != "codex" {
		t.Fatalf("runtime metadata = %#v, want ACP runtime metadata preserved", runtimeMetadata)
	}
}

func TestUpdateSessionSwitchesACPAgentToChatClearsMetadataAndClosesRuntime(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "44444444-4444-4444-4444-444444444444"
	queries := &sessionUpdateQueries{
		bot: httpfixture.BotRow(botID, map[string]any{}),
		session: session.Thread{
			ID:    sessionID,
			BotID: botID,
			Type:  session.TypeACPAgent,
			Title: "Codex",
			Metadata: map[string]any{
				"acp_agent_id":     "codex",
				"project_path":     "/data/app",
				"acp_project_mode": "project",
				"acp_session_id":   "runtime-1",
				"acp_status":       "active",
			},
			RuntimeMetadata: map[string]any{
				"acp_agent_id":             "codex",
				"project_path":             "/data/app",
				"acp_project_mode":         "project",
				"runtime_owner_account_id": "original-owner",
			},
		},
	}
	closer := &recordingACPSessionCloser{}
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		closer,
		httpfixture.NewBotService(nil, httpfixture.NewBotStore(queries.bot, queries.permissions)),
		httpfixture.NewAdminAccountService("admin"),
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
	metadata := queries.updateParams.Metadata
	for _, key := range []string{"acp_agent_id", "project_path", "acp_project_mode", "acp_session_id", "acp_status"} {
		if _, ok := metadata[key]; ok {
			t.Fatalf("metadata key %q was not cleared: %#v", key, metadata)
		}
	}
	runtimeMetadata := queries.updateParams.RuntimeMetadata
	if len(runtimeMetadata) != 0 {
		t.Fatalf("runtime metadata = %#v, want cleared", runtimeMetadata)
	}
	if len(closer.closed) != 1 || closer.closed[0] != sessionID {
		t.Fatalf("closed ACP sessions = %#v, want [%s]", closer.closed, sessionID)
	}
}

func callUpdateSession(handler *SessionHandler, botID, sessionID, body string) (*httptest.ResponseRecorder, error) {
	return callUpdateSessionAs(handler, botID, sessionID, "user-1", body)
}

func callUpdateSessionAs(handler *SessionHandler, botID, sessionID, userID, body string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPatch, "/bots/"+botID+"/sessions/"+sessionID, bytes.NewBufferString(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.UpdateSession(ctx)
}

func callGetSession(handler *SessionHandler, botID, sessionID, userID string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/bots/"+botID+"/sessions/"+sessionID, nil)
	rec := httptest.NewRecorder()
	ctx := httpfixture.AuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id")
	ctx.SetParamNames("bot_id", "session_id")
	ctx.SetParamValues(botID, sessionID)
	return rec, handler.GetSession(ctx)
}
