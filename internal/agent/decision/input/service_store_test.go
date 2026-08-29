package input

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/decision"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

const (
	storeTestBotID     = "00000000-0000-0000-0000-000000002001"
	storeTestSessionID = "00000000-0000-0000-0000-000000002002"
)

// fakeUserInputQueries is an in-memory stand-in for the user_input_requests
// table. It embeds dbstore.Queries so any method the Service is not expected
// to call panics, and mirrors the SQL semantics in
// db/postgres/queries/user_input.sql that the Service relies on:
//
//   - CreateUserInputRequest upserts on (session_id, tool_call_id). The
//     conflict update only applies while the existing row is pending and
//     unexpired; otherwise the guarded RETURNING matches nothing and the
//     call yields pgx.ErrNoRows, which the Service disambiguates via
//     GetUserInputRequestBySessionToolCall.
//   - Submit/Cancel are guarded by status='pending' AND (expires_at IS NULL
//     OR expires_at > now()) and yield pgx.ErrNoRows when the guard fails.
//   - Pending lookups exclude expired rows.
type fakeUserInputQueries struct {
	dbstore.Queries

	mu        sync.Mutex
	rows      map[string]*sqlc.UserInputRequest // request id -> row
	byCall    map[string]string                 // session|tool_call -> request id
	nextShort map[string]int32                  // session -> last issued short_id
}

func newFakeUserInputQueries() *fakeUserInputQueries {
	return &fakeUserInputQueries{
		rows:      map[string]*sqlc.UserInputRequest{},
		byCall:    map[string]string{},
		nextShort: map[string]int32{},
	}
}

func storeUUIDKey(id pgtype.UUID) string {
	return uuid.UUID(id.Bytes).String()
}

func storeCallKey(sessionID pgtype.UUID, toolCallID string) string {
	return storeUUIDKey(sessionID) + "|" + toolCallID
}

// storeRowIsLivePending mirrors the SQL guard
// status = 'pending' AND (expires_at IS NULL OR expires_at > now()).
func storeRowIsLivePending(row *sqlc.UserInputRequest, now time.Time) bool {
	return row.Status == StatusPending && (!row.ExpiresAt.Valid || row.ExpiresAt.Time.After(now))
}

func storeFenceMatches(left, right pgtype.Int8) bool {
	return left.Valid == right.Valid && (!left.Valid || left.Int64 == right.Int64)
}

func storeRowAllowsDecision(row *sqlc.UserInputRequest, token pgtype.Int8, now time.Time) bool {
	if row.Status != StatusPending {
		return false
	}
	if row.RuntimeFencingToken.Valid {
		return token.Valid && row.RuntimeFencingToken.Int64 == token.Int64
	}
	return !row.ExpiresAt.Valid || row.ExpiresAt.Time.After(now)
}

func (q *fakeUserInputQueries) CreateUserInputRequest(_ context.Context, arg sqlc.CreateUserInputRequestParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	if id, ok := q.byCall[storeCallKey(arg.SessionID, arg.ToolCallID)]; ok {
		row := q.rows[id]
		if !storeRowIsLivePending(row, now) || !storeFenceMatches(row.RuntimeFencingToken, arg.RuntimeFencingToken) ||
			!bytes.Equal(row.InputJson, arg.InputJson) || !bytes.Equal(row.UiPayloadJson, arg.UiPayloadJson) ||
			!bytes.Equal(row.ProviderMetadata, arg.ProviderMetadata) || row.WorkspaceTargetID != arg.WorkspaceTargetID {
			// ON CONFLICT DO UPDATE ... WHERE status='pending' matched no row.
			return sqlc.UserInputRequest{}, pgx.ErrNoRows
		}
		row.RequestedByChannelIdentityID = arg.RequestedByChannelIdentityID
		row.SourcePlatform = arg.SourcePlatform
		row.ReplyTarget = arg.ReplyTarget
		row.ConversationType = arg.ConversationType
		row.ExpiresAt = arg.ExpiresAt
		row.UpdatedAt = pgtype.Timestamptz{Time: now, Valid: true}
		return *row, nil
	}
	sessionKey := storeUUIDKey(arg.SessionID)
	q.nextShort[sessionKey]++
	row := &sqlc.UserInputRequest{
		ID:                           pgtype.UUID{Bytes: [16]byte(uuid.New()), Valid: true},
		BotID:                        arg.BotID,
		SessionID:                    arg.SessionID,
		RouteID:                      arg.RouteID,
		ChannelIdentityID:            arg.ChannelIdentityID,
		WorkspaceTargetID:            arg.WorkspaceTargetID,
		ToolCallID:                   arg.ToolCallID,
		ToolName:                     arg.ToolName,
		ShortID:                      q.nextShort[sessionKey],
		Status:                       StatusPending,
		RuntimeFencingToken:          arg.RuntimeFencingToken,
		InputJson:                    arg.InputJson,
		UiPayloadJson:                arg.UiPayloadJson,
		InteractionJson:              []byte("{}"),
		ResultJson:                   []byte("{}"),
		ProviderMetadata:             arg.ProviderMetadata,
		RequestedByChannelIdentityID: arg.RequestedByChannelIdentityID,
		SourcePlatform:               arg.SourcePlatform,
		ReplyTarget:                  arg.ReplyTarget,
		ConversationType:             arg.ConversationType,
		ExpiresAt:                    arg.ExpiresAt,
		CreatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
		UpdatedAt:                    pgtype.Timestamptz{Time: now, Valid: true},
	}
	q.rows[storeUUIDKey(row.ID)] = row
	q.byCall[storeCallKey(arg.SessionID, arg.ToolCallID)] = storeUUIDKey(row.ID)
	return *row, nil
}

