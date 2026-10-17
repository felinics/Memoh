package message

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// Keep the runtime fence prefix: the durable runtime CI job runs this filter.
func TestPostgresRuntimeFenceReplacementSelectsFirstStepAndCopiesRequest(t *testing.T) {
	f := newBoundReplacementFixture(t, PersistInput{
		Content:     []byte(`{"role":"user","content":"original request"}`),
		Metadata:    map[string]any{"hook_output": "already transformed"},
		DisplayText: "original display", SessionMode: "chat", RuntimeType: "native",
		ExternalMessageID: "source-request", SourceReplyToMessageID: "source-parent",
		Assets: []AssetRef{{
			ContentHash: "sha256:retry-request", Role: "attachment", Ordinal: 2,
			Mime: "image/png", SizeBytes: 42, StorageKey: "images/request.png", Name: "request.png",
			Metadata: map[string]any{"width": float64(16)},
		}},
	})
	first, err := f.service.PersistAgentStep(f.owner, AgentStep{
		RunID: f.runID, Replacement: &f.replacement,
		Messages: []PersistInput{f.input("assistant", `{"role":"assistant","content":"first step"}`, f.user.ID)},
	})
	if err != nil || len(first) != 2 {
		t.Fatalf("first replacement step = (%#v, %v), want copied request and assistant", first, err)
	}
	cloned := first[0]
	if cloned.ID == f.user.ID || cloned.Role != "user" || !bytes.Equal(cloned.Content, f.user.Content) ||
		cloned.DisplayContent != f.user.DisplayContent || !reflect.DeepEqual(cloned.Metadata, f.user.Metadata) ||
		cloned.SenderUserID != f.user.SenderUserID || cloned.SenderChannelIdentityID != f.user.SenderChannelIdentityID ||
		cloned.SessionMode != f.user.SessionMode || cloned.RuntimeType != f.user.RuntimeType ||
		cloned.ExternalMessageID != f.user.ExternalMessageID || cloned.SourceReplyToMessageID != f.user.SourceReplyToMessageID ||
		!reflect.DeepEqual(cloned.Assets, f.user.Assets) {
		t.Fatalf("retry request was not copied faithfully: original=%#v clone=%#v", f.user, cloned)
	}
	if cloned.TurnID != f.replacement.ReplacementTurnID || cloned.TurnPosition == nil || *cloned.TurnPosition != *f.replacement.ReplacementTurnPosition {
		t.Fatalf("copied request omitted admitted turn identity: %#v", cloned)
	}
	assertBoundReplacementHistory(t, f, cloned.ID, first[1].ID)
	assertBoundReplacementRow(t, f, f.user.ID, f.oldTurn.ID, 1, false)
	assertBoundReplacementRow(t, f, f.assistant.ID, f.oldTurn.ID, 2, false)
	assertBoundReplacementRow(t, f, cloned.ID, f.replacement.ReplacementTurnID, 1, true)
	assertBoundReplacementRow(t, f, first[1].ID, f.replacement.ReplacementTurnID, 2, true)

	var copiedLinks, originalLinks int
	if err := f.pool.QueryRow(f.owner, `
		SELECT count(*) FILTER (WHERE message_id = $1), count(*) FILTER (WHERE message_id = $2)
		FROM bot_history_message_assets WHERE message_id IN ($1, $2)`, cloned.ID, f.user.ID).Scan(&copiedLinks, &originalLinks); err != nil {
		t.Fatal(err)
	}
	if copiedLinks != 1 || originalLinks != 1 {
		t.Fatalf("attachment links clone/original = %d/%d, want 1/1", copiedLinks, originalLinks)
	}

	// Later steps use the copied request directly. Reverse their timestamps to
	// ensure replay order comes from turn_message_seq rather than wall time.
	second, err := f.service.PersistAgentStep(f.owner, AgentStep{RunID: f.runID, Messages: []PersistInput{
		f.input("tool", `{"role":"tool","content":"tool result"}`, cloned.ID),
		f.input("assistant", `{"role":"assistant","content":"second step"}`, cloned.ID),
	}})
	if err != nil || len(second) != 2 {
		t.Fatalf("append ordinary replacement step = (%#v, %v)", second, err)
	}
	if _, err := f.pool.Exec(f.owner, `
		UPDATE bot_history_messages SET created_at = CASE id
		WHEN $1::uuid THEN '2030-01-01'::timestamptz
		WHEN $2::uuid THEN '2020-01-01'::timestamptz
		ELSE '2010-01-01'::timestamptz END
		WHERE id IN ($1, $2, $3)`, first[1].ID, second[0].ID, second[1].ID); err != nil {
		t.Fatal(err)
	}
	assertBoundReplacementHistory(t, f, cloned.ID, first[1].ID, second[0].ID, second[1].ID)
	assertBoundReplacementRow(t, f, second[0].ID, f.replacement.ReplacementTurnID, 3, true)
	assertBoundReplacementRow(t, f, second[1].ID, f.replacement.ReplacementTurnID, 4, true)
	var unbound, runRows int
	if err := f.pool.QueryRow(f.owner, `
		SELECT count(*) FILTER (WHERE turn_id IS NULL OR turn_position IS NULL OR turn_message_seq IS NULL),
		       count(*) FILTER (WHERE run_id = $2)
		FROM bot_history_messages WHERE session_id = $1`, f.sessionID, f.runID).Scan(&unbound, &runRows); err != nil {
		t.Fatal(err)
	}
	if unbound != 0 || runRows != 4 {
		t.Fatalf("unbound/run-owned rows = %d/%d, want 0/4", unbound, runRows)
	}
}

