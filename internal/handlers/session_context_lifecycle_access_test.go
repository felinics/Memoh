package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const lifecycleTestGranteeID = "cccccccc-cccc-cccc-cccc-cccccccccccc"

// contextLifecycleAccessStub adds a grantee, a run row, and stored texts to
// the lifecycle query stub so the text endpoints can be exercised end to end.
type contextLifecycleAccessStub struct {
	*contextLifecycleQueryStub
	grants     []sqlc.ListBotUserGrantsForUserRow
	run        sqlc.GetContextLifecycleByRunIDRow
	textParams []sqlc.ListContextFragmentTextsParams
}

func (s *contextLifecycleAccessStub) ListBotUserGrantsForUser(context.Context, sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return s.grants, nil
}

func (s *contextLifecycleAccessStub) GetContextLifecycleByRunID(context.Context, pgtype.UUID) (sqlc.GetContextLifecycleByRunIDRow, error) {
	return s.run, nil
}

func (s *contextLifecycleAccessStub) ListContextFragmentTexts(_ context.Context, arg sqlc.ListContextFragmentTextsParams) ([]sqlc.ListContextFragmentTextsRow, error) {
	s.textParams = append(s.textParams, arg)
	return []sqlc.ListContextFragmentTextsRow{{ContentHash: "sys", Kind: "system_prompt", Label: "system.prompt.body", Text: "You are Memoh.", TextBytes: 14}}, nil
}

func newContextLifecycleAccessStub(t *testing.T, permissions ...string) *contextLifecycleAccessStub {
	t.Helper()
	const runID = "66666666-6666-6666-6666-666666666666"
	base := newContextLifecycleTestQueries()
	base.session.CreatedByUserID = testUUID(lifecycleTestGranteeID)
	snapshot := lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{
		Version:   contextfrag.LifecycleSnapshotVersion,
		Fragments: []contextfrag.FragmentRef{{Kind: contextfrag.KindSystemPrompt, Slot: contextfrag.SlotSystem, ContentHash: "canon-sys", TextHash: "sys", TokenEstimate: 40}},
	})
	base.lifecycleRows = []sqlc.ListRecentContextLifecyclesBySessionRow{{
		RunID:     testUUID(runID),
		Status:    "completed",
		CreatedAt: pgtype.Timestamptz{Valid: true},
		Snapshot:  snapshot,
	}}
	base.previewRows = []sqlc.ListContextFragmentPreviewsRow{{ContentHash: "sys", Kind: "system_prompt", Label: "system.prompt.body", Preview: "You are Memoh.", TextBytes: 14}}
	rawGrant, _ := json.Marshal(permissions)
	return &contextLifecycleAccessStub{
		contextLifecycleQueryStub: base,
		grants:                    []sqlc.ListBotUserGrantsForUserRow{{BotID: testUUID(lifecycleTestBotID), UserID: testUUID(lifecycleTestGranteeID), Permissions: rawGrant}},
		run: sqlc.GetContextLifecycleByRunIDRow{
			RunID:     testUUID(runID),
			BotID:     testUUID(lifecycleTestBotID),
			SessionID: testUUID(lifecycleTestSessionID),
			Snapshot:  snapshot,
		},
	}
}

func newContextLifecycleGranteeContext(t *testing.T, path string, runID bool) echo.Context {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+lifecycleTestBotID+"/sessions/"+lifecycleTestSessionID+"/context-lifecycle"+path, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, lifecycleTestGranteeID)
	if runID {
		ctx.SetParamNames("bot_id", "session_id", "run_id")
		ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID, "66666666-6666-6666-6666-666666666666")
	} else {
		ctx.SetParamNames("bot_id", "session_id")
		ctx.SetParamValues(lifecycleTestBotID, lifecycleTestSessionID)
	}
	return ctx
}

func newContextLifecycleGranteeHandler(queries dbstore.Queries) *SessionInfoHandler {
	return NewSessionInfoHandler(slog.New(slog.DiscardHandler), queries, bots.NewService(nil, queries), newTestAdminAccountService("user"), nil, nil)
}

func TestContextLifecycleTextsRequireWorkspaceRead(t *testing.T) {
	t.Parallel()

	queries := newContextLifecycleAccessStub(t, "chat")
	handler := newContextLifecycleGranteeHandler(queries)

	listCtx := newContextLifecycleGranteeContext(t, "", false)
	if err := handler.GetSessionContextLifecycle(listCtx); err != nil {
		t.Fatalf("a chat grantee still reads the content-light page: %v", err)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(listCtx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if _, ok := page["fragment_previews"]; ok || len(queries.previewParams) != 0 {
		t.Fatalf("fragment previews must not reach a chat-only grantee: %s", page["fragment_previews"])
	}

	err := handler.GetSessionContextLifecycleFragments(newContextLifecycleGranteeContext(t, "/66666666-6666-6666-6666-666666666666/fragments", true))
	problem, ok := apperror.ProblemFrom(err, "request-1")
	if !ok || problem.Code != string(apperror.CodeContextLifecycleAccessDenied) || problem.Status != http.StatusForbidden {
		t.Fatalf("fragments for a chat-only grantee = %v, want access denied", err)
	}
	if len(queries.textParams) != 0 {
		t.Fatalf("texts were read for a denied caller")
	}
}

func TestContextLifecycleTextsOpenToWorkspaceReaders(t *testing.T) {
	t.Parallel()

	queries := newContextLifecycleAccessStub(t, "chat", "workspace_read")
	handler := newContextLifecycleGranteeHandler(queries)

	listCtx := newContextLifecycleGranteeContext(t, "", false)
	if err := handler.GetSessionContextLifecycle(listCtx); err != nil {
		t.Fatalf("list: %v", err)
	}
	var page ContextLifecycleResponse
	if err := json.Unmarshal(listCtx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &page); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	if page.FragmentPreviews["sys"].Preview != "You are Memoh." || len(queries.previewParams) != 1 || queries.previewParams[0].BotID != testUUID(lifecycleTestBotID) {
		t.Fatalf("previews = %#v (params %#v)", page.FragmentPreviews, queries.previewParams)
	}

	fragCtx := newContextLifecycleGranteeContext(t, "/66666666-6666-6666-6666-666666666666/fragments", true)
	if err := handler.GetSessionContextLifecycleFragments(fragCtx); err != nil {
		t.Fatalf("fragments: %v", err)
	}
	var fragments ContextLifecycleFragmentsResponse
	if err := json.Unmarshal(fragCtx.Response().Writer.(*httptest.ResponseRecorder).Body.Bytes(), &fragments); err != nil {
		t.Fatalf("decode fragments: %v", err)
	}
	if len(fragments.Fragments) != 1 || !fragments.Fragments[0].Available || fragments.Fragments[0].Text != "You are Memoh." || len(queries.textParams) != 1 || queries.textParams[0].BotID != testUUID(lifecycleTestBotID) {
		t.Fatalf("fragments = %#v (params %#v)", fragments.Fragments, queries.textParams)
	}
}
