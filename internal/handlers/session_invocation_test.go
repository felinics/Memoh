package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/server"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

const (
	invocationTestBotID      = "11111111-1111-1111-1111-111111111111"
	invocationTestSessionA   = "22222222-2222-2222-2222-222222222222"
	invocationTestSessionB   = "33333333-3333-3333-3333-333333333333"
	invocationTestOwnerID    = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	invocationTestOtherUser  = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	invocationTestInvocation = "inv-lost-ack"
)

// sessionInvocationQueries serves the bot, the grants, and a set of sessions
// keyed by id, so tests can hold two sessions of the same bot side by side.
type sessionInvocationQueries struct {
	dbstore.Queries
	bot         sqlc.GetBotByIDRow
	sessions    map[string]sqlc.BotSession
	permissions []byte
}

func (q *sessionInvocationQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *sessionInvocationQueries) GetSessionByID(_ context.Context, id pgtype.UUID) (sqlc.BotSession, error) {
	row, ok := q.sessions[id.String()]
	if !ok {
		return sqlc.BotSession{}, pgx.ErrNoRows
	}
	return row, nil
}

func (q *sessionInvocationQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	if q.permissions == nil {
		return []sqlc.ListBotUserGrantsForUserRow{{Permissions: []byte(`["chat"]`)}}, nil
	}
	return []sqlc.ListBotUserGrantsForUserRow{{Permissions: q.permissions}}, nil
}

func invocationTestSession(sessionID, createdBy string) sqlc.BotSession {
	return sqlc.BotSession{
		ID:              testUUID(sessionID),
		BotID:           testUUID(invocationTestBotID),
		Type:            session.TypeChat,
		SessionMode:     session.TypeChat,
		RuntimeType:     session.RuntimeModel,
		Metadata:        testJSON(map[string]any{}),
		CreatedByUserID: testUUID(createdBy),
	}
}

func newInvocationTestQueries(sessions ...sqlc.BotSession) *sessionInvocationQueries {
	q := &sessionInvocationQueries{bot: testBotRow(invocationTestBotID, map[string]any{}), sessions: map[string]sqlc.BotSession{}}
	for _, row := range sessions {
		q.sessions[row.ID.String()] = row
	}
	return q
}

// recordingInvocationLookup wraps the shared in-memory ledger and records the
// scope every lookup was asked for.
type recordingInvocationLookup struct {
	store *sessionledger.Store
	err   error
	calls []string
}

func (r *recordingInvocationLookup) GetByInvocation(ctx context.Context, sessionID, invocationID string) (ledger.Run, error) {
	r.calls = append(r.calls, sessionID+"/"+invocationID)
	if r.err != nil {
		return ledger.Run{}, r.err
	}
	return r.store.GetByInvocation(ctx, sessionID, invocationID)
}

func newInvocationTestHandler(queries *sessionInvocationQueries, lookup sessionInvocationLookup) *SessionHandler {
	handler := NewSessionHandler(
		slog.Default(),
		newThreadServiceForTest(queries),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)
	handler.SetInvocationLookup(lookup)
	return handler
}

func admitInvocationTestRun(t *testing.T, store *sessionledger.Store, runID, sessionID, invocationID string) ledger.Run {
	t.Helper()
	run, created, err := store.Admit(context.Background(), ledger.AdmitParams{
		RunID:            runID,
		BotID:            invocationTestBotID,
		SessionID:        sessionID,
		InvocationID:     invocationID,
		TurnID:           "turn-" + runID,
		Input:            []byte(`{"text":"secret payload"}`),
		InputFingerprint: "fingerprint-" + runID,
	})
	if err != nil || !created {
		t.Fatalf("Admit() = created %v, err %v", created, err)
	}
	return run
}

func callGetSessionInvocation(handler *SessionHandler, sessionID, invocationID, userID string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/bots/"+invocationTestBotID+"/sessions/"+sessionID+"/invocations/"+invocationID, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, userID)
	ctx.SetPath("/bots/:bot_id/sessions/:session_id/invocations/:invocation_id")
	ctx.SetParamNames("bot_id", "session_id", "invocation_id")
	ctx.SetParamValues(invocationTestBotID, sessionID, invocationID)
	return rec, handler.GetSessionInvocation(ctx)
}

func decodeInvocationBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return body
}

func sortedKeys(body map[string]any) string {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func TestGetSessionInvocationReportsAdmittedRun(t *testing.T) {
	store := sessionledger.New()
	run := admitInvocationTestRun(t, store, "run-a", invocationTestSessionA, invocationTestInvocation)
	lookup := &recordingInvocationLookup{store: store}
	handler := newInvocationTestHandler(newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID)), lookup)

	rec, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	if err != nil {
		t.Fatalf("GetSessionInvocation() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := decodeInvocationBody(t, rec)
	if got, want := sortedKeys(body), "found,invocation_id,run_id,session_id,state,turn_id,turn_position"; got != want {
		t.Fatalf("response keys = %s, want exactly %s", got, want)
	}
	if body["found"] != true || body["invocation_id"] != invocationTestInvocation || body["session_id"] != invocationTestSessionA {
		t.Fatalf("identity = %#v", body)
	}
	if body["run_id"] != run.RunID || body["turn_id"] != run.TurnID || body["turn_position"] != float64(run.TurnPosition) {
		t.Fatalf("run identity = %#v, want run %s turn %s position %d", body, run.RunID, run.TurnID, run.TurnPosition)
	}
	if body["state"] != string(ledger.StateAccepted) {
		t.Fatalf("state = %v, want %q", body["state"], ledger.StateAccepted)
	}
	if strings.Contains(rec.Body.String(), "secret payload") || strings.Contains(rec.Body.String(), "fingerprint") {
		t.Fatalf("response leaked run input: %s", rec.Body.String())
	}
}

func TestGetSessionInvocationReportsLedgerStateAsIs(t *testing.T) {
	store := sessionledger.New()
	run := admitInvocationTestRun(t, store, "run-a", invocationTestSessionA, invocationTestInvocation)
	store.Runs[run.RunID].State = ledger.StateLost
	handler := newInvocationTestHandler(newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID)), store)

	rec, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	if err != nil {
		t.Fatalf("GetSessionInvocation() error = %v", err)
	}
	if body := decodeInvocationBody(t, rec); body["state"] != string(ledger.StateLost) {
		t.Fatalf("state = %v, want %q", body["state"], ledger.StateLost)
	}
}

func TestGetSessionInvocationUnknownInvocationIsNotAnError(t *testing.T) {
	lookup := &recordingInvocationLookup{store: sessionledger.New()}
	handler := newInvocationTestHandler(newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID)), lookup)

	rec, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	if err != nil {
		t.Fatalf("GetSessionInvocation() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := decodeInvocationBody(t, rec)
	if got, want := sortedKeys(body), "found,invocation_id,session_id"; got != want {
		t.Fatalf("response keys = %s, want exactly %s", got, want)
	}
	if body["found"] != false || body["invocation_id"] != invocationTestInvocation || body["session_id"] != invocationTestSessionA {
		t.Fatalf("body = %#v", body)
	}
}

func TestGetSessionInvocationDoesNotResolveAcrossSessions(t *testing.T) {
	store := sessionledger.New()
	// The same invocation id was admitted into a sibling session of the same
	// bot, which the caller can also read.
	admitInvocationTestRun(t, store, "run-b", invocationTestSessionB, invocationTestInvocation)
	lookup := &recordingInvocationLookup{store: store}
	handler := newInvocationTestHandler(newInvocationTestQueries(
		invocationTestSession(invocationTestSessionA, invocationTestOwnerID),
		invocationTestSession(invocationTestSessionB, invocationTestOwnerID),
	), lookup)

	rec, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	if err != nil {
		t.Fatalf("GetSessionInvocation() error = %v", err)
	}
	body := decodeInvocationBody(t, rec)
	if body["found"] != false || body["run_id"] != nil {
		t.Fatalf("session A resolved session B's run: %#v", body)
	}
	if len(lookup.calls) != 1 || lookup.calls[0] != invocationTestSessionA+"/"+invocationTestInvocation {
		t.Fatalf("ledger lookups = %v, want one scoped to session A", lookup.calls)
	}
}

// foreignRunLookup ignores the session scope, standing in for a broken store.
type foreignRunLookup struct{ run ledger.Run }

func (f foreignRunLookup) GetByInvocation(context.Context, string, string) (ledger.Run, error) {
	return f.run, nil
}

func TestGetSessionInvocationDropsRunFromAnotherSession(t *testing.T) {
	handler := newInvocationTestHandler(
		newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID)),
		foreignRunLookup{run: ledger.Run{RunID: "run-b", SessionID: invocationTestSessionB, InvocationID: invocationTestInvocation, State: ledger.StateRunning}},
	)

	rec, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	if err != nil {
		t.Fatalf("GetSessionInvocation() error = %v", err)
	}
	if body := decodeInvocationBody(t, rec); body["found"] != false || body["run_id"] != nil {
		t.Fatalf("foreign run leaked: %#v", body)
	}
}