func TestPostgresRuntimeFenceReplacementSteerDecisionContinuation(t *testing.T) {
	for _, tool := range []string{"ask_user", "exec"} {
		t.Run(tool, func(t *testing.T) {
			f := newBoundReplacementFixture(t, PersistInput{})
			first, err := f.service.PersistAgentStep(f.owner, AgentStep{
				RunID: f.runID, Replacement: &f.replacement,
				Messages: []PersistInput{f.input("assistant", `{"role":"assistant","content":"started"}`, f.user.ID)},
			})
			if err != nil || len(first) != 2 {
				t.Fatalf("first replacement step = (%#v, %v)", first, err)
			}
			slot, err := f.service.AllocateTurnSlot(f.owner, f.sessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			feedback := f.input("user", `{"role":"user","content":"media carrier"}`, first[0].ID)
			feedback.Metadata = map[string]any{MessageSourceMetadataKey: MessageSourceInternalFeedback}
			steer := f.input("user", `{"role":"user","content":"follow this direction"}`, first[0].ID)
			steer.TurnID, steer.TurnPosition = slot.TurnID, &slot.Position
			deferred, err := f.service.PersistAgentStep(f.owner, AgentStep{RunID: f.runID, Messages: []PersistInput{
				feedback, steer,
				f.input("assistant", `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"decision","toolName":"`+tool+`","input":{}}]}`, first[0].ID),
			}})
			if err != nil || len(deferred) != 3 {
				t.Fatalf("steered deferred step = (%#v, %v)", deferred, err)
			}
			assertBoundReplacementRow(t, f, deferred[0].ID, f.replacement.ReplacementTurnID, 3, true)
			assertBoundReplacementRow(t, f, deferred[1].ID, slot.TurnID, 1, true)
			assertBoundReplacementRow(t, f, deferred[2].ID, slot.TurnID, 2, true)
			requestID, err := f.service.GetRunTurnRequestMessageID(f.owner, f.sessionID.String(), f.runID, f.replacement.ReplacementTurnID)
			if err != nil || requestID != deferred[1].ID {
				t.Fatalf("continuation request = %q, %v, want steer %s", requestID, err, deferred[1].ID)
			}
			result, err := f.service.Persist(f.owner, f.input("tool", `{"role":"tool","content":[{"type":"tool-result","toolCallId":"decision","result":{"approved":true}}]}`, requestID))
			if err != nil {
				t.Fatalf("persist decision result: %v", err)
			}
			continued, err := f.service.PersistAgentStep(f.owner, AgentStep{RunID: f.runID, Messages: []PersistInput{
				f.input("assistant", `{"role":"assistant","content":"continued after decision"}`, requestID),
			}})
			if err != nil || len(continued) != 1 {
				t.Fatalf("ordinary continuation = (%#v, %v)", continued, err)
			}
			assertBoundReplacementRow(t, f, result.ID, slot.TurnID, 3, true)
			assertBoundReplacementRow(t, f, continued[0].ID, slot.TurnID, 4, true)
			assertBoundReplacementHistory(t, f, first[0].ID, first[1].ID, deferred[0].ID, deferred[1].ID, deferred[2].ID, result.ID, continued[0].ID)
		})
	}
}

func TestPostgresRuntimeFenceReplacementFirstInterruptedOrFailureStep(t *testing.T) {
	for _, edit := range []bool{false, true} {
		name := "retry_interrupted"
		if edit {
			name = "edit_empty_failure"
		}
		t.Run(name, func(t *testing.T) {
			f := newBoundReplacementFixture(t, PersistInput{})
			assistant := f.input("assistant", `{"role":"assistant","content":"partial output"}`, f.user.ID)
			assistant.Metadata = map[string]any{AgentStepInterruptedMetadataKey: true}
			inputs := []PersistInput{assistant}
			if edit {
				f.replacement.RequestMessageID, f.replacement.Reason = "", "edit"
				user := f.input("user", `{"role":"user","content":"edited request"}`, "")
				user.TurnID, user.TurnPosition = f.replacement.ReplacementTurnID, f.replacement.ReplacementTurnPosition
				assistant.Content = []byte(`{"role":"assistant","content":""}`)
				assistant.Metadata[HistoryErrorCodeMetadataKey] = "model_request_failed"
				inputs = []PersistInput{user, assistant}
			}
			persisted, err := f.service.PersistAgentStep(f.owner, AgentStep{
				RunID: f.runID, Messages: inputs, Interrupted: true, Replacement: &f.replacement,
			})
			if err != nil || len(persisted) != 2 {
				t.Fatalf("first interrupted/failure step = (%#v, %v)", persisted, err)
			}
			assertBoundReplacementHistory(t, f, persisted[0].ID, persisted[1].ID)
			assertBoundReplacementRow(t, f, f.user.ID, f.oldTurn.ID, 1, false)
			assertBoundReplacementRow(t, f, persisted[1].ID, f.replacement.ReplacementTurnID, 2, true)
			if persisted[1].Metadata[AgentStepInterruptedMetadataKey] != true ||
				(edit && persisted[1].Metadata[HistoryErrorCodeMetadataKey] != "model_request_failed") {
				t.Fatalf("failure checkpoint metadata = %#v", persisted[1].Metadata)
			}
		})
	}
}

func TestPostgresRuntimeFenceReplacementFailureRollsBackAttempt(t *testing.T) {
	for _, failure := range []string{"stale_fence", "stale_target", "invalid_step"} {
		t.Run(failure, func(t *testing.T) {
			f := newBoundReplacementFixture(t, PersistInput{})
			input := f.input("assistant", `{"role":"assistant","content":"must not survive"}`, f.user.ID)
			wantVisible := []string{f.user.ID, f.assistant.ID}
			switch failure {
			case "stale_fence":
				acquireRuntimeFenceToken(t, context.Background(), dbsqlc.New(f.pool), f.botID, f.sessionID)
			case "stale_target":
				newer, err := f.service.Persist(f.owner, f.input("user", `{"role":"user","content":"newer input"}`, ""))
				if err != nil {
					t.Fatal(err)
				}
				wantVisible = append(wantVisible, newer.ID)
			default:
				input.Content = []byte(`invalid JSON`)
			}
			var before int
			if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM bot_history_messages WHERE session_id = $1", f.sessionID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, err := f.service.PersistAgentStep(f.owner, AgentStep{RunID: f.runID, Messages: []PersistInput{input}, Replacement: &f.replacement})
			if err == nil || (failure == "stale_fence" && !errors.Is(err, runtimefence.ErrStale)) {
				t.Fatalf("failed replacement error = %v", err)
			}
			var after int
			if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM bot_history_messages WHERE session_id = $1", f.sessionID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatalf("failed replacement leaked rows: before=%d after=%d", before, after)
			}
			assertBoundReplacementHistory(t, f, wantVisible...)
			assertBoundReplacementRow(t, f, f.user.ID, f.oldTurn.ID, 1, true)
		})
	}
}

