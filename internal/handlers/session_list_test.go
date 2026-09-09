package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// sessionListQueries records the arguments passed to the paged list queries
// and returns a configurable canned page so tests can assert filter and cursor
// behavior without a real database.
type sessionListQueries struct {
	dbstore.Queries
	bot                sqlc.GetBotByIDRow
	pagedCall          sqlc.ListSessionsByBotPagedParams
	pagedCallCount     int
	pagedRows          []sqlc.ListSessionsByBotPagedRow
	userPagedCall      sqlc.ListSessionsByBotAndCreatedByUserPagedParams
	userPagedCallCount int
	userPagedRows      []sqlc.ListSessionsByBotAndCreatedByUserPagedRow
}

type sessionListEnricher struct {
	called bool
}

func (e *sessionListEnricher) EnrichThreads(_ context.Context, _ string, threads []session.Thread) ([]session.Thread, error) {
	e.called = true
	out := append([]session.Thread(nil), threads...)
	for i := range out {
		out[i].RouteConversationType = "group"
		out[i].RouteMetadata = map[string]any{"conversation_name": "Memoh"}
	}
	return out, nil
}

func (q *sessionListQueries) GetBotByID(_ context.Context, _ pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return q.bot, nil
}

func (q *sessionListQueries) ListSessionsByBotPaged(_ context.Context, arg sqlc.ListSessionsByBotPagedParams) ([]sqlc.ListSessionsByBotPagedRow, error) {
	q.pagedCall = arg
	q.pagedCallCount++
	return q.pagedRows, nil
}

func (q *sessionListQueries) ListSessionsByBotAndCreatedByUserPaged(_ context.Context, arg sqlc.ListSessionsByBotAndCreatedByUserPagedParams) ([]sqlc.ListSessionsByBotAndCreatedByUserPagedRow, error) {
	q.userPagedCall = arg
	q.userPagedCallCount++
	return q.userPagedRows, nil
}

// ListBotUserGrantsForUser feeds the non-admin permission resolver. The
// default response grants `chat`, which is enough for the list endpoint to
// authorize but not enough to read `discuss` sessions — exactly the shape
// needed to exercise the post-filter cursor path.
func (*sessionListQueries) ListBotUserGrantsForUser(_ context.Context, _ sqlc.ListBotUserGrantsForUserParams) ([]sqlc.ListBotUserGrantsForUserRow, error) {
	return []sqlc.ListBotUserGrantsForUserRow{
		{Permissions: []byte(`["chat"]`)},
	}, nil
}

func newListSessionHandler(t *testing.T, queries *sessionListQueries) *SessionHandler {
	t.Helper()
	return NewSessionHandler(
		slog.Default(),
		session.NewService(nil, queries, nil),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("admin"),
	)
}

func callListSessions(handler *SessionHandler, botID, rawQuery string) (*httptest.ResponseRecorder, error) {
	e := echo.New()
	target := "/bots/" + botID + "/sessions"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	ctx := testAuthContext(e, req, rec, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	ctx.SetPath("/bots/:bot_id/sessions")
	ctx.SetParamNames("bot_id")
	ctx.SetParamValues(botID)
	return rec, handler.ListSessions(ctx)
}

func decodeListResponse(t *testing.T, rec *httptest.ResponseRecorder) listSessionsResponse {
	t.Helper()
	var resp listSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}

func TestListSessionsDefaultsToUserFacingTypes(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	if _, err := callListSessions(handler, botID, ""); err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if queries.pagedCallCount != 1 {
		t.Fatalf("ListSessionsByBotPaged called %d times, want 1", queries.pagedCallCount)
	}
	// The default listing filters by stored visibility, not by a type list:
	// the type predicate is widened to every known type so schedule-created
	// sessions marked user-visible surface too.
	got := append([]string(nil), queries.pagedCall.Types...)
	sort.Strings(got)
	want := session.AllSessionTypes()
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default types = %v, want %v", got, want)
	}
	if !queries.pagedCall.UseVisibility {
		t.Fatalf("default listing should filter by visibility")
	}
	if queries.pagedCall.Visibility != string(session.VisibilityUser) {
		t.Fatalf("default visibility = %q, want %q", queries.pagedCall.Visibility, session.VisibilityUser)
	}
	if queries.pagedCall.LimitCount != sessionListDefaultLimit+1 {
		t.Fatalf("default limit = %d, want %d (default+1 has-more probe)", queries.pagedCall.LimitCount, sessionListDefaultLimit+1)
	}
	if queries.pagedCall.UseCursor {
		t.Fatalf("UseCursor should be false when no cursor was supplied")
	}
}

