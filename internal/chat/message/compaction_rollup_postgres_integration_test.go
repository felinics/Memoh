package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresCompleteCompactionRollupSupersedesParentsAtomically(t *testing.T) {
	fixture := setupCompactionRollupFixture(t)
	ctx := context.Background()

	completed, err := dbsqlc.New(fixture.pool).CompleteCompactionRollup(ctx, fixture.rollupParams())
	if err != nil {
		t.Fatalf("complete compaction rollup: %v", err)
	}
	if completed.Status != "ok" {
		t.Fatalf("rollup status = %q, want ok", completed.Status)
	}
	if completed.ArtifactLevel != 1 {
		t.Fatalf("rollup artifact level = %d, want 1", completed.ArtifactLevel)
	}
	assertUUIDsEqual(t, completed.ParentIds, fixture.parentIDs)

	for _, parentID := range fixture.parentIDs {
		state := readCompactionRollupState(t, fixture.pool, parentID)
		if state.status != "ok" || state.supersededBy != fixture.target.ID || !state.supersededAt.Valid {
			t.Fatalf("parent %v state = %+v, want ok and superseded by target", parentID, state)
		}
	}
	assertMessageCompactID(t, fixture.pool, fixture.parentMessageIDs[0], fixture.parentIDs[0])
	assertMessageCompactID(t, fixture.pool, fixture.parentMessageIDs[1], fixture.parentIDs[1])
}

func TestPostgresCompleteCompactionRollupRejectsAlreadySupersededParentWithoutPartialUpdate(t *testing.T) {
	fixture := setupCompactionRollupFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	successor := fixture.createLog(t)
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET superseded_by = $2, superseded_at = now()
		WHERE id = $1
	`, fixture.parentIDs[0], successor.ID); err != nil {
		t.Fatalf("pre-supersede parent: %v", err)
	}

	if _, err := queries.CompleteCompactionRollup(ctx, fixture.rollupParams()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("complete rollup with superseded parent = %v, want pgx.ErrNoRows", err)
	}
	first := readCompactionRollupState(t, fixture.pool, fixture.parentIDs[0])
	if first.supersededBy != successor.ID || !first.supersededAt.Valid {
		t.Fatalf("pre-superseded parent state = %+v, want original successor", first)
	}
	second := readCompactionRollupState(t, fixture.pool, fixture.parentIDs[1])
	if second.status != "ok" || second.supersededBy.Valid || second.supersededAt.Valid {
		t.Fatalf("other parent was partially superseded: %+v", second)
	}
	target := readCompactionRollupState(t, fixture.pool, fixture.target.ID)
	if target.status != "pending" {
		t.Fatalf("rejected rollup target status = %q, want pending", target.status)
	}
}

func TestPostgresCompleteCompactionRollupRejectsStaleEpochWithoutSupersedingParents(t *testing.T) {
	fixture := setupCompactionRollupFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_sessions
		SET compaction_epoch = compaction_epoch + 1
		WHERE id = $1
	`, fixture.sessionID); err != nil {
		t.Fatalf("advance compaction epoch: %v", err)
	}

	if _, err := dbsqlc.New(fixture.pool).CompleteCompactionRollup(ctx, fixture.rollupParams()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("complete stale rollup = %v, want pgx.ErrNoRows", err)
	}
	for _, parentID := range fixture.parentIDs {
		state := readCompactionRollupState(t, fixture.pool, parentID)
		if state.status != "ok" || state.supersededBy.Valid || state.supersededAt.Valid {
			t.Fatalf("stale rollup superseded parent %v: %+v", parentID, state)
		}
	}
	target := readCompactionRollupState(t, fixture.pool, fixture.target.ID)
	if target.status != "pending" {
		t.Fatalf("stale rollup target status = %q, want pending", target.status)
	}
}