type boundReplacementFixture struct {
	pool        *pgxpool.Pool
	service     *DBService
	owner       context.Context
	botID       pgtype.UUID
	sessionID   pgtype.UUID
	runID       string
	user        Message
	assistant   Message
	oldTurn     HistoryTurn
	replacement TurnReplacement
}

func newBoundReplacementFixture(t *testing.T, request PersistInput) boundReplacementFixture {
	t.Helper()
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	f := boundReplacementFixture{pool: pool, service: NewService(nil, postgresstore.NewQueriesWithPool(pool, queries)), botID: botID, sessionID: sessionID, runID: uuid.NewString()}
	request.BotID, request.SessionID, request.Role = botID.String(), sessionID.String(), "user"
	if err := pool.QueryRow(ctx, "SELECT owner_user_id::text FROM bots WHERE id = $1", botID).Scan(&request.SenderUserID); err != nil {
		t.Fatalf("load original sender: %v", err)
	}
	if len(request.Content) == 0 {
		request.Content = []byte(`{"role":"user","content":"original request"}`)
	}
	var err error
	f.user, err = f.service.Persist(ctx, request)
	if err != nil {
		t.Fatalf("persist original user: %v", err)
	}
	f.assistant, err = f.service.Persist(ctx, PersistInput{BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant", TurnRequestMessageID: f.user.ID, Content: []byte(`{"role":"assistant","content":"original answer"}`)})
	if err != nil {
		t.Fatalf("persist original assistant: %v", err)
	}
	f.oldTurn, err = f.service.GetVisibleTurnByMessage(ctx, sessionID.String(), f.assistant.ID)
	if err != nil {
		t.Fatalf("load original turn: %v", err)
	}
	f.user, err = f.service.GetByIDBySession(ctx, sessionID.String(), f.user.ID)
	if err != nil {
		t.Fatalf("reload original request: %v", err)
	}
	slot, err := f.service.AllocateTurnSlot(ctx, sessionID.String())
	if err != nil {
		t.Fatalf("allocate replacement admission: %v", err)
	}
	f.replacement = *runtimeFenceReplacement(f.oldTurn, f.oldTurn.ID, f.user.ID, "retry")
	f.replacement.ReplacementTurnID, f.replacement.ReplacementTurnPosition = slot.TurnID, &slot.Position
	token := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	f.owner = runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: token})
	if _, err := pool.Exec(ctx, `
		INSERT INTO session_runs
		(run_id, bot_id, session_id, invocation_id, turn_id, turn_position, state,
		 input_json, input_fingerprint, owner_id, fencing_token, owner_since, live_generation)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', '{}'::jsonb, 'replacement-test', 'test-owner', $7, now(), 'test')`,
		f.runID, botID, sessionID, uuid.NewString(), f.replacement.ReplacementTurnID, *f.replacement.ReplacementTurnPosition, token); err != nil {
		t.Fatalf("create replacement run: %v", err)
	}
	return f
}