func TestListSessionsPassesExplicitTypesFilter(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	if _, err := callListSessions(handler, botID, "types=chat,discuss"); err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if queries.pagedCall.UseVisibility {
		t.Fatalf("explicit types filter should not add a visibility predicate")
	}
	if got := strings.Join(queries.pagedCall.Types, ","); got != "chat,discuss" {
		t.Fatalf("types filter = %q, want %q", got, "chat,discuss")
	}
}

func TestListSessionsRejectsUnknownType(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	_, err := callListSessions(handler, botID, "types=chat,bogus")
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("ListSessions() error = %v, want HTTP 400", err)
	}
	if queries.pagedCallCount != 0 {
		t.Fatalf("query should not run when types validation fails")
	}
}

func TestListSessionsClampsLimit(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	if _, err := callListSessions(handler, botID, "limit=500"); err == nil {
		t.Fatalf("expected limit out of range to return an error")
	}
	if _, err := callListSessions(handler, botID, "limit=0"); err == nil {
		t.Fatalf("expected limit=0 to return an error")
	}
	if _, err := callListSessions(handler, botID, "limit=not-a-number"); err == nil {
		t.Fatalf("expected non-integer limit to return an error")
	}
	if _, err := callListSessions(handler, botID, "limit=10"); err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if queries.pagedCall.LimitCount != 11 {
		t.Fatalf("limit = %d, want 11 (request+1 has-more probe)", queries.pagedCall.LimitCount)
	}
}