func TestPostgresCompleteCompactionRollupRechecksPendingTargetAfterSessionLock(t *testing.T) {
	fixture := setupCompactionRollupFixture(t)
	ctx := context.Background()

	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin target-status blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE
	`, fixture.sessionID); err != nil {
		t.Fatalf("lock owner session: %v", err)
	}

	rollupTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocked rollup: %v", err)
	}
	defer func() { _ = rollupTx.Rollback(ctx) }()
	rollupPID := postgresBackendPID(t, rollupTx)
	type rollupResult struct {
		err       error
		commitErr error
	}
	result := make(chan rollupResult, 1)
	go func() {
		_, runErr := dbsqlc.New(rollupTx).CompleteCompactionRollup(ctx, fixture.rollupParams())
		result <- rollupResult{err: runErr, commitErr: rollupTx.Commit(ctx)}
	}()
	waitForPostgresLock(t, fixture.pool, rollupPID)

	if _, err := blockerTx.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET status = 'error', completed_at = now()
		WHERE id = $1 AND status = 'pending'
	`, fixture.target.ID); err != nil {
		t.Fatalf("invalidate pending target: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release target-status blocker: %v", err)
	}

	select {
	case got := <-result:
		if !errors.Is(got.err, pgx.ErrNoRows) || got.commitErr != nil {
			t.Fatalf("rollup after target invalidation = run %v, commit %v; want pgx.ErrNoRows and committed transaction", got.err, got.commitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rollup did not resume after target invalidation")
	}
	for _, parentID := range fixture.parentIDs {
		state := readCompactionRollupState(t, fixture.pool, parentID)
		if state.status != "ok" || state.supersededBy.Valid || state.supersededAt.Valid {
			t.Fatalf("target-status race superseded parent %v: %+v", parentID, state)
		}
	}
}

type compactionRollupFixture struct {
	committedCompactionFixture
	parentIDs        []pgtype.UUID
	parentMessageIDs []pgtype.UUID
	target           dbsqlc.BotHistoryMessageCompact
	targetMessageIDs []pgtype.UUID
}

func setupCompactionRollupFixture(t *testing.T) compactionRollupFixture {
	t.Helper()
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	parentMessageIDs := fixtureMessageIDs(t, fixture)
	parentIDs := make([]pgtype.UUID, 0, len(parentMessageIDs))
	for i, messageID := range parentMessageIDs {
		parent := fixture.createLog(t)
		marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
			CompactID:          parent.ID,
			MessageIds:         []pgtype.UUID{messageID},
			ExpectedCompactIds: emptyCompactionClaims(1),
		})
		if err != nil || marked != 1 {
			t.Fatalf("claim parent %d source = %d, %v", i, marked, err)
		}
		if _, err := queries.CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{
			ID:           parent.ID,
			Status:       "ok",
			Summary:      "parent summary",
			MessageCount: 1,
			Coverage:     []byte("[]"),
		}); err != nil {
			t.Fatalf("complete parent %d: %v", i, err)
		}
		parentIDs = append(parentIDs, parent.ID)
	}

	svc := NewService(nil, postgresstore.NewQueries(queries))
	user, err := svc.Persist(ctx, PersistInput{
		BotID: fixture.botID, SessionID: fixture.sessionID, Role: "user",
		Content: []byte(`{"role":"user","content":"new question"}`),
	})
	if err != nil {
		t.Fatalf("persist rollup user message: %v", err)
	}
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID: fixture.botID, SessionID: fixture.sessionID, Role: "assistant",
		Content: []byte(`{"role":"assistant","content":"new answer"}`),
	})
	if err != nil {
		t.Fatalf("persist rollup assistant message: %v", err)
	}
	targetMessageIDs := []pgtype.UUID{mustTestUUID(t, user.ID), mustTestUUID(t, assistant.ID)}
	target := fixture.createLog(t)
	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          target.ID,
		MessageIds:         targetMessageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(targetMessageIDs)),
	})
	if err != nil || marked != int64(len(targetMessageIDs)) {
		t.Fatalf("claim rollup target sources = %d, %v", marked, err)
	}
	return compactionRollupFixture{
		committedCompactionFixture: fixture,
		parentIDs:                  parentIDs,
		parentMessageIDs:           parentMessageIDs,
		target:                     target,
		targetMessageIDs:           targetMessageIDs,
	}
}

func (fixture compactionRollupFixture) rollupParams() dbsqlc.CompleteCompactionRollupParams {
	return dbsqlc.CompleteCompactionRollupParams{
		ID:           fixture.target.ID,
		Status:       "ok",
		Summary:      "fused summary",
		MessageCount: int32(len(fixture.targetMessageIDs)), //nolint:gosec // fixed test fixture size
		Coverage:     []byte("[]"),
		Level:        1,
		Parents:      fixture.parentIDs,
	}
}

type compactionRollupState struct {
	status       string
	supersededBy pgtype.UUID
	supersededAt pgtype.Timestamptz
}

type compactionRollupQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readCompactionRollupState(t *testing.T, pool compactionRollupQueryRower, id pgtype.UUID) compactionRollupState {
	t.Helper()
	var state compactionRollupState
	if err := pool.QueryRow(context.Background(), `
		SELECT status, superseded_by, superseded_at
		FROM bot_history_message_compacts
		WHERE id = $1
	`, id).Scan(&state.status, &state.supersededBy, &state.supersededAt); err != nil {
		t.Fatalf("read compaction %v state: %v", id, err)
	}
	return state
}

func assertMessageCompactID(t *testing.T, pool compactionRollupQueryRower, messageID, want pgtype.UUID) {
	t.Helper()
	var got pgtype.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT compact_id FROM bot_history_messages WHERE id = $1
	`, messageID).Scan(&got); err != nil {
		t.Fatalf("read message %v compact id: %v", messageID, err)
	}
	if got != want {
		t.Fatalf("message %v compact id = %v, want %v", messageID, got, want)
	}
}

func assertUUIDsEqual(t *testing.T, got, want []pgtype.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("uuid list length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uuid list[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