func TestGetSessionInvocationRequiresSessionReadAccess(t *testing.T) {
	cases := []struct {
		name        string
		sessionID   string
		createdBy   string
		permissions []byte
		wantStatus  int
	}{
		// A chat grant reads only the caller's own sessions; another member's
		// session is reported exactly as GetSession reports it.
		{name: "other user's session", sessionID: invocationTestSessionA, createdBy: invocationTestOtherUser, wantStatus: http.StatusNotFound},
		{name: "no bot grant", sessionID: invocationTestSessionA, createdBy: invocationTestOwnerID, permissions: []byte(`[]`), wantStatus: http.StatusForbidden},
		{name: "unknown session", sessionID: invocationTestSessionB, createdBy: invocationTestOwnerID, wantStatus: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := sessionledger.New()
			admitInvocationTestRun(t, store, "run-a", invocationTestSessionA, invocationTestInvocation)
			lookup := &recordingInvocationLookup{store: store}
			queries := newInvocationTestQueries(invocationTestSession(invocationTestSessionA, tc.createdBy))
			queries.permissions = tc.permissions
			handler := newInvocationTestHandler(queries, lookup)

			_, err := callGetSessionInvocation(handler, tc.sessionID, invocationTestInvocation, invocationTestOwnerID)
			if got := invocationTestStatus(err); got != tc.wantStatus {
				t.Fatalf("GetSessionInvocation() error = %v, want HTTP %d", err, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusNotFound && apperror.CodeOf(err) != apperror.CodeSessionNotFound {
				t.Fatalf("GetSessionInvocation() code = %q, want session.not_found", apperror.CodeOf(err))
			}
			_, getErr := callGetSession(handler, invocationTestBotID, tc.sessionID, invocationTestOwnerID)
			if invocationTestStatus(getErr) != tc.wantStatus || apperror.CodeOf(getErr) != apperror.CodeOf(err) {
				t.Fatalf("GetSession() error = %v, want the same error as the invocation lookup (%v)", getErr, err)
			}
			if len(lookup.calls) != 0 {
				t.Fatalf("ledger consulted before authorization: %v", lookup.calls)
			}
		})
	}
}

func TestGetSessionInvocationLedgerFailureIsServerError(t *testing.T) {
	lookup := &recordingInvocationLookup{store: sessionledger.New(), err: errors.New("database unavailable")}
	handler := newInvocationTestHandler(newInvocationTestQueries(invocationTestSession(invocationTestSessionA, invocationTestOwnerID)), lookup)

	_, err := callGetSessionInvocation(handler, invocationTestSessionA, invocationTestInvocation, invocationTestOwnerID)
	problem, ok := server.ProblemFrom(err, "invocation-request")
	if !ok || problem.Status != http.StatusInternalServerError || problem.Code != string(apperror.CodeInternal) {
		t.Fatalf("GetSessionInvocation() error = %v, want internal Problem with HTTP 500", err)
	}
	if !errors.Is(apperror.CauseOf(err), lookup.err) {
		t.Fatalf("ledger failure lost its diagnostic cause: %v", err)
	}
	public, marshalErr := json.Marshal(problem)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(public), "database unavailable") {
		t.Fatalf("internal error leaked to the client: %s", public)
	}
}

// invocationTestStatus returns the HTTP status of a transport error or of a
// public error, and 0 for any other error.
func invocationTestStatus(err error) int {
	var httpErr *echo.HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Code
	}
	def, _ := apperror.Lookup(apperror.CodeOf(err))
	return def.HTTPStatus
}