func (f boundReplacementFixture) input(role, content, requestID string) PersistInput {
	return PersistInput{BotID: f.botID.String(), SessionID: f.sessionID.String(), RunID: f.runID, Role: role, Content: []byte(content), TurnRequestMessageID: requestID}
}

func assertBoundReplacementHistory(t *testing.T, f boundReplacementFixture, want ...string) {
	t.Helper()
	rows, err := f.service.ListBySession(context.Background(), f.sessionID.String())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(rows))
	for i, row := range rows {
		got[i] = row.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible history = %v, want %v", got, want)
	}
}

func assertBoundReplacementRow(t *testing.T, f boundReplacementFixture, messageID, turnID string, seq int64, visible bool) {
	t.Helper()
	var gotTurn string
	var gotSeq int64
	var gotVisible bool
	if err := f.pool.QueryRow(context.Background(), `SELECT turn_id::text, turn_message_seq, turn_visible FROM bot_history_messages WHERE id = $1`, messageID).Scan(&gotTurn, &gotSeq, &gotVisible); err != nil {
		t.Fatal(err)
	}
	if gotTurn != turnID || gotSeq != seq || gotVisible != visible {
		t.Fatalf("row %s binding = (%s, %d, %v), want (%s, %d, %v)", messageID, gotTurn, gotSeq, gotVisible, turnID, seq, visible)
	}
}