func TestListSessionsCursorRoundTrip(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	rowUpdated := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	rowID := "33333333-3333-3333-3333-333333333333"
	// The handler probes one row past `limit` to detect "has more". Returning
	// two rows for a limit=1 request stands in for "the DB has more rows" and
	// drives the cursor down the round-trip path.
	queries := &sessionListQueries{
		bot: testBotRow(botID, nil),
		pagedRows: []sqlc.ListSessionsByBotPagedRow{
			pagedRow(rowID, rowUpdated),
			pagedRow("44444444-4444-4444-4444-444444444444", rowUpdated.Add(-time.Minute)),
		},
	}
	handler := newListSessionHandler(t, queries)

	// With limit=1 and 2 returned rows, next_cursor must point at the last
	// kept row (the probe row is sliced off).
	rec, err := callListSessions(handler, botID, "limit=1")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	resp := decodeListResponse(t, rec)
	if resp.NextCursor == "" {
		t.Fatalf("expected next_cursor when page is full")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(resp.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[1] != rowID {
		t.Fatalf("cursor payload = %q, want trailing id %s", string(decoded), rowID)
	}

	// Round-trip the cursor back in; the handler must decode it and forward
	// the parsed values to the query layer.
	queries.pagedCall = sqlc.ListSessionsByBotPagedParams{}
	if _, err := callListSessions(handler, botID, "limit=1&cursor="+url.QueryEscape(resp.NextCursor)); err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if !queries.pagedCall.UseCursor {
		t.Fatalf("UseCursor should be true when a cursor was supplied")
	}
	if !queries.pagedCall.CursorUpdatedAt.Time.Equal(rowUpdated) {
		t.Fatalf("CursorUpdatedAt = %v, want %v", queries.pagedCall.CursorUpdatedAt.Time, rowUpdated)
	}
	if queries.pagedCall.CursorID.String() != rowID {
		t.Fatalf("CursorID = %s, want %s", queries.pagedCall.CursorID.String(), rowID)
	}
}

func TestListSessionsOmitsNextCursorOnShortPage(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{
		bot: testBotRow(botID, nil),
		pagedRows: []sqlc.ListSessionsByBotPagedRow{
			pagedRow("33333333-3333-3333-3333-333333333333", time.Now()),
		},
	}
	handler := newListSessionHandler(t, queries)

	rec, err := callListSessions(handler, botID, "limit=10")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	resp := decodeListResponse(t, rec)
	if resp.NextCursor != "" {
		t.Fatalf("next_cursor = %q, want empty when page is not full", resp.NextCursor)
	}
}

// TestListSessionsEmptyResultSerializesAsEmptyArray pins down that an empty
// page renders as `"items": []` rather than `"items": null`. Clients iterate
// the field directly and a null value would crash strict decoders.
func TestListSessionsEmptyResultSerializesAsEmptyArray(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	rec, err := callListSessions(handler, botID, "")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"items":[]`) {
		t.Fatalf("empty page body = %s, want items rendered as []", body)
	}
}

func TestListSessionsPreservesRouteProjection(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{
		bot: testBotRow(botID, nil),
		pagedRows: []sqlc.ListSessionsByBotPagedRow{
			pagedRow("33333333-3333-3333-3333-333333333333", time.Now()),
		},
	}
	handler := newListSessionHandler(t, queries)
	enricher := &sessionListEnricher{}
	handler.SetThreadEnricher(enricher)

	rec, err := callListSessions(handler, botID, "")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	response := decodeListResponse(t, rec)
	if !enricher.called {
		t.Fatal("route projection enricher was not called")
	}
	if len(response.Items) != 1 || response.Items[0].RouteConversationType != "group" {
		t.Fatalf("items = %#v", response.Items)
	}
	if response.Items[0].RouteMetadata["conversation_name"] != "Memoh" {
		t.Fatalf("route metadata = %#v", response.Items[0].RouteMetadata)
	}
}

func TestListSessionsRejectsMalformedCursor(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	_, err := callListSessions(handler, botID, "cursor=not%20base64%21")
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("ListSessions() error = %v, want HTTP 400", err)
	}
}

// TestListSessionsRejectsCursorWithBadUUID guards against a cursor whose
// timestamp portion parses but whose UUID portion does not. Without this
// guard the malformed UUID would surface as a 500 from the service layer
// instead of a 400 from the cursor decoder.
func TestListSessionsRejectsCursorWithBadUUID(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	handler := newListSessionHandler(t, queries)

	cursor := base64.RawURLEncoding.EncodeToString([]byte("2026-06-19T00:00:00Z|not-a-uuid"))
	_, err := callListSessions(handler, botID, "cursor="+url.QueryEscape(cursor))
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Code != http.StatusBadRequest {
		t.Fatalf("ListSessions() error = %v, want HTTP 400", err)
	}
}

// TestListSessionsCursorNotTruncatedByPermissionFilter pins down that
// `next_cursor` reflects the database's resume position rather than the
// post-filter survivorship. If the permission filter drops every row on a
// full DB page, pagination must still surface a cursor — otherwise the
// caller would silently stop walking at the first inaccessible page even
// though older accessible rows remain on disk.
func TestListSessionsCursorNotTruncatedByPermissionFilter(t *testing.T) {
	botID := "11111111-1111-1111-1111-111111111111"
	userID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	rowUpdated := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	keptID := "22222222-2222-2222-2222-222222222222"
	droppedID := "33333333-3333-3333-3333-333333333333"

	queries := &sessionListQueries{bot: testBotRow(botID, nil)}
	// limit=2 page where the first row is the user's chat session and the
	// second row is a discuss session created by the same user that the
	// permission filter discards. The third row is the has-more probe — its
	// id is what the cursor must surface so pagination resumes from the DB
	// position rather than the post-filter survivorship.
	queries.userPagedRows = []sqlc.ListSessionsByBotAndCreatedByUserPagedRow{
		userPagedRow(keptID, userID, session.TypeChat, rowUpdated.Add(2*time.Minute)),
		userPagedRow(droppedID, userID, session.TypeDiscuss, rowUpdated),
		userPagedRow("44444444-4444-4444-4444-444444444444", userID, session.TypeChat, rowUpdated.Add(-time.Minute)),
	}

	handler := NewSessionHandler(
		slog.Default(),
		session.NewService(nil, queries, nil),
		nil,
		bots.NewService(nil, queries),
		newTestAdminAccountService("user"),
	)

	rec, err := callListSessions(handler, botID, "limit=2")
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	resp := decodeListResponse(t, rec)
	if len(resp.Items) != 1 || resp.Items[0].ID != keptID {
		ids := make([]string, len(resp.Items))
		for i, item := range resp.Items {
			ids[i] = item.ID
		}
		t.Fatalf("filtered items = %v, want only %s", ids, keptID)
	}
	if resp.NextCursor == "" {
		t.Fatalf("expected next_cursor when DB returned a full page, even though the filter trimmed it")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(resp.NextCursor)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[1] != droppedID {
		t.Fatalf("cursor payload = %q, want trailing dropped row id %s", string(decoded), droppedID)
	}
}

func userPagedRow(id, userID, typ string, updatedAt time.Time) sqlc.ListSessionsByBotAndCreatedByUserPagedRow {
	return sqlc.ListSessionsByBotAndCreatedByUserPagedRow{
		ID:              testUUID(id),
		BotID:           testUUID("11111111-1111-1111-1111-111111111111"),
		Type:            typ,
		Title:           "test session",
		Metadata:        []byte(`{}`),
		CreatedByUserID: testUUID(userID),
		CreatedAt:       pgtype.Timestamptz{Time: updatedAt, Valid: true},
		UpdatedAt:       pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}
}

func pagedRow(id string, updatedAt time.Time) sqlc.ListSessionsByBotPagedRow {
	return sqlc.ListSessionsByBotPagedRow{
		ID:        testUUID(id),
		BotID:     testUUID("11111111-1111-1111-1111-111111111111"),
		Type:      session.TypeChat,
		Title:     "test session",
		Metadata:  []byte(`{}`),
		CreatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: updatedAt, Valid: true},
	}
}