func (q *fakeUserInputQueries) UpdateUserInputInteraction(_ context.Context, arg sqlc.UpdateUserInputInteractionParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	row, ok := q.rows[storeUUIDKey(arg.ID)]
	if !ok || !storeRowIsLivePending(row, now) || row.InteractionRevision != arg.InteractionRevision {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	row.InteractionJson = arg.InteractionJson
	row.InteractionRevision++
	row.UpdatedAt = pgtype.Timestamptz{Time: now, Valid: true}
	return *row, nil
}

func (q *fakeUserInputQueries) GetUserInputRequest(_ context.Context, id pgtype.UUID) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	row, ok := q.rows[storeUUIDKey(id)]
	if !ok {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	return *row, nil
}

func (q *fakeUserInputQueries) GetRespondableUserInputRequest(_ context.Context, arg sqlc.GetRespondableUserInputRequestParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	row, ok := q.rows[storeUUIDKey(arg.ID)]
	if !ok || row.Status != StatusPending {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	if arg.RuntimeFencingToken.Valid {
		if !row.RuntimeFencingToken.Valid || row.RuntimeFencingToken.Int64 != arg.RuntimeFencingToken.Int64 {
			return sqlc.UserInputRequest{}, pgx.ErrNoRows
		}
	} else if row.RuntimeFencingToken.Valid || !storeRowIsLivePending(row, time.Now()) {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	return *row, nil
}

func (q *fakeUserInputQueries) GetUserInputRequestBySessionToolCall(_ context.Context, arg sqlc.GetUserInputRequestBySessionToolCallParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	id, ok := q.byCall[storeCallKey(arg.SessionID, arg.ToolCallID)]
	if !ok {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	return *q.rows[id], nil
}

func (q *fakeUserInputQueries) SubmitUserInputRequest(_ context.Context, arg sqlc.SubmitUserInputRequestParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	row, ok := q.rows[storeUUIDKey(arg.ID)]
	if !ok || !storeRowAllowsDecision(row, arg.RuntimeFencingToken, now) {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	stamp := pgtype.Timestamptz{Time: now, Valid: true}
	row.Status = StatusSubmitted
	row.ResultJson = arg.ResultJson
	row.RespondedByChannelIdentityID = arg.RespondedByChannelIdentityID
	row.RespondedAt = stamp
	row.UpdatedAt = stamp
	return *row, nil
}

func (q *fakeUserInputQueries) CancelUserInputRequest(_ context.Context, arg sqlc.CancelUserInputRequestParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	row, ok := q.rows[storeUUIDKey(arg.ID)]
	if !ok || !storeRowAllowsDecision(row, arg.RuntimeFencingToken, now) {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	stamp := pgtype.Timestamptz{Time: now, Valid: true}
	row.Status = StatusCanceled
	row.ResultJson = arg.ResultJson
	row.RespondedByChannelIdentityID = arg.RespondedByChannelIdentityID
	row.RespondedAt = stamp
	row.CanceledAt = stamp
	row.UpdatedAt = stamp
	return *row, nil
}

func (q *fakeUserInputQueries) GetLatestPendingUserInputBySession(_ context.Context, arg sqlc.GetLatestPendingUserInputBySessionParams) (sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	var latest *sqlc.UserInputRequest
	for _, row := range q.rows {
		if row.BotID != arg.BotID || row.SessionID != arg.SessionID || !storeRowIsLivePending(row, now) {
			continue
		}
		if latest == nil || row.ShortID > latest.ShortID {
			latest = row
		}
	}
	if latest == nil {
		return sqlc.UserInputRequest{}, pgx.ErrNoRows
	}
	return *latest, nil
}

func (q *fakeUserInputQueries) ListPendingUserInputsBySession(_ context.Context, arg sqlc.ListPendingUserInputsBySessionParams) ([]sqlc.UserInputRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	var rows []sqlc.UserInputRequest
	for _, row := range q.rows {
		if row.BotID == arg.BotID && row.SessionID == arg.SessionID && storeRowIsLivePending(row, now) {
			rows = append(rows, *row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ShortID < rows[j].ShortID })
	return rows, nil
}

func newStoreUserInputService(t *testing.T) *Service {
	t.Helper()
	return NewService(slog.New(slog.DiscardHandler), newFakeUserInputQueries())
}

func TestFakeUserInputQueriesMatchesFencedSQLGuards(t *testing.T) {
	t.Parallel()

	queries := newFakeUserInputQueries()
	botID := pgtype.UUID{Bytes: [16]byte(uuid.MustParse(storeTestBotID)), Valid: true}
	sessionID := pgtype.UUID{Bytes: [16]byte(uuid.MustParse(storeTestSessionID)), Valid: true}
	create := func(callID string, token int64) sqlc.UserInputRequest {
		t.Helper()
		row, err := queries.CreateUserInputRequest(context.Background(), sqlc.CreateUserInputRequestParams{
			BotID:               botID,
			SessionID:           sessionID,
			WorkspaceTargetID:   "workspace-a",
			ToolCallID:          callID,
			ToolName:            ToolNameAskUser,
			RuntimeFencingToken: pgtype.Int8{Int64: token, Valid: true},
			InputJson:           []byte(`{"questions":[]}`),
			UiPayloadJson:       []byte(`{"questions":[]}`),
			ProviderMetadata:    []byte(`{}`),
		})
		if err != nil {
			t.Fatalf("create fenced fake row: %v", err)
		}
		return row
	}

	row := create("same-fence", 7)
	replayExpiry := time.Now().Add(time.Hour).Round(time.Microsecond)
	requesterID := pgtype.UUID{Bytes: [16]byte(uuid.New()), Valid: true}
	replay, err := queries.CreateUserInputRequest(context.Background(), sqlc.CreateUserInputRequestParams{
		BotID: botID, SessionID: sessionID, WorkspaceTargetID: "workspace-a",
		ToolCallID: "same-fence", ToolName: ToolNameAskUser,
		RuntimeFencingToken: pgtype.Int8{Int64: 7, Valid: true},
		InputJson:           []byte(`{"questions":[]}`), UiPayloadJson: []byte(`{"questions":[]}`), ProviderMetadata: []byte(`{}`),
		RequestedByChannelIdentityID: requesterID, SourcePlatform: "web", ReplyTarget: "reply-a",
		ConversationType: "direct", ExpiresAt: pgtype.Timestamptz{Time: replayExpiry, Valid: true},
	})
	if err != nil {
		t.Fatalf("same-fence replay: %v", err)
	}
	if replay.ID != row.ID {
		t.Fatalf("same-fence replay id = %s, want %s", replay.ID, row.ID)
	}
	if replay.RequestedByChannelIdentityID != requesterID || replay.SourcePlatform != "web" ||
		replay.ReplyTarget != "reply-a" || replay.ConversationType != "direct" ||
		!replay.ExpiresAt.Valid || !replay.ExpiresAt.Time.Equal(replayExpiry) {
		t.Fatalf("same-fence replay did not update mutable routing fields: %#v", replay)
	}
	if _, err := queries.CreateUserInputRequest(context.Background(), sqlc.CreateUserInputRequestParams{
		BotID:               botID,
		SessionID:           sessionID,
		WorkspaceTargetID:   "workspace-a",
		ToolCallID:          "same-fence",
		ToolName:            ToolNameAskUser,
		RuntimeFencingToken: pgtype.Int8{Int64: 8, Valid: true},
		InputJson:           []byte(`{"questions":[]}`),
		UiPayloadJson:       []byte(`{"questions":[]}`),
		ProviderMetadata:    []byte(`{}`),
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale create error = %v, want pgx.ErrNoRows", err)
	}
	queries.mu.Lock()
	queries.rows[storeUUIDKey(row.ID)].ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	queries.mu.Unlock()
	if _, err := queries.SubmitUserInputRequest(context.Background(), sqlc.SubmitUserInputRequestParams{
		ID: row.ID, ResultJson: []byte(`{"status":"submitted"}`),
		RuntimeFencingToken: pgtype.Int8{Int64: 8, Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale submit error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := queries.SubmitUserInputRequest(context.Background(), sqlc.SubmitUserInputRequestParams{
		ID: row.ID, ResultJson: []byte(`{"status":"submitted"}`),
		RuntimeFencingToken: pgtype.Int8{Int64: 7, Valid: true},
	}); err != nil {
		t.Fatalf("matching-fence expired submit: %v", err)
	}

	cancelRow := create("matching-cancel", 9)
	queries.mu.Lock()
	queries.rows[storeUUIDKey(cancelRow.ID)].ExpiresAt = pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true}
	queries.mu.Unlock()
	if _, err := queries.CancelUserInputRequest(context.Background(), sqlc.CancelUserInputRequestParams{
		ID: cancelRow.ID, ResultJson: []byte(`{"status":"canceled"}`),
		RuntimeFencingToken: pgtype.Int8{Int64: 10, Valid: true},
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale cancel error = %v, want pgx.ErrNoRows", err)
	}
	if _, err := queries.CancelUserInputRequest(context.Background(), sqlc.CancelUserInputRequestParams{
		ID: cancelRow.ID, ResultJson: []byte(`{"status":"canceled"}`),
		RuntimeFencingToken: pgtype.Int8{Int64: 9, Valid: true},
	}); err != nil {
		t.Fatalf("matching-fence expired cancel: %v", err)
	}
}

func createStorePending(t *testing.T, svc *Service, expiresAt *time.Time, callID string) Request {
	t.Helper()
	req, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      storeTestBotID,
		SessionID:  storeTestSessionID,
		ToolCallID: callID,
		Input: map[string]any{
			"questions": []any{
				map[string]any{
					"text": "Which plan?",
					"kind": QuestionKindSingleSelect,
					"options": []any{
						map[string]any{"label": "Plan A"},
						map[string]any{"label": "Plan B"},
					},
				},
			},
		},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	return req
}

func TestServiceSubmitLifecycleNotifiesWaiter(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req := createStorePending(t, svc, nil, "call-1")
	if req.Status != StatusPending || len(req.UIPayload.Questions) != 1 {
		t.Fatalf("unexpected pending request: %#v", req)
	}
	if req.UIPayload.Questions[0].ID != "q1" || req.UIPayload.Questions[0].Options[0].ID != "q1.o1" {
		t.Fatalf("unexpected normalized payload: %#v", req.UIPayload)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), decision.DefaultFallbackInterval/5)
	defer cancel()
	waited := make(chan Request, 1)
	waitErr := make(chan error, 1)
	go func() {
		resolved, err := svc.WaitForResponse(waitCtx, req.ID)
		if err != nil {
			waitErr <- err
			return
		}
		waited <- resolved
	}()

	// The waiter registry must reflect the blocked WaitForResponse: this is
	// the signal responders use to refuse answers nobody would consume.
	deadline := time.Now().Add(2 * time.Second)
	for !svc.HasWaiter(req.ID) {
		if time.Now().After(deadline) {
			t.Fatal("waiter never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	submitted, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o2"}}},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.Status != StatusSubmitted {
		t.Fatalf("submitted status = %q", submitted.Status)
	}
	answers, ok := submitted.Result["answers"].([]any)
	if !ok || len(answers) != 1 {
		t.Fatalf("unexpected result answers: %#v", submitted.Result)
	}
	metadata := DeferredMetadata(submitted)
	publicAnswers, ok := metadata["answers"].([]UIAnswer)
	if !ok || len(publicAnswers) != 1 || publicAnswers[0].Selected[0].Label != "Plan B" {
		t.Fatalf("unexpected public answers: %#v", metadata["answers"])
	}
	if _, exposed := metadata["instruction"]; exposed {
		t.Fatalf("model instruction leaked into public metadata: %#v", metadata)
	}

	// The wait context is shorter than the fallback ticker, so a timely return
	// here proves the Submit broadcast woke the waiter, not the polling safety
	// net.
	select {
	case resolved := <-waited:
		if resolved.Status != StatusSubmitted {
			t.Fatalf("waited status = %q", resolved.Status)
		}
	case err := <-waitErr:
		t.Fatalf("wait for response: %v", err)
	}
	if svc.HasWaiter(req.ID) {
		t.Fatal("waiter must unregister after the wait ends")
	}

	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("second submit error = %v, want ErrAlreadyDecided", err)
	}
}

func TestServiceWaitForRegisteredResponseUsesExistingWaiter(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req := createStorePending(t, svc, nil, "call-1")
	release := svc.RegisterWaiter(req.ID)
	defer release()

	waitCtx, cancel := context.WithTimeout(context.Background(), decision.DefaultFallbackInterval/5)
	defer cancel()
	waited := make(chan Request, 1)
	waitErr := make(chan error, 1)
	go func() {
		resolved, err := svc.WaitForRegisteredResponse(waitCtx, req.ID)
		if err != nil {
			waitErr <- err
			return
		}
		waited <- resolved
	}()

	if !svc.HasWaiter(req.ID) {
		t.Fatal("registered waiter was lost")
	}

	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	select {
	case resolved := <-waited:
		if resolved.Status != StatusSubmitted {
			t.Fatalf("waited status = %q", resolved.Status)
		}
	case err := <-waitErr:
		t.Fatalf("wait for registered response: %v", err)
	case <-waitCtx.Done():
		t.Fatal("registered waiter was not notified")
	}
}

func TestServiceCancelNotifiesWaiter(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req := createStorePending(t, svc, nil, "call-1")

	waitCtx, cancel := context.WithTimeout(context.Background(), decision.DefaultFallbackInterval/5)
	defer cancel()
	waited := make(chan Request, 1)
	waitErr := make(chan error, 1)
	go func() {
		resolved, err := svc.WaitForResponse(waitCtx, req.ID)
		if err != nil {
			waitErr <- err
			return
		}
		waited <- resolved
	}()

	deadline := time.Now().Add(2 * time.Second)
	for !svc.HasWaiter(req.ID) {
		if time.Now().After(deadline) {
			t.Fatal("waiter never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}

	canceled, err := svc.Cancel(context.Background(), CancelInput{RequestID: req.ID, Reason: "user input timed out"})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if canceled.Status != StatusCanceled || canceled.Result["reason"] != "user input timed out" {
		t.Fatalf("unexpected canceled request: %#v", canceled)
	}

	select {
	case resolved := <-waited:
		if resolved.Status != StatusCanceled {
			t.Fatalf("waited status = %q", resolved.Status)
		}
	case err := <-waitErr:
		t.Fatalf("wait for response: %v", err)
	case <-waitCtx.Done():
		t.Fatal("waiter was not notified before the fallback ticker")
	}
}

func TestServiceCreatePendingIsIdempotentPerToolCall(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	first := createStorePending(t, svc, nil, "call-1")
	second, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      storeTestBotID,
		SessionID:  storeTestSessionID,
		ToolCallID: "call-1",
		Input: map[string]any{
			"questions": []any{
				map[string]any{
					"text": "Which plan?", "kind": QuestionKindSingleSelect,
					"options": []any{map[string]any{"label": "Plan A"}, map[string]any{"label": "Plan B"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("create duplicate pending: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate tool call created request %s, want existing %s", second.ID, first.ID)
	}
	if second.ShortID != first.ShortID {
		t.Fatalf("duplicate tool call short_id = %d, want %d", second.ShortID, first.ShortID)
	}
	if _, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      storeTestBotID,
		SessionID:  storeTestSessionID,
		ToolCallID: "call-1",
		Input: map[string]any{
			"questions": []any{map[string]any{"text": "Changed question?", "kind": QuestionKindText}},
		},
	}); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("changed duplicate payload error = %v, want ErrAlreadyDecided", err)
	}

	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: second.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); err != nil {
		t.Fatalf("submit duplicate row: %v", err)
	}
	_, err = svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      storeTestBotID,
		SessionID:  storeTestSessionID,
		ToolCallID: "call-1",
		Input: map[string]any{
			"questions": []any{
				map[string]any{"text": "Third question?", "kind": QuestionKindText},
			},
		},
	})
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("create duplicate after submit error = %v, want ErrAlreadyDecided", err)
	}
}

func TestServiceWaitPrefersResolutionOverContextCancel(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req := createStorePending(t, svc, nil, "call-1")

	waitCtx, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	waited := make(chan Request, 1)
	waitErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		resolved, err := svc.WaitForResponse(waitCtx, req.ID)
		if err != nil {
			waitErr <- err
			return
		}
		waited <- resolved
	}()
	<-started

	// Submit commits (and buffers the notification) before the context is
	// canceled; even if ctx.Done wins the select, the waiter must deliver the
	// answer, never ctx.Err().
	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	cancelWait()

	select {
	case resolved := <-waited:
		if resolved.Status != StatusSubmitted {
			t.Fatalf("waited status = %q", resolved.Status)
		}
	case err := <-waitErr:
		t.Fatalf("wait returned error despite committed answer: %v", err)
	case <-time.After(4 * time.Second):
		t.Fatal("waiter did not return")
	}
}

func TestServiceCancelAfterDecisionReturnsAlreadyDecided(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req := createStorePending(t, svc, nil, "call-1")

	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	// The guarded update matches no row; the error must disambiguate to
	// ErrAlreadyDecided instead of pretending the request does not exist.
	if _, err := svc.Cancel(context.Background(), CancelInput{RequestID: req.ID, Reason: "late"}); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("cancel after decision error = %v, want ErrAlreadyDecided", err)
	}
}

func TestServiceExpiredRequestIsClosed(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	expired := time.Now().Add(-time.Minute)
	req := createStorePending(t, svc, &expired, "call-1")

	got, err := svc.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusExpired {
		t.Fatalf("status = %q, want %q", got.Status, StatusExpired)
	}

	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: req.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("submit expired error = %v, want ErrAlreadyDecided", err)
	}

	if _, err := svc.ResolveTarget(context.Background(), ResolveInput{
		BotID:     storeTestBotID,
		SessionID: storeTestSessionID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve target error = %v, want ErrNotFound", err)
	}
	if _, err := svc.ResolveTarget(context.Background(), ResolveInput{
		BotID: storeTestBotID, SessionID: storeTestSessionID, ExplicitID: req.ID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("resolve expired explicit target error = %v, want ErrNotFound", err)
	}

	pending, err := svc.ListPendingBySession(context.Background(), storeTestBotID, storeTestSessionID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %d, want 0", len(pending))
	}

	// A future expiry must keep the request answerable.
	future := time.Now().Add(time.Hour)
	live := createStorePending(t, svc, &future, "call-2")
	gotLive, err := svc.Get(context.Background(), live.ID)
	if err != nil {
		t.Fatalf("get live: %v", err)
	}
	if gotLive.Status != StatusPending {
		t.Fatalf("live status = %q, want pending", gotLive.Status)
	}
	if _, err := svc.Submit(context.Background(), SubmitInput{
		RequestID: live.ID,
		Answers:   []QuestionAnswer{{QuestionID: "q1", OptionIDs: []string{"q1.o1"}}},
	}); err != nil {
		t.Fatalf("submit live: %v", err)
	}
}

func TestServiceResolveTargetKeepsClaimedRequestRespondableAfterExpiry(t *testing.T) {
	t.Parallel()

	queries := newFakeUserInputQueries()
	svc := NewService(slog.New(slog.DiscardHandler), queries)
	expired := time.Now().Add(-time.Minute)
	req := createStorePending(t, svc, &expired, "call-claimed-expired")
	queries.mu.Lock()
	row := queries.rows[req.ID]
	row.RuntimeFencingToken = pgtype.Int8{Int64: 7, Valid: true}
	queries.mu.Unlock()
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: storeTestBotID, SessionID: storeTestSessionID, Token: 7,
	})

	resolved, err := svc.ResolveTarget(ctx, ResolveInput{
		BotID: storeTestBotID, SessionID: storeTestSessionID, ExplicitID: req.ID,
	})
	if err != nil {
		t.Fatalf("resolve claimed expired request: %v", err)
	}
	if resolved.ID != req.ID {
		t.Fatalf("resolved request = %q, want %q", resolved.ID, req.ID)
	}
	if resolved.Status != StatusPending {
		t.Fatalf("resolved status = %q, want pending", resolved.Status)
	}
	if _, err := svc.ResolveTarget(context.Background(), ResolveInput{
		BotID: storeTestBotID, SessionID: storeTestSessionID, ExplicitID: req.ID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unfenced resolve error = %v, want ErrNotFound", err)
	}
}

func TestServiceAdvanceTextPersistsWizardState(t *testing.T) {
	t.Parallel()

	svc := newStoreUserInputService(t)
	req, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      storeTestBotID,
		SessionID:  storeTestSessionID,
		ToolCallID: "plain-text-wizard",
		Input: map[string]any{"questions": []any{
			map[string]any{"text": "Plan?", "kind": QuestionKindSingleSelect, "options": []any{
				map[string]any{"label": "A"}, map[string]any{"label": "B"},
			}},
			map[string]any{"text": "Topics?", "kind": QuestionKindMultiSelect, "options": []any{
				map[string]any{"label": "Go"}, map[string]any{"label": "Rust"}, map[string]any{"label": "Vue"},
			}},
			map[string]any{"text": "Notes?", "kind": QuestionKindText},
			map[string]any{"text": "Ship?", "kind": QuestionKindSingleSelect, "options": []any{
				map[string]any{"label": "Yes"}, map[string]any{"label": "No"},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("create wizard: %v", err)
	}

	advance := func(text string) AdvanceTextResult {
		t.Helper()
		result, err := svc.AdvanceText(context.Background(), AdvanceTextInput{
			BotID: storeTestBotID, SessionID: storeTestSessionID, ExplicitID: req.ID, Text: text,
		})
		if err != nil {
			t.Fatalf("advance %q: %v", text, err)
		}
		if !result.Handled {
			t.Fatalf("advance %q was not handled", text)
		}
		return result
	}

	if got := advance("2"); got.Request.Interaction.QuestionIndex != 1 || got.Request.Interaction.Answers[0].OptionIDs[0] != "q1.o2" {
		t.Fatalf("single-select state = %#v", got.Request.Interaction)
	}
	if got := advance("1, 3"); got.Request.Interaction.QuestionIndex != 2 || len(got.Request.Interaction.Answers[1].OptionIDs) != 2 {
		t.Fatalf("multi-select state = %#v", got.Request.Interaction)
	}
	if got := advance("research"); got.Request.Interaction.QuestionIndex != 3 {
		t.Fatalf("text state = %#v", got.Request.Interaction)
	}
	if got := advance("back"); got.Request.Interaction.QuestionIndex != 2 {
		t.Fatalf("back state = %#v", got.Request.Interaction)
	}
	_ = advance("updated notes")
	invalid := advance("maybe")
	if !invalid.Invalid || invalid.Request.Interaction.QuestionIndex != 3 || invalid.Request.Interaction.Completed {
		t.Fatalf("invalid state = %#v", invalid)
	}
	completed := advance("跳过")
	if !completed.Request.Interaction.Completed || completed.Request.Interaction.Answers[3].Skipped != true {
		t.Fatalf("completed state = %#v", completed.Request.Interaction)
	}

	reloaded, err := svc.Get(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("reload wizard: %v", err)
	}
	if !reloaded.Interaction.Completed || reloaded.InteractionRevision != 6 {
		t.Fatalf("persisted state = %#v revision=%d", reloaded.Interaction, reloaded.InteractionRevision)
	}
	if _, err := svc.Submit(context.Background(), SubmitInput{RequestID: req.ID, Answers: reloaded.Interaction.Answers}); err != nil {
		t.Fatalf("submit persisted answers: %v", err)
	}
}
